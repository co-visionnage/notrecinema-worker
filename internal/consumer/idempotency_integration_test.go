//go:build integration

// Интеграционному тесту нужен настоящий Postgres по адресу $DATABASE_URL с
// уже применённой схемой notrecinema-app (таблица worker_processed_events,
// см. ../../notrecinema-schema/migrations/0027_worker_processed_events.sql).
// Запуск: go test -tags integration ./...
package consumer

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"

	"notrecinema/worker/internal/postgres"
)

func TestMarkProcessedIsIdempotent(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL не задан, пропускаем интеграционный тест")
	}

	ctx := context.Background()
	db, err := postgres.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("не удалось подключиться: %v", err)
	}
	defer db.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := New(nil, db, logger, nil, 5)

	var randomBytes [16]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		t.Fatalf("не удалось сгенерировать event_id: %v", err)
	}
	eventID := fmt.Sprintf("%x-%x-%x-%x-%x",
		randomBytes[0:4], randomBytes[4:6], randomBytes[6:8], randomBytes[8:10], randomBytes[10:16])

	firstAlready, err := c.markProcessed(ctx, eventID)
	if err != nil {
		t.Fatalf("markProcessed() (первый вызов) error = %v", err)
	}
	if firstAlready {
		t.Fatal("markProcessed() (первый вызов) = true, want false -- событие ещё не обрабатывалось")
	}

	secondAlready, err := c.markProcessed(ctx, eventID)
	if err != nil {
		t.Fatalf("markProcessed() (повторный вызов) error = %v", err)
	}
	if !secondAlready {
		t.Fatal("markProcessed() (повторный вызов) = false, want true -- это и есть идемпотентность")
	}
}
