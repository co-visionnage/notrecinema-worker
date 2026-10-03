// Package emails собирает и отрисовывает письма notrecinema.
//
// Все письма проходят через один макет (templates/layout.*.tmpl) в стиле
// сайта -- нео-брутализм: чёрные рамки в 4px, жёсткая «тень», жёлтый,
// лаймовый и розовый акценты (те же цвета, что у Tailwind-классов
// bg-yellow-400 / bg-lime-500 и розового курсора на фронтенде). Компоненты
// фронтенда напрямую в письмо не переносятся: почтовые клиенты не
// понимают ни Tailwind, ни CSS-переменные, ни клиентские React-компоненты,
// поэтому тот же вид собирается на таблицах и inline-стилях.
//
// Письмо описывается данными (Content), а не своим HTML: тексты и
// структура живут в builders.go, вид -- в одном макете. Так все письма
// выглядят одинаково, а правка дизайна делается в одном месте.
package emails

import (
	"bytes"
	"embed"
	"fmt"
	htmltemplate "html/template"
	texttemplate "text/template"
)

// Accent -- цветовой акцент письма (кнопка, плашка, полоска над заголовком).
type Accent string

const (
	AccentYellow Accent = "yellow"
	AccentLime   Accent = "lime"
	AccentPink   Accent = "pink"
)

var accentColors = map[Accent]htmltemplate.CSS{
	AccentYellow: "#facc15",
	AccentLime:   "#84cc16",
	AccentPink:   "#ec4899",
}

// Button -- главная кнопка письма. Под ней всегда выводится сама ссылка
// текстом: часть почтовых клиентов не показывает кнопки.
type Button struct {
	Label string
	URL   string
}

// Highlight -- цветная плашка с главным фактом письма (название сериала и т.п.).
type Highlight struct {
	Label string
	Title string
	Text  string
}

// Section -- блок письма со списком: заголовок и пункты (например, «Что
// посмотрели» и названия сериалов).
type Section struct {
	Title string
	Items []string
}

// Content -- всё содержимое письма.
type Content struct {
	Subject    string
	Preheader  string // короткий текст, который клиент показывает рядом с темой
	Heading    string
	Greeting   string
	Paragraphs []string
	Highlight  *Highlight
	Sections   []Section
	Button     *Button
	Footnote   string // мелкий текст под основным (напр. «если это были не вы»)
	Reason     string // почему человек получил письмо (подвал)
	// UnsubscribeURL и PreferencesURL заполняются только у писем-уведомлений
	// (их можно отключить): в подвале появляются ссылки «отписаться» и
	// «настройки уведомлений». У транзакционных писем (подтверждение,
	// сброс пароля, безопасность, приглашение) их нет -- от них нельзя
	// отказаться.
	UnsubscribeURL string
	PreferencesURL string
	Accent         Accent
}

// Rendered -- готовое письмо: HTML и текстовая версия.
type Rendered struct {
	Subject string
	HTML    string
	Text    string
}

//go:embed templates/layout.html.tmpl templates/layout.txt.tmpl
var templateFS embed.FS

var (
	htmlLayout = htmltemplate.Must(htmltemplate.ParseFS(templateFS, "templates/layout.html.tmpl"))
	textLayout = texttemplate.Must(texttemplate.ParseFS(templateFS, "templates/layout.txt.tmpl"))
)

const defaultReason = "Вы получили это письмо, потому что у вас есть аккаунт в notrecinema."

type htmlData struct {
	Content
	AccentColor htmltemplate.CSS
}

// Render отрисовывает письмо в HTML и обычный текст.
func Render(c Content) (Rendered, error) {
	if c.Subject == "" {
		return Rendered{}, fmt.Errorf("emails: у письма нет темы")
	}
	if c.Accent == "" {
		c.Accent = AccentYellow
	}
	color, ok := accentColors[c.Accent]
	if !ok {
		return Rendered{}, fmt.Errorf("emails: неизвестный акцент %q", c.Accent)
	}
	if c.Reason == "" {
		c.Reason = defaultReason
	}

	var htmlBuf bytes.Buffer
	if err := htmlLayout.Execute(&htmlBuf, htmlData{Content: c, AccentColor: color}); err != nil {
		return Rendered{}, fmt.Errorf("emails: html: %w", err)
	}

	var textBuf bytes.Buffer
	if err := textLayout.Execute(&textBuf, c); err != nil {
		return Rendered{}, fmt.Errorf("emails: text: %w", err)
	}

	return Rendered{Subject: c.Subject, HTML: htmlBuf.String(), Text: textBuf.String()}, nil
}
