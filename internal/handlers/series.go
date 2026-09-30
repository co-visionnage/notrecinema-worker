package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"notrecinema/worker/internal/notifications"
	"notrecinema/worker/internal/webpush"
)

type seriesAddedPayload struct {
	SeriesID string `json:"seriesId"`
	FamilyID string `json:"familyId"`
	UserID   string `json:"userId"`
	Title    string `json:"title"`
}

func MovieAdded(logger *slog.Logger, notifier *notifications.Notifier) func(ctx context.Context, payload json.RawMessage) error {
	return func(ctx context.Context, payload json.RawMessage) error {
		var p seriesAddedPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("movie.added: разбор payload: %w", err)
		}

		logger.Info("обработано событие movie.added", "series_id", p.SeriesID, "family_id", p.FamilyID)

		return notifier.NotifyFamily(ctx, p.FamilyID, p.UserID, webpush.Payload{
			Title: "Новый сериал в списке",
			Body:  fmt.Sprintf("Добавлено: «%s»", p.Title),
		})
	}
}

type seriesWatchedPayload struct {
	SeriesID string `json:"seriesId"`
	FamilyID string `json:"familyId"`
	UserID   string `json:"userId"`
	Title    string `json:"title"`
}

func MovieWatched(logger *slog.Logger, notifier *notifications.Notifier) func(ctx context.Context, payload json.RawMessage) error {
	return func(ctx context.Context, payload json.RawMessage) error {
		var p seriesWatchedPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("movie.watched: разбор payload: %w", err)
		}

		logger.Info("обработано событие movie.watched", "series_id", p.SeriesID, "family_id", p.FamilyID)

		return notifier.NotifyFamily(ctx, p.FamilyID, p.UserID, webpush.Payload{
			Title: "Отмечено как просмотренное",
			Body:  fmt.Sprintf("«%s» теперь в просмотренных", p.Title),
		})
	}
}

type seriesRatedPayload struct {
	SeriesID string `json:"seriesId"`
	FamilyID string `json:"familyId"`
	UserID   string `json:"userId"`
	Title    string `json:"title"`
	Rating   int    `json:"rating"`
}

func MovieRated(logger *slog.Logger, notifier *notifications.Notifier) func(ctx context.Context, payload json.RawMessage) error {
	return func(ctx context.Context, payload json.RawMessage) error {
		var p seriesRatedPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("movie.rated: разбор payload: %w", err)
		}

		logger.Info("обработано событие movie.rated", "series_id", p.SeriesID, "family_id", p.FamilyID, "rating", p.Rating)

		return notifier.NotifyFamily(ctx, p.FamilyID, p.UserID, webpush.Payload{
			Title: "Новая оценка",
			Body:  fmt.Sprintf("«%s» оценили на %d/5", p.Title, p.Rating),
		})
	}
}
