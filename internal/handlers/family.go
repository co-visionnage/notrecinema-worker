// Package handlers содержит обработчики доменных событий. Каждый
// обработчик получает уже разобранный payload события и ничего не знает
// про JetStream, ack/nak или идемпотентность -- это ответственность
// internal/consumer, который их вызывает.
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"notrecinema/worker/internal/notifications"
	"notrecinema/worker/internal/webpush"
)

type memberJoinedPayload struct {
	FamilyID    string `json:"familyId"`
	UserID      string `json:"userId"`
	Role        string `json:"role"`
	DisplayName string `json:"displayName"`
}

// FamilyMemberJoined уведомляет остальных участников семьи о новом
// участнике. При создании семьи (role="owner") уведомлять некого --
// NotifyFamily просто не найдёт чужих подписок, это не отдельный случай.
func FamilyMemberJoined(logger *slog.Logger, notifier *notifications.Notifier) func(ctx context.Context, payload json.RawMessage) error {
	return func(ctx context.Context, payload json.RawMessage) error {
		var p memberJoinedPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("family.member.joined: разбор payload: %w", err)
		}

		logger.Info("обработано событие family.member.joined",
			"family_id", p.FamilyID, "user_id", p.UserID, "role", p.Role)

		if p.Role == "owner" {
			return nil
		}

		return notifier.NotifyFamily(ctx, p.FamilyID, p.UserID, webpush.Payload{
			Title: "Новый участник семьи",
			Body:  fmt.Sprintf("%s присоединился(-ась) к вашей семье", p.DisplayName),
		})
	}
}
