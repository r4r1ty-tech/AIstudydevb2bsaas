# Архитектура

Документ описывает, как устроен проект: процессы, пакеты, схему данных и ключевые сценарии. Замысел и продуктовые решения — в [../PLAN.md](../PLAN.md). Эксплуатация — в [OPERATIONS.md](OPERATIONS.md).

## Процессы

Четыре независимых бинарника, общая sqlite (WAL, `busy_timeout=8000`, `MaxOpenConns=1`), один `.env`. Общая библиотека — `internal/*`; в проде как отдельные процессы не запускается.

| Бинарник | Вход | Пишет в БД | Слушает порт |
| --- | --- | --- | --- |
| `tg` | Telegram Bot API (long polling) | `settings` (`dl_sent:*`); читает `lecture_packs`, `pack_lessons`, `moodle_items`, `accounts`, `users` | нет |
| `rasp` | `ssau.ru/rasp`, `lk.ssau.ru`, `do.ssau.ru/moodle`, провайдеры ИИ | `lessons`, `parse_runs`, `events`, `accounts`, `moodle_items`, `llm_usage`, `settings`, `quiz-debug/*.json` | нет |
| `panel` | HTTP (Mini App + API) | `users`, `join_intents`, `bbb_links`, `accounts`, `web_sessions`, `llm_keys`, `llm_usage`, `settings` | да (`LISTEN_ADDR`, на проде `127.0.0.1:8080` за Caddy) |
| `bbb` | `bbb.ssau.ru` (HTTP/DDP/WebRTC), ffmpeg, Vosk | `presence`, `events`, `lecture_packs`, `pack_lessons`, `llm_usage`, `settings` | нет |

`ssau.target` поднимает все четыре. `panel` — единственный, кому нужен входящий TLS: на проде его даёт Caddy с сертификатом Let's Encrypt (`ssau.r4r1ty.fun`). `ssau-tunnel.service` (cloudflared quick tunnel) — запасной вариант без своего домена, включается только без `PANEL_TUNNEL=0` и в `ssau.target` не входит.

## Карта пакетов

```
cmd/{tg,rasp,panel,bbb}   тонкие main: собрать зависимости, запустить Run/Refresh/Listen
cmd/ssau-keys             интерактивная смена ключа ИИ (cheaperinference) и токена бота на проде
cmd/unicheck              разовая проверка клиентов ЛК и Moodle на живых сайтах
internal/app              общий старт: .env, логи, sqlite, tz, signal-контекст
internal/config           загрузка .env и env, вайтлист, дефолты
internal/logx             уровни, формат, stderr + файл
internal/store            sqlite: пользователи, пары, интенты, presence, events, паки, настройки
internal/model            доменные типы и чистые методы (User, Lesson, JoinIntent, …)
internal/rasp             Fetch (HTTP+UA+cookie), Parse (goquery), Diff, Refresh, cron
internal/tg               Bot: личный пароль в панель, напоминания о сроках Moodle, кнопка PDF под «конспект готов»
internal/bbb              воркер захода: DirectJoiner (Greenlight HTTP, Meteor DDP, Pion WebRTC), лобби, слайды, запись, spotter
internal/capture          OpusRec: прямой дамп RTP Opus в .ogg + декод PCM 16 кГц через ffmpeg (без PulseAudio)
internal/wake             вейкворды: Vosk через wake.py (свободное распознавание) + поиск слов и их форм
internal/notes            после пары: 4-стадийный кэш, STT (Deepgram -> Gemini-пул -> cheaper -> AssemblyAI/Fish/Groq), vision батчами, PDF (typst, пакеты cmarker и mitex вшиты)
internal/archive          имена файлов и путей записей, слайдов, конспектов
internal/notify           отправка сообщений в Telegram без зависимости от tg-процесса
internal/webapp           Mini App: статика (с safeHref) + JSON API + вход по личному паролю + web_sessions + rate-limit
internal/panelpass        личные часовые пароли в панель (HMAC от ключа vault, id и часа)
internal/access           кто админ (строго ADMIN_TELEGRAM_ID), мост ключей/лимитов (InitLLMBridge), цепочки ИИ, ФИО из ЛК
internal/llm              провайдеры ИИ (Gemini, OpenCode Zen, cheaper), KeyPool (429 cooldown + семафор 2/ключ), AskParts/AskImage/AskAudio
internal/quizlog          журнал прогонов решателя тестов (<папка базы>/quiz-debug)
internal/lk               клиент lk.ssau.ru: вход (server action Next.js), расписание, ФИО со страницы /account
internal/moodle           клиент do.ssau.ru/moodle: вход по форме, курсы, лента сроков, разбор заданий и тестов
internal/uni              синк по аккаунтам: расписание и ФИО из ЛК, задания Moodle, авторешение тестов
internal/vault            AES-GCM для паролей и сессий аккаунтов (ключ вне базы)
internal/websess          HTTP-сессия «как браузер» с выгрузкой кук в строку
```

## Схема данных (sqlite)

| Таблица | Ключ | Что хранит |
| --- | --- | --- |
| `users` | `telegram_id` | имя для BBB, подгруппа, on/off, `disabled_until`, вейкворды, признак «первый вход пройден» (создаётся в панели) |
| `lessons` | `id` | день, слот, дисциплина, препод, место, подгруппа, тип, `online` |
| `join_intents` | `(telegram_id, lesson_id)` | решение «заходить / не заходить» из панели: `pending/yes/no`, `asked_at`, `decided_at` |
| `presence` | `telegram_id` | текущее состояние в BBB: `none/lobby/room/error` + сообщение |
| `events` | `id` | лента для вкладки «Логи»: join/leave/lobby/wake/reparse/error/… |
| `bbb_links` | `key` | ссылка комнаты. `SetLessonBBB` пишет `lesson:<id>` и `room|предмет|препод|тип`; `model.ResolveBBB` (воркер, панель): своя `lesson:<id>` (пустая — снята), иначе `l.BBBURL` из ЛК, иначе `room|…` по типу пары, иначе `<group>|предмет|препод`. Ссылки задаются в панели (и в ЛК) |
| `lecture_packs` | `lesson_id` | запись/слайды/конспект: статус, пути, `meeting_key` (частичный уникальный индекс `idx_lecture_packs_meeting_key`), `claimed_at` (45-мин lease), ошибка |
| `pack_lessons` | `lesson_id` | алиасы `lesson_id -> pack_id`, связывающие несколько строк `lessons` (подгруппы, ИОТ) с одним паком лекции |
| `web_sessions` | `token_hash` | долгоживущие сессии панели (`telegram_id`, `created_at`, `expires_at`) |
| `llm_keys` | `(telegram_id, provider)` | личные ключи пользователей (`secret_enc` через `vault`, `mask`, `share_pool`, `updated_at`) |
| `llm_usage` | `(telegram_id, provider, day)` | учёт вызовов за UTC-день (`requests`, `input_tokens`, `output_tokens`, `cooldown_until`, `updated_at`) |
| `parse_runs` | `id` | последний прогон парсера: время, ок/статус, счётчики, дифф |
| `accounts` | `(telegram_id, kind)` | аккаунты универа `lk`/`moodle`: логин, пароль и куки (зашифрованы), статус `pending/ok/bad_login/error`, `synced_at` |
| `moodle_items` | `(telegram_id, cmid)` | задания и тесты Moodle: курс, раздел, тип, срок открытия и сдачи, статус `todo/draft/submitted/done`, «что сделать» |
| `settings` | `key` | `test_join`, `group_id`, `rasp_refresh`, `slides_done:*`, `notes_done:*`, `notes_attempts:*`, `dl_sent:*`, `lk_name:<tg>`/`lk_name_at:<tg>` (ФИО из ЛК), `llm_choice:<tg>` (выбор модели), `quiz_solved/quiz_fail/quiz_parsed/quiz_force:<tg>:<cmid>`, `moodle_kick:<tg>`, `moodle_wake_kick` |

## Ключевые сценарии

### Парсер расписания

`rasp.Refresh` (`internal/rasp/refresh.go`): `Fetch` (browser UA, cookie jar, при 403 повтор на `www.`), `Parse` (goquery: `.schedule__*`), при наличии — добор следующей недели, `Diff` и `ReplaceLessons` (upsert по `Identity()` = дата+начало+предмет+тип+препод+подгруппа и `+ |o<Owner>` для ИОТ: смена аудитории, препода, типа или online обновляет пару на месте, id не меняется; удалённые пары уносят свои согласия и presence, а их ссылка переезжает в `room|…`; паки лекций остаются). Результат пишется в `parse_runs` и `events`. `StartCron` тикает раз в 30 с и срабатывает в 07/15/19/22, а при источнике из ЛК ещё каждый час с 07 до 21, или по kick из панели (`settings.rasp_refresh`). Панель дергает kick и ждёт появления нового `parse_runs` (`rasp.WaitRun`). В `cmd/rasp/main.go` горутина `uni.Syncer.Run` дожидается завершения через `defer wg.Wait()` перед закрытием БД при shutdown.

### ЛК и Moodle

`uni.Syncer` живёт в процессе `rasp`. `Lessons` — источник для `rasp.Refresher.Source`: по аккаунту ЛК (админ первым) берёт группу, учебный год (`statements/student/years`), начало года (кэш `settings.lk_year_start:<год>`) и две недели `timetable/get-timetable`. Из ответа: подгруппа своей группы у пары, `weeks[].isOnline` и аудитория именно этой недели, `conference.url` → `lessons.bbb_url`, пары ИОТ (`ietLessons`) → `lessons.owner` = telegram id студента; у остальных аккаунтов берутся только их ИОТ. `Lesson.For(user)` прячет чужие ИОТ и чужую подгруппу. `ResolveBBB`: ручная ссылка пары, затем ссылка из ЛК, затем комнаты. ЛК недоступен — Refresher идёт на `ssau.ru/rasp`, как раньше.

Сессии: куки после входа шифруются и пишутся в `accounts.session`; следующий синк поднимает их и логинится по паролю, только если сайт ответил «не авторизован».

ФИО: при синке ЛК раз в сутки `lk.FullName` берёт `fullName` со страницы `/account` и кладёт в `settings.lk_name:<tg>` (пустое «имя для BBB» заполняется им же). `access.Name` подписывает людей в панели, журнале решателя и сообщении бота: ФИО из ЛК → имя для BBB → @ник → id.

Moodle: курсы (`core_course_get_enrolled_courses_by_timeline_classification`), лента `core_calendar_get_action_events_by_timesort` (по 50, это и есть «несделанное»), состав курса из HTML `course/view.php` (assign/quiz/lesson/workshop), страница элемента → сроки (`activity-dates`), условия выполнения, статус (`submissionstatustable`, `quizattemptsummary`), попытки и лимит времени, начало описания. Новые задания со сроком впереди приходят сообщением. Бот (`tg`) напоминает о сроках за 24 и 3 часа (`settings.dl_sent:*`); список «Сдать» — в панели (`storeMoodle` читает `moodle_items`).

### Авторешение тестов Moodle

`uni.detectAndHandleOpenedQuizzes` на каждом синке Moodle аккаунта (`internal/uni/quiz_solve.go`) запускает параллельных воркеров (`MOODLE_WORKERS`, по умолчанию `4`), отдавая приоритет задачам с `moodle_kick` / `moodle_wake_kick` (`sortMoodleTasks`):

- тест открылся (был закрыт или новый) или пришёл `moodle_wake_kick` от вейкворда на паре → «Открылся тест в Moodle!» и `processQuiz`;
- прошлая попытка упала (`quiz_fail` непуст) → тихий повтор на каждом обходе;
- кнопка «Пройти» в панели (`quiz_force:<tg>:<cmid>` + `moodle_kick`) → `processQuiz(forced)` сразу, даже для просроченного теста; почему не вышло, человеку сообщается всегда.

`processQuiz` берёт межворкерную блокировку `TryLockQuiz` (`<tgID>:<quizID>`), учитывает оставшееся время попытки (`TimeLeft` с резервом 75 с, ускорение `FastMode` при `< 3 мин`), решает страницы параллельно (до 4 страниц и до 4 картинок на страницу одновременно) и отправляет ответы строго по возрастанию номера страницы (поддерживается и `sequential`-навигация). Цепочка ИИ — `access.QuizChainWithVault` (личные ключи из БД + подписанные ключи `.env`). Подробности — в [MOODLE_QUIZ.md](MOODLE_QUIZ.md).

### ИИ

`internal/llm`: все провайдеры — OpenAI-совместимый `chat/completions` (для мультимодальности — `Part` / `AskParts` / `AskImage` / `AskAudio`, ограничение части `MaxMediaPartBytes = 20 MiB`).

| Провайдер | Ключи | Модели (от лучшей к быстрой) | Кому |
| --- | --- | --- | --- |
| `gemini` (Google AI Studio, бесплатный тариф) | `GEMINI_API_KEYS` + личные (`share_pool=1`) | `gemini-4-hydrogen`, `gemini-3.6-flash`, `gemini-3.5-flash-lite` | всем |
| `opencode` (OpenCode Zen, бесплатные модели) | `OPENCODE_API_KEYS`, `OPENCODE_API_KEY` + личные (`share_pool=1`) | `glm-5.3-flash-free` | всем |
| `cheaper` (`LLM_API_*`) | один | `muse-spark-1.3-contributor`, `glm-5.3-flash` (+ `LLM_MODEL`) | только админ |

- **Ключи и пул:** ключи из `.env` подписываются владельцем (`Агеев:ключ`), личные ключи пользователей хранятся зашифрованными в `llm_keys` (`vault` + `MaskKey`, проверка изолированной пробой `TestProviderKey`). Во всех процессах (`cmd/bbb`, `cmd/rasp`, `webapp.New`) регистрируется `access.InitLLMBridge(st, v)`: отдаёт в пул расшаренные ключи (`share_pool=1` для `gemini`/`opencode`) и пишет статистику/кулдауны в `llm_usage` за UTC-день (ФИО владельца кэшируется на 5 мин в `ownerLookupCache`). Секреты в панель и логи не попадают (`Provider.Public`, `scrub`), маски/хвосты своих ключей видит владелец, всё остальное — только админ.
- **Изоляция цепочек:**
  - `PoolChain(cfg)` — общая бесплатная цепочка для конспектов, STT и vision (`Gemini` и `OpenCode` со случайным порядком ключей внутри модели); `Cheaper` и `AdminOnly` таргеты в неё **никогда** не попадают.
  - `ChainForWithUserKeys` / `access.QuizChainWithVault` — цепочка для тестов: не-админ жёстко изолирован на своих ключах (личный из `llm_keys` и/или подписанный фамилией из `.env`), чужих ключей и `cheaper` нет; админ получает свой выбор, затем `cheaper`, затем пул.
- **Управление нагрузкой (`KeyPool`, `internal/llm/pool.go`):** на каждый секрет стоит семафор максимум 2 одновременных запроса (`MaxConcurrentPerKey = 2`) и отслеживание HTTP 429 (`DefaultRateLimitCooldown = 30s`, `MaxRateLimitCooldown = 15m`, парсинг `Retry-After` и `retryDelay`/`retry_delay`/`retry in`). `askChain` сначала пробует ключи вне cooldown и со свободным слотом (`tryAcquire`), затем ждёт занятый ключ; если все в cooldown — `ErrAllCooldown`. Таймаут одного вызова `PerCallTimeout = 4m`.
- **Медиа-возможности (`internal/llm/media.go`):** `SupportsAudio` есть только у Gemini (`GeminiPoolChain`), `SupportsVision` — у Gemini и `Cheaper` (OpenCode без медиа).

### Решение по паре

Карточек T-15 в боте больше нет. Решение «заходить / не заходить» человек ставит в панели (`POST /api/intent`, `POST /api/day`), оно пишется в `join_intents` (`yes/no`). Нет записи или `pending` — молчание = заходить.

### Заход на пару

`bbb.Worker.Run` (`internal/bbb/worker.go`) тикает каждые 15 с. На каждом тике:

1. `LessonsInJoinWindow` — пары, которые идут сейчас или начнутся в ближайшие 15 минут.
2. Задачи группируются по `meetingGroupKey` = `model.MeetingKey(lesson, rawURL)` (`bbb:<нормализованный URL>|<дата>|<начало>`, с фоллбэком `lesson:<id>`): одна и та же онлайн-лекция у разных подгрупп и ИОТ (`Owner`) сводится в одну группу встречи.
3. Для каждого активного пользователя подходящей подгруппы, который хочет зайти (`WantsJoin`) и не заблокирован, собирается `pending`-задача.
4. Нет ссылки BBB → `missingBBB` (presence=error + событие + сообщение пользователю; админу — одно сообщение на пару), задача не создаётся.
5. `ShouldBeInRoom` (окно входа `EnterAt` … `leaveAt`) решает, пора ли заходить или уже выходить (`leaveAt` — случайные 0–5 минут до конца слота; у пишущей вкладки — ровно конец пары). После выхода по времени ключ помечается `done` и до конца пары не заходит снова.
6. Одна запись на встречу: `chooseRecorder` держит одну пишущую сессию на группу `meetingGroupKey` (сохраняет живой рекордер, иначе выбирает участника с самым ранним `seatedAt` среди живых или самым ранним `EnterAt`, tie-break по `TelegramID` и `lesson.ID`). Выпавший рекордер передаёт запись живому участнику (`promoteRecorder`), а `reserveRecorder`/`recJoining` не позволяют двум горутинам одновременно стартовать второй рекордер. При ошибке старта записи следующая попытка не раньше чем через 2 минуты (`recRetryGap`).
7. Любой сбой БД в тике — живые сессии не трогаем (иначе один `SQLITE_BUSY` выкинет всех и оборвёт запись). Состояние ключей ушедших из окна пар и их залипшие presence чистит `forget`.
8. Каждый заход запускается **в отдельной горутине**; тик не блокируется. `beginJoin/endJoin` — per-key guard против дублей, `joinWG` ждёт заходы при shutdown.

`ensureIn` (там же): `DirectJoiner.Join` → вход через Greenlight HTTP (`csrf-token`, форма `[join_name]`) → `/bigbluebutton/api/enter` (room или `guestWait` лобби) → SockJS Meteor DDP (`validateAuthToken` + подписки) → `SetPresence` + событие. Отменённый контекст (рестарт) — не неудача. Если пока шёл заход нажали «Не сегодня», сессия закрывается (`userLeftMeeting`). При ошибке — `joinFail`: пауза 30 с / 60 с и новая попытка, после 3 неудач подряд — стоп до нового `JoinYes` (пользователю — без сырых ошибок, причину получает админ). Живая сессия считается мёртвой после 3 ошибок проверки подряд; первый `dropDead` — тихий перезаход, второй — стоп. При старте и остановке `bbb` presence очищается целиком.

### Звук пишущей сессии

Пишущая (или тестовая `listen`) сессия поднимает аудио только через `EnsureAudio` (`internal/bbb/audio.go`): `wss://<host>/bbb-webrtc-sfu?sessionToken=…` + `pion/webrtc/v4` (`listenOnlyOffering: false`, `role: "recv"`, `type: "audio"`). В лобби аудио не ждём; после перехода в комнату и затем каждый тик `checkAudio` проверяет `AudioActive()` (или переподключает SFU), около минуты без звука — одно сообщение админу.

### Форма гостя и лобби

`internal/bbb/direct.go`: Greenlight HTML разбирается через `goquery` (`meta[name=csrf-token]`, форма с `[join_name]` или пригласительная страница). Если сервер направляет на `guestWait` или `/enter` возвращает `guestStatus == "WAIT"`, сессия остаётся в состоянии `InLobby` и каждые несколько секунд опрашивает `/bigbluebutton/api/enter`; как только `guestStatus == "ALLOW"`, доподнимает DDP и переходит в `InRoom`.

### Лобби

`watchLobby` на каждом тике проверяет живую сессию и пишет presence/приветствие только при смене состояния. Лобби до начала пары — норма; лобби дольше 2 минут после начала — алерт пользователю один раз и админу не чаще раза в 20 минут, событие `lobby`.

### Прокси на сессию

`internal/proxyrelay` читает `PROXY_FILE` (`login:password@ip:port`) и на каждый адрес поднимает локальный SOCKS5-релей без авторизации, который сам ходит вверх по SOCKS5 с логином/паролем. Каждая сессия `DirectJoiner.Join` берёт следующий прокси из пула по кругу (`proxyrelay.Pool.Next`), ходит через его `LocalAddr()` по HTTP и WebSocket (`golang.org/x/net/proxy`), логирует только `host:port`. Прокси освобождается при закрытии сессии. Файла нет — все идут напрямую. Кривая строка файла пропускается, остальные прокси работают. Прокси штрафуется только за сетевые ошибки (HTTP/WS-соединение), а не за форму или лобби; три неудачи подряд — пауза 10 минут, потом пробная попытка. Если прокси настроены, но все на паузе, заход — ошибка (громко), а не тихий заход с IP сервера. NB: SOCKS5 проксирует TCP (HTTP/DDP/SFU-сигналинг); media WebRTC идёт по ICE/UDP напрямую или через TURN.

### Тестовый заход

Отдельная сущность `settings.test_join` (ссылка, имя гостя, режим `dummy/listen`, статус). Управляется из панели (`/api/test`, «Служебное → Тест»), ход теста приходит админу сообщениями. Пока идёт пара, тест останавливается (`pauseTest`: ставит явную ошибку «идёт пара: воркер занят лекцией» и один раз уведомляет админа). Тест-заход тоже выполняется в горутине с guard'ом. Неудачи считаются в памяти: админу — первая причина, повторы с бэкоффом, после 3 подряд `Want=off` и одно сообщение «останавливаюсь».

### Запись, слайды и вейкворды

Для каждой встречи выбирается один рекордер (`chooseRecorder`). `attachRecorder` создаёт или переиспользует общий пак через `EnsurePack` (`PackByLesson` -> `PackByMeetingKey` + таблица алиасов `pack_lessons`, при появлении `meeting_key` позже — перепривязка `BindLessonToPack`; при старте БД старые дубликаты сливаются в `migrateDeduplicatePacks`). Рекордер запускает `startSessionRecorder` (`StreamRecorder.StartRecording` → `capture.StartOpus`: прямой дамп incoming RTP `audio/opus` в `.ogg` и decode-поток через `ffmpeg` в PCM 16 кГц для пайпа), инкрементально скачивает слайды каждые 45 с и перед выходом в `GrabSlides` (картинки + `slides.json` манифест с полями `FirstShownAt`, `ShownOffsetS`, `Timestamp` и текстовым слоем `Text` с фильтрацией небезопасных имён `isSafeSlideFilename`) и поднимает `startSpotter`. Споттер (`internal/bbb/spot.go`) гоняет PCM через `internal/wake` и на совпадении шлёт вейкворд пользователям, а также выставляет `moodle_wake_kick` / `moodle_kick:<id>` для немедленной проверки открывшихся тестов в Moodle. Один поток на комнату, без PulseAudio.

### После пары

Сразу после окончания лекции (в тот же день, когда нет активных сессий) `maybeNotes` собирает конспект. Для защиты от гонок и повторной сборки вызывается `ClaimPackForNotes` (атомарный lease на `PackClaimLease = 45m` по полю `claimed_at`) и `ReleasePackClaim` (`SavePack` также сбрасывает `claimed_at` в финальных статусах `done`, `empty`, `error`), а `maybeHarvest` добирает слайды, если во время пары они не сохранились.

Пайплайн `notes.Build` (`internal/notes/run.go`) разбит на 4 стадии с инкрементальным кэшем `.build_cache.json` (прогресс не теряется при обрыве на следующем шаге):

1. `stepTranscript` — проверка аудио `capture.Probe` (минимум 1 минута `minAudioLen`, пик не тише −50 dBFS `minAudioPeak`, плотность речи ≥ 60 симв/мин `minCharsPerMinute`; иначе пак закрывается как `empty` без вызовов API и рассылки). Кэш `transcript.txt` переиспользуется, если длина/размер аудио совместимы (`< max(5m, 10%)` и размер ±20%). STT (`internal/notes/fish.go`, `gemini_stt.go`): сначала Deepgram (если задан ключ), затем бесплатный пул Gemini (`transcribeGemini` — нарезка `ffmpeg` в mp3 16 кГц моно 64 kbps чанками по 10 мин с перекрытием 8 с, одиночный чанк до 12 мин, хвост < 15 с склеивается; параллельные воркеры по числу ключей, ротация ключей, сдвиг таймкодов и склейка перекрытий; кусок, который Gemini не осилил, идёт в Deepgram, затем в cheaper), где `deepgram` (`DEEPGRAM_API_KEY`, `internal/notes/deepgram_stt.go`) → `cheaper` (`LLM_API_*`), затем платные фоллбэки `assemblyai` → `fish` → `groq`.
2. `stepSlides` — `DescribeSlides` (`internal/notes/vision.go`): до 60 слайдов (`maxVisionSlides`) батчами по 6 (`visionBatchSize`) через `MediaTargets(PoolChain(cfg), false, true)`, фоллбэк при пустом пуле на `legacyVisionProviders` (Groq/Grok), а при ошибке батча или пропуске слайда — поштучный фоллбэк на текстовый слой `Text` из `slides.json` (с таймкодами `FirstShownAt`/`Timestamp`). Кэш в `.slides_desc.txt` по SHA-256 `SlidesHash` (картинки + `slides.json`).
3. `stepNotes` — `Summarize` через `PoolChain(cfg)` (кэш по `SummarizeInputHash` от дисциплины, номера, транскрипта и слайдов).
4. `stepPDF` — `WritePDF` (`internal/notes/pdf.go`: разовый `typst compile` с Typst-шаблоном `internal/notes/typst/main.typ`; Markdown разбирает пакет `cmarker` 0.1.10, LaTeX-формулы переводит `mitex` 0.2.7, оба вшиты в бинарник через `go:embed` и распаковываются во временный каталог, сеть не нужна; текст конспекта передаётся файлом `notes.md`, а не кодом шаблона). Если mitex не разбирает формулу, сборка падает целиком: тогда такие формулы находятся по одной и печатаются исходником, остальной конспект собирается. Таймаут 3 мин; typst запускается в отдельной группе процессов `Setpgid: true` и завершается через `syscall.Kill(-pid, SIGKILL)`. Бинарник: `TYPST_BIN` (по умолчанию `typst` из PATH). Кэш по `PDFNotesHash`/`NotesMDHash`.

Готовый PDF рассылается таргетированно: `announceNotes` через `noteRecipients` проверяет все привязанные к паку `PackLessonIDs` и фильтр `lessons[i].For(u)` (подгруппа и ИОТ `Owner`), с фоллбэком на историю присутствия в `events` (`participantsByHistory`) или админа — личный конспект ИОТ не уходит всей группе. В Telegram приходит уведомление с кнопкой «Скачать PDF» (обрабатывает `tg`), а `notes.md` и `transcript.txt` сразу доступны решателю тестов Moodle. При ошибке STT/LLM пак остаётся в `error`: 3 повтора через 10 мин, затем раз в 2 часа, всего до 10 попыток (`settings.notes_attempts:<pack>`); `maybeNotes` догоняет последние 7 дней (`noteCatchUpDays`), у которых ещё не стоит `notes_done`.

Вейкворды: `wake.py` распознаёт всю речь без закрытого словаря (с грамматикой из нескольких слов Vosk подгонял под них любую фразу) и отдаёт законченные фразы; `wake.Match` ищет в них слова пользователей вместе с формами («контрольная» → «контрольную»), но не однокоренные («тест» не ловит «тестирование»).

### Telegram-бот

Бот (`internal/tg`) делает только две вещи: выдаёт личный пароль в панель и шлёт уведомления. Отвечает лишь вайтлисту и админу, остальным молчит.

- `/start` — короткое «здесь только пароль и уведомления» и одна кнопка внизу «🔑 Пароль в панель»; `/pass` — то же. Ответ: пароль, «действует до ЧЧ:ММ», кнопка «Открыть панель». Слева от поля ввода у каждого кнопка меню «Панель».
- Любой другой текст — подсказка, что всё остальное в панели; старые команды — молчание.
- Сам `tg` шлёт напоминания о сроках Moodle и по кнопке «Скачать PDF» под «конспект готов» присылает файл. Остальные уведомления шлют `bbb` (нет ссылки, не удалось зайти, лобби, вейкворд, конспект готов, тревоги записи админу) и `rasp` (новое в Moodle, открылся тест, тест решён, проблема с тестом, сбой парсера админу) напрямую через `internal/notify` тем же токеном.
- `notify.HTML` и `tg` шлют с `parse_mode=HTML` и экранируют динамику; `notify.User`/`UserMarkup` — простым текстом.
- Карточек T-15, live-карточек статуса, онбординга, меню пар/конспектов/профиля, `/connect`, `/test`, `/panel` больше нет — всё это в панели.

### Панель и безопасность

`internal/webapp`: статика (`static/`: `index.html`, `css/`, `js/`, шрифты и картинки вшиты в бинарник; все динамические ссылки проходят через `safeHref` в `static/js/dom.js`, допускающий только `#...`, `/...`, `http:`, `https:`, `mailto:`) + JSON API.

- **Аутентификация и rate-limit:** кука долгой сессии `panel_session` (`session.go`, `web_sessions`: sha256 токена, Telegram id, скользящий TTL 30 дней / потолок 90; выдаётся в `POST /api/login`, гасится в `POST /api/logout`), либо заголовок с личным часовым паролем (`internal/panelpass`) / общим `PANEL_PASSWORD` (вход админом). `POST /api/login` защищён IP-ограничителем `loginLimiter`: 5 ошибок за 10 минут блокируют IP на 5 минут (`429 Too Many Requests` + `Retry-After`). Фильтр по IP (`PANEL_ALLOW_IPS`, `PANEL_TRUST_PROXY`) стоит до всего; вход по одному IP без пароля разрешён, только если ключ хранилища не настроен (fail-closed -> 401).
- **Права и изоляция:** `viewerIsAdmin` вызывает `access.IsAdmin` — админ определяется **строго** по `ADMIN_TELEGRAM_ID` (и входу по `PANEL_PASSWORD`). Пары ИОТ (`Lesson.Owner`) изолированы через `Server.lessonVisible` и `Lesson.For(u)`: чужие ИОТ скрыты в `/api/day`, `/api/week`, `/api/lessons`, `/api/rooms`, `/api/notes` и при редактировании `POST /api/bbb`.
- **Демо-режим (`PANEL_DEMO=1`):** `NewDemo` и `sanitizeDemoConfig` принудительно обнуляют все реальные API-ключи, токены, `DBPath`, `SecretKey`/`SecretKeyFile`; пробные вызовы ИИ в демо не выполняют реальных запросов. WebSocket нет — пуллинг.

Пять экранов (hash-роутинг, нижняя панель на телефоне, боковая от 900px):

- **Сегодня** — пары дня, переключатель «заходить / не заходить» по каждой паре, режим дня, ближайшая пара;
- **Неделя** — расписание по дням, те же переключатели;
- **Сдать** — дедлайны Moodle по срочности (просрочено за 14 дней, сегодня и завтра, неделя, позже, сдано за 30 дней) и отдельный свёрнутый блок «Без срока», который в счётчики не идёт;
- **Конспекты** — паки лекций по предметам и их статус, ссылки на PDF в GitHub;
- **Ещё** — аккаунт универа (Moodle + ЛК одним логином), профиль (имя для BBB, подгруппа, свои вейкворды, вкл/выкл бота), «Мои ключи ИИ» (`static/js/ai.js`: добавление/удаление личного ключа, флаг «Делиться ключом с пулом конспектов», суточный счётчик и cooldown) + «ИИ для тестов» (выбор провайдера, модели и ключа), комнаты BBB и — только админу — «Служебное»: ИИ и ключи, Тесты Moodle (журнал решателя), Тест (заход в тестовую комнату), Сейчас, Люди, Пары, Парсер, Логи.

У тестов в **Сдать** — кнопка «Пройти» (`POST /api/quiz/force`) и кнопка массового прохождения (`POST /api/quiz/force-all`).

Первый вход: пока у «меня» нет имени для BBB и подгруппы, вместо приложения экран «Первый вход»; `POST /api/me {fio, subgroup, onboard:true}` заканчивает настройку.

«Я» — тот, чей личный пароль введён (общий пароль — `ADMIN_TELEGRAM_ID`). Решения пишутся в `join_intents`; воркер `bbb` читает их сам. Молчание = заходить.

| Метод | Путь | Что делает | Основные поля |
| --- | --- | --- | --- |
| POST | `/api/login` / `/api/logout` | Обмен пароля на `panel_session` (с rate-limit 5/10м) / сброс куки | `{password}` |
| GET | `/api/day?date=YYYY-MM-DD` | Пары дня (по умолчанию сегодня) с моим решением | `lessons[]` (`decision`, `phase`, `mine`, `will_join`, `presence`), `mode`, `current_id`, `next_id`, `upcoming`, `me`, `demo` |
| POST | `/api/day` | Решение разом по онлайн-парам моей подгруппы, которые ещё не кончились | `{date?, mode: auto\|all\|none}`, ответ как у GET |
| GET | `/api/week` | Все дни расписания | `days[]` (`date`, `mode`, `lessons[]`), `today`, `me` |
| POST | `/api/intent` | Решение по одной онлайн-паре | `{lesson_id, decision: yes\|no\|auto}` -> `{lesson, mode}`; `auto` возвращает «молчание = заходить» |
| GET | `/api/deadlines` | Дедлайны Moodle | `moodle` (`connected`, `available`, `login`, `synced_at`, `message`), `deadlines[]` (`group`, `due`, `status`, `kind`), `counts` |
| POST | `/api/moodle/login` | Подключить Moodle (и ЛК тем же логином). Пароль хранится зашифрованным, в логи не пишется | `{login, password}`; 501 — нет ключа шифрования, 422 — неверные данные |
| POST | `/api/moodle/logout` | Отключить Moodle | — |
| GET | `/api/notes` | Конспекты по предметам | `subjects[]` (`discipline`, `latest`, `notes[]` со `status`, `topic`, `pdf_url`), `repo_url`, `total` |
| GET | `/api/me` | Мой профиль | `fio`, `subgroup`, `enabled`, `active`, `reason`, `extra_words`, `wake_words` |
| POST | `/api/me` | Правка профиля, любые поля по отдельности | `{fio?, subgroup?, extra_words?, enabled?}` |
| GET | `/api/rooms` | Комнаты BBB по предметам | `rooms[]` (`key`, `discipline`, `teacher`, `kind`, `url`, `lessons`); сохранение через `POST /api/bbb {key, url}` |
| GET | `/api/settings` | Группа, часовой пояс, префикс BBB, `is_admin` | всем |
| GET | `/api/lessons` | Всё расписание (с учётом ИОТ), ссылки BBB, записи | всем |
| POST | `/api/bbb` | Ссылка на комнату (общая для группы) | `{url, lesson_id \| key \| discipline+teacher}` |
| GET | `/api/llm` | Мой выбор ИИ и цепочки | `me`, `admin`, `providers[]` (подписанные + личный `Mask`; хвосты и `cheaper` — админу), `choice`, `chain`, `default`, `notes_chain` |
| GET | `/api/llm/models?provider=` | Живой список моделей провайдера | `models`, `recommended` |
| POST | `/api/llm/choice` | Сохранить свой выбор | `{provider, model, key}`; пустой провайдер — общая цепочка; `cheaper` не админу и чужой ключ — 400 |
| POST | `/api/llm/test` | Один пробный вызов | `{provider, model, key}` -> `{ok, text, attempts}` |
| GET/PUT/DELETE | `/api/llm/keys`, `/api/llm/keys/{provider}` | Управление своими ключами ИИ (`llm_keys`) | `{provider, api_key?, share_pool}`, проверка нового ключа через `TestProviderKey`, шифрование через `vault` |
| GET | `/api/llm/limits` | Мои лимиты, суточный расход (`llm_usage`) и cooldown | `providers[]` (`requests`, `input_tokens`, `output_tokens`, `est_daily_limit`, `cooldown_until`) |
| POST | `/api/quiz/force` | «Пройти» свой тест | `{cmid}` |
| POST | `/api/quiz/force-all` | Запустить все свои открытые тесты | — |

Только админ (иначе 403): `/api/now`, `/api/people`, `POST /api/people/{id}`, `/api/parser`, `POST /api/parser/refresh`, `/api/logs`, `GET/POST /api/test`, `/api/admin/llm/people` (кто что выбрал), `/api/admin/quiz-runs` и `/api/admin/quiz-runs/{name}` (журнал решателя), `POST /api/admin/quiz/resolve {cmid, telegram_id}` (перерешать чужой тест).

Публичный адрес Mini App резолвится в `config.ResolveWebAppURL`: свежий URL туннеля из `WEBAPP_URL_FILE` важнее статичного `WEBAPP_PUBLIC_URL`. `tg` опрашивает файл каждые 20 с (`webappLoop`) и при смене переставляет кнопку «Панель» всем из вайтлиста через `setChatMenuButton` — рестарт не требуется.

## Конкурентность (bbb)

- Один тик-цикл (`Run`) + горутины заходов и фоновых джобов.
- Все карты воркера (`sessions`, `joining`, `blockedAt`, `dropRetry`, `lobbyAt`, `leaveAt`) под `Worker.mu`.
- `joining` — single-flight по ключу сессии (в т.ч. `"test"`).
- `joinWG.Wait()` в `closeAll` перед закрытием Store, чтобы горутины не писали в закрытую БД.
- Ресурсы процесса (CPU/RAM для `ffmpeg`/Vosk и постобработки) защищаются через `hogs` (freeze сторонних «тяжёлых» процессов на время пары).

## Логирование

`internal/logx` — тонкая обёртка над std `log`:

- уровни `debug/info/warn/error`, порог из `LOG_LEVEL`;
- вывод в stderr и, при `LOG_FILE`, в файл (`io.MultiWriter`);
- std `log` перенаправляется туда же, поэтому старые `log.Printf` тоже попадают в файл;
- формат строки: `[LEVEL] component: message`.

Ключевые события BBB (join/seated/fail/drop/lobby), тест-захода, ссылок, парсера, синка ЛК/Moodle, решателя тестов и вызовов ИИ (`llm:` — какой провайдер/модель/ключ, сколько думал) и HTTP-запросы панели логируются. См. [OPERATIONS.md#логи](OPERATIONS.md#логи).