// Package eventbus подключается к тому же NATS JetStream, что и
// notrecinema-api. В отличие от API, воркер не создаёт и не настраивает
// стрим (это ответственность издателя, см.
// notrecinema-api/internal/eventbus) -- он только подключается и получает
// jetstream.JetStream для internal/consumer.
package eventbus

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"notrecinema/worker/internal/telemetry"
)

const StreamName = "NOTRECINEMA"

// DeadLetterStreamName/deadLetterSubjectPrefix -- отдельный стрим и префикс
// subject'ов, а не "notrecinema.dead-letter.*": основной консьюмер слушает
// "notrecinema.>", и сообщение, опубликованное под тем же префиксом, само
// стало бы новым событием для обработчиков. Отдельный стрим держит
// "окончательно не обработанные" сообщения доступными для реального
// мониторинга (а не только через consumer info основного стрима), пока их
// на практике не набралось достаточно, чтобы понадобился настоящий
// alerting -- см. README и docs/ROADMAP.md.
const (
	DeadLetterStreamName    = "NOTRECINEMA_DEADLETTER"
	deadLetterSubjectPrefix = "dead-letter."
)

func Connect(url string) (*nats.Conn, jetstream.JetStream, error) {
	conn, err := nats.Connect(url,
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
		nats.ConnectHandler(func(*nats.Conn) { telemetry.SetNATSConnected(true) }),
		nats.DisconnectErrHandler(func(*nats.Conn, error) { telemetry.SetNATSConnected(false) }),
		nats.ReconnectHandler(func(*nats.Conn) {
			telemetry.SetNATSConnected(true)
			telemetry.RecordNATSReconnect()
		}),
		nats.ClosedHandler(func(*nats.Conn) { telemetry.SetNATSConnected(false) }),
	)
	if err != nil {
		return nil, nil, err
	}
	telemetry.SetNATSConnected(conn.IsConnected())

	js, err := jetstream.New(conn)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}

	return conn, js, nil
}

// DeadLetterSubject добавляет к типу события dead-letter-префикс:
// "movie.added" -> "dead-letter.movie.added".
func DeadLetterSubject(eventType string) string {
	return deadLetterSubjectPrefix + eventType
}

// EnsureDeadLetterStream идемпотентно создаёт (или обновляет) стрим для
// сообщений, окончательно исчерпавших MaxDeliver попыток. В отличие от
// основного стрима NOTRECINEMA (создаётся notrecinema-api), этот создаёт
// сам воркер -- он единственный писатель в него.
func EnsureDeadLetterStream(ctx context.Context, js jetstream.JetStream) (jetstream.Stream, error) {
	stream, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      DeadLetterStreamName,
		Subjects:  []string{deadLetterSubjectPrefix + ">"},
		Retention: jetstream.LimitsPolicy,
		MaxAge:    30 * 24 * time.Hour,
		Storage:   jetstream.FileStorage,
	})
	if err != nil {
		return nil, fmt.Errorf("eventbus: create dead-letter stream: %w", err)
	}
	return stream, nil
}

// DeadLetterPublisher публикует сообщение, которое исчерпало все попытки
// обработки, в отдельный стрим -- для мониторинга и ручного разбора
// (consumer.go зовёт это перед Term вместо того, чтобы молча дать
// сообщению просто перестать передоставляться).
type DeadLetterPublisher struct {
	js jetstream.JetStream
}

func NewDeadLetterPublisher(js jetstream.JetStream) *DeadLetterPublisher {
	return &DeadLetterPublisher{js: js}
}

func (p *DeadLetterPublisher) Publish(ctx context.Context, eventType string, data []byte) error {
	_, err := p.js.Publish(ctx, DeadLetterSubject(eventType), data)
	if err != nil {
		return fmt.Errorf("eventbus: publish dead-letter for %s: %w", eventType, err)
	}
	return nil
}
