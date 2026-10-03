# Roadmap рекомендательной системы musik

Это актуальный план развития. Старый список шагов «ТЗ 3.0» описывал создание
первой рабочей версии и больше не используется для планирования.

Обозначения: **готово** — реализовано и проверено; **следующий этап** — основной
текущий приоритет; **позже** — зависит от данных и критериев предыдущих этапов.

## Принципы перехода

- Источником истины служат catalog data, append-only события и явные настройки.
- Сначала собираются достоверные события, затем вводится единая модель `w·x`,
  после этого обучение и только затем bandit.
- Новое ядро заменит старое одним runtime-путём: без параллельных `v1/v2` и
  долгоживущих feature flags.
- Дизлайк и ранний скип не вычитаются из положительного long-вектора:
  используются отдельные persistent/session negative prototypes.
- Невоспроизведённые элементы очереди не считаются отрицательным feedback.
- Расширение БД выполняется нормализованными сущностями и версионированным JSON,
  а не набором nullable-полей «на всякий случай».

## Статус этапов

| Этап | Содержание | Статус |
|------|------------|--------|
| **0A** | Alembic migrations, `musik db migrate`, общий schema head и точный Go gate | **готово** |
| **0E** | SQLite/PostgreSQL portability через SQLAlchemy и GORM, проверенный SQLite transfer | **реализовано в ветке** |
| **0B** | Упорядоченный startup worker → healthcheck → player | **готово** |
| **0C** | Foundation-схема requests/events/contexts/rules/entities/models | **готово** |
| **0D** | Детерминированный listener simulator и 2k/50k exact benchmarks | **готово** |
| **1** | Корректный request → impression → event → outcome lifecycle | **готово** |
| **2** | Единое taste/candidates/features/score/sequence/select ядро | **готово** |
| **2A** | Entity vectors, custom contexts и radio rules | **готово** |
| **2B** | Пользовательские и smart playlists | **готово** |
| **3** | Обучаемый линейный ранкер с temporal holdout | **готово**, публикация после порога данных |
| **4** | Адаптивное исследование / Thompson sampling | **готово**, bandit после порога по source |

## Готовый фундамент

### Схема и запуск

- Alembic через Python является единственным владельцем миграций.
- Worker применяет миграции до запуска HTTP-сервиса.
- Go player проверяет Alembic head и для SQLite дополнительно проверяет
  `PRAGMA user_version`; при несовпадении завершается с понятной ошибкой.
- SQLite остаётся backend по умолчанию; PostgreSQL выбирается общим
  `MUSIK_DATABASE_URL` для player и worker.
- Compose запускает player только после успешного worker healthcheck.
- Новые таблицы покрывают recommendation requests, event/context references,
  taste contexts, entity vectors, radio rules, model versions и training runs.
- `listening_history` подготовлена как append-only event log; расширяемые JSON
  поля требуют версии формата и проходят проверку SQLite.

### Воспроизводимость и производительность

```bash
make sim
make bench
```

Simulator проверяет один вкус, два разнесённых вкуса, смену настроения и
устойчивый dislike-кластер по нескольким seed, печатая median и разброс.

На тестовой машине exact benchmark для 50k×512 дал примерно:

- fused similarity scan: **56,5 ms**;
- полное построение очереди: **58,7 ms**.

Shortlist удалён: radio всегда строит очередь через `BuildCore` и exact fused
scan. Живой p95 смотри в профиле и `GET /api/metrics/recommendations`.

## Этап 1 — достоверный lifecycle рекомендаций

- Одна строка `recommendation_requests` на одно построение очереди.
- Каждое алгоритмическое решение, включая первый трек радио, получает случайный
  128-bit `impression_id`.
- Impression начинается как `pending`, получает `played_at` на `track_start` и
  закрывается как `finished`, `partial`, `early_skip`, `superseded` или
  `abandoned`.
- `superseded` и `abandoned` не участвуют в обучении как отрицательный feedback.
- События становятся idempotent; повтор браузерного запроса не удваивает
  статистику.
- Public share-radio не обновляет вкус и метрики владельца.
- Метрики разделяются по source/daypart/maturity/date и показывают минимальный
  размер выборки и неопределённость.

Критерий перехода дальше: один algorithmic play связан ровно с одним impression;
refresh/like не повышает счётчики несыгранных треков; baseline собран минимум за
две недели и содержит ориентировочно не менее 300 разрешённых impressions.

## Этап 2 — единое ядро рекомендаций

### Taste и контексты

- Long/daypart/session positive state и отдельные negative prototypes.
- До шести устойчивых taste centroids через weighted spherical k-means++.
- Версионированные artist/album/genre/custom-collection vectors.
- Независимые пользовательские `mood`, `place`, `activity` contexts без
  комбинаторного создания профилей.
- GPS не хранится; будущая автоматическая активация только opt-in.

### Правила и последовательность

- `block`, `downrank`, `cooldown` для track/song/artist/album/genre/cluster.
- Правила «не играть сейчас» не смешиваются с dislike.
- Unary track score отделяется от pairwise transition score A→B.
- Очередь строится как последовательность `current→q1→…`, с ограничениями на
  повторы artist/album/MD5/song clone и глобальным MMR.

### Candidates, features и model artifact

- Один fused exact scan для active centroids/current/session.
- Версионированный ordered feature schema и snapshot признаков в момент решения.
- Один формат model artifact: schema, feature order, normalization, weights,
  bias и bounds; несовместимый файл отклоняется целиком.
- Realtime Go radio и Python batch mixes получают одинаковую семантику
  features/sources/MMR и golden parity fixtures.
- Старые отдельные scoring/exploration пути удаляются при атомарном подключении
  нового orchestrator.

Критерий: exact top-N и Go↔Python parity проходят; simulator проходит multimodal
taste и mood shift; p95 queue build укладывается в бюджет; реальные finished и
early-skip метрики не ухудшаются с контролем daypart.

## Этап 3 — обучаемый линейный ранкер

- Один label на impression; explicit like/dislike важнее implicit outcome.
- Manual, partial, superseded, abandoned и legacy исключаются.
- Temporal train/holdout split с gap.
- Guardrails: log-loss, AUC и calibration против встроенной default-модели.
- Ориентир запуска — около 1500 размеченных impressions и оба класса в
  достаточном количестве.
- Model artifact заменяется атомарно только при полном совпадении feature schema
  и отсутствии деградации guardrail-метрик.

## Этап 4 — адаптивное исследование

- Bandit включается только после минимального числа разрешённых outcomes по
  каждому source.
- Aggregate arm управляет общей explore-share, source arms распределяют уже
  выделенные explore slots.
- Обновление только по сыгранным impressions; delayed outcomes поддерживаются,
  partial/abandoned/superseded игнорируются.
- Sampled probabilities, allocation и bounds логируются для воспроизводимости.

## Порядок поставки

1. **Готово:** migrations, schema gate, foundation schema, simulator/bench.
2. **Готово:** impression lifecycle, client/API contract и outcome-метрики.
3. Сбор baseline на живой библиотеке (14 дней / 300 algorithmic plays).
4. **Готово:** entity vectors, contexts, rules и playlists.
5. **Готово:** единое taste/candidate/feature/score/sequence/select ядро.
6. **Готово:** атомарный cutover, старый shortlist/far-pool путь удалён.
7. Post-change метрики в профиле: источники, finish/skip, модель, bandit.
8. Ranker публикуется только после порога данных и guardrails.
9. Bandit включается только после минимума allowed outcomes по source.

Для каждого этапа обязательны `go test ./...`, `pytest`, migration tests,
API-schema tests, deterministic simulator и релевантный benchmark.

## Эксплуатационные задачи

- Проверить benchmark на целевой 50k-библиотеке.
- Настроить SQLite backup через `.backup`/cron.
- Публичный deploy выполнять только через HTTPS.
- Flutter подключать к новым event/impression полям после стабилизации
  API-контракта.

Запуск и эксплуатация: [DEPLOY.md](DEPLOY.md). Текущий HTTP-контракт:
[API.md](API.md).
