package rasp

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
)

func fixtureHTML(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "week.html"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestExtractSelectedWeek(t *testing.T) {
	html := []byte(`<a class="week-nav__week" href="/rasp?groupId=1&selectedWeek=2">2</a>
<a class="week-nav__week week-nav__week_current" href="/rasp?groupId=1&selectedWeek=3">3</a>`)
	n, ok := extractSelectedWeek(html)
	if !ok || n != 3 {
		t.Fatalf("got %d %v, want 3 true", n, ok)
	}
}

func TestExtractSSAUCurrentWeekNavSunday(t *testing.T) {
	html := []byte(`<div class="week-nav">
<a href="/rasp?groupId=1&selectedWeek=2&selectedWeekday=1" class="week-nav-prev"><span>2 неделя</span></a>
<div class="week-nav-current"><span class="h3-text week-nav-current_week">3 неделя</span>
<div class="week-nav-current_date">13.09.2026</div></div>
<a href="/rasp?groupId=1&selectedWeek=4&selectedWeekday=1" class="week-nav-next"><span>4 неделя</span></a>
</div>`)
	n, ok := extractSelectedWeek(html)
	if !ok || n != 3 {
		t.Fatalf("current week = %d %v, want 3", n, ok)
	}
	next, ok := extractNextWeek(html)
	if !ok || next != 4 {
		t.Fatalf("next week = %d %v, want 4", next, ok)
	}
}

func TestRefreshSuccessAndDiff(t *testing.T) {
	html := fixtureHTML(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	loc, err := time.LoadLocation("Europe/Samara")
	if err != nil {
		t.Fatal(err)
	}

	prev := fetchSchedule
	fetchSchedule = func(ctx context.Context, groupID int64, week int) ([]byte, int, error) {
		if week == 0 {
			nav := []byte(`<a class="week-nav__week_current" href="/rasp?groupId=1&selectedWeek=5">5</a>`)
			return append(nav, html...), 200, nil
		}
		if week != 6 {
			t.Errorf("unexpected week %d", week)
		}
		return html, 200, nil
	}
	t.Cleanup(func() { fetchSchedule = prev })

	r := &Refresher{Store: st, GroupID: 531023229, Loc: loc}
	run, err := r.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !run.OK || run.Status != "200" {
		t.Fatalf("run = %+v", run)
	}
	if run.LessonCount != 2 {
		t.Fatalf("lesson_count = %d (dedupe of two identical weeks)", run.LessonCount)
	}
	if run.OnlineCount != 1 {
		t.Fatalf("online_count = %d", run.OnlineCount)
	}
	if run.Diff == "" || !strings.Contains(run.Diff, "+") {
		t.Fatalf("expected add diff, got %q", run.Diff)
	}

	got, err := st.ListLessons()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("stored %d", len(got))
	}

	run2, err := r.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if run2.Diff != "" {
		t.Fatalf("second refresh diff = %q", run2.Diff)
	}

	ev, err := st.ListEvents(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ev) == 0 || ev[0].Type != model.EventReparse {
		t.Fatalf("events = %+v", ev)
	}
}

func TestRefreshHTTPErrorDoesNotReplace(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	keep := []model.Lesson{{
		Date: "2026-01-01", Start: "08:00", End: "09:35",
		Begin: time.Now(), Finish: time.Now().Add(time.Hour),
		Discipline: "Keep", Teacher: "T", Place: "online", Online: true,
	}}
	if err := st.ReplaceLessons(keep); err != nil {
		t.Fatal(err)
	}

	prev := fetchSchedule
	fetchSchedule = func(ctx context.Context, groupID int64, week int) ([]byte, int, error) {
		return []byte("no"), http.StatusForbidden, nil
	}
	t.Cleanup(func() { fetchSchedule = prev })

	r := &Refresher{Store: st, GroupID: 1, Loc: time.UTC}
	run, err := r.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if run.OK || run.Status != "403" {
		t.Fatalf("run = %+v", run)
	}
	got, err := st.ListLessons()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Discipline != "Keep" {
		t.Fatalf("lessons replaced on 403: %+v", got)
	}
}

func TestStartCronFiresOncePerMinute(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var calls int
	prevFetch := fetchSchedule
	fetchSchedule = func(ctx context.Context, groupID int64, week int) ([]byte, int, error) {
		calls++
		return []byte(`<div class="schedule"></div>`), 200, nil
	}
	prevNow := nowFn
	prevInt := cronInterval
	loc := time.FixedZone("Europe/Samara", 4*3600)
	slot := time.Date(2026, 9, 8, 7, 0, 10, 0, loc)
	nowFn = func() time.Time { return slot }
	cronInterval = 15 * time.Millisecond
	t.Cleanup(func() {
		fetchSchedule = prevFetch
		nowFn = prevNow
		cronInterval = prevInt
	})

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	r := &Refresher{Store: st, GroupID: 1, Loc: loc}
	r.StartCron(ctx)
	if calls == 0 {
		t.Fatal("cron did not refresh")
	}
	if calls > 2 {
		t.Fatalf("expected one fire this minute, got %d fetches (week0+maybe week+1)", calls)
	}
}

func TestKeepFutureLessons(t *testing.T) {
	parsed := []model.Lesson{{Date: "2026-09-13", Start: "08:00", Discipline: "A"}}
	old := []model.Lesson{
		{Date: "2026-09-12", Start: "08:00", Discipline: "gone"},
		{Date: "2026-09-13", Start: "08:00", Discipline: "A"},
		{Date: "2026-09-14", Start: "09:45", Discipline: "B"},
	}
	got := keepFutureLessons(parsed, old)
	if len(got) != 2 {
		t.Fatalf("got %d: %+v", len(got), got)
	}
	var hasB bool
	for _, l := range got {
		if l.Discipline == "B" {
			hasB = true
		}
		if l.Discipline == "gone" {
			t.Fatal("kept past week leftover")
		}
	}
	if !hasB {
		t.Fatal("dropped next-week lesson")
	}
}

func TestRefreshKeepsFutureWhenNextWeekUnknown(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	loc := time.FixedZone("Europe/Samara", 4*3600)
	future := model.Lesson{
		Date: "2026-09-20", Start: "09:45", End: "11:20",
		Begin: time.Date(2026, 9, 20, 5, 45, 0, 0, time.UTC), Finish: time.Date(2026, 9, 20, 7, 20, 0, 0, time.UTC),
		Discipline: "Статистика", Teacher: "К", Place: "online", Online: true,
	}
	if err := st.ReplaceLessons([]model.Lesson{future}); err != nil {
		t.Fatal(err)
	}

	prev := fetchSchedule
	fetchSchedule = func(ctx context.Context, groupID int64, week int) ([]byte, int, error) {
		if week != 0 {
			t.Errorf("should not fetch week %d without nav", week)
		}
		return fixtureHTML(t), 200, nil
	}
	t.Cleanup(func() { fetchSchedule = prev })

	r := &Refresher{Store: st, GroupID: 1, Loc: loc}
	run, err := r.Refresh(context.Background())
	if err != nil || !run.OK {
		t.Fatalf("run=%+v err=%v", run, err)
	}
	got, err := st.ListLessons()
	if err != nil {
		t.Fatal(err)
	}
	var kept bool
	for _, l := range got {
		if l.Date == "2026-09-20" && l.Discipline == "Статистика" {
			kept = true
		}
	}
	if !kept {
		t.Fatalf("next week dropped: %+v", got)
	}
}
