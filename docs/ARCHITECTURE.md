# Архитектура

Документ описывает, как устроен проект: процессы, пакеты, схему данных и ключевые сценарии. Замысел и продуктовые решения — в [../PLAN.md](../PLAN.md). Эксплуатация — в [OPERATIONS.md](OPERATIONS.md).

## Процессы

Четыре независимых бинарника, общая sqlite (WAL, `busy_timeout=8000`, `MaxOpenConns=1`), один `.env`. Общая библиотека — `internal/*`; в проде как отдельные процессы не запускается.

| Бинарник | Вход | Пишет в БД | Слушает порт |
| --- | --- | --- | --- |
| `tg` | Telegram Bot API (long polling) | lecture_packs, moodle_items, accounts (чтение), settings | нет |
| `rasp` | `ssau.ru/rasp` | lessons, parse_runs, events, settings | нет |
| `panel` | HTTP (Mini App + API) | users, bbb_links, settings, intents(косвенно) | да (`:8080`) |
| `bbb` | `bbb.ssau.ru` (HTTP/DDP/WebRTC), ffmpeg, Vosk | presence, events, lecture_packs, settings | нет |

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
internal/tg               Bot: личный пароль в панель, напоминания о сроках Moodle, кнопка PDF под «конспект готов»
internal/bbb              воркер захода: DirectJoiner (Greenlight HTTP, Meteor DDP, Pion WebRTC), лобби, слайды, запись, spotter
internal/capture          OpusRec: прямой дамп RTP Opus в .ogg + декод PCM 16 кГц через ffmpeg (без PulseAudio)
internal/wake             вейкворды: Vosk через wake.py (свободное распознавание) + поиск слов и их форм
internal/notes            после пары: STT (AssemblyAI/Fish/Groq), vision (Groq/Grok), PDF
internal/archive          имена файлов и путей записей, слайдов, конспектов
internal/notify           отправка сообщений в Telegram без зависимости от tg-процесса
internal/webapp           Mini App: статика + JSON API + password-gate
internal/lk               клиент lk.ssau.ru: вход (server action Next.js), расписание
internal/moodle           клиент do.ssau.ru/moodle: вход по форме, курсы, лента сроков, разбор заданий и тестов
internal/uni              синк по аккаунтам: расписание из ЛК для rasp, задания Moodle
internal/vault            AES-GCM для паролей и сессий аккаунтов (ключ вне базы)
internal/websess          HTTP-сессия «как браузер» с выгрузкой кук в строку
```

## Схема данных (sqlite)

| Таблица | Ключ | Что хранит |
| --- | --- | --- |
| `users` | `telegram_id` | ФИО для BBB, подгруппа, on/off, `disabled_until`, вейкворды, стадия онбординга |
| `lessons` | `id` | день, слот, дисциплина, препод, место, подгруппа, тип, `online` |
| `join_intents` | `(telegram_id, lesson_id)` | решение по T-15: `pending/yes/no`, `asked_at`, `decided_at` |
| `presence` | `telegram_id` | текущее состояние в BBB: `none/lobby/room/error` + сообщение |
| `events` | `id` | лента для вкладки «Логи»: join/leave/lobby/wake/reparse/error/… |
| `bbb_links` | `key` | ссылка комнаты. `SetLessonBBB` пишет `lesson:<id>` и `room|предмет|препод|тип`; `model.ResolveBBB` (воркер, tg, панель): своя `lesson:<id>` (пустая — снята), иначе `room|…` по типу пары, иначе свежий ключ панели `<group>|предмет|препод`. Привязка новой ссылки в tg смотрит только на свои ссылки пар |
| `lecture_packs` | `lesson_id` | запись/слайды/конспект: статус, пути, ошибка |
| `parse_runs` | `id` | последний прогон парсера: время, ок/статус, счётчики, дифф |
| `accounts` | `(telegram_id, kind)` | аккаунты универа `lk`/`moodle`: логин, пароль и куки (зашифрованы), статус `pending/ok/bad_login/error`, `synced_at` |
| `moodle_items` | `(telegram_id, cmid)` | задания и тесты Moodle: курс, раздел, тип, срок открытия и сдачи, статус `todo/draft/submitted/done`, «что сделать» |
| `settings` | `key` | `test_join`, `group_id`, `rasp_refresh`, `admin_id`, `slides_done:*`, `notes_done:*`, `notes_attempts:*` |

## Ключевые сценарии

### Парсер расписания

`rasp.Refresh` (`internal/rasp/refresh.go`): `Fetch` (browser UA, cookie jar, при 403 повтор на `www.`), `Parse` (goquery: `.schedule__*`), при наличии — добор следующей недели, `Diff` и `ReplaceLessons` (upsert по `Identity()` = день+начало+предмет+подгруппа: смена аудитории, препода, типа или online обновляет пару на месте, id не меняется; удалённые пары уносят свои согласия и presence, а их ссылка переезжает в `room|…`; паки лекций остаются). Результат пишется в `parse_runs` и `events`. `StartCron` тикает раз в 30 с и срабатывает в 07/15/19/22 или по kick из панели (`settings.rasp_refresh`). Панель дергает kick и ждёт появления нового `parse_runs` (`rasp.WaitRun`).

### ЛК и Moodle

`uni.Syncer` живёт в процессе `rasp`. `Lessons` — источник для `rasp.Refresher.Source`: по аккаунту ЛК (админ первым) берёт группу, учебный год (`statements/student/years`), начало года (кэш `settings.lk_year_start:<год>`) и две недели `timetable/get-timetable`. Из ответа: подгруппа своей группы у пары, `weeks[].isOnline` и аудитория именно этой недели, `conference.url` → `lessons.bbb_url`, пары ИОТ (`ietLessons`) → `lessons.owner` = telegram id студента; у остальных аккаунтов берутся только их ИОТ. `Lesson.For(user)` прячет чужие ИОТ и чужую подгруппу. `ResolveBBB`: ручная ссылка пары, затем ссылка из ЛК, затем комнаты. ЛК недоступен — Refresher идёт на `ssau.ru/rasp`, как раньше.

Сессии: куки после входа шифруются и пишутся в `accounts.session`; следующий синк поднимает их и логинится по паролю, только если сайт ответил «не авторизован».

Moodle: курсы (`core_course_get_enrolled_courses_by_timeline_classification`), лента `core_calendar_get_action_events_by_timesort` (по 50, это и есть «несделанное»), состав курса из HTML `course/view.php` (assign/quiz/lesson/workshop), страница элемента → сроки (`activity-dates`), условия выполнения, статус (`submissionstatustable`, `quizattemptsummary`), попытки и лимит времени, начало описания. Новые задания со сроком впереди приходят сообщением. Когда открывается тест (`mod/quiz`, в том числе по 5-секундному триггеру `moodle_wake_kick` от вейкворда на паре), `internal/moodle/quiz.go` и `internal/uni/quiz_solve.go` заходят в попытку (`StartOrResumeAttempt`), парсят вопросы `.que`, подтягивают конспекты/транскрипты лекций (`lecture_packs`) по предмету, при наличии ключа LLM решают и завершают попытку (`SubmitAnswers` → `FinishAttempt`) и шлют отчёт в Telegram (без ключа LLM — присылают распарсенный текст вопросов и вариантов). Бот (`tg`) напоминает за 24 и 3 часа (`settings.dl_sent:*`), список «Сдать» — в панели; панель читает `moodle_items` через `storeMoodle`.

### Решение по паре

Карточек T-15 в боте больше нет. Решение «заходить / не заходить» человек ставит в панели (`POST /api/intent`, `POST /api/day`), оно пишется в `join_intents` (`yes/no`). Нет записи или `pending` — молчание = заходить.

### Заход на пару

`bbb.Worker.Run` (`internal/bbb/worker.go`) тикает каждые 15 с. На каждом тике:

1. `LessonsInJoinWindow` — пары, которые идут сейчас или начнутся в ближайшие 15 минут.
2. Для каждого активного пользователя подходящей подгруппы, который хочет зайти (`WantsJoin`) и не заблокирован, собирается `pending`-задача.
3. Нет ссылки BBB → `missingBBB` (presence=error + событие + сообщение пользователю; админу — одно сообщение на пару), задача не создаётся.
4. `ShouldBeInRoom` (окно входа `EnterAt` … `leaveAt`) решает, пора ли заходить или уже выходить (`leaveAt` — случайные 0–5 минут до конца слота; у пишущей вкладки — ровно конец пары). После выхода по времени ключ помечается `done` и до конца пары не заходит снова.
5. Лекция: `chooseRecorder` держит одну пишущую вкладку на пару. Живой рекордер остаётся, выпавший передаёт запись следующему (его presence-вкладка переоткрывается с записью). Если запись не стартовала, новая попытка — не раньше чем через 2 минуты.
6. Любой сбой БД в тике — живые сессии не трогаем (иначе один `SQLITE_BUSY` выкинет всех и оборвёт запись). Состояние ключей ушедших из окна пар и их залипшие presence чистит `forget`.
7. Каждый заход запускается **в отдельной горутине**; тик не блокируется. `beginJoin/endJoin` — per-key guard против дублей, `joinWG` ждёт заходы при shutdown.

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

Отдельная сущность `settings.test_join` (ссылка, имя гостя, режим `dummy/listen`, статус). Управляется из Telegram (`/test`, кнопки `tx:*`) и из панели (`/api/test`). Пока идёт пара, тест останавливается (`pauseTest`: ставит явную ошибку «идёт пара: воркер занят лекцией» и один раз уведомляет админа). Тест-заход тоже выполняется в горутине с guard'ом. Неудачи считаются в памяти: админу — первая причина, повторы с бэкоффом, после 3 подряд `Want=off` и одно сообщение «останавливаюсь».

### Запись, слайды и вейкворды

Для лекционных пар выбирается один рекордер (`chooseRecorder`: держит живого, иначе минимальный Telegram id). `attachRecorder` создаёт пак (`EnsurePack`), запускает `startSessionRecorder` (`StreamRecorder.StartRecording` → `capture.StartOpus`: прямой дамп incoming RTP `audio/opus` в `.ogg` и decode-поток через `ffmpeg` в PCM 16 кГц для пайпа), инкрементальное сохранение слайдов каждые 45 с по ходу лекции (плюс финальный сбор перед закрытием сессии в `GrabSlides`) и `startSpotter`. Споттер (`internal/bbb/spot.go`) гоняет PCM через `internal/wake` и на совпадении шлёт вейкворд пользователям, а также выставляет `moodle_wake_kick` / `moodle_kick:<id>` для немедленной проверки открывшихся тестов в Moodle. Один поток на комнату, без PulseAudio.

### После пары

Сразу после окончания лекции (в тот же день, когда нет активных сессий) `maybeNotes` запускает `notes.Build` (STT + vision до 60 слайдов + LLM `notes.md` + PDF через `internal/notes/pdf.go`, где единственно остался разовый headless `chromium --print-to-pdf`), а `maybeHarvest` служит резервным фоллбэком, если во время лекции слайды не скачались. Готовый PDF уходит кнопкой в Telegram, а `notes.md` и `transcript.txt` сразу доступны решателю тестов Moodle.

Перед расшифровкой `notes.Build` проверяет запись (`capture.Probe`): короче минуты, тишина (пик ниже −50 dBFS) или расшифровка в пару фраз на всю пару — пак закрывается как `empty`, во внешние сервисы и в рассылку ничего не уходит. Ошибка STT/LLM оставляет пак в `error`: три повтора через 10 минут, дальше раз в 2 часа, всего 10 попыток; счётчик лежит в `settings` (`notes_attempts:<pack>`) и переживает рестарт. `maybeNotes` догоняет не только вчерашний день, но и дни за последнюю неделю, у которых `notes_done` ещё не стоит.

Вейкворды: `wake.py` распознаёт всю речь без закрытого словаря (с грамматикой из нескольких слов Vosk подгонял под них любую фразу) и отдаёт законченные фразы; `wake.Match` ищет в них слова пользователей вместе с формами («контрольная» → «контрольную»), но не однокоренные («тест» не ловит «тестирование»).

### Telegram UX

- Все сообщения отправляются с `parse_mode=HTML`; динамика (ФИО, дисциплина, препод, ссылки, имена гостя) экранируется через `internal/tg/html.go` (`esc/bold/code/hlink`). 
- **Статус захода — в панели** (карточка пары, `presence`). Бот live-карточек больше не ведёт; `bbb` шлёт только действия и тревоги: нет ссылки, ошибка захода, лобби >2 мин, вейкворд, готовый конспект.
- Навигация: reply-клавиатура (Пары / Конспекты / Профиль) + inline-кнопки в карточках; настройки редактируются inline, а не новыми сообщениями.
- Команды: `/start`, `/help`, `/settings`, `/today`, `/notes`, `/cancel` (+ `/test` и `/panel` у админа). Неизвестная команда возвращает подсказку, а не тишину. `/cancel` сбрасывает ожидание ввода.

### Панель

`internal/webapp`: статика (`static/`: `index.html`, `css/`, `js/`, шрифты и картинки вшиты в бинарник) + JSON API. Вход в API: личный часовой пароль из бота (`internal/panelpass`, определяет, кто вошёл), общий `PANEL_PASSWORD` (вход админом), либо паролей нет, но задан `PANEL_ALLOW_IPS`, либо демо; иначе 401. Служебные ручки — только админу. Фильтр по IP (`PANEL_ALLOW_IPS`, `PANEL_TRUST_PROXY`) стоит перед всем, включая статику. Обработчики работают через интерфейс `Source`: `*store.Store` в обычном режиме, `demoSource` в памяти при `PANEL_DEMO=1`. WebSocket нет — пуллинг.

Пять экранов (hash-роутинг, нижняя панель на телефоне, боковая от 900px):

- **Сегодня** — пары дня, переключатель «заходить / не заходить» по каждой паре, режим дня, ближайшая пара;
- **Неделя** — расписание по дням, те же переключатели;
- **Сдать** — дедлайны Moodle по срочности (просрочено за 14 дней, сегодня и завтра, неделя, позже, сдано за 30 дней) и отдельный свёрнутый блок «Без срока», который в счётчики не идёт;
- **Конспекты** — паки лекций по предметам и их статус, ссылки на PDF в GitHub;
- **Ещё** — профиль (имя для BBB, подгруппа, свои вейкворды, вкл/выкл бота), аккаунт Moodle, комнаты BBB, служебное (пульт: люди, парсер, логи, тестовый заход).

Первый вход: пока у «меня» нет ФИО для BBB и подгруппы, вместо приложения экран «Первый вход»; `POST /api/me {fio, subgroup, onboard:true}` заканчивает настройку (как онбординг в Telegram).

«Я» — тот, чей личный пароль введён (общий пароль — `ADMIN_TELEGRAM_ID`). Решения пишутся в `join_intents`; воркер `bbb` читает их сам. Молчание = заходить.

| Метод | Путь | Что делает | Основные поля |
| --- | --- | --- | --- |
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

Служебные ручки пульта остались: `/api/now`, `/api/people`, `/api/people/{id}`, `/api/lessons`, `/api/bbb`, `/api/parser`, `/api/parser/refresh`, `/api/logs`, `/api/settings`, `/api/test`.

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

Ключевые события BBB (join/seated/fail/drop/lobby), тест-захода, T-15, ссылок, онбординга, парсера и HTTP-запросы панели логируются. См. [OPERATIONS.md#логи](OPERATIONS.md#логи).