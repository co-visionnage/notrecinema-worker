// Package mailing связывает доменные события с письмами: решает, кому,
// что и когда отправлять. Сами тексты и вид писем -- в internal/emails,
// доставка -- в internal/mailer, а работа с БД спрятана за Store, чтобы
// логику можно было проверять без Postgres.
//
// Письма делятся на два рода с разной политикой ошибок:
//
//   - транзакционные (подтверждение email, сброс пароля, уведомления о
//     безопасности, удаление аккаунта) -- человек их ждёт, поэтому ошибка
//     отправки возвращается наверх, и JetStream повторяет доставку события;
//   - уведомления о семье (новый сериал, новый сезон, напоминание) --
//     best-effort, как и в notrecinema-app: сбой почты не должен вызывать
//     повторную доставку события, иначе push, отправленный тем же
//     обработчиком, ушёл бы повторно.
package mailing

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"notrecinema/worker/internal/emails"
	"notrecinema/worker/internal/mailer"
	"notrecinema/worker/internal/telemetry"
	"notrecinema/worker/internal/unsubscribe"
)

const (
	kindVerifyEmail   = "verify_email"
	kindResetPassword = "reset_password"

	verifyTTL     = 24 * time.Hour
	resetTTL      = time.Hour
	invitationTTL = 7 * 24 * time.Hour

	// Категории уведомлений: те же ключи, что в notification_preferences
	// (миграция 0037) и в настройках пользователя.
	categorySeriesAdded      = "series_added"
	categorySeasonUpdate     = "season_update"
	categoryProgressReminder = "progress_reminder"
	categoryWeeklyDigest     = "weekly_digest"
)

// ErrUserNotFound -- пользователя уже нет (например, удалил аккаунт, пока
// событие лежало в очереди). Для писем это не ошибка: слать некому.
var ErrUserNotFound = errors.New("mailing: пользователь не найден")

// UserInfo -- то, что нужно знать о получателе.
type UserInfo struct {
	Email       string
	DisplayName string
	Verified    bool
}

// Recipient -- адресат письма семье.
type Recipient struct {
	UserID      string
	Email       string
	DisplayName string
}

// InvitationInfo -- данные для письма-приглашения.
type InvitationInfo struct {
	Email       string
	FamilyName  string
	InviterName string
}

// ErrInvitationNotFound -- приглашения уже нет (принято или отозвано, пока
// событие лежало в очереди): письмо слать не нужно.
var ErrInvitationNotFound = errors.New("mailing: приглашение не найдено")

// Store -- доступ к данным, нужным для писем.
type Store interface {
	CreateEmailToken(ctx context.Context, userID, kind, tokenHash string, expiresAt time.Time) error
	UserInfo(ctx context.Context, userID string) (UserInfo, error)
	// UserEmailEnabled -- включена ли у пользователя почта для категории.
	UserEmailEnabled(ctx context.Context, userID, category string) (bool, error)
	// FamilyRecipients возвращает только подтверждённые адреса участников
	// семьи с включённой почтой для категории, кроме excludeUserID (nil --
	// никого не исключать).
	FamilyRecipients(ctx context.Context, familyID string, excludeUserID *string, category string) ([]Recipient, error)
	InvitationInfo(ctx context.Context, invitationID string) (InvitationInfo, error)
	// CreateInvitationToken сохраняет хэш токена приглашения; false --
	// приглашение уже принято или отозвано.
	CreateInvitationToken(ctx context.Context, invitationID, tokenHash string, expiresAt time.Time) (bool, error)
	// WeeklyDigest собирает еженедельную сводку человека по всем его семьям:
	// что произошло в [since, until) и что запланировано на неделю после.
	WeeklyDigest(ctx context.Context, userID string, since, until time.Time) ([]emails.DigestFamily, error)
}

type Service struct {
	sender            mailer.Sender
	store             Store
	links             emails.Links
	unsubscribeSecret []byte
	logger            *slog.Logger
	now               func() time.Time
}

// unsubscribeSecret подписывает ссылки отписки в письмах-уведомлениях; тот
// же ключ должен быть у notrecinema-api, который их проверяет.
func NewService(sender mailer.Sender, store Store, appURL, unsubscribeSecret string, logger *slog.Logger) *Service {
	return &Service{
		sender:            sender,
		store:             store,
		links:             emails.NewLinks(appURL),
		unsubscribeSecret: []byte(unsubscribeSecret),
		logger:            logger,
		now:               time.Now,
	}
}

// Enabled возвращает false для nil-сервиса и для незаданного транспорта:
// обработчики можно вызывать без проверок.
func (s *Service) Enabled() bool {
	return s != nil && s.sender != nil && s.sender.Enabled()
}

func newToken() (token, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("mailing: генерация токена: %w", err)
	}
	token = hex.EncodeToString(buf)
	return token, HashToken(token), nil
}

// HashToken -- тот же SHA-256 в hex, которым notrecinema-api хэширует токен
// из ссылки перед сверкой с БД (internal/auth.hashToken): токен в БД
// хранится только в таком виде.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// deliver отрисовывает и отправляет письмо. kind -- вид письма для метрик
// (verify_email, series_added, ...): фиксированный набор из кода.
func (s *Service) deliver(ctx context.Context, kind, to string, content emails.Content, headers map[string]string) error {
	started := time.Now()

	rendered, err := emails.Render(content)
	if err != nil {
		telemetry.RecordMail(kind, started, err)
		return err
	}
	err = s.sender.Send(ctx, mailer.Message{
		To:      to,
		Subject: rendered.Subject,
		HTML:    rendered.HTML,
		Text:    rendered.Text,
		Headers: headers,
	})
	telemetry.RecordMail(kind, started, err)
	return err
}

// deliverNotification отправляет письмо-уведомление, от которого можно
// отказаться: добавляет в подвал ссылки «отписаться» и «настройки», а в
// заголовки -- List-Unsubscribe с отпиской в один клик (RFC 8058), чтобы
// почтовый клиент показал кнопку рядом с отправителем.
func (s *Service) deliverNotification(ctx context.Context, userID, category, to string, content emails.Content) error {
	token := unsubscribe.Sign(s.unsubscribeSecret, userID, category)
	content.UnsubscribeURL = s.links.Unsubscribe(token)
	content.PreferencesURL = s.links.Settings()

	return s.deliver(ctx, category, to, content, map[string]string{
		"List-Unsubscribe":      "<" + s.links.UnsubscribeAPI(token) + ">",
		"List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
	})
}

// lookup достаёт получателя; ok == false означает «письмо слать не нужно»
// (нет пользователя либо, если requireVerified, адрес не подтверждён).
func (s *Service) lookup(ctx context.Context, kind, userID string, requireVerified bool) (info UserInfo, ok bool, err error) {
	info, err = s.store.UserInfo(ctx, userID)
	if errors.Is(err, ErrUserNotFound) {
		s.logger.Info("mailing: пользователя нет, письмо не отправляем", "user_id", userID)
		telemetry.RecordMailSkipped(kind, "user_not_found")
		return UserInfo{}, false, nil
	}
	if err != nil {
		return UserInfo{}, false, err
	}
	if requireVerified && !info.Verified {
		s.logger.Info("mailing: адрес не подтверждён, письмо не отправляем", "user_id", userID)
		telemetry.RecordMailSkipped(kind, "unverified")
		return UserInfo{}, false, nil
	}
	return info, true, nil
}

// SendVerification создаёт токен подтверждения и отправляет письмо. Если
// адрес уже подтверждён (например, письмо запросили дважды и первое успели
// открыть), ничего не отправляет.
func (s *Service) SendVerification(ctx context.Context, userID string) error {
	if !s.Enabled() {
		return s.skipDisabled("email.verification_requested")
	}
	info, ok, err := s.lookup(ctx, "verify_email", userID, false)
	if err != nil || !ok {
		return err
	}
	if info.Verified {
		telemetry.RecordMailSkipped("verify_email", "already_verified")
		return nil
	}

	token, hash, err := newToken()
	if err != nil {
		return err
	}
	if err := s.store.CreateEmailToken(ctx, userID, kindVerifyEmail, hash, s.now().Add(verifyTTL)); err != nil {
		return fmt.Errorf("mailing: сохранение токена: %w", err)
	}
	telemetry.RecordToken(kindVerifyEmail)
	return s.deliver(ctx, "verify_email", info.Email, emails.VerifyEmail(info.DisplayName, s.links.VerifyEmail(token)), nil)
}

// SendPasswordReset создаёт токен сброса и отправляет письмо. Адрес может
// быть и неподтверждённым: сброс -- как раз способ вернуть аккаунт.
func (s *Service) SendPasswordReset(ctx context.Context, userID string) error {
	if !s.Enabled() {
		return s.skipDisabled("email.password_reset_requested")
	}
	info, ok, err := s.lookup(ctx, "password_reset", userID, false)
	if err != nil || !ok {
		return err
	}

	token, hash, err := newToken()
	if err != nil {
		return err
	}
	if err := s.store.CreateEmailToken(ctx, userID, kindResetPassword, hash, s.now().Add(resetTTL)); err != nil {
		return fmt.Errorf("mailing: сохранение токена: %w", err)
	}
	telemetry.RecordToken(kindResetPassword)
	return s.deliver(ctx, "password_reset", info.Email, emails.PasswordReset(info.DisplayName, s.links.ResetPassword(token)), nil)
}

// SendPasswordChanged, SendTwoFactorEnabled и SendTwoFactorDisabled --
// уведомления о безопасности. Идут только на подтверждённый адрес: иначе
// тот, кто зарегистрировался на чужой email, засыпал бы его владельца.
func (s *Service) SendPasswordChanged(ctx context.Context, userID string) error {
	return s.securityNotice(ctx, userID, "security.password_changed", func(info UserInfo) emails.Content {
		return emails.PasswordChanged(info.DisplayName, s.links.ForgotPassword())
	})
}

func (s *Service) SendTwoFactorEnabled(ctx context.Context, userID string) error {
	return s.securityNotice(ctx, userID, "security.two_factor_enabled", func(info UserInfo) emails.Content {
		return emails.TwoFactorEnabled(info.DisplayName)
	})
}

func (s *Service) SendTwoFactorDisabled(ctx context.Context, userID string) error {
	return s.securityNotice(ctx, userID, "security.two_factor_disabled", func(info UserInfo) emails.Content {
		return emails.TwoFactorDisabled(info.DisplayName, s.links.ForgotPassword())
	})
}

// SendBackupCodeUsed сообщает о входе с резервным кодом и сколько их осталось.
func (s *Service) SendBackupCodeUsed(ctx context.Context, userID string, remaining int) error {
	return s.securityNotice(ctx, userID, "security.backup_code_used", func(info UserInfo) emails.Content {
		return emails.BackupCodeUsed(info.DisplayName, remaining, s.links.Settings())
	})
}

// SendBackupCodesRegenerated сообщает, что резервные коды выпущены заново.
func (s *Service) SendBackupCodesRegenerated(ctx context.Context, userID string) error {
	return s.securityNotice(ctx, userID, "security.backup_codes_regenerated", func(info UserInfo) emails.Content {
		return emails.BackupCodesRegenerated(info.DisplayName, s.links.Settings())
	})
}

// SendWelcome отправляет приветствие после подтверждения адреса. Письмо
// транзакционное, но только на подтверждённый адрес.
func (s *Service) SendWelcome(ctx context.Context, userID string) error {
	return s.securityNotice(ctx, userID, "email.welcome", func(info UserInfo) emails.Content {
		return emails.Welcome(info.DisplayName, s.links.Home())
	})
}

// SendFamilyInvitation создаёт токен приглашения и отправляет письмо. Токен
// живёт 7 дней; повторная отправка заменяет прежний. Если приглашение уже
// принято или отозвано, письмо не нужно.
func (s *Service) SendFamilyInvitation(ctx context.Context, invitationID string) error {
	if !s.Enabled() {
		return s.skipDisabled("family.invitation_requested")
	}

	info, err := s.store.InvitationInfo(ctx, invitationID)
	if errors.Is(err, ErrInvitationNotFound) {
		s.logger.Info("mailing: приглашения уже нет, письмо не отправляем", "invitation_id", invitationID)
		telemetry.RecordMailSkipped("family_invitation", "invitation_gone")
		return nil
	}
	if err != nil {
		return err
	}

	token, hash, err := newToken()
	if err != nil {
		return err
	}
	stored, err := s.store.CreateInvitationToken(ctx, invitationID, hash, s.now().Add(invitationTTL))
	if err != nil {
		return fmt.Errorf("mailing: сохранение токена приглашения: %w", err)
	}
	if !stored {
		telemetry.RecordMailSkipped("family_invitation", "invitation_gone")
		return nil
	}
	telemetry.RecordToken("invitation")

	return s.deliver(ctx, "family_invitation", info.Email, emails.FamilyInvitation(info.InviterName, info.FamilyName, s.links.Invite(token)), nil)
}

func (s *Service) securityNotice(ctx context.Context, userID, event string, build func(UserInfo) emails.Content) error {
	if !s.Enabled() {
		return s.skipDisabled(event)
	}
	info, ok, err := s.lookup(ctx, event, userID, true)
	if err != nil || !ok {
		return err
	}
	return s.deliver(ctx, event, info.Email, build(info), nil)
}

// SendAccountDeleted отправляет прощальное письмо. Профиля к этому моменту
// уже нет, поэтому адрес и имя приходят в самом событии. Письмо уходит
// только на подтверждённый адрес.
func (s *Service) SendAccountDeleted(ctx context.Context, email, displayName string, emailVerified bool) error {
	if !s.Enabled() {
		return s.skipDisabled("account.deleted")
	}
	if !emailVerified || email == "" {
		telemetry.RecordMailSkipped("account_deleted", "unverified")
		return nil
	}
	return s.deliver(ctx, "account_deleted", email, emails.AccountDeleted(displayName), nil)
}

// NotifyFamilySeriesAdded сообщает семье о новом сериале (всем, кроме того,
// кто его добавил).
func (s *Service) NotifyFamilySeriesAdded(ctx context.Context, familyID string, excludeUserID *string, seriesID, title string) {
	s.notifyFamily(ctx, familyID, excludeUserID, categorySeriesAdded, "movie.added", func(r Recipient) emails.Content {
		return emails.SeriesAdded(r.DisplayName, title, s.links.Series(seriesID))
	})
}

// NotifyFamilySeasonUpdated сообщает семье о новом сезоне.
func (s *Service) NotifyFamilySeasonUpdated(ctx context.Context, familyID string, excludeUserID *string, title string, totalSeasons int) {
	s.notifyFamily(ctx, familyID, excludeUserID, categorySeasonUpdate, "season.updated", func(r Recipient) emails.Content {
		return emails.SeasonUpdated(r.DisplayName, title, totalSeasons, s.links.Home())
	})
}

func (s *Service) notifyFamily(ctx context.Context, familyID string, excludeUserID *string, category, event string, build func(Recipient) emails.Content) {
	if !s.Enabled() {
		return
	}
	recipients, err := s.store.FamilyRecipients(ctx, familyID, excludeUserID, category)
	if err != nil {
		s.logger.Error("mailing: не удалось получить адресатов семьи", "event", event, "family_id", familyID, "error", err)
		return
	}
	for _, r := range recipients {
		if err := s.deliverNotification(ctx, r.UserID, category, r.Email, build(r)); err != nil {
			s.logger.Error("mailing: не удалось отправить письмо семье", "event", event, "family_id", familyID, "error", err)
		}
	}
}

// NotifyUserProgressStale напоминает одному человеку о заброшенном сериале.
func (s *Service) NotifyUserProgressStale(ctx context.Context, userID, seriesID, title string, season, episode int) {
	if !s.Enabled() {
		return
	}
	info, ok, err := s.lookup(ctx, categoryProgressReminder, userID, true)
	if err != nil {
		s.logger.Error("mailing: не удалось получить адресата", "event", "progress.stale", "user_id", userID, "error", err)
		return
	}
	if !ok {
		return
	}
	enabled, err := s.store.UserEmailEnabled(ctx, userID, categoryProgressReminder)
	if err != nil {
		s.logger.Error("mailing: не удалось прочитать настройки уведомлений", "event", "progress.stale", "user_id", userID, "error", err)
		return
	}
	if !enabled {
		telemetry.RecordMailSkipped(categoryProgressReminder, "opted_out")
		return
	}
	if err := s.deliverNotification(ctx, userID, categoryProgressReminder, info.Email, emails.ProgressStale(info.DisplayName, title, season, episode, s.links.Series(seriesID))); err != nil {
		s.logger.Error("mailing: не удалось отправить напоминание", "event", "progress.stale", "user_id", userID, "error", err)
	}
}

// SendWeeklyDigest отправляет еженедельную сводку. Письма нет, если адрес не
// подтверждён, человек отказался от сводки или за неделю нечего рассказать.
// Ошибка сборки или отправки возвращается: событие будет доставлено повторно.
func (s *Service) SendWeeklyDigest(ctx context.Context, userID string, since, until time.Time) error {
	if !s.Enabled() {
		return s.skipDisabled("email.weekly_digest")
	}
	info, ok, err := s.lookup(ctx, categoryWeeklyDigest, userID, true)
	if err != nil || !ok {
		return err
	}
	enabled, err := s.store.UserEmailEnabled(ctx, userID, categoryWeeklyDigest)
	if err != nil {
		return err
	}
	if !enabled {
		telemetry.RecordMailSkipped(categoryWeeklyDigest, "opted_out")
		return nil
	}

	all, err := s.store.WeeklyDigest(ctx, userID, since, until)
	if err != nil {
		return err
	}
	families := make([]emails.DigestFamily, 0, len(all))
	for _, family := range all {
		if !family.Empty() {
			families = append(families, family)
		}
	}
	if len(families) == 0 {
		telemetry.RecordMailSkipped(categoryWeeklyDigest, "empty")
		return nil
	}

	return s.deliverNotification(ctx, userID, categoryWeeklyDigest, info.Email, emails.WeeklyDigest(info.DisplayName, families, s.links.Home()))
}

func (s *Service) skipDisabled(event string) error {
	telemetry.RecordMailSkipped(event, "disabled")
	if s != nil && s.logger != nil {
		s.logger.Warn("mailing: почта не настроена (RESEND_API_KEY), письмо пропущено", "event", event)
	}
	return nil
}
