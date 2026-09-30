package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"notrecinema/worker/internal/notifications"
	"notrecinema/worker/internal/webpush"
)

type pollCreatedPayload struct {
	PollID    string `json:"pollId"`
	FamilyID  string `json:"familyId"`
	CreatedBy string `json:"createdBy"`
	Title     string `json:"title"`
}

func PollCreated(logger *slog.Logger, notifier *notifications.Notifier) func(ctx context.Context, payload json.RawMessage) error {
	return func(ctx context.Context, payload json.RawMessage) error {
		var p pollCreatedPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("poll.created: разбор payload: %w", err)
		}

		logger.Info("обработано событие poll.created", "poll_id", p.PollID, "family_id", p.FamilyID)

		return notifier.NotifyFamily(ctx, p.FamilyID, p.CreatedBy, webpush.Payload{
			Title: "Новый опрос",
			Body:  fmt.Sprintf("«%s» — проголосуйте за то, что смотрим", p.Title),
		})
	}
}

type pollClosedPayload struct {
	PollID   string `json:"pollId"`
	FamilyID string `json:"familyId"`
	ClosedBy string `json:"closedBy"`
	Title    string `json:"title"`
}

func PollClosed(logger *slog.Logger, notifier *notifications.Notifier) func(ctx context.Context, payload json.RawMessage) error {
	return func(ctx context.Context, payload json.RawMessage) error {
		var p pollClosedPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("poll.closed: разбор payload: %w", err)
		}

		logger.Info("обработано событие poll.closed", "poll_id", p.PollID, "family_id", p.FamilyID)

		return notifier.NotifyFamily(ctx, p.FamilyID, p.ClosedBy, webpush.Payload{
			Title: "Опрос закрыт",
			Body:  fmt.Sprintf("«%s» — результаты голосования готовы", p.Title),
		})
	}
}
