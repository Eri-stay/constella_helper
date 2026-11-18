# Ручний сценарій: відтворити M4 (трасування) і M5 (експерименти)

Усі команди для PowerShell, з кореня `constella_helper`. Скріншоти для звіту: Jaeger `http://localhost:16686`, UI `http://localhost:8080` (токен `dev-token`).

## 0. Підготовка

```powershell
docker compose -f deploy/docker-compose.yml up --build -d --wait
```

Перевірка: у UI угорі праворуч усі сервіси «ок». Потім:

1. UI → «Збірки» → «Запустити збірку», зачекати «перевірено».
2. Лишайте цю збірку: нею публікуються всі релізи нижче. Реліз можна повторити, лише якщо попередній зі збірки не опублікований: після успішного релізу збірку `catalog` вважає випущеною, тож для кожного нового ручного релізу **запускайте нову збірку**.

## M4. Три трейси

### Трейс 1: класичний режим

1. «Релізи» → оберіть нову збірку → «Опублікувати (класично)». Очікується «опубліковано» за ~0,1 с.
2. Поряд із рядком релізу натисніть «↗» (відкриє трейс у Jaeger) і зробіть скріншот.
3. Перевірка командою:
   ```powershell
   go run ./tools/tracecheck -scenario classic
   ```
   Має бути `OK` і дерево: `gateway → release → catalog (reserve, release) → telemetry (audit)`.

### Трейс 2: режим подій

1. Нова збірка → «Опублікувати (події)». Відповідь приходить за мілісекунди (`прийнято`), статус потім стає «опубліковано».
2. «↗» біля релізу → скріншот. В одному трейсі: `release request`, `publish release.requested`, `release saga` з кроками, `publish release.published` і `consume …` у `gateway` та `telemetry`.
3. ```powershell
   go run ./tools/tracecheck -scenario eda
   ```

### Трейс 3: збій (telemetry зупинено)

1. Зупиніть telemetry:
   ```powershell
   docker compose -f deploy/docker-compose.yml stop telemetry
   ```
2. **Класичний.** Нова збірка → «Опублікувати (класично)». Очікується помилка (`502`), статус «помилка, відкат ▾»; розкрийте його: крок «аудит», відкат виконано. Скріншот трейсу («↗»): червоний `saga audit`, далі `saga compensate`.
   ```powershell
   go run ./tools/tracecheck -scenario classic-fail
   ```
3. **Подієвий.** Нова збірка → «Опублікувати (події)». Реліз проходить («опубліковано»), трейс без помилок (скріншот).
4. Поверніть telemetry і подивіться, що події дочитались:
   ```powershell
   docker compose -f deploy/docker-compose.yml start telemetry
   ```
   «Аудит і логи»: з'явились `release.requested` і `release.published` зі шляхом «подія». У Jaeger: `consume release.published` у `telemetry` з'явився вже після запуску.

## M5. Експерименти

### Швидка перевірка (1–2 хв)

```powershell
go run ./tools/bench -n 5 -s S1,S5 -pause 3s -out $env:TEMP\bench-smoke.md
```

### Повний прогін (≈15 хв)

```powershell
go run ./tools/bench
```

Бенч сам запускає збірку, зупиняє й запускає telemetry і RabbitMQ (`docker compose`), вмикає затримку `catalog`, публікує по 50 релізів у кожному режимі і **замінює** автозгенеровану частину `docs/EXPERIMENTS.md` (між `<!-- bench:begin -->` і `<!-- bench:end -->`). Не чіпайте платформу під час прогону. Один сценарій: `-s S3`.

| Сценарій | Що робить бенч | Що має вийти |
|---|---|---|
| S1 | норма | обидва режими без помилок; подієвий відповідає швидше, завершується довше |
| S2 | `telemetry` зупинено | класичний: помилки (відкат); подієвий: 0 % помилок |
| S3 | `catalog` +2 с | класичний чекає суму затримок; подієвий відповідає одразу |
| S4 | RabbitMQ зупинено | обидва проходять; усі події дійшли після запуску брокера |
| S5 | подія доставлена двічі | в аудиті по одному рядку на подію |
| S6 | telemetry зупинено, потім запущено | усі події дочитано |

Лише таблиця складності (без стенду):

```powershell
go run ./tools/bench -loc
```

### Скільки це триває і чому

- S3 найдовший: класичний режим кожного разу чекає ~4 с (дві затримки по 2 с), подієвий завершується за ~4 с, але відповідає одразу.
- Після S2 бенч робить паузу 12 с, щоб circuit breaker закрився (параметр `-pause`).

### Якщо щось пішло не так

- Бенч зупинився посеред сценарію: запустіть вручну `docker compose -f deploy/docker-compose.yml start telemetry rabbitmq` і скиньте затримки на вкладці «Збої» (кнопка «Скинути»).
- «the platform did not become healthy»: `docker compose -f deploy/docker-compose.yml up -d --wait`.
- Docker Desktop віддає `500`: перезапустіть Docker Desktop і повторіть.

## M6. Дослідження (генерація сузір'їв)

1. Підняти стек і мати перевірену збірку (див. «Підготовка»). `catalog` слухає gRPC на `:9082` (лише всередині Docker-мережі), `research` на `:8085` через gateway.
2. UI → «Дослідження». Оберіть культуру (`modern_iau`), блиск до 5 m, відстані `6, 8, 10, 12`, seed `42`, усі п'ять стратегій → «Запустити свіп». Прогрес `n з N` оновлюється; за кілька секунд з'являються «Підсумок за стратегіями» (найкращий і середній Jaccard) і таблиця всіх запусків (сортування кліком по заголовку).
3. «Перевірити детермінізм»: той самий seed двічі, бейдж «детерміновано» і два однакові хеші. Увімкніть «Джиттер, °» = 0.5, щоб бачити, що seed справді діє: різні seed дають різні графи, однакові дають однаковий.
4. «CSV» завантажує таблицю запусків для диплома. Для серії seed (наприклад `1, 2, 3, 4, 5`) при джиттері 0.5 видно розкид метрик.
5. Трейс: Jaeger → сервіс `research`, операція `research sweep`: дочірні `load stars` (з gRPC-викликом у `catalog`) і `generate <стратегія>`.
6. Без UI:
   ```powershell
   curl.exe -s -X POST -H "Authorization: Bearer dev-token" -d '{\"culture\":\"modern_iau\",\"eps_list\":[6,8,10,12],\"params\":{\"mag_max\":5}}' http://localhost:8080/api/research/sweeps
   ```

Перевірка стратегій на реальних даних без Docker (потрібен `hyg_v42.csv`):

```powershell
go test -count=1 -v -run ResearchStrategies ./services/dataprep/internal/adapters/
```

Перегенерація gRPC-коду після зміни `api/proto/catalog.proto` (потрібен Docker):

```powershell
docker run --rm -v "${PWD}:/workspace" -w /workspace bufbuild/buf generate
```
