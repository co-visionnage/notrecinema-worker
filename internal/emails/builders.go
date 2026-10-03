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
	settingsPath       = "/settings"
	invitePath         = "/invite"
	unsubscribePath    = "/unsubscribe"
	unsubscribeAPIPath = "/api/v1/unsubscribe"
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

// Settings -- страница настроек аккаунта (безопасность, уведомления).
func (l Links) Settings() string { return l.base + settingsPath }

func (l Links) Invite(token string) string {
	return l.base + invitePath + "?token=" + url.QueryEscape(token)
}

// Unsubscribe -- страница на фронтенде, которую открывает человек по ссылке
// в подвале письма.
func (l Links) Unsubscribe(token string) string {
	return l.base + unsubscribePath + "?token=" + url.QueryEscape(token)
}

// UnsubscribeAPI -- адрес, на который почтовый клиент шлёт POST для
// отписки в один клик (заголовок List-Unsubscribe, RFC 8058).
func (l Links) UnsubscribeAPI(token string) string {
	return l.base + unsubscribeAPIPath + "?token=" + url.QueryEscape(token)
}

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

// BackupCodeUsed -- уведомление: вход выполнен резервным кодом. Если кодов
// почти не осталось, письмо прямо просит выпустить новые.
func BackupCodeUsed(name string, remaining int, settingsURL string) Content {
	paragraphs := []string{
		"Только что в ваш аккаунт в notrecinema вошли с резервным кодом двухфакторной аутентификации. Этот код больше не действует.",
		fmt.Sprintf("Осталось неиспользованных резервных кодов: %d.", remaining),
	}
	if remaining <= 2 {
		paragraphs = append(paragraphs, "Кодов осталось совсем мало. Выпустите новый набор в настройках безопасности, пока не потеряли доступ к аккаунту.")
	}
	paragraphs = append(paragraphs, "Если это были не вы, срочно смените пароль и перевыпустите резервные коды.")

	return Content{
		Subject:    "В аккаунт вошли с резервным кодом",
		Preheader:  fmt.Sprintf("Осталось резервных кодов: %d", remaining),
		Heading:    "Использован резервный код",
		Greeting:   greeting(name),
		Paragraphs: paragraphs,
		Button:     &Button{Label: "Настройки безопасности", URL: settingsURL},
		Reason:     "Это уведомление о безопасности вашего аккаунта в notrecinema.",
		Accent:     AccentPink,
	}
}

// BackupCodesRegenerated -- уведомление: резервные коды выпущены заново.
func BackupCodesRegenerated(name, settingsURL string) Content {
	return Content{
		Subject:   "Резервные коды 2FA выпущены заново",
		Preheader: "Старые резервные коды больше не действуют",
		Heading:   "Новые резервные коды",
		Greeting:  greeting(name),
		Paragraphs: []string{
			"Для вашего аккаунта в notrecinema выпущен новый набор резервных кодов двухфакторной аутентификации. Все прежние коды перестали действовать.",
			"Если это были не вы, срочно смените пароль.",
		},
		Button: &Button{Label: "Настройки безопасности", URL: settingsURL},
		Reason: "Это уведомление о безопасности вашего аккаунта в notrecinema.",
		Accent: AccentPink,
	}
}

// Welcome -- приветствие после подтверждения адреса.
func Welcome(name, homeURL string) Content {
	return Content{
		Subject:   "Добро пожаловать в notrecinema",
		Preheader: "Создайте семью и начните отслеживать сериалы вместе",
		Heading:   "Добро пожаловать!",
		Greeting:  greeting(name),
		Paragraphs: []string{
			"Рады, что вы с нами. notrecinema — это общий список сериалов для вашей семьи: добавляйте, что хотите посмотреть, отмечайте серии и решайте, что смотреть сегодня, вместе.",
			"Начните с малого: создайте семью и пригласите близких — по коду или по email.",
		},
		Button: &Button{Label: "Открыть notrecinema", URL: homeURL},
		Reason: "Вы получили это письмо, потому что подтвердили email в notrecinema.",
		Accent: AccentLime,
	}
}

// FamilyInvitation -- приглашение в семью (ссылка действует 7 дней).
func FamilyInvitation(inviterName, familyName, acceptURL string) Content {
	return Content{
		Subject:   inviterName + " приглашает вас в семью «" + familyName + "»",
		Preheader: "Присоединяйтесь к общему списку сериалов",
		Heading:   "Вас приглашают в семью",
		Greeting:  "Здравствуйте!",
		Paragraphs: []string{
			inviterName + " приглашает вас присоединиться к семье в notrecinema — общему списку сериалов, где вы вместе отмечаете, что посмотрели, и выбираете, что смотреть дальше.",
			"Чтобы принять приглашение, войдите в аккаунт или зарегистрируйтесь, а затем нажмите кнопку. Ссылка действует 7 дней.",
		},
		Highlight: &Highlight{Label: "Семья", Title: familyName, Text: "Приглашает: " + inviterName},
		Button:    &Button{Label: "Принять приглашение", URL: acceptURL},
		Footnote:  "Если вы не ждали этого письма, просто проигнорируйте его: без вашего согласия никто в семью не добавляется.",
		Reason:    "Вы получили это письмо, потому что вас пригласили в notrecinema.",
		Accent:    AccentYellow,
	}
}
