package mcpapp

import (
	"context"
	"encoding/json"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultCoursesLimit = 20
	maxCoursesLimit     = 100

	// CoursesResourceURI — JSON-ресурс со списком курсов.
	CoursesResourceURI = "moodle://courses"
)

type statusInput struct{}

// StatusOutput — ответ moodle_status. Секретов (пароль, куки, sesskey) здесь нет.
type StatusOutput struct {
	Authenticated bool     `json:"authenticated"`
	Login         string   `json:"login,omitempty"`
	BaseURL       string   `json:"base_url"`
	LastRefresh   string   `json:"last_refresh,omitempty"`
	Warnings      []string `json:"warnings,omitempty"`
}

// CoursesInput — аргументы moodle_courses.
type CoursesInput struct {
	Limit int `json:"limit,omitempty" jsonschema:"Сколько курсов вернуть: по умолчанию 20, максимум 100"`
}

// CoursesOutput — ответ moodle_courses: компактный список и общее число курсов.
type CoursesOutput struct {
	Courses []Course `json:"courses"`
	Total   int      `json:"total"`
}

func registerTools(s *mcp.Server, sess *Session) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "moodle_status",
		Title:       "Статус Moodle",
		Description: "Проверяет сессию Moodle do.ssau.ru: авторизован ли пользователь, под каким логином, когда сессия обновлялась и какие есть предупреждения. Пароль и куки не возвращает.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ statusInput) (*mcp.CallToolResult, StatusOutput, error) {
		st := sess.State(ctx)
		out := StatusOutput{
			Authenticated: st.Authenticated,
			Login:         st.Login,
			BaseURL:       st.BaseURL,
			Warnings:      st.Warnings,
		}
		if !st.LastRefresh.IsZero() {
			out.LastRefresh = st.LastRefresh.Format(time.RFC3339)
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "moodle_courses",
		Title:       "Курсы Moodle",
		Description: "Возвращает курсы студента из Moodle do.ssau.ru: id, название, короткое имя, признак скрытости и ссылку. Только чтение.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in CoursesInput) (*mcp.CallToolResult, CoursesOutput, error) {
		limit := coursesLimit(in.Limit)
		all, err := sess.Courses(ctx)
		if err != nil {
			return nil, CoursesOutput{}, err
		}
		out := CoursesOutput{Total: len(all), Courses: make([]Course, 0, min(limit, len(all)))}
		out.Courses = append(out.Courses, all[:min(limit, len(all))]...)
		return nil, out, nil
	})
}

// coursesLimit приводит запрошенный лимит к диапазону: <=0 — 20, >100 — 100.
func coursesLimit(n int) int {
	if n <= 0 {
		return defaultCoursesLimit
	}
	if n > maxCoursesLimit {
		return maxCoursesLimit
	}
	return n
}

// registerResource отдаёт курсы как MCP-ресурс moodle://courses (JSON).
func registerResource(s *mcp.Server, sess *Session) {
	s.AddResource(&mcp.Resource{
		URI:         CoursesResourceURI,
		Name:        "courses",
		Title:       "Курсы Moodle",
		Description: "Список курсов студента из Moodle do.ssau.ru в JSON: id, name, shortname, hidden, url.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		all, err := sess.Courses(ctx)
		if err != nil {
			return nil, err
		}
		out := CoursesOutput{Total: len(all), Courses: all}
		raw, err := json.Marshal(out)
		if err != nil {
			return nil, err
		}
		uri := CoursesResourceURI
		if req != nil && req.Params != nil && req.Params.URI != "" {
			uri = req.Params.URI
		}
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{
				URI:      uri,
				MIMEType: "application/json",
				Text:     string(raw),
			}},
		}, nil
	})
}
