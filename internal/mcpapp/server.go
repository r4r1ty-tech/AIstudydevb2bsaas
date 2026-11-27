package mcpapp

import (
	"log/slog"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Version — версия MCP-сервера, которую видит клиент.
const Version = "0.1.0"

// NewServer собирает MCP-сервер: статус, курсы, структура курса, материалы,
// поиск, сроки, файлы и локальные конспекты.
// Логи SDK идут в stderr, чтобы не засорять stdout с JSON-RPC.
func NewServer(sess *Session) *mcp.Server {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	s := mcp.NewServer(&mcp.Implementation{
		Name:        "ssau-mcp",
		Title:       "SSAU Moodle MCP",
		Description: "Чтение Moodle do.ssau.ru и тесты по явному confirm: сессия, курсы, материалы, сроки, файлы, конспекты и попытки",
		Version:     Version,
	}, &mcp.ServerOptions{
		Instructions: "Сервер по умолчанию только читает Moodle. Начните с moodle_status и moodle_courses, затем moodle_deadlines для сроков, moodle_course_outline и moodle_material_read для материалов, moodle_file_get для файлов, moodle_notes для локальных конспектов. Тесты: moodle_quiz_info и moodle_quiz_review читают, moodle_quiz_start/submit/finish меняют Moodle только при MOODLE_MCP_ALLOW_WRITES=1 и confirm=true. Готовые сценарии — промпты moodle_prepare_lecture, moodle_make_notes, moodle_exam_review и moodle_solve_quiz.",
		Logger:       logger,
	})
	registerTools(s, sess)
	registerResource(s, sess)
	registerMaterialTools(s, sess)
	registerDeadlineTools(s, sess)
	registerFileTools(s, sess)
	registerNoteTools(s, sess)
	registerNoteResources(s, sess)
	registerQuizTools(s, sess)
	registerQuizResources(s, sess)
	registerPrompts(s)
	return s
}
