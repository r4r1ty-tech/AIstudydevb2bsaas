# Эксплуатация

Конфиг, логи, деплой, диагностика. Устройство — в [ARCHITECTURE.md](ARCHITECTURE.md), замысел — в [../PLAN.md](../PLAN.md).

## Конфиг и переменные окружения

Один `.env` на все процессы (`internal/config`). Реальные env имеют приоритет над файлом. Секреты в git не кладём.

| Переменная | Дефолт | Назначение |
| --- | --- | --- |
| `TELEGRAM_BOT_TOKEN` | — | Bot API-токен. Нужен `tg` и `panel` |
| `ADMIN_TELEGRAM_ID` | `1074442235` | Админ: `/panel`, `/test`, пуш-уведомления |
| `PANEL_PASSWORD` | — | Пароль API панели (заголовок `X-Panel-Password` или `Authorization: Bearer`) |
| `PANEL_ALLOW_IPS` | — | IP и CIDR через запятую, которым открыта панель; остальным 403. Пусто — без фильтра. Без `PANEL_PASSWORD` API отвечает только при непустом списке. Кривой элемент закрывает панель для всех |
| `PANEL_TRUST_PROXY` | `0` | `1` — IP клиента берётся из правого элемента `X-Forwarded-For`, но только если запрос пришёл с loopback (то есть от Caddy на этой же машине) |
| `PANEL_DEMO` | `0` | `1` — демо: панель на тестовых данных в памяти, без бота, базы и пароля |
| `WEBAPP_PUBLIC_URL` | — | HTTPS-адрес туннеля для Mini App (фолбэк, если нет файла) |
| `WEBAPP_URL_FILE` | — | Файл с текущим URL туннеля. Если задан и непуст — важнее `WEBAPP_PUBLIC_URL` |
| `LISTEN_ADDR` | `:8080` | Порт панели |
| `DATABASE_PATH` | `data/bot.db` | Путь к sqlite |
| `GROUP_ID` / `GROUP_CODE` | `531023229` / `6301-090301D` | Группа-якорь |
| `TZ` | `Europe/Samara` | Часовой пояс расписания |
| `RECORDINGS_DIR` | `recordings` | Корень записей, слайдов, конспектов |
| `BBB_DRY_RUN` | `0` | `1` — не ходить в сеть BBB (заглушка `DryJoiner`) |
| `LECTURE_PAUSE` | `1` | Замораживать сторонние «тяжёлые» процессы на время пары |
| `CHROME_BIN` | — | Путь к Chromium (используется только в `notes/pdf.go` для печати PDF конспектов) |
| `PROXY_FILE` | `/opt/ssau-bot/proxies.txt` | SOCKS5-список `login:password@ip:port`, по строке. Есть файл — каждая сессия `bbb` идёт через свой прокси |
| `LLM_API_KEY` / `LLM_API_URL` / `LLM_MODEL` | — | LLM для конспекта. Любой OpenAI-совместимый endpoint `chat/completions` (DeepSeek, Groq, OpenRouter, локальный и т.п.). URL можно и с `/v1`, и без; `LLM_MODEL` обязателен. Старые имена `DEEPSEEK_API_KEY/URL/MODEL` читаются как фолбэк (с WARN) — лучше переименовать |
| `ASSEMBLYAI_API_KEY` | — | Расшифровка лекций (STT), основной провайдер. Нет ключа или ошибка — дальше Fish, затем Groq |
| `FISH_STUDIO_*`, `GROK_*`, `GROQ_*` | — | Ключи STT/vision/конспектов |
| `GITHUB_TOKEN` / `GITHUB_OWNER` / `GITHUB_REPO` / `GITHUB_BRANCH` | `r4r1ty-tech` / `LectionsSSAU` / `main` | Выгрузка конспектов в GitHub. Токен `remote.sh` берёт из `LECTURES_TOKEN` или `gh auth token` на сервере |
| `VOSK_MODEL` / `VOSK_SCRIPT` | `/opt/ssau-bot/vosk-model`, `/opt/ssau-bot/wake.py` | Вейкворды |
| `WHITELIST_EXTRA` | — | Доп. Telegram id через запятую |
| `SECRET_KEY` | — | base64 от 32 байт: ключ шифрования паролей и сессий ЛК/Moodle. Пусто — ключ из `SECRET_KEY_FILE` |
| `SECRET_KEY_FILE` | `<папка базы>/secret.key` | Файл ключа; нет — создаётся сам (0600) первым процессом. Не кладите рядом с бэкапом базы: без ключа пароли из базы не восстановить, это и задумано. Потеряли ключ — пользователи подключают аккаунт заново |
| `LK_BASE_URL` / `MOODLE_BASE_URL` | `https://lk.ssau.ru` / `https://do.ssau.ru/moodle` | Адреса ЛК и Moodle (для стендов) |
| `LOG_LEVEL` | `info` | `debug/info/warn/error` |
| `LOG_FILE` | — | Если задан — писать лог ещё и в файл |

Шаблон — [../env.example](../env.example). `deploy/remote.sh` при первом деплое дописывает недостающие `LECTURE_PAUSE`, `VOSK_*`, `LOG_LEVEL`, `LOG_FILE`, `WEBAPP_URL_FILE`.

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

## ЛК и Moodle

Аккаунт универа подключается в боте (`/connect`, «Профиль → Аккаунт универа») или в панели («Ещё → Аккаунт универа»). Пароль проверяется на сайте и сохраняется в `accounts` зашифрованным (AES-GCM, ключ из `SECRET_KEY`/`SECRET_KEY_FILE`), сообщение с паролем бот удаляет из чата. Синк крутится в процессе `rasp`:

- расписание — из ЛК, если есть хоть один рабочий аккаунт ЛК (у админа приоритет), иначе с `ssau.ru/rasp`; днём обновляется каждый час;
- Moodle — раз в 30 минут лента сроков и несданное с близким сроком, полный обход курсов раз в 6 часов и сразу после подключения или кнопки «Обновить».

Сайт не принял пароль — аккаунт получает статус `bad_login` и больше не дёргается, пока пароль не обновят. Проверить клиентов на живых сайтах без бота:

```bash
read -rp 'Логин: ' UNI_LOGIN; read -rsp 'Пароль: ' UNI_PASSWORD; echo; UNI_LOGIN="$UNI_LOGIN" UNI_PASSWORD="$UNI_PASSWORD" go run ./cmd/unicheck
```

Ничего не сохраняет, печатает только что удалось достать (пары по неделям, ссылки BBB, ИОТ, курсы и сроки Moodle).

## Логи

Каждый процесс пишет `[LEVEL] component: message` в stderr (→ journald) и, если задан `LOG_FILE`, в тот же файл. Все четыре процесса могут писать в один `/opt/ssau-bot/ssau.log` — строки короткие и атомарные, префикс процесса различает источник.

```bash
# живой хвост одного сервиса
journalctl -u ssau-bbb -f

# все сервисы цели за окно времени
journalctl -u ssau-tg -u ssau-rasp -u ssau-panel -u ssau-bbb --since "2 hours ago"

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
| `ssau-panel.service` | Mini App + API |
| `ssau-bbb.service` | BBB-воркер на Go (`DirectJoiner`: DDP + Pion WebRTC + ffmpeg + Vosk) |
| `ssau-tunnel.service` | HTTPS-туннель (cloudflared) |

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
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o bin/bbb ./cmd/bbb
scp bin/bbb root@95.182.114.82:/tmp/ssau-deploy/
ssh root@95.182.114.82 'bash /tmp/ssau-deploy/remote.sh /tmp/ssau-deploy'
```

`remote.sh`:
- доставляет бинарники, юниты, `tunnel.sh`, `wake.py`;
- через apt ставит недостающее: `chromium` (для PDF конспектов), `ffmpeg`, `fonts-liberation`, `python3`, `unzip`;
- создаёт swap при необходимости;
- ставит Vosk-модель (если нет);
- при отсутствии `/opt/ssau-bot/.env` останавливается и просит скопировать его.

Секреты Actions: `VDS_SSH_KEY` (обязателен), `VDS_HOST`, `VDS_USER`, `VDS_PORT`.

## Сброс данных

`Actions → Reset VDS data → Run workflow` вызывает `deploy/reset-data.sh` на сервере (БД и конспекты).

## Диагностика

**Бот не заходит на пару**

1. `journalctl -u ssau-bbb -n 200` — есть ли `join key=...`, чем кончилось.
2. Нет `join` вообще — проверь, что `ssau-bbb.service` запущен и пара `online` в `/api/now`.
3. `no bbb link` — нет ссылки на пару; пришли её боту (привяжется к идущей паре).
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

- `systemctl status ssau-panel` и `ssau-tunnel`;
- Telegram Mini App требует валидный HTTPS. При быстром `cloudflared` URL меняется при каждом рестарте туннеля: `tunnel.sh` пишет его в `WEBAPP_URL_FILE` (`/opt/ssau-bot/webapp_url`), а `ssau-tg` читает файл каждые 20 с и сам переставляет кнопку админу — рестарт не нужен. Проверь `cat /opt/ssau-bot/webapp_url` и `journalctl -u ssau-tg | grep 'webapp url='`;
- если файла нет, используется статичный `WEBAPP_PUBLIC_URL` — годится для именованного туннеля/ngrok со своим доменом;
- 401 — неверный `PANEL_PASSWORD`.

**VDS «встал» на старте пары**

- `dmesg -T | tail -50` — искать `oom-kill`;
- `free -m`, `ps aux --sort=-%mem | head`;
- `ffmpeg` + Vosk (и разовый `chromium` после пары для сборки PDF): смотри `free -m`, `ps aux --sort=-%mem | head`;
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