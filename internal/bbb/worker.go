package bbb

import (
	"context"
	"fmt"
	"io"
	"log"
	"math/rand"
	"strings"
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

	Hogs Hogs

	mu       sync.Mutex
	sessions map[string]Session
	leaveAt  map[string]time.Time
	noBBB    map[string]struct{}
	lobbyAt  map[string]time.Time
	busy     bool
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
		cj := NewChromeJoiner(bin)
		if cfg != nil && cfg.ChromeUserDir != "" {
			cj.UserDataDir = cfg.ChromeUserDir
		}
		j = cj
	}
	hogs := Hogs(nopHogs{})
	if cfg != nil && !cfg.BBBDryRun && cfg.LecturePause {
		hogs = newProcHogs()
	}
	return &Worker{
		Cfg:      cfg,
		Store:    st,
		Loc:      loc,
		Joiner:   j,
		Hogs:     hogs,
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
	wanted := make(map[string]struct{})

	type pending struct {
		u      model.User
		lesson model.Lesson
		url    string
		key    string
		record bool
	}
	var joins []pending
	needLecture := false

	lessons, err := w.Store.LessonsInJoinWindow(now, JoinEarlyYes)
	if err != nil {
		log.Printf("bbb: lessons: %v", err)
	} else if users, err := w.Store.ListUsers(); err != nil {
		log.Printf("bbb: users: %v", err)
	} else {
		for _, lesson := range lessons {
			url := strings.TrimSpace(w.Store.GetLessonBBB(lesson.ID))
			recID := int64(0)
			if url != "" && model.IsLecture(lesson.Type) {
				recID = w.pickRecorder(users, lesson, now)
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
				if !ShouldBeInRoom(now, EnterAt(lesson.Begin, intent), leave) {
					w.leave(ctx, u.TelegramID, lesson.ID, key, "time")
					continue
				}
				needLecture = true
				joins = append(joins, pending{
					u: u, lesson: lesson, url: url, key: key,
					record: recID != 0 && u.TelegramID == recID,
				})
			}
		}
	}

	// Тест — отдельная сущность (settings.test_join). Chrome один:
	// если идёт пара — тест гасим; иначе крутим тест.
	if needLecture {
		w.stopTest(ctx, "lecture", false)
	} else {
		w.tickTest(ctx, now, wanted)
	}

	for _, j := range joins {
		wanted[j.key] = struct{}{}
		w.ensureIn(ctx, j.u, j.lesson, j.url, j.key, now, j.record)
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
		if key == testSessionKey {
			w.stopTest(ctx, "idle", false)
			continue
		}
		tgID, lessonID := splitKey(key)
		w.leave(ctx, tgID, lessonID, key, "slot over")
	}
	w.maybeHarvest(ctx, now)
	w.maybeNotes(ctx, now)
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
	notify.User(ctx, w.Cfg, u.TelegramID, fmt.Sprintf(
		"Не зашёл на «%s»: нет ссылки на комнату. Пришли bbb.ssau.ru/b/… сюда — запомню.",
		lesson.Discipline,
	))
}

func (w *Worker) ensureIn(ctx context.Context, u model.User, lesson model.Lesson, url, key string, now time.Time, record bool) {
	w.mu.Lock()
	_, live := w.sessions[key]
	w.mu.Unlock()
	if live {
		w.watchLobby(ctx, u, lesson, key, now)
		return
	}
	w.hogs().Hold()
	role := RolePresence
	if record {
		role = RoleRecord
	}
	sess, err := w.Joiner.Join(ctx, JoinReq{URL: url, FIO: u.FIO, SOCKS5: u.SOCKS5, Role: role})
	if err != nil {
		w.hogs().Release()
		_ = w.Store.SetPresence(model.Presence{
			TelegramID: u.TelegramID, LessonID: lesson.ID,
			State: model.PresenceError, Message: err.Error(), UpdatedAt: now,
		})
		_ = w.Store.AddEvent(model.Event{
			At: now, Type: model.EventError,
			TelegramID: u.TelegramID, LessonID: lesson.ID, Message: err.Error(),
		})
		notify.User(ctx, w.Cfg, u.TelegramID, fmt.Sprintf("Не смог зайти на «%s».", lesson.Discipline))
		return
	}
	if record {
		sess = w.attachRecorder(ctx, sess, lesson, url)
	}
	state := model.PresenceRoom
	if lobby, _ := sess.InLobby(ctx); lobby {
		state = model.PresenceLobby
	} else if ok, _ := sess.InMeeting(ctx); !ok {
		_ = sess.Close()
		w.hogs().Release()
		_ = w.Store.SetPresence(model.Presence{
			TelegramID: u.TelegramID, LessonID: lesson.ID,
			State: model.PresenceError, Message: "не в комнате", UpdatedAt: now,
		})
		_ = w.Store.AddEvent(model.Event{
			At: now, Type: model.EventError,
			TelegramID: u.TelegramID, LessonID: lesson.ID, Message: "не в комнате после join",
		})
		notify.User(ctx, w.Cfg, u.TelegramID, fmt.Sprintf("Не смог зайти на «%s».", lesson.Discipline))
		return
	}
	w.mu.Lock()
	w.sessions[key] = sess
	if state == model.PresenceLobby {
		w.lobbyAt[key] = now
	}
	w.mu.Unlock()
	_ = w.Store.SetPresence(model.Presence{
		TelegramID: u.TelegramID, LessonID: lesson.ID,
		State: state, Message: "join", UpdatedAt: now,
	})
	_ = w.Store.AddEvent(model.Event{
		At: now, Type: model.EventJoin,
		TelegramID: u.TelegramID, LessonID: lesson.ID, Message: lesson.Discipline,
	})
	if state == model.PresenceLobby {
		notify.User(ctx, w.Cfg, u.TelegramID, fmt.Sprintf(
			"На «%s» жду в лобби. Имя в списке: %s.",
			lesson.Discipline, u.FIO,
		))
		return
	}
	w.greet(ctx, sess)
	notify.User(ctx, w.Cfg, u.TelegramID, fmt.Sprintf(
		"Зашёл на «%s» как %s. Без микрофона, имя в списке.",
		lesson.Discipline, u.FIO,
	))
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
	if err != nil {
		return
	}
	if !lobby {
		if ok, _ := sess.InMeeting(ctx); !ok {
			return
		}
		_ = w.Store.SetPresence(model.Presence{
			TelegramID: u.TelegramID, LessonID: lesson.ID,
			State: model.PresenceRoom, Message: "join", UpdatedAt: now,
		})
		w.mu.Lock()
		_, wasLobby := w.lobbyAt[key]
		delete(w.lobbyAt, key)
		w.mu.Unlock()
		if wasLobby {
			notify.User(ctx, w.Cfg, u.TelegramID, fmt.Sprintf(
				"Пустили на «%s» как %s. Без микрофона.",
				lesson.Discipline, u.FIO,
			))
		}
		w.greet(ctx, sess)
		return
	}
	_ = w.Store.SetPresence(model.Presence{
		TelegramID: u.TelegramID, LessonID: lesson.ID,
		State: model.PresenceLobby, Message: "waiting room", UpdatedAt: now,
	})
	if now.Sub(since) >= 2*time.Minute {
		notify.Admin(ctx, w.Cfg, fmt.Sprintf("не пустили из лобби: %s / %d", u.FIO, u.TelegramID))
		notify.User(ctx, w.Cfg, u.TelegramID, fmt.Sprintf(
			"На «%s» всё ещё лобби — модератор пока не пускает.",
			lesson.Discipline,
		))
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
	title := reason
	if l, err := w.Store.LessonByID(lessonID); err == nil && l != nil && l.Discipline != "" {
		title = l.Discipline
	}
	notify.User(ctx, w.Cfg, telegramID, fmt.Sprintf("Вышел с «%s».", title))
	w.hogs().Release()
}

func (w *Worker) closeAll() {
	w.mu.Lock()
	for k, s := range w.sessions {
		_ = s.Close()
		delete(w.sessions, k)
	}
	if c, ok := w.Joiner.(io.Closer); ok {
		_ = c.Close()
	}
	w.mu.Unlock()
	w.hogs().Reset()
}

func (w *Worker) hogs() Hogs {
	if w == nil || w.Hogs == nil {
		return nopHogs{}
	}
	return w.Hogs
}

func splitKey(key string) (int64, int64) {
	var a, b int64
	fmt.Sscanf(key, "%d:%d", &a, &b)
	return a, b
}

func (w *Worker) greet(ctx context.Context, sess Session) {
	if sess == nil {
		return
	}
	if err := sess.Greet(ctx); err != nil {
		log.Printf("bbb: hello: %v", err)
	}
}
