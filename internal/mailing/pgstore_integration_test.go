//go:build integration

// Запуск: DATABASE_URL=... go test -tags integration ./...
package mailing_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"notrecinema/worker/internal/mailing"
	"notrecinema/worker/internal/postgres"
)

func connect(t *testing.T) (*postgres.Pool, *pgx.Conn) {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL не задан, пропускаем интеграционный тест")
	}
	ctx := context.Background()

	pool, err := postgres.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect pool: %v", err)
	}
	t.Cleanup(pool.Close)

	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })
	return pool, conn
}

func randomHex(t *testing.T, n int) string {
	t.Helper()
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(buf)
}

// registerUser создаёт пароль-аккаунт (неподтверждённый), как это делает
// регистрация в API.
func registerUser(t *testing.T, conn *pgx.Conn, name string) (id, email string) {
	t.Helper()
	email = fmt.Sprintf("mailing-test-%s@example.com", randomHex(t, 8))
	err := conn.QueryRow(context.Background(), `
		SELECT user_id FROM public.register_profile_account($1, $2, 'hash', $3, $4)
	`, email, name, randomHex(t, 16), time.Now().Add(time.Hour)).Scan(&id)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	return id, email
}

// verifiedUser создаёт аккаунт через OAuth-путь: адрес сразу подтверждён.
func verifiedUser(t *testing.T, conn *pgx.Conn, name string) (id, email string) {
	t.Helper()
	email = fmt.Sprintf("mailing-test-%s@example.com", randomHex(t, 8))
	err := conn.QueryRow(context.Background(), `
		SELECT user_id FROM public.create_profile_session($1, $2, $3, $4)
	`, email, name, randomHex(t, 16), time.Now().Add(time.Hour)).Scan(&id)
	if err != nil {
		t.Fatalf("create verified user: %v", err)
	}
	return id, email
}

func createFamily(t *testing.T, conn *pgx.Conn, owner string, members ...string) string {
	t.Helper()
	ctx := context.Background()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SELECT set_config('app.current_user_id', $1, true)", owner); err != nil {
		t.Fatalf("set owner context: %v", err)
	}
	var familyID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO public.families (name, owner_id, invite_code) VALUES ('Mailing Test', $1, $2) RETURNING id
	`, owner, randomHex(t, 4)).Scan(&familyID); err != nil {
		t.Fatalf("create family: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO public.family_members (family_id, user_id, role) VALUES ($1, $2, 'owner')`, familyID, owner); err != nil {
		t.Fatalf("add owner: %v", err)
	}
	for _, member := range members {
		if _, err := tx.Exec(ctx, "SELECT set_config('app.current_user_id', $1, true)", member); err != nil {
			t.Fatalf("set member context: %v", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO public.family_members (family_id, user_id, role) VALUES ($1, $2, 'member')`, familyID, member); err != nil {
			t.Fatalf("add member: %v", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return familyID
}

func TestUserInfoReflectsVerification(t *testing.T) {
	pool, conn := connect(t)
	store := mailing.NewPgStore(pool)
	ctx := context.Background()

	pwID, pwEmail := registerUser(t, conn, "Парольный")
	info, err := store.UserInfo(ctx, pwID)
	if err != nil {
		t.Fatalf("UserInfo() error: %v", err)
	}
	if info.Email != pwEmail || info.DisplayName != "Парольный" || info.Verified {
		t.Errorf("password account info = %+v, want unverified %q", info, pwEmail)
	}

	oauthID, _ := verifiedUser(t, conn, "GitHub")
	info, err = store.UserInfo(ctx, oauthID)
	if err != nil {
		t.Fatalf("UserInfo() error: %v", err)
	}
	if !info.Verified {
		t.Error("an OAuth account must be verified")
	}

	_, err = store.UserInfo(ctx, "00000000-0000-0000-0000-000000000000")
	if !errors.Is(err, mailing.ErrUserNotFound) {
		t.Errorf("UserInfo(unknown) error = %v, want ErrUserNotFound", err)
	}
}

// Токен, созданный воркером, должен приниматься SQL-функцией, которую
// вызывает API: это и есть договорённость о формате хэша между двумя
// независимыми Go-модулями.
func TestTokenHashFormatIsAcceptedByTheAPIFunctions(t *testing.T) {
	pool, conn := connect(t)
	store := mailing.NewPgStore(pool)
	ctx := context.Background()
	userID, _ := registerUser(t, conn, "Токен")

	raw := randomHex(t, 32)
	if err := store.CreateEmailToken(ctx, userID, "verify_email", mailing.HashToken(raw), time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("CreateEmailToken() error: %v", err)
	}

	var ok bool
	if err := conn.QueryRow(ctx, `SELECT public.verify_email_with_token($1)`, mailing.HashToken(raw)).Scan(&ok); err != nil {
		t.Fatalf("verify_email_with_token: %v", err)
	}
	if !ok {
		t.Fatal("the API-side function rejected a token hashed by the worker")
	}

	info, err := store.UserInfo(ctx, userID)
	if err != nil {
		t.Fatalf("UserInfo() error: %v", err)
	}
	if !info.Verified {
		t.Error("the account is not verified after consuming the token")
	}
}

func TestFamilyRecipientsOnlyVerifiedAndHonourExclude(t *testing.T) {
	pool, conn := connect(t)
	store := mailing.NewPgStore(pool)
	ctx := context.Background()

	ownerID, ownerEmail := verifiedUser(t, conn, "Владелец")
	memberID, memberEmail := verifiedUser(t, conn, "Участник")
	squatterID, _ := registerUser(t, conn, "Неподтверждённый")
	familyID := createFamily(t, conn, ownerID, memberID, squatterID)

	got, err := store.FamilyRecipients(ctx, familyID, nil)
	if err != nil {
		t.Fatalf("FamilyRecipients() error: %v", err)
	}
	if want := []string{memberEmail, ownerEmail}; !sameEmails(got, want) {
		t.Errorf("recipients = %v, want %v (the unverified member must be left out)", emailsOf(got), want)
	}

	got, err = store.FamilyRecipients(ctx, familyID, &ownerID)
	if err != nil {
		t.Fatalf("FamilyRecipients() error: %v", err)
	}
	if want := []string{memberEmail}; !sameEmails(got, want) {
		t.Errorf("recipients excluding owner = %v, want %v", emailsOf(got), want)
	}
}

func emailsOf(rs []mailing.Recipient) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Email)
	}
	sort.Strings(out)
	return out
}

func sameEmails(got []mailing.Recipient, want []string) bool {
	g := emailsOf(got)
	w := append([]string(nil), want...)
	sort.Strings(w)
	if len(g) != len(w) {
		return false
	}
	for i := range g {
		if g[i] != w[i] {
			return false
		}
	}
	return true
}
