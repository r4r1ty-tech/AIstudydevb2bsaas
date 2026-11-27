package mcpapp

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// PromptPrepareLecture — SOP подготовки к лекции.
	PromptPrepareLecture = "moodle_prepare_lecture"
	// PromptMakeNotes — SOP сборки конспекта.
	PromptMakeNotes = "moodle_make_notes"
	// PromptExamReview — SOP подготовки к зачёту или экзамену.
	PromptExamReview = "moodle_exam_review"
	// PromptSolveQuiz — SOP прохождения теста по шагам.
	PromptSolveQuiz = "moodle_solve_quiz"
)

// registerPrompts добавляет три готовых сценария работы с Moodle. Сервер не
// вызывает LLM: промпт лишь задаёт порядок инструментов и критерии результата.
func registerPrompts(s *mcp.Server) {
	s.AddPrompt(&mcp.Prompt{
		Name:        PromptPrepareLecture,
		Title:       "Подготовка к лекции",
		Description: "Собрать из Moodle всё нужное к лекции: материалы, файлы, сроки и уже готовые конспекты. Только чтение.",
		Arguments: []*mcp.PromptArgument{
			{Name: "course", Title: "Курс", Description: "Название или shortname курса Moodle", Required: true},
			{Name: "topic", Title: "Тема", Description: "Тема или раздел лекции (необязательно)"},
			{Name: "cmid", Title: "Модуль", Description: "id модуля курса, если он уже известен", Required: false},
		},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return promptResult(req, PromptPrepareLecture, prepareLecturePrompt(promptArgs(req)))
	})

	s.AddPrompt(&mcp.Prompt{
		Name:        PromptMakeNotes,
		Title:       "Сделать конспект",
		Description: "Собрать структурированный конспект лекции по материалам Moodle и локальным записям.",
		Arguments: []*mcp.PromptArgument{
			{Name: "course", Title: "Курс", Description: "Название или shortname курса Moodle", Required: true},
			{Name: "topic", Title: "Тема", Description: "Тема лекции (необязательно)"},
			{Name: "cmid", Title: "Модуль", Description: "id модуля курса, если он уже известен", Required: false},
		},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return promptResult(req, PromptMakeNotes, makeNotesPrompt(promptArgs(req)))
	})

	s.AddPrompt(&mcp.Prompt{
		Name:        PromptExamReview,
		Title:       "Подготовка к экзамену",
		Description: "Построить план повторения по курсу: сроки, материалы, конспекты и вопросы для самопроверки.",
		Arguments: []*mcp.PromptArgument{
			{Name: "course", Title: "Курс", Description: "Название или shortname курса Moodle", Required: true},
			{Name: "topic", Title: "Тема", Description: "Раздел или тема для повторения (необязательно)"},
		},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return promptResult(req, PromptExamReview, examReviewPrompt(promptArgs(req)))
	})

	s.AddPrompt(&mcp.Prompt{
		Name:        PromptSolveQuiz,
		Title:       "Пройти тест Moodle",
		Description: "Пошаговый SOP: информация о тесте, материалы, попытка, отправка страниц, завершение и разбор. Запись в Moodle — только по явному confirm.",
		Arguments: []*mcp.PromptArgument{
			{Name: "cmid", Title: "Тест", Description: "id модуля теста (cmid) в Moodle", Required: true},
			{Name: "course", Title: "Курс", Description: "Название или shortname курса для поиска материалов (необязательно)"},
			{Name: "time_limit", Title: "Лимит времени", Description: "Сколько времени есть на решение, например 40m (необязательно)"},
		},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return promptResult(req, PromptSolveQuiz, solveQuizPrompt(promptArgs(req)))
	})
}

// promptArgs достаёт аргументы запроса без паники на пустом запросе.
func promptArgs(req *mcp.GetPromptRequest) map[string]string {
	if req == nil || req.Params == nil || req.Params.Arguments == nil {
		return map[string]string{}
	}
	return req.Params.Arguments
}

// argOr возвращает значение аргумента или пояснение, что он не задан.
func argOr(args map[string]string, key, fallback string) string {
	if v := strings.TrimSpace(args[key]); v != "" {
		return v
	}
	return fallback
}

// promptResult оборачивает текст SOP в ответ prompts/get.
func promptResult(_ *mcp.GetPromptRequest, name, text string) (*mcp.GetPromptResult, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("mcpapp: пустой промпт %s", name)
	}
	return &mcp.GetPromptResult{
		Description: "SOP Moodle MCP: " + name,
		Messages: []*mcp.PromptMessage{{
			Role:    mcp.Role("user"),
			Content: &mcp.TextContent{Text: text},
		}},
	}, nil
}

// prepareLecturePrompt — порядок чтения и критерии готового плана подготовки.
func prepareLecturePrompt(args map[string]string) string {
	course := argOr(args, "course", "(курс не указан)")
	topic := argOr(args, "topic", "(вся ближайшая лекция)")
	cmid := argOr(args, "cmid", "(не задан)")
	return fmt.Sprintf(`Подготовь меня к лекции по курсу %q, тема: %s. Модуль cmid: %s.

Работай только через MCP-инструменты Moodle и только на чтение. Не входи в тесты, не вызывай startattempt/submit, не решай задания.

Порядок действий:
1. moodle_status — проверь сессию. Если authenticated=false, остановись и покажи warnings; не выдумывай данные.
2. moodle_courses — найди курс по названию или shortname %q. Если подходящих курсов несколько, перечисли их и попроси уточнить, а не выбирай наугад.
3. moodle_course_outline — если cmid задан, открой нужный модуль; иначе просмотри структуру курса и найди раздел с темой %s.
4. moodle_deadlines — выпиши ближайшие тесты, задания и сроки по этому курсу.
5. Материалы читай в таком порядке: moodle_material_read (страницы, книги, уроки), moodle_file_get с extract_text=true (PDF, презентации, документы). Для больших файлов сначала бери оглавление или первые страницы, затем углубляйся.
6. moodle_notes с discipline, равной названию курса, — проверь, есть ли уже готовый конспект; при необходимости прочитай его через action=read.

Итог оформи по-русски:
- что будет на лекции: 3–6 пунктов;
- ключевые термины и определения;
- что прочитать заранее и в каком порядке (с указанием модуля или файла);
- вопросы, которые стоит задать преподавателю;
- оценка времени на подготовку;
- отдельный блок «пробелы»: чего нет в Moodle или что прочитать не удалось.

Критерий готового результата: использованы все доступные по теме материалы и сроки; у каждого факта указан источник (название модуля или файла); нет выдуманных ссылок, дат и требований; недоступные данные честно помечены. Секреты, куки и пароли не запрашивай и не выводи.`, course, topic, cmid, course, topic)
}

// makeNotesPrompt — порядок сборки конспекта и требования к структуре.
func makeNotesPrompt(args map[string]string) string {
	course := argOr(args, "course", "(курс не указан)")
	topic := argOr(args, "topic", "(тема не указана — возьми последнюю лекцию курса)")
	cmid := argOr(args, "cmid", "(не задан)")
	return fmt.Sprintf(`Сделай конспект лекции по курсу %q, тема: %s. Модуль cmid: %s.

Работай только через MCP-инструменты Moodle и локальные конспекты, только на чтение. Не запускай тесты и не отправляй ответы.

Порядок действий:
1. moodle_status — убедись, что сессия рабочая. Если нет, сообщи об этом и не подставляй данные из памяти.
2. moodle_courses — найди курс %q; при неоднозначности покажи варианты и уточни.
3. moodle_course_outline — определи раздел и модули по теме %s.
4. Прочитай источники: moodle_material_read для страниц и книг, moodle_file_get с extract_text=true для PDF и презентаций. Если есть несколько материалов, начинай с конспекта лекции и презентации, затем переходи к дополнительным.
5. moodle_notes — проверь локальные записи: action=list с discipline курса, затем action=read с source=auto. Если есть transcript или slides, используй их для уточнения формулировок и примеров.
6. Сверь факты между источниками. Расхождения помечай явно, не усредняй.

Структура конспекта:
- заголовок: курс, тема, номер и дата лекции, если известны;
- 3–5 главных тезисов;
- определения и обозначения;
- формулы, схемы или алгоритмы — с пояснением, что означает каждый элемент;
- примеры из материалов;
- связь с предыдущими темами;
- вопросы для самопроверки (5–7);
- источники: название модуля или файла.

Критерий готового результата: конспект можно прочитать без открытия Moodle; все утверждения опираются на прочитанные материалы; неподтверждённое помечено как предположение; объём — по существу, без воды. Если материалов нет, верни пустой каркас и список того, что отсутствует, а не выдумывай содержание.`, course, topic, cmid, course, topic)
}

// examReviewPrompt — план повторения с приоритетами по срокам.
func examReviewPrompt(args map[string]string) string {
	course := argOr(args, "course", "(курс не указан)")
	topic := argOr(args, "topic", "(весь курс)")
	return fmt.Sprintf(`Составь план подготовки к зачёту или экзамену по курсу %q. Раздел: %s.

Работай только через MCP-инструменты Moodle и локальные конспекты, только на чтение. Не открывай тесты, не вызывай startattempt/submit и не решай задания.

Порядок действий:
1. moodle_status — проверь сессию; при ошибке остановись и покажи предупреждения.
2. moodle_courses — найди курс %q. Если курсов несколько, перечисли их и уточни.
3. moodle_deadlines — собери все сроки: тесты, задания, пересдачи. Это основа приоритетов.
4. moodle_course_outline — выпиши разделы и модули курса; отметь, какие относятся к разделу %s.
5. Для каждого раздела прочитай материалы: moodle_material_read, затем moodle_file_get с extract_text=true. Сначала — обзорные страницы и презентации, потом детали.
6. moodle_notes — action=list по дисциплине, затем action=read для существующих конспектов. Используй их как готовые выжимки, но проверяй по материалам Moodle.

Итог:
- таблица «раздел — материалы — срок — приоритет»;
- план по дням до ближайшего срока, с конкретными материалами на каждый день;
- список тем с высокой вероятностью вопросов (на основе структуры и заданий);
- 10–15 вопросов для самопроверки с краткими ответами;
- чек-лист «готов / не готов» по каждому разделу;
- блок «чего не хватает»: закрытые материалы, пустые разделы, отсутствующие конспекты.

Критерий готового результата: план выполним по времени, каждый пункт привязан к материалу или сроку из Moodle, приоритеты объяснены, пробелы перечислены честно. Не выдумывай вопросы экзамена и не обещай оценку. Секреты и куки не выводи.`, course, topic, course, topic)
}

// solveQuizPrompt — порядок прохождения теста и ограничения записи.
func solveQuizPrompt(args map[string]string) string {
	cmid := argOr(args, "cmid", "(cmid не указан)")
	course := argOr(args, "course", "(курс не указан — найди по cmid через структуру)")
	timeLimit := argOr(args, "time_limit", "(не задан — ориентируйся на time_left теста)")
	return fmt.Sprintf(`Пройди тест Moodle cmid=%s. Курс: %q. Лимит времени: %s.

Запись в Moodle по умолчанию выключена. Инструменты moodle_quiz_start, moodle_quiz_submit и moodle_quiz_finish работают только при MOODLE_MCP_ALLOW_WRITES=1 и требуют confirm=true. Не начинай попытку и не завершай её без явного подтверждения пользователя: сначала покажи правила теста и спроси разрешение.

Порядок действий:
1. moodle_status — проверь сессию. Если authenticated=false, остановись и покажи warnings.
2. moodle_quiz_info — выпиши правила, число и метод оценивания, can_start, незавершённую попытку и attempts_left. Если can_start=false и незавершённой попытки нет, остановись и объясни причину.
3. Подготовься по материалам: moodle_course_outline и moodle_material_read для страниц и книг, moodle_file_get с extract_text=true для PDF и презентаций, moodle_search для терминов, moodle_notes для готовых конспектов. Сначала прочитай материал, потом отвечай.
4. Спроси у пользователя разрешение на старт. Только после явного «да» вызови moodle_quiz_start с confirm=true.
5. Для каждой страницы: moodle_quiz_page, затем разбери вопросы. Если в условии есть рисунок, используй moodle_quiz_image (slot и index с 0).
6. Отвечай строго по типу вопроса:
   - multichoice с чекбоксами — choices (значения, name или тексты вариантов);
   - multichoice с radio и truefalse — choice;
   - shortanswer и numerical — text;
   - match — matches (ключ — имя select или текст строки, значение — вариант).
   Не отправляй ответ на слот, которого нет на странице.
7. После заполнения страницы вызови moodle_quiz_submit с теми же attempt_id и page, answers и confirm=true. Для adaptive-вопроса с кнопкой «Проверить» используй check_slot, не переходя дальше.
8. Если инструмент вернул unsupported_question (essay, drag&drop, multianswer и другие неизвестные типы), не выдумывай ответ: остановись и верни пользователю слот, тип и что именно не поддерживается.
9. Когда next_page < 0 или needs_finish=true, покажи сводку отвеченного и спроси разрешение на завершение. Только после явного «да» вызови moodle_quiz_finish с confirm=true.
10. moodle_quiz_review — разбери результат: итог, вопросы, правильные ответы и ошибки. Если review_closed, скажи, что просмотр закрыт настройками теста, и не подставляй ответы из памяти.

Ограничения:
- Не начинай попытку автоматически из сроков, курсов или дедлайнов.
- Не отправляй страницу, пока не уверен в ответах; частично заполненная страница допустима, но об этом нужно сказать.
- Следи за time_left. Если времени меньше, чем нужно на оставшиеся вопросы, остановись, сохрани уже отвеченное и предупреди пользователя; не завершай тест в спешке.
- Не выводи пароль, куки, sesskey и полные URL с токенами.

Критерий готового результата: каждый ответ опирается на прочитанный материал или явное рассуждение; неизвестные типы честно помечены; оценка и разбор взяты из Moodle, а не выдуманы; все write-вызовы сделаны только после подтверждения.`, cmid, course, timeLimit)
}
