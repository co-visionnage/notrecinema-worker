//go:build integration

// Запуск: go test -tags integration ./...
package notifications_test

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	gowebpush "github.com/SherClockHolmes/webpush-go"
	"github.com/jackc/pgx/v5"

	"notrecinema/worker/internal/notifications"
	"notrecinema/worker/internal/postgres"
	"notrecinema/worker/internal/webpush"
)

// rawConnect открывает соединение в обход postgres.Pool: тестовой
// подготовке (создание пользователей/семьи/подписок) нужен
// app.current_user_id на транзакцию, а Pool из этого пакета сознательно
// его не даёт -- у воркера в проде нет пользовательского контекста.
func rawConnect(t *testing.T, ctx context.Context, databaseURL string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })
	return conn
}

func createTestUser(t *testing.T, ctx context.Context, conn *pgx.Conn) string {
	t.Helper()
	tokenBytes := make([]byte, 16)
	_, _ = rand.Read(tokenBytes)
	token := hex.EncodeToString(tokenBytes)
	hash := sha256.Sum256([]byte(token))
	email := fmt.Sprintf("notify-test-%s@example.com", token)

	var userID string
	row := conn.QueryRow(ctx,
		"SELECT user_id FROM public.create_profile_session($1, $2, $3, $4)",
		email, "Notify Test", hex.EncodeToString(hash[:]), time.Now().Add(time.Hour),
	)
	if err := row.Scan(&userID); err != nil {
		t.Fatalf("failed to create test user: %v", err)
	}
	return userID
}

func createTestFamilyWithMembers(t *testing.T, ctx context.Context, conn *pgx.Conn, owner string, members ...string) string {
	t.Helper()
	tokenBytes := make([]byte, 4)
	_, _ = rand.Read(tokenBytes)

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SELECT set_config('app.current_user_id', $1, true)", owner); err != nil {
		t.Fatalf("set owner context: %v", err)
	}

	var familyID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO public.families (name, owner_id, invite_code)
		VALUES ($1, $2, $3) RETURNING id
	`, "Notify Test Family", owner, hex.EncodeToString(tokenBytes)).Scan(&familyID); err != nil {
		t.Fatalf("create family: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO public.family_members (family_id, user_id, role) VALUES ($1, $2, 'owner')
	`, familyID, owner); err != nil {
		t.Fatalf("add owner: %v", err)
	}

	for _, member := range members {
		if _, err := tx.Exec(ctx, "SELECT set_config('app.current_user_id', $1, true)", member); err != nil {
			t.Fatalf("set member context: %v", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO public.family_members (family_id, user_id, role) VALUES ($1, $2, 'member')
		`, familyID, member); err != nil {
			t.Fatalf("add member: %v", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return familyID
}

// createTestSubscription создаёт настоящую пару ключей P-256 (то же, что
// делает браузер) и сохраняет её как push-подписку пользователя.
func createTestSubscription(t *testing.T, ctx context.Context, conn *pgx.Conn, userID, endpoint string) {
	t.Helper()

	sub := realSubscription(t, endpoint)

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SELECT set_config('app.current_user_id', $1, true)", userID); err != nil {
		t.Fatalf("set context: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO public.push_subscriptions (user_id, endpoint, p256dh, auth)
		VALUES ($1, $2, $3, $4)
	`, userID, sub.Endpoint, sub.P256dh, sub.Auth); err != nil {
		t.Fatalf("insert subscription: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func realSubscription(t *testing.T, endpoint string) webpush.Subscription {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate EC key: %v", err)
	}
	authSecret := make([]byte, 16)
	_, _ = rand.Read(authSecret)

	return webpush.Subscription{
		Endpoint: endpoint,
		P256dh:   base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()),
		Auth:     base64.RawURLEncoding.EncodeToString(authSecret),
	}
}

func TestNotifyFamilyExcludesActorAndReachesOtherMembers(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL не задан, пропускаем интеграционный тест")
	}

	ctx := context.Background()
	conn := rawConnect(t, ctx, databaseURL)

	actor := createTestUser(t, ctx, conn)
	other := createTestUser(t, ctx, conn)
	familyID := createTestFamilyWithMembers(t, ctx, conn, actor, other)

	var receivedByActor, receivedByOther bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/actor":
			receivedByActor = true
		case "/other":
			receivedByOther = true
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	createTestSubscription(t, ctx, conn, actor, server.URL+"/actor")
	createTestSubscription(t, ctx, conn, other, server.URL+"/other")

	privateKey, publicKey, err := gowebpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatalf("generate VAPID keys: %v", err)
	}
	pushCfg := webpush.Config{PublicKey: publicKey, PrivateKey: privateKey, Subject: "mailto:test@example.com"}
	sender := webpush.NewSender(pushCfg)

	db, err := postgres.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect pool: %v", err)
	}
	defer db.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	notifier := notifications.NewNotifier(db, sender, pushCfg, logger)

	if err := notifier.NotifyFamily(ctx, familyID, actor, "series_added", webpush.Payload{Title: "t", Body: "b"}); err != nil {
		t.Fatalf("NotifyFamily() error: %v", err)
	}

	if receivedByActor {
		t.Error("actor (исключённый из рассылки) получил уведомление, want не должен был")
	}
	if !receivedByOther {
		t.Error("other (участник семьи) не получил уведомление, want должен был")
	}
}

// setPushPreference сохраняет настройку пользователя под его же контекстом
// (так её сохраняет и API): RLS пускает только в свои строки.
func setPushPreference(t *testing.T, ctx context.Context, conn *pgx.Conn, userID, category string, push bool) {
	t.Helper()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SELECT set_config('app.current_user_id', $1, true)", userID); err != nil {
		t.Fatalf("set context: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO public.notification_preferences (user_id, category, push, email) VALUES ($1, $2, $3, false)
		ON CONFLICT (user_id, category) DO UPDATE SET push = EXCLUDED.push
	`, userID, category, push); err != nil {
		t.Fatalf("set preference: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func newTestNotifier(t *testing.T, ctx context.Context, databaseURL string) *notifications.Notifier {
	t.Helper()
	privateKey, publicKey, err := gowebpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatalf("generate VAPID keys: %v", err)
	}
	pushCfg := webpush.Config{PublicKey: publicKey, PrivateKey: privateKey, Subject: "mailto:test@example.com"}

	db, err := postgres.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect pool: %v", err)
	}
	t.Cleanup(db.Close)

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	return notifications.NewNotifier(db, webpush.NewSender(pushCfg), pushCfg, logger)
}

func TestNotifyFamilySkipsMembersWhoSwitchedTheCategoryOff(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL не задан, пропускаем интеграционный тест")
	}
	ctx := context.Background()
	conn := rawConnect(t, ctx, databaseURL)

	actor := createTestUser(t, ctx, conn)
	optedOut := createTestUser(t, ctx, conn)
	subscribed := createTestUser(t, ctx, conn)
	familyID := createTestFamilyWithMembers(t, ctx, conn, actor, optedOut, subscribed)

	hits := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits[r.URL.Path]++
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	createTestSubscription(t, ctx, conn, optedOut, server.URL+"/opted-out")
	createTestSubscription(t, ctx, conn, subscribed, server.URL+"/subscribed")
	setPushPreference(t, ctx, conn, optedOut, "poll", false)

	notifier := newTestNotifier(t, ctx, databaseURL)

	// Категория "poll" выключена только у одного участника.
	if err := notifier.NotifyFamily(ctx, familyID, actor, "poll", webpush.Payload{Title: "t", Body: "b"}); err != nil {
		t.Fatalf("NotifyFamily(poll) error: %v", err)
	}
	if hits["/opted-out"] != 0 {
		t.Error("a member who switched the category off still got the push")
	}
	if hits["/subscribed"] != 1 {
		t.Errorf("a member with default settings got %d pushes, want 1", hits["/subscribed"])
	}

	// Отказ действует только на свою категорию: другие приходят как раньше.
	if err := notifier.NotifyFamily(ctx, familyID, actor, "series_added", webpush.Payload{Title: "t", Body: "b"}); err != nil {
		t.Fatalf("NotifyFamily(series_added) error: %v", err)
	}
	if hits["/opted-out"] != 1 {
		t.Errorf("another category must still reach the member: got %d", hits["/opted-out"])
	}
}

func TestNotifyUserHonoursTheCategorySwitch(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL не задан, пропускаем интеграционный тест")
	}
	ctx := context.Background()
	conn := rawConnect(t, ctx, databaseURL)
	user := createTestUser(t, ctx, conn)

	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	createTestSubscription(t, ctx, conn, user, server.URL+"/me")
	notifier := newTestNotifier(t, ctx, databaseURL)

	if err := notifier.NotifyUser(ctx, user, "progress_reminder", webpush.Payload{Title: "t", Body: "b"}); err != nil {
		t.Fatalf("NotifyUser() error: %v", err)
	}
	if hits != 1 {
		t.Fatalf("pushes by default = %d, want 1", hits)
	}

	setPushPreference(t, ctx, conn, user, "progress_reminder", false)
	if err := notifier.NotifyUser(ctx, user, "progress_reminder", webpush.Payload{Title: "t", Body: "b"}); err != nil {
		t.Fatalf("NotifyUser() error: %v", err)
	}
	if hits != 1 {
		t.Errorf("a push was sent after the category was switched off (total %d)", hits)
	}
}
