package rasp

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
)

func waitStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestWaitRunSeesFreshRun(t *testing.T) {
	st := waitStore(t)
	asked := time.Now()
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = st.SaveParseRun(model.ParseRun{At: time.Now(), OK: true, Status: "200", LessonCount: 7})
	}()
	run, err := WaitRun(context.Background(), st, asked, 5*time.Second)
	if err != nil || run.LessonCount != 7 {
		t.Fatalf("WaitRun = %+v %v", run, err)
	}
}

func TestWaitRunIgnoresOldRunAndTimesOut(t *testing.T) {
	st := waitStore(t)
	if err := st.SaveParseRun(model.ParseRun{At: time.Now().Add(-time.Hour), OK: true}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := WaitRun(nil, st, time.Now(), 500*time.Millisecond) //nolint:staticcheck // nil ctx is handled
	if err == nil {
		t.Fatal("old run must not count")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("timeout not honoured")
	}
}

func TestWaitRunCancelAndNilStore(t *testing.T) {
	if _, err := WaitRun(context.Background(), nil, time.Now(), time.Second); err == nil {
		t.Fatal("nil store")
	}
	st := waitStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := WaitRun(ctx, st, time.Now(), time.Minute); err != context.Canceled {
		t.Fatalf("cancelled = %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := WaitRun(context.Background(), st, time.Now(), time.Second); err == nil {
		t.Fatal("store error must surface")
	}
}
