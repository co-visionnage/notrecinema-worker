package emails

import (
	"fmt"
	"net/url"
	"strings"
)

// Ссылки на страницы фронтенда, на которые ведут письма. Страницы
// /verify-email и /reset-password принимают токен из query-параметра и
// вызывают соответствующие эндпоинты API (POST /api/v1/auth/verify-email,
// POST /api/v1/auth/password-reset/confirm).
const (
	verifyEmailPath    = "/verify-email"
	resetPasswordPath  = "/reset-password"
	forgotPasswordPath = "/forgot-password"
)

// Links строит ссылки для писем от адреса фронтенда.
type Links struct {
	base string
}

func NewLinks(appURL string) Links {
	return Links{base: strings.TrimRight(appURL, "/")}
}

func (l Links) Home() string { return l.base + "/" }

func (l Links) VerifyEmail(token string) string {
	return l.base + verifyEmailPath + "?token=" + url.QueryEscape(token)
}

func (l Links) ResetPassword(token string) string {
	return l.base + resetPasswordPath + "?token=" + url.QueryEscape(token)
}

func (l Links) ForgotPassword() string { return l.base + forgotPasswordPath }

func (l Links) Series(seriesID string) string {
	return l.base + "/series/" + url.PathEscape(seriesID)
}

func greeting(name string) string {
	if name = strings.TrimSpace(name); name != "" {
		return "Привет, " + name + "!"
	}
	return "Здравствуйте!"
}

// pluralRu подбирает форму слова для числа: 1 сезон, 2 сезона, 5 сезонов.
func pluralRu(n int, one, few, many string) string {
	n10, n100 := n%10, n%100
	switch {
	case n10 == 1 && n100 != 11:
		return one
	case n10 >= 2 && n10 <= 4 && (n100 < 10 || n100 >= 20):
		return few
	default:
		return many
	}
}

// VerifyEmail -- письмо со ссылкой подтверждения адреса (действует 24 часа).
func VerifyEmail(name, verifyURL string) Content {
	return Content{
		Subject:   "Подтвердите email в notrecinema",
		Preheader: "Одно нажатие — и аккаунт подтверждён",
		Heading:   "Подтвердите email",
		Greeting:  greeting(name),
		Paragraphs: []string{
			"Вы зарегистрировались в notrecinema. Подтвердите, что этот адрес принадлежит вам, — нажмите кнопку ниже.",
			"Ссылка действует 24 часа.",
		},
		Button:   &Button{Label: "Подтвердить email", URL: verifyURL},
		Footnote: "Если вы не регистрировались в notrecinema, просто проигнорируйте это письмо — без подтверждения аккаунт ничего не сможет.",
		Accent:   AccentLime,
	}
}

// PasswordReset -- письмо со ссылкой на сброс пароля (действует 1 час).
func PasswordReset(name, resetURL string) Content {
	return Content{
		Subject:   "Сброс пароля в notrecinema",
		Preheader: "Ссылка для создания нового пароля действует 1 час",
		Heading:   "Сброс пароля",
		Greeting:  greeting(name),
		Paragraphs: []string{
			"Мы получили запрос на сброс пароля для вашего аккаунта. Нажмите кнопку, чтобы задать новый пароль.",
			"Ссылка действует 1 час и работает один раз. После смены пароля вы выйдете из аккаунта на всех устройствах.",
		},
		Button:   &Button{Label: "Задать новый пароль", URL: resetURL},
		Footnote: "Если вы не запрашивали сброс, ничего делать не нужно: пароль останется прежним.",
		Accent:   AccentYellow,
	}
}

// PasswordChanged -- уведомление о смене пароля.
func PasswordChanged(name, forgotURL string) Content {
	return Content{
		Subject:   "Пароль в notrecinema изменён",
		Preheader: "Если это были не вы — сбросьте пароль",
		Heading:   "Пароль изменён",
		Greeting:  greeting(name),
		Paragraphs: []string{
			"Пароль от вашего аккаунта в notrecinema был изменён. Во всех остальных местах вы вышли из аккаунта.",
			"Если это сделали вы, больше ничего делать не нужно.",
			"Если это были не вы, немедленно сбросьте пароль заново.",
		},
		Button: &Button{Label: "Сбросить пароль", URL: forgotURL},
		Reason: "Это уведомление о безопасности вашего аккаунта в notrecinema.",
		Accent: AccentPink,
	}
}

// TwoFactorEnabled -- уведомление о включении двухфакторной аутентификации.
func TwoFactorEnabled(name string) Content {
	return Content{
		Subject:   "Двухфакторная аутентификация включена",
		Preheader: "Теперь при входе нужен код из приложения",
		Heading:   "Двухфакторная аутентификация включена",
		Greeting:  greeting(name),
		Paragraphs: []string{
			"К вашему аккаунту в notrecinema подключена двухфакторная аутентификация: при входе, кроме пароля, нужен код из приложения-аутентификатора.",
			"Если это были не вы, смените пароль и свяжитесь с нами.",
		},
		Reason: "Это уведомление о безопасности вашего аккаунта в notrecinema.",
		Accent: AccentLime,
	}
}

// TwoFactorDisabled -- уведомление об отключении двухфакторной аутентификации.
func TwoFactorDisabled(name, forgotURL string) Content {
	return Content{
		Subject:   "Двухфакторная аутентификация отключена",
		Preheader: "Если это были не вы — смените пароль",
		Heading:   "Двухфакторная аутентификация отключена",
		Greeting:  greeting(name),
		Paragraphs: []string{
			"Двухфакторная аутентификация для вашего аккаунта в notrecinema отключена. Теперь для входа достаточно одного пароля.",
			"Если вы этого не делали, сбросьте пароль, а затем снова включите двухфакторную аутентификацию в настройках.",
		},
		Button: &Button{Label: "Сбросить пароль", URL: forgotURL},
		Reason: "Это уведомление о безопасности вашего аккаунта в notrecinema.",
		Accent: AccentPink,
	}
}

// AccountDeleted -- прощальное письмо после удаления аккаунта.
func AccountDeleted(name string) Content {
	return Content{
		Subject:   "Ваш аккаунт notrecinema удалён",
		Preheader: "Данные аккаунта удалены безвозвратно",
		Heading:   "Аккаунт удалён",
		Greeting:  greeting(name),
		Paragraphs: []string{
			"Ваш аккаунт в notrecinema и связанные с ним данные удалены. Восстановить их нельзя.",
			"Спасибо, что были с нами. Если захотите вернуться, можно зарегистрироваться заново.",
		},
		Footnote: "Если аккаунт удалили не вы, ответьте на это письмо или напишите нам как можно скорее.",
		Reason:   "Это последнее письмо: больше мы вам писать не будем.",
		Accent:   AccentPink,
	}
}

// SeriesAdded -- в семью добавили сериал.
func SeriesAdded(name, title, seriesURL string) Content {
	return Content{
		Subject:   "Новый сериал в списке: «" + title + "»",
		Preheader: "В вашей семье добавили «" + title + "»",
		Heading:   "Новый сериал в списке",
		Greeting:  greeting(name),
		Paragraphs: []string{
			"В списке вашей семьи появился новый сериал.",
		},
		Highlight: &Highlight{Label: "Добавлено", Title: title},
		Button:    &Button{Label: "Открыть сериал", URL: seriesURL},
		Reason:    "Вы получили это письмо, потому что состоите в семье в notrecinema.",
		Accent:    AccentLime,
	}
}

// SeasonUpdated -- у сериала вышел новый сезон.
func SeasonUpdated(name, title string, totalSeasons int, homeURL string) Content {
	seasons := fmt.Sprintf("%d %s", totalSeasons, pluralRu(totalSeasons, "сезон", "сезона", "сезонов"))
	return Content{
		Subject:   "Вышел новый сезон: «" + title + "»",
		Preheader: "У «" + title + "» теперь " + seasons,
		Heading:   "Вышел новый сезон!",
		Greeting:  greeting(name),
		Paragraphs: []string{
			"Мы заметили, что у сериала из вашего списка вышел новый сезон.",
		},
		Highlight: &Highlight{Label: "Теперь в сериале", Title: title, Text: seasons},
		Button:    &Button{Label: "Открыть notrecinema", URL: homeURL},
		Reason:    "Вы получили это письмо, потому что состоите в семье в notrecinema.",
		Accent:    AccentYellow,
	}
}

// ProgressStale -- напоминание о заброшенном сериале.
func ProgressStale(name, title string, season, episode int, seriesURL string) Content {
	return Content{
		Subject:   "Давно не продолжали «" + title + "»",
		Preheader: fmt.Sprintf("Вы остановились на сезоне %d, серии %d", season, episode),
		Heading:   "Давно не продолжали!",
		Greeting:  greeting(name),
		Paragraphs: []string{
			"Вы давно не возвращались к сериалу — он ждёт продолжения.",
		},
		Highlight: &Highlight{
			Label: "Вы остановились",
			Title: title,
			Text:  fmt.Sprintf("Сезон %d, серия %d", season, episode),
		},
		Button: &Button{Label: "Продолжить смотреть", URL: seriesURL},
		Reason: "Вы получили это письмо, потому что отслеживаете этот сериал в notrecinema.",
		Accent: AccentPink,
	}
}
