# Тестирование: все функции бота в тестовой комнате BBB

Runbook для агента (и человека). Цель — прогнать **каждую** пользовательскую функцию
через реальную тестовую комнату BBB и убедиться, что она работает и **логируется**.
Устройство — [ARCHITECTURE.md](ARCHITECTURE.md), эксплуатация — [OPERATIONS.md](OPERATIONS.md).

Тестовая комната создаётся руками (ссылка `https://bbb.ssau.ru/b/...`). Ссылка
привязывается командой `/test` в Telegram и **не** трогает пары (`settings.test_join`,
ключ `lesson:*` не используется).

## 0. Границы

- Тестируем 4 процесса: `tg`, `rasp`, `panel`, `bbb`. `bbb` дергает Chromium, PulseAudio,
  ffmpeg и Vosk — это и есть предмет большинства кейсов.
- `panel` не обязан быть снаружи: можно ходить `curl` по `localhost:8080` с VDS, либо по
  туннелю (`ssh ssau webapp-url`).
- Секреты не читаем и не логируем. `cat .env` недоступен — только `ssh ssau env-keys`.
- Debug-логи по умолчанию выключены (`LOG_LEVEL=info`). Для разбора нужно
  `LOG_LEVEL=debug` в `/opt/ssau-bot/.env` и рестарт — это действие подтверждает человек.

## 1. Карта функций → кейсы

| Функция | Где код | Кейсы |
| --- | --- | --- |
| Онбординг (ФИО → подгруппа → слова) | `internal/tg/handlers.go` | TC-01…TC-04 |
| Меню, `/help`, `/today`, `/cancel`, fallback | `internal/tg/handlers.go` | TC-05…TC-08 |
| Профиль/настройки, ФИО, подгруппа, слова | `internal/tg/handlers.go`, `keyboard.go` | TC-09…TC-12 |
| Привязка ссылки BBB к паре | `handlers.go`, `store` | TC-13 |
| T-15 карточка, «Зайти/Не сегодня» | `internal/tg/t15.go` | TC-14…TC-15 |
| Live-карточка пары + «Отключиться» | `internal/tg/live.go` | TC-16 |
| `/test`: карточка, ссылка, имя | `internal/tg/testjoin.go` | TC-17…TC-19 |
| Тест «болванчик» (dummy) | `internal/bbb/worker_testjoin.go` | TC-20…TC-22 |
| Тест «со звуком» (listen) + запись | `worker_testjoin.go`, `capture` | TC-23…TC-25 |
| Смена режима dummy↔listen | `worker_testjoin.go` | TC-26 |
| Вейкворды в тесте | `internal/bbb/spot.go`, `internal/wake` | TC-27 |
| Выход из теста | `worker_testjoin.go` | TC-28 |
| Пауза теста во время пары | `worker_testjoin.go` | TC-29 |
| Тест через панель `/api/test` | `internal/webapp/api.go` | TC-30 |
| Авторизация и остальное API | `internal/webapp` | TC-31…TC-32 |
| Конспекты после пары + GitHub | `worker_lecture.go`, `internal/notes`, `publish` | TC-33 |
| Парсер расписания + вкладка логов | `internal/rasp`, `/api/parser` | TC-34 |
| Устойчивость: нет ссылки, ошибки, дубли | `internal/bbb/worker.go` | TC-35…TC-36 |

## 2. Подготовка

### 2.1. Сервисы

```bash
ssh ssau status          # ssau-tg/rasp/panel/bbb/tunnel
ssh ssau units           # is-active
ssh ssau disk            # место: Chromium+Vosk+записи жрут диск
```

Всё должно быть `active`. Если нет — `ssh ssau restart <unit>` (спросит подтверждение).

### 2.2. Логи

```bash
ssh ssau logs ssau-bbb 300      # хвост, максимум 500 строк
ssh ssau logs ssau-tg 200
ssh ssau logs ssau-panel 100
```

Для подробностей — `LOG_LEVEL=debug` в `/opt/ssau-bot/.env` (человек) + рестарт.
Формат строки: `[LEVEL] component: message`.

### 2.3. Локальная сборка/тесты (без комнаты)

```bash
gofmt -l cmd internal
go build ./...
go vet ./...
CGO_ENABLED=0 go test ./...          # то же, что CI
BBB_DRY_RUN=1 go run ./cmd/bbb       # Chromium не трогаем: DryJoiner всегда "room"
```

`BBB_DRY_RUN=1` удобен для проверки логики `ensureTestN`, но **не** проверяет реальную
классификацию страницы, лобби и запись — это только TC-20…TC-28 в живой комнате.

Сквозной тест звука (Chromium играет тон → `ssau_rec.monitor` → ffmpeg → PCM не тишина)
создаёт sink и меняет default sink, поэтому гоняется только на **отдельном** PulseAudio,
не на продовом `/run/user/0`:

```bash
T=$(mktemp -d); mkdir -p $T/xdg $T/home; chmod 700 $T/xdg
export HOME=$T/home XDG_RUNTIME_DIR=$T/xdg PULSE_SERVER=unix:$T/xdg/pulse/native
env -u DBUS_SESSION_BUS_ADDRESS pulseaudio -n --daemonize=yes --exit-idle-time=-1 \
  -L module-native-protocol-unix -L "module-null-sink sink_name=dummy"
SSAU_AUDIO_E2E=1 go test ./internal/bbb -run TestRecordTabAudioReachesFFmpeg -v
pulseaudio --kill
```

### 2.4. Telegram

- Бот запущен под `TELEGRAM_BOT_TOKEN`, ты пишешь с `ADMIN_TELEGRAM_ID`.
- `/test` и `/panel` видны только админу (`SetMyCommands` scope chat).
- Админ получает live-карточку теста (`syncTestLive`) и пуш-уведомления (`notify.Admin`).

### 2.5. Тестовая комната

- Создай комнату в BBB, скопируй `https://bbb.ssau.ru/b/...`.
- Если комната с модерацией — понадобится второй человек/устройство, чтобы пустить гостя
  (иначе кейс проверяет только лобби).
- Имя гостя по умолчанию — `тест`; меняется кнопкой «Имя» (`/test Иванов`).

## 3. Сброс перед прогоном

Чтобы результаты были чистыми:

```bash
# в Telegram
/test            # карточка теста
# кнопка «Выйти», если статус не idle
```

```bash
# БД: убрать прошлый тест и события (локально/на VDS при доступе к sqlite)
sqlite3 /opt/ssau-bot/data/bot.db \
  "delete from settings where key='test_join'; \
   delete from events where type in ('join','leave','wake','t15','skip','record') and message like '%тест%';"
```

```bash
ssh ssau recordings      # при желании удалить recordings/test вручную
```

## 4. Кейсы

### Группа A. Health и онбординг (tg)

#### TC-01. Новый пользователь: /start
- **Проверяет:** `onStart`, `sendAskFIO`, `UpsertUser`, событие `onboard`.
- **Шаги:** с чистого аккаунта (или после `reset`) отправить `/start`.
- **Ожидаемо:** приветствие + запрос ФИО, reply-клавиатура «Пары/Конспекты/Профиль».
- **Логи:** `tg`, `onboard created`, дальше `fio saved`.
- **БД:** `users` строка с `onboarded=0`, `onboard_stage=0`.

#### TC-02. Онбординг: ФИО → подгруппа → слова
- **Проверяет:** `continueOnboarding`, `setSubgroupAndAskWords`, `finishOnboarding`,
  callback `ob:sub:1/2`, `ob:skipw`.
- **Шаги:** ввести ФИО → выбрать подгруппу → ввести слова или «Пропустить».
- **Ожидаемо:** после слов — профиль (`formatOnboardDone`), `onboard_stage=3`.
- **Логи:** `onboard fio`, `onboard subgroup`, `onboard done`.
- **БД:** `users.fio`, `users.subgroup`, `users.wake_words`, `onboarded=1`; событие `onboard`.

#### TC-03. Повторный /start онборднутого
- **Проверяет:** ветку `u.Onboarded` в `onStart`.
- **Ожидаемо:** сразу `/today`-сводка, без онбординга.
- **Логи:** `onStart: onboarded`.

#### TC-04. Whitelist
- **Проверяет:** `allowed`.
- **Шаги:** написать с аккаунта вне whitelist и не админа.
- **Ожидаемо:** тишина.
- **Логи:** `allowed: rejected tg=...`.

### Группа B. Меню и навигация (tg)

#### TC-05. Кнопки меню и команды
- **Проверяет:** `onToday`, `onNotes`, `onSettings`, `onHelp`, `onCancel`, роутинг `onText`.
- **Шаги:** по очереди «Пары», «Конспекты», «Профиль», `/help`, `/today`, `/cancel`.
- **Ожидаемо:** каждая кнопка — свой экран; `/cancel` сбрасывает ожидание ввода.
- **Логи:** `sendToday`, `sendNotes`, `sendSettings`, `onCancel`.

#### TC-06. Неизвестная команда/текст
- **Проверяет:** fallback.
- **Шаги:** `/qwerty`, затем случайный текст.
- **Ожидаемо:** подсказка (`fallbackText`), не тишина.
- **Логи:** `onText: unknown command`, `onText: fallback`.

#### TC-07. Отмена ввода
- **Проверяет:** `cancelKeyboard`, `clearAwait`.
- **Шаги:** нажать «Профиль» → «Изменить имя» → «Отмена».
- **Ожидаемо:** возврат в профиль, ввод не применяется.
- **Логи:** `clearAwait`, `onSettingsCallback`.

#### TC-08. HTML-экранирование
- **Проверяет:** `internal/tg/html.go`.
- **Шаги:** сохранить ФИО `<b>hack</b> & <script>`.
- **Ожидаемо:** в сообщении видны символы, разметка не ломается.
- **Юнит:** `internal/tg/html_test.go`.

### Группа C. Профиль и слова (tg)

#### TC-09. Смена ФИО
- **Проверяет:** `st:fio`, `handleAwait awaitFIO`, `SetFIO`.
- **Ожидаемо:** новое ФИО в профиле и в live/BBB-имени.
- **Логи:** `fio saved tg=...`.

#### TC-10. Смена подгруппы
- **Проверяет:** `st:sub:1/2`, `SetSubgroup`.
- **Ожидаемо:** галочка на выбранной, влияет на фильтр пар.
- **Логи:** `settings subgroup`.

#### TC-11. Слова: добавить / заменить / очистить
- **Проверяет:** `onWords`, `st:words`, `MergeWakeWords`, `ParseWakeWords`, `isClearWords`.
- **Шаги:** `/words фамилия, тест`; затем «Профиль → Слова» → `-`.
- **Ожидаемо:** слова сохраняются, `-` очищает extra.
- **Логи:** `words saved`, `words updated`.
- **БД:** `users.wake_words`.

#### TC-12. Экранирование слов
- **Проверяет:** `FormatWakeWords`, `model`.
- **Ожидаемо:** дедуп, lower-case, обрезка мусора.

### Группа D. Ссылка BBB к паре (tg)

#### TC-13. Привязка ссылки текстом
- **Проверяет:** `extractBBBURL`, `handleBBBURL`, `pickBBBTarget`, `SetLessonBBB`.
- **Шаги:** отправить `https://bbb.ssau.ru/b/xxxx` когда есть подходящая online-пара.
- **Ожидаемо:** ответ «ссылка сохранена», `bbb_links[lesson:<id>]`.
- **Логи:** `bbb link lesson=<id> tg=...`.
- **Событие:** type `bbb`.
- **Негатив:** если пары нет — `noBBBTarget`.
  **Логи:** `handleBBBURL: no target`.

### Группа E. T-15 (tg)

#### TC-14. Карточка T-15
- **Проверяет:** `t15Loop`, `sendT15Card`, `PutIntent`, событие `t15`.
- **Предусловие:** есть online-пара, стартующая в ≤15 мин, пользователь active, подгруппа совпадает.
- **Ожидаемо:** карточка с «Зайти за меня / Не сегодня» ровно один раз.
- **Логи:** `t15 sent tg=... lesson=...`; `t15 card` при ошибке.
- **БД:** `join_intents.decision=pending`.

#### TC-15. Решение по T-15
- **Проверяет:** `onJoinCallback`, `j:` callbacks.
- **Шаги:** «Не сегодня» → проверить, затем «Зайти» (в новом заходе).
- **Ожидаемо:** `decision=no`/`yes`, карточка перередактирована (`formatSkipAck`/`formatJoinAck`).
- **Логи:** `t15 decision tg=... join=false|true`; для `no` — событие `skip`.

### Группа F. Live-карточка пары (tg)

#### TC-16. Live-карточка и «Отключиться»
- **Проверяет:** `liveLoop`, `syncLiveCard`, `clearLiveCard`, `onLeaveCallback` (`x:`).
- **Предусловие:** bbb зашёл в пару (см. TC-35/36 или реальная пара), `presence=room|lobby`.
- **Ожидаемо:** карточка появляется и редактируется на месте (предмет, ссылка, остаток,
  статус, ФИО); при выходе — «Вышел из комнаты»; кнопка «Отключиться» ставит `decision=no`.
- **Логи:** `live card sent`, `live card gone`, `leave button tg=... lesson=...`.

### Группа G. Тестовая комната: подготовка и режимы (bbb)

#### TC-17. Карточка теста
- **Проверяет:** `onTest`, `sendTestCard`, `formatTestCard`, `testMarkup`.
- **Шаги:** `/test` без ссылки.
- **Ожидаемо:** карточка, кнопки «Болванчик», «Со звуком», «Ссылка», «Имя», «Пульт».
- **Логи:** `tg`, `sendTestCard`.

#### TC-18. Привязка ссылки теста
- **Проверяет:** `armTestURL`, `awaitTestURL`, `extractBBBURL`.
- **Шаги:** `/test https://bbb.ssau.ru/b/...` или кнопка «Ссылка» → вставить URL.
- **Ожидаемо:** «ссылка привязана», кнопки режимов активны.
- **Логи:** `test url armed url=...`.
- **БД:** `settings.test_join.url`.
- **Негатив:** URL не bbb.ssau.ru → WebApp вернёт 400; в Telegram `extractBBBURL` не сработает.

#### TC-19. Имя гостя
- **Проверяет:** `setTestGuestName`, `normalizeTestName`, `tx:name`, `awaitTestName`.
- **Шаги:** `/test Иванов Пётр`; затем «Имя» → `-`.
- **Ожидаемо:** имя меняется, `-` возвращает `тест`.
- **Логи:** `test guest name=...`.

#### TC-20. Dummy-заход
- **Проверяет:** `onTestCallback tx:dummy`, `tickTest`, `ensureTestN`, `Joiner.Join`,
  `classifySeat`, `PublishPresence`.
- **Шаги:** задать ссылку → «Болванчик».
- **Ожидаемо:** Chromium заходит гостем, статус `joining → room|lobby`, админ получает
  `testInRoomText` («болванчик без звука») или сообщение про лобби.
- **Логи (bbb):** `test join want=dummy`, `test seated state=room|lobby`,
  при ошибке `test join fail: ...`.
- **Логи (tg):** `test callback want=dummy`, `test live card sent`.
- **БД:** `settings.test_join.status`, событие `join` «тест dummy».

#### TC-21. Dummy: страница/сессия живы
- **Проверяет:** `watchTest`.
- **Ожидаемо:** статус не скачет; если вкладка упала — `stopTest dead` и уведомление.
- **Логи:** `watchTest: InLobby`, `watchTest: InRoom`, `stopTest reason=dead`.

#### TC-22. Лобби → комната
- **Проверяет:** `InLobby`, `watchTest`.
- **Предусловие:** комната с модерацией, гость в лобби.
- **Ожидаемо:** пуш админу «тест: лобби как «...». Пусти из модерации.»; после допуска —
  «тест: пустили как «...»», статус `room`.
- **Логи:** `test seated state=lobby`, затем `syncTestLive` edit.

#### TC-23. Listen-заход + запись
- **Проверяет:** `tx:listen`, `attachTestRecorder`, `capture.Start`, `capture.SegmentPath`,
  `startSpotter`, `hogs.Hold`.
- **Шаги:** из карточки «Со звуком» (или смена режима, TC-26).
- **Ожидаемо:** в комнате, идёт запись; в `recordings/test/` появляется `audio-<nano>.ogg`.
- **Логи:** `test join want=listen`, `rec seg ...`, `capture`, `ffmpeg pid=... -> ...`,
  `test seated state=room`.
- **Проверка:** `ssh ssau recordings` — растущий `recordings/test/audio-*.ogg`.

#### TC-24. Остановка и склейка записи
- **Проверяет:** `rec.Stop`, `capture.MergeSegments`.
- **Шаги:** «Выйти».
- **Ожидаемо:** сегменты склеены в `recordings/test/audio.ogg`; одиночный сегмент
  переименован без перекодирования.
- **Логи:** `capture: ffmpeg concat`, `capture: merge done`, `test stop rec`,
  `test merge` (при ошибке — WARN).
- **Проверка:** `ssh ssau recordings` — есть `audio.ogg` размером >0.

#### TC-25. Пульт во время теста
- **Проверяет:** `handleTestGet`/`handleTestPost`, `syncTestLive`.
- **Шаги:** открыть Mini App «Пульт», посмотреть статус, нажать режимы/выход.
- **Ожидаемо:** состояние теста меняется так же, как из Telegram; live-карточка
  редактируется.
- **Логи (panel):** `handleTestGet: want=...`, `handleTestPost: want=...`.

#### TC-26. Смена режима dummy ↔ listen
- **Проверяет:** ветку `mode != want` в `ensureTestN`.
- **Шаги:** в комнате в dummy нажать «Со звуком».
- **Ожидаемо:** сессия закрывается и переоткрывается с записью; `mode` меняется.
- **Логи:** `ensureTestN: mode switch dummy -> listen`, новый `test join want=listen`.

### Группа H. Вейкворды и выход (bbb)

#### TC-27. Вейкворд в тесте
- **Проверяет:** `spot.go`, `wake.Engine`, `wake.Vocab`, `wake.Match`, `onWake`.
- **Предусловие:** listen-режим (TC-23), установлен `VOSK_MODEL` и `VOSK_SCRIPT`.
- **Шаги:** сказать в микрофон тестовой комнаты общее слово (`тест`, `контрольная`,
  `мудл`, `moodle`) или фамилию пользователя.
- **Ожидаемо:** админу приходит «тест услышал: «<слово>»»; событие `wake`.
- **Логи:** `wake: <msg>`, `onWake: lesson=... word=...`.
- **Если Vosk нет:** `wake: vosk: ... — пейджер молчит` — это ожидаемый WARN, кейс
  не проходит по инфраструктуре.
- **Юнит:** `internal/wake` (`engine_test.go`, `match_test.go`).

#### TC-28. Выход из теста
- **Проверяет:** `tx:leave`, `stopTest(reason=off)`, `clearTestLive`.
- **Ожидаемо:** сессия закрыта, `status=idle`, `want=""`, live-карточка «Тест: вышел».
- **Логи:** `stopTest: reason=off`, `test stopped reason=off was_live=true`.
- **Событие:** `leave` «тест off».

#### TC-29. Пауза теста во время пары
- **Проверяет:** `pauseTest`, `resumeTest`.
- **Предусловие:** тест активен (room/lobby) и стартует/идёт лекционная пара.
- **Ожидаемо:** тест остановлен, `status=error`, `message="идёт пара: Chrome занят лекцией"`,
  одно пуш-уведомление админу.
- **Логи:** `bbb: test paused: lecture in progress`, `pauseTest: active=true`.
- **Важно:** проверить, что уведомление пришло **один раз** (флаг `testPaused`).

### Группа I. Панель (panel)

#### TC-30. `/api/test` через curl
- **Проверяет:** авторизацию и `handleTestPost`.
- **Шаги:**
  ```bash
  BASE=$(ssh ssau webapp-url)         # или http://localhost:8080
  curl -s -H "X-Panel-Password: $PASSWORD" "$BASE/api/test"
  curl -s -H "X-Panel-Password: $PASSWORD" -H 'Content-Type: application/json' \
       -d '{"url":"https://bbb.ssau.ru/b/xxx","want":"dummy"}' "$BASE/api/test"
  ```
- **Ожидаемо:** JSON `TestJoin`; невалидный URL → 400 `url must be https://bbb.ssau.ru/b/...`;
  `want` без URL → 400 «сначала ссылка».
- **Логи:** `handleTestGet`, `handleTestPost`, `bad url`.

#### TC-31. Авторизация
- **Проверяет:** `auth.go`, `requirePassword`.
- **Ожидаемо:** без заголовка — 401; неверный пароль — 401 и WARN; верный — 200.
- **Логи:** `requirePassword: auth failed`, `passwordOK`.

#### TC-32. Остальное API
- **Проверяет:** `/api/now`, `/api/people`, `/api/lessons`, `/api/parser`, `/api/logs`,
  `/api/settings`, `POST /api/bbb`, `POST /api/people/{id}`, `POST /api/parser/refresh`.
- **Ожидаемо:** JSON, вкладка «Логи» читает таблицу `events`.
- **Логи:** соответствующие `handle*`.

### Группа J. Конспекты и парсер (фоновые)

#### TC-33. Конспект после пары (не тест)
- **Проверяет:** `maybeHarvest`, `maybeNotes`, `buildNotes`, `notes.Build` (STT→LLM→PDF),
  `publisher`, `announceNotes`, `cleanPack`.
- **Предусловие:** есть пак со статусом `recorded` за прошлый день; ключи LLM/STT/vision.
- **Ожидаемо:** `slides/`, `transcript.txt`, `notes.md`, `notes.pdf`; PDF уходит кнопкой
  в Telegram (`/notes`); выгрузка в GitHub; локальные файлы чистятся через сутки.
- **Логи:** `maybeNotes: start day=...`, `notes: Build: done`, `publish`, `notes pdf sent`.
- **Юнит:** `internal/notes/*_test.go`, `internal/publish`.

#### TC-34. Парсер и kick из панели
- **Проверяет:** `Refresh`, `Parse`, `Diff`, `StartCron`, `RequestRaspRefresh`, `WaitRun`.
- **Шаги:** `POST /api/parser/refresh`, смотреть вкладку «Парсер/Логи».
- **Ожидаемо:** новый `parse_runs`, событие `reparse`, дифф.
- **Логи:** `rasp: lessons=... online=...`, `rasp: diff:`.

### Группа K. Устойчивость (bbb)

#### TC-35. Заход пары без ссылки
- **Проверяет:** `missingBBB`.
- **Ожидаемо:** `presence=error`, событие `no_bbb`, сообщение пользователю.
- **Логи:** `bbb: no bbb link lesson=<id>`.

#### TC-36. Ошибка и повтор захода
- **Проверяет:** `ensureIn`, `joinFail`, `dropDead`, `isBlocked`.
- **Шаги:** дать битую ссылку (`/b/несуществующая`) на пару/тест.
- **Ожидаемо:** `presence=error`, событие `error`, пуш админу/пользователю; повтор по
  кнопке «Зайти»; первый `dropDead` — тихий retry, второй — блок.
- **Логи:** `join fail <key> tg=...: <причина>`, `drop retry`, `seat`.
- **Тесты:** `internal/bbb/decide_test.go`, `worker_block_test.go`, `seat_test.go`.

## 5. Шпаргалка по логам

```bash
# вся тестовая комната
ssh ssau logs ssau-bbb 500 | grep -E 'test join|test seated|test stopped|test paused|test ffmpeg|ensureTestN|stopTest'

# telegram-сторона теста
ssh ssau logs ssau-tg 300 | grep -E 'test |test_card|t15 |live card|onboard|fio saved|words'

# панель и авторизация
ssh ssau logs ssau-panel 200 | grep -E 'handleTest|requirePassword|auth failed'

# вейкворды
ssh ssau logs ssau-bbb 500 | grep -E 'wake|vosk|услышал'

# ошибки по всем сервисам
ssh ssau status
```

## 6. Сброс после прогона

```bash
# Telegram: /test → «Выйти»
sqlite3 /opt/ssau-bot/data/bot.db \
  "delete from settings where key='test_join';"
ssh ssau recordings      # удалить recordings/test при необходимости
# вернуть LOG_LEVEL=info и рестарт
```

## 7. Известные ограничения

- Реальная комната нужна для TC-20…TC-29; `BBB_DRY_RUN=1` их не заменяет.
- Тестовая запись (`recordings/test/`) **не** проходит пайплайн конспектов — это только
  лекционные паки (`lecture_packs`). TC-33 отдельный.
- Лобби-таймаут >2 мин шлётся только для пар (`watchLobby`), не для теста.
- Пока идёт пара, тест принудительно останавливается (один Chromium) — не баг.
- Vosk/STT/vision требуют ключей и модели; без них соответствующие кейсы дают WARN,
  а не падение.