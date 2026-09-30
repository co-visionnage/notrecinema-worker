package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"notrecinema/worker/internal/notifications"
	"notrecinema/worker/internal/webpush"
)

type watchEventCreatedPayload struct {
	EventID     string    `json:"eventId"`
	FamilyID    string    `json:"familyId"`
	CreatedBy   string    `json:"createdBy"`
	Title       string    `json:"title"`
	ScheduledAt time.Time `json:"scheduledAt"`
}

func WatchEventCreated(logger *slog.Logger, notifier *notifications.Notifier) func(ctx context.Context, payload json.RawMessage) error {
	return func(ctx context.Context, payload json.RawMessage) error {
		var p watchEventCreatedPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("watch_event.created: разбор payload: %w", err)
		}

		logger.Info("обработано событие watch_event.created", "event_id", p.EventID, "family_id", p.FamilyID)

		return notifier.NotifyFamily(ctx, p.FamilyID, p.CreatedBy, webpush.Payload{
			Title: "Запланирован совместный просмотр",
			Body:  fmt.Sprintf("«%s» — %s", p.Title, p.ScheduledAt.Format("2 January, 15:04")),
		})
	}
}
