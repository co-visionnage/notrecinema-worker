package telemetry

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	JobsTotal   *prometheus.CounterVec
	JobDuration *prometheus.HistogramVec
}

func NewMetrics(registry prometheus.Registerer) *Metrics {
	factory := promauto.With(registry)

	return &Metrics{
		JobsTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "worker_jobs_total",
			Help: "Обработанные события по типу и результату (success/failure/duplicate).",
		}, []string{"event_type", "outcome"}),

		JobDuration: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "worker_job_duration_seconds",
			Help:    "Длительность обработки одного события обработчиком.",
			Buckets: prometheus.DefBuckets,
		}, []string{"event_type"}),
	}
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.Handler()
}
