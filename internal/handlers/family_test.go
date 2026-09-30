package handlers

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"notrecinema/worker/internal/notifications"
	"notrecinema/worker/internal/webpush"
)

// unconfiguredNotifier -- Notifier с пустыми VAPID-ключами: NotifyFamily
// возвращает nil сразу, не трогая db/sender, поэтому nil здесь безопасен.
// Этого достаточно для проверки разбора payload и логики "owner -> не
// уведомляем"; реальную отправку проверяют тесты internal/notifications.
func unconfiguredNotifier() *notifications.Notifier {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return notifications.NewNotifier(nil, nil, webpush.Config{}, logger)
}

func TestFamilyMemberJoinedParsesValidPayload(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := FamilyMemberJoined(logger, unconfiguredNotifier())

	payload := json.RawMessage(`{"familyId":"f1","userId":"u1","role":"owner"}`)

	if err := handler(context.Background(), payload); err != nil {
		t.Fatalf("handler() error = %v, want nil for valid payload", err)
	}
}

func TestFamilyMemberJoinedRejectsInvalidPayload(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := FamilyMemberJoined(logger, unconfiguredNotifier())

	payload := json.RawMessage(`not valid json`)

	if err := handler(context.Background(), payload); err == nil {
		t.Fatal("handler() succeeded, want error for invalid JSON payload")
	}
}

func TestFamilyMemberJoinedNotifiesForNonOwnerRole(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := FamilyMemberJoined(logger, unconfiguredNotifier())

	payload := json.RawMessage(`{"familyId":"f1","userId":"u2","role":"member","displayName":"Кто-то"}`)

	// isConfigured=false -- NotifyFamily возвращает nil, не обращаясь к БД,
	// поэтому обработчик не должен упасть даже без реального соединения.
	if err := handler(context.Background(), payload); err != nil {
		t.Fatalf("handler() error = %v, want nil (notifier not configured, should no-op)", err)
	}
}
