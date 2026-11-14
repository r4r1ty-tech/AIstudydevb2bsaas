package tg

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

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
