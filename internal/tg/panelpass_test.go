package tg

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/panelpass"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/vault"
)

var pwRe = regexp.MustCompile(`<code>([a-z0-9]{4}-[a-z0-9]{4})</code>`)

func TestPanelPassButtonGivesPersonalPassword(t *testing.T) {
	h := newHarness(t)
	v, err := vault.Load("", filepath.Join(t.TempDir(), "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	h.b.vault = v
	key := v.Derive(panelpass.Label)

	h.onboarded(studentID, "Иванов И.И.", 1)
	h.text(studentID, btnPanelPass)
	m := pwRe.FindStringSubmatch(h.lastTo(studentID))
	if m == nil {
		t.Fatalf("нет пароля в ответе: %q", h.lastTo(studentID))
	}
	ids := []int64{adminID, studentID}
	if id, ok := panelpass.Who(key, ids, m[1], time.Now()); !ok || id != studentID {
		t.Fatalf("пароль студента опознан как %d,%v", id, ok)
	}

	// /pass работает и до онбординга (админ без профиля)
	h.text(adminID, "/pass")
	m = pwRe.FindStringSubmatch(h.lastTo(adminID))
	if m == nil {
		t.Fatalf("/pass без пароля: %q", h.lastTo(adminID))
	}
	if id, ok := panelpass.Who(key, ids, m[1], time.Now()); !ok || id != adminID {
		t.Fatalf("пароль админа опознан как %d,%v", id, ok)
	}

	// чужому — тишина
	before := len(h.api.Calls("sendMessage"))
	h.text(strangerID, btnPanelPass)
	if len(h.api.Calls("sendMessage")) != before {
		t.Fatal("чужому что-то ответили")
	}
}

func TestPanelPassNoVault(t *testing.T) {
	h := newHarness(t)
	h.onboarded(studentID, "Иванов И.И.", 1)
	h.text(studentID, btnPanelPass)
	if got := h.lastTo(studentID); !strings.Contains(got, "не выдаются") {
		t.Fatalf("без ключа: %q", got)
	}
}

func TestMainKeyboardHasPanelPass(t *testing.T) {
	kb := mainKeyboard()
	for _, row := range kb.Keyboard {
		for _, btn := range row {
			if btn.Text == btnPanelPass {
				return
			}
		}
	}
	t.Fatal("в нижнем меню нет кнопки пароля")
}

func groupMessageUpdate(updateID, chatID, userID int64, text string) *gotgbot.Update {
	msg := &gotgbot.Message{
		MessageId: updateID,
		Date:      1,
		Chat:      gotgbot.Chat{Id: chatID, Type: "supergroup"},
		From:      &gotgbot.User{Id: userID, FirstName: "U", Username: fmt.Sprintf("u%d", userID)},
		Text:      text,
	}
	if strings.HasPrefix(text, "/") {
		cmd := strings.Fields(text)[0]
		msg.Entities = []gotgbot.MessageEntity{{Type: "bot_command", Offset: 0, Length: int64(len(cmd))}}
	}
	return &gotgbot.Update{UpdateId: updateID, Message: msg}
}

func TestPanelPassGroupChatNeverLeaksPassword(t *testing.T) {
	h := newHarness(t)
	v, err := vault.Load("", filepath.Join(t.TempDir(), "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	h.b.vault = v
	h.onboarded(studentID, "Иванов И.И.", 1)

	const groupID int64 = -100123456
	for _, cmd := range []string{"/pass", btnPanelPass} {
		h.api.Reset()
		h.upd++
		h.do(groupMessageUpdate(h.upd, groupID, studentID, cmd))

		dmText := h.lastTo(studentID)
		if !pwRe.MatchString(dmText) {
			t.Fatalf("%s: пароль должен прийти в личку studentID, получили: %q", cmd, dmText)
		}
		groupText := h.lastTo(groupID)
		if pwRe.MatchString(groupText) || strings.Contains(groupText, "<code>") {
			t.Fatalf("%s: пароль утёк в групповой чат: %q", cmd, groupText)
		}
		if !strings.Contains(groupText, "Отправил в личные сообщения") {
			t.Fatalf("%s: в группу ждали уведомление об отправке в личку, получили: %q", cmd, groupText)
		}
	}

	// При ошибке доставки в личку в групповой чат уходит предупреждение без пароля.
	h.api.Reset()
	h.api.Fail("sendMessage", "Forbidden: bot can't initiate conversation with a user")
	h.upd++
	h.do(groupMessageUpdate(h.upd, groupID, studentID, "/pass"))

	var sawGroupWarn bool
	for _, c := range h.api.Calls("sendMessage") {
		if c.Param("chat_id") == itoa(groupID) {
			txt := c.Param("text")
			if pwRe.MatchString(txt) || strings.Contains(txt, "<code>") {
				t.Fatalf("при ошибке доставки пароль утёк в группу: %q", txt)
			}
			if strings.Contains(txt, "Не удалось отправить пароль") {
				sawGroupWarn = true
			}
		}
	}
	if !sawGroupWarn {
		t.Fatalf("при ошибке доставки в личку не пришло предупреждение в группу: %+v", h.api.Calls("sendMessage"))
	}
}
