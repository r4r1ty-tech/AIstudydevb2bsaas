# Тестирование: прогон всех функций

Runbook для агента (и человека). Цель — прогнать **каждую** пользовательскую функцию
вживую и убедиться, что она работает и **логируется**.
Устройство — [ARCHITECTURE.md](ARCHITECTURE.md), эксплуатация — [OPERATIONS.md](OPERATIONS.md).

Модель: бот выдаёт только личный пароль в панель и шлёт уведомления; всё остальное —
в панели (`https://panel.example.com`, открывается через VPN пула). Тестовая комната BBB
создаётся руками (ссылка `https://bbb.ssau.ru/b/...`) и задаётся в панели
«Ещё → Служебное → Тест» — **не** трогает пары (`settings.test_join`, ключ `lesson:*` не используется).

## 0. Границы

- 4 процесса: `tg`, `rasp`, `panel`, `bbb`. `bbb` дергает `DirectJoiner` (HTTP + DDP + Pion WebRTC),
  `ffmpeg` (декодер PCM) и Vosk; `rasp` — ЛК, Moodle и ИИ (решатель тестов).
- Доступ к серверу: из РФ напрямую не виден, только через jump-VDS:
  `alias S='ssh -J jumphost root@203.0.113.20'`.
- Секреты не читаем и не логируем. Ключи `.env` без значений: `S cut -d= -f1 /opt/ssau-bot/.env`.
- Debug-логи по умолчанию выключены (`LOG_LEVEL=info`). Для разбора нужно
  `LOG_LEVEL=debug` в `/opt/ssau-bot/.env` и рестарт — это действие подтверждает человек.

Локальный сквозной тест протокола BBB и аудио-тракта (`DirectJoiner` + `pion/webrtc/v4` + `OpusRec` → `ffmpeg` → 16 кГц PCM без PulseAudio) запускается на mock-серверах:

```bash
go test -v -race ./internal/capture/... ./internal/bbb/... -run 'TestOpus|TestDirect'
```

## 1. Карта функций → кейсы

| Функция | Где код | Кейсы |
| --- | --- | --- |
| Бот: `/start`, кнопка пароля, `/pass`, молчание чужим | `internal/tg/start.go`, `panelpass.go` | TC-01…TC-03 |
| Вход в панель личным паролем, срок, админ/не админ | `internal/panelpass`, `internal/webapp/server.go` | TC-04…TC-06 |
| Первый вход, профиль, аккаунт универа, комнаты BBB | `internal/webapp/personal*.go`, `moodle_store.go` | TC-07…TC-09 |
| Решения по парам (Сегодня/Неделя) | `internal/webapp/personal.go` | TC-10 |
| ФИО из ЛК | `internal/lk`, `internal/uni/sync.go` | TC-11 |
| ИИ для тестов: выбор провайдера/модели/ключа, «Проверить» | `internal/llm`, `internal/access`, `internal/webapp/llm_api.go` | TC-12…TC-13 |
| Авторешение тестов Moodle, «Пройти», журнал, «Перерешать» | `internal/uni/quiz_solve.go`, `internal/quizlog` | TC-14…TC-17 |
| Напоминания о сроках Moodle | `internal/tg/deadlines.go` | TC-18 |
| Тест «болванчик» (dummy) | `internal/bbb/worker_testjoin.go` | TC-20…TC-22 |
| Тест «со звуком» (listen) + запись | `worker_testjoin.go`, `capture` | TC-23…TC-25 |
| Смена режима dummy↔listen | `worker_testjoin.go` | TC-26 |
| Вейкворды в тесте | `internal/bbb/spot.go`, `internal/wake` | TC-27 |
| Выход из теста | `worker_testjoin.go` | TC-28 |
| Пауза теста во время пары | `worker_testjoin.go` | TC-29 |
| Служебные ручки через curl | `internal/webapp` | TC-30…TC-32 |
| Конспекты после пары + GitHub | `worker_lecture.go`, `internal/notes`, `publish` | TC-33 |
| Парсер расписания + вкладка логов | `internal/rasp`, `/api/parser` | TC-34 |
| Устойчивость: нет ссылки, ошибки, дубли | `internal/bbb/worker.go` | TC-35…TC-36 |

## 2. Подготовка

### 2.1. Сервисы

```bash
S systemctl is-active ssau-tg ssau-rasp ssau-panel ssau-bbb caddy
S df -h /                 # место: Vosk+записи+паки жрут диск
```

Всё должно быть `active`. Если нет — `S systemctl restart <unit>` (подтверждает человек).

### 2.2. Логи

```bash
S journalctl -u ssau-bbb -n 300 --no-pager
S journalctl -u ssau-rasp -n 200 --no-pager
S tail -n 200 /opt/ssau-bot/ssau.log          # общий файл всех процессов
```

Для подробностей — `LOG_LEVEL=debug` в `/opt/ssau-bot/.env` (человек) + рестарт.
Формат строки: `[LEVEL] component: message`.

### 2.3. Локальная сборка/тесты (без комнаты)

```bash
gofmt -l cmd internal
go build ./...
go vet ./...
CGO_ENABLED=0 go test ./...          # то же, что CI
BBB_DRY_RUN=1 go run ./cmd/bbb       # сеть BBB не трогаем: DryJoiner всегда "room"
PANEL_DEMO=1 LISTEN_ADDR=127.0.0.1:18080 go run ./cmd/panel   # панель на тестовых данных
```

`BBB_DRY_RUN=1` удобен для проверки логики `ensureTestN`, но **не** проверяет реальную
работу с Greenlight/DDP/SFU, лобби и запись — это только TC-20…TC-28 в живой комнате.

### 2.4. Telegram и панель

- Бот запущен под `TELEGRAM_BOT_TOKEN`, ты пишешь с `ADMIN_TELEGRAM_ID` (или с аккаунта, у которого логин универа на `ADMIN_LOGIN_SUFFIX`).
- Пароль в панель — кнопка «🔑 Пароль в панель» в боте; панель открывать с VPN пула.
- Ход теста и тревоги приходят админу сообщениями (`notify.Admin`).

### 2.5. Тестовая комната

- Создай комнату в BBB, скопируй `https://bbb.ssau.ru/b/...`.
- Если комната с модерацией — понадобится второй человек/устройство, чтобы пустить гостя
  (иначе кейс проверяет только лобби).
- Имя гостя по умолчанию — `тест`; меняется полем «Имя в BBB» во вкладке «Тест».

## 3. Сброс перед прогоном

Во вкладке «Служебное → Тест» — «Выйти», если статус не idle.

```bash
# БД: убрать прошлый тест и события
S sqlite3 /opt/ssau-bot/data/bot.db \
  "delete from settings where key='test_join'; \
   delete from events where type in ('join','leave','wake','skip','record') and message like '%тест%';"
S ls -la /opt/ssau-bot/recordings/test      # при желании удалить вручную
```

## 4. Кейсы

### Группа A. Бот (tg)

#### TC-01. /start
- **Проверяет:** `onStart`, `mainKeyboard`, `setMenuButton`.
- **Шаги:** `/start` с аккаунта из вайтлиста.
- **Ожидаемо:** «Здесь только пароль в панель и уведомления…», внизу одна кнопка «🔑 Пароль в панель», слева от поля ввода — кнопка меню «Панель».

#### TC-02. Пароль в панель
- **Проверяет:** `sendPanelPass`, `panelpass.Issue`.
- **Шаги:** нажать «🔑 Пароль в панель» (или `/pass`).
- **Ожидаемо:** «🔑 Пароль в панель для <ФИО из ЛК>: `xxxx-xxxx`», «Действует до ЧЧ:ММ» (1–2 часа), кнопка «Открыть панель».
- **Логи:** `panel pass issued tg=... until=...`.
- **Негатив:** без ключа хранилища — «Пароли в панель сейчас не выдаются».

#### TC-03. Чужим и прочему тексту
- **Шаги:** любой текст, старые команды (`/today`, `/test`), сообщение с чужого аккаунта.
- **Ожидаемо:** свой текст — подсказка «Бот только выдаёт пароль…»; старые команды — молчание; чужому — молчание и `allowed: rejected tg=...` в логе.

### Группа B. Вход в панель (panel)

#### TC-04. Личный пароль
- **Шаги:** открыть панель, ввести пароль из TC-02.
- **Ожидаемо:** приложение открывается от лица этого человека («Ещё → ИИ для тестов»: «Ты: <ФИО>»).
- **Негатив:** чужой/выдуманный пароль — «Пароль не подошёл.», в логе `bad password`. Пароль, выданный больше 2 часов назад, — то же.

#### TC-05. Не админ
- **Ожидаемо:** нет блока «Служебное»; `curl` к `/api/now`, `/api/people`, `/api/logs`, `/api/admin/llm/people` с его паролем — 403.

#### TC-06. Админ
- **Ожидаемо:** «Ещё → Служебное» с вкладками «ИИ и ключи», «Тесты Moodle», «Тест», «Сейчас», «Люди», «Пары», «Парсер», «Логи». То же — по общему `PANEL_PASSWORD`.

### Группа C. Своё в панели

#### TC-07. Первый вход
- **Предусловие:** у человека нет имени для BBB или подгруппы.
- **Ожидаемо:** экран «Первый вход»; после сохранения — приложение; `users.onboarded=1`.

#### TC-08. Аккаунт универа
- **Шаги:** «Ещё → Аккаунт универа», логин и пароль ЛК/Moodle.
- **Ожидаемо:** «Moodle подключён», через ≤5 с появляется «Сдать»; пароль в `accounts` только зашифрован. Неверный пароль — «Moodle не принял логин или пароль.».

#### TC-09. Комнаты BBB
- **Шаги:** «Ещё → Комнаты BBB», вставить ссылку `https://bbb.ssau.ru/b/...`.
- **Ожидаемо:** «Ссылка сохранена»; ссылка общая для группы (видна всем).

#### TC-10. Решения по парам
- **Шаги:** «Сегодня»: переключатель у онлайн-пары, «Заходить на все» / «Никуда».
- **Ожидаемо:** `join_intents` (`yes/no`); без записи — молчание = заходить. Очная пара — 400 «Это очная пара…».

#### TC-11. ФИО из ЛК
- **Предусловие:** подключён ЛК.
- **Ожидаемо:** при синке ЛК (раз в сутки) в логе `uni: lk name tg=...: <ФИО>`, `settings.lk_name:<tg>`; пустое имя для BBB заполнилось; ФИО видно в сообщении с паролем и в «Служебном».

### Группа D. ИИ и тесты Moodle

#### TC-12. Выбор провайдера, модели, ключа
- **Шаги:** «Ещё → ИИ для тестов»: провайдер Google, модель, ключ конкретного владельца → «Сохранить».
- **Ожидаемо:** строка «Твои тесты решает по порядку» начинается с выбранной модели на выбранном ключе, дальше та же модель на остальных ключах, потом общая цепочка. Не админ не видит ключ админа (`LLM_API_*`) и хвосты ключей; выбрать его — 400.

#### TC-13. «Проверить» и «Все модели»
- **Ожидаемо:** «отвечает» и текст модели (gemini-4-hydrogen думает до минуты) или «не отвечает» со списком попыток и ошибками (429 — лимит бесплатного тарифа). «Все модели» — живой список провайдера в подсказках поля.

#### TC-14. Тест открылся
- **Предусловие:** подключён Moodle, есть ключи ИИ, тест переходит из «не открыт» в открытый.
- **Ожидаемо:** «Открылся тест в Moodle!», затем «Тест Moodle автоматически решён» с оценкой, моделью и ответами; `quiz_solved:<tg>:<cmid>`; запись `solved` в журнале.

#### TC-15. «Пройти»
- **Шаги:** «Сдать» → у теста (в т.ч. просроченного) «Пройти».
- **Ожидаемо:** кнопка «Запущено — результат придёт в Telegram»; за ≤5 с синк (`quiz_force`), дальше как TC-14. Moodle не пускает — «⚠️ Не получилось пройти тест» с причиной.

#### TC-16. ИИ не ответил / ответы не сохранились
- **Ожидаемо:** попытка в Moodle **не завершена**, «⚠️ Проблема с тестом» (первый раз и дальше раз в ~3 ч), `quiz_fail` растёт, повтор на каждом обходе (30 мин); запись `failed` в журнале с попытками по моделям.

#### TC-17. Журнал и «Перерешать» (админ)
- **Шаги:** «Служебное → Тесты Moodle» → «Подробнее», «Перерешать».
- **Ожидаемо:** кто (ФИО), модель и ключ, все попытки по цепочке, ответы по вопросам, сырой ответ ИИ, оценка; «Перерешать» снимает отметки и запускает синк.

#### TC-18. Напоминания о сроках
- **Ожидаемо:** за 24 и за 3 часа до срока несданного — «⏰ Срок …» в Telegram, по одному разу (`dl_sent:*`).

### Группа G. Тестовая комната: режимы (bbb)

#### TC-20. Dummy-заход
- **Проверяет:** `POST /api/test {want:dummy}`, `tickTest`, `ensureTestN`, `DirectJoiner.Join`,
  `PublishPresence`.
- **Шаги:** «Служебное → Тест»: ссылка, имя → «Болванчик».
- **Ожидаемо:** бот заходит гостем (Greenlight + Meteor DDP), статус `joining → room|lobby`, админ получает
  `testInRoomText` («болванчик без звука») или сообщение про лобби.
- **Логи (bbb):** `test join want=dummy`, `test seated state=room|lobby`,
  при ошибке `test join fail: ...`.
- **Логи (panel):** `handleTestPost: want=dummy`.
- **БД:** `settings.test_join.status`, событие `join` «тест dummy».

#### TC-21. Dummy: сессия жива
- **Проверяет:** `watchTest`.
- **Ожидаемо:** статус не скачет; если DDP/WS сессия оборвалась — `stopTest dead` и уведомление.
- **Логи:** `watchTest: InLobby`, `watchTest: InRoom`, `stopTest reason=dead`.

#### TC-22. Лобби → комната
- **Проверяет:** `InLobby`, `watchTest`.
- **Предусловие:** комната с модерацией, гость в лобби.
- **Ожидаемо:** пуш админу «тест: лобби как «...». Пусти из модерации.»; после допуска —
  «тест: пустили как «...»», статус `room`.
- **Логи:** `test seated state=lobby`, затем `test seated state=room`; во вкладке «Тест» статус меняется сам (опрос каждые 4 с).

#### TC-23. Listen-заход + запись
- **Проверяет:** `POST /api/test {want:listen}`, `attachTestRecorder`, `startSessionRecorder`, `capture.StartOpus`, `capture.SegmentPath`,
  `startSpotter`, `hogs.Hold`.
- **Шаги:** «Служебное → Тест» → «Со звуком» (или смена режима, TC-26).
- **Ожидаемо:** в комнате, подключен `bbb-webrtc-sfu` (`listen-only`), идёт запись; в `recordings/test/` появляется `audio-<nano>.ogg`.
- **Логи:** `test join want=listen`, `rec seg ...`, `capture: opus rec ...`,
  `test seated state=room`.
- **Проверка:** `S ls -la /opt/ssau-bot/recordings/test` — растущий `recordings/test/audio-*.ogg`.

#### TC-24. Остановка и склейка записи
- **Проверяет:** `rec.Stop`, `capture.MergeSegments`.
- **Шаги:** «Выйти».
- **Ожидаемо:** сегменты склеены в `recordings/test/audio.ogg`; одиночный сегмент
  переименован без перекодирования.
- **Логи:** `capture: ffmpeg concat`, `capture: merge done`, `test stop rec`,
  `test merge` (при ошибке — WARN).
- **Проверка:** `S ls -la /opt/ssau-bot/recordings/test` — есть `audio.ogg` размером >0.

#### TC-25. Пульт во время теста
- **Проверяет:** `handleTestGet`/`handleTestPost`, опрос вкладки «Тест».
- **Шаги:** во вкладке «Служебное → Тест» смотреть статус, переключать режимы, «Выйти».
- **Ожидаемо:** статус обновляется каждые 4 с; ход теста приходит админу сообщениями в Telegram (`notify.Admin`).
- **Логи (panel):** `handleTestGet: want=...`, `handleTestPost: want=...`.

#### TC-26. Смена режима dummy ↔ listen
- **Проверяет:** ветку `mode != want` в `ensureTestN`.
- **Шаги:** в комнате в dummy нажать «Со звуком» во вкладке «Тест».
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
- **Проверяет:** `POST /api/test {want:leave}`, `stopTest(reason=off)`.
- **Ожидаемо:** сессия закрыта, `status=idle`, `want=""`, админу «тест: вышел из комнаты.».
- **Логи:** `stopTest: reason=off`, `test stopped reason=off was_live=true`.
- **Событие:** `leave` «тест off».

#### TC-29. Пауза теста во время пары
- **Проверяет:** `pauseTest`, `resumeTest`.
- **Предусловие:** тест активен (room/lobby) и стартует/идёт лекционная пара.
- **Ожидаемо:** тест остановлен, `status=error`, `message="идёт пара: воркер занят лекцией"`,
  одно пуш-уведомление админу.
- **Логи:** `bbb: test paused: lecture in progress`, `pauseTest: active=true`.
- **Важно:** проверить, что уведомление пришло **один раз** (флаг `testPaused`).

### Группа I. Служебные ручки (panel)

#### TC-30. `/api/test` через curl
- **Шаги:**
  ```bash
  BASE=https://panel.example.com        # с IP из PANEL_ALLOW_IPS
  PASSWORD=...                        # личный пароль админа или PANEL_PASSWORD
  curl -s -H "X-Panel-Password: $PASSWORD" "$BASE/api/test"
  curl -s -X POST -H "X-Panel-Password: $PASSWORD" -H 'Content-Type: application/json' \
       -d '{"url":"https://bbb.ssau.ru/b/xxx","want":"dummy"}' "$BASE/api/test"
  ```
- **Ожидаемо:** JSON `TestJoin`; с паролем не админа — 403.

#### TC-31. Авторизация
- **Ожидаемо:** без заголовка — 401; неверный/просроченный пароль — 401 и `bad password` в логе; чужой IP — 403 `forbidden` до проверки пароля; верный — 200.

#### TC-32. Остальное API
- **Проверяет:** `/api/now`, `/api/people`, `/api/lessons`, `/api/parser`, `/api/logs`,
  `/api/settings`, `POST /api/bbb`, `POST /api/people/{id}`, `POST /api/parser/refresh`,
  `/api/admin/llm/people`, `/api/admin/quiz-runs`.
- **Ожидаемо:** JSON, вкладка «Логи» читает таблицу `events`.

### Группа J. Конспекты и парсер (фоновые)

#### TC-33. Конспект после пары (не тест)
- **Проверяет:** `maybeHarvest`, `maybeNotes`, `buildNotes`, `notes.Build` (STT→LLM→PDF),
  `publisher`, `announceNotes`, `cleanPack`.
- **Предусловие:** есть пак со статусом `recorded` за прошлый день; ключи LLM/STT/vision.
- **Ожидаемо:** `slides/`, `transcript.txt`, `notes.md`, `notes.pdf`; в Telegram уведомление «Конспект готов» с кнопкой «Скачать PDF» (присылает файл); конспект виден в панели «Конспекты»; выгрузка в GitHub; локальные файлы чистятся через сутки. LLM — общая бесплатная цепочка (`llm:` в логах).
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
- **Ожидаемо:** `presence=error`, событие `no_bbb`, пользователю «Нет ссылки… Добавь её в панели», админу одно сообщение на пару.
- **Логи:** `bbb: no bbb link lesson=<id>`.

#### TC-36. Ошибка и повтор захода
- **Проверяет:** `ensureIn`, `joinFail`, `dropDead`, `isBlocked`.
- **Шаги:** дать битую ссылку (`/b/несуществующая`) на пару/тест.
- **Ожидаемо:** `presence=error`, событие `error`, пуш админу/пользователю; повтор — переключить у пары «Не заходить» → «Заходить» в панели; первый `dropDead` — тихий retry, второй — блок.
- **Логи:** `join fail <key> tg=...: <причина>`, `drop retry`, `seat`.
- **Тесты:** `internal/bbb/decide_test.go`, `worker_block_test.go`, `seat_test.go`.

## 5. Шпаргалка по логам

```bash
# тестовая комната
S journalctl -u ssau-bbb -n 500 --no-pager | grep -E 'test join|test seated|test stopped|test paused|ensureTestN|stopTest'

# бот: пароли и отказы
S journalctl -u ssau-tg -n 300 --no-pager | grep -E 'panel pass|allowed: rejected|remind|webapp url'

# панель и авторизация
S journalctl -u ssau-panel -n 200 --no-pager | grep -E 'bad password|allow: отказ|admin only|llm choice|quiz force|quiz resolve'

# решатель тестов и ИИ
S journalctl -u ssau-rasp -n 500 --no-pager | grep -E 'quiz|llm:|lk name'

# вейкворды
S journalctl -u ssau-bbb -n 500 --no-pager | grep -E 'wake|vosk|услышал'
```

## 6. Сброс после прогона

```bash
# панель: «Служебное → Тест» → «Выйти»
S sqlite3 /opt/ssau-bot/data/bot.db "delete from settings where key='test_join';"
# удалить /opt/ssau-bot/recordings/test при необходимости
# вернуть LOG_LEVEL=info и рестарт
```

## 7. Известные ограничения

- Реальная комната нужна для TC-20…TC-29; `BBB_DRY_RUN=1` их не заменяет.
- Тестовая запись (`recordings/test/`) **не** проходит пайплайн конспектов — это только
  лекционные паки (`lecture_packs`). TC-33 отдельный.
- Лобби-таймаут >2 мин шлётся только для пар (`watchLobby`), не для теста.
- Пока идёт пара, тест принудительно останавливается (приоритет лекции в `bbb`) — не баг.
- Vosk/STT/vision требуют ключей и модели; без них соответствующие кейсы дают WARN,
  а не падение.