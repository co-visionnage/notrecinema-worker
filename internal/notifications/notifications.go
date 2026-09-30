// Package notifications связывает доменные события с реальной доставкой
// push-уведомлений: вычитывает подписки участников семьи и рассылает через
// internal/webpush. Обработчики событий (internal/handlers) используют
// Notifier, а не webpush.Sender напрямую -- им не нужно знать, как
// вычитывать подписки, какая функция БД для этого нужна и что делать с
// "мёртвой" подпиской.
package notifications

import (
	"context"
	"errors"
	"log/slog"

	"notrecinema/worker/internal/postgres"
	"notrecinema/worker/internal/webpush"
)

type Notifier struct {
	db           *postgres.Pool
	sender       *webpush.Sender
	logger       *slog.Logger
	isConfigured bool
}

// cfg.Configured() == false -- VAPID-ключи не заданы: тот же ранний выход,
// что и ensureConfigured() в notrecinema-app, чтобы окружение без
// настроенного push (например локальная разработка) не заваливало логи
// ошибками отправки на каждое событие.
func NewNotifier(db *postgres.Pool, sender *webpush.Sender, cfg webpush.Config, logger *slog.Logger) *Notifier {
	return &Notifier{db: db, sender: sender, logger: logger, isConfigured: cfg.Configured()}
}

// NotifyFamily отправляет payload всем подпискам участников семьи, кроме
// excludeUserID (обычно это тот, кто своим действием и породил событие --
// не нужно уведомлять человека о его собственном действии). Использует
// get_family_push_subscriptions_system: SECURITY DEFINER функцию из схемы,
// которая не требует app.current_user_id (у воркера в принципе нет
// пользовательского контекста -- он не действует от лица кого-то одного).
func (n *Notifier) NotifyFamily(ctx context.Context, familyID, excludeUserID string, payload webpush.Payload) error {
	return n.NotifyFamilyExcept(ctx, familyID, &excludeUserID, payload)
}

// NotifyFamilyExcept — то же самое, что NotifyFamily, но exclude может быть
// nil: get_family_push_subscriptions_system трактует NULL как "никого не
// исключать" (миграция 0028). Нужно там, где события породил не один
// конкретный пользователь, а фоновая проверка без актора -- например
// internal/seasons при массовом cron-обновлении, в отличие от его же
// разового пользовательского запроса, который исключает самого себя.
func (n *Notifier) NotifyFamilyExcept(ctx context.Context, familyID string, excludeUserID *string, payload webpush.Payload) error {
	if !n.isConfigured {
		return nil
	}

	rows, err := n.db.Query(ctx, `
		SELECT endpoint, p256dh, auth
		FROM public.get_family_push_subscriptions_system($1, $2)
	`, familyID, excludeUserID)
	if err != nil {
		return err
	}
	defer rows.Close()

	var subs []webpush.Subscription
	for rows.Next() {
		var sub webpush.Subscription
		if err := rows.Scan(&sub.Endpoint, &sub.P256dh, &sub.Auth); err != nil {
			return err
		}
		subs = append(subs, sub)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	n.sendAll(ctx, subs, payload)
	return nil
}

// NotifyUser отправляет payload всем подпискам одного пользователя --
// используется там, где событие касается конкретного человека, а не всей
// семьи (например, напоминание о заброшенном сериале). Использует
// get_user_push_subscriptions_system (SECURITY DEFINER, миграция 0011) по
// той же причине, что и NotifyFamily: у воркера нет app.current_user_id.
func (n *Notifier) NotifyUser(ctx context.Context, userID string, payload webpush.Payload) error {
	if !n.isConfigured {
		return nil
	}

	rows, err := n.db.Query(ctx, `
		SELECT endpoint, p256dh, auth
		FROM public.get_user_push_subscriptions_system($1)
	`, userID)
	if err != nil {
		return err
	}
	defer rows.Close()

	var subs []webpush.Subscription
	for rows.Next() {
		var sub webpush.Subscription
		if err := rows.Scan(&sub.Endpoint, &sub.P256dh, &sub.Auth); err != nil {
			return err
		}
		subs = append(subs, sub)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	n.sendAll(ctx, subs, payload)
	return nil
}

func (n *Notifier) sendAll(ctx context.Context, subs []webpush.Subscription, payload webpush.Payload) {
	for _, sub := range subs {
		err := n.sender.Send(ctx, sub, payload)
		if err == nil {
			continue
		}

		// "Мёртвая" подписка -- ожидаемая ситуация (пользователь отписался,
		// очистил данные браузера), не повод шуметь в логах на уровне error.
		// В отличие от notrecinema-app (там такие строки вычищались лениво,
		// только при следующей попытке подписаться, и копились до этого),
		// здесь подписка удаляется сразу -- delete_push_subscription_system
		// (SECURITY DEFINER, миграция 0029) скоупится по endpoint, а не по
		// пользователю, ровно потому что это всё, что известно в точке отказа.
		if errors.Is(err, webpush.ErrDeadSubscription) {
			n.logger.Debug("notifications: подписка больше не существует, удаляем", "endpoint", sub.Endpoint)
			n.deleteDeadSubscription(ctx, sub.Endpoint)
			continue
		}

		n.logger.Error("notifications: не удалось отправить push", "endpoint", sub.Endpoint, "error", err)
	}
}

func (n *Notifier) deleteDeadSubscription(ctx context.Context, endpoint string) {
	rows, err := n.db.Query(ctx, `SELECT public.delete_push_subscription_system($1)`, endpoint)
	if err != nil {
		n.logger.Warn("notifications: не удалось удалить мёртвую подписку", "endpoint", endpoint, "error", err)
		return
	}
	rows.Close()
}
