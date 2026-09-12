# Правила и Контекст Проекта: Бот лекций SSAU (BBB)

В этом файле сохранен ключевой контекст, правила и знания, сформированные в рамках работы над проектом.

## Архитектура проекта

Проект представляет собой комплекс микросервисов на Go:
- `cmd/tg` / `internal/tg`: Telegram-бот с меню просмотра записей, алертером и онбордингом.
- `cmd/rasp` / `internal/rasp`: Парсер расписания `ssau.ru/rasp`.
- `cmd/panel` / `internal/panel`: Mini App и админская панель.
- `cmd/bbb` / `internal/bbb`: Модуль автоматического захода на онлайн-лекции BBB через Chromium (`rod`).

## Добавленный функционал и модули

1. **`internal/media/recorder.go`**:
   - Запись видео лекции в формате `.mp4` через `ffmpeg` (720p 15fps, 100-200 МБ на лекцию).
   - Быстрый экспорт аудиодорожки `.ogg` для передачи в транскрибатор.

2. **`internal/stt/fishstudio.go`**:
   - Интеграция с **Fish Studio STT API** (`https://api.fish.audio/v1/stt`) для распознавания речи в текст без использования ресурсов ОЗУ/GPU сервера.

3. **`internal/llm/reporter.go`**:
   - Генерация отчетов лекций (TL;DR, темы, термины, д/з, вопросы) через LLM (`gpt-4o-mini` или `deepseek/deepseek-chat`).
   - Автоматический фоллбэк `generateFallbackReport`, если `LLM_API_KEY` отсутствует.

4. **`internal/notify/alerter.go`**:
   - Мгновенные экстренные пуш-уведомления в Telegram всем пользователям белого списка при обнаружении вейквордов (`тест`, `moodle`, `контрольная`, `квиз`, `экзамен`).

5. **`internal/procmanager/scheduler.go`**:
   - Ранжирование задач (Живой браузер > Извлечение аудио > STT/LLM).
   - Использование временных файлов на диске (`/tmp`) и семафор ограничения параллельных процессов `ffmpeg` (для работы на 4 ГБ VDS).

6. **`internal/tg/recordings_menu.go`**:
   - Меню бота с кнопками: `📅 Сегодня`, `🔗 Ссылки`, `📁 Архив лекций и отчетов`, `⚙️ Настройки`, `❓ Помощь`.
   - Двухуровневое Inline-меню: **Предмет** $\rightarrow$ **Номер лекции** $\rightarrow$ Выдача **видео с VDS**, **LLM-конспекта** и **файла расшифровки**.

## Конфигурация (.env)

Ключевые переменные окружения:
- `TELEGRAM_BOT_TOKEN`: Токен бота от @BotFather.
- `ADMIN_TELEGRAM_ID`: ID администратора Telegram.
- `FISH_STUDIO_API_KEY`: API ключ от Fish Studio STT.
- `LLM_API_KEY`: API ключ от OpenAI или OpenRouter.
- `LLM_BASE_URL`: URL эндпоинта LLM (`https://api.openai.com/v1` или `https://openrouter.ai/api/v1`).
- `LLM_MODEL`: Модель LLM (`gpt-4o-mini` или `deepseek/deepseek-chat`).
- `VIDEO_RECORDINGS_DIR`: Папка хранения видео на VDS (`recordings/video`).
- `MAX_CONCURRENT_MEDIA_JOBS`: Ограничение процессов media (по умолчанию 1).
