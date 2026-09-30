// Package webpush отправляет push-уведомления браузеру по протоколу Web
// Push (VAPID + шифрование aes128gcm, RFC8291) через
// github.com/SherClockHolmes/webpush-go — ту же библиотеку, что
// использует notrecinema-app (там это npm-пакет web-push, тот же
// протокол). Endpoint подписки -- это адрес push-сервиса браузера
// (FCM/Mozilla push и т.д.), сюда и уходит HTTP-запрос.
package webpush

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	gowebpush "github.com/SherClockHolmes/webpush-go"
)

type Config struct {
	PublicKey  string
	PrivateKey string
	Subject    string
}

// Configured сообщает, заданы ли VAPID-ключи -- без них отправка
// невозможна в принципе, и вызывающий код должен пропускать уведомления,
// а не падать (см. ту же логику ensureConfigured в notrecinema-app).
func (c Config) Configured() bool {
	return c.PublicKey != "" && c.PrivateKey != "" && c.Subject != ""
}

type Subscription struct {
	Endpoint string
	P256dh   string
	Auth     string
}

type Payload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url,omitempty"`
}

// ErrDeadSubscription — endpoint ответил 404/410: подписка больше не
// существует на стороне браузера (пользователь отписался, очистил
// данные и т.д.). Ожидаемая ситуация, не ошибка отправки -- вызывающий
// код не должен считать это неудачей доставки для метрик/логов уровня
// error, только debug.
var ErrDeadSubscription = errors.New("webpush: subscription no longer exists")

type Sender struct {
	cfg Config
}

func NewSender(cfg Config) *Sender {
	return &Sender{cfg: cfg}
}

func (s *Sender) Send(ctx context.Context, sub Subscription, payload Payload) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("webpush: marshal payload: %w", err)
	}

	resp, err := gowebpush.SendNotificationWithContext(ctx, data, &gowebpush.Subscription{
		Endpoint: sub.Endpoint,
		Keys: gowebpush.Keys{
			P256dh: sub.P256dh,
			Auth:   sub.Auth,
		},
	}, &gowebpush.Options{
		Subscriber:      s.cfg.Subject,
		VAPIDPublicKey:  s.cfg.PublicKey,
		VAPIDPrivateKey: s.cfg.PrivateKey,
		TTL:             60,
	})
	if err != nil {
		return fmt.Errorf("webpush: send: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return ErrDeadSubscription
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webpush: push-сервис ответил %d", resp.StatusCode)
	}
	return nil
}
