# Deploy musik

## Обновление контейнеров и PostgreSQL

Для стандартной установки из согласованных CI-образов используйте
`docker-compose.images.yml`, `.env.example` и `scripts/update-compose.sh`.
PostgreSQL включается через `COMPOSE_PROFILES=postgres`; `migrate` подготавливает
БД перед запуском приложений. Скрипт останавливает только player/worker:
внешних писателей и backup конфигурации нужно обработать отдельно.

Перед выбором тега проверить [готовность версии и порядок обновления](UPGRADE-v8.md).
Локальные исправления после внедрения не следует считать уже опубликованными
в GHCR. Для внешнего GPU и standby:
[инструкция установки на трёх серверах](THREE-HOST-OPERATIONS.md).

Для существующей установки: [пошаговое обновление до схемы v8 и откат](UPGRADE-v8.md).

По умолчанию musik работает для одного владельца: пароль для UI, Bearer-токен
для API. Worker не публикуется наружу.

## Auth (fail-closed)

В single-user без `MUSIK_PASSWORD` и/или `MUSIK_API_TOKEN` player **не стартует**, пока явно не задано `MUSIK_AUTH_DISABLED=1` (только localhost-отладка).

| Переменная | Назначение |
|------------|------------|
| `MUSIK_PASSWORD` | вход в Web UI (cookie) |
| `MUSIK_API_TOKEN` | `Authorization: Bearer …` для API / Flutter |
| `MUSIK_SESSION_SECRET` | HMAC cookie |
| `MUSIK_SECURE_COOKIE=1` | cookie только по HTTPS |
| `MUSIK_AUTH_DISABLED=1` | открытый режим (не для публичного IP) |
| `MUSIK_CORS_ORIGINS` | список Origin через запятую для cross-origin (Flutter web); пусто = same-origin |

Login: не больше 5 попыток / IP / минуту (`429`).

### Multi-user

Пользователи входят по имени и паролю; OIDC можно подключить вторым способом
входа. Режим требует HTTPS. Не
задавай в нём общие `MUSIK_PASSWORD`, `MUSIK_API_TOKEN` или
`MUSIK_AUTH_DISABLED`; CORS оставь same-origin. После входа пользователь
может сменить пароль и создать персональный Bearer-токен устройства в разделе «Устройства и токены»
на странице аккаунта. Он фиксированно связан с выбранным профилем, живёт 180
дней и не наследует административные права.

```dotenv
MUSIK_MULTI_USER=1
MUSIK_PUBLIC_BASE_URL=https://music.example.com
# Необязательно, вход через OIDC. Callback в OIDC-клиенте:
# https://music.example.com/api/auth/oidc/default/callback
MUSIK_OIDC_ISSUER=https://identity.example.com/realms/music
MUSIK_OIDC_CLIENT_ID=musik
MUSIK_OIDC_CLIENT_SECRET=...
```

В стандартном CI Compose первое приглашение создаёт migrate через
`MUSIK_BOOTSTRAP_INVITE`; порядок описан в [инструкции обновления](UPGRADE-v8.md).
Альтернатива после миграции — ручной bootstrap с окружением выбранной БД:

```bash
musik-player admin bootstrap --ttl 1h
```

Не добавляй `--db`, если задан `MUSIK_DATABASE_URL`. Для отдельного запуска
SQLite без URL укажи проверенный путь через `--db`.

Команда один раз выведет секрет приглашения. Открой
`https://music.example.com/?invitation=<secret>` и придумай имя и пароль (или
войди через OIDC, если он настроен; `--issuer` ограничит приглашение им).
После первого входа этот владелец становится администратором и приглашает
остальных из раздела «Профиль». Секреты приглашений хранятся в базе только в
виде хеша; сам секрет показывается при создании один раз.

## Docker Compose (рекомендуется)

```bash
cp .env.example .env
# single-user: MUSIK_PASSWORD, MUSIK_API_TOKEN; OIDC: MUSIK_MULTI_USER=1 и OIDC настройки
# в обоих режимах: MUSIK_SESSION_SECRET, MUSIK_LIBRARY

make up
make logs
make rescan && make mixes
make smoke
```

По умолчанию `MUSIK_DATABASE_URL` указывает на SQLite в томе `musik-data`; кэши
и темы также хранятся там. Для PostgreSQL задай тот же `MUSIK_DATABASE_URL` для
player и worker. Worker не публикуется наружу; подключай оба контейнера к одной
сети с БД.

Порядок старта Compose намеренно строгий:

1. одноразовый migrate готовит базу и завершается успешно;
2. worker запускается и начинает отвечать на `/health`;
3. после успешного healthcheck запускается player;
4. player проверяет общий Alembic migration head; для SQLite дополнительно
   проверяется `PRAGMA user_version` (схема v8).

Для обновления существующей базы использовать скрипт и остановить внешних
писателей. Один `compose up` не обеспечивает этого. Healthcheck не заменяет
проверку данных, новой загрузки и мобильного воспроизведения.

Go player никогда не создаёт и не изменяет таблицы.

### Готовые образы (без сборки)

GitHub Actions (`.github/workflows/images.yml`) собирает полный набор образов при push в
любую ветку, на теги `v*` и по кнопке: `ghcr.io/<owner>/musik-player` и
`ghcr.io/<owner>/musik-worker`, `ghcr.io/<owner>/musik-migrate` с тегами ветки
`branch-<имя>`, `sha-<коммит>` и версией тега; `latest` — только основная ветка.
Запуск из них — тот же стек, но без Go/torch на сервере:

```bash
cp .env.example .env
# single-user: MUSIK_PASSWORD, MUSIK_API_TOKEN; OIDC: MUSIK_MULTI_USER=1 и OIDC настройки
# в обоих режимах: MUSIK_SESSION_SECRET, MUSIK_LIBRARY
./scripts/update-compose.sh
```

`MUSIK_IMAGE_TAG` закрепляет конкретную сборку, `MUSIK_IMAGE_OWNER` — чьи образы
брать (форк публикует свои). Новые пакеты GHCR создаются приватными: владельцу
репозитория нужно один раз включить Public в настройках пакета.

### Публичный VPS (белый IP)

1. Задай сильные секреты в `.env` (не `AUTH_DISABLED`).
2. Наружу только `:8787` (player). Worker не публикуй.
3. Reverse-proxy (Caddy/Nginx) → HTTPS.
4. Env:
   ```bash
   MUSIK_PUBLIC_BASE_URL=https://music.example.com
   MUSIK_SECURE_COOKIE=1
   ```
5. Для SQLite делай согласованный бэкап через `sqlite3 .backup`; для PostgreSQL
   используй `pg_dump` и регулярно проверяй восстановление.
6. Play-сессии сохраняются в выбранной СУБД и переживают рестарт контейнера.

### LAN / телефон

1. Compose уже публикует `8787`.
2. `http://<lan-ip>:8787` → пароль.
3. Для share-ссылок: `MUSIK_PUBLIC_BASE_URL=http://<lan-ip>:8787`.

### Share radio

Нужен **ffmpeg** (есть в `Dockerfile.player`).

```bash
MUSIK_PUBLIC_BASE_URL=https://music.example.com
# MUSIK_SHARE_BITRATE=192k
# MUSIK_SHARE_MAX_LISTENERS=4
```

UI **Поделиться** → `…/listen/<token>.mp3`. Отозвать в Профиле. Слушатели не меняют вкус.

## Темы

Плеер читает папку тем с диска при каждом запросе. Новая тема не требует пересборки и перезапуска: положи каталог и обнови страницу.

По умолчанию это `data/themes` рядом с базой (`MUSIK_THEMES`). В Docker каталог — `/data/themes` на томе `musik-data`, не папка проекта:

```bash
docker compose cp themes/ink player:/data/themes/ink
```

```text
data/themes/ink/
  theme.json
  theme.css
  fonts/          # необязательно, только .woff2 / .woff
```

`theme.json` задаёт `id` (он же имя папки), `name`, `blurb`, `swatch` и `color`. `theme.css` стилизует `html[data-theme="<id>"]`. Готовый пример: `cp -a themes/ink data/themes/`. Папка с тем же `id`, что у встроенной темы, заменяет её файлы. Своя палитра из профиля по-прежнему живёт только в браузере.

## Makefile

| Target | Действие |
|--------|----------|
| `make up` / `down` / `logs` | Compose |
| `make rescan` / `mixes` | jobs (Bearer) |
| `make smoke` | HTTP smoke с auth |
| `make player` | сборка Go |

## Bare-metal

Следующий пример — для новой single-user установки. Для обновления существующей
сначала выполнить подготовку и backup из [UPGRADE-v8.md](UPGRADE-v8.md);
scan/embed для самой миграции не нужны.

```bash
export MUSIK_PASSWORD=… MUSIK_API_TOKEN=…
export MUSIK_DATABASE_URL="sqlite:///$PWD/data/db/musik.db" MUSIK_LIBRARY=/path/to/music
musik db migrate
musik scan && musik embed && musik clusters
musik worker   # terminal 1
./player/bin/musik-player   # terminal 2
```

Schema: единственный владелец — Alembic, запускаемый командой `musik db migrate`.
Она обновляет существующую SQLite v5 до схемы v8, сохраняет данные в профиле
владельца установки и создаёт эквивалентную новую схему PostgreSQL; Go player и
worker проверяют один migration head и сами схему не изменяют. Если база имеет
неподдерживаемую версию, сервис завершает старт и предлагает выполнить миграцию.

### PostgreSQL и перенос с SQLite

Подготовь пустую PostgreSQL базу и передай SQLite backup после остановки player,
worker и всех внешних писателей. Сначала проверь [готовность версии](UPGRADE-v8.md#2-готовность-версии). Команда оставляет исходный файл нетронутым, создаёт согласованный
snapshot, проверяет SQLite integrity и foreign keys, копирует строки и сравнивает
число строк и SHA-256 по каждой таблице. Перенос требует SQLite v8 и копирует
данные существующего владельца/профиля вместе с персональными строками,
включая хеши device tokens и их привязку к профилям. Сырые секреты в БД не
хранятся; копия БД сохраняет действие ранее выданных токенов:

```bash
musik db transfer --source /backup/musik.db \
  --destination "$MUSIK_TRANSFER_DATABASE_URL" \
  --report /backup/musik-transfer-report.json
```

Укажи в `MUSIK_TRANSFER_DATABASE_URL` URL только целевой PostgreSQL, а после
успешного отчёта переключи `MUSIK_DATABASE_URL` обоих сервисов. Музыкальные и
обложечные файлы база не переносит: их пути должны быть доступны на новом хосте.
Если владелец ещё не активирован через OIDC, первое приглашение создаётся
через migrate либо ручной bootstrap из раздела авторизации. Если OIDC уже
работал до переноса, повторный bootstrap не нужен: учётные записи переносятся
вместе с данными. При PostgreSQL передать ту же `MUSIK_DATABASE_URL`, без `--db`.

Подробнее: [ROADMAP.md](ROADMAP.md) · [API.md](API.md) · [MOBILE.md](MOBILE.md) ·
**[CAPACITY.md](CAPACITY.md)** (ресурсы под 50k треков).
