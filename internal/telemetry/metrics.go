package telemetry

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics -- метрики консьюмера событий. Сквозные (письма, push, NATS,
// исходящие запросы) живут отдельно, см. global.go.
type Metrics struct {
	JobsTotal   *prometheus.CounterVec
	JobDuration *prometheus.HistogramVec

	// EventsInFlight -- сколько событий обрабатывается прямо сейчас.
	EventsInFlight prometheus.Gauge
	// EventAge -- сколько прошло от записи события в outbox до начала его
	// обработки: сквозная задержка цепочки outbox -> NATS -> воркер.
	EventAge *prometheus.HistogramVec
	// Redeliveries -- повторные доставки (NumDelivered > 1) по типу.
	Redeliveries *prometheus.CounterVec
	// DeadLetterPublished -- исход публикации в dead-letter стрим.
	DeadLetterPublished *prometheus.CounterVec

	// Состояние durable-консьюмера в JetStream (обновляется по таймеру).
	ConsumerPending     prometheus.Gauge
	ConsumerAckPending  prometheus.Gauge
	ConsumerRedelivered prometheus.Gauge
	// DeadLetterMessages -- сколько сообщений лежит в dead-letter стриме
	// (любое число больше нуля -- повод разобраться).
	DeadLetterMessages prometheus.Gauge
}

func NewMetrics(registry prometheus.Registerer) *Metrics {
	factory := promauto.With(registry)

	return &Metrics{
		JobsTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "worker_jobs_total",
			Help: "Обработанные события по типу и результату (success/failure/duplicate/dead_letter/no_handler/error).",
		}, []string{"event_type", "outcome"}),

		JobDuration: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "worker_job_duration_seconds",
			Help:    "Длительность обработки одного события обработчиком.",
			Buckets: prometheus.DefBuckets,
		}, []string{"event_type"}),

		EventsInFlight: factory.NewGauge(prometheus.GaugeOpts{
			Name: "worker_events_in_flight",
			Help: "Сколько событий обрабатывается воркером прямо сейчас.",
		}),

		EventAge: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "worker_event_age_seconds",
			Help:    "Сколько прошло от создания события в API до начала его обработки в воркере.",
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 15, 60, 300, 1800},
		}, []string{"event_type"}),

		Redeliveries: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "worker_event_redeliveries_total",
			Help: "Повторные доставки события (после ошибки обработчика) по типу.",
		}, []string{"event_type"}),

		DeadLetterPublished: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "worker_dead_letter_published_total",
			Help: "События, исчерпавшие все попытки и отправленные в dead-letter стрим, по типу и исходу публикации (ok/error).",
		}, []string{"event_type", "result"}),

		ConsumerPending: factory.NewGauge(prometheus.GaugeOpts{
			Name: "worker_consumer_pending_messages",
			Help: "Сколько сообщений ещё не доставлено консьюмеру (очередь в NATS).",
		}),

		ConsumerAckPending: factory.NewGauge(prometheus.GaugeOpts{
			Name: "worker_consumer_ack_pending_messages",
			Help: "Сколько доставленных консьюмеру сообщений ещё не подтверждено.",
		}),

		ConsumerRedelivered: factory.NewGauge(prometheus.GaugeOpts{
			Name: "worker_consumer_redelivered_messages",
			Help: "Сколько сообщений консьюмера сейчас ждут повторной доставки.",
		}),

		DeadLetterMessages: factory.NewGauge(prometheus.GaugeOpts{
			Name: "worker_dead_letter_messages",
			Help: "Сколько сообщений лежит в dead-letter стриме NATS.",
		}),
	}
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.Handler()
}
