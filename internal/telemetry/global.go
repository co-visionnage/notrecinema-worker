package telemetry

import (
	"net/http"
	"runtime"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Сквозные метрики воркера: письма, push, токены, соединение с NATS,
// исходящие HTTP-запросы. Пишутся из разных пакетов, поэтому это пакетные
// переменные на реестре по умолчанию (по одной на процесс), а не поля
// структуры, которую пришлось бы протаскивать в каждый конструктор.
var (
	mailSent = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "worker_mail_total",
		Help: "Письма по виду (verify_email, series_added, ...) и результату (sent/failed).",
	}, []string{"kind", "result"})

	mailDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "worker_mail_send_duration_seconds",
		Help:    "Длительность отправки письма через почтовый сервис по виду письма.",
		Buckets: prometheus.DefBuckets,
	}, []string{"kind"})

	mailSkipped = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "worker_mail_skipped_total",
		Help: "Письма, которые не отправлены намеренно: по виду и причине (disabled, unverified, opted_out, ...).",
	}, []string{"kind", "reason"})

	pushSent = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "worker_push_total",
		Help: "Web push по категории и результату (sent/dead_subscription/failed).",
	}, []string{"category", "result"})

	pushDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "worker_push_send_duration_seconds",
		Help:    "Длительность отправки одного web push.",
		Buckets: prometheus.DefBuckets,
	})

	tokensCreated = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "worker_tokens_created_total",
		Help: "Созданные воркером одноразовые токены по виду (verify_email, reset_password, invitation).",
	}, []string{"kind"})

	natsConnected = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "worker_nats_connected",
		Help: "1, если воркер сейчас подключён к NATS, иначе 0.",
	})

	natsReconnects = promauto.NewCounter(prometheus.CounterOpts{
		Name: "worker_nats_reconnects_total",
		Help: "Сколько раз воркер переподключался к NATS.",
	})

	outboundRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "outbound_http_requests_total",
		Help: "Исходящие HTTP-запросы к внешним сервисам по хосту и исходу (success/client_error/server_error/error).",
	}, []string{"host", "outcome"})

	outboundDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "outbound_http_request_duration_seconds",
		Help:    "Длительность исходящего HTTP-запроса к внешнему сервису.",
		Buckets: prometheus.DefBuckets,
	}, []string{"host"})

	buildInfo = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "build_info",
		Help: "Версия и сборка сервиса (значение всегда 1, данные в метках).",
	}, []string{"service", "version", "go_version"})
)

// SetBuildInfo публикует версию сервиса.
func SetBuildInfo(service, version string) {
	buildInfo.WithLabelValues(service, version, runtime.Version()).Set(1)
}

// RecordMail учитывает попытку отправки письма и её длительность.
func RecordMail(kind string, started time.Time, err error) {
	result := "sent"
	if err != nil {
		result = "failed"
	}
	mailSent.WithLabelValues(kind, result).Inc()
	mailDuration.WithLabelValues(kind).Observe(time.Since(started).Seconds())
}

// RecordMailSkipped учитывает письмо, которое не отправлено намеренно.
func RecordMailSkipped(kind, reason string) {
	mailSkipped.WithLabelValues(kind, reason).Inc()
}

// RecordPush учитывает отправку одного web push. result: sent,
// dead_subscription или failed.
func RecordPush(category, result string, started time.Time) {
	pushSent.WithLabelValues(category, result).Inc()
	pushDuration.Observe(time.Since(started).Seconds())
}

// RecordToken учитывает созданный одноразовый токен.
func RecordToken(kind string) { tokensCreated.WithLabelValues(kind).Inc() }

// SetNATSConnected отражает состояние соединения с NATS.
func SetNATSConnected(connected bool) {
	if connected {
		natsConnected.Set(1)
		return
	}
	natsConnected.Set(0)
}

// RecordNATSReconnect учитывает переподключение к NATS.
func RecordNATSReconnect() { natsReconnects.Inc() }

type instrumentedTransport struct {
	base http.RoundTripper
}

func (t *instrumentedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	started := time.Now()
	resp, err := t.base.RoundTrip(req)

	outcome := "error"
	if err == nil {
		switch {
		case resp.StatusCode >= 500:
			outcome = "server_error"
		case resp.StatusCode >= 400:
			outcome = "client_error"
		default:
			outcome = "success"
		}
	}
	// Хост берётся из запроса; набор хостов конечен: почтовый API и push-
	// сервисы браузеров (FCM, Mozilla, Apple и т.д.).
	host := req.URL.Hostname()
	outboundRequests.WithLabelValues(host, outcome).Inc()
	outboundDuration.WithLabelValues(host).Observe(time.Since(started).Seconds())
	return resp, err
}

// InstrumentedClient возвращает HTTP-клиент, который считает запросы и их
// длительность по хосту назначения. Таймаут не задаётся (как у
// http.DefaultClient): срок определяет контекст запроса.
func InstrumentedClient() *http.Client {
	return &http.Client{Transport: &instrumentedTransport{base: http.DefaultTransport}}
}
