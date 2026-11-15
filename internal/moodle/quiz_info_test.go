package moodle

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// inspectFixture отдаёт view.php с заданным HTML (BASE заменяется адресом сервера).
func inspectFixture(t *testing.T, html string) *QuizInfo {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(strings.ReplaceAll(html, "BASE", srv.URL)))
	}))
	defer srv.Close()
	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	info, err := c.InspectQuiz(context.Background(), 61422)
	if err != nil {
		t.Fatalf("InspectQuiz: %v", err)
	}
	return info
}

func TestInspectQuizAttemptsAndMethod(t *testing.T) {
	info := inspectFixture(t, sampleQuizViewHTML)
	if info.Allowed != 2 || info.Used != 1 || info.GradingMethod != "Высшая оценка" {
		t.Fatalf("allowed=%d used=%d method=%q", info.Allowed, info.Used, info.GradingMethod)
	}
	if got := info.AttemptsLeft(); got != 1 {
		t.Errorf("AttemptsLeft = %d, ожидали 1", got)
	}
}

func TestInspectQuizUnlimitedAttempts(t *testing.T) {
	html := strings.Replace(sampleQuizViewHTML, "Разрешено попыток: 2", "Разрешено попыток: Без ограничений", 1)
	info := inspectFixture(t, html)
	if info.Allowed != 0 {
		t.Fatalf("Allowed = %d, ожидали 0 (без ограничений)", info.Allowed)
	}
	if got := info.AttemptsLeft(); got != -1 {
		t.Errorf("AttemptsLeft = %d, ожидали -1", got)
	}
}

func TestInspectQuizAttemptsExhaustedAndInProgress(t *testing.T) {
	// Разрешена 1 попытка, она завершена: левых попыток нет.
	html := strings.Replace(sampleQuizViewHTML, "Разрешено попыток: 2", "Разрешено попыток: 1", 1)
	info := inspectFixture(t, html)
	if info.Used != 1 || info.AttemptsLeft() != 0 {
		t.Fatalf("used=%d left=%d, ожидали used=1 left=0", info.Used, info.AttemptsLeft())
	}
	// Незавершённая попытка в таблице не считается использованной.
	inProgress := strings.Replace(sampleQuizViewHTML,
		`<td class="cell c1 state">Завершены</td>`, `<td class="cell c1 state">В процессе</td>`, 1)
	info = inspectFixture(t, inProgress)
	if info.Used != 0 {
		t.Errorf("Used = %d при незавершённой попытке, ожидали 0", info.Used)
	}
}

func TestInspectQuizLastAttemptMethod(t *testing.T) {
	html := strings.Replace(sampleQuizViewHTML, "Метод оценивания: Высшая оценка", "Метод оценивания: Последняя попытка", 1)
	info := inspectFixture(t, html)
	if info.GradingMethod != "Последняя попытка" {
		t.Errorf("GradingMethod = %q", info.GradingMethod)
	}
}
