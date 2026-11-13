package webapp

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/vault"
)

func TestFirstRunOnboard(t *testing.T) {
	s, st := newServer(t, nil)
	var me meView
	decodeBody(t, call(t, s, http.MethodGet, "/api/me", ""), &me)
	if me.Onboarded || me.FIO != "" {
		t.Fatalf("fresh me: %+v", me)
	}
	if rec := call(t, s, http.MethodPost, "/api/me", `{"fio":"Иванов Иван","onboard":true}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("onboard without subgroup: %d", rec.Code)
	}
	rec := call(t, s, http.MethodPost, "/api/me", `{"fio":"  Иванов   Иван ","subgroup":2,"onboard":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("onboard: %d %s", rec.Code, rec.Body)
	}
	decodeBody(t, rec, &me)
	if !me.Onboarded || me.FIO != "Иванов Иван" || me.Subgroup != 2 || !me.Active {
		t.Fatalf("after onboard: %+v", me)
	}
	u, _ := st.GetUser(config.DefaultAdminID)
	if u == nil || u.OnboardStage != model.StageDone {
		t.Fatalf("user %+v", u)
	}
}

func TestStoreMoodleWindowsAndNoDue(t *testing.T) {
	s, st := newServer(t, nil)
	v, err := vault.Load("", filepath.Join(t.TempDir(), "k"))
	if err != nil {
		t.Fatal(err)
	}
	m := &storeMoodle{st: st, v: v, owner: config.DefaultAdminID}
	s.moodle = m
	if err := st.PutAccount(model.Account{TelegramID: config.DefaultAdminID, Kind: model.AccountMoodle, Login: "s", Status: model.AccountOK}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	at := func(d time.Duration) *time.Time { x := now.Add(d); return &x }
	day := 24 * time.Hour
	items := []model.MoodleItem{
		{CMID: 1, Kind: model.MoodleQuiz, Title: "Скоро", Due: at(2 * day), Status: model.MoodleTodo},
		{CMID: 2, Kind: model.MoodleAssign, Title: "Свежая просрочка", Due: at(-3 * day), Status: model.MoodleTodo},
		{CMID: 3, Kind: model.MoodleAssign, Title: "Прошлый год", Due: at(-200 * day), Status: model.MoodleTodo},
		{CMID: 4, Kind: model.MoodleQuiz, Title: "Давно сдано", Due: at(-60 * day), Status: model.MoodleSubmitted},
		{CMID: 5, Kind: model.MoodleAssign, Title: "Без срока", Status: model.MoodleTodo},
		{CMID: 6, Kind: model.MoodleAssign, Title: "Без срока, сдано", Status: model.MoodleDone},
		{CMID: 7, Kind: model.MoodleAssign, Title: "Ещё закрыто", Opens: at(day), Status: model.MoodleTodo},
	}
	if err := st.ReplaceMoodleItems(config.DefaultAdminID, items); err != nil {
		t.Fatal(err)
	}
	list, err := m.Deadlines(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, d := range list {
		got[d.Title] = true
	}
	for _, want := range []string{"Скоро", "Свежая просрочка", "Без срока"} {
		if !got[want] {
			t.Errorf("missing %q: %v", want, got)
		}
	}
	for _, no := range []string{"Прошлый год", "Давно сдано", "Без срока, сдано", "Ещё закрыто"} {
		if got[no] {
			t.Errorf("must hide %q", no)
		}
	}

	var resp deadlinesResponse
	decodeBody(t, call(t, s, http.MethodGet, "/api/deadlines", ""), &resp)
	if resp.Counts[dueNone] != 1 || resp.Counts[dueOverdue] != 1 {
		t.Fatalf("counts %+v", resp.Counts)
	}
	if last := resp.Deadlines[len(resp.Deadlines)-1]; last.Group != dueNone {
		t.Fatalf("no-due must go last: %+v", last)
	}
}
