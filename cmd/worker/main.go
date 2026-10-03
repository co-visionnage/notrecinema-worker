// Команда worker — фоновый процесс, потребляющий доменные события из NATS
// JetStream, которые notrecinema-api публикует через transactional outbox.
// Отдельный процесс, а не горутина внутри API, чтобы обработка событий
// могла масштабироваться, деплоиться и падать независимо от HTTP-трафика.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"notrecinema/worker/internal/config"
	"notrecinema/worker/internal/consumer"
	"notrecinema/worker/internal/eventbus"
	"notrecinema/worker/internal/handlers"
	"notrecinema/worker/internal/logging"
	"notrecinema/worker/internal/mailer"
	"notrecinema/worker/internal/mailing"
	"notrecinema/worker/internal/notifications"
	"notrecinema/worker/internal/postgres"
	"notrecinema/worker/internal/telemetry"
	"notrecinema/worker/internal/webpush"
)

// version подставляется при сборке образа: -ldflags "-X main.version=<sha>".
var version = "dev"

func main() {
	logger := logging.New("notrecinema-worker")

	if err := run(logger); err != nil {
		logger.Error("fatal startup error", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdownTracing, err := telemetry.InitTracing(ctx, "notrecinema-worker", cfg.OtlpEndpoint)
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTracing(shutdownCtx); err != nil {
			logger.Warn("telemetry: ошибка при остановке трейсера", "error", err)
		}
	}()

	metrics := telemetry.NewMetrics(prometheus.DefaultRegisterer)
	telemetry.SetBuildInfo("notrecinema-worker", version)

	db, err := postgres.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	prometheus.MustRegister(telemetry.NewPoolCollector(db.Stat))

	conn, js, err := eventbus.Connect(cfg.NatsURL)
	if err != nil {
		return err
	}
	defer conn.Close()

	deadLetterStream, err := eventbus.EnsureDeadLetterStream(ctx, js)
	if err != nil {
		return err
	}
	deadLetterPublisher := eventbus.NewDeadLetterPublisher(js)

	pushConfig := webpush.Config{
		PublicKey:  cfg.VAPIDPublicKey,
		PrivateKey: cfg.VAPIDPrivateKey,
		Subject:    cfg.VAPIDSubject,
	}
	if !pushConfig.Configured() {
		logger.Warn("webpush: VAPID-ключи не заданы, push-уведомления отключены")
	}
	sender := webpush.NewSender(pushConfig)
	notifier := notifications.NewNotifier(db, sender, pushConfig, logger)

	var mailSender mailer.Sender = mailer.Disabled{}
	if cfg.ResendAPIKey != "" {
		resend := mailer.NewResend(cfg.ResendAPIKey, cfg.MailFrom, cfg.MailReplyTo, nil)
		if cfg.ResendAPIURL != "" {
			resend = resend.WithBaseURL(cfg.ResendAPIURL)
		}
		mailSender = resend
	} else {
		logger.Warn("mailer: RESEND_API_KEY не задан, письма отключены")
	}
	mail := mailing.NewService(mailSender, mailing.NewPgStore(db), cfg.AppURL, cfg.UnsubscribeSecret, logger)

	c := consumer.New(js, db, logger, metrics, cfg.MaxDeliver).
		WithDeadLetterPublisher(deadLetterPublisher).
		WithDeadLetterStream(deadLetterStream)
	c.Handle("family.member.joined", handlers.FamilyMemberJoined(logger, notifier))
	c.Handle("movie.added", handlers.MovieAdded(logger, notifier, mail))
	c.Handle("movie.watched", handlers.MovieWatched(logger, notifier))
	c.Handle("movie.rated", handlers.MovieRated(logger, notifier))
	c.Handle("poll.created", handlers.PollCreated(logger, notifier))
	c.Handle("poll.closed", handlers.PollClosed(logger, notifier))
	c.Handle("watch_event.created", handlers.WatchEventCreated(logger, notifier))
	c.Handle("progress.stale", handlers.ProgressStale(logger, notifier, mail))
	c.Handle("season.updated", handlers.SeasonUpdated(logger, notifier, mail))
	c.Handle("series.bulk_added", handlers.SeriesBulkAdded(logger, notifier))

	// Транзакционные письма: подтверждение email, сброс пароля,
	// уведомления о безопасности и удаление аккаунта.
	c.Handle("email.verification_requested", handlers.EmailVerificationRequested(logger, mail))
	c.Handle("email.password_reset_requested", handlers.PasswordResetRequested(logger, mail))
	c.Handle("security.password_changed", handlers.PasswordChanged(logger, mail))
	c.Handle("security.two_factor_enabled", handlers.TwoFactorEnabled(logger, mail))
	c.Handle("security.two_factor_disabled", handlers.TwoFactorDisabled(logger, mail))
	c.Handle("account.deleted", handlers.AccountDeleted(logger, mail))
	c.Handle("security.backup_code_used", handlers.BackupCodeUsed(logger, mail))
	c.Handle("security.backup_codes_regenerated", handlers.BackupCodesRegenerated(logger, mail))
	c.Handle("email.welcome", handlers.Welcome(logger, mail))
	c.Handle("family.invitation_requested", handlers.FamilyInvitationRequested(logger, mail))

	// Минимальный HTTP-сервер для docker/k8s healthcheck и Prometheus -- у
	// воркера нет публичного API, но метрики и пробы всё равно нужно
	// куда-то отдавать. liveness (/healthz) отвечает, пока процесс жив;
	// readiness (/readyz) -- пока жив ещё и NATS-коннекшен, иначе
	// оркестратор продолжал бы слать поду трафик health-check'а как
	// "готов", пока воркер реально ничего не потребляет.
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		})
		mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
			if !conn.IsConnected() {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"status":"unavailable"}`))
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ready"}`))
		})
		mux.Handle("GET /metrics", metrics.Handler())
		if err := http.ListenAndServe(":8081", mux); err != nil {
			logger.Error("healthz server error", "error", err)
		}
	}()

	logger.Info("worker starting", "nats_url", cfg.NatsURL, "max_deliver", cfg.MaxDeliver)

	if err := c.Run(ctx, eventbus.StreamName, "notrecinema-worker"); err != nil {
		return err
	}

	logger.Info("worker остановлен")
	return nil
}
