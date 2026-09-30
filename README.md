# notrecinema-worker

Фоновый процесс, потребляющий доменные события из NATS JetStream, которые
`notrecinema-api` публикует через transactional outbox
(`notrecinema-api/internal/outbox`). Отдельный процесс, а не горутина
внутри API — обработка событий должна масштабироваться, деплоиться и падать
независимо от HTTP-трафика.

## Как это работает

```
notrecinema-api                         notrecinema-worker
     │                                          │
     │ BEGIN                                    │
     │ INSERT family_members                    │
     │ INSERT outbox_events   ← одна транзакция  │
     │ COMMIT                                    │
     │                                            │
     │ outbox.Poller (тикер, отдельная горутина) │
     │ публикует в NATS JetStream ───────────────►│
     │                                            │ идемпотентность:
     │                                            │ INSERT INTO worker_processed_events
     │                                            │ (PK-конфликт = уже обработано)
     │                                            │
     │                                            │ обработчик (internal/handlers)
     │                                            │ ack / nak
```

- **Идемпотентность.** JetStream гарантирует только at-least-once —
  сообщение может прийти повторно. Перед вызовом обработчика воркер
  пытается вставить `event_id` в `worker_processed_events`; конфликт
  PRIMARY KEY означает "уже обработано", обработчик не вызывается
  повторно, сообщение просто подтверждается (ack).
- **Ретраи с backoff.** Если обработчик вернул ошибку — `nak`, и JetStream
  передоставит сообщение по экспоненциальному backoff
  (`internal/consumer.Consumer.Run`, поле `BackOff`).
- **Dead letter.** Отдельный стрим `NOTRECINEMA_DEADLETTER` (создаёт сам
  воркер при старте, `internal/eventbus.EnsureDeadLetterStream`), subject'ы
  вида `dead-letter.<eventType>` — не `notrecinema.dead-letter.*`, чтобы не
  попасть обратно под `FilterSubject: "notrecinema.>"` основного консьюмера
  и не превратиться в новое событие для тех же обработчиков. На последней
  попытке редоставки (`msg.Metadata().NumDelivered >= MaxDeliver`)
  `handleMessage` публикует исходный envelope в dead-letter стрим и
  завершает сообщение через `Term` (а не даёт ему просто "зависнуть" после
  исчерпания `MaxDeliver`). Видно не только через `consumer info`
  (`:8222`), но и напрямую — сообщения реально лежат в стриме 30 дней
  (`MaxAge`).

## Notifications (web push)

Зарегистрированные обработчики (`family.member.joined`,
`movie.added/watched/rated`, `poll.created/closed`, `watch_event.created`,
`progress.stale`, `season.updated`, `series.bulk_added`) не просто логируют
событие — они реально уведомляют семью (или одного пользователя, см.
ниже) через `internal/notifications` + `internal/webpush`:

```
handler ──► notifications.Notifier.NotifyFamily(familyID, excludeUserID, payload)
                  │
                  ├── SELECT ... FROM get_family_push_subscriptions_system($1, $2)
                  │   (SECURITY DEFINER -- у воркера нет app.current_user_id,
                  │    поэтому обычные RLS-функции не подходят)
                  │
                  └── webpush.Sender.Send(...) на каждую подписку
                      (VAPID JWT + aes128gcm, RFC8291)
```

`excludeUserID` — обычно тот, чьим действием событие и порождено: не нужно
уведомлять человека о его собственном действии. Без `VAPID_PUBLIC_KEY`/
`VAPID_PRIVATE_KEY`/`VAPID_SUBJECT` уведомления молча отключены (см.
`webpush.Config.Configured()`), а не падают.

`progress.stale` (публикует `notrecinema-api/internal/nudges`, когда кто-то
давно не продолжал сериал) — единственное событие, адресованное не всей
семье, а одному пользователю: обработчик зовёт `Notifier.NotifyUser`
(вычитывает подписки через `get_user_push_subscriptions_system`, миграция
0011), а не `NotifyFamily`.

`season.updated` (публикует `notrecinema-api/internal/seasons`, когда у
привязанного к Kinopoisk/OMDb сериала выросло `total_seasons`) — может
прийти и с исключением, и без: на cron-обновлении (`ExcludeUserID == nil`)
уведомляются все участники семьи, на разовой проверке по требованию
(`POST .../season-updates/check`) — все, кроме того, кто её запустил. Для
этого `Notifier.NotifyFamily` теперь обёртка над `NotifyFamilyExcept`,
которая умеет передать `exclude_user_id = NULL` в
`get_family_push_subscriptions_system` (миграция 0028).

`series.bulk_added` (публикует `notrecinema-api/internal/series.CreateBulk`
после массового импорта -- Trakt watchlist/IMDb CSV/поиск) — одно
агрегированное уведомление на весь импорт ("Добавлено N сериалов"), а не
одно на каждый сериал, ровно как `notifyFamilyOfEvent` в
`addSeriesBulkAction` оригинала.

"Мёртвая" подписка (браузер ответил 404/410 — пользователь отписался,
очистил данные) логируется на уровне debug и удаляется сразу же через
`delete_push_subscription_system` (SECURITY DEFINER, миграция 0029,
скоупится по `endpoint`, а не по пользователю — это всё, что известно в
точке отказа). В отличие от notrecinema-app, где такие строки копились до
следующей попытки подписаться, здесь `push_subscriptions` не растёт
мусором.

## Добавление обработчика нового типа события

```go
c.Handle("movie.added", handlers.MovieAdded(logger, notifier))
```

в `cmd/worker/main.go`, и сам обработчик — новый файл в `internal/handlers`
по образцу `series.go`. `notifier.NotifyFamily` доступен любому
обработчику, отдельно настраивать его не нужно.

## Запуск

Обычно поднимается вместе с остальной платформой из
`notrecinema-api/docker-compose.yml` (сервис `worker`, использует
`notrecinema-api/.env`). Для запуска в одиночку — см. `.env.example` здесь.

## Тесты

```bash
go test ./...                                       # юнит-тесты
DATABASE_URL=... go test -tags integration ./...    # идемпотентность и notifications против настоящего Postgres
```

`internal/webpush` тестируется настоящей криптографией: генерирует
реальную пару ключей P-256 (то, что делает браузер в
`pushManager.subscribe()`) и реальные VAPID-ключи, затем проверяет через
httptest, что запрос дошёл с корректными `Authorization` (VAPID JWT) и
`Content-Encoding: aes128gcm` — не заглушка, а весь протокол целиком.
`internal/notifications` интеграционным тестом подтверждает, что
`get_family_push_subscriptions_system` реально исключает автора события и
находит остальных участников семьи.

## Observability

`GET :8081/healthz` — liveness (всегда 200, если процесс жив).
`GET :8081/readyz` — readiness (падает, если соединение с NATS разорвано).
`GET :8081/metrics` — `worker_jobs_total{event_type,outcome}`,
`worker_job_duration_seconds{event_type}`. Трейсинг — OpenTelemetry, span
`worker.process` продолжает trace, начатый в `notrecinema-api` на
HTTP-запросе (контекст передаётся через заголовки NATS-сообщения).

## CI/CD

`.gitlab-ci.yml` — тот же набор джобов, что у `notrecinema-api` (lint,
юнит- и интеграционные тесты, Trivy, Kaniko, ручной деплой). См. README
`notrecinema-api` за списком нужных CI/CD-переменных.
