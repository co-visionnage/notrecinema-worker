package mailer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestResend(t *testing.T, handler http.HandlerFunc) *Resend {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return NewResend("re_test_key", "notrecinema <noreply@notrecinema.ru>", "", nil).WithBaseURL(server.URL)
}

func TestSendPostsExpectedRequest(t *testing.T) {
	var (
		gotAuth, gotType, gotPath, gotMethod string
		gotBody                              map[string]any
	)
	r := newTestResend(t, func(w http.ResponseWriter, req *http.Request) {
		gotAuth = req.Header.Get("Authorization")
		gotType = req.Header.Get("Content-Type")
		gotPath = req.URL.Path
		gotMethod = req.Method
		body, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"abc"}`))
	})

	err := r.Send(context.Background(), Message{To: "anna@example.com", Subject: "Тема", HTML: "<p>hi</p>", Text: "hi"})
	if err != nil {
		t.Fatalf("Send() error: %v", err)
	}

	if gotMethod != http.MethodPost || gotPath != "/emails" {
		t.Errorf("request = %s %s, want POST /emails", gotMethod, gotPath)
	}
	if gotAuth != "Bearer re_test_key" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotType != "application/json" {
		t.Errorf("Content-Type = %q", gotType)
	}
	if gotBody["from"] != "notrecinema <noreply@notrecinema.ru>" {
		t.Errorf("from = %v", gotBody["from"])
	}
	to, _ := gotBody["to"].([]any)
	if len(to) != 1 || to[0] != "anna@example.com" {
		t.Errorf("to = %v, want exactly one recipient", gotBody["to"])
	}
	if gotBody["subject"] != "Тема" || gotBody["html"] != "<p>hi</p>" || gotBody["text"] != "hi" {
		t.Errorf("body = %v", gotBody)
	}
	if _, present := gotBody["reply_to"]; present {
		t.Error("reply_to must be omitted when not configured")
	}
}

func TestSendIncludesReplyToWhenConfigured(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(body, &gotBody)
	}))
	t.Cleanup(server.Close)

	r := NewResend("key", "from@notrecinema.ru", "support@notrecinema.ru", nil).WithBaseURL(server.URL)
	if err := r.Send(context.Background(), Message{To: "a@example.com", Subject: "s"}); err != nil {
		t.Fatalf("Send() error: %v", err)
	}
	if gotBody["reply_to"] != "support@notrecinema.ru" {
		t.Errorf("reply_to = %v", gotBody["reply_to"])
	}
}

func TestSendReportsStatusErrors(t *testing.T) {
	cases := []struct {
		status    int
		retryable bool
	}{
		{http.StatusUnprocessableEntity, false},
		{http.StatusUnauthorized, false},
		{http.StatusTooManyRequests, true},
		{http.StatusInternalServerError, true},
		{http.StatusBadGateway, true},
	}
	for _, tc := range cases {
		r := newTestResend(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(`{"message":"nope"}`))
		})

		err := r.Send(context.Background(), Message{To: "a@example.com", Subject: "s"})
		var statusErr *StatusError
		if !errors.As(err, &statusErr) {
			t.Fatalf("status %d: error = %v, want *StatusError", tc.status, err)
		}
		if statusErr.StatusCode != tc.status || statusErr.Retryable() != tc.retryable {
			t.Errorf("status %d: got code=%d retryable=%v, want retryable=%v", tc.status, statusErr.StatusCode, statusErr.Retryable(), tc.retryable)
		}
	}
}

func TestSendFailsOnNetworkError(t *testing.T) {
	r := NewResend("key", "from@notrecinema.ru", "", nil).WithBaseURL("http://127.0.0.1:1")
	if err := r.Send(context.Background(), Message{To: "a@example.com", Subject: "s"}); err == nil {
		t.Error("Send() succeeded against a closed port")
	}
}

func TestEnabled(t *testing.T) {
	if NewResend("", "from@notrecinema.ru", "", nil).Enabled() {
		t.Error("a transport without an API key must be disabled")
	}
	if NewResend("key", "", "", nil).Enabled() {
		t.Error("a transport without a from address must be disabled")
	}
	if !NewResend("key", "from@notrecinema.ru", "", nil).Enabled() {
		t.Error("a fully configured transport must be enabled")
	}
	var nilResend *Resend
	if nilResend.Enabled() {
		t.Error("a nil transport must be disabled")
	}
	if (Disabled{}).Enabled() {
		t.Error("Disabled{} must report disabled")
	}
	if err := (Disabled{}).Send(context.Background(), Message{}); err != nil {
		t.Errorf("Disabled.Send() = %v, want nil", err)
	}
}

func TestSendRefusesWhenNotConfigured(t *testing.T) {
	if err := NewResend("", "from@notrecinema.ru", "", nil).Send(context.Background(), Message{To: "a@example.com"}); err == nil {
		t.Error("Send() without an API key should fail loudly, not pretend to succeed")
	}
}
