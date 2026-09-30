// Package config загружает и валидирует конфигурацию процесса из
// переменных окружения через cleanenv. Fail-fast при старте лучше, чем
// непонятный сбой в рантайме из-за отсутствующей переменной.
package config

import (
	"github.com/ilyakaznacheev/cleanenv"
)

type Config struct {
	// DatabaseURL — та же RLS-ограниченная роль app_user, что и у API:
	// воркеру для его таблицы идемпотентности (worker_processed_events)
	// расширенные права не нужны.
	DatabaseURL string `env:"DATABASE_URL" env-required:"true"`

	// NatsURL — тот же брокер JetStream, куда notrecinema-api публикует
	// события из outbox.
	NatsURL string `env:"NATS_URL" env-default:"nats://localhost:4222"`

	// MaxDeliver — сколько раз JetStream попробует доставить сообщение,
	// прежде чем перестать (после этого оно остаётся недоставленным в
	// стриме и видно через consumer info как "потерянное" -- это и есть
	// dead-letter в терминах JetStream: не отдельная очередь, а лимит
	// редоставки плюс мониторинг num_pending/num_ack_pending).
	MaxDeliver int `env:"MAX_DELIVER" env-default:"5"`

	// OtlpEndpoint -- адрес коллектора трейсов (Jaeger, слушает OTLP/HTTP).
	OtlpEndpoint string `env:"OTLP_ENDPOINT" env-default:"localhost:4318"`

	// VAPID-ключи для web push (RFC8291) -- те же самые, что настроены в
	// notrecinema-app (VAPID-пара общая на весь домен, не per-service).
	// Опциональны: без них internal/webpush.Config.Configured() вернёт
	// false и уведомления просто не отправляются, а не падают.
	VAPIDPublicKey  string `env:"VAPID_PUBLIC_KEY"`
	VAPIDPrivateKey string `env:"VAPID_PRIVATE_KEY"`
	VAPIDSubject    string `env:"VAPID_SUBJECT"`
}

func Load() (Config, error) {
	var cfg Config
	if err := cleanenv.ReadEnv(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
