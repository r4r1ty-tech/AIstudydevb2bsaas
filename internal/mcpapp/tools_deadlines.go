package mcpapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodle"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodlematerial"
)

const (
	// DeadlineResourceURI — JSON-ресурс с ближайшими сроками.
	DeadlineResourceURI = "moodle://deadlines"

	// defaultDeadlineDays — окно по умолчанию: две недели вперёд.
	defaultDeadlineDays = 14
	// maxDeadlineDays — потолок окна, чтобы одна лента не тянула весь семестр.
	maxDeadlineDays = 365
	// deadlineTodoMaxRunes — сколько текста «что сделать» отдаём на один элемент.
	deadlineTodoMaxRunes = 400
	// deadlineTimeLayout — человекочитаемый формат времени в Europe/Samara.
	deadlineTimeLayout = "02.01.2006 15:04"
	// deadlineTimezone — часовой пояс, в котором Moodle показывает сроки студенту.
	deadlineTimezone = "Europe/Samara"
)

// DeadlinesInput — аргументы moodle_deadlines.
type DeadlinesInput struct {
	DaysAhead      int   `json:"days_ahead,omitempty" jsonschema:"окно в днях: вперёд и, при include_overdue, назад; по умолчанию 14"`
	IncludeOverdue *bool `json:"include_overdue,omitempty" jsonschema:"показывать просроченные сроки; по умолчанию true"`
	CourseID       int64 `json:"course_id,omitempty" jsonschema:"ограничить одним курсом из moodle_courses"`
	Refresh        bool  `json:"refresh,omitempty" jsonschema:"игнорировать кэш и перечитать ленту сроков"`
}

// DeadlineItem — один срок: задание, тест, урок или семинар.
type DeadlineItem struct {
	CMID     int64  `json:"cmid"`
	CourseID int64  `json:"course_id"`
	Course   string `json:"course"`
	Kind     string `json:"kind"`
	Title    string `json:"title"`
	Due      string `json:"due,omitempty"`
	Opens    string `json:"opens,omitempty"`
	Overdue  bool   `json:"overdue"`
	Status   string `json:"status"`
	Todo     string `json:"todo,omitempty"`
	URL      string `json:"url"`
}

// DeadlinesOutput — ответ moodle_deadlines, отсортированный по сроку.
type DeadlinesOutput struct {
	DaysAhead      int            `json:"days_ahead"`
	IncludeOverdue bool           `json:"include_overdue"`
	Timezone       string         `json:"timezone"`
	Now            string         `json:"now"`
	Total          int            `json:"total"`
	OverdueTotal   int            `json:"overdue_total"`
	Items          []DeadlineItem `json:"items"`
	Truncated      bool           `json:"truncated,omitempty"`
}

// samaraLocation — Europe/Samara; при отсутствии tzdata фиксированный UTC+4.
var samaraLocation = func() *time.Location {
	if loc, err := time.LoadLocation(deadlineTimezone); err == nil {
		return loc
	}
	return time.FixedZone("Samara", 4*60*60)
}()

// registerDeadlineTools добавляет moodle_deadlines и ресурс moodle://deadlines.
// Инструмент только читает страницы элементов: попытки тестов не запускает.
func registerDeadlineTools(s *mcp.Server, sess *Session) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "moodle_deadlines",
		Title:       "Сроки Moodle",
		Description: "Возвращает ближайшие сроки Moodle (задания, тесты, уроки, семинары) по ленте событий: cmid, курс, тип, название, срок, открытие, просрочка, статус, что сделать и ссылку. Время в Europe/Samara. Для тестов отдаёт только метаданные, сроки и попытки — попытки не начинает.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in DeadlinesInput) (*mcp.CallToolResult, DeadlinesOutput, error) {
		out, err := readDeadlines(ctx, sess, in)
		if err != nil {
			return nil, DeadlinesOutput{}, err
		}
		return nil, out, nil
	})

	registerDeadlineResource(s, sess)
}

// registerDeadlineResource отдаёт moodle://deadlines как JSON того же ответа.
func registerDeadlineResource(s *mcp.Server, sess *Session) {
	s.AddResource(&mcp.Resource{
		URI:         DeadlineResourceURI,
		Name:        "deadlines",
		Title:       "Сроки Moodle",
		Description: "Ближайшие сроки Moodle на 14 дней в JSON: cmid, course_id, course, kind, title, due, opens, overdue, status, todo, url.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		out, err := readDeadlines(ctx, sess, DeadlinesInput{})
		if err != nil {
			return nil, err
		}
		uri := DeadlineResourceURI
		if req != nil && req.Params != nil && req.Params.URI != "" {
			uri = req.Params.URI
		}
		raw, err := json.Marshal(out)
		if err != nil {
			return nil, err
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

// readDeadlines отдаёт сроки из кэша (TTL 1m) или перечитывает ленту.
// refresh=true обходит кэш, но всё равно обновляет его.
func readDeadlines(ctx context.Context, sess *Session, in DeadlinesInput) (DeadlinesOutput, error) {
	days := in.DaysAhead
	if days <= 0 {
		days = defaultDeadlineDays
	}
	if days > maxDeadlineDays {
		days = maxDeadlineDays
	}
	includeOverdue := true
	if in.IncludeOverdue != nil {
		includeOverdue = *in.IncludeOverdue
	}
	courseID := in.CourseID
	if courseID < 0 {
		courseID = 0
	}
	key := fmt.Sprintf("days=%d;overdue=%t;course=%d", days, includeOverdue, courseID)
	if !in.Refresh {
		if out, ok := sess.caches.deadlines.Get(key); ok {
			out.Now = time.Now().In(samaraLocation).Format(deadlineTimeLayout)
			return out, nil
		}
	}
	var out DeadlinesOutput
	err := sess.WithClient(ctx, func(ctx context.Context, c *moodle.Client) error {
		res, cerr := collectDeadlines(ctx, sess, c, courseID, days, includeOverdue)
		if cerr != nil {
			return cerr
		}
		out = res
		return nil
	})
	if err != nil {
		return DeadlinesOutput{}, deadlineToolError(err)
	}
	sess.caches.deadlines.Set(key, out)
	return out, nil
}

// collectDeadlines читает ленту событий и уточняет каждый элемент страницей
// модуля. Страницы открываются параллельно, но не более чем позволяет Limiter.
func collectDeadlines(ctx context.Context, sess *Session, c *moodle.Client, courseID int64, days int, includeOverdue bool) (DeadlinesOutput, error) {
	loc := samaraLocation
	now := time.Now().In(loc)
	from := now
	if includeOverdue {
		from = now.AddDate(0, 0, -days)
	}
	to := now.AddDate(0, 0, days)
	events, err := c.Timeline(ctx, from, to)
	if err != nil {
		return DeadlinesOutput{}, err
	}

	acts := make([]moodle.Activity, 0, len(events))
	eventDue := make(map[int64]*time.Time, len(events))
	seen := make(map[int64]bool, len(events))
	for _, e := range events {
		cmid := e.CMID()
		if cmid <= 0 || seen[cmid] {
			continue
		}
		if courseID > 0 && e.Course.ID != courseID {
			continue
		}
		seen[cmid] = true
		title := strings.TrimSpace(strings.TrimSuffix(e.Name, " закрывается"))
		acts = append(acts, moodle.Activity{
			CMID:     cmid,
			CourseID: e.Course.ID,
			Course:   e.Course.FullName,
			Kind:     strings.ToLower(strings.TrimSpace(e.ModuleName)),
			Name:     title,
			URL:      e.URL,
		})
		if e.TimeSort > 0 {
			t := time.Unix(e.TimeSort, 0).In(loc)
			eventDue[cmid] = &t
		}
	}

	type outcome struct {
		activity moodle.Activity
		detail   moodle.Detail
		err      error
	}
	outcomes := make([]outcome, len(acts))
	var wg sync.WaitGroup
	for i := range acts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			a := acts[i]
			if aerr := sess.limit.Acquire(ctx); aerr != nil {
				outcomes[i] = outcome{activity: a, err: aerr}
				return
			}
			defer sess.limit.Release()
			d, derr := c.ActivityDetail(ctx, a, loc)
			outcomes[i] = outcome{activity: a, detail: d, err: derr}
		}(i)
	}
	wg.Wait()

	type row struct {
		item DeadlineItem
		due  *time.Time
	}
	rows := make([]row, 0, len(outcomes))
	for _, o := range outcomes {
		if o.err != nil {
			// Протухшая сессия и отмена контекста — фатальные: Session.do
			// должен перелогиниться и повторить всю ленту.
			if errors.Is(o.err, moodle.ErrUnauthorized) || errors.Is(o.err, context.Canceled) ||
				errors.Is(o.err, context.DeadlineExceeded) {
				return DeadlinesOutput{}, o.err
			}
			logx.Debugf("mcpapp", "сроки: элемент cmid=%d не разобран: %v", o.activity.CMID, o.err)
			if eventDue[o.activity.CMID] == nil {
				continue // ни срока, ни разбора — показывать нечего
			}
		}
		due := o.detail.Due
		if due == nil {
			due = eventDue[o.activity.CMID]
		}
		overdue := due != nil && due.Before(now) && !deadlineFinished(o.detail.Status)
		if overdue && !includeOverdue {
			continue
		}
		item := DeadlineItem{
			CMID:     o.activity.CMID,
			CourseID: o.activity.CourseID,
			Course:   o.activity.Course,
			Kind:     o.activity.Kind,
			Title:    o.activity.Name,
			Due:      formatDeadline(due),
			Opens:    formatDeadline(o.detail.Opens),
			Overdue:  overdue,
			Status:   o.detail.Status,
			Todo:     deadlineTodo(o.detail.Todo),
			URL:      absoluteMoodleURL(sess.cfg.BaseURL, o.activity.URL),
		}
		rows = append(rows, row{item: item, due: due})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		switch {
		case a.due != nil && b.due != nil && !a.due.Equal(*b.due):
			return a.due.Before(*b.due)
		case a.due == nil && b.due != nil:
			return false
		case a.due != nil && b.due == nil:
			return true
		}
		if a.item.Course != b.item.Course {
			return a.item.Course < b.item.Course
		}
		return a.item.CMID < b.item.CMID
	})

	out := DeadlinesOutput{
		DaysAhead:      days,
		IncludeOverdue: includeOverdue,
		Timezone:       deadlineTimezone,
		Now:            now.Format(deadlineTimeLayout),
		Items:          make([]DeadlineItem, 0, len(rows)),
	}
	used := 0
	for _, r := range rows {
		cost := moodlematerial.RuneLen(r.item.Title) + moodlematerial.RuneLen(r.item.Todo) + 64
		if used+cost > maxToolOutputRunes {
			out.Truncated = true
			break
		}
		used += cost
		out.Items = append(out.Items, r.item)
		if r.item.Overdue {
			out.OverdueTotal++
		}
	}
	out.Total = len(out.Items)
	return out, nil
}

// deadlineFinished — статус, при котором срок уже не важен.
func deadlineFinished(status string) bool {
	return status == model.MoodleSubmitted || status == model.MoodleDone
}

// formatDeadline — время в Europe/Samara; пустая строка для отсутствующего срока.
func formatDeadline(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.In(samaraLocation).Format(deadlineTimeLayout)
}

// deadlineTodo склеивает условия выполнения и обрезает до разумной длины.
func deadlineTodo(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	text, _ := TruncateText(strings.Join(lines, "\n"), deadlineTodoMaxRunes)
	return text
}

// absoluteMoodleURL превращает href со страницы Moodle в абсолютный адрес.
// Ссылка «/…» считается от корня сайта, «…» — от пути Moodle.
func absoluteMoodleURL(base, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err == nil && u.IsAbs() {
		return u.String()
	}
	b, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || b.Scheme == "" || b.Host == "" {
		return raw
	}
	origin := b.Scheme + "://" + b.Host
	if strings.HasPrefix(raw, "/") {
		return origin + raw
	}
	return strings.TrimRight(base, "/") + "/" + raw
}

// deadlineToolError добавляет к ошибке код, чтобы LLM мог исправить запрос.
func deadlineToolError(err error) error {
	code := "moodle_error"
	switch {
	case errors.Is(err, ErrNoCredentials):
		code = "auth_required"
	case errors.Is(err, moodle.ErrUnauthorized):
		code = "auth_expired"
	case errors.Is(err, context.DeadlineExceeded):
		code = "timeout"
	}
	return toolFailure(code, "проверьте сессию (moodle_status) и повторите", err)
}
