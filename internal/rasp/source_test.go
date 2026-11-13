package rasp

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
)

func TestRefreshPrefersSourceAndFallsBack(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	siteCalls := 0
	prev := fetchSchedule
	fetchSchedule = func(ctx context.Context, groupID int64, week int) ([]byte, int, error) {
		siteCalls++
		return []byte("no"), http.StatusForbidden, nil
	}
	t.Cleanup(func() { fetchSchedule = prev })

	begin := time.Now().Add(time.Hour)
	lk := []model.Lesson{{
		Date: begin.Format("2006-01-02"), Start: "09:45", End: "11:20", Begin: begin, Finish: begin.Add(95 * time.Minute),
		Discipline: "Из ЛК", Type: "Лекция", Online: true, BBBURL: "https://bbb.ssau.ru/b/x", Subgroup: 2,
	}, {
		Date: begin.Format("2006-01-02"), Start: "13:30", End: "15:05", Begin: begin, Finish: begin.Add(95 * time.Minute),
		Discipline: "ИОТ", Type: "Практика", Owner: 42,
	}}
	var srcErr error
	r := &Refresher{Store: st, GroupID: 1, Loc: time.UTC, Source: func(context.Context) ([]model.Lesson, error) { return lk, srcErr }}
	run, err := r.Refresh(context.Background())
	if err != nil || !run.OK || run.LessonCount != 2 {
		t.Fatalf("run=%+v err=%v", run, err)
	}
	if siteCalls != 0 {
		t.Fatalf("site fetched although LK worked: %d", siteCalls)
	}
	got, _ := st.ListLessons()
	byName := map[string]model.Lesson{}
	for _, l := range got {
		byName[l.Discipline] = l
	}
	if byName["Из ЛК"].BBBURL != "https://bbb.ssau.ru/b/x" || byName["Из ЛК"].Subgroup != 2 || byName["ИОТ"].Owner != 42 {
		t.Fatalf("stored %+v", got)
	}

	// ЛК упал — идём на сайт; сайт отдал 403 — старое расписание остаётся.
	srcErr = errors.New("lk down")
	run, _ = r.Refresh(context.Background())
	if siteCalls == 0 || run.OK {
		t.Fatalf("fallback: calls=%d run=%+v", siteCalls, run)
	}
	if got, _ := st.ListLessons(); len(got) != 2 {
		t.Fatalf("lessons lost on fallback failure: %d", len(got))
	}
}
