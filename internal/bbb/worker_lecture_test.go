package bbb

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
)

func TestPickRecorderMinID(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Date(2026, 9, 15, 8, 10, 0, 0, time.UTC)
	lesson := model.Lesson{ID: 7, Subgroup: 0, Type: "Лекция"}
	users := []model.User{
		{TelegramID: 20, FIO: "Бб", Onboarded: true, Enabled: true, Subgroup: 1},
		{TelegramID: 10, FIO: "Аа", Onboarded: true, Enabled: true, Subgroup: 1},
	}
	w := &Worker{Store: st}
	if id := w.pickRecorder(users, lesson, now); id != 10 {
		t.Fatalf("got %d", id)
	}
}
