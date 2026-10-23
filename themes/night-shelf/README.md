# Night Record Shelf

Самостоятельная тема для musik. Она сохраняет визуальный стиль ветки `home`,
но поставляется как пакет и не меняет HTML, JavaScript или базовые стили upstream.

Установка в запущенный Docker Compose сервис без пересборки:

```sh
docker compose cp themes/night-shelf player:/data/themes/night-shelf
```

Открой Профиль и выбери **Night Record Shelf**. Upstream тема **Retro** и другие
установленные темы остаются доступны. Файлы темы изолированы в этом каталоге;
редактирование или добавление другой темы не затрагивает код плеера.

Шрифты Unbounded и IBM Plex включены локально, без внешних запросов. Они
распространяются по SIL Open Font License 1.1; авторы и лицензия указаны в
`fonts/NOTICE`.
