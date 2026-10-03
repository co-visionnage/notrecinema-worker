package config

import (
	"os"
	"testing"
)

func TestMailRequiresTheUnsubscribeSecret(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgresql://u:p@localhost/db")
	t.Setenv("RESEND_API_KEY", "re_key")
	t.Setenv("UNSUBSCRIBE_SECRET", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a mail key without UNSUBSCRIBE_SECRET: notification letters would have a dead unsubscribe link")
	}

	t.Setenv("UNSUBSCRIBE_SECRET", "shared-secret")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.UnsubscribeSecret != "shared-secret" {
		t.Errorf("UnsubscribeSecret = %q", cfg.UnsubscribeSecret)
	}
}

func TestWithoutMailTheSecretIsOptional(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgresql://u:p@localhost/db")
	t.Setenv("RESEND_API_KEY", "")
	t.Setenv("UNSUBSCRIBE_SECRET", "")

	if _, err := Load(); err != nil {
		t.Errorf("Load() with mail disabled error: %v", err)
	}
}

func TestMailDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgresql://u:p@localhost/db")
	t.Setenv("RESEND_API_KEY", "")
	// Пустое значение -- уже заданное; чтобы сработал default, переменную
	// нужно именно снять (t.Setenv вернёт прежнее значение после теста).
	for _, name := range []string{"MAIL_FROM", "APP_URL"} {
		t.Setenv(name, "")
		_ = os.Unsetenv(name)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.MailFrom != "notrecinema <noreply@notrecinema.ru>" {
		t.Errorf("MailFrom = %q", cfg.MailFrom)
	}
	if cfg.AppURL != "http://localhost:3000" {
		t.Errorf("AppURL = %q", cfg.AppURL)
	}
}
