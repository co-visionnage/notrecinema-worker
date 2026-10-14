package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"notrecinema/worker/internal/mailing"
	"notrecinema/worker/internal/notifications"
	"notrecinema/worker/internal/webpush"
)

// moscow -- время в напоминаниях по Москве: часового пояса у пользователя
// в профиле нет (так же, как в письмах-сводках).
var moscow = time.FixedZone("MSK", 3*60*60)

type watchEventCreatedPayload struct {
	EventID     string    `json:"eventId"`
	FamilyID    string    `json:"familyId"`
	CreatedBy   string    `json:"createdBy"`
	Title       string    `json:"title"`
	ScheduledAt time.Time `json:"scheduledAt"`
}

type watchEventReminderPayload struct {
	EventID     string    `json:"eventId"`
	FamilyID    string    `json:"familyId"`
	UserID      string    `json:"userId"`
	Title       string    `json:"title"`
	ScheduledAt time.Time `json:"scheduledAt"`
}

// WatchEventReminder напоминает одному участнику семьи о скорой встрече --
// см. internal/eventreminders (notrecinema-api): он находит встречи, до
// которых остался час, и ставит по событию на каждого, кроме ответивших «нет».
// Push и, если настроена почта, письмо.
func WatchEventReminder(logger *slog.Logger, notifier *notifications.Notifier, mail *mailing.Service) func(ctx context.Context, payload json.RawMessage) error {
	return func(ctx context.Context, payload json.RawMessage) error {
		var p watchEventReminderPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("watch_event.reminder: разбор payload: %w", err)
		}
		if p.UserID == "" {
			return fmt.Errorf("watch_event.reminder: в payload нет userId")
		}

		logger.Info("обработано событие watch_event.reminder", "event_id", p.EventID, "user_id", p.UserID)

		if err := notifier.NotifyUser(ctx, p.UserID, "event_reminder", webpush.Payload{
			Title: "Скоро совместный просмотр",
			Body:  fmt.Sprintf("«%s» — начало в %s по Москве", p.Title, p.ScheduledAt.In(moscow).Format("15:04")),
		}); err != nil {
			return err
		}

		mail.NotifyUserEventReminder(ctx, p.UserID, p.Title, p.ScheduledAt)
		return nil
	}
}

func WatchEventCreated(logger *slog.Logger, notifier *notifications.Notifier) func(ctx context.Context, payload json.RawMessage) error {
	return func(ctx context.Context, payload json.RawMessage) error {
		var p watchEventCreatedPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("watch_event.created: разбор payload: %w", err)
		}

		logger.Info("обработано событие watch_event.created", "event_id", p.EventID, "family_id", p.FamilyID)

		return notifier.NotifyFamily(ctx, p.FamilyID, p.CreatedBy, "watch_event", webpush.Payload{
			Title: "Запланирован совместный просмотр",
			Body:  fmt.Sprintf("«%s» — %s", p.Title, p.ScheduledAt.Format("2 January, 15:04")),
		})
	}
}
