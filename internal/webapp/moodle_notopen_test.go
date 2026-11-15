package webapp

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/uni"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/vault"
)

// Классификация «ещё не открыт»: по Opens в будущем и по тексту страницы.
func TestNotOpenClassification(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { x := now.Add(d); return &x }
	const hour = time.Hour
	const note = "Тест сейчас не открыт для попыток"
	cases := []struct {
		name        string
		item        model.MoodleItem
		wantNotOpen bool
		wantClosed  bool
		wantGroup   string
	}{
		{"откроется позже, срок через 25 мин", model.MoodleItem{CMID: 1, Kind: model.MoodleQuiz, Title: "Т", Status: model.MoodleTodo, Opens: at(2 * hour), Due: at(2*hour + 25*time.Minute)}, true, false, dueNotOpen},
		{"откроется позже, срока нет", model.MoodleItem{CMID: 2, Kind: model.MoodleQuiz, Title: "Т", Status: model.MoodleTodo, Opens: at(hour)}, true, false, dueNotOpen},
		{"пометка без Opens: закрыт (часто исчерпаны попытки)", model.MoodleItem{CMID: 3, Kind: model.MoodleQuiz, Title: "Т", Status: model.MoodleTodo, Todo: note}, false, true, dueClosed},
		{"пометка есть, но Opens уже прошёл: закрыт", model.MoodleItem{CMID: 4, Kind: model.MoodleQuiz, Title: "Т", Status: model.MoodleTodo, Opens: at(-hour), Todo: note}, false, true, dueClosed},
		{"пометка есть, срок прошёл: закрыт", model.MoodleItem{CMID: 5, Kind: model.MoodleQuiz, Title: "Т", Status: model.MoodleTodo, Due: at(-hour), Todo: note}, false, true, dueClosed},
		{"открыт, срок впереди", model.MoodleItem{CMID: 6, Kind: model.MoodleQuiz, Title: "Т", Status: model.MoodleTodo, Opens: at(-hour), Due: at(3 * hour)}, false, false, dueSoon},
		{"открыт, срока нет", model.MoodleItem{CMID: 7, Kind: model.MoodleQuiz, Title: "Т", Status: model.MoodleTodo}, false, false, dueNone},
		{"сдан, Opens в будущем", model.MoodleItem{CMID: 8, Kind: model.MoodleQuiz, Title: "Т", Status: model.MoodleSubmitted, Opens: at(hour)}, false, false, dueDone},
		{"задание с открытием в будущем", model.MoodleItem{CMID: 9, Kind: model.MoodleAssign, Title: "З", Status: model.MoodleTodo, Opens: at(hour), Due: at(5 * 24 * hour)}, true, false, dueNotOpen},
		{"задание с пометкой без Opens: не открыт не считается", model.MoodleItem{CMID: 10, Kind: model.MoodleAssign, Title: "З", Status: model.MoodleTodo, Todo: note}, false, false, dueNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := deadlineFromItem(c.item, now)
			if d.NotOpen != c.wantNotOpen {
				t.Errorf("NotOpen=%v want %v", d.NotOpen, c.wantNotOpen)
			}
			if d.Closed != c.wantClosed {
				t.Errorf("Closed=%v want %v", d.Closed, c.wantClosed)
			}
			if g := dueGroup(d, now); g != c.wantGroup {
				t.Errorf("group=%q want %q", g, c.wantGroup)
			}
			if c.item.Opens != nil && !d.Opens.Equal(*c.item.Opens) {
				t.Errorf("Opens=%v want %v", d.Opens, *c.item.Opens)
			}
		})
	}
}

// newMoodleServer — панель с элементами Moodle в базе (как у владельца).
func newMoodleServer(t *testing.T, items []model.MoodleItem) (*Server, *store.Store) {
	t.Helper()
	s, st := newServer(t, nil)
	v, err := vault.Load("", filepath.Join(t.TempDir(), "k"))
	if err != nil {
		t.Fatal(err)
	}
	s.moodle = &storeMoodle{st: st, v: v, owner: testAdminID}
	if err := st.PutAccount(model.Account{TelegramID: testAdminID, Kind: model.AccountMoodle, Login: "s", Status: model.AccountOK}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceMoodleItems(testAdminID, items); err != nil {
		t.Fatal(err)
	}
	return s, st
}

func notOpenFixture(now time.Time) []model.MoodleItem {
	at := func(d time.Duration) *time.Time { x := now.Add(d); return &x }
	return []model.MoodleItem{
		{CMID: 101, Kind: model.MoodleQuiz, Title: "Ещё не открыт", Course: "К", Status: model.MoodleTodo,
			Opens: at(2 * time.Hour), Due: at(2*time.Hour + 25*time.Minute)},
		{CMID: 102, Kind: model.MoodleQuiz, Title: "Открыт", Course: "К", Status: model.MoodleTodo,
			Opens: at(-time.Hour), Due: at(3 * time.Hour)},
		{CMID: 103, Kind: model.MoodleQuiz, Title: "Сдан", Course: "К", Status: model.MoodleSubmitted,
			Opens: at(2 * time.Hour), Due: at(2*time.Hour + 25*time.Minute)},
	}
}

// API: поля not_open/opens и группа «notopen» в /api/deadlines; открытый тест не задет.
func TestNotOpenDeadlinesAPI(t *testing.T) {
	now := time.Now()
	items := notOpenFixture(now)
	s, _ := newMoodleServer(t, items)

	var resp deadlinesResponse
	decodeBody(t, call(t, s, http.MethodGet, "/api/deadlines", ""), &resp)
	byID := map[string]deadlineView{}
	for _, d := range resp.Deadlines {
		byID[d.ID] = d
	}
	nf, ok := byID["101"]
	if !ok {
		t.Fatalf("not-open quiz is hidden: %+v", resp.Deadlines)
	}
	if !nf.NotOpen || nf.Group != dueNotOpen || nf.Opens.IsZero() || nf.Opens.Unix() != items[0].Opens.Unix() {
		t.Errorf("not-open quiz: %+v", nf)
	}
	if o := byID["102"]; o.NotOpen || o.Group == dueNotOpen {
		t.Errorf("open quiz must not be not-open: %+v", o)
	}
	if d := byID["103"]; d.NotOpen || d.Group != dueDone {
		t.Errorf("submitted quiz: %+v", d)
	}
	if resp.Counts[dueNotOpen] != 1 {
		t.Errorf("counts %+v", resp.Counts)
	}
	if first := resp.Deadlines[0]; first.Group != dueNotOpen {
		t.Errorf("not-open must be sorted by opening time first: %+v", first)
	}
}

// «Пройти все» не ставит ещё не открытый тест в очередь; открытый ставит.
func TestNotOpenExcludedFromPassAll(t *testing.T) {
	now := time.Now()
	s, st := newMoodleServer(t, notOpenFixture(now))

	var out map[string]any
	decodeBody(t, call(t, s, http.MethodPost, "/api/quiz/force-all", ""), &out)
	if n, _ := out["count"].(float64); n != 1 {
		t.Fatalf("force-all count=%v want 1", out["count"])
	}
	assertForced(t, st, 102, true)
	assertForced(t, st, 101, false)
}

// «Пройти» для одного теста: ещё не открытый отклоняем, открытый запускаем.
func TestNotOpenQuizForceRejected(t *testing.T) {
	now := time.Now()
	s, st := newMoodleServer(t, notOpenFixture(now))

	if rec := call(t, s, http.MethodPost, "/api/quiz/force", `{"cmid":101}`); rec.Code != http.StatusConflict {
		t.Fatalf("not-open force: %d %s", rec.Code, rec.Body)
	}
	assertForced(t, st, 101, false)
	if rec := call(t, s, http.MethodPost, "/api/quiz/force", `{"cmid":102}`); rec.Code != http.StatusOK {
		t.Fatalf("open force: %d %s", rec.Code, rec.Body)
	}
	assertForced(t, st, 102, true)
}

// Демо: синтетический ещё не открытый тест виден в группе «Скоро откроются».
func TestDemoHasNotOpenQuiz(t *testing.T) {
	s := newDemoServer(t)
	var resp deadlinesResponse
	decodeBody(t, demoCall(t, s, http.MethodGet, "/api/deadlines", ""), &resp)
	if resp.Counts[dueNotOpen] < 1 {
		t.Fatalf("demo has no not-open quiz: %+v", resp.Counts)
	}
	for _, d := range resp.Deadlines {
		if d.NotOpen && (d.Kind != DeadlineQuiz || d.Opens.IsZero() || d.Group != dueNotOpen) {
			t.Errorf("demo not-open item malformed: %+v", d)
		}
	}
}

func assertForced(t *testing.T, st *store.Store, cmid int64, want bool) {
	t.Helper()
	v, ok, err := st.GetSetting(uni.QuizForceKey(testAdminID, cmid))
	if err != nil {
		t.Fatal(err)
	}
	if got := ok && v == "1"; got != want {
		t.Errorf("cmid %d forced=%v want %v", cmid, got, want)
	}
}
