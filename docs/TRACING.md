# Трасування (Лаб №3, завдання 4)

Усі сервіси надсилають спани в Jaeger через OTLP (`OTEL_EXPORTER_OTLP_ENDPOINT=http://jaeger:4317`). Контекст W3C `traceparent` передається:

| Межа | Як |
|---|---|
| UI → gateway → сервіс | HTTP-заголовок: `otelhttp` на сервері gateway, у reverse proxy і в клієнті `httpx` |
| сервіс → сервіс | HTTP-заголовок через `httpx.Client` (timeout, retry, breaker) |
| сервіс → RabbitMQ → споживач | заголовки AMQP: `mq.Publish` вставляє, `mq.Consume` витягує |
| outbox → relay | `traceparent` лежить у рядку outbox, тому relay продовжує той самий трейс |
| запит → воркер саги (режим подій) | `traceparent` лежить у події `requested`, воркер продовжує трейс запиту |

Атрибути спанів: `build.id`, `release.id`, `mode`, `attempt` (номер спроби кроку), `failed.step`, `correlation.id` (у споживачі `telemetry`). Невдалий крок позначається статусом Error (`otel.status_code=ERROR`, червоний у Jaeger).

Спани, які додає код саги (`release`): `release publish` (класичний), `release request` і `release saga` (режим подій), `saga reserve|switch|mark_released|audit`, `saga compensate` з дочірніми `compensate restore manifest` і `compensate cancel reservation`.

## Три обов'язкові трейси

Для кожного: запустіть сценарій, потім перевірте трейс командою (вона друкує дерево спанів і завершується кодом 1, якщо чогось бракує) і зробіть скріншот у Jaeger (`http://localhost:16686`, посилання на трейс друкується, а в UI є «↗» біля релізу).

1. **Класичний режим** (`classic`)
   - UI: «Релізи» → «Опублікувати (класично)».
   - `go run ./tools/tracecheck -scenario classic`
   - Очікується: `gateway → release → catalog` (reserve, mark released) → `telemetry` (аудит) в одному трейсі, відповідь після всіх кроків.
2. **Режим подій** (`eda`)
   - UI: «Релізи» → «Опублікувати (події)».
   - `go run ./tools/tracecheck -scenario eda`
   - Очікується: коротка відповідь `202` у `gateway/release`, далі в **тому самому трейсі** воркер (`release saga`, кроки, виклики `catalog`), `publish release.published` і `consume release.published` у `telemetry` і `gateway`.
3. **Збій: `telemetry` зупинено**
   - Класичний: `docker compose -f deploy/docker-compose.yml stop telemetry`, потім «Опублікувати (класично)» → `502`, потім `go run ./tools/tracecheck -scenario classic-fail`. Очікується червоний `saga audit` і далі `saga compensate` з відкатом.
   - Режим подій: з тим самим зупиненим `telemetry` опублікувати «(події)»: реліз проходить (`eda`-трейс без помилок), а `consume release.published` у `telemetry` з'являється після `docker compose start telemetry`, коли черга дочитується.

## Корисне в Jaeger

- Пошук за тегом: `release.id=<id>`, `build.id=<id>`, `mode=eda`, `error=true`.
- Сервіс `release`, операція `release publish` або `release saga`: список трейсів публікацій.
- У трейсі збірки (`dataprep build`) видно етапи `load sources`, `assemble dataset`, `write cosmos.db`.
