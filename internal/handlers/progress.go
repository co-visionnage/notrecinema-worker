package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"notrecinema/worker/internal/mailing"
	"notrecinema/worker/internal/notifications"
	"notrecinema/worker/internal/webpush"
)

type progressStalePayload struct {
	SeriesID       string `json:"seriesId"`
	UserID         string `json:"userId"`
	Title          string `json:"title"`
	CurrentSeason  int    `json:"currentSeason"`
	CurrentEpisode int    `json:"currentEpisode"`
}

// ProgressStale уведомляет одного пользователя (не всю семью) о том, что он
// давно не продолжал сериал -- см. internal/nudges (notrecinema-api),
// который находит застрявшие progress-строки и публикует это событие.
// Прямой перенос check-inactive из notrecinema-app: push и, если настроена
// почта (Resend), письмо.
func ProgressStale(logger *slog.Logger, notifier *notifications.Notifier, mail *mailing.Service) func(ctx context.Context, payload json.RawMessage) error {
	return func(ctx context.Context, payload json.RawMessage) error {
		var p progressStalePayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("progress.stale: разбор payload: %w", err)
		}

		logger.Info("обработано событие progress.stale", "series_id", p.SeriesID, "user_id", p.UserID)

		if err := notifier.NotifyUser(ctx, p.UserID, webpush.Payload{
			Title: "Давно не продолжали!",
			Body: fmt.Sprintf(
				"Вы остановились на сезоне %d, серии %d — «%s» ждёт продолжения",
				p.CurrentSeason, p.CurrentEpisode, p.Title,
			),
			URL: fmt.Sprintf("/series/%s", p.SeriesID),
		}); err != nil {
			return err
		}

		mail.NotifyUserProgressStale(ctx, p.UserID, p.SeriesID, p.Title, p.CurrentSeason, p.CurrentEpisode)
		return nil
	}
}
