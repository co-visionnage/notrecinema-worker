package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"notrecinema/worker/internal/notifications"
	"notrecinema/worker/internal/webpush"
)

type seasonUpdatedPayload struct {
	SeriesID      string  `json:"seriesId"`
	FamilyID      string  `json:"familyId"`
	Title         string  `json:"title"`
	TotalSeasons  int     `json:"totalSeasons"`
	ExcludeUserID *string `json:"excludeUserId,omitempty"`
}

// SeasonUpdated уведомляет семью о новом сезоне сериала, импортированного
// из Kinopoisk/OMDb -- публикует internal/seasons (notrecinema-api), и на
// cron-обновлении (никого не исключаем, ExcludeUserID == nil), и на
// разовой проверке по запросу пользователя (исключаем того, кто её
// запустил, как и в notrecinema-app). Текст уведомления -- дословный
// перенос: "Вышел новый сезон!" / "У «...» теперь N сезон(ов)".
func SeasonUpdated(logger *slog.Logger, notifier *notifications.Notifier) func(ctx context.Context, payload json.RawMessage) error {
	return func(ctx context.Context, payload json.RawMessage) error {
		var p seasonUpdatedPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("season.updated: разбор payload: %w", err)
		}

		logger.Info("обработано событие season.updated", "series_id", p.SeriesID, "family_id", p.FamilyID, "total_seasons", p.TotalSeasons)

		return notifier.NotifyFamilyExcept(ctx, p.FamilyID, p.ExcludeUserID, webpush.Payload{
			Title: "Вышел новый сезон!",
			Body:  fmt.Sprintf("У «%s» теперь %d сезон(ов)", p.Title, p.TotalSeasons),
			URL:   "/",
		})
	}
}
