package emails

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func allContents() map[string]Content {
	links := NewLinks("https://notrecinema.ru/")
	return map[string]Content{
		"verify":          VerifyEmail("Аня", links.VerifyEmail("tok")),
		"reset":           PasswordReset("Аня", links.ResetPassword("tok")),
		"password":        PasswordChanged("Аня", links.ForgotPassword()),
		"2fa-enabled":     TwoFactorEnabled("Аня"),
		"2fa-disabled":    TwoFactorDisabled("Аня", links.ForgotPassword()),
		"deleted":         AccountDeleted("Аня"),
		"series":          SeriesAdded("Аня", "Во все тяжкие", links.Series("abc")),
		"season":          SeasonUpdated("Аня", "Во все тяжкие", 5, links.Home()),
		"stale":           ProgressStale("Аня", "Во все тяжкие", 2, 7, links.Series("abc")),
		"backup-used":     BackupCodeUsed("Аня", 7, links.Settings()),
		"backup-low":      BackupCodeUsed("Аня", 1, links.Settings()),
		"backup-regen":    BackupCodesRegenerated("Аня", links.Settings()),
		"welcome":         Welcome("Аня", links.Home()),
		"invitation":      FamilyInvitation("Борис", "Семья Ивановых", links.Invite("tok")),
		"no-name-verify":  VerifyEmail("", links.VerifyEmail("tok")),
		"no-name-deleted": AccountDeleted("   "),
	}
}

func TestEveryEmailRenders(t *testing.T) {
	for name, content := range allContents() {
		t.Run(name, func(t *testing.T) {
			out, err := Render(content)
			if err != nil {
				t.Fatalf("Render() error: %v", err)
			}
			if out.Subject == "" || out.HTML == "" || out.Text == "" {
				t.Fatalf("empty part: subject=%q html=%d text=%d", out.Subject, len(out.HTML), len(out.Text))
			}
			if !strings.Contains(out.HTML, content.Heading) {
				t.Errorf("html does not contain the heading %q", content.Heading)
			}
			if !strings.Contains(out.Text, content.Heading) {
				t.Errorf("text does not contain the heading %q", content.Heading)
			}
			if strings.Contains(out.HTML, "ZgotmplZ") {
				t.Error("html/template filtered a value (ZgotmplZ): a style or url was rejected")
			}
			if strings.Contains(out.HTML, "<no value>") || strings.Contains(out.Text, "<no value>") {
				t.Error("a template field was left unset")
			}
		})
	}
}

func TestButtonLinkAppearsInBothVersions(t *testing.T) {
	link := NewLinks("https://notrecinema.ru").VerifyEmail("a b&c")
	out, err := Render(VerifyEmail("Аня", link))
	if err != nil {
		t.Fatalf("Render() error: %v", err)
	}

	// Токен в ссылке должен быть экранирован для query-строки.
	if want := "https://notrecinema.ru/verify-email?token=a+b%26c"; link != want {
		t.Fatalf("link = %q, want %q", link, want)
	}
	if !strings.Contains(out.Text, link) {
		t.Error("the text version must contain the full link")
	}
	// В HTML ссылка встречается в кнопке и в запасной ссылке для клиентов,
	// не показывающих кнопки (href + видимый текст). html/template
	// записывает "+" как &#43; -- это тот же символ для браузера.
	if got := strings.Count(out.HTML, "https://notrecinema.ru/verify-email?token=a&#43;b%26c"); got != 3 {
		t.Errorf("html contains the link %d times, want 3 (button href + fallback href + fallback text)", got)
	}
}

func TestHTMLEscapesUserControlledText(t *testing.T) {
	evil := `<script>alert("x")</script> & «title»`
	out, err := Render(SeriesAdded(`<b>Аня</b>`, evil, "https://notrecinema.ru/series/1"))
	if err != nil {
		t.Fatalf("Render() error: %v", err)
	}

	if strings.Contains(out.HTML, "<script>") || strings.Contains(out.HTML, "<b>Аня</b>") {
		t.Errorf("user-controlled text was not escaped:\n%s", out.HTML)
	}
	if !strings.Contains(out.HTML, "&lt;script&gt;") {
		t.Error("escaped script tag not found in html")
	}
	// Текстовая версия не HTML: там символы остаются как есть.
	if !strings.Contains(out.Text, evil) {
		t.Error("text version should keep the title verbatim")
	}
}

func TestRejectsJavascriptLinks(t *testing.T) {
	out, err := Render(Content{
		Subject: "x", Heading: "x",
		Button: &Button{Label: "go", URL: "javascript:alert(1)"},
	})
	if err != nil {
		t.Fatalf("Render() error: %v", err)
	}
	if strings.Contains(out.HTML, `href="javascript:`) {
		t.Error("a javascript: URL must never reach an href")
	}
}

func TestRenderValidatesInput(t *testing.T) {
	if _, err := Render(Content{Heading: "no subject"}); err == nil {
		t.Error("Render() accepted a letter without a subject")
	}
	if _, err := Render(Content{Subject: "s", Accent: "purple"}); err == nil {
		t.Error("Render() accepted an unknown accent")
	}
}

func TestDefaultsApplied(t *testing.T) {
	out, err := Render(Content{Subject: "s", Heading: "h"})
	if err != nil {
		t.Fatalf("Render() error: %v", err)
	}
	if !strings.Contains(out.HTML, "#facc15") {
		t.Error("default accent should be yellow")
	}
	if !strings.Contains(out.Text, defaultReason) {
		t.Error("default footer reason is missing")
	}
}

func TestGreetingFallsBackWithoutName(t *testing.T) {
	if got := greeting("  "); got != "Здравствуйте!" {
		t.Errorf("greeting(blank) = %q", got)
	}
	if got := greeting("Аня"); got != "Привет, Аня!" {
		t.Errorf("greeting(Аня) = %q", got)
	}
}

func TestPluralRu(t *testing.T) {
	cases := map[int]string{
		1: "сезон", 2: "сезона", 4: "сезона", 5: "сезонов", 11: "сезонов",
		12: "сезонов", 21: "сезон", 22: "сезона", 25: "сезонов", 101: "сезон", 111: "сезонов",
	}
	for n, want := range cases {
		if got := pluralRu(n, "сезон", "сезона", "сезонов"); got != want {
			t.Errorf("pluralRu(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestSeasonEmailMentionsPluralizedCount(t *testing.T) {
	c := SeasonUpdated("Аня", "Шоу", 3, "https://notrecinema.ru/")
	if c.Highlight == nil || c.Highlight.Text != "3 сезона" {
		t.Errorf("highlight = %+v, want text %q", c.Highlight, "3 сезона")
	}
}

// TestWritePreviews сохраняет все письма в HTML-файлы для просмотра
// глазами в браузере. Без переменной окружения пропускается:
//
//	EMAIL_PREVIEW_DIR=/tmp/previews go test ./internal/emails -run Previews
func TestWritePreviews(t *testing.T) {
	dir := os.Getenv("EMAIL_PREVIEW_DIR")
	if dir == "" {
		t.Skip("EMAIL_PREVIEW_DIR не задан")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	var index strings.Builder
	index.WriteString("<!doctype html><meta charset=utf-8><title>Превью писем</title><h1>Превью писем</h1><ul>")
	names := make([]string, 0)
	for name := range allContents() {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		out, err := Render(allContents()[name])
		if err != nil {
			t.Fatalf("Render(%s): %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".html"), []byte(out.HTML), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		index.WriteString(fmt.Sprintf(`<li><a href="%s.html">%s</a> — %s</li>`, name, name, out.Subject))
	}
	index.WriteString("</ul>")
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(index.String()), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
}

func TestNotificationFooterOffersUnsubscribe(t *testing.T) {
	links := NewLinks("https://notrecinema.ru")
	content := SeriesAdded("Аня", "Шоу", links.Series("1"))
	content.UnsubscribeURL = links.Unsubscribe("tok")
	content.PreferencesURL = links.Settings()

	out, err := Render(content)
	if err != nil {
		t.Fatalf("Render() error: %v", err)
	}
	for _, want := range []string{"https://notrecinema.ru/unsubscribe?token=tok", "https://notrecinema.ru/settings"} {
		if !strings.Contains(out.HTML, want) {
			t.Errorf("html footer lacks %q", want)
		}
		if !strings.Contains(out.Text, want) {
			t.Errorf("text footer lacks %q", want)
		}
	}
	if !strings.Contains(out.HTML, "Отписаться от таких писем") {
		t.Error("html footer lacks the unsubscribe wording")
	}
}

func TestTransactionalLettersHaveNoUnsubscribe(t *testing.T) {
	for name, content := range map[string]Content{
		"verify":     VerifyEmail("Аня", "https://notrecinema.ru/verify-email?token=t"),
		"reset":      PasswordReset("Аня", "https://notrecinema.ru/reset-password?token=t"),
		"password":   PasswordChanged("Аня", "https://notrecinema.ru/forgot-password"),
		"backup":     BackupCodeUsed("Аня", 3, "https://notrecinema.ru/settings"),
		"invitation": FamilyInvitation("Борис", "Семья", "https://notrecinema.ru/invite?token=t"),
		"welcome":    Welcome("Аня", "https://notrecinema.ru/"),
	} {
		out, err := Render(content)
		if err != nil {
			t.Fatalf("%s: Render() error: %v", name, err)
		}
		if strings.Contains(out.HTML, "Отписаться") || strings.Contains(out.Text, "Отписаться") {
			t.Errorf("%s: a transactional letter must not offer to unsubscribe (it cannot be turned off)", name)
		}
	}
}

func TestBackupCodeLetterWarnsWhenCodesRunLow(t *testing.T) {
	few := BackupCodeUsed("Аня", 2, "https://notrecinema.ru/settings")
	many := BackupCodeUsed("Аня", 8, "https://notrecinema.ru/settings")

	joined := func(c Content) string { return strings.Join(c.Paragraphs, " ") }
	if !strings.Contains(joined(few), "Кодов осталось совсем мало") {
		t.Error("the low-codes warning is missing at 2 remaining")
	}
	if strings.Contains(joined(many), "Кодов осталось совсем мало") {
		t.Error("the low-codes warning is shown with 8 remaining")
	}
	if !strings.Contains(joined(many), "8") {
		t.Error("the remaining count is not mentioned")
	}
}

func TestInvitationLetterNamesFamilyAndInviter(t *testing.T) {
	c := FamilyInvitation("Борис", "Семья Ивановых", "https://notrecinema.ru/invite?token=t")
	if !strings.Contains(c.Subject, "Борис") || !strings.Contains(c.Subject, "Семья Ивановых") {
		t.Errorf("subject = %q", c.Subject)
	}
	if c.Highlight == nil || c.Highlight.Title != "Семья Ивановых" {
		t.Errorf("highlight = %+v", c.Highlight)
	}
}

func TestLinksForNewPages(t *testing.T) {
	links := NewLinks("https://notrecinema.ru/")
	cases := map[string]string{
		links.Settings():              "https://notrecinema.ru/settings",
		links.Invite("a b"):           "https://notrecinema.ru/invite?token=a+b",
		links.Unsubscribe("x.y"):      "https://notrecinema.ru/unsubscribe?token=x.y",
		links.UnsubscribeAPI("x.y"):   "https://notrecinema.ru/api/v1/unsubscribe?token=x.y",
		links.ResetPassword("t/1"):    "https://notrecinema.ru/reset-password?token=t%2F1",
		links.Series("id with space"): "https://notrecinema.ru/series/id%20with%20space",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("link = %q, want %q", got, want)
		}
	}
}
