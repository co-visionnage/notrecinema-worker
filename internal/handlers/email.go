package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

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

// Welcome -- приветствие после подтверждения адреса (или первого входа через
// GitHub, где адрес подтверждён самим GitHub).
func Welcome(logger *slog.Logger, mail *mailing.Service) func(ctx context.Context, payload json.RawMessage) error {
	return userEmailHandler(logger, "email.welcome", mail.SendWelcome)
}

// BackupCodesRegenerated -- уведомление о перевыпуске резервных кодов 2FA.
func BackupCodesRegenerated(logger *slog.Logger, mail *mailing.Service) func(ctx context.Context, payload json.RawMessage) error {
	return userEmailHandler(logger, "security.backup_codes_regenerated", mail.SendBackupCodesRegenerated)
}

type backupCodeUsedPayload struct {
	UserID    string `json:"userId"`
	Remaining int    `json:"remaining"`
}

// BackupCodeUsed -- уведомление о входе по резервному коду: сколько кодов
// осталось (число берётся из события, а не из БД: так письмо точно
// соответствует моменту, когда код был потрачен).
func BackupCodeUsed(logger *slog.Logger, mail *mailing.Service) func(ctx context.Context, payload json.RawMessage) error {
	return func(ctx context.Context, payload json.RawMessage) error {
		var p backupCodeUsedPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("security.backup_code_used: разбор payload: %w", err)
		}
		if p.UserID == "" {
			return fmt.Errorf("security.backup_code_used: в payload нет userId")
		}

		logger.Info("обработано событие security.backup_code_used", "user_id", p.UserID, "remaining", p.Remaining)
		return mail.SendBackupCodeUsed(ctx, p.UserID, p.Remaining)
	}
}

type weeklyDigestPayload struct {
	UserID string    `json:"userId"`
	Since  time.Time `json:"since"`
	Until  time.Time `json:"until"`
}

// WeeklyDigest -- еженедельная сводка семей. Содержимое собирается здесь, в
// момент отправки: в событии только человек и границы недели.
func WeeklyDigest(logger *slog.Logger, mail *mailing.Service) func(ctx context.Context, payload json.RawMessage) error {
	return func(ctx context.Context, payload json.RawMessage) error {
		var p weeklyDigestPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("email.weekly_digest: разбор payload: %w", err)
		}
		if p.UserID == "" || p.Since.IsZero() || p.Until.IsZero() {
			return fmt.Errorf("email.weekly_digest: в payload нет userId или границ недели")
		}

		logger.Info("обработано событие email.weekly_digest", "user_id", p.UserID)
		return mail.SendWeeklyDigest(ctx, p.UserID, p.Since, p.Until)
	}
}

type invitationRequestedPayload struct {
	InvitationID string `json:"invitationId"`
}

// FamilyInvitationRequested -- письмо-приглашение в семью. Токен создаётся
// здесь, в момент отправки (в событии его нет).
func FamilyInvitationRequested(logger *slog.Logger, mail *mailing.Service) func(ctx context.Context, payload json.RawMessage) error {
	return func(ctx context.Context, payload json.RawMessage) error {
		var p invitationRequestedPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("family.invitation_requested: разбор payload: %w", err)
		}
		if p.InvitationID == "" {
			return fmt.Errorf("family.invitation_requested: в payload нет invitationId")
		}

		logger.Info("обработано событие family.invitation_requested", "invitation_id", p.InvitationID)
		return mail.SendFamilyInvitation(ctx, p.InvitationID)
	}
}
