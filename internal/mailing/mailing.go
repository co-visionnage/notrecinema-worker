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
)

const (
	kindVerifyEmail   = "verify_email"
	kindResetPassword = "reset_password"

	verifyTTL = 24 * time.Hour
	resetTTL  = time.Hour
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
	Email       string
	DisplayName string
}

// Store -- доступ к данным, нужным для писем.
type Store interface {
	CreateEmailToken(ctx context.Context, userID, kind, tokenHash string, expiresAt time.Time) error
	UserInfo(ctx context.Context, userID string) (UserInfo, error)
	// FamilyRecipients возвращает только подтверждённые адреса участников
	// семьи, кроме excludeUserID (nil -- никого не исключать).
	FamilyRecipients(ctx context.Context, familyID string, excludeUserID *string) ([]Recipient, error)
}

type Service struct {
	sender mailer.Sender
	store  Store
	links  emails.Links
	logger *slog.Logger
	now    func() time.Time
}

func NewService(sender mailer.Sender, store Store, appURL string, logger *slog.Logger) *Service {
	return &Service{
		sender: sender,
		store:  store,
		links:  emails.NewLinks(appURL),
		logger: logger,
		now:    time.Now,
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

func (s *Service) deliver(ctx context.Context, to string, content emails.Content) error {
	rendered, err := emails.Render(content)
	if err != nil {
		return err
	}
	return s.sender.Send(ctx, mailer.Message{
		To:      to,
		Subject: rendered.Subject,
		HTML:    rendered.HTML,
		Text:    rendered.Text,
	})
}

// lookup достаёт получателя; ok == false означает «письмо слать не нужно»
// (нет пользователя либо, если requireVerified, адрес не подтверждён).
func (s *Service) lookup(ctx context.Context, userID string, requireVerified bool) (info UserInfo, ok bool, err error) {
	info, err = s.store.UserInfo(ctx, userID)
	if errors.Is(err, ErrUserNotFound) {
		s.logger.Info("mailing: пользователя нет, письмо не отправляем", "user_id", userID)
		return UserInfo{}, false, nil
	}
	if err != nil {
		return UserInfo{}, false, err
	}
	if requireVerified && !info.Verified {
		s.logger.Info("mailing: адрес не подтверждён, письмо не отправляем", "user_id", userID)
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
	info, ok, err := s.lookup(ctx, userID, false)
	if err != nil || !ok {
		return err
	}
	if info.Verified {
		return nil
	}

	token, hash, err := newToken()
	if err != nil {
		return err
	}
	if err := s.store.CreateEmailToken(ctx, userID, kindVerifyEmail, hash, s.now().Add(verifyTTL)); err != nil {
		return fmt.Errorf("mailing: сохранение токена: %w", err)
	}
	return s.deliver(ctx, info.Email, emails.VerifyEmail(info.DisplayName, s.links.VerifyEmail(token)))
}

// SendPasswordReset создаёт токен сброса и отправляет письмо. Адрес может
// быть и неподтверждённым: сброс -- как раз способ вернуть аккаунт.
func (s *Service) SendPasswordReset(ctx context.Context, userID string) error {
	if !s.Enabled() {
		return s.skipDisabled("email.password_reset_requested")
	}
	info, ok, err := s.lookup(ctx, userID, false)
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
	return s.deliver(ctx, info.Email, emails.PasswordReset(info.DisplayName, s.links.ResetPassword(token)))
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

func (s *Service) securityNotice(ctx context.Context, userID, event string, build func(UserInfo) emails.Content) error {
	if !s.Enabled() {
		return s.skipDisabled(event)
	}
	info, ok, err := s.lookup(ctx, userID, true)
	if err != nil || !ok {
		return err
	}
	return s.deliver(ctx, info.Email, build(info))
}

// SendAccountDeleted отправляет прощальное письмо. Профиля к этому моменту
// уже нет, поэтому адрес и имя приходят в самом событии. Письмо уходит
// только на подтверждённый адрес.
func (s *Service) SendAccountDeleted(ctx context.Context, email, displayName string, emailVerified bool) error {
	if !s.Enabled() {
		return s.skipDisabled("account.deleted")
	}
	if !emailVerified || email == "" {
		return nil
	}
	return s.deliver(ctx, email, emails.AccountDeleted(displayName))
}

// NotifyFamilySeriesAdded сообщает семье о новом сериале (всем, кроме того,
// кто его добавил).
func (s *Service) NotifyFamilySeriesAdded(ctx context.Context, familyID string, excludeUserID *string, seriesID, title string) {
	s.notifyFamily(ctx, familyID, excludeUserID, "movie.added", func(r Recipient) emails.Content {
		return emails.SeriesAdded(r.DisplayName, title, s.links.Series(seriesID))
	})
}

// NotifyFamilySeasonUpdated сообщает семье о новом сезоне.
func (s *Service) NotifyFamilySeasonUpdated(ctx context.Context, familyID string, excludeUserID *string, title string, totalSeasons int) {
	s.notifyFamily(ctx, familyID, excludeUserID, "season.updated", func(r Recipient) emails.Content {
		return emails.SeasonUpdated(r.DisplayName, title, totalSeasons, s.links.Home())
	})
}

func (s *Service) notifyFamily(ctx context.Context, familyID string, excludeUserID *string, event string, build func(Recipient) emails.Content) {
	if !s.Enabled() {
		return
	}
	recipients, err := s.store.FamilyRecipients(ctx, familyID, excludeUserID)
	if err != nil {
		s.logger.Error("mailing: не удалось получить адресатов семьи", "event", event, "family_id", familyID, "error", err)
		return
	}
	for _, r := range recipients {
		if err := s.deliver(ctx, r.Email, build(r)); err != nil {
			s.logger.Error("mailing: не удалось отправить письмо семье", "event", event, "family_id", familyID, "error", err)
		}
	}
}

// NotifyUserProgressStale напоминает одному человеку о заброшенном сериале.
func (s *Service) NotifyUserProgressStale(ctx context.Context, userID, seriesID, title string, season, episode int) {
	if !s.Enabled() {
		return
	}
	info, ok, err := s.lookup(ctx, userID, true)
	if err != nil {
		s.logger.Error("mailing: не удалось получить адресата", "event", "progress.stale", "user_id", userID, "error", err)
		return
	}
	if !ok {
		return
	}
	if err := s.deliver(ctx, info.Email, emails.ProgressStale(info.DisplayName, title, season, episode, s.links.Series(seriesID))); err != nil {
		s.logger.Error("mailing: не удалось отправить напоминание", "event", "progress.stale", "user_id", userID, "error", err)
	}
}

func (s *Service) skipDisabled(event string) error {
	if s != nil && s.logger != nil {
		s.logger.Warn("mailing: почта не настроена (RESEND_API_KEY), письмо пропущено", "event", event)
	}
	return nil
}
