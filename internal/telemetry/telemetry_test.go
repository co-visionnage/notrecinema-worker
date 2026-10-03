package telemetry

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func value(t *testing.T, name string, labels map[string]string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
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
			case metric.Gauge != nil:
				return metric.GetGauge().GetValue()
			case metric.Histogram != nil:
				return float64(metric.GetHistogram().GetSampleCount())
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

func TestRecordMailCountsResultAndDuration(t *testing.T) {
	sentBefore := value(t, "worker_mail_total", map[string]string{"kind": "unit_kind", "result": "sent"})
	failedBefore := value(t, "worker_mail_total", map[string]string{"kind": "unit_kind", "result": "failed"})
	durationBefore := value(t, "worker_mail_send_duration_seconds", map[string]string{"kind": "unit_kind"})

	RecordMail("unit_kind", time.Now(), nil)
	RecordMail("unit_kind", time.Now(), errors.New("boom"))

	if got := value(t, "worker_mail_total", map[string]string{"kind": "unit_kind", "result": "sent"}) - sentBefore; got != 1 {
		t.Errorf("sent = %v, want 1", got)
	}
	if got := value(t, "worker_mail_total", map[string]string{"kind": "unit_kind", "result": "failed"}) - failedBefore; got != 1 {
		t.Errorf("failed = %v, want 1", got)
	}
	if got := value(t, "worker_mail_send_duration_seconds", map[string]string{"kind": "unit_kind"}) - durationBefore; got != 2 {
		t.Errorf("duration observations = %v, want 2", got)
	}
}

func TestRecordPushAndMailSkippedAndToken(t *testing.T) {
	skippedBefore := value(t, "worker_mail_skipped_total", map[string]string{"kind": "unit_kind", "reason": "opted_out"})
	pushBefore := value(t, "worker_push_total", map[string]string{"category": "unit_cat", "result": "dead_subscription"})
	tokenBefore := value(t, "worker_tokens_created_total", map[string]string{"kind": "unit_token"})

	RecordMailSkipped("unit_kind", "opted_out")
	RecordPush("unit_cat", "dead_subscription", time.Now())
	RecordToken("unit_token")

	if value(t, "worker_mail_skipped_total", map[string]string{"kind": "unit_kind", "reason": "opted_out"})-skippedBefore != 1 {
		t.Error("a skipped letter was not counted")
	}
	if value(t, "worker_push_total", map[string]string{"category": "unit_cat", "result": "dead_subscription"})-pushBefore != 1 {
		t.Error("a push result was not counted")
	}
	if value(t, "worker_tokens_created_total", map[string]string{"kind": "unit_token"})-tokenBefore != 1 {
		t.Error("a created token was not counted")
	}
}

func TestNATSConnectionGaugeAndReconnects(t *testing.T) {
	SetNATSConnected(true)
	if got := value(t, "worker_nats_connected", nil); got != 1 {
		t.Errorf("connected gauge = %v, want 1", got)
	}
	SetNATSConnected(false)
	if got := value(t, "worker_nats_connected", nil); got != 0 {
		t.Errorf("connected gauge = %v, want 0", got)
	}

	before := value(t, "worker_nats_reconnects_total", nil)
	RecordNATSReconnect()
	if got := value(t, "worker_nats_reconnects_total", nil) - before; got != 1 {
		t.Errorf("reconnects = %v, want 1", got)
	}
}

func TestInstrumentedClientClassifiesOutcomes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.WriteHeader(http.StatusCreated)
		case "/gone":
			w.WriteHeader(http.StatusGone)
		default:
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	t.Cleanup(server.Close)

	host := strings.TrimPrefix(server.URL, "http://")
	host = host[:strings.LastIndex(host, ":")]
	count := func(outcome string) float64 {
		return value(t, "outbound_http_requests_total", map[string]string{"host": host, "outcome": outcome})
	}
	ok0, client0, srv0 := count("success"), count("client_error"), count("server_error")

	client := InstrumentedClient()
	for _, path := range []string{"/ok", "/gone", "/bad"} {
		resp, err := client.Get(server.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_ = resp.Body.Close()
	}
	if count("success")-ok0 != 1 || count("client_error")-client0 != 1 || count("server_error")-srv0 != 1 {
		t.Error("outcomes were not classified")
	}
}

func TestOperationOf(t *testing.T) {
	cases := map[string]string{
		"SELECT public.create_email_token($1)": "function",
		"SELECT email FROM public.profiles":    "select",
		"INSERT INTO public.x VALUES ($1)":     "insert",
		"update public.x set a = 1":            "update",
		"DELETE FROM public.x":                 "delete",
		"":                                     "other",
	}
	for sql, want := range cases {
		if got := operationOf(sql); got != want {
			t.Errorf("operationOf(%q) = %q, want %q", sql, got, want)
		}
	}
}
