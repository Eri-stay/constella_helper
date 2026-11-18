# Constella Dev Platform

Мікросервісна платформа розробника для проєкту Constella (Лаб №3 і №4). План: [docs/IMPLEMENTATION_PLAN.md](docs/IMPLEMENTATION_PLAN.md).

## Стан

**M0 (каркас)**, **M1** і **M2** (код) готові.

- M0: спільні пакети `internal/platform/*`, інфраструктура в Docker Compose.
- M1: сервіси `dataprep` (HYG + 14 культур Stellarium → `cosmos.db`), `catalog` (перевірка збірки, пошук зір і сузір'їв), `gateway` (токен, проксі, `/api/overview`, SSE) і UI з вкладками «Збірки» та «Перевірка».
- M2: `telemetry` (аудит, логи клієнтів, Sentry за `SENTRY_DSN`) і `release` (сага класичної публікації з відкатом, `/updates/version.json`); вкладки «Релізи» та «Аудит і логи».
- Далі: M3 (режим подій, брокер, вкладка «Збої»).

| Сервіс | Порт | Що робить |
|---|---|---|
| `gateway` | 8080 | UI, `/api/*`, потік подій |
| `dataprep` | 8081 | збірка `cosmos-<id>.db` у фоні |
| `catalog` | 8082 | вердикт перевірки, пошук, резервування збірок |
| `release` | 8083 | публікація релізу, `version.json` |
| `telemetry` | 8084 | аудит, логи клієнтів |

Нові спільні пакети: `internal/domain/catalog` (чисті правила: набір даних, хеш, валідація) і `internal/cosmosdb` (формат файлу `cosmos.db`).

| Пакет | Призначення |
|---|---|
| `events` | конверт події, типи подій (= routing keys) |
| `mq` | RabbitMQ (з Лаб №2): publisher confirms, DLX/DLQ, перепідключення, OTel у заголовках |
| `outbox` / `inbox` | transactional outbox + relay; ідемпотентні споживачі |
| `sqlitex` | SQLite без CGO (WAL, `synchronous=FULL`), транзакції |
| `httpx` | middleware (otel, recover, log, auth), JSON-помилки з кодами, клієнт із timeout, retry, circuit breaker |
| `otelx`, `logx`, `envx`, `fault` | трасування в Jaeger, JSON-логи з `trace_id`, конфіг з env, інжекція збоїв |

## Запуск

```bash
docker compose -f deploy/docker-compose.yml up --build -d --wait
```

- UI: http://localhost:8080 (токен `dev-token`, змінюється через `PLATFORM_TOKEN`)
- RabbitMQ: http://localhost:15672 (guest / guest)
- Jaeger: http://localhost:16686

`dataprep` монтує HYG з `../../data` (тобто `constella/data/hyg_v42.csv`); інший каталог задається змінною `HYG_DIR`. У UI: «Збірки» → «Запустити збірку» → за 3–4 с збірка `готово`, вердикт `перевірено`; вкладка «Перевірка» показує звіт, пошук (`Сіріус`, `HIP 32349`) і схеми сузір'їв.

Тест пайплайна на реальних даних (пропускається, якщо HYG немає; шлях задається `CONSTELLA_HYG`):

```bash
go test -count=1 -v -run Real ./services/dataprep/internal/adapters/
```

Перевірка всього ланцюжка (outbox → RabbitMQ → inbox → трейс у Jaeger):

```bash
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317 go run ./tools/smoke
```

Тести:

```bash
go test ./...
```

```bash
CONSTELLA_TEST_AMQP=amqp://guest:guest@localhost:5672/ go test -count=1 ./internal/platform/mq/
```
