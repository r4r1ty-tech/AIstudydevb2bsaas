package bbb

import (
	"context"
	"fmt"
	"io"
	"log"
	"math/rand"
	"sync"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/notify"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
)

type Worker struct {
	Cfg    *config.Config
	Store  *store.Store
	Loc    *time.Location
	Joiner Joiner

	mu       sync.Mutex
	sessions map[string]Session
	leaveAt  map[string]time.Time
	noBBB    map[string]struct{}
	lobbyAt  map[string]time.Time
}

func NewWorker(cfg *config.Config, st *store.Store, loc *time.Location) *Worker {
	var j Joiner = DryJoiner{}
	if cfg == nil || !cfg.BBBDryRun {
		bin := ""
		if cfg != nil {
			bin = FindChrome(cfg.ChromeBin)
		} else {
			bin = FindChrome("")
		}
		j = NewChromeJoiner(bin)
	}
	return &Worker{
		Cfg:      cfg,
		Store:    st,
		Loc:      loc,
		Joiner:   j,
		sessions: make(map[string]Session),
		leaveAt:  make(map[string]time.Time),
		noBBB:    make(map[string]struct{}),
		lobbyAt:  make(map[string]time.Time),
	}
}

func (w *Worker) Run(ctx context.Context) error {
	w.tick(ctx)
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			w.closeAll()
			return nil
		case <-t.C:
			w.tick(ctx)
		}
	}
}

func (w *Worker) tick(ctx context.Context) {
	now := time.Now().In(w.Loc)
	lessons, err := w.Store.LessonsHappening(now)
	if err != nil {
		log.Printf("bbb: lessons: %v", err)
		return
	}
	users, err := w.Store.ListUsers()
	if err != nil {
		log.Printf("bbb: users: %v", err)
		return
	}

	wanted := make(map[string]struct{})
	for _, lesson := range lessons {
		link, err := w.Store.GetBBB(model.BBBKey(w.Cfg.GroupID, lesson.Discipline, lesson.Teacher))
		url := ""
		if err == nil && link != nil {
			url = link.URL
		}
		for i := range users {
			u := users[i]
			if !u.Active(now) || !lesson.MatchesSubgroup(u.Subgroup) {
				continue
			}
			intent, err := w.Store.GetIntent(u.TelegramID, lesson.ID)
			if err != nil {
				continue
			}
			if !WantsJoin(intent) {
				continue
			}
			key := sessionKey(u.TelegramID, lesson.ID)
			if url == "" {
				w.missingBBB(ctx, u, lesson, key)
				continue
			}
			leave := w.ensureLeave(key, lesson.Finish)
			if !ShouldBeInRoom(now, lesson.Begin, leave) {
				w.leave(ctx, u.TelegramID, lesson.ID, key, "time")
				continue
			}
			wanted[key] = struct{}{}
			w.ensureIn(ctx, u, lesson, url, key, now)
		}
	}

	w.mu.Lock()
	var extra []string
	for key := range w.sessions {
		if _, ok := wanted[key]; !ok {
			extra = append(extra, key)
		}
	}
	w.mu.Unlock()
	for _, key := range extra {
		tgID, lessonID := splitKey(key)
		w.leave(ctx, tgID, lessonID, key, "slot over")
	}
}

func (w *Worker) ensureLeave(key string, finish time.Time) time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	if t, ok := w.leaveAt[key]; ok {
		return t
	}
	early := time.Duration(rand.Intn(int(maxLeaveEarly) + 1))
	t := LeaveAt(finish, early)
	w.leaveAt[key] = t
	return t
}

func (w *Worker) missingBBB(ctx context.Context, u model.User, lesson model.Lesson, key string) {
	w.mu.Lock()
	_, seen := w.noBBB[key]
	if !seen {
		w.noBBB[key] = struct{}{}
	}
	w.mu.Unlock()
	if seen {
		return
	}
	_ = w.Store.SetPresence(model.Presence{
		TelegramID: u.TelegramID,
		LessonID:   lesson.ID,
		State:      model.PresenceError,
		Message:    "нет ссылки BBB",
		UpdatedAt:  time.Now(),
	})
	_ = w.Store.AddEvent(model.Event{
		At: time.Now(), Type: model.EventNoBBB,
		TelegramID: u.TelegramID, LessonID: lesson.ID,
		Message: lesson.Discipline,
	})
	notify.Admin(ctx, w.Cfg, fmt.Sprintf("нет ссылки BBB: %s / %d", lesson.Discipline, u.TelegramID))
}

func (w *Worker) ensureIn(ctx context.Context, u model.User, lesson model.Lesson, url, key string, now time.Time) {
	w.mu.Lock()
	_, live := w.sessions[key]
	w.mu.Unlock()
	if live {
		w.watchLobby(ctx, u, lesson, key, now)
		return
	}
	sess, err := w.Joiner.Join(ctx, url, u.FIO, u.SOCKS5)
	if err != nil {
		_ = w.Store.SetPresence(model.Presence{
			TelegramID: u.TelegramID, LessonID: lesson.ID,
			State: model.PresenceError, Message: err.Error(), UpdatedAt: now,
		})
		_ = w.Store.AddEvent(model.Event{
			At: now, Type: model.EventError,
			TelegramID: u.TelegramID, LessonID: lesson.ID, Message: err.Error(),
		})
		return
	}
	w.mu.Lock()
	w.sessions[key] = sess
	w.lobbyAt[key] = now
	w.mu.Unlock()
	state := model.PresenceRoom
	if lobby, _ := sess.InLobby(ctx); lobby {
		state = model.PresenceLobby
	}
	_ = w.Store.SetPresence(model.Presence{
		TelegramID: u.TelegramID, LessonID: lesson.ID,
		State: state, Message: "join", UpdatedAt: now,
	})
	_ = w.Store.AddEvent(model.Event{
		At: now, Type: model.EventJoin,
		TelegramID: u.TelegramID, LessonID: lesson.ID, Message: lesson.Discipline,
	})
}

func (w *Worker) watchLobby(ctx context.Context, u model.User, lesson model.Lesson, key string, now time.Time) {
	w.mu.Lock()
	sess := w.sessions[key]
	since := w.lobbyAt[key]
	w.mu.Unlock()
	if sess == nil {
		return
	}
	lobby, err := sess.InLobby(ctx)
	if err != nil || !lobby {
		return
	}
	_ = w.Store.SetPresence(model.Presence{
		TelegramID: u.TelegramID, LessonID: lesson.ID,
		State: model.PresenceLobby, Message: "waiting room", UpdatedAt: now,
	})
	if now.Sub(since) >= 2*time.Minute {
		notify.Admin(ctx, w.Cfg, fmt.Sprintf("не пустили из лобби: %s / %d", u.FIO, u.TelegramID))
		_ = w.Store.AddEvent(model.Event{
			At: now, Type: model.EventLobby,
			TelegramID: u.TelegramID, LessonID: lesson.ID, Message: "лобби >2 мин",
		})
		w.lobbyAt[key] = now // don't spam every tick
	}
}

func (w *Worker) leave(ctx context.Context, telegramID, lessonID int64, key, reason string) {
	w.mu.Lock()
	sess, ok := w.sessions[key]
	delete(w.sessions, key)
	delete(w.leaveAt, key)
	delete(w.lobbyAt, key)
	w.mu.Unlock()
	if !ok {
		return
	}
	_ = sess.Close()
	_ = w.Store.ClearPresence(telegramID)
	_ = w.Store.AddEvent(model.Event{
		At: time.Now(), Type: model.EventLeave,
		TelegramID: telegramID, LessonID: lessonID, Message: reason,
	})
}

func (w *Worker) closeAll() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for k, s := range w.sessions {
		_ = s.Close()
		delete(w.sessions, k)
	}
	if c, ok := w.Joiner.(io.Closer); ok {
		_ = c.Close()
	}
}

func splitKey(key string) (int64, int64) {
	var a, b int64
	fmt.Sscanf(key, "%d:%d", &a, &b)
	return a, b
}
