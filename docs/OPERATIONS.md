# Эксплуатация

Конфиг, логи, деплой, диагностика. Устройство — в [ARCHITECTURE.md](ARCHITECTURE.md), замысел — в [../PLAN.md](../PLAN.md).

## Конфиг и переменные окружения

Один `.env` на все процессы (`internal/config`). Реальные env имеют приоритет над файлом. Секреты в git не кладём.

| Переменная | Дефолт | Назначение |
| --- | --- | --- |
| `TELEGRAM_BOT_TOKEN` | — | Bot API-токен. Нужен `tg` и `panel` |
| `ADMIN_TELEGRAM_ID` | `1074442235` | Админ (единственный способ получить права администратора на уровне пользователя): «Служебное» в панели, `cheaper`, админские уведомления (тревоги записи, парсер, тест BBB) |
| `PANEL_PASSWORD` | — | Общий пароль API панели — вход **админом** (заголовок `X-Panel-Password` или `Authorization: Bearer`, либо через `/api/login`). Остальные входят личным часовым паролем из бота, см. «Вход в панель» |
| `PANEL_ALLOW_IPS` | — | IP и CIDR через запятую, которым открыта панель; остальным 403. Пусто — без фильтра. Фильтр стоит до пароля и закрывает и статику. Вход по одному IP (без пароля) — только если ключ хранилища не настроен совсем. Кривой элемент закрывает панель для всех |
| `PANEL_TRUST_PROXY` | `0` | `1` — IP клиента берётся из правого элемента `X-Forwarded-For`, но только если запрос пришёл с loopback (то есть от Caddy на этой же машине) |
| `PANEL_TUNNEL` | `1` | `0` — `remote.sh` выключает `ssau-tunnel` (cloudflared) и не трогает `WEBAPP_URL_FILE`: панель за своим Caddy |
| `PANEL_DEMO` | `0` | `1` — демо: панель на тестовых данных в памяти (`sanitizeDemoConfig` обнуляет все реальные ключи API, токены, пути БД и `SecretKey`/`SecretKeyFile`, отключает реальные пробные вызовы ИИ) |
| `WEBAPP_PUBLIC_URL` | — | HTTPS-адрес туннеля для Mini App (фолбэк, если нет файла) |
| `WEBAPP_URL_FILE` | — | Файл с текущим URL туннеля. Если задан и непуст — важнее `WEBAPP_PUBLIC_URL` |
| `LISTEN_ADDR` | `:8080` | Порт панели |
| `DATABASE_PATH` | `data/bot.db` | Путь к sqlite |
| `GROUP_ID` / `GROUP_CODE` | `531023229` / `6301-090301D` | Группа-якорь |
| `TZ` | `Europe/Samara` | Часовой пояс расписания |
| `RECORDINGS_DIR` | `recordings` | Корень записей, слайдов, конспектов |
| `BBB_DRY_RUN` | `0` | `1` — не ходить в сеть BBB (заглушка `DryJoiner`) |
| `LECTURE_PAUSE` | `1` | Замораживать сторонние «тяжёлые» процессы на время пары |
| `TYPST_BIN` | `typst` (из PATH) | Путь к бинарнику Typst (`notes/pdf.go`, печать PDF конспектов). На VDS: `/usr/local/bin/typst` |
| `PROXY_FILE` | `/opt/ssau-bot/proxies.txt` | SOCKS5-список `login:password@ip:port`, по строке. Есть файл — каждая сессия `bbb` идёт через свой прокси |
| `GEMINI_API_KEYS` | — | Ключи Google AI Studio через запятую (бесплатный тариф). Основная цепочка (`PoolChain`) для конспектов, STT, vision и пула: `gemini-4-hydrogen` → `gemini-3.6-flash` → `gemini-3.5-flash-lite`, внутри модели ключи (включая личные с `share_pool=1`) перемешиваются (`internal/llm`) |
| `OPENCODE_API_KEYS` / `OPENCODE_API_KEY` | — | OpenCode Zen, бесплатная модель `glm-5.3-flash-free` — хвост цепочки. Ключи (и в `GEMINI_API_KEYS`) подписываются владельцем: `Агеев:oc_sk_…` — подпись видна в панели |
| `LLM_API_KEY` / `LLM_API_URL` / `LLM_MODEL` | — | `cheaper` — **только админ**: его запасной вариант после своего выбора (`muse-spark-1.3-contributor` → `glm-5.3-flash`, `LLM_MODEL` в конце, если другой). В `PoolChain` конспектов не входит. **Используется и для расшифровки аудио** (`LLM_API_KEY` + `LLM_API_URL`, первый провайдер STT, до пула Gemini, модель — `CHEAPER_STT_MODEL`, см. раздел «Расшифровка лекций (STT)»). Старые `DEEPSEEK_*` читаются как фолбэк |
| `MOODLE_WORKERS` | `4` | Число параллельных воркеров в `uni.Syncer` (`MoodleWorkersLimit()`) для обхода Moodle |
| `QUIZ_MODE` | `service` | `service` — тест ставится в очередь в базе (`quiz_jobs`), решает сервис `ssau-quiz`; `inline` — решает пул в процессе `rasp`, как раньше (для локальной разработки и тестов). Подробно — раздел «Решение тестов (ssau-quiz)» |
| `QUIZ_MAX_PARALLEL` | `50` | Сколько тестов `ssau-quiz` решает одновременно, у каждого свой ключ ИИ |
| `QUIZ_MOODLE_RPS` | `3` | Входов и стартов попыток Moodle в секунду на весь процесс `ssau-quiz` (общий ограничитель). Плюс случайный разнос старта каждой задачи 0–5 с |
| `MOODLE_QUIZ_WORKERS` | `4` | Только при `QUIZ_MODE=inline`: сколько тестов решает пул процесса `rasp` |
| `DEEPGRAM_API_KEY` / `DEEPGRAM_MODEL` | — | Расшифровка аудио у Deepgram, второй провайдер STT после пула Gemini (модель по умолчанию `nova-3`, адрес — `DEEPGRAM_API_URL`, по умолчанию `https://api.deepgram.com`). Без ключа пропускается |
| `ASSEMBLYAI_API_KEY` | — | Платный фоллбэк STT для конспектов: порядок STT — deepgram → gemini → cheaper (`LLM_API_*`) → assemblyai → fish → groq; AssemblyAI идёт после Deepgram, Gemini и cheaper, если они не сработали |
| `FISH_STUDIO_*`, `GROK_*`, `GROQ_*` | — | Запасные платные ключи STT/vision: Fish и Groq идут в STT после AssemblyAI (порядок — см. раздел «Расшифровка лекций (STT)») |
| `GITHUB_TOKEN` / `GITHUB_OWNER` / `GITHUB_REPO` / `GITHUB_BRANCH` | `r4r1ty-tech` / `LectionsSSAU` / `main` | Выгрузка конспектов в GitHub. Токен `remote.sh` берёт из `LECTURES_TOKEN` или `gh auth token` на сервере |
| `VOSK_MODEL` / `VOSK_SCRIPT` | `/opt/ssau-bot/vosk-model`, `/opt/ssau-bot/wake.py` | Вейкворды |
| `WHITELIST_EXTRA` | — | Доп. Telegram id через запятую |
| `SECRET_KEY` | — | base64 от 32 байт: ключ шифрования паролей и сессий ЛК/Moodle. Пусто — ключ из `SECRET_KEY_FILE` |
| `SECRET_KEY_FILE` | `<папка базы>/secret.key` | Файл ключа; нет — создаётся сам (0600) первым процессом. Не кладите рядом с бэкапом базы: без ключа пароли из базы не восстановить, это и задумано. Потеряли ключ — пользователи подключают аккаунт заново |
| `LK_BASE_URL` / `MOODLE_BASE_URL` | `https://lk.ssau.ru` / `https://do.ssau.ru/moodle` | Адреса ЛК и Moodle (для стендов) |
| `LOG_LEVEL` | `info` | `debug/info/warn/error` |
| `LOG_FILE` | — | Если задан — писать лог ещё и в файл |
| `LOG_DEBUG` / `LOG_DIR` | `1` / `logs` | Подробный лог: каждый процесс пишет всё, включая debug, в `LOG_DIR/<процесс>.log` независимо от `LOG_LEVEL` |
| `LOG_DEBUG_MAX_MB` / `LOG_DEBUG_KEEP` | `50` / `20` | Ротация подробного лога: размер куска и сколько сжатых кусков хранить (`<процесс>.log.N.gz`) |
| `LOG_DUMP_MAX_MB` | `500` | Потолок снимков `LOG_DIR/dumps` (HTML страниц тестов, промпты и ответы ИИ, ответы с ошибкой), старые удаляются |

Шаблон — [../env.example](../env.example). `deploy/remote.sh` при первом деплое дописывает недостающие `LECTURE_PAUSE`, `VOSK_*`, `LOG_LEVEL`, `LOG_FILE` и — только при включённом туннеле — `WEBAPP_URL_FILE`.

## Вход в панель

У каждого из вайтлиста и у админа свой пароль: кнопка «🔑 Пароль в панель» внизу в боте (или `/pass`). Пароль — HMAC от ключа хранилища (`SECRET_KEY`/`SECRET_KEY_FILE`, `vault.Derive("panel-pass/v1")`), Telegram id и номера часа (`internal/panelpass`). Ничего не хранится: бот и панель считают его одинаково, и по паролю панель понимает, кто вошёл.

- Меняется раз в час; принимается пароль текущего и прошлого часа, так что выданный живёт до двух часов (в сообщении бота — «действует до ЧЧ:ММ»). Пароль нужен только для входа: панель меняет его (`POST /api/login`) на долгую сессию в куке.
- Сессия: случайный токен в куке `panel_session` (HttpOnly, Secure, SameSite=Lax, Path=/), в базе (`web_sessions`) только его sha256 и Telegram id. Срок скользящий: 30 дней с последнего захода (когда остаётся меньше 15 дней, сервер продлевает тот же токен и перевыпускает куку), потолок — 90 дней с момента входа. На человека держится не больше 20 сессий. Выход — «Ещё → Вход в панель → Выйти» (`POST /api/logout`: сессия удаляется, кука стирается). Старый вход заголовком `X-Panel-Password` / `Authorization: Bearer` работает как раньше.
- Сессию отзывает только сервер: убрали из вайтлиста — все его сессии перестают работать сразу; смена `SECRET_KEY` сессии **не** сбрасывает, для этого `DELETE FROM web_sessions;` в `bot.db`. Кука с флагом Secure не ставится по голому http (кроме localhost) — по http не из localhost панель сессию не запомнит.
- Своё у каждого: решения «заходить/нет», профиль, Moodle и ЛК, выбор ИИ, «Пройти». Общее для всех вошедших: расписание (`/api/lessons`, `/api/day`, `/api/week`), ссылки BBB (`/api/bbb` меняет ссылку для всей группы), конспекты, `/api/settings`.
- «Служебное» (`/api/now`, `/api/people*`, `/api/logs`, `/api/parser*`, `/api/test`, `/api/admin/*`) — только админу (строго `ADMIN_TELEGRAM_ID` или вход по `PANEL_PASSWORD`; суффикс логина `528` прав админа больше не даёт), остальным 403.
- Вход по `PANEL_PASSWORD` и по одному IP — от лица `ADMIN_TELEGRAM_ID`: «мои» пары, Moodle и выбор ИИ — его.
- Вайтлист задаётся конфигом (`DefaultWhitelist` + `WHITELIST_EXTRA`): убрать человека — правка `.env` и рестарт `ssau-panel`/`ssau-tg`, после этого его пароль не принимается. Выключатель «Бот заходит за меня» (`enabled`) на вход в панель не влияет. Сменить все пароли разом — новый `SECRET_KEY` (но тогда слетят и сохранённые пароли ЛК/Moodle и личные ключи ИИ, а долгие сессии останутся — см. выше).
- **Rate-limit на `POST /api/login` (`loginLimiter`):** 5 неудачных попыток с одного IP за 10 минут (`loginFailWindow`) блокируют этот IP на 5 минут (`loginBlockDuration`) с ответом `429 Too Many Requests` и заголовком `Retry-After`. От перебора дополнительно закрывает фильтр `PANEL_ALLOW_IPS`.
- Ключ хранилища (`SECRET_KEY_FILE`) по умолчанию лежит рядом с базой (`data/secret.key`): бэкап `data/` целиком раскрывает и пароли ЛК/Moodle, и личные ключи из `llm_keys`. Файл ключа пропал — создаётся новый: пароли в панель сменятся, сохранённые пароли универа и ключи ИИ не расшифруются.

## ИИ для тестов и личные ключи

- **Личные ключи в панели («Ещё → Мои ключи ИИ»):**
  - `GET /api/llm/keys` — список сохранённых провайдеров пользователя с маскированными значениями (`MaskKey`: первые 4 + `...` + последние 4 символа, либо `****`), без выдачи секретов.
  - `PUT /api/llm/keys` (или `/api/llm/keys/{provider}`) — сохранить/заменить личный ключ (`gemini` / `opencode`) или переключить флаг `share_pool` («Делиться ключом с пулом конспектов»). При вводе нового ключа выполняется изолированная проверка `llm.TestProviderKey` (не затрагивает `KeyPool` и счётчики), после чего ключ шифруется через `vault` в таблицу `llm_keys`.
  - `DELETE /api/llm/keys` / `/api/llm/keys/{provider}` — удалить свой ключ.
  - `GET /api/llm/limits` — статистика вызовов за UTC-день (`requests`, `input_tokens`, `output_tokens`), ориентировочные суточные лимиты (Gemini `1500`, OpenCode `200`) и активный `cooldown_until` после 429.
  - Межпроцессный мост `access.InitLLMBridge(st, v)` поднимается во всех сервисах с ИИ (`bbb`, `rasp`, `panel`): подмешивает ключи с `share_pool=1` в общий `PoolChain` и записывает расход и 429-кулдауны в SQLite `llm_usage`.
- **Выбор модели («Ещё → ИИ для тестов»):** каждый выбирает модель и **свой** ключ (личный из БД `llm_keys` и/или подписанный фамилией в `.env` из ФИО ЛК). Обычный человек видит и использует **только** свои ключи: чужих ключей и `cheaper` нет; при отсутствии своих ключей ИИ не решает и бот присылает текст вопросов. Админ: свой выбор → `cheaper` (`muse-spark-1.3-contributor`, затем `glm-5.3-flash`) → все ключи всех моделей. «Проверить» — один вызов, «Все модели» — живой список `/models`, хвосты всех ключей видит только админ.
- **«Пройти»** у теста в «Сдать»: `POST /api/quiz/force` (один тест) и `POST /api/quiz/force-all` (все открытые тесты) снимают отметки и выставляют `quiz_force:<tg>:<cmid>` + `moodle_kick`, запуская решение даже для просроченных тестов.
- **Админ** («Служебное»): «ИИ и ключи» — все ключи с хвостами и чей выбор какой; «Тесты Moodle» — журнал решателя у всех (`<папка базы>/quiz-debug/*.json`, хранятся последние 200, во вкладке — 50): кто (ФИО из ЛК), модель и ключ, все попытки по цепочке, ответы, сырой ответ ИИ, оценка; «Перерешать».
- ИИ не ответил или ответ не разобрался — попытка в Moodle **не завершается**, пустые ответы не уходят; человеку «⚠️ Проблема с тестом» (первый раз и дальше раз в ~3 часа), синк повторяет на каждом обходе.

Известные ограничения:
- «Проверить» при выбранной «Общей цепочке» проверяет первого провайдера с его первой моделью, а не всю цепочку.
- Строку модели панель не проверяет — опечатка в названии даст ошибку на первом шаге цепочки и откат дальше.
- Бесплатный лимит OpenCode Zen общий на аккаунт (`glm-5.3-flash-free`), поэтому OpenCode — только хвост цепочки.
- Повтор решения после неудачи не ограничен по числу попыток: пока тест открыт и не решён, каждые 30 мин тратится доступная квота (с учётом cooldown `KeyPool`).
- Между тиками разных аккаунтов синк и решение тестов распараллелены (`MOODLE_WORKERS=4`, до 4 страниц и до 4 картинок за волну в `uni`, семафор 2 одновременных вызова на ключ в `KeyPool`).

## Расшифровка лекций (STT)

Порядок провайдеров в `notes.Transcribe` (`internal/notes/fish.go`); пропускается провайдер, у которого нет ключа:

1. **deepgram** (`internal/notes/deepgram_stt.go`) — `POST {DEEPGRAM_API_URL}/v1/listen?model=…&language=ru&smart_format=true&punctuate=true&utterances=true`, тело — сырой mp3-фрагмент, заголовок `Authorization: Token …`. Модель — `DEEPGRAM_MODEL` (по умолчанию `nova-3`). Куски по 10 мин, mp3 mono 16 кГц 48 кбит/с, до 4 параллельно. Метки `[мм:сс]` строятся по `utterances`; если их нет, весь текст фрагмента получает метку его начала. Ответы 429 и 5xx повторяются дважды, как у cheaper.
2. **gemini** — бесплатный пул `GEMINI_API_KEYS`. Запись режется на куски по 10 мин с перекрытием 8 с и идёт параллельно (`transcribeGemini`). Кусок, который Gemini не расшифровал после перебора всех ключей, уходит в **deepgram** (если задан ключ), затем в **cheaper** (если задан) — только этот кусок, остальные Gemini не пересчитывает. Если запасные тоже не справились, вся расшифровка переходит к следующему провайдеру.
3. **cheaper** (`internal/notes/cheaper_stt.go`) — админский `LLM_API_KEY` + `LLM_API_URL`, запрос `POST {LLM_API_URL}/audio/transcriptions` (multipart, `language=ru`, `response_format=verbose_json`). Адрес можно задавать с `/v1` и без него, как у чат-провайдера. Модель — `CHEAPER_STT_MODEL` (по умолчанию `whisper-large-v3-turbo`). Куски по 10 мин, mp3 mono 16 кГц 48 кбит/с, до 4 параллельно. Ответы 429 и 5xx повторяются дважды: пауза по `Retry-After` (не больше минуты), иначе 2 с и 4 с.
4. **assemblyai** → 5. **fish** → 6. **groq** — платные запасные.

Правило «cheaper в конспектах не используется» (`ValidateChain`, `ErrCheaperNotAllowed`) действует для текста конспектов, слайдов и вопросов `Ask`. Исключение только одно: расшифровка аудио через этот провайдер. Расход админского ключа cheaper растёт на каждую лекцию, которую не смогли обработать пул Gemini и Deepgram.

## Кто есть кто

ФИО берётся из ЛК (`/account`, поле `fullName`) раз в сутки при синке ЛК и лежит в `settings.lk_name:<tg>`. По нему подписаны люди в «Служебном», журнале решателя и сообщении бота с паролем; пустое «имя для BBB» заполняется им же. Связка: Telegram id ↔ аккаунт универа (ЛК/Moodle) ↔ личный пароль в панель.

## Панель за Caddy

Caddyfile:

```caddy
panel.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

В `.env`:

```
LISTEN_ADDR=127.0.0.1:8080
PANEL_TRUST_PROXY=1
PANEL_ALLOW_IPS=203.0.113.10,198.51.100.0/24   # свои IP
```

Слушаем только loopback, чтобы до панели нельзя было достучаться в обход Caddy: иначе `X-Forwarded-For` подделывается, а фильтр по IP обходится. `PANEL_PASSWORD` можно не задавать, пока список IP не пуст.

Демо на тестовых данных (ни базы, ни Telegram не нужно):

```bash
PANEL_DEMO=1 LISTEN_ADDR=127.0.0.1:18080 go run ./cmd/panel
```

## Прод (ГлебПрод)

- VDS 392601 «ГлебПрод», OneDash, Прага, 4 CPU / 4 ГБ, Debian 13, `13.143.234.228`. На нём же живут выход VPN-пула (`xray-exit`), OpenFlux и сайт `gandon.broodmother.me` — их не трогать.
- Из РФ IP напрямую недоступен: SSH — через `ProxyJump` с ПродАлексей, панель — через VPN пула.
- DNS: `A ssau.r4r1ty.fun → 13.143.234.228`. Блок в `/etc/caddy/Caddyfile`:

  ```caddy
  ssau.r4r1ty.fun {
      header {
          -Server
          -Via
          defer
          Strict-Transport-Security "max-age=31536000"
      }
      encode zstd gzip
      reverse_proxy 127.0.0.1:8080
  }
  ```

  В Caddy там `admin off`, поэтому `systemctl reload caddy` падает — только `systemctl restart caddy`.
- `.env`: `LISTEN_ADDR=127.0.0.1:8080`, `PANEL_TRUST_PROXY=1`, `PANEL_TUNNEL=0`, `WEBAPP_PUBLIC_URL=https://ssau.r4r1ty.fun`, `WEBAPP_URL_FILE=` пусто, `PANEL_ALLOW_IPS` — четыре хельсинкских выхода пула (`177.3.196.241,242,243,245`), сам ГлебПрод и ПродАлексей (`194.87.52.18`), плюс `PANEL_PASSWORD` (вход админом; остальные — личным паролем из бота).
- CI ходит ключом `gh-actions-ssau-deploy` (публичная часть в `/root/.ssh/authorized_keys`, приватная — секрет `VDS_SSH_KEY`).

### Смена ключей: `ssau-keys`

Ярлык «SSAU ключи» в меню (или `~/.local/bin/ssau-keys`, исходник `cmd/ssau-keys`) открывает терминал и спрашивает ключ ИИ (cheaperinference, `https://api.cheaperinference.com/v1`, модель по умолчанию `deepseek-v4.1-flash`) и токен Telegram-бота. Пустой ввод — не менять. Ключи проверяются (`/models` + пробный запрос, `getMe`), пишутся в `/opt/ssau-bot/.env` по SSH через `moyavds`, работающие сервисы перезапускаются, `LLM_*` уходят в секреты GitHub (иначе CI-деплой вернёт старый ключ), токен бота — ещё и в локальный `.env`.

Пересборка: `go build -o ~/.local/bin/ssau-keys ./cmd/ssau-keys`.

## ЛК и Moodle

Аккаунт универа подключается в панели («Ещё → Аккаунт универа», один логин для Moodle и ЛК). Пароль проверяется на сайте и сохраняется в `accounts` зашифрованным (AES-GCM, ключ из `SECRET_KEY`/`SECRET_KEY_FILE`). Синк крутится в процессе `rasp`:

- расписание — из ЛК, если есть хоть один рабочий аккаунт ЛК (у админа приоритет), иначе с `ssau.ru/rasp`; днём обновляется каждый час;
- Moodle — раз в 30 минут лента сроков и несданное с близким сроком, полный обход курсов раз в 6 часов и сразу после подключения или кнопки «Обновить».

Сайт не принял пароль — аккаунт получает статус `bad_login` и больше не дёргается, пока пароль не обновят. Проверить клиентов на живых сайтах без бота:

```bash
read -rp 'Логин: ' UNI_LOGIN; read -rsp 'Пароль: ' UNI_PASSWORD; echo; UNI_LOGIN="$UNI_LOGIN" UNI_PASSWORD="$UNI_PASSWORD" go run ./cmd/unicheck
```

Ничего не сохраняет, печатает только что удалось достать (пары по неделям, ссылки BBB, ИОТ, курсы и сроки Moodle).

## Решение тестов (ssau-quiz)

Синк Moodle в `rasp` только находит открытые тесты. Решение идёт в отдельном сервисе `ssau-quiz` (`cmd/quiz`), а связывает их очередь в таблице `quiz_jobs` общей базы:

1. `rasp` видит открытый тест (или «Пройти» из панели) и ставит задачу `EnqueueQuizJob`. Пара (аккаунт, тест) одна: если задача уже `queued` или `running`, повтора не будет. «Пройти» поднимает приоритет (`forced`).
2. `ssau-quiz` атомарно берёт задачу (`ClaimQuizJob`): сначала «Пройти», потом тесты с ближайшим сроком закрытия, потом по времени постановки. У одного аккаунта одновременно решается один тест.
3. Пока тест решается, аренда задачи (10 минут) продлевается каждые 2 минуты. Если процесс умер, аренда истекает, и другой захват забирает задачу. Попытка Moodle при этом продолжается той же (старт не повторяется).
4. Итог задачи: `done` — тест решён; `failed` — не решён, `last_error` говорит почему. Повтор идёт по прежним правилам: через `quiz_fail` на следующем синке, после 6 неудач подряд только по «Пройти».

Остановка `ssau-quiz` (деплой, `systemctl stop`) не теряет тесты: незавершённые задачи возвращаются в `queued`, попытка Moodle продолжится. `TimeoutStopSec=60` даёт время на возврат. Деплой перезапускает `ssau-quiz` только если изменился бинарник (сравнивается sha256 в `remote.sh`); `rasp` и `tg` при деплое не трогают решение.

Вход и старт попытки в Moodle идут через общий в процессе ограничитель `QUIZ_MOODLE_RPS`, чтобы сотни входов не пришли с одного IP разом. Остальное решение (страницы, ИИ, перелогин, журнал `quiz`) — то же, что и в `inline`.

Если `ssau-quiz` не запущен, тесты копятся в очереди и не решаются, а `rasp` продолжает синк. Проверить очередь (sqlite3 на сервере, база `/opt/ssau-bot/data/bot.db`):

```bash
DB=/opt/ssau-bot/data/bot.db
# сколько задач в каждом статусе
sqlite3 -header -column "$DB" "SELECT status, COUNT(*) AS n FROM quiz_jobs GROUP BY status;"
# активные задачи в порядке захвата
sqlite3 -header -column "$DB" "SELECT id, telegram_id, cmid, title, forced, status, attempts, claimed_by, lease_until
  FROM quiz_jobs WHERE status IN ('queued','running')
  ORDER BY forced DESC, deadline IS NULL, deadline, created_at;"
# задачи, которые держит умерший воркер (аренда истекла, но статус running)
sqlite3 -header -column "$DB" "SELECT id, telegram_id, cmid, claimed_by, lease_until FROM quiz_jobs
  WHERE status='running' AND lease_until < strftime('%Y-%m-%dT%H:%M:%SZ','now');"
# последние неудачи с причиной
sqlite3 -header -column "$DB" "SELECT id, telegram_id, cmid, last_error, updated_at FROM quiz_jobs
  WHERE status='failed' ORDER BY updated_at DESC LIMIT 20;"
```

Задача с просроченной арендой уйдёт воркеру сама на следующем захвате, вручную ничего трогать не надо. Журналы — `journalctl -u ssau-quiz` и `logs/quiz.log` (строки `quiz-job <id>`, `quiz tg=… cmid=…`).

Отключить очередь и решать тесты в `rasp`, как раньше: `QUIZ_MODE=inline` в `.env` и перезапуск `ssau-rasp`. В этом режиме `ssau-quiz` не нужен, а задачи в `quiz_jobs` не создаются.

## Логи

Каждый процесс пишет `[LEVEL] component: message` в stderr (→ journald) и, если задан `LOG_FILE`, в тот же файл. Все четыре процесса могут писать в один `/opt/ssau-bot/ssau.log` — строки короткие и атомарные, префикс процесса различает источник.

```bash
# живой хвост одного сервиса
journalctl -u ssau-bbb -f

# все сервисы цели за окно времени
journalctl -u ssau-tg -u ssau-rasp -u ssau-panel -u ssau-bbb --since "2 hours ago"

# подробный лог процесса (всё, включая debug): rasp — синк ЛК/Moodle и решатель тестов,
# bbb — лекции, запись, слайды, конспекты; panel, tg
tail -f /opt/ssau-bot/logs/rasp.log
# один человек / один тест (во всех строках решателя есть tg=… cmid=…)
grep 'tg=2047776316' /opt/ssau-bot/logs/rasp.log
zgrep 'cmid=58084' /opt/ssau-bot/logs/rasp.log*
# снимки: страницы теста (quiz-view, quiz-attempt, quiz-finish), промпты и ответы ИИ (quiz-llm), лекции (notes, notes-llm)
ls -t /opt/ssau-bot/logs/dumps/quiz-attempt | head
zcat /opt/ssau-bot/logs/dumps/quiz-llm/<файл>.gz | less

# файл (после того как LOG_FILE прописан в .env)
tail -f /opt/ssau-bot/ssau.log

# только ошибки и предупреждения
journalctl -u ssau-bbb -p warning -f
```

Подробный разбор — временно поднять уровень:

```bash
printf '\nLOG_LEVEL=debug\n' >> /opt/ssau-bot/.env
systemctl restart ssau-bbb.service ssau-tg.service
# ... после диагностики вернуть info
```

Что искать по симптомам:

- заход не состоялся — `[WARN] bbb: join fail <key> tg=<id>: <причина>`;
- успешный заход — `[INFO] bbb: seated key=<key> state=room|lobby`;
- вылет и retry — `[WARN] bbb: drop retry ...`;
- нет ссылки — `[WARN] bbb: no bbb link lesson=<id>`;
- лобби >2 мин — `[WARN] bbb: lobby >2m ...`;
- тест во время пары — `[WARN] bbb: test paused: lecture in progress`;
- расписание — `[INFO] rasp: lessons=<n> online=<n> next_week=<bool>` и `rasp: diff:`;
- панель — `[WARN] panel: <method> <path> <status> <duration>` (ошибки), debug — все запросы.

Вкладка «Логи» в панели читает таблицу `events` (последние N), а не файл/ journald. Это разные представления: `events` — продуктовые события, `LOG_LEVEL`/`LOG_FILE` — технические.

## systemd

| Unit | Роль |
| --- | --- |
| `ssau.target` | Поднимает все сервисы |
| `ssau-tg.service` | Telegram-бот |
| `ssau-rasp.service` | Парсер + cron |
| `ssau-quiz.service` | Решение тестов Moodle из очереди `quiz_jobs` (`cmd/quiz`), см. «Решение тестов (ssau-quiz)» |
| `ssau-panel.service` | Mini App + API |
| `ssau-bbb.service` | BBB-воркер на Go (`DirectJoiner`: DDP + Pion WebRTC + ffmpeg + Vosk) |
| `ssau-tunnel.service` | HTTPS-туннель (cloudflared quick tunnel) — запасной, без своего домена. На проде выключен (`PANEL_TUNNEL=0`), в `ssau.target` не входит |

```bash
systemctl status ssau.target ssau-bbb.service
systemctl restart ssau-bbb.service
systemctl stop ssau.target          # остановить всё
cat /etc/systemd/system/ssau-bbb.service
```

`ssau-bbb` специально не стартует автоматически при деплое, если был выключен (`remote.sh` обновляет бинарник, но сервис не поднимает). Включается вручную.

## Деплой

Автоматически: пуш в `main` → GitHub Actions (`.github/workflows/deploy.yml`) → тесты → сборка linux/amd64 → `scp` в `/tmp/ssau-deploy` → `deploy/remote.sh`.

Руками то же самое:

```bash
for b in tg rasp quiz panel bbb; do GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/$b ./cmd/$b; done
scp -J moyavds bin/tg bin/rasp bin/quiz bin/panel bin/bbb deploy/ssau-*.service deploy/ssau.target deploy/remote.sh deploy/tunnel.sh deploy/wake.py root@13.143.234.228:/tmp/ssau-deploy/
ssh -J moyavds root@13.143.234.228 'chmod +x /tmp/ssau-deploy/*; bash /tmp/ssau-deploy/remote.sh /tmp/ssau-deploy'
```

`remote.sh` требует все четыре бинарника, все юниты, `tunnel.sh` и `wake.py` (ищет в папке, её `bin/` и `deploy/`). Из РФ сервер напрямую не виден — через `-J moyavds`. Выключенный `ssau-bbb` после деплоя поднимать руками: `systemctl start ssau-bbb`.

`remote.sh`:
- доставляет бинарники, юниты, `tunnel.sh`, `wake.py` (и удаляет устаревший `audio.sh`);
- через apt ставит недостающее: `ffmpeg`, `ca-certificates`, `python3`, `python3-pip`, `unzip`;
- кладёт `typst` в `/usr/local/bin/typst` из артефакта деплоя (бинарник v0.15.1, sha256 проверяет CI в `.github/workflows/deploy.yml`);
- создаёт swap до 8 ГБ (оставляя ~10 ГБ резерва на диске), ставит `vosk` и модель (если нет), качает `cloudflared`;
- при отсутствии `/opt/ssau-bot/.env` останавливается и просит скопировать его;
- атомарно (через временный файл `0600` в Python) обновляет `LLM_*` и `GITHUB_*` в `/opt/ssau-bot/.env` (запрещая переводы строк в значениях) и дописывает дефолты (`LECTURE_PAUSE`, `VOSK_*`, `LOG_*`);
- при `PANEL_TUNNEL=0` отключает `ssau-tunnel.service`, иначе поднимает `tunnel.sh` (который пишет свежий URL туннеля в `webapp_url` и атомарно обновляет `WEBAPP_PUBLIC_URL` в `.env`).

Секреты Actions: `VDS_SSH_KEY` (обязателен), `VDS_HOST`, `VDS_USER`, `VDS_PORT`; для админского `cheaper` `LLM_API_KEY/URL/MODEL` или `DEEPSEEK_API_KEY`, для выгрузки `LECTURES_TOKEN`. Деплой срабатывает на пуш в `main`, с другой ветки — `gh workflow run deploy.yml --ref <ветка>`.

## Сброс данных и миграция дубликатов

- **Автоматическая дедупликация паков:** при открытии базы (`store.Open`) выполняется миграция `migrateDeduplicatePacks()` — группирует существующие `lecture_packs` по `meeting_key` (при его отсутствии вычисляет `model.MeetingKey` по привязанному уроку), оставляет канонический пак (приоритет статуса `done > notes > transcribe > slides > recorded > recording > error > empty`, далее меньший `lesson_id`) и переносит все привязки уроков в `pack_lessons`.
- **Полный сброс на VDS:** `Actions → Reset VDS data → Run workflow` вызывает `deploy/reset-data.sh` на сервере: останавливает `ssau-tg`, `ssau-bbb`, `ssau-panel`, `ssau-rasp`, удаляет `data/*.db*` (включая `-wal`/`-shm`/`-journal`) и содержимое `recordings/`, не трогая `.env`, бинарники и `vosk-model`, затем запускает сервисы заново.

## Диагностика

**Бот не заходит на пару**

1. `journalctl -u ssau-bbb -n 200` — есть ли `join key=...`, чем кончилось.
2. Нет `join` вообще — проверь, что `ssau-bbb.service` запущен и пара `online` в `/api/now`.
3. `no bbb link` — нет ссылки на пару; добавь её в панели («Ещё → Комнаты BBB» или у пары).
4. `join fail ... форма гостя` — Greenlight не отдал `csrf-token` или форму `[join_name]`; смотри `LOG_LEVEL=debug` (логи `direct:`).
5. `лобби >2 мин` — модератор не пускает (`guestStatus=WAIT`); алерт приходит админу.

**Бот в комнате, а запись пустая / тихая**

Запись идёт так: пишущая сессия подключается к `bbb-webrtc-sfu` в режиме `listen-only` через `pion/webrtc/v4` → incoming RTP `audio/opus` пишется напрямую в `audio-*.ogg` (`capture.OpusRec`), а параллельно через pipe в `ffmpeg` декодируется 16 кГц PCM для вейкворда.

```bash
grep -aE 'audioOnce|audio joined|sfu|audio level|silent|ffmpeg|vosk' /opt/ssau-bot/ssau.log | tail -40
```

1. `audioOnce: ... listen-only ready` или `already in audio` — WebRTC-аудио к SFU поднято. Ошибки `EnsureAudio` / `sfu` означают проблему сигналинга, TURN/STUN или ICE.
2. `audio level ... peak=... dBFS` раз в минуту. 5 минут тишины подряд — WARN `silent` и сообщение админу.
3. `capture: opus rec ...` / `ffmpeg: ...` — декодер PCM и размер `.ogg`. `OpusRec.Stop: ... packets=... size=N` показывает число принятых RTP-пакетов и байт.
   `vosk lags: dropped N chunks` — вейкворд не успевает, запись от этого не страдает.
   Vosk распознаёт всю речь и занимает около половины ядра (запущен с nice 10); срабатывание приходит после конца фразы, до ~20 с.
4. Проверка тракта и WebRTC/Opus без живого BBB — `go test -v -race ./internal/capture/... ./internal/bbb/...`.

**Панель не открывается**

- `systemctl status ssau-panel caddy`; `curl -sI https://ssau.r4r1ty.fun` с разрешённого IP (из РФ — только через VPN пула);
- 403 `forbidden` — IP не в `PANEL_ALLOW_IPS` (в логе `allow: отказ ip=…`);
- 401 — пароль истёк (живёт до 2 часов) или чужой: новый кнопкой «🔑 Пароль в панель» в боте; в логе `bad password`;
- «Пароли в панель сейчас не выдаются» в боте — не загрузился ключ хранилища (`SECRET_KEY`/`SECRET_KEY_FILE`);
- кнопка «Панель» в Telegram ведёт не туда — `WEBAPP_PUBLIC_URL` в `.env`, `ssau-tg` переставляет кнопку всем из вайтлиста за 20 с (`journalctl -u ssau-tg | grep 'webapp url='`);
- запасной вариант без домена — cloudflared: убрать `PANEL_TUNNEL=0`, задеплоить; `tunnel.sh` пишет свежий URL в `WEBAPP_URL_FILE` (`/opt/ssau-bot/webapp_url`), бот подхватывает его сам.

**VDS «встал» на старте пары**

- `dmesg -T | tail -50` — искать `oom-kill`;
- `free -m`, `ps aux --sort=-%mem | head`;
- `ffmpeg` + Vosk (и разовый `typst` после пары для сборки PDF, пик около 60 МБ): смотри `free -m`, `ps aux --sort=-%mem | head`;
- логи `bbb:`, `capture:`.

**Парсер отдаёт 403/пусто**

- `journalctl -u ssau-rasp -n 100`: `fetch status=403`, `parse: ...`;
- `Fetch` сам повторяет запрос через `www.ssau.ru`; при смене вёрстки правится `internal/rasp/parse.go`.

## Тесты и качество

```bash
go test ./...                     # всё
CGO_ENABLED=0 go test ./...       # как в CI
go test ./internal/bbb/ -race     # конкурентный воркер
go test ./... -cover              # покрытие
go vet ./...
```

CI (`deploy.yml`) гоняет `CGO_ENABLED=0 go test ./...` на каждый PR и push в `main`. Тесты изолированы: временный sqlite (`t.TempDir`), `httptest` вместо внешних API, `DryJoiner` вместо живого `bbb.ssau.ru` (а протокол `DirectJoiner`/Pion/Opus покрыт локальными mock-серверами).