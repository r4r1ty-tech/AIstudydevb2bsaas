package bbb

import (
	"context"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

func TestWantsJoin(t *testing.T) {
	no := &model.JoinIntent{Decision: model.JoinNo}
	yes := &model.JoinIntent{Decision: model.JoinYes}
	pending := &model.JoinIntent{Decision: model.JoinPending}
	if WantsJoin(no) {
		t.Fatal("no should skip")
	}
	if !WantsJoin(yes) || !WantsJoin(pending) || !WantsJoin(nil) {
		t.Fatal("silence/yes/pending join")
	}
}

func TestEnterAt(t *testing.T) {
	begin := time.Date(2026, 9, 12, 11, 30, 0, 0, time.UTC)
	if got := EnterAt(begin, nil); !got.Equal(begin.Add(-5 * time.Minute)) {
		t.Fatalf("silence: %v", got)
	}
	pending := &model.JoinIntent{Decision: model.JoinPending}
	if got := EnterAt(begin, pending); !got.Equal(begin.Add(-5 * time.Minute)) {
		t.Fatalf("pending: %v", got)
	}
	yes := &model.JoinIntent{Decision: model.JoinYes}
	if got := EnterAt(begin, yes); !got.Equal(begin.Add(-15 * time.Minute)) {
		t.Fatalf("yes: %v", got)
	}
}

func TestShouldBeInRoom(t *testing.T) {
	begin := time.Date(2026, 9, 11, 8, 0, 0, 0, time.UTC)
	leave := time.Date(2026, 9, 11, 9, 30, 0, 0, time.UTC)
	if ShouldBeInRoom(begin.Add(-time.Minute), begin, leave) {
		t.Fatal("before begin")
	}
	if !ShouldBeInRoom(begin, begin, leave) {
		t.Fatal("at begin")
	}
	if ShouldBeInRoom(leave, begin, leave) {
		t.Fatal("at leave")
	}
}

func TestDryJoin(t *testing.T) {
	s, err := DryJoiner{}.Join(context.Background(), JoinReq{URL: "https://bbb.ssau.ru/b/x", FIO: "Иванов"})
	if err != nil || s == nil {
		t.Fatalf("dry: %v %v", s, err)
	}
	lobby, err := s.InLobby(context.Background())
	if err != nil || lobby {
		t.Fatalf("lobby: %v %v", lobby, err)
	}
	room, err := s.InRoom(context.Background())
	if err != nil || !room {
		t.Fatalf("room: %v %v", room, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Greet(context.Background()); err != nil {
		t.Fatal(err)
	}
	n, err := s.GrabSlides(context.Background(), t.TempDir())
	if err != nil || n != 0 {
		t.Fatalf("slides: %d %v", n, err)
	}
}
