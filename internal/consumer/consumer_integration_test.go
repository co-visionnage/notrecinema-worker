//go:build integration

package consumer

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"notrecinema/worker/internal/postgres"
	"notrecinema/worker/internal/telemetry"
)

// fakeMsg -- jetstream.Msg, который запоминает, чем на него ответили.
type fakeMsg struct {
	data         []byte
	numDelivered uint64
	acked        bool
	naked        bool
	termed       bool
}

func (m *fakeMsg) Metadata() (*jetstream.MsgMetadata, error) {
	return &jetstream.MsgMetadata{NumDelivered: m.numDelivered}, nil
}
func (m *fakeMsg) Data() []byte                     { return m.data }
func (m *fakeMsg) Headers() nats.Header             { return nats.Header{} }
func (m *fakeMsg) Subject() string                  { return "notrecinema.test" }
func (m *fakeMsg) Reply() string                    { return "" }
func (m *fakeMsg) Ack() error                       { m.acked = true; return nil }
func (m *fakeMsg) DoubleAck(context.Context) error  { m.acked = true; return nil }
func (m *fakeMsg) Nak() error                       { m.naked = true; return nil }
func (m *fakeMsg) NakWithDelay(time.Duration) error { m.naked = true; return nil }
func (m *fakeMsg) InProgress() error                { return nil }
func (m *fakeMsg) Term() error                      { m.termed = true; return nil }
func (m *fakeMsg) TermWithReason(string) error      { m.termed = true; return nil }

type fakeDeadLetter struct {
	published []string
	err       error
}

func (f *fakeDeadLetter) Publish(_ context.Context, eventType string, _ []byte) error {
	f.published = append(f.published, eventType)
	return f.err
}

func randomID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func newConsumerUnderTest(t *testing.T, maxDeliver int) (*Consumer, *prometheus.Registry, *fakeDeadLetter) {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL не задан, пропускаем интеграционный тест")
	}
	db, err := postgres.Connect(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(db.Close)

	registry := prometheus.NewRegistry()
	dead := &fakeDeadLetter{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := New(nil, db, logger, telemetry.NewMetrics(registry), maxDeliver).WithDeadLetterPublisher(dead)
	return c, registry, dead
}

func envelopeJSON(t *testing.T, eventID, eventType string, createdAt time.Time) []byte {
	t.Helper()
	data, err := json.Marshal(Envelope{EventID: eventID, EventType: eventType, Payload: json.RawMessage(`{}`), CreatedAt: createdAt})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

func counterValue(t *testing.T, registry *prometheus.Registry, name string, labels map[string]string) float64 {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			if !hasLabels(metric, labels) {
				continue
			}
			switch {
			case metric.Counter != nil:
				return metric.GetCounter().GetValue()
			case metric.Histogram != nil:
				return float64(metric.GetHistogram().GetSampleCount())
			case metric.Gauge != nil:
				return metric.GetGauge().GetValue()
			}
		}
	}
	return 0
}

func hasLabels(metric *dto.Metric, want map[string]string) bool {
	have := map[string]string{}
	for _, l := range metric.GetLabel() {
		have[l.GetName()] = l.GetValue()
	}
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

func TestSuccessfulEventIsAckedAndMeasured(t *testing.T) {
	c, registry, _ := newConsumerUnderTest(t, 5)
	handled := 0
	c.Handle("test.ok", func(context.Context, json.RawMessage) error { handled++; return nil })

	msg := &fakeMsg{data: envelopeJSON(t, randomID(t), "test.ok", time.Now().Add(-2*time.Second)), numDelivered: 1}
	c.handleMessage(context.Background(), msg)

	if !msg.acked || msg.naked || msg.termed || handled != 1 {
		t.Errorf("ack=%v nak=%v term=%v handled=%d", msg.acked, msg.naked, msg.termed, handled)
	}
	if got := counterValue(t, registry, "worker_jobs_total", map[string]string{"event_type": "test.ok", "outcome": "success"}); got != 1 {
		t.Errorf("success outcomes = %v, want 1", got)
	}
	if got := counterValue(t, registry, "worker_job_duration_seconds", map[string]string{"event_type": "test.ok"}); got != 1 {
		t.Errorf("duration observations = %v, want 1", got)
	}
	// Задержка от создания в API до начала обработки видна в метрике.
	if got := counterValue(t, registry, "worker_event_age_seconds", map[string]string{"event_type": "test.ok"}); got != 1 {
		t.Errorf("event age observations = %v, want 1", got)
	}
	if got := counterValue(t, registry, "worker_events_in_flight", nil); got != 0 {
		t.Errorf("in flight after handling = %v, want 0", got)
	}
	if got := counterValue(t, registry, "worker_event_redeliveries_total", map[string]string{"event_type": "test.ok"}); got != 0 {
		t.Errorf("redeliveries on a first delivery = %v, want 0", got)
	}
}

func TestFailedEventIsNakedAndRedeliveryIsCounted(t *testing.T) {
	c, registry, dead := newConsumerUnderTest(t, 5)
	c.Handle("test.fail", func(context.Context, json.RawMessage) error { return errors.New("boom") })

	// Вторая доставка из пяти: ещё не финальная.
	msg := &fakeMsg{data: envelopeJSON(t, randomID(t), "test.fail", time.Now()), numDelivered: 2}
	c.handleMessage(context.Background(), msg)

	if !msg.naked || msg.acked || msg.termed {
		t.Errorf("ack=%v nak=%v term=%v, want only nak", msg.acked, msg.naked, msg.termed)
	}
	if len(dead.published) != 0 {
		t.Error("a non-final failure went to the dead-letter stream")
	}
	if got := counterValue(t, registry, "worker_jobs_total", map[string]string{"event_type": "test.fail", "outcome": "failure"}); got != 1 {
		t.Errorf("failure outcomes = %v, want 1", got)
	}
	if got := counterValue(t, registry, "worker_event_redeliveries_total", map[string]string{"event_type": "test.fail"}); got != 1 {
		t.Errorf("redeliveries = %v, want 1 (NumDelivered=2 is a repeat)", got)
	}
}

func TestFinalFailureGoesToDeadLetterAndIsCounted(t *testing.T) {
	c, registry, dead := newConsumerUnderTest(t, 3)
	c.Handle("test.dead", func(context.Context, json.RawMessage) error { return errors.New("always broken") })

	msg := &fakeMsg{data: envelopeJSON(t, randomID(t), "test.dead", time.Now()), numDelivered: 3}
	c.handleMessage(context.Background(), msg)

	if !msg.termed || msg.naked || msg.acked {
		t.Errorf("ack=%v nak=%v term=%v, want only term", msg.acked, msg.naked, msg.termed)
	}
	if len(dead.published) != 1 || dead.published[0] != "test.dead" {
		t.Errorf("dead-letter publishes = %v", dead.published)
	}
	if got := counterValue(t, registry, "worker_jobs_total", map[string]string{"event_type": "test.dead", "outcome": "dead_letter"}); got != 1 {
		t.Errorf("dead_letter outcomes = %v, want 1", got)
	}
	if got := counterValue(t, registry, "worker_dead_letter_published_total", map[string]string{"event_type": "test.dead", "result": "ok"}); got != 1 {
		t.Errorf("dead-letter publications = %v, want 1", got)
	}
}

func TestDeadLetterPublishFailureIsCountedSeparately(t *testing.T) {
	c, registry, dead := newConsumerUnderTest(t, 1)
	dead.err = errors.New("nats down")
	c.Handle("test.dead2", func(context.Context, json.RawMessage) error { return errors.New("broken") })

	msg := &fakeMsg{data: envelopeJSON(t, randomID(t), "test.dead2", time.Now()), numDelivered: 1}
	c.handleMessage(context.Background(), msg)

	if !msg.termed {
		t.Error("the message must still be terminated when the dead-letter publish fails")
	}
	if got := counterValue(t, registry, "worker_dead_letter_published_total", map[string]string{"event_type": "test.dead2", "result": "error"}); got != 1 {
		t.Errorf("failed dead-letter publications = %v, want 1", got)
	}
}

func TestDuplicateEventIsAckedWithoutRunningTheHandler(t *testing.T) {
	c, registry, _ := newConsumerUnderTest(t, 5)
	handled := 0
	c.Handle("test.dup", func(context.Context, json.RawMessage) error { handled++; return nil })

	eventID := randomID(t)
	first := &fakeMsg{data: envelopeJSON(t, eventID, "test.dup", time.Now()), numDelivered: 1}
	second := &fakeMsg{data: envelopeJSON(t, eventID, "test.dup", time.Now()), numDelivered: 2}
	c.handleMessage(context.Background(), first)
	c.handleMessage(context.Background(), second)

	if handled != 1 {
		t.Errorf("handler ran %d times for one event id, want 1", handled)
	}
	if !second.acked {
		t.Error("the duplicate was not acked")
	}
	if got := counterValue(t, registry, "worker_jobs_total", map[string]string{"event_type": "test.dup", "outcome": "duplicate"}); got != 1 {
		t.Errorf("duplicate outcomes = %v, want 1", got)
	}
}

func TestUnknownAndBrokenMessages(t *testing.T) {
	c, registry, _ := newConsumerUnderTest(t, 5)

	unknown := &fakeMsg{data: envelopeJSON(t, randomID(t), "test.nobody", time.Now()), numDelivered: 1}
	c.handleMessage(context.Background(), unknown)
	if !unknown.acked {
		t.Error("an event without a handler must be acked (nothing will ever handle it)")
	}
	if got := counterValue(t, registry, "worker_jobs_total", map[string]string{"event_type": "test.nobody", "outcome": "no_handler"}); got != 1 {
		t.Errorf("no_handler outcomes = %v, want 1", got)
	}

	broken := &fakeMsg{data: []byte("{not json"), numDelivered: 1}
	c.handleMessage(context.Background(), broken)
	if !broken.termed || broken.naked {
		t.Errorf("a malformed envelope: term=%v nak=%v, want term only (retrying cannot help)", broken.termed, broken.naked)
	}
}
