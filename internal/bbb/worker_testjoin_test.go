package bbb

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
)

func TestTickTestDryJoin(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.PutTestJoin(model.TestJoin{
		URL:    "https://bbb.ssau.ru/b/test-room",
		Want:   model.TestWantDummy,
		Status: model.TestJoining,
	}); err != nil {
		t.Fatal(err)
	}
	w := NewWorker(&config.Config{BBBDryRun: true, AdminID: 1074442235}, st, time.UTC)
	w.tick(context.Background())
	got, err := st.GetTestJoin()
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.TestRoom || got.Mode != model.TestWantDummy {
		t.Fatalf("after join: %+v", got)
	}
	w.mu.Lock()
	_, live := w.sessions[testSessionKey]
	w.mu.Unlock()
	if !live {
		t.Fatal("expected test session")
	}
	got.Want = model.TestWantOff
	if err := st.PutTestJoin(got); err != nil {
		t.Fatal(err)
	}
	w.tick(context.Background())
	w.mu.Lock()
	_, live = w.sessions[testSessionKey]
	w.mu.Unlock()
	if live {
		t.Fatal("test session should leave")
	}
}

func TestInRoomCopy(t *testing.T) {
	dummy := testInRoomText(model.TestJoin{Name: "тест", Want: model.TestWantDummy})
	if dummy == "" {
		t.Fatal("empty dummy")
	}
	listen := testInRoomText(model.TestJoin{Name: "тест", Want: model.TestWantListen})
	if listen == dummy {
		t.Fatal("listen should differ")
	}
}
