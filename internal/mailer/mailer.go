// Package mailer отправляет готовые письма. Отвечает только за доставку:
// что именно писать в письме, решают internal/emails и internal/mailing.
package mailer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"notrecinema/worker/internal/telemetry"
)

// Message -- одно письмо одному адресату. Адресаты намеренно не
// объединяются в один to: [...] -- иначе участники семьи увидели бы адреса
// друг друга.
type Message struct {
	To      string
	Subject string
	HTML    string
	Text    string
	// Headers -- дополнительные заголовки письма. Для рассылок это
	// List-Unsubscribe и List-Unsubscribe-Post (RFC 8058): почтовые клиенты
	// показывают кнопку «Отписаться» рядом с отправителем.
	Headers map[string]string
}

// Sender -- всё, что нужно остальному коду от почтового транспорта.
type Sender interface {
	Send(ctx context.Context, msg Message) error
	// Enabled возвращает false, если транспорт не настроен (нет ключа):
	// вызывающий код пропускает отправку, а не падает.
	Enabled() bool
}

// StatusError -- ответ API с неуспешным статусом. Различие 4xx/5xx нужно
// вызывающему: 5xx и 429 имеет смысл повторить, остальные 4xx (например,
// адрес отклонён) от повтора не изменятся.
type StatusError struct {
	StatusCode int
	Body       string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("resend: статус %d: %s", e.StatusCode, e.Body)
}

// Retryable сообщает, стоит ли повторять запрос.
func (e *StatusError) Retryable() bool {
	return e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= 500
}

const defaultBaseURL = "https://api.resend.com"

// Resend отправляет письма через HTTP API Resend. SDK и SMTP не нужны:
// одного POST /emails достаточно.
type Resend struct {
	apiKey     string
	from       string
	replyTo    string
	baseURL    string
	httpClient *http.Client
}

func NewResend(apiKey, from, replyTo string, httpClient *http.Client) *Resend {
	if httpClient == nil {
		httpClient = telemetry.InstrumentedClient()
		httpClient.Timeout = 15 * time.Second
	}
	return &Resend{
		apiKey:     apiKey,
		from:       from,
		replyTo:    replyTo,
		baseURL:    defaultBaseURL,
		httpClient: httpClient,
	}
}

// WithBaseURL подменяет адрес API (для тестов).
func (r *Resend) WithBaseURL(baseURL string) *Resend {
	r.baseURL = strings.TrimSuffix(baseURL, "/")
	return r
}

func (r *Resend) Enabled() bool {
	return r != nil && r.apiKey != "" && r.from != ""
}

type sendRequest struct {
	From    string            `json:"from"`
	To      []string          `json:"to"`
	Subject string            `json:"subject"`
	HTML    string            `json:"html"`
	Text    string            `json:"text"`
	ReplyTo string            `json:"reply_to,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

func (r *Resend) Send(ctx context.Context, msg Message) error {
	if !r.Enabled() {
		return fmt.Errorf("resend: транспорт не настроен")
	}

	body, err := json.Marshal(sendRequest{
		From:    r.from,
		To:      []string{msg.To},
		Subject: msg.Subject,
		HTML:    msg.HTML,
		Text:    msg.Text,
		ReplyTo: r.replyTo,
		Headers: msg.Headers,
	})
	if err != nil {
		return fmt.Errorf("resend: сборка запроса: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+"/emails", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("resend: создание запроса: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+r.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("resend: запрос: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return &StatusError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(respBody))}
}

// Disabled -- заглушка для окружений без ключа Resend.
type Disabled struct{}

func (Disabled) Send(context.Context, Message) error { return nil }
func (Disabled) Enabled() bool                       { return false }
