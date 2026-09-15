# Эксплуатация

Конфиг, логи, деплой, диагностика. Устройство — в [ARCHITECTURE.md](ARCHITECTURE.md), замысел — в [../PLAN.md](../PLAN.md).

## Конфиг и переменные окружения

Один `.env` на все процессы (`internal/config`). Реальные env имеют приоритет над файлом. Секреты в git не кладём.

| Переменная | Дефолт | Назначение |
| --- | --- | --- |
| `TELEGRAM_BOT_TOKEN` | — | Bot API-токен. Нужен `tg` и `panel` |
| `ADMIN_TELEGRAM_ID` | `1074442235` | Админ: `/panel`, `/test`, пуш-уведомления |
| `PANEL_PASSWORD` | — | Пароль API панели (заголовок `X-Panel-Password` или `Authorization: Bearer`) |
| `WEBAPP_PUBLIC_URL` | — | HTTPS-адрес туннеля для Mini App (фолбэк, если нет файла) |
| `WEBAPP_URL_FILE` | — | Файл с текущим URL туннеля. Если задан и непуст — важнее `WEBAPP_PUBLIC_URL` |
| `LISTEN_ADDR` | `:8080` | Порт панели |
| `DATABASE_PATH` | `data/bot.db` | Путь к sqlite |
| `GROUP_ID` / `GROUP_CODE` | `531023229` / `6301-090301D` | Группа-якорь |
| `TZ` | `Europe/Samara` | Часовой пояс расписания |
| `RECORDINGS_DIR` | `recordings` | Корень записей, слайдов, конспектов |
| `BBB_DRY_RUN` | `0` | `1` — не трогать Chromium (заглушка) |
| `LECTURE_PAUSE` | `1` | Замораживать сторонние «тяжёлые» процессы на время пары |
| `CHROME_BIN` / `CHROME_USER_DATA_DIR` | — | Путь к Chromium и его профилю |
| `DEEPSEEK_API_KEY` / `DEEPSEEK_API_URL` / `DEEPSEEK_MODEL` | `.../deepseek.com` / `deepseek-chat` | LLM для конспекта (OpenAI-совместимый `chat/completions`). URL можно и с `/v1`, и без |
| `FISH_STUDIO_*`, `GROK_*`, `GROQ_*` | — | Ключи STT/vision/конспектов |
| `VOSK_MODEL` / `VOSK_SCRIPT` | `/opt/ssau-bot/vosk-model`, `/opt/ssau-bot/wake.py` | Вейкворды |
| `WHITELIST_EXTRA` | — | Доп. Telegram id через запятую |
| `LOG_LEVEL` | `info` | `debug/info/warn/error` |
| `LOG_FILE` | — | Если задан — писать лог ещё и в файл |

Шаблон — [../env.example](../env.example). `deploy/remote.sh` при первом деплое дописывает недостающие `LECTURE_PAUSE`, `CHROME_USER_DATA_DIR`, `VOSK_*`, `LOG_LEVEL`, `LOG_FILE`.

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
| `ssau-bbb.service` | Chromium-воркер. `ExecStartPre=audio.sh` (PulseAudio) |
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
- доставляет бинарники, юниты, `tunnel.sh`, `audio.sh`, `wake.py`;
- через apt ставит недостающее: chromium, ffmpeg, pulseaudio, python3, unzip;
- создаёт swap при необходимости;
- ставит Vosk-модель (если нет);
- при отсутствии `/opt/ssau-bot/.env` останавливается и просит скопировать его.

Секреты Actions: `VDS_SSH_KEY` (обязателен), `VDS_HOST`, `VDS_USER`, `VDS_PORT`.

## Сброс данных

`Actions → Reset VDS data → Run workflow` вызывает `deploy/reset-data.sh` на сервере (БД, конспекты, профиль Chrome). Данные профиля Chrome нужно сбрасывать, если Chromium залип на битом профиле.

## Доступ агента (opencode) к VDS

Агенту не нужен root-shell и не нужно видеть секреты. Схема: юзер `agent` + SSH **forced command** на whitelisted диспетчер + правила `opencode.json` (`.env` и ключи на чтение закрыты). Агент умеет только `status`/`units`/`logs`/`restart`/`webapp-url`/`env-keys`/`disk`.

Установка идёт через CI/CD (на VDS ничего не собирается): публичный ключ кладётся в секрет `AGENT_SSH_PUBKEY`, и `remote.sh` при деплое идемпотентно ставит юзера/диспетчер/sudoers/ключ. Отзыв и детали — [../deploy/agent-ssh/README.md](../deploy/agent-ssh/README.md).

```bash
ssh-keygen -t ed25519 -f ~/.ssh/ssau_agent_ed25519 -C opencode-agent -N ""
gh secret set AGENT_SSH_PUBKEY --repo r4r1ty-tech/AIstudydevb2bsaas < ~/.ssh/ssau_agent_ed25519.pub
ssh ssau env-keys
```

## Диагностика

**Бот не заходит на пару**

1. `journalctl -u ssau-bbb -n 200` — есть ли `join key=...`, чем кончилось.
2. Нет `join` вообще — проверь, что `ssau-bbb.service` запущен и пара `online` в `/api/now`.
3. `no bbb link` — нет ссылки на пару; пришли её боту (привяжется к идущей паре).
4. `join fail ... форма гостя` — не нашлась форма/кнопка входа; смотри `LOG_LEVEL=debug` и `pageHint` в логе.
5. `лобби >2 мин` — модератор не пускает; алерт приходит админу.

**Панель не открывается**

- `systemctl status ssau-panel` и `ssau-tunnel`;
- Telegram Mini App требует валидный HTTPS. При быстром `cloudflared` URL меняется при каждом рестарте туннеля: `tunnel.sh` пишет его в `WEBAPP_URL_FILE` (`/opt/ssau-bot/webapp_url`), а `ssau-tg` читает файл каждые 20 с и сам переставляет кнопку админу — рестарт не нужен. Проверь `cat /opt/ssau-bot/webapp_url` и `journalctl -u ssau-tg | grep 'webapp url='`;
- если файла нет, используется статичный `WEBAPP_PUBLIC_URL` — годится для именованного туннеля/ngrok со своим доменом;
- 401 — неверный `PANEL_PASSWORD`.

**VDS «встал» на старте пары**

- `dmesg -T | tail -50` — искать `oom-kill`;
- `free -m`, `ps aux --sort=-%mem | head`;
- Chromium + ffmpeg + Vosk на 4 ГБ: смотри, не поднимается ли лишний процесс;
- логи `bbb: chromium`, `capture: ffmpeg pid=...`.

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

CI (`deploy.yml`) гоняет `CGO_ENABLED=0 go test ./...` на каждый PR и push в `main`. Тесты изолированы: временный sqlite (`t.TempDir`), `httptest` вместо внешних API, `DryJoiner` вместо Chromium.