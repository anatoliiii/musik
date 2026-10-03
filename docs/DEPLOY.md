# Deploy musik

Один владелец: пароль для UI, Bearer-токен для API. Worker не публикуется наружу.

## Auth (fail-closed)

Без `MUSIK_PASSWORD` и/или `MUSIK_API_TOKEN` player **не стартует**, пока явно не задано `MUSIK_AUTH_DISABLED=1` (только localhost-отладка).

| Переменная | Назначение |
|------------|------------|
| `MUSIK_PASSWORD` | вход в Web UI (cookie) |
| `MUSIK_API_TOKEN` | `Authorization: Bearer …` для API / Flutter |
| `MUSIK_SESSION_SECRET` | HMAC cookie |
| `MUSIK_SECURE_COOKIE=1` | cookie только по HTTPS |
| `MUSIK_AUTH_DISABLED=1` | открытый режим (не для публичного IP) |
| `MUSIK_CORS_ORIGINS` | список Origin через запятую для cross-origin (Flutter web); пусто = same-origin |

Login: не больше 5 попыток / IP / минуту (`429`).

## Docker Compose (рекомендуется)

```bash
cp .env.example .env
# обязательно: MUSIK_PASSWORD, MUSIK_API_TOKEN, MUSIK_SESSION_SECRET, MUSIK_LIBRARY

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

1. worker выполняет `musik db migrate`;
2. worker начинает отвечать на `/health`;
3. только после успешного healthcheck запускается player;
4. player проверяет общий Alembic migration head; для SQLite дополнительно
   проверяется `PRAGMA user_version`.

Go player никогда не создаёт и не изменяет таблицы.

### Готовые образы (без сборки)

GitHub Actions (`.github/workflows/images.yml`) собирает образы при каждом push в
`main`, на теги `v*` и по кнопке: `ghcr.io/<owner>/musik-player` и
`ghcr.io/<owner>/musik-worker` с тегами `latest`, `sha-<коммит>` и версией тега.
Запуск из них — тот же стек, но без Go/torch на сервере:

```bash
cp .env.example .env
# обязательно: MUSIK_PASSWORD, MUSIK_API_TOKEN, MUSIK_SESSION_SECRET, MUSIK_LIBRARY
docker compose -f docker-compose.images.yml pull
docker compose -f docker-compose.images.yml up -d
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

```bash
export MUSIK_PASSWORD=… MUSIK_API_TOKEN=…
export MUSIK_DATABASE_URL="sqlite:///$PWD/data/db/musik.db" MUSIK_LIBRARY=/path/to/music
musik db migrate
musik scan && musik embed && musik clusters
musik worker   # terminal 1
./player/bin/musik-player   # terminal 2
```

Schema: единственный владелец — Alembic, запускаемый командой `musik db migrate`.
Она принимает существующую SQLite v5 и создаёт схему PostgreSQL; Go player и
worker проверяют один migration head и сами схему не изменяют. Если база имеет
неподдерживаемую версию, сервис завершает старт и предлагает выполнить миграцию.

### PostgreSQL и перенос с SQLite

Подготовь пустую PostgreSQL базу и передай SQLite backup после остановки player
и worker. Команда оставляет исходный файл нетронутым, создаёт согласованный
snapshot, проверяет SQLite integrity и foreign keys, копирует строки и сравнивает
число строк и SHA-256 по каждой таблице:

```bash
musik db transfer --source /backup/musik.db \
  --destination "$MUSIK_TRANSFER_DATABASE_URL" \
  --report /backup/musik-transfer-report.json
```

Укажи в `MUSIK_TRANSFER_DATABASE_URL` URL только целевой PostgreSQL, а после
успешного отчёта переключи `MUSIK_DATABASE_URL` обоих сервисов. Музыкальные и
обложечные файлы база не переносит: их пути должны быть доступны на новом хосте.
Текущий transfer переносит общую SQLite v5 схему; перед объединением multi-user
ветки его нужно дополнить переносом существующих данных во владельца и профиль.

Подробнее: [ROADMAP.md](ROADMAP.md) · [API.md](API.md) · [MOBILE.md](MOBILE.md) ·
**[CAPACITY.md](CAPACITY.md)** (ресурсы под 50k треков).
