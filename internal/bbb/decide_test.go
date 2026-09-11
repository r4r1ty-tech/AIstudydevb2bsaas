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
	s, err := DryJoiner{}.Join(context.Background(), "https://bbb.ssau.ru/b/x", "Иванов", "")
	if err != nil || s == nil {
		t.Fatalf("dry: %v %v", s, err)
	}
	lobby, err := s.InLobby(context.Background())
	if err != nil || lobby {
		t.Fatalf("lobby: %v %v", lobby, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}
