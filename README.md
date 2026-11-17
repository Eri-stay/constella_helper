# Constella Dev Platform

Мікросервісна платформа розробника для проєкту Constella (Лаб №3 і №4). План: [docs/IMPLEMENTATION_PLAN.md](docs/IMPLEMENTATION_PLAN.md).

## Стан

**M0 (каркас)** готовий: спільні пакети `internal/platform/*`, інфраструктура в Docker Compose. Сервіси з'являться з M1.

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
docker compose -f deploy/docker-compose.yml up -d --wait
```

- RabbitMQ: http://localhost:15672 (guest / guest)
- Jaeger: http://localhost:16686

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
