package webpush

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	gowebpush "github.com/SherClockHolmes/webpush-go"
)

// realSubscription генерирует настоящую пару ключей P-256 (то, что в
// браузере делает pushManager.subscribe()) и случайный auth-секрет --
// без них шифрование payload'а (RFC8291) внутри Send() просто не
// отработает, а не только подделает данные. То есть тест гоняет
// реальную криптографию библиотеки, а не заглушку.
func realSubscription(t *testing.T, endpoint string) Subscription {
	t.Helper()

	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate EC key: %v", err)
	}

	authSecret := make([]byte, 16)
	if _, err := rand.Read(authSecret); err != nil {
		t.Fatalf("failed to generate auth secret: %v", err)
	}

	return Subscription{
		Endpoint: endpoint,
		P256dh:   base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()),
		Auth:     base64.RawURLEncoding.EncodeToString(authSecret),
	}
}

func TestSendPerformsRealEncryptedRequest(t *testing.T) {
	privateKey, publicKey, err := gowebpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatalf("failed to generate VAPID keys: %v", err)
	}

	var (
		gotAuthHeader   string
		gotEncoding     string
		gotBodyNonEmpty bool
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthHeader = r.Header.Get("Authorization")
		gotEncoding = r.Header.Get("Content-Encoding")
		buf := make([]byte, 1)
		n, _ := r.Body.Read(buf)
		gotBodyNonEmpty = n > 0
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	sender := NewSender(Config{
		PublicKey:  publicKey,
		PrivateKey: privateKey,
		Subject:    "mailto:test@example.com",
	})

	sub := realSubscription(t, server.URL)

	err = sender.Send(context.Background(), sub, Payload{Title: "Заголовок", Body: "Текст уведомления"})
	if err != nil {
		t.Fatalf("Send() error: %v", err)
	}

	if gotAuthHeader == "" {
		t.Error("запрос не содержал заголовок Authorization (VAPID JWT)")
	}
	if gotEncoding != "aes128gcm" {
		t.Errorf("Content-Encoding = %q, want aes128gcm", gotEncoding)
	}
	if !gotBodyNonEmpty {
		t.Error("тело запроса пустое, want зашифрованный payload")
	}
}

func TestSendReportsDeadSubscription(t *testing.T) {
	privateKey, publicKey, err := gowebpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatalf("failed to generate VAPID keys: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone)
	}))
	defer server.Close()

	sender := NewSender(Config{PublicKey: publicKey, PrivateKey: privateKey, Subject: "mailto:test@example.com"})
	sub := realSubscription(t, server.URL)

	err = sender.Send(context.Background(), sub, Payload{Title: "x", Body: "y"})
	if !errors.Is(err, ErrDeadSubscription) {
		t.Errorf("Send() error = %v, want ErrDeadSubscription", err)
	}
}

func TestConfigConfigured(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"all set", Config{PublicKey: "a", PrivateKey: "b", Subject: "c"}, true},
		{"missing subject", Config{PublicKey: "a", PrivateKey: "b"}, false},
		{"empty", Config{}, false},
	}
	for _, tc := range cases {
		if got := tc.cfg.Configured(); got != tc.want {
			t.Errorf("%s: Configured() = %v, want %v", tc.name, got, tc.want)
		}
	}
}
