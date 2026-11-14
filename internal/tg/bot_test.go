package tg

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

func TestStartShowsOnlyPasswordButton(t *testing.T) {
	h := newHarness(t)
	h.text(studentID, "/start")
	h.wantText(studentID, "только пароль в панель и уведомления")
	kb := mainKeyboard()
	if len(kb.Keyboard) != 1 || len(kb.Keyboard[0]) != 1 || kb.Keyboard[0][0].Text != btnPanelPass {
		t.Fatalf("клавиатура = %+v, ждали одну кнопку пароля", kb.Keyboard)
	}
}

func TestOtherTextGetsHint(t *testing.T) {
	h := newHarness(t)
	for _, in := range []string{"Пары", "привет", "https://bbb.ssau.ru/b/abc-def"} {
		h.text(studentID, in)
		h.wantText(studentID, "только выдаёт пароль")
	}
	before := len(h.api.Calls("sendMessage"))
	h.text(studentID, "/today") // старых команд больше нет — молчим
	if len(h.api.Calls("sendMessage")) != before {
		t.Fatal("на старую команду что-то ответили")
	}
}

func TestStrangerIgnored(t *testing.T) {
	h := newHarness(t)
	for _, in := range []string{"/start", "/pass", btnPanelPass, "привет"} {
		h.text(strangerID, in)
	}
	if n := len(h.api.Calls("sendMessage")); n != 0 {
		t.Fatalf("чужому отправлено %d сообщений", n)
	}
}

func TestDeadlineReminderOnce(t *testing.T) {
	h := newHarness(t)
	if err := h.st.PutAccount(model.Account{TelegramID: studentID, Kind: model.AccountMoodle, Login: "x"}); err != nil {
		t.Fatal(err)
	}
	due := time.Now().Add(2 * time.Hour)
	if err := h.st.ReplaceMoodleItems(studentID, []model.MoodleItem{{CMID: 7, Title: "Лаба 1", Course: "Матан", Kind: "assign", Due: &due}}); err != nil {
		t.Fatal(err)
	}
	h.b.remindDeadlines(context.Background())
	h.b.remindDeadlines(context.Background())
	n := 0
	for _, c := range h.api.Calls("sendMessage") {
		if c.Param("chat_id") == itoa(studentID) && strings.Contains(c.Param("text"), "Срок") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("напоминаний = %d, ждали 1", n)
	}
}

func TestParseNotesCallback(t *testing.T) {
	if id, ok := parseNotesCallback("nt:42"); !ok || id != 42 {
		t.Fatalf("nt:42 -> %d,%v", id, ok)
	}
	for _, bad := range []string{"nt:", "nt:-1", "x:1", "nt:abc"} {
		if _, ok := parseNotesCallback(bad); ok {
			t.Fatalf("%q разобрано", bad)
		}
	}
}
