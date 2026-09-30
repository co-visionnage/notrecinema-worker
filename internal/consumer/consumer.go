// Package consumer подписывается на NATS JetStream и раздаёт события
// зарегистрированным обработчикам по типу события. Отвечает за три вещи,
// ради которых вообще существует отдельный воркер: идемпотентность
// (обработка одного и того же событии дважды не должна давать двойной
// эффект), ретраи с backoff (JetStream redelivery) и dead letter
// (MaxDeliver -- после стольки попыток сообщение перестаёт
// передоставляться и остаётся видно как "застрявшее" через consumer info).
package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"notrecinema/worker/internal/postgres"
	"notrecinema/worker/internal/telemetry"
)

var tracer = otel.Tracer("notrecinema-worker/consumer")

// Envelope — формат сообщения, который публикует
// notrecinema-api/internal/outbox.Poller. Структура намеренно продублирована
// (а не вынесена в общий модуль): notrecinema-api и notrecinema-worker —
// независимые Go-модули на время, пока не появятся отдельные репозитории,
// и это единственный договор между ними, зафиксированный явно в обоих
// местах, а не спрятанный за общей зависимостью.
type Envelope struct {
	EventID   string          `json:"eventId"`
	EventType string          `json:"eventType"`
	Payload   json.RawMessage `json:"payload"`
}

// Handler обрабатывает один тип события. Возврат ошибки -- сигнал
// consumer'у не подтверждать (Nak) сообщение, чтобы JetStream передоставил
// его позже согласно backoff-политике консьюмера.
type Handler func(ctx context.Context, payload json.RawMessage) error

// DeadLetterPublisher публикует сообщение, окончательно исчерпавшее
// MaxDeliver попыток, в отдельный стрим для мониторинга. nil -- допустимое
// значение (например в тестах): тогда такие сообщения просто Term'ятся без
// публикации, как и раньше.
type DeadLetterPublisher interface {
	Publish(ctx context.Context, eventType string, data []byte) error
}

type Consumer struct {
	js         jetstream.JetStream
	db         *postgres.Pool
	logger     *slog.Logger
	metrics    *telemetry.Metrics
	maxDeliver int
	handlers   map[string]Handler
	deadLetter DeadLetterPublisher
}

func New(js jetstream.JetStream, db *postgres.Pool, logger *slog.Logger, metrics *telemetry.Metrics, maxDeliver int) *Consumer {
	return &Consumer{
		js:         js,
		db:         db,
		logger:     logger,
		metrics:    metrics,
		maxDeliver: maxDeliver,
		handlers:   make(map[string]Handler),
	}
}

// WithDeadLetterPublisher включает публикацию в dead-letter стрим для
// сообщений, у которых эта попытка обработки -- последняя перед тем, как
// JetStream перестанет их передоставлять. Возвращает тот же *Consumer для
// чейнинга в main.go.
func (c *Consumer) WithDeadLetterPublisher(p DeadLetterPublisher) *Consumer {
	c.deadLetter = p
	return c
}

// Handle регистрирует обработчик для eventType (например
// "family.member.joined"). Вызывать до Run.
func (c *Consumer) Handle(eventType string, handler Handler) {
	c.handlers[eventType] = handler
}

// Run создаёт (идемпотентно) durable-консьюмер на стриме NOTRECINEMA и
// блокирует вызывающего, раздавая сообщения обработчикам, пока не
// отменят ctx.
func (c *Consumer) Run(ctx context.Context, streamName, durableName string) error {
	stream, err := c.waitForStream(ctx, streamName)
	if err != nil {
		return err
	}

	cons, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Durable:       durableName,
		AckPolicy:     jetstream.AckExplicitPolicy,
		MaxDeliver:    c.maxDeliver,
		FilterSubject: "notrecinema.>",
		// Экспоненциальный backoff между попытками редоставки одного и
		// того же сообщения -- без него JetStream ретраит по одному
		// фиксированному AckWait, что для транзиентных сбоев (сеть,
		// временная недоступность БД) хуже, чем растущая пауза.
		BackOff: []time.Duration{
			1 * time.Second,
			5 * time.Second,
			15 * time.Second,
			30 * time.Second,
		},
	})
	if err != nil {
		return fmt.Errorf("consumer: создать consumer %s: %w", durableName, err)
	}

	consumeCtx, err := cons.Consume(func(msg jetstream.Msg) {
		c.handleMessage(ctx, msg)
	})
	if err != nil {
		return fmt.Errorf("consumer: запустить consume: %w", err)
	}
	defer consumeCtx.Stop()

	<-ctx.Done()
	return nil
}

// waitForStream ждёt появления стрима, который создаёт notrecinema-api при
// своём старте. Без ретрая тут был бы race при `docker compose up`: если
// воркер стартует раньше API, единственная попытка Stream() упадёт и
// процесс больше никогда не поднимется (только внешним рестартом
// контейнера).
func (c *Consumer) waitForStream(ctx context.Context, streamName string) (jetstream.Stream, error) {
	const maxAttempts = 30
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		stream, err := c.js.Stream(ctx, streamName)
		if err == nil {
			return stream, nil
		}
		lastErr = err

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}

	return nil, fmt.Errorf("consumer: стрим %s не появился за %d попыток: %w", streamName, maxAttempts, lastErr)
}

func (c *Consumer) handleMessage(ctx context.Context, msg jetstream.Msg) {
	var envelope Envelope
	if err := json.Unmarshal(msg.Data(), &envelope); err != nil {
		// Битое сообщение ретраить бессмысленно -- Term вместо Nak, чтобы
		// не жечь MaxDeliver попытки на то, что никогда не распарсится.
		c.logger.Error("consumer: не удалось разобрать envelope, term", "error", err)
		_ = msg.Term()
		return
	}

	// Продолжаем trace, начатый в notrecinema-api на HTTP-запросе: заголовки
	// NATS-сообщения несут тот же traceparent, что otel.eventbus.Publish
	// туда положил.
	ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(msg.Headers()))
	ctx, span := tracer.Start(ctx, "worker.process",
		trace.WithAttributes(
			attribute.String("event.id", envelope.EventID),
			attribute.String("event.type", envelope.EventType),
		),
	)
	defer span.End()

	start := time.Now()
	log := c.logger.With("event_id", envelope.EventID, "event_type", envelope.EventType)

	recordOutcome := func(outcome string) {
		if c.metrics == nil {
			return
		}
		c.metrics.JobsTotal.WithLabelValues(envelope.EventType, outcome).Inc()
		c.metrics.JobDuration.WithLabelValues(envelope.EventType).Observe(time.Since(start).Seconds())
	}

	handler, ok := c.handlers[envelope.EventType]
	if !ok {
		log.Debug("consumer: нет обработчика для типа события, ack и пропуск")
		_ = msg.Ack()
		recordOutcome("no_handler")
		return
	}

	alreadyProcessed, err := c.markProcessed(ctx, envelope.EventID)
	if err != nil {
		log.Error("consumer: не удалось проверить идемпотентность, nak", "error", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, "idempotency check failed")
		_ = msg.Nak()
		recordOutcome("error")
		return
	}
	if alreadyProcessed {
		log.Info("consumer: событие уже обработано ранее, ack без повторной обработки")
		span.SetAttributes(attribute.Bool("event.duplicate", true))
		_ = msg.Ack()
		recordOutcome("duplicate")
		return
	}

	if err := handler(ctx, envelope.Payload); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "handler failed")

		if c.isFinalAttempt(msg) {
			log.Error("consumer: обработчик вернул ошибку на последней попытке, отправляем в dead-letter", "error", err)
			c.publishDeadLetter(ctx, log, envelope, msg)
			_ = msg.Term()
			recordOutcome("dead_letter")
			return
		}

		log.Error("consumer: обработчик вернул ошибку, nak для повторной доставки", "error", err)
		_ = msg.Nak()
		recordOutcome("failure")
		return
	}

	_ = msg.Ack()
	log.Info("consumer: событие обработано")
	recordOutcome("success")
}

// isFinalAttempt сообщает, что это последняя попытка редоставки перед тем,
// как JetStream сам перестанет доставлять сообщение (NumDelivered считает
// уже произошедшую доставку, включая текущую). Ошибка чтения метаданных
// намеренно не считается финальной попыткой -- лучше лишний Nak, чем
// потерять сообщение в dead-letter по сбою, не связанному с самим событием.
func (c *Consumer) isFinalAttempt(msg jetstream.Msg) bool {
	meta, err := msg.Metadata()
	if err != nil {
		return false
	}
	return meta.NumDelivered >= uint64(c.maxDeliver)
}

// publishDeadLetter отправляет исходный envelope в dead-letter стрим для
// последующего мониторинга/ручного разбора. Публикация — best-effort: если
// сам NATS недоступен, сообщение и так корректно завершится через Term в
// вызывающем коде, просто без следа в dead-letter стриме.
func (c *Consumer) publishDeadLetter(ctx context.Context, log *slog.Logger, envelope Envelope, msg jetstream.Msg) {
	if c.deadLetter == nil {
		return
	}
	if err := c.deadLetter.Publish(ctx, envelope.EventType, msg.Data()); err != nil {
		log.Error("consumer: не удалось опубликовать в dead-letter", "error", err)
	}
}

// markProcessed атомарно вставляет event_id в worker_processed_events.
// PRIMARY KEY конфликт означает, что событие уже обрабатывалось --
// возвращаем true и обработчик не вызываем повторно. Именно это и есть
// идемпотентность: at-least-once доставка от JetStream превращается в
// effectively-once обработку здесь.
func (c *Consumer) markProcessed(ctx context.Context, eventID string) (alreadyProcessed bool, err error) {
	_, err = c.db.Exec(ctx, `
		INSERT INTO public.worker_processed_events (event_id) VALUES ($1)
	`, eventID)
	if err == nil {
		return false, nil
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
		return true, nil
	}
	return false, err
}
