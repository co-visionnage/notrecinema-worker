package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"notrecinema/worker/internal/mailing"
)

type userEventPayload struct {
	UserID string `json:"userId"`
}

// userEmailHandler собирает обработчик события вида {"userId": "..."} для
// одного из транзакционных писем. Ошибка отправки возвращается наверх --
// JetStream повторит доставку (см. политику в internal/mailing).
func userEmailHandler(
	logger *slog.Logger,
	eventType string,
	send func(ctx context.Context, userID string) error,
) func(ctx context.Context, payload json.RawMessage) error {
	return func(ctx context.Context, payload json.RawMessage) error {
		var p userEventPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("%s: разбор payload: %w", eventType, err)
		}
		if p.UserID == "" {
			return fmt.Errorf("%s: в payload нет userId", eventType)
		}

		logger.Info("обработано событие "+eventType, "user_id", p.UserID)
		return send(ctx, p.UserID)
	}
}

// EmailVerificationRequested -- письмо со ссылкой подтверждения email после
// регистрации (и при повторном запросе).
func EmailVerificationRequested(logger *slog.Logger, mail *mailing.Service) func(ctx context.Context, payload json.RawMessage) error {
	return userEmailHandler(logger, "email.verification_requested", mail.SendVerification)
}

// PasswordResetRequested -- письмо со ссылкой на сброс пароля.
func PasswordResetRequested(logger *slog.Logger, mail *mailing.Service) func(ctx context.Context, payload json.RawMessage) error {
	return userEmailHandler(logger, "email.password_reset_requested", mail.SendPasswordReset)
}

// PasswordChanged -- уведомление о смене пароля.
func PasswordChanged(logger *slog.Logger, mail *mailing.Service) func(ctx context.Context, payload json.RawMessage) error {
	return userEmailHandler(logger, "security.password_changed", mail.SendPasswordChanged)
}

// TwoFactorEnabled -- уведомление о включении двухфакторной аутентификации.
func TwoFactorEnabled(logger *slog.Logger, mail *mailing.Service) func(ctx context.Context, payload json.RawMessage) error {
	return userEmailHandler(logger, "security.two_factor_enabled", mail.SendTwoFactorEnabled)
}

// TwoFactorDisabled -- уведомление об отключении двухфакторной аутентификации.
func TwoFactorDisabled(logger *slog.Logger, mail *mailing.Service) func(ctx context.Context, payload json.RawMessage) error {
	return userEmailHandler(logger, "security.two_factor_disabled", mail.SendTwoFactorDisabled)
}

type accountDeletedPayload struct {
	Email         string `json:"email"`
	DisplayName   string `json:"displayName"`
	EmailVerified bool   `json:"emailVerified"`
}

// AccountDeleted -- прощальное письмо. Профиля уже нет, поэтому адрес и
// имя берутся из самого события.
func AccountDeleted(logger *slog.Logger, mail *mailing.Service) func(ctx context.Context, payload json.RawMessage) error {
	return func(ctx context.Context, payload json.RawMessage) error {
		var p accountDeletedPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("account.deleted: разбор payload: %w", err)
		}

		logger.Info("обработано событие account.deleted")
		return mail.SendAccountDeleted(ctx, p.Email, p.DisplayName, p.EmailVerified)
	}
}
