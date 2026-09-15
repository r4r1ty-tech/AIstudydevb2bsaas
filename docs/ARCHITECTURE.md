# Архитектура

Документ описывает, как устроен проект: процессы, пакеты, схему данных и ключевые сценарии. Замысел и продуктовые решения — в [../PLAN.md](../PLAN.md). Эксплуатация — в [OPERATIONS.md](OPERATIONS.md).

## Процессы

Четыре независимых бинарника, общая sqlite (WAL, `busy_timeout=8000`, `MaxOpenConns=1`), один `.env`. Общая библиотека — `internal/*`; в проде как отдельные процессы не запускается.

| Бинарник | Вход | Пишет в БД | Слушает порт |
| --- | --- | --- | --- |
| `tg` | Telegram Bot API (long polling) | users, intents, presence(чтение), events, bbb_links, settings | нет |
| `rasp` | `ssau.ru/rasp` | lessons, parse_runs, events, settings | нет |
| `panel` | HTTP (Mini App + API) | users, bbb_links, settings, intents(косвенно) | да (`:8080`) |
| `bbb` | `bbb.ssau.ru`, Chromium, ffmpeg, Vosk | presence, events, lecture_packs, settings | нет |

`ssau.target` поднимает все четыре; `panel` — единственный, кому нужен входящий TLS (даёт туннель `cloudflared`/ngrok).

## Карта пакетов

```
cmd/{tg,rasp,panel,bbb}   тонкие main: собрать зависимости, запустить Run/Refresh/Listen
internal/app              общий старт: .env, логи, sqlite, tz, signal-контекст
internal/config           загрузка .env и env, вайтлист, дефолты
internal/logx             уровни, формат, stderr + файл
internal/store            sqlite: пользователи, пары, интенты, presence, events, паки, настройки
internal/model            доменные типы и чистые методы (User, Lesson, JoinIntent, …)
internal/rasp             Fetch (HTTP+UA+cookie), Parse (goquery), Diff, Refresh, cron
internal/tg               Bot: роутинг, онбординг, T-15, live-карточки, тестовая карточка
internal/bbb              воркер захода: Chrome (rod), seat, lobby, запись, spotter
internal/capture          PulseAudio null-sink + ffmpeg (ogg + PCM для споттера)
internal/wake             keyword spotting: grammar/Vosk через wake.py
internal/notes            после пары: STT (Fish/Groq), vision (Groq/Grok), PDF
internal/archive          имена файлов и путей записей, слайдов, конспектов
internal/notify           отправка сообщений в Telegram без зависимости от tg-процесса
internal/webapp           Mini App: статика + JSON API + password-gate
```

## Схема данных (sqlite)

| Таблица | Ключ | Что хранит |
| --- | --- | --- |
| `users` | `telegram_id` | ФИО для BBB, подгруппа, on/off, `disabled_until`, вейкворды, стадия онбординга |
| `lessons` | `id` | день, слот, дисциплина, препод, место, подгруппа, тип, `online` |
| `join_intents` | `(telegram_id, lesson_id)` | решение по T-15: `pending/yes/no`, `asked_at`, `decided_at` |
| `presence` | `telegram_id` | текущее состояние в BBB: `none/lobby/room/error` + сообщение |
| `events` | `id` | лента для вкладки «Логи»: join/leave/lobby/wake/reparse/error/… |
| `bbb_links` | `key` | ссылка комнаты. Ключ `lesson:<id>` (старые ключи «предмет+препод» не читаются воркером) |
| `lecture_packs` | `lesson_id` | запись/слайды/конспект: статус, пути, ошибка |
| `parse_runs` | `id` | последний прогон парсера: время, ок/статус, счётчики, дифф |
| `settings` | `key` | `test_join`, `group_id`, `rasp_refresh`, `admin_id`, `slides_done:*`, `notes_done:*` |

## Ключевые сценарии

### Парсер расписания

`rasp.Refresh` (`internal/rasp/refresh.go`): `Fetch` (browser UA, cookie jar, при 403 повтор на `www.`), `Parse` (goquery: `.schedule__*`), при наличии — добор следующей недели, `Diff` и `ReplaceLessons` (upsert по `Identity()`, старые пары удаляются). Результат пишется в `parse_runs` и `events`. `StartCron` тикает раз в 30 с и срабатывает в 07/15/19/22 или по kick из панели (`settings.rasp_refresh`). Панель дергает kick и ждёт появления нового `parse_runs` (`rasp.WaitRun`).

### T-15 и решение

`tg.t15Loop` каждые 20 с берёт пары, начинающиеся в ближайшие 15 минут, и для активных пользователей их подгруппы шлёт карточку с кнопками «Зайти/Не сегодня», создавая `join_intent(pending)`. Кнопка пишет решение (`yes/no`). Молчание = `pending`.

### Заход на пару

`bbb.Worker.Run` (`internal/bbb/worker.go`) тикает каждые 15 с. На каждом тике:

1. `LessonsInJoinWindow` — пары, которые идут сейчас или начнутся в ближайшие 15 минут.
2. Для каждого активного пользователя подходящей подгруппы, который хочет зайти (`WantsJoin`) и не заблокирован, собирается `pending`-задача.
3. Нет ссылки BBB → `missingBBB` (presence=error + событие + сообщение пользователю), задача не создаётся.
4. `ShouldBeInRoom` (окно входа `EnterAt` … `leaveAt`) решает, пора ли заходить или уже выходить (`leaveAt` — случайные 0–5 минут до конца слота).
5. Каждый заход запускается **в отдельной горутине**; тик не блокируется. `beginJoin/endJoin` — per-key guard против дублей, `joinWG` ждёт заходы при shutdown.

`ensureIn` (там же): `ChromeJoiner.Join` → `waitSeated` → классификация `seat` (room/lobby/form) → `SetPresence` + событие. При ошибке — `joinFail`: presence=error, событие, сообщение, блок до нового `JoinYes`. Если живая сессия выпала, первый `dropDead` — тихий retry, второй — блок.

### Классификация страницы и форма гостя

`internal/bbb/seat.go`: `signals` читает селекторы формы/комнаты/лобби и видимый текст. `classifySeat` по приоритету: **room → lobby-элемент → форма → текстовые метки лобби → unknown**. Форма важнее текстовых меток: если она есть, `waitSeated` вводит ФИО и жмёт join, а не «ждёт модератора».

### Лобби

`watchLobby` на каждом тике проверяет живую сессию. Переход lobby→room уведомляет пользователя. Лобби дольше 2 минут — алерт админу и событие `lobby`.

### Тестовый заход

Отдельная сущность `settings.test_join` (ссылка, имя гостя, режим `dummy/listen`, статус). Управляется из Telegram (`/test`, кнопки `tx:*`) и из панели (`/api/test`). Пока идёт пара, воркер занят лекционным Chromium: `pauseTest` останавливает тест, ставит явную ошибку «идёт пара: Chrome занят лекцией» и один раз уведомляет админа. Тест-заход тоже выполняется в горутине с guard'ом.

### Запись и вейкворды

Для лекционных пар выбирается один рекордер (`pickRecorder` — минимальный Telegram id). `attachRecorder` создаёт пак (`EnsurePack`), запускает `capture.Start` (PulseAudio null-sink + ffmpeg: ogg на диск и PCM 16 кГц в пайп) и `startSpotter`. Споттер (`internal/bbb/spot.go`) гоняет PCM через `internal/wake` и на совпадении шлёт вейкворд пользователям. Один поток на комнату.

### После пары

`maybeHarvest` (слайды скриншотами) и `maybeNotes` (STT + vision + PDF) запускаются фоновыми джобами `beginJob/endJob`. Notes ждут, пока нет живых вкладок. Готовый PDF уходит кнопкой в Telegram.

### Telegram UX

- Все сообщения отправляются с `parse_mode=HTML`; динамика (ФИО, дисциплина, препод, ссылки, имена гостя) экранируется через `internal/tg/html.go` (`esc/bold/code/hlink`). В `internal/tg/html_test.go` есть защитный тест: ни один форматтер не пропускает сырые `<`/`>` даже на враждебном вводе.
- **Единый источник статуса захода** — live-карточка `tg` (`syncLiveCard`): она редактируется на месте и показывает предмет, ссылку, остаток, статус и ФИО. `bbb` больше не шлёт дублирующие «зашёл/жду в лобби/вышел» — только действия и тревоги: нет ссылки, ошибка захода, лобби >2 мин, вейкворд, готовый конспект.
- Навигация: reply-клавиатура (Пары / Конспекты / Профиль) + inline-кнопки в карточках; настройки редактируются inline, а не новыми сообщениями.
- Команды: `/start`, `/help`, `/settings`, `/today`, `/notes`, `/cancel` (+ `/test` и `/panel` у админа). Неизвестная команда возвращает подсказку, а не тишину. `/cancel` сбрасывает ожидание ввода.

### Панель

`internal/webapp`: статика + JSON API под паролем (`X-Panel-Password`/`Bearer`). Вкладки читают `/api/now`, `/api/people`, `/api/lessons`, `/api/parser`, `/api/logs`; пульт пишет `/api/people/{id}`, `/api/bbb`, `/api/settings`, `/api/test`. WebSocket не используется — обычный пуллинг.

Публичный адрес Mini App резолвится в `config.ResolveWebAppURL`: свежий URL туннеля из `WEBAPP_URL_FILE` важнее статичного `WEBAPP_PUBLIC_URL`. `tg` опрашивает файл каждые 20 с (`webappLoop`) и при смене переставляет кнопку админу через `setChatMenuButton` — рестарт не требуется.

## Конкурентность (bbb)

- Один тик-цикл (`Run`) + горутины заходов и фоновых джобов.
- Все карты воркера (`sessions`, `joining`, `blockedAt`, `dropRetry`, `lobbyAt`, `leaveAt`) под `Worker.mu`.
- `joining` — single-flight по ключу сессии (в т.ч. `"test"`).
- `joinWG.Wait()` в `closeAll` перед закрытием Store, чтобы горутины не писали в закрытую БД.
- Ресурсы звука/Chrome считаются через `hogs` (freeze сторонних «тяжёлых» процессов на время пары).

## Логирование

`internal/logx` — тонкая обёртка над std `log`:

- уровни `debug/info/warn/error`, порог из `LOG_LEVEL`;
- вывод в stderr и, при `LOG_FILE`, в файл (`io.MultiWriter`);
- std `log` перенаправляется туда же, поэтому старые `log.Printf` тоже попадают в файл;
- формат строки: `[LEVEL] component: message`.

Ключевые события BBB (join/seated/fail/drop/lobby), тест-захода, T-15, ссылок, онбординга, парсера и HTTP-запросы панели логируются. См. [OPERATIONS.md#логи](OPERATIONS.md#логи).