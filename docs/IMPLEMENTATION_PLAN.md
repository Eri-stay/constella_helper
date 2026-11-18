# Constella Dev Platform: план імплементації (Лаб №3)

Мікросервісна платформа розробника для проєкту Constella. Покриває частину фаз P0, P3 і P7 з `constella/docs/PLAN.md`. Методичка: `text_info_context/lab3_manual.txt`.

## 0. Відповідність вимогам Лаб №3

| Вимога методички | Чим закривається |
|---|---|
| Результат: опис архітектури і компонентів | `docs/ARCHITECTURE.md`, розділи 2–4 цього плану, звіт |
| Результат: реалізовані сервіси | 6 сервісів і gateway у Docker Compose |
| 1. Об'єкти домену, сервіси, межі, способи зв'язку, підходи (класичний, event-driven, хореографія) | розділи 2–3: сервіси, події, стилі зв'язку |
| 2. Одна бізнес-функція двома способами; затримки, складність розробки, відновлення після помилок | `PublishRelease` у режимах A і B (розділ 4), `tools/bench`, `docs/EXPERIMENTS.md` (p50/p95, частка помилок, LOC і компоненти) |
| 3. Брокер із Лаб №2 між сервісами | `internal/platform/mq` (копія `lab2/internal/rabbitmq` плюс outbox, inbox, DLQ) |
| 4. OpenTelemetry і Jaeger; трейси обох варіантів і сценарію зі збоєм | M4: пропагація через HTTP, gRPC, AMQP; три обов'язкові трейси |
| 5. Клієнтський UI для основних бізнес-сценаріїв | `web/` за gateway, 6 вкладок (розділ 6) |
| Теорія: API Gateway, Database-per-Service, API Composition / CQRS, Circuit Breaker, конфіг на сервіс, discovery | gateway, SQLite на сервіс, `/api/overview`, `gobreaker`, env-конфіг, DNS-імена Compose |

## 1. Мета

Платформа розробника Constella:
- збирає `cosmos.db` із HYG і культур сузір'їв;
- дає її перевірити;
- публікує реліз (`version.json` + SHA-256);
- веде аудит і приймає логи клієнтів;
- проганяє дослідницькі експерименти з генерації сузір'їв.

Шкільний застосунок лишається модульним монолітом (як у `PLAN.md`), а мікросервіси живуть лише в платформі розробника. Це протиставлення буде у звіті.

Чистий доменний код (`internal/domain/*`) за шляхами збігається з `constella/server/internal/domain/*` (модуль теж `constella`). Тому його можна перенести в диплом без змін імпортів.

## 2. Сервіси

| Сервіс | Порт | Фаза PLAN | Власні дані | Відповідальність |
|---|---|---|---|---|
| `gateway` | 8080 | усі | — | REST-вхід, bearer-токен, маршрутизація, API Composition `/api/overview`, SSE `/api/stream`, статичний UI |
| `dataprep` | 8081 | P0 | `dataprep.db` (builds), том `artifacts/` | ETL: HYG CSV + культури → `cosmos-<build>.db`, SHA-256; асинхронна задача |
| `catalog` | 8082, gRPC 9082 | P0 | `catalog.db` (звіти, резерви) | валідація збірки, пошук зір і сузір'їв, Reserve / MarkReleased, `StreamStars` |
| `release` | 8083 | P7 | `release.db` (event store агрегату `Release`, outbox), том `releases/` | сага публікації, `version.json`, роздача артефактів |
| `telemetry` | 8084 | P0, P7 | `telemetry.db` (audit, client_logs, inbox) | локальний аудит усіх подій у SQLite, `POST /v1/client-log` зі scrubber; `ErrorSink`: Noop за замовчуванням, Sentry лише за `SENTRY_DSN` |
| `research` | 8085 | P3 | `research.db` (runs, results) | генерація (кластеризація + MST) і порівняння (Jaccard) з культурою; свіпи за seed; CSV для диплома. **Останній етап, mock-дані допустимі** |

Інфраструктура: RabbitMQ (`rabbitmq:4-management`, 5672/15672) і Jaeger v2 (`jaegertracing/jaeger:2.12.0`, UI 16686, OTLP 4317).

```
 Admin UI (web/) ──REST/SSE──► [gateway]
                                  │ REST (sync, timeout + retry + breaker)
     ┌───────────┬───────────┬────┴──────┬────────────┐
 [dataprep]  [catalog]  [research]  [release]  [telemetry]
                 ▲ gRPC StreamStars ─┘
 усі ◄══ RabbitMQ topic `constella.platform` ══► усі
 усі ──► OpenTelemetry (OTLP) ──► Jaeger
```

## 3. Стилі зв'язку

| Взаємодія | Стиль | Чому |
|---|---|---|
| UI → gateway → сервіси | REST/JSON, синхронно | запити й перегляди |
| Запуск збірки або дослідження | async request-reply: `202` + id, прогрес через SSE | довгі задачі |
| `dataprep` → `catalog` | хореографія: `dataprep.build.completed` | ETL не знає споживачів |
| `PublishRelease` | **оркестрована сага** в `release` (sync-виклики до `catalog` через breaker, retry, timeout) і компенсація | порядок, відкат, видимий стан |
| Після релізу: аудит, SSE, research | хореографія: `release.published` | побічні ефекти не блокують реліз |
| `research` → `catalog` | gRPC server-streaming | великий типізований потік зір |

**Подія** (`internal/platform/events`):
- конверт: `{id, type, occurred_at, producer, correlation_id, causation_id, schema_ver, data}`;
- `traceparent` передається в AMQP headers.

**Брокер**:
- exchange `constella.platform` (topic), DLX `constella.dlx`, черги `<svc>.q` і `<svc>.dlq`.

| Черга | Ключі |
|---|---|
| `catalog.q` | `dataprep.build.completed` |
| `release.q` | `catalog.build.*` |
| `research.q` | `release.published` |
| `telemetry.q` | `#` |
| `gateway.sse` (exclusive) | `#` |

**Події**:
- `dataprep.build.started|completed|failed`;
- `catalog.build.validated|rejected`;
- `release.requested|published|failed`;
- `research.run.completed|failed`.

**Надійність**:
- transactional outbox: подія пишеться в ту ж транзакцію SQLite, що й стан, потім relay-горутина її публікує;
- inbox для дедуплікації за `event.id`;
- manual ack, retry, DLQ.

## 4. `PublishRelease`: два режими (Завдання 2)

Агрегат `Release` побудовано за патерном лаб2: команда повертає подію, `Apply` змінює стан. Store стоїть першим, потім dispatch. Статуси: `Requested → Reserved → Switched → Published | Failed(compensated)`.

| | **A. Класичний** (`?mode=classic`) | **B. Гібридний / event-driven** (`?mode=eda`) |
|---|---|---|
| Відповідь клієнту | `200` після всіх кроків | `202` одразу, далі статус через SSE |
| Кроки | HTTP: catalog.Reserve → switch `version.json` → catalog.MarkReleased → **telemetry.POST /audit (обов'язково)** | worker саги: ті самі 3 кроки з breaker і retry → outbox → `release.published` → telemetry, gateway і research підписані |
| `telemetry` лежить | 502 і компенсація, реліз скасовано | реліз проходить; аудит дочитується з черги після підйому |
| `catalog` повільний | клієнт чекає суму затримок | сага чекає, breaker відкривається, повтор із backoff; клієнт не блокується |

## 5. Структура репозиторію

```
constella_helper/                 module constella, go 1.26 (вимога OTel 1.47)
├─ services/{gateway,dataprep,catalog,release,telemetry,research}/
│    cmd/main.go (тільки wiring) · internal/{app,adapters}
├─ internal/domain/{catalog,release,genconst,compare}   чисті, без I/O
├─ internal/platform/{events,mq,outbox,inbox,otelx,logx,httpx,sqlitex,envx,fault}
├─ api/ proto/catalog.proto (+ згенерований код у git) · events/*.md · openapi/*.yaml
├─ web/ (index.html, style.css, js/*.js як ES-модулі; без збірки; вбудовується в gateway через go:embed)
├─ internal/cosmosdb/   формат cosmos.db (схема, запис, інспектор); спільний для dataprep і catalog
├─ testdata/cultures/*.json (14 культур Stellarium) · testdata/names_uk.csv
├─ deploy/docker-compose.yml · services/*/Dockerfile
├─ tools/bench/  ·  docs/{IMPLEMENTATION_PLAN,ARCHITECTURE,EXPERIMENTS}.md
└─ CLAUDE.md
```

Що звідки береться:
- `lab2/internal/rabbitmq` копіюється (лаб2 лишається без змін) і розширюється: generic `Publish(ctx, events.Envelope)`, декларування топології на сервіс, DLX, пропагація OTel;
- `lab2/internal/domain/aggregate.go` є шаблоном для `internal/domain/release`.

Залежності:
- `amqp091-go`, `modernc.org/sqlite` (pure Go), `go.opentelemetry.io/otel` + `otelhttp` + `otelgrpc` + OTLP exporter;
- `sony/gobreaker`, `google.golang.org/grpc`, `log/slog`.

Конфіг задається env-змінними на сервіс (аналог `appsettings.json`); discovery через DNS-імена Compose.

## 6. UI (Завдання 5): `web/` за gateway, українською

Вкладки:
1. **Збірки**: запуск ETL, живий статус.
2. **Перевірка**: звіт валідації, пошук і таблиця зір, сузір'я.
3. **Релізи**: кнопки «Опублікувати (класично)» і «Опублікувати (події)», час відповіді, історія, поточний `version.json`.
4. **Дослідження**: запуск run і sweep, метрики, CSV.
5. **Аудит і логи**.
6. **Збої**: затримка або помилка на сервіс через `POST /debug/fault`, посилання на Jaeger.

## 7. Етапи

Кожен етап закінчується так: `gofmt -l .` порожній, `go vet ./...` і `go test ./...` зелені, `docker compose up` піднімається, сценарій етапу пройдено.

### M0. Підготовка і каркас
- Go 1.26+ (локально 1.27.4 ✅); `protoc` знадобиться лише в M6 (`buf generate` у Docker).
- `go.mod`, compose з RabbitMQ і Jaeger.
- `platform`: `events`, `mq` (з лаб2), `otelx.Init(service)`, `httpx` (middleware recover/slog/auth/otel; клієнт із timeout, retry і breaker), `sqlitex` (WAL), `outbox`/`inbox`, `fault`.
- Юніт-тести outbox/inbox.

### M1. `dataprep`, `catalog`, `gateway` і UI (вкладки 1–2) ✅
- HYG v4.2 читається з примонтованого `data/` (у git не кладеться).
- Культури: **14 реальних культур зі Stellarium** (`skycultures/<id>/index.json`, файли без змін у `testdata/cultures/`, ліцензії в `testdata/cultures/README.md`): `modern_iau` (88 сузір'їв, з нього беруться назви IAU), `egyptian`, `maya`, `norse`, `inuit`, `navajo`, `maori`, `sami`, `hawaiian_starlines`, `boorong`, `aztec`, `mongolian`, `tibetan`, `indian`. Формат `.fab` не використовується: сузір'я лежать в `constellations[].lines` як полілінії HIP. Вершина, що не є HIP-зорею (координата, `DSO:M42`), розриває полілінію.
- Українські назви 50 яскравих зір: `testdata/names_uk.csv` (чернетка; перевірити перед дипломом).
- Схема `cosmos.db` (пакет `internal/cosmosdb`, спільний для dataprep і catalog): `stars`, `constellations(brightest_star_id)`, `cultures`, `culture_constellations`, `culture_edges`, `meta(schema_version, content_hash, dropped_edges)`. Файл без WAL, один файл; пишеться в `*.tmp` і перейменовується.
- `content_hash` = SHA-256 канонічного вмісту (`domain/catalog.ContentHasher`), `sha256` у події = хеш байтів файлу. Збірка відтворювана: той самий вхід дає той самий SHA-256 файлу.
- Валідація (`domain/catalog.Validate`, коди без тексту, переклад у UI):
  - кількість зір за ярусами (1616 / 5020 / 8835 / 15459 / 41149 / 83124 / 107938, Сонце враховано; збіг точний);
  - 88 сузір'їв IAU, кожне з найяскравішою зорею з того ж сузір'я;
  - кожне ребро культури має HIP-зорю в таблиці `stars`;
  - SHA-256 файлу і хеш вмісту збігаються;
  - попередження: ребра, відкинуті при збірці (у культурах є HIP, яких немає в HYG: зараз 4).
- Потік: `POST /api/dataprep/builds` → `202` → події `dataprep.build.started|completed` через outbox → `catalog` (inbox, дедуплікація) → вердикт `catalog.build.validated|rejected` → SSE у gateway. Одна збірка за раз (`409`); збірка, перервана перезапуском, позначається `INTERRUPTED`.
- Один наскрізний трейс у Jaeger: gateway → dataprep → RabbitMQ → catalog → gateway SSE.
- Gateway: токен, маршрути `/api/{service}/…` → `/v1/…`, `/api/overview` (API Composition: сервіс, що впав, показується як `down`), `/api/stream` (SSE), UI вбудований (`web/embed.go`). Circuit breaker стоїть у клієнті overview; проксі має лише timeout.
- UI, вкладки 1–2: збірки з живим статусом, звіт перевірки, пошук зір (англійською, українською, `HIP 32349`, `Alp CMa`), культури і схема сузір'я в SVG.

### M2. `telemetry` і `release` у режимі A ✅ (код і тести; наскрізний сценарій у Docker ще не пройдено)
- `domain/release`: агрегат `Release` (event sourcing у стилі лаб2; `Replay` дає той самий стан, що й живий агрегат). Статуси `requested → reserved → switched → published | failed`.
- Сага `PublishClassic` у `release`: `catalog.Reserve` → копія артефакту з перевіркою SHA-256 і атомарний switch `version.json` (temp + rename) → `catalog.MarkReleased` → **обов'язковий** `telemetry POST /v1/audit`. Збій кроку відкочує попередні (відновлення попереднього `version.json`, `catalog.Cancel`), реліз зберігається як `failed` із кроком і кодом; HTTP `502 RELEASE_FAILED`. У класичному режимі події брокера не публікуються.
- `catalog`: протокол резервування (`POST/DELETE /v1/builds/{id}/reserve`, `POST …/release`), ідемпотентний за `release_id`; тепер зберігає SHA-256 і розмір артефакту.
- `telemetry`: аудит (`POST /v1/audit` з `Idempotency-Key`, плюс консюмер `#` через inbox), `POST /v1/client-log` зі scrubber `C:\Users\<name>\` (`domain/telemetry`), `ErrorSink`: Noop за замовчуванням, Sentry лише за `SENTRY_DSN`.
- Роздача: `GET /updates/version.json` і `/updates/data/cosmos-<id>.db` через gateway без токена.
- UI: вкладки «Релізи» (публікація, історія, поточний `version.json`) і «Аудит і логи» (з демо scrubber).
- Режим `eda` відповідає `501 MODE_NOT_IMPLEMENTED` до M3.

### M3. Режим B і брокер (Завдання 3) ✅ (код і тести; сценарій у Docker ще не пройдено)
- `POST /api/release/releases?mode=eda` зберігає `requested` і `release.requested` в outbox, відповідає `202`; працює воркер саги (`RunWorker`, той самий код кроків, що й у класичному режимі).
- Воркер повторює крок із backoff (6 спроб: 0,5 с, 1 с, 2 с, 4 с, …; вистачає, щоб breaker клієнта відкрився і знову пробував). Відмова `catalog` (4xx) і локальний збій switch не повторюються. Не вдалось → компенсація (відновлення `version.json`, `catalog.Cancel`) і `release.failed`.
- Стан саги лежить у event store, тому воркер продовжує реліз після перезапуску. Попередній `version.json` зберігається в події `switched`, тож відкат працює і після перезапуску. Зупинка процесу не провалює реліз.
- Аудит у режимі подій не синхронний: `release.requested|published|failed` через outbox → RabbitMQ → `telemetry.q` (`#`, inbox) і SSE у gateway. Якщо `telemetry` лежить, реліз проходить, а події чекають у черзі.
- `catalog` валідує збірку подією `dataprep.build.completed` (з M1). `release.q` поки нічого не споживає, його підписка на `catalog.build.*` не потрібна, бо реліз ініціює користувач; `research.q` додасться в M6.
- UI: кнопка «Опублікувати (події)», живий статус релізу, вкладка «Збої» (затримка і частка помилок на сервіс через `/debug/fault`, стан breaker-ів, посилання на Jaeger і RabbitMQ).

### M4. Трасування (Завдання 4)
- `otelhttp` скрізь, ін'єкція та витяг `traceparent` в AMQP, `traceparent` у рядку outbox (щоб relay продовжив трейс).
- Атрибути спанів `build.id`, `release.id`, `mode`; помилки позначаються статусом Error.
- Скріншоти: (1) режим A, (2) режим B, (3) збій: `telemetry` зупинено в режимі A (червоний спан і компенсація) і в режимі B (реліз пройшов, аудит пізніше).

### M5. Експерименти (Завдання 2)
`tools/bench` робить N=50 публікацій на режим і сценарій та виводить p50, p95 і частку помилок у markdown у `docs/EXPERIMENTS.md`.

| Сценарій | Що перевіряє |
|---|---|
| S1 | норма |
| S2 | `telemetry` зупинено |
| S3 | `catalog` +2 с затримки |
| S4 | брокер зупинено під час публікації (outbox тримає подію) |
| S5 | повторна доставка (аудит не дублюється) |
| S6 | `telemetry` піднявся і дочитав чергу |

Складність розробки: LOC і кількість компонентів на режим плюс якісний опис.

### M6. `research` і gRPC (останній)
- `catalog.StreamStars`.
- `genconst`: haversine-кластеризація + Kruskal MST; режим «none», інші режими полярної зорі як заглушки.
- `compare`: Jaccard за ребрами відносно культури з `cosmos.db` (наприклад `modern_iau`).
- Перевірка детермінізму: той самий seed двічі дає той самий хеш.
- Свіп у CSV; mock-зорі, якщо HYG немає.
- Додатковий трейс у Jaeger з gRPC.

### M7. Документація
`ARCHITECTURE.md` (діаграма, межі, рішення, альтернативи: модульний моноліт, serverless), чернетка звіту за пунктами методички. Мінімальні Dockerfile вже є з M1; multi-stage оптимізація переходить у Лаб №4.

## 8. Ризики

| Ризик | Що робимо |
|---|---|
| ~~Локальний Go замалий для OTel~~ | знято: локально Go 1.27.4 |
| Немає `protoc` на Windows | `buf generate` у контейнері, згенерований код комітимо |
| Культури Stellarium | ~~mock~~ знято: 14 реальних `index.json` у `testdata/cultures/` (без культур з ліцензією CC BY-ND) |
| Обсяг (6 сервісів) | `research` останній; без нього всі 5 завдань методички закриті |

## 9. Перевірка end-to-end

1. `docker compose -f deploy/docker-compose.yml up --build`; потім UI `http://localhost:8080`, RabbitMQ `:15672`, Jaeger `:16686`.
2. Збірки: старт → `completed` → у «Перевірка» звіт `validated`, пошук «Сіріус» або HIP 32349 працює.
3. Релізи: режим A і режим B; `GET /updates/version.json` віддає нову версію з правильним SHA-256 (перевірити `certutil -hashfile … SHA256`).
4. `docker compose stop telemetry`: A дає 502 і компенсацію, B проходить; `start telemetry` і аудит з'являється.
5. Jaeger: є три обов'язкові трейси з M4.
6. `go run ./tools/bench` дає таблицю в `docs/EXPERIMENTS.md`.
7. Дослідження: run із seed=42 двічі дає ідентичний хеш і CSV.
