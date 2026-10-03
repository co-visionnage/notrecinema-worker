package mailing

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"notrecinema/worker/internal/emails"
	"notrecinema/worker/internal/mailer"
	"notrecinema/worker/internal/unsubscribe"
)

type fakeSender struct {
	enabled bool
	sent    []mailer.Message
	failFor map[string]error
}

func (f *fakeSender) Enabled() bool { return f.enabled }

func (f *fakeSender) Send(_ context.Context, msg mailer.Message) error {
	if err := f.failFor[msg.To]; err != nil {
		return err
	}
	f.sent = append(f.sent, msg)
	return nil
}

type storedToken struct {
	userID, kind, hash string
	expiresAt          time.Time
}

type fakeStore struct {
	users      map[string]UserInfo
	recipients []Recipient
	tokens     []storedToken
	tokenErr   error

	// emailOff -- пользователи, выключившие почту (по умолчанию включена).
	emailOff map[string]bool

	invitations      map[string]InvitationInfo
	invitationTokens map[string]storedToken
	invitationGone   map[string]bool

	digest    []emails.DigestFamily
	digestErr error

	gotFamilyID string
	gotExclude  *string
	gotCategory string
}

func (f *fakeStore) CreateEmailToken(_ context.Context, userID, kind, tokenHash string, expiresAt time.Time) error {
	if f.tokenErr != nil {
		return f.tokenErr
	}
	f.tokens = append(f.tokens, storedToken{userID, kind, tokenHash, expiresAt})
	return nil
}

func (f *fakeStore) UserInfo(_ context.Context, userID string) (UserInfo, error) {
	info, ok := f.users[userID]
	if !ok {
		return UserInfo{}, ErrUserNotFound
	}
	return info, nil
}

func (f *fakeStore) FamilyRecipients(_ context.Context, familyID string, exclude *string, category string) ([]Recipient, error) {
	f.gotFamilyID, f.gotExclude, f.gotCategory = familyID, exclude, category
	return f.recipients, nil
}

func (f *fakeStore) UserEmailEnabled(_ context.Context, userID, _ string) (bool, error) {
	return !f.emailOff[userID], nil
}

func (f *fakeStore) WeeklyDigest(_ context.Context, _ string, _, _ time.Time) ([]emails.DigestFamily, error) {
	return f.digest, f.digestErr
}

func (f *fakeStore) InvitationInfo(_ context.Context, invitationID string) (InvitationInfo, error) {
	info, ok := f.invitations[invitationID]
	if !ok {
		return InvitationInfo{}, ErrInvitationNotFound
	}
	return info, nil
}

func (f *fakeStore) CreateInvitationToken(_ context.Context, invitationID, tokenHash string, expiresAt time.Time) (bool, error) {
	if f.invitationGone[invitationID] {
		return false, nil
	}
	if f.invitationTokens == nil {
		f.invitationTokens = map[string]storedToken{}
	}
	f.invitationTokens[invitationID] = storedToken{kind: "invitation", hash: tokenHash, expiresAt: expiresAt}
	return true, nil
}

const testSecret = "mailing-test-secret"

var fixedNow = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

func newTestService(sender *fakeSender, store *fakeStore) *Service {
	svc := NewService(sender, store, "https://notrecinema.ru/", testSecret, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.now = func() time.Time { return fixedNow }
	return svc
}

var tokenInLink = regexp.MustCompile(`token=([0-9a-f]{64})`)

func extractToken(t *testing.T, msg mailer.Message) string {
	t.Helper()
	m := tokenInLink.FindStringSubmatch(msg.Text)
	if m == nil {
		t.Fatalf("no token link in the text version:\n%s", msg.Text)
	}
	return m[1]
}

func TestSendVerificationStoresHashOfTheEmailedToken(t *testing.T) {
	sender := &fakeSender{enabled: true}
	store := &fakeStore{users: map[string]UserInfo{"u1": {Email: "anna@example.com", DisplayName: "Аня"}}}
	svc := newTestService(sender, store)

	if err := svc.SendVerification(context.Background(), "u1"); err != nil {
		t.Fatalf("SendVerification() error: %v", err)
	}

	if len(sender.sent) != 1 || sender.sent[0].To != "anna@example.com" {
		t.Fatalf("sent = %+v, want one letter to anna@example.com", sender.sent)
	}
	if len(store.tokens) != 1 {
		t.Fatalf("stored tokens = %d, want 1", len(store.tokens))
	}

	token := extractToken(t, sender.sent[0])
	stored := store.tokens[0]
	if stored.hash != HashToken(token) {
		t.Error("the stored hash is not the hash of the token that was emailed")
	}
	if stored.hash == token {
		t.Error("the raw token must never be stored")
	}
	if stored.kind != "verify_email" || stored.userID != "u1" {
		t.Errorf("stored = %+v", stored)
	}
	if want := fixedNow.Add(24 * time.Hour); !stored.expiresAt.Equal(want) {
		t.Errorf("expiresAt = %v, want %v", stored.expiresAt, want)
	}
	if !strings.Contains(sender.sent[0].Text, "https://notrecinema.ru/verify-email?token=") {
		t.Errorf("link does not point to the frontend verify page:\n%s", sender.sent[0].Text)
	}
	if !strings.Contains(sender.sent[0].Text, "Привет, Аня!") {
		t.Error("the letter should greet the user by name")
	}
}

func TestEveryLetterGetsAFreshToken(t *testing.T) {
	sender := &fakeSender{enabled: true}
	store := &fakeStore{users: map[string]UserInfo{"u1": {Email: "anna@example.com"}}}
	svc := newTestService(sender, store)

	for i := 0; i < 2; i++ {
		if err := svc.SendVerification(context.Background(), "u1"); err != nil {
			t.Fatalf("SendVerification() error: %v", err)
		}
	}
	if a, b := extractToken(t, sender.sent[0]), extractToken(t, sender.sent[1]); a == b {
		t.Error("two letters carried the same token")
	}
}

func TestSendVerificationSkipsAlreadyVerified(t *testing.T) {
	sender := &fakeSender{enabled: true}
	store := &fakeStore{users: map[string]UserInfo{"u1": {Email: "anna@example.com", Verified: true}}}

	if err := newTestService(sender, store).SendVerification(context.Background(), "u1"); err != nil {
		t.Fatalf("SendVerification() error: %v", err)
	}
	if len(sender.sent) != 0 || len(store.tokens) != 0 {
		t.Errorf("an already verified address got a letter or a token: sent=%d tokens=%d", len(sender.sent), len(store.tokens))
	}
}

func TestSendVerificationForMissingUserIsNotAnError(t *testing.T) {
	sender := &fakeSender{enabled: true}
	svc := newTestService(sender, &fakeStore{users: map[string]UserInfo{}})

	if err := svc.SendVerification(context.Background(), "gone"); err != nil {
		t.Errorf("SendVerification() = %v, want nil (a retry would not help)", err)
	}
	if len(sender.sent) != 0 {
		t.Error("a letter was sent to a user that does not exist")
	}
}

func TestSendPasswordResetWorksForUnverifiedAddress(t *testing.T) {
	sender := &fakeSender{enabled: true}
	store := &fakeStore{users: map[string]UserInfo{"u1": {Email: "anna@example.com", Verified: false}}}
	svc := newTestService(sender, store)

	if err := svc.SendPasswordReset(context.Background(), "u1"); err != nil {
		t.Fatalf("SendPasswordReset() error: %v", err)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("sent = %d, want 1: reset is how an unverified owner reclaims the account", len(sender.sent))
	}
	stored := store.tokens[0]
	if stored.kind != "reset_password" {
		t.Errorf("kind = %q", stored.kind)
	}
	if want := fixedNow.Add(time.Hour); !stored.expiresAt.Equal(want) {
		t.Errorf("reset token expiresAt = %v, want %v (1 hour)", stored.expiresAt, want)
	}
	if !strings.Contains(sender.sent[0].Text, "/reset-password?token="+extractToken(t, sender.sent[0])) {
		t.Error("link does not point to the reset page")
	}
}

func TestTokenStoreFailureIsReturnedAndNothingIsSent(t *testing.T) {
	sender := &fakeSender{enabled: true}
	store := &fakeStore{
		users:    map[string]UserInfo{"u1": {Email: "anna@example.com"}},
		tokenErr: errors.New("db down"),
	}
	if err := newTestService(sender, store).SendPasswordReset(context.Background(), "u1"); err == nil {
		t.Error("a failed token insert must be returned so the event is retried")
	}
	if len(sender.sent) != 0 {
		t.Error("a letter with a link that cannot work was sent")
	}
}

func TestSendFailureIsReturnedForTransactionalLetters(t *testing.T) {
	sender := &fakeSender{enabled: true, failFor: map[string]error{"anna@example.com": errors.New("resend down")}}
	store := &fakeStore{users: map[string]UserInfo{"u1": {Email: "anna@example.com"}}}

	if err := newTestService(sender, store).SendVerification(context.Background(), "u1"); err == nil {
		t.Error("a delivery failure of a transactional letter must be returned for retry")
	}
}

func TestSecurityNoticesRequireVerifiedAddress(t *testing.T) {
	sender := &fakeSender{enabled: true}
	store := &fakeStore{users: map[string]UserInfo{
		"verified":   {Email: "ok@example.com", DisplayName: "Аня", Verified: true},
		"unverified": {Email: "squat@example.com", Verified: false},
	}}
	svc := newTestService(sender, store)
	ctx := context.Background()

	senders := map[string]func(context.Context, string) error{
		"password": svc.SendPasswordChanged,
		"2fa-on":   svc.SendTwoFactorEnabled,
		"2fa-off":  svc.SendTwoFactorDisabled,
	}
	for name, send := range senders {
		before := len(sender.sent)
		if err := send(ctx, "unverified"); err != nil {
			t.Errorf("%s: unverified: %v", name, err)
		}
		if len(sender.sent) != before {
			t.Errorf("%s: a security notice went to an unverified address", name)
		}
		if err := send(ctx, "verified"); err != nil {
			t.Errorf("%s: verified: %v", name, err)
		}
		if len(sender.sent) != before+1 || sender.sent[len(sender.sent)-1].To != "ok@example.com" {
			t.Errorf("%s: the verified user did not get exactly one notice", name)
		}
	}
}

func TestSendAccountDeletedUsesPayloadData(t *testing.T) {
	sender := &fakeSender{enabled: true}
	svc := newTestService(sender, &fakeStore{})
	ctx := context.Background()

	if err := svc.SendAccountDeleted(ctx, "anna@example.com", "Аня", true); err != nil {
		t.Fatalf("SendAccountDeleted() error: %v", err)
	}
	if len(sender.sent) != 1 || sender.sent[0].To != "anna@example.com" {
		t.Fatalf("sent = %+v", sender.sent)
	}

	if err := svc.SendAccountDeleted(ctx, "squat@example.com", "", false); err != nil {
		t.Fatalf("SendAccountDeleted() error: %v", err)
	}
	if err := svc.SendAccountDeleted(ctx, "", "", true); err != nil {
		t.Fatalf("SendAccountDeleted() error: %v", err)
	}
	if len(sender.sent) != 1 {
		t.Errorf("sent = %d, want still 1: unverified or empty addresses get no goodbye", len(sender.sent))
	}
}

func TestFamilyNotificationsGoToEachRecipientSeparately(t *testing.T) {
	sender := &fakeSender{enabled: true}
	store := &fakeStore{recipients: []Recipient{
		{UserID: "ua", Email: "a@example.com", DisplayName: "Аня"},
		{UserID: "ub", Email: "b@example.com", DisplayName: "Борис"},
	}}
	svc := newTestService(sender, store)
	actor := "actor-id"

	svc.NotifyFamilySeriesAdded(context.Background(), "fam-1", &actor, "series-9", "Во все тяжкие")

	if store.gotFamilyID != "fam-1" || store.gotExclude == nil || *store.gotExclude != actor {
		t.Errorf("lookup = (%q, %v), want (fam-1, actor excluded)", store.gotFamilyID, store.gotExclude)
	}
	if len(sender.sent) != 2 {
		t.Fatalf("sent = %d, want one letter per recipient", len(sender.sent))
	}
	for _, msg := range sender.sent {
		if strings.Contains(msg.Text, "a@example.com") && strings.Contains(msg.Text, "b@example.com") {
			t.Error("recipients must not see each other's addresses")
		}
		if !strings.Contains(msg.Subject, "Во все тяжкие") {
			t.Errorf("subject = %q", msg.Subject)
		}
		if !strings.Contains(msg.Text, "https://notrecinema.ru/series/"+url.PathEscape("series-9")) {
			t.Errorf("letter does not link to the series:\n%s", msg.Text)
		}
	}
	if !strings.Contains(sender.sent[0].Text, "Привет, Аня!") || !strings.Contains(sender.sent[1].Text, "Привет, Борис!") {
		t.Error("each letter should greet its own recipient")
	}
}

func TestFamilyNotificationsAreBestEffort(t *testing.T) {
	sender := &fakeSender{enabled: true, failFor: map[string]error{"a@example.com": errors.New("bounce")}}
	store := &fakeStore{recipients: []Recipient{{UserID: "ua", Email: "a@example.com"}, {UserID: "ub", Email: "b@example.com"}}}
	svc := newTestService(sender, store)

	svc.NotifyFamilySeasonUpdated(context.Background(), "fam-1", nil, "Шоу", 3)

	if len(sender.sent) != 1 || sender.sent[0].To != "b@example.com" {
		t.Errorf("sent = %+v, want a failure for one recipient to not stop the others", sender.sent)
	}
	if store.gotExclude != nil {
		t.Error("a cron-wide notification must not exclude anyone")
	}
	if !strings.Contains(sender.sent[0].Subject, "Шоу") || !strings.Contains(sender.sent[0].Text, "3 сезона") {
		t.Errorf("season letter = %q / %q", sender.sent[0].Subject, sender.sent[0].Text)
	}
}

func TestProgressStaleSendsOnlyToVerifiedUser(t *testing.T) {
	sender := &fakeSender{enabled: true}
	store := &fakeStore{users: map[string]UserInfo{
		"v": {Email: "v@example.com", DisplayName: "Вера", Verified: true},
		"u": {Email: "u@example.com", Verified: false},
	}}
	svc := newTestService(sender, store)

	svc.NotifyUserProgressStale(context.Background(), "u", "s1", "Шоу", 2, 7)
	if len(sender.sent) != 0 {
		t.Error("a reminder went to an unverified address")
	}

	svc.NotifyUserProgressStale(context.Background(), "v", "s1", "Шоу", 2, 7)
	if len(sender.sent) != 1 || !strings.Contains(sender.sent[0].Text, "Сезон 2, серия 7") {
		t.Errorf("sent = %+v", sender.sent)
	}
}

func TestDisabledServiceDoesNothingAndNeverFails(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{users: map[string]UserInfo{"u": {Email: "a@example.com", Verified: true}}, recipients: []Recipient{{Email: "a@example.com"}}}

	for name, svc := range map[string]*Service{
		"no key":  newTestService(&fakeSender{enabled: false}, store),
		"nil svc": nil,
	} {
		if svc.Enabled() {
			t.Errorf("%s: Enabled() = true", name)
		}
		for label, err := range map[string]error{
			"verification": svc.SendVerification(ctx, "u"),
			"reset":        svc.SendPasswordReset(ctx, "u"),
			"password":     svc.SendPasswordChanged(ctx, "u"),
			"2fa":          svc.SendTwoFactorEnabled(ctx, "u"),
			"deleted":      svc.SendAccountDeleted(ctx, "a@example.com", "", true),
		} {
			if err != nil {
				t.Errorf("%s/%s: %v, want nil so the event is consumed instead of retried forever", name, label, err)
			}
		}
		svc.NotifyFamilySeriesAdded(ctx, "f", nil, "s", "t")
		svc.NotifyFamilySeasonUpdated(ctx, "f", nil, "t", 2)
		svc.NotifyUserProgressStale(ctx, "u", "s", "t", 1, 1)
	}
	if len(store.tokens) != 0 {
		t.Error("a disabled service must not create tokens")
	}
}

func TestHashTokenMatchesAPIFormat(t *testing.T) {
	// SHA-256("abc") в hex -- тот же формат, которым notrecinema-api
	// (internal/auth.hashToken) хэширует токен перед сверкой с БД.
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got := HashToken("abc"); got != want {
		t.Errorf("HashToken(abc) = %s, want %s", got, want)
	}
}

func TestFamilyNotificationsCarryUnsubscribeAndAreScopedToTheCategory(t *testing.T) {
	sender := &fakeSender{enabled: true}
	store := &fakeStore{recipients: []Recipient{{UserID: "ua", Email: "a@example.com", DisplayName: "Аня"}}}
	svc := newTestService(sender, store)

	svc.NotifyFamilySeriesAdded(context.Background(), "fam-1", nil, "s1", "Шоу")
	svc.NotifyFamilySeasonUpdated(context.Background(), "fam-1", nil, "Шоу", 2)

	if len(sender.sent) != 2 {
		t.Fatalf("sent = %d, want 2", len(sender.sent))
	}

	// Категория запрашивается у БД: настройки пользователей учитывает SQL.
	if store.gotCategory != "season_update" {
		t.Errorf("last category = %q, want season_update", store.gotCategory)
	}

	for i, category := range []string{"series_added", "season_update"} {
		msg := sender.sent[i]
		token := unsubscribe.Sign([]byte(testSecret), "ua", category)

		if !strings.Contains(msg.Text, "https://notrecinema.ru/unsubscribe?token="+token) {
			t.Errorf("%s: the footer has no unsubscribe link for that exact category", category)
		}
		if !strings.Contains(msg.Text, "https://notrecinema.ru/settings") {
			t.Errorf("%s: the footer has no link to the settings", category)
		}
		if got := msg.Headers["List-Unsubscribe"]; got != "<https://notrecinema.ru/api/v1/unsubscribe?token="+token+">" {
			t.Errorf("%s: List-Unsubscribe = %q", category, got)
		}
		if got := msg.Headers["List-Unsubscribe-Post"]; got != "List-Unsubscribe=One-Click" {
			t.Errorf("%s: List-Unsubscribe-Post = %q", category, got)
		}
	}
}

func TestProgressReminderHonoursTheEmailSwitch(t *testing.T) {
	sender := &fakeSender{enabled: true}
	store := &fakeStore{
		users:    map[string]UserInfo{"v": {Email: "v@example.com", DisplayName: "Вера", Verified: true}},
		emailOff: map[string]bool{"v": true},
	}
	svc := newTestService(sender, store)

	svc.NotifyUserProgressStale(context.Background(), "v", "s1", "Шоу", 2, 7)
	if len(sender.sent) != 0 {
		t.Error("a reminder was sent although the user switched the email off")
	}

	store.emailOff["v"] = false
	svc.NotifyUserProgressStale(context.Background(), "v", "s1", "Шоу", 2, 7)
	if len(sender.sent) != 1 {
		t.Fatalf("sent = %d, want 1", len(sender.sent))
	}
	token := unsubscribe.Sign([]byte(testSecret), "v", "progress_reminder")
	if !strings.Contains(sender.sent[0].Text, token) {
		t.Error("the reminder has no unsubscribe link for its category")
	}
}

func TestTransactionalLettersCarryNoUnsubscribe(t *testing.T) {
	sender := &fakeSender{enabled: true}
	store := &fakeStore{users: map[string]UserInfo{"u1": {Email: "anna@example.com", DisplayName: "Аня", Verified: true}}}
	svc := newTestService(sender, store)
	ctx := context.Background()

	_ = svc.SendPasswordReset(ctx, "u1")
	_ = svc.SendPasswordChanged(ctx, "u1")
	_ = svc.SendBackupCodeUsed(ctx, "u1", 5)
	_ = svc.SendBackupCodesRegenerated(ctx, "u1")
	_ = svc.SendWelcome(ctx, "u1")

	if len(sender.sent) != 5 {
		t.Fatalf("sent = %d, want 5", len(sender.sent))
	}
	for _, msg := range sender.sent {
		if len(msg.Headers) != 0 {
			t.Errorf("%q: transactional letter has headers %v", msg.Subject, msg.Headers)
		}
		if strings.Contains(msg.Text, "Отписаться") {
			t.Errorf("%q: transactional letter offers to unsubscribe", msg.Subject)
		}
	}
}

func TestBackupCodeLetterReportsTheRemainingCount(t *testing.T) {
	sender := &fakeSender{enabled: true}
	store := &fakeStore{users: map[string]UserInfo{
		"v": {Email: "v@example.com", Verified: true},
		"u": {Email: "u@example.com", Verified: false},
	}}
	svc := newTestService(sender, store)

	if err := svc.SendBackupCodeUsed(context.Background(), "v", 3); err != nil {
		t.Fatalf("SendBackupCodeUsed() error: %v", err)
	}
	if len(sender.sent) != 1 || !strings.Contains(sender.sent[0].Text, "3") {
		t.Fatalf("sent = %+v", sender.sent)
	}
	if err := svc.SendBackupCodeUsed(context.Background(), "u", 3); err != nil || len(sender.sent) != 1 {
		t.Error("an unverified address got a security notice")
	}
}

func TestWelcomeOnlyForVerifiedAddress(t *testing.T) {
	sender := &fakeSender{enabled: true}
	store := &fakeStore{users: map[string]UserInfo{
		"v": {Email: "v@example.com", DisplayName: "Вера", Verified: true},
		"u": {Email: "u@example.com", Verified: false},
	}}
	svc := newTestService(sender, store)

	_ = svc.SendWelcome(context.Background(), "u")
	if len(sender.sent) != 0 {
		t.Error("an unverified address got the welcome letter")
	}
	_ = svc.SendWelcome(context.Background(), "v")
	if len(sender.sent) != 1 || !strings.Contains(sender.sent[0].Text, "Привет, Вера!") {
		t.Errorf("welcome = %+v", sender.sent)
	}
}

func TestFamilyInvitationStoresTheHashOfTheEmailedToken(t *testing.T) {
	sender := &fakeSender{enabled: true}
	store := &fakeStore{invitations: map[string]InvitationInfo{
		"inv-1": {Email: "friend@example.com", FamilyName: "Семья Ивановых", InviterName: "Борис"},
	}}
	svc := newTestService(sender, store)

	if err := svc.SendFamilyInvitation(context.Background(), "inv-1"); err != nil {
		t.Fatalf("SendFamilyInvitation() error: %v", err)
	}
	if len(sender.sent) != 1 || sender.sent[0].To != "friend@example.com" {
		t.Fatalf("sent = %+v", sender.sent)
	}

	m := regexp.MustCompile(`/invite\?token=([0-9a-f]{64})`).FindStringSubmatch(sender.sent[0].Text)
	if m == nil {
		t.Fatalf("no invite link in the letter:\n%s", sender.sent[0].Text)
	}
	stored := store.invitationTokens["inv-1"]
	if stored.hash != HashToken(m[1]) || stored.hash == m[1] {
		t.Error("the stored value is not the hash of the emailed token")
	}
	if want := fixedNow.Add(7 * 24 * time.Hour); !stored.expiresAt.Equal(want) {
		t.Errorf("expiresAt = %v, want %v (7 days)", stored.expiresAt, want)
	}
	if !strings.Contains(sender.sent[0].Subject, "Борис") || !strings.Contains(sender.sent[0].Text, "Семья Ивановых") {
		t.Errorf("letter does not name the inviter and the family: %q", sender.sent[0].Subject)
	}
	if len(sender.sent[0].Headers) != 0 {
		t.Error("an invitation is transactional and must not carry unsubscribe headers")
	}
}

func TestFamilyInvitationIsSkippedWhenGone(t *testing.T) {
	sender := &fakeSender{enabled: true}
	store := &fakeStore{
		invitations:    map[string]InvitationInfo{"inv-2": {Email: "x@example.com", FamilyName: "F", InviterName: "I"}},
		invitationGone: map[string]bool{"inv-2": true},
	}
	svc := newTestService(sender, store)
	ctx := context.Background()

	// Приглашения нет вовсе (отозвано).
	if err := svc.SendFamilyInvitation(ctx, "no-such"); err != nil {
		t.Errorf("SendFamilyInvitation(missing) = %v, want nil: a retry cannot help", err)
	}
	// Приняли между чтением и записью токена.
	if err := svc.SendFamilyInvitation(ctx, "inv-2"); err != nil {
		t.Errorf("SendFamilyInvitation(accepted) = %v, want nil", err)
	}
	if len(sender.sent) != 0 {
		t.Errorf("letters sent for a missing or accepted invitation: %d", len(sender.sent))
	}
}

func TestFamilyInvitationFailureIsReturnedForRetry(t *testing.T) {
	sender := &fakeSender{enabled: true, failFor: map[string]error{"friend@example.com": errors.New("resend down")}}
	store := &fakeStore{invitations: map[string]InvitationInfo{"inv-3": {Email: "friend@example.com", FamilyName: "F", InviterName: "I"}}}

	if err := newTestService(sender, store).SendFamilyInvitation(context.Background(), "inv-3"); err == nil {
		t.Error("an invitation delivery failure must be returned so the event is retried")
	}
}

func digestStore() *fakeStore {
	return &fakeStore{
		users: map[string]UserInfo{"u1": {Email: "anna@example.com", DisplayName: "Аня", Verified: true}},
		digest: []emails.DigestFamily{
			{Name: "Семья", Added: []emails.DigestItem{{Title: "Шоу", Detail: "Борис"}}},
			{Name: "Пустая семья"},
		},
	}
}

func TestSendWeeklyDigestDeliversOnlyFamiliesWithNews(t *testing.T) {
	sender := &fakeSender{enabled: true}
	svc := newTestService(sender, digestStore())

	if err := svc.SendWeeklyDigest(context.Background(), "u1", fixedNow.Add(-7*24*time.Hour), fixedNow); err != nil {
		t.Fatalf("SendWeeklyDigest() error: %v", err)
	}
	if len(sender.sent) != 1 || sender.sent[0].To != "anna@example.com" {
		t.Fatalf("sent = %+v, want one letter to anna@example.com", sender.sent)
	}
	msg := sender.sent[0]
	if !strings.Contains(msg.Text, "Шоу") || strings.Contains(msg.Text, "Пустая семья") {
		t.Errorf("the digest must list the family with news and skip the empty one:\n%s", msg.Text)
	}
	// Это обычное уведомление: в нём есть отписка в один клик.
	if msg.Headers["List-Unsubscribe-Post"] != "List-Unsubscribe=One-Click" || !strings.Contains(msg.Text, "/unsubscribe?token=") {
		t.Errorf("the digest has no unsubscribe link:\n%s", msg.Text)
	}
}

func TestSendWeeklyDigestStaysSilentWhenThereIsNothingToTell(t *testing.T) {
	store := digestStore()
	store.digest = []emails.DigestFamily{{Name: "Тихая семья"}}
	sender := &fakeSender{enabled: true}

	if err := newTestService(sender, store).SendWeeklyDigest(context.Background(), "u1", fixedNow.Add(-7*24*time.Hour), fixedNow); err != nil {
		t.Fatalf("SendWeeklyDigest() error: %v", err)
	}
	if len(sender.sent) != 0 {
		t.Errorf("sent %d letters for an empty week", len(sender.sent))
	}
}

func TestSendWeeklyDigestRespectsVerificationAndOptOut(t *testing.T) {
	cases := map[string]func(*fakeStore){
		"unverified": func(f *fakeStore) { f.users["u1"] = UserInfo{Email: "anna@example.com", Verified: false} },
		"opted out":  func(f *fakeStore) { f.emailOff = map[string]bool{"u1": true} },
		"no user":    func(f *fakeStore) { delete(f.users, "u1") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			store := digestStore()
			mutate(store)
			sender := &fakeSender{enabled: true}

			if err := newTestService(sender, store).SendWeeklyDigest(context.Background(), "u1", fixedNow.Add(-7*24*time.Hour), fixedNow); err != nil {
				t.Fatalf("SendWeeklyDigest() error: %v", err)
			}
			if len(sender.sent) != 0 {
				t.Errorf("sent %d letters, want none", len(sender.sent))
			}
		})
	}
}

func TestSendWeeklyDigestReturnsStoreAndSendErrorsSoTheEventIsRetried(t *testing.T) {
	store := digestStore()
	store.digestErr = errors.New("db down")
	if err := newTestService(&fakeSender{enabled: true}, store).SendWeeklyDigest(context.Background(), "u1", fixedNow.Add(-time.Hour), fixedNow); err == nil {
		t.Error("a store error was swallowed")
	}

	sender := &fakeSender{enabled: true, failFor: map[string]error{"anna@example.com": errors.New("resend 500")}}
	if err := newTestService(sender, digestStore()).SendWeeklyDigest(context.Background(), "u1", fixedNow.Add(-time.Hour), fixedNow); err == nil {
		t.Error("a send error was swallowed")
	}
}

func TestSendWeeklyDigestIsSkippedWhenMailIsNotConfigured(t *testing.T) {
	sender := &fakeSender{enabled: false}
	if err := newTestService(sender, digestStore()).SendWeeklyDigest(context.Background(), "u1", fixedNow.Add(-time.Hour), fixedNow); err != nil {
		t.Fatalf("SendWeeklyDigest() error: %v", err)
	}
	if len(sender.sent) != 0 {
		t.Error("a letter went out although mail is disabled")
	}
}
