# musik API v1

Base URL: `http://127.0.0.1:8787`  
OpenAPI: [`GET /api/openapi.json`](/api/openapi.json) · source [`openapi.yaml`](openapi.yaml)

Документ описывает работающий API v1. Radio current/queue items содержат
`request_id`, `impression_id` и `source`; события идемпотентны по `event_id`.
Контексты вкуса, radio rules и пользовательские/smart playlists доступны через
`/api/contexts`, `/api/rules` и `/api/playlists`.

Content-Type: `application/json` (кроме stream/artwork и `POST /api/library/upload`).
Все JSON-ошибки используют envelope `{"error":"…","code":"…"}`.

## Auth (один владелец)

| | |
|--|--|
| UI | пароль `MUSIK_PASSWORD` → `POST /api/auth/login` → httpOnly cookie `musik_session` |
| API / скрипты | `Authorization: Bearer <MUSIK_API_TOKEN>` |
| Отключить | только явно: `MUSIK_AUTH_DISABLED=1` |

Публично без auth: `GET /api/health` (без `tracks`/`dim`), `GET /api/openapi.json`, `GET /api/auth/me`, `POST /api/auth/login`, **`GET /listen/{token}`** (share radio).  
`POST /api/reload` — loopback **или** Bearer (callback worker).  
**Старт без пароля/токена запрещён**, кроме `MUSIK_AUTH_DISABLED=1`. Login rate-limit: 5/мин/IP.

Мобильная разработка: [MOBILE.md](MOBILE.md).

| Method | Path | Описание |
|--------|------|----------|
| POST | `/api/auth/login` | `{ "password": "…" }` → cookie, TTL ~14 дней |
| POST | `/api/auth/logout` | сброс cookie |
| GET | `/api/auth/me` | `{ok, auth_enabled}` |

## Core

| Method | Path | Описание |
|--------|------|----------|
| GET | `/api/health` | `{ok, version, api_version, auth}` (+ `tracks`, `dim` если auth) |
| GET | `/api/status` | tracks, maturity, mode, session, explore, `db_backend` (`sqlite` или `postgresql`) |
| GET | `/api/profile` | maturity, signals, top_artists/clusters, confidence, explore bounds |
| PUT | `/api/profile/explore` | `{explore_lo, explore_hi}` — UI bounds for gated Thompson sampling |
| GET | `/api/library` | плоский список треков |
| GET | `/api/artists` | `{artists, count}` |
| GET | `/api/albums` | `{albums, count}` |
| GET | `/api/tracks/{id}` | один трек |
| GET | `/api/stream/{id}` | оригинал (Range); `?q=mobile|aac|mp3`, alias `?fmt=…` — мобильный transcode |
| GET | `/api/artwork/{id}` | JPEG 96/256/640 (`?w=`, default 640); `?full=1` — оригинал |
| GET | `/api/similar/{id}` | top-10 cosine |
| POST | `/api/reload` | перечитать индекс из выбранной БД |
| GET | `/api/now` | current + queue (`?session_id=`) |
| POST | `/api/events` | playback events |
| POST | `/api/session/start` | `{seed_track_id?}` |
| POST | `/api/session/jump` | прыжок по индексу в fixed playlist |
| POST | `/api/session/back` | предыдущий трек в списке или радио |
| POST | `/api/radio/start` | радио по вкусу (diverse seed) |
| POST | `/api/share/radio` | создать публичную ссылку на эфир |
| GET | `/api/share/radio` | список ссылок (`?all=1` — с отозванными) |
| DELETE | `/api/share/radio/{token}` | отозвать ссылку |
| GET | `/listen/{token}` · `/listen/{token}.mp3` | непрерывный MP3-эфир (публично, ffmpeg) |
| POST | `/api/play` | listen: track / artist / album / track_ids |
| GET/POST | `/api/contexts` | пользовательские mood/place/activity |
| GET/PATCH/DELETE | `/api/contexts/{id}` | получить, изменить, архивировать |
| POST | `/api/contexts/{id}/activate` | включить контекст в сессии |
| POST | `/api/session/contexts` | заменить активные контексты |
| GET/POST | `/api/rules` | временные block/downrank/cooldown |
| POST | `/api/rules/undo` | вернуть последнее правило |
| GET/POST | `/api/playlists` | ручные и smart плейлисты |
| POST | `/api/playlists/import` | импорт M3U/JSON без server path |
| GET | `/api/playlists/{id}/export` | экспорт JSON или `?format=m3u` |
| POST | `/api/playlists/{id}/play` | играть с позиции |
| POST | `/api/playlists/from-favorites` | скопировать избранное |

## Mixes & playlists

| Method | Path | Описание |
|--------|------|----------|
| GET | `/api/mixes` | полки |
| POST | `/api/mixes/{kind}/play` | играть микс (в т.ч. daily) |
| GET | `/api/later` | отложенные |
| POST | `/api/later` | `{track_id}` |
| DELETE | `/api/later` | `{track_id}` |

## Favorites & recommend

| Method | Path | Описание |
|--------|------|----------|
| GET | `/api/favorites` | tracks / artists / albums |
| POST | `/api/favorites` | добавить |
| DELETE | `/api/favorites` | убрать |
| POST | `/api/favorites/toggle` | toggle |
| GET | `/api/favorites/status` | `?type=track\|artist\|album&…` → `{favorited}` |
| GET | `/api/recommend/seed` | `?type=&track_id=\|artist=\|album=` → похожие треки |
| GET | `/api/recommend/favorites` | по центроидам избранного |
| GET | `/api/similar/artists` | похожие артисты |
| GET | `/api/similar/albums` | похожие альбомы |

## Jobs & discover

| Method | Path | Описание |
|--------|------|----------|
| GET | `/api/jobs` | `?status=` список |
| GET | `/api/jobs/{id}` | статус |
| POST | `/api/jobs/{kind}` | scan\|embed\|clusters\|daily\|album_tips\|full_rescan\|mix_pack; ответ содержит одинаковые aliases `id` и `job_id` |
| POST | `/api/library/upload` | multipart: `file` + `path` (относительный путь внутри библиотеки) |
| POST | `/api/library/rescan` | enqueue full_rescan; ответ содержит одинаковые aliases `id` и `job_id` |
| GET | `/api/discover/albums` | new album tips |
| GET | `/api/discover/resurfaced` | старый каталог |
| GET | `/api/metrics/weekly` | outcome/source/daypart/maturity/date за 7 дней, интервалы 95% и готовность baseline |
| GET | `/api/metrics/recommendations` | 90-дневные outcomes, p50/p95/p99 latency, last_policy, recent_requests, training_runs и состояние explore/bandit |

## Events `POST /api/events`

```json
{
  "type": "track_start|progress|track_end|skip|like|dislike",
  "event_id": "client-stable-id-for-retries",
  "impression_id": "128-bit-id-from-current-or-queue",
  "client_id": "stable-browser-id",
  "device_id": "Linux x86_64",
  "track_id": 83,
  "session_id": "...",
  "position_sec": 12.5,
  "duration_sec": 240,
  "listened_sec": 12.5,
  "reason": "completed|skipped|next"
}
```

Новый radio-клиент обязан вернуть `impression_id` из current/queue и повторять
тот же `event_id` при retry. Для старых клиентов сервер ищет единственный
последний pending impression по `(session_id, track_id)`; неоднозначность
возвращает `409 ambiguous_impression`, а не приписывает feedback случайной
рекомендации. Повторный start/end/skip не меняет impression или `track_stats`.
Public share-radio не пишет owner events, transitions, taste или метрики.

Ответ на skip/track_end: `{ok, next, queue, maturity, signed_weight, session_id, …}`.

## Сессии и вкус

- Каждая вкладка — свой `session_id` из `/api/radio/start`, `/api/play`, `/api/session/start`.
- Сессии персистятся в выбранной БД (`play_sessions`) и переживают рестарт player.
- Вкус использует отдельные long/daypart/session positive-векторы. Малоданный
  daypart откатывается к long/session; session state переживает рестарт.
  Early skip попадает только в session negative prototypes, dislike — в
  persistent negative prototypes и не вычитается из positive taste.
- `transitions` участвуют в score очереди аддитивно (`+ λ · norm(log1p(weight))`); без данных поведение как раньше.
- Радио строит очередь одним ядром `candidates → features → score → select`.
  Explore-доля сначала статическая; Thompson sampling включается только после
  минимума разрешённых исходов на каждый exploratory source и остаётся внутри
  `explore_lo`/`explore_hi`.
- `offline_report` — Python CLI, не затирает global.

## Maturity

| maturity | условие | поведение |
|----------|---------|-----------|
| `discovering` | n_positive &lt; forming_at (default 3) | выше explore, больше новых и соседних источников |
| `forming` | &lt; ready_at (default 8) | смесь |
| `ready` | ≥ ready_at | вкус + continuity; старт радио — sample из top-K |

Пороги настраиваются через `MUSIK_PROFILE_FORMING_AT` и `MUSIK_PROFILE_READY_AT`.

## Share radio

`POST /api/share/radio` → `{ url, token }` — URL вида `http://host:8787/listen/<token>.mp3`.  
Вставь в VLC / браузер / любой HTTP-аудио клиент. Поток бесконечный (перекодирование через ffmpeg в MP3).  
Слушатели **не** обновляют вкус владельца. Лимит параллельных слушателей: `MUSIK_SHARE_MAX_LISTENERS` (default 4).

Для абсолютных URL с LAN задай `MUSIK_PUBLIC_BASE_URL`.

Env: `MUSIK_PASSWORD`, `MUSIK_API_TOKEN`, `MUSIK_SESSION_SECRET`, `MUSIK_AUTH_DISABLED`, `MUSIK_SECURE_COOKIE`, `MUSIK_PUBLIC_BASE_URL`, `MUSIK_FFMPEG`, `MUSIK_SHARE_BITRATE`, `MUSIK_SHARE_MAX_LISTENERS`, `MUSIK_PROFILE_*`, `MUSIK_WORKER_URL`.
