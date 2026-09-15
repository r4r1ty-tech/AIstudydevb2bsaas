package bbb

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
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

	mu           sync.Mutex
	sessions     map[string]Session
	leaveAt      map[string]time.Time
	noBBB        map[string]struct{}
	lobbyAt      map[string]time.Time
	blockedAt    map[string]time.Time // fail → стоп до JoinYes после этого
	dropRetry    map[string]int       // mid-session: 0/1, второй drop → block
	noteAttempts map[int64]int        // pack id → сколько раз не собрался конспект
	joining      map[string]struct{}
	testPaused   bool
	busy         bool
	joinWG       sync.WaitGroup
	jobWG        sync.WaitGroup
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
		Cfg:          cfg,
		Store:        st,
		Loc:          loc,
		Joiner:       j,
		Hogs:         hogs,
		sessions:     make(map[string]Session),
		leaveAt:      make(map[string]time.Time),
		noBBB:        make(map[string]struct{}),
		lobbyAt:      make(map[string]time.Time),
		blockedAt:    make(map[string]time.Time),
		dropRetry:    make(map[string]int),
		noteAttempts: make(map[int64]int),
		joining:      make(map[string]struct{}),
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
	validLessons := make(map[int64]struct{})

	lessons, err := w.Store.LessonsInJoinWindow(now, JoinEarlyYes)
	if err != nil {
		logx.Errorf("bbb", "lessons: %v", err)
	} else if users, err := w.Store.ListUsers(); err != nil {
		logx.Errorf("bbb", "users: %v", err)
	} else {
		for _, lesson := range lessons {
			validLessons[lesson.ID] = struct{}{}
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
				if w.isBlocked(key, intent) {
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
		w.pauseTest(ctx)
	} else {
		w.resumeTest()
		w.tickTest(ctx, now, wanted)
	}

	logx.Debugf("bbb", "tick lessons=%d joins=%d lecture=%v", len(lessons), len(joins), needLecture)
	for _, j := range joins {
		wanted[j.key] = struct{}{}
		w.joinWG.Add(1)
		go func() {
			defer w.joinWG.Done()
			w.ensureIn(ctx, j.u, j.lesson, j.url, j.key, now, j.record)
		}()
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

	// noBBB живёт только пока пара в окне входа — иначе карта течёт.
	w.mu.Lock()
	for key := range w.noBBB {
		_, lessonID := splitKey(key)
		if _, ok := validLessons[lessonID]; !ok {
			delete(w.noBBB, key)
		}
	}
	w.mu.Unlock()

	w.maybeHarvest(ctx, now)
	w.maybeNotes(ctx, now)
	w.maybePublish(ctx, now)
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
	logx.Warnf("bbb", "no bbb link lesson=%d tg=%d %q", lesson.ID, u.TelegramID, lesson.Discipline)
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
	_, blocked := w.blockedAt[key]
	w.mu.Unlock()
	if blocked {
		return
	}
	if live {
		w.watchLobby(ctx, u, lesson, key, now)
		return
	}
	if !w.beginJoin(key) {
		return
	}
	defer w.endJoin(key)
	w.mu.Lock()
	_, live = w.sessions[key]
	w.mu.Unlock()
	if live {
		w.watchLobby(ctx, u, lesson, key, now)
		return
	}
	role := RolePresence
	if record {
		role = RoleRecord
	}
	logx.Infof("bbb", "join key=%s tg=%d fio=%q record=%v url=%s", key, u.TelegramID, u.FIO, record, url)
	sess, err := w.Joiner.Join(ctx, JoinReq{URL: url, FIO: u.FIO, Role: role})
	if err != nil {
		w.joinFail(ctx, u, lesson, key, now, err.Error())
		return
	}
	lobby, err := sess.InLobby(ctx)
	if err != nil {
		_ = sess.Close()
		w.joinFail(ctx, u, lesson, key, now, err.Error())
		return
	}
	room, err := sess.InRoom(ctx)
	if err != nil {
		_ = sess.Close()
		w.joinFail(ctx, u, lesson, key, now, err.Error())
		return
	}
	if !lobby && !room {
		_ = sess.Close()
		w.joinFail(ctx, u, lesson, key, now, "страница не комната и не лобби")
		return
	}
	w.hogs().Hold()
	if record {
		sess = w.attachRecorder(ctx, sess, lesson, url)
	}
	state := model.PresenceRoom
	if lobby {
		state = model.PresenceLobby
	}
	w.mu.Lock()
	w.sessions[key] = sess
	delete(w.dropRetry, key)
	if state == model.PresenceLobby {
		w.lobbyAt[key] = now
	}
	w.mu.Unlock()
	logx.Infof("bbb", "seated key=%s state=%s tg=%d", key, state, u.TelegramID)
	_ = w.Store.SetPresence(model.Presence{
		TelegramID: u.TelegramID, LessonID: lesson.ID,
		State: state, Message: "join", UpdatedAt: now,
	})
	_ = w.Store.AddEvent(model.Event{
		At: now, Type: model.EventJoin,
		TelegramID: u.TelegramID, LessonID: lesson.ID, Message: lesson.Discipline,
	})
	if state == model.PresenceLobby {
		return
	}
	w.greet(ctx, sess)
}

func (w *Worker) isBlocked(key string, intent *model.JoinIntent) bool {
	w.mu.Lock()
	at, ok := w.blockedAt[key]
	w.mu.Unlock()
	if !ok {
		return false
	}
	// JoinYes после стопа (кнопка / T-15) снимает блок.
	if intent != nil && intent.Decision == model.JoinYes && intent.DecidedAt != nil && intent.DecidedAt.After(at) {
		w.mu.Lock()
		delete(w.blockedAt, key)
		delete(w.dropRetry, key)
		w.mu.Unlock()
		return false
	}
	return true
}

func (w *Worker) joinFail(ctx context.Context, u model.User, lesson model.Lesson, key string, now time.Time, reason string) {
	w.mu.Lock()
	w.blockedAt[key] = now
	delete(w.dropRetry, key)
	w.mu.Unlock()
	_ = w.Store.SetPresence(model.Presence{
		TelegramID: u.TelegramID, LessonID: lesson.ID,
		State: model.PresenceError, Message: reason, UpdatedAt: now,
	})
	_ = w.Store.AddEvent(model.Event{
		At: now, Type: model.EventError,
		TelegramID: u.TelegramID, LessonID: lesson.ID, Message: reason,
	})
	logx.Warnf("bbb", "join fail %s tg=%d: %s", key, u.TelegramID, reason)
	notify.User(ctx, w.Cfg, u.TelegramID, fmt.Sprintf("Не зашёл на «%s»: %s", lesson.Discipline, reason))
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
		w.dropDead(ctx, u, lesson, key, err.Error())
		return
	}
	room, err := sess.InRoom(ctx)
	if err != nil {
		w.dropDead(ctx, u, lesson, key, err.Error())
		return
	}
	if !lobby && !room {
		w.dropDead(ctx, u, lesson, key, "страница не комната")
		return
	}
	if !lobby {
		_ = w.Store.SetPresence(model.Presence{
			TelegramID: u.TelegramID, LessonID: lesson.ID,
			State: model.PresenceRoom, Message: "join", UpdatedAt: now,
		})
		w.mu.Lock()
		delete(w.lobbyAt, key)
		w.mu.Unlock()
		w.greet(ctx, sess)
		return
	}
	_ = w.Store.SetPresence(model.Presence{
		TelegramID: u.TelegramID, LessonID: lesson.ID,
		State: model.PresenceLobby, Message: "waiting room", UpdatedAt: now,
	})
	if since.IsZero() {
		w.mu.Lock()
		w.lobbyAt[key] = now
		w.mu.Unlock()
		since = now
	}
	if now.Sub(since) >= 2*time.Minute {
		logx.Warnf("bbb", "lobby >2m key=%s tg=%d fio=%q", key, u.TelegramID, u.FIO)
		notify.Admin(ctx, w.Cfg, fmt.Sprintf("не пустили из лобби: %s / %d", u.FIO, u.TelegramID))
		notify.User(ctx, w.Cfg, u.TelegramID, fmt.Sprintf(
			"На «%s» всё ещё лобби — модератор пока не пускает.",
			lesson.Discipline,
		))
		_ = w.Store.AddEvent(model.Event{
			At: now, Type: model.EventLobby,
			TelegramID: u.TelegramID, LessonID: lesson.ID, Message: "лобби >2 мин",
		})
		w.mu.Lock()
		w.lobbyAt[key] = now
		w.mu.Unlock()
	}
}

// dropDead: первый вылет — тихий retry; второй — стоп до JoinYes.
func (w *Worker) dropDead(ctx context.Context, u model.User, lesson model.Lesson, key, reason string) {
	w.mu.Lock()
	sess, ok := w.sessions[key]
	if !ok {
		// Нет живой сессии — это не вылет, счётчик не трогаем.
		w.mu.Unlock()
		return
	}
	delete(w.sessions, key)
	delete(w.lobbyAt, key)
	n := w.dropRetry[key] + 1
	w.dropRetry[key] = n
	w.mu.Unlock()
	if sess != nil {
		_ = sess.Close()
	}
	w.hogs().Release()
	now := time.Now()
	if n == 1 {
		logx.Warnf("bbb", "drop retry key=%s tg=%d: %s", key, u.TelegramID, reason)
		_ = w.Store.ClearPresence(u.TelegramID)
		_ = w.Store.AddEvent(model.Event{
			At: now, Type: model.EventError,
			TelegramID: u.TelegramID, LessonID: lesson.ID, Message: "retry: " + reason,
		})
		return
	}
	w.joinFail(ctx, u, lesson, key, now, reason)
}

func (w *Worker) leave(ctx context.Context, telegramID, lessonID int64, key, reason string) {
	w.mu.Lock()
	sess, ok := w.sessions[key]
	delete(w.sessions, key)
	delete(w.leaveAt, key)
	delete(w.lobbyAt, key)
	delete(w.blockedAt, key)
	delete(w.dropRetry, key)
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
	w.hogs().Release()
}

func (w *Worker) closeAll() {
	w.joinWG.Wait()
	w.jobWG.Wait()
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

func (w *Worker) beginJoin(key string) bool {
	if w == nil || key == "" {
		return true
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.joining[key]; ok {
		return false
	}
	if w.joining == nil {
		w.joining = make(map[string]struct{})
	}
	w.joining[key] = struct{}{}
	return true
}

func (w *Worker) endJoin(key string) {
	if w == nil || key == "" {
		return
	}
	w.mu.Lock()
	delete(w.joining, key)
	w.mu.Unlock()
}

func (w *Worker) WaitIdle() {
	if w == nil {
		return
	}
	w.joinWG.Wait()
	w.jobWG.Wait()
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
		logx.Warnf("bbb", "hello: %v", err)
	}
}
