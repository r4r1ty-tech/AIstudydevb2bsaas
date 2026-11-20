// Package quizlog — журнал авторешения тестов Moodle для отладки в панели
// админа: какая модель отвечала, сколько вариантов перебрали, что пришло в
// ответ, какие ответы ушли в Moodle и что поставили. Один прогон — один JSON
// в <папка базы>/quiz-debug (rasp пишет, panel читает). Хранятся последние Keep.
package quizlog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/llm"
)

const (
	Keep   = 200
	MaxRaw = 20000
)

// Статусы прогона.
const (
	Solved = "solved" // решён и завершён
	Failed = "failed" // ИИ не ответил или ответ не разобрали: попытка не завершена
	Parsed = "parsed" // ключей ИИ нет: прислали текст вопросов
	// Partial — попытка завершена, оценка неполная: решена не полностью (повтор или «Пройти»).
	Partial = "partial"
)

// Search — один веб-запрос решателя.
type Search struct {
	Slot     int    `json:"slot"`
	Query    string `json:"query"`
	Provider string `json:"provider,omitempty"`
	Hits     int    `json:"hits"`
	Error    string `json:"error,omitempty"`
	Refine   bool   `json:"refine,omitempty"` // второй проход: запрос придумала модель
	Exact    bool   `json:"exact,omitempty"`  // дословная фраза вопроса в кавычках
}

type Page struct {
	Page     int            `json:"page"`
	Attempts []llm.Attempt  `json:"attempts"`
	Used     string         `json:"used,omitempty"`
	Raw      string         `json:"raw,omitempty"`
	Answers  map[int]string `json:"answers,omitempty"`
	Error    string         `json:"error,omitempty"`
	Search   []Search       `json:"search,omitempty"`
	Refined  []int          `json:"refined,omitempty"` // слоты, ответ на которые поменялся в раундах уточнения
	Notes    []string       `json:"notes,omitempty"`   // рисунки условия: распознаны или нет
	// FromBase — слоты, ответы на которые взяты из базы проверенных ответов (без ИИ).
	FromBase []int `json:"from_base,omitempty"`
	// Confidence — уверенность модели по слотам (0..1), если модель её сообщила.
	Confidence map[int]float64 `json:"confidence,omitempty"`
}

type Run struct {
	Name       string    `json:"name"`
	At         time.Time `json:"at"`
	TelegramID int64     `json:"telegram_id"`
	CMID       int64     `json:"cmid"`
	Course     string    `json:"course"`
	Title      string    `json:"title"`
	Status     string    `json:"status"`
	Chain      []string  `json:"chain"`
	Pages      []Page    `json:"pages,omitempty"`
	Grade      string    `json:"grade,omitempty"`
	Feedback   string    `json:"feedback,omitempty"`
	Error      string    `json:"error,omitempty"`
	TookMS     int64     `json:"took_ms"`
}

// Dir — папка журнала рядом с базой.
func Dir(dbPath string) string { return filepath.Join(filepath.Dir(dbPath), "quiz-debug") }

func clip(s string) string {
	if len(s) <= MaxRaw {
		return s
	}
	return s[:MaxRaw] + "\n…(обрезано)"
}

// Write сохраняет прогон и чистит старые.
func Write(dir string, r *Run) error {
	if dir == "" || r == nil {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if r.At.IsZero() {
		r.At = time.Now()
	}
	r.Name = fmt.Sprintf("%d-%d-%d.json", r.At.UnixMilli(), r.TelegramID, r.CMID)
	for i := range r.Pages {
		r.Pages[i].Raw = clip(r.Pages[i].Raw)
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, r.Name), b, 0o600); err != nil {
		return err
	}
	names, _ := names(dir)
	for len(names) > Keep {
		_ = os.Remove(filepath.Join(dir, names[len(names)-1]))
		names = names[:len(names)-1]
	}
	return nil
}

// names — файлы журнала, новые первыми.
func names(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			out = append(out, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out, nil
}

// List — последние n прогонов без сырых ответов.
func List(dir string, n int) ([]Run, error) {
	ns, err := names(dir)
	if os.IsNotExist(err) {
		return []Run{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Run{}
	for _, name := range ns {
		if len(out) >= n {
			break
		}
		r, err := Read(dir, name)
		if err != nil {
			continue
		}
		for i := range r.Pages {
			r.Pages[i].Raw = ""
		}
		out = append(out, *r)
	}
	return out, nil
}

// Read — один прогон целиком. Имя проверяется: только файл из папки.
func Read(dir, name string) (*Run, error) {
	if name == "" || name != filepath.Base(name) || !strings.HasSuffix(name, ".json") {
		return nil, fmt.Errorf("плохое имя %q", name)
	}
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return nil, err
	}
	var r Run
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}
