// Package unsubscribe подписывает ссылки «отписаться от писем».
//
// Ссылку подписывает воркер при отправке письма, а проверяет
// notrecinema-api (internal/notifications). Формат -- единственный договор
// между двумя независимыми модулями; он продублирован там и сверяется
// тестами с общим тестовым вектором:
//
//	token = base64url(userId + "|" + category) + "." + base64url(HMAC-SHA256(secret, base64url(userId + "|" + category)))
//
// В токене нет срока действия: отписка должна работать по любому, даже
// очень старому письму, и она безвредна -- выключает только почту одной
// категории уведомлений.
package unsubscribe

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
)

// Sign возвращает токен отписки пользователя от писем категории.
func Sign(secret []byte, userID, category string) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(userID + "|" + category))

	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
