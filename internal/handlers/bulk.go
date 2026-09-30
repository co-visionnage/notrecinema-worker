package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"notrecinema/worker/internal/notifications"
	"notrecinema/worker/internal/webpush"
)

type seriesBulkAddedPayload struct {
	FamilyID   string `json:"familyId"`
	UserID     string `json:"userId"`
	AddedCount int    `json:"addedCount"`
}

// SeriesBulkAdded уведомляет семью одним агрегированным push-уведомлением
// о массовом импорте (Trakt watchlist / IMDb CSV / поиск) -- публикует
// internal/series.CreateBulk (notrecinema-api). Прямой перенос
// notifyFamilyOfEvent из addSeriesBulkAction: одно уведомление на весь
// импорт, а не одно на каждый добавленный сериал.
func SeriesBulkAdded(logger *slog.Logger, notifier *notifications.Notifier) func(ctx context.Context, payload json.RawMessage) error {
	return func(ctx context.Context, payload json.RawMessage) error {
		var p seriesBulkAddedPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("series.bulk_added: разбор payload: %w", err)
		}

		logger.Info("обработано событие series.bulk_added", "family_id", p.FamilyID, "added_count", p.AddedCount)

		noun := "сериалов"
		if p.AddedCount == 1 {
			noun = "сериал"
		}

		return notifier.NotifyFamily(ctx, p.FamilyID, p.UserID, webpush.Payload{
			Title: "Новые сериалы в списке",
			Body:  fmt.Sprintf("Добавлено %d %s", p.AddedCount, noun),
			URL:   "/",
		})
	}
}
