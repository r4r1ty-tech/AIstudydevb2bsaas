package tg

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/vault"
)

// fakeSites — ЛК пускает с паролем pw, Moodle — с паролем pw2 (разные пароли).
func fakeSites(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/account/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Write([]byte(`<form method="POST"><input type="hidden" name="$ACTION_KEY" value="k"><input name="login"><input type="password" name="password"></form>`))
			return
		}
		r.ParseMultipartForm(1 << 20)
		if r.FormValue("password") != "pw" {
			w.Write([]byte("Неверный логин и(или) пароль"))
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "lk", Value: "1", Path: "/"})
		w.WriteHeader(http.StatusSeeOther)
	})
	mux.HandleFunc("/api/proxy/personal/groups", func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie("lk"); err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`[{"id":1,"name":"g"}]`))
	})
	mux.HandleFunc("/moodle/login/index.php", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Write([]byte(`<form id="login" method="post" action="` + srv.URL + `/moodle/login/index.php"><input name="username"><input type="password" name="password"></form>`))
			return
		}
		r.ParseForm()
		if r.FormValue("password") != "pw2" {
			http.Redirect(w, r, "/moodle/login/index.php", http.StatusSeeOther)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "MoodleSession", Value: "ok", Path: "/"})
		http.Redirect(w, r, "/moodle/my/", http.StatusSeeOther)
	})
	mux.HandleFunc("/moodle/my/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`"sesskey":"S"`))
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func withVault(t *testing.T, h *harness, base string) {
	t.Helper()
	v, err := vault.Load("", filepath.Join(t.TempDir(), "k"))
	if err != nil {
		t.Fatal(err)
	}
	h.b.vault = v
	h.b.cfg.LKBaseURL = base
	h.b.cfg.MoodleBaseURL = base + "/moodle"
}

func TestConnectFlow(t *testing.T) {
	h := newHarness(t)
	h.onboarded(studentID, "Иванов Иван", 1)
	srv := fakeSites(t)
	withVault(t, h, srv.URL)

	h.text(studentID, "/connect")
	h.wantText(studentID, "логин от личного кабинета")
	h.text(studentID, "student")
	h.wantText(studentID, "пароль")
	h.text(studentID, "pw") // подходит к ЛК, не к Moodle
	if len(h.api.Calls("deleteMessage")) != 1 {
		t.Fatalf("password message not deleted: %+v", h.api.Calls("deleteMessage"))
	}
	h.wantText(studentID, "ЛК: ✅")
	if got := h.lastTo(studentID); !strings.Contains(got, "Moodle: ❌") {
		t.Fatalf("moodle result: %q", got)
	}
	acc, _ := h.st.GetAccount(studentID, model.AccountLK)
	if acc == nil || acc.Login != "student" || acc.Secret == "" || acc.Secret == "pw" || acc.Session == "" {
		t.Fatalf("lk account %+v", acc)
	}
	if m, _ := h.st.GetAccount(studentID, model.AccountMoodle); m != nil {
		t.Fatalf("moodle saved with wrong password: %+v", m)
	}
	if kicked, _ := h.st.ConsumeRaspRefresh(); !kicked {
		t.Fatal("rasp refresh not requested after LK connect")
	}

	// Отдельный логин для Moodle.
	h.press(studentID, "un:moodle")
	h.text(studentID, "student")
	h.text(studentID, "pw2")
	h.wantText(studentID, "Moodle: ✅")
	if m, _ := h.st.GetAccount(studentID, model.AccountMoodle); m == nil {
		t.Fatal("moodle account not saved")
	}

	h.press(studentID, "un:off")
	if a, _ := h.st.GetAccount(studentID, model.AccountLK); a != nil {
		t.Fatal("lk not deleted")
	}
}

func TestPasswordLooksLikeCommand(t *testing.T) {
	h := newHarness(t)
	h.onboarded(studentID, "Иванов Иван", 1)
	srv := fakeSites(t)
	withVault(t, h, srv.URL)
	h.text(studentID, "/connect")
	h.text(studentID, "student")
	h.text(studentID, "/Пары") // пароль, похожий на команду, — всё равно пароль
	if len(h.api.Calls("deleteMessage")) != 1 {
		t.Fatal("password-like command not treated as password")
	}
}

func TestTasksAndReminders(t *testing.T) {
	h := newHarness(t)
	h.onboarded(studentID, "Иванов Иван", 1)
	h.text(studentID, btnTasks)
	h.wantText(studentID, "Moodle не подключён")

	if err := h.st.PutAccount(model.Account{TelegramID: studentID, Kind: model.AccountMoodle, Login: "s", Status: model.AccountOK}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().In(h.b.loc)
	in := func(d time.Duration) *time.Time { x := now.Add(d); return &x }
	items := []model.MoodleItem{
		{CMID: 1, Course: "ТФЯГ", Kind: model.MoodleQuiz, Title: "Тест <1>", Due: in(2 * time.Hour), Status: model.MoodleTodo, Todo: "Разрешено попыток: 1"},
		{CMID: 2, Course: "Python", Kind: model.MoodleAssign, Title: "ДЗ 2", Due: in(20 * time.Hour), Status: model.MoodleDraft},
		{CMID: 3, Course: "Python", Kind: model.MoodleAssign, Title: "Сдано", Due: in(5 * time.Hour), Status: model.MoodleSubmitted},
		{CMID: 4, Course: "Матан", Kind: model.MoodleAssign, Title: "Просрочка", Due: in(-30 * time.Hour), Status: model.MoodleTodo},
		{CMID: 5, Course: "Матан", Kind: model.MoodleAssign, Title: "Через неделю", Due: in(7 * 24 * time.Hour), Status: model.MoodleTodo},
	}
	if err := h.st.ReplaceMoodleItems(studentID, items); err != nil {
		t.Fatal(err)
	}
	h.text(studentID, btnTasks)
	got := h.lastTo(studentID)
	for _, want := range []string{"Просрочено", "Просрочка", "Тест &lt;1&gt;", "ДЗ 2 ✏️", "Через неделю"} {
		if !strings.Contains(got, want) {
			t.Errorf("tasks miss %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Сдано") {
		t.Errorf("submitted item listed:\n%s", got)
	}
	h.press(studentID, "un:i:1")
	h.wantText(studentID, "Разрешено попыток: 1")

	h.api.Reset()
	h.b.remindDeadlines(context.Background())
	sent := h.api.Calls("sendMessage")
	if len(sent) != 2 { // тест через 2 ч и ДЗ через 20 ч; сданное и далёкое — нет
		t.Fatalf("reminders=%d: %+v", len(sent), sent)
	}
	h.api.Reset()
	h.b.remindDeadlines(context.Background())
	if n := len(h.api.Calls("sendMessage")); n != 0 {
		t.Fatalf("reminders repeated: %d", n)
	}
}
