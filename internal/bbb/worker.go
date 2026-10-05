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
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/proxyrelay"
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
	blockedAt    map[string]time.Time // maxJoinFails неудач подряд → стоп до JoinYes после этого
	dropRetry    map[string]int       // вылеты из комнаты за пару; maxDrops → block
	failN        map[string]int       // неудачные заходы подряд
	retryAt      map[string]time.Time // следующая попытка после неудачи (бэкофф)
	failAt       map[string]time.Time // последняя неудача: JoinYes позже неё снимает стоп
	probeErr     map[string]int       // ошибки проверки живой сессии подряд
	done         map[string]struct{}  // вышли по времени — до конца пары не заходим
	lobbyAlertAt map[string]time.Time // последний алерт «долго в лобби»
	recorder     map[int64]string     // пара → ключ сессии, которая пишет звук
	recFailAt    map[int64]time.Time  // пара → когда не стартовала запись
	recFailN     map[int64]int        // пара → сколько раз подряд запись не стартовала
	noBBBAdmin   map[int64]struct{}   // пара → админу про «нет ссылки» уже писали
	audioMiss    map[string]int       // пишущая вкладка не в аудио, тиков подряд
	joining      map[string]struct{}
	testPaused   bool
	lectureNow   bool      // этот тик решил сидеть на паре — тяжёлые задачи ждут
	testFailN    int       // неудачные тест-заходы подряд
	testLastFail string    // последняя причина — не дублируем админу
	testRetryAt  time.Time // бэкофф тест-захода
	busy         bool
	joinWG       sync.WaitGroup
	jobWG        sync.WaitGroup
}

func NewWorker(cfg *config.Config, st *store.Store, loc *time.Location) *Worker {
	logx.Debugf("bbb", "NewWorker: cfg_nil=%v", cfg == nil)
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
		if cfg != nil {
			cj.Proxies = loadProxies(cfg.ProxyFile)
		}
		j = cj
	}
	hogs := Hogs(nopHogs{})
	if cfg != nil && !cfg.BBBDryRun && cfg.LecturePause {
		hogs = newProcHogs()
	}
	w := &Worker{
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
		failN:        make(map[string]int),
		retryAt:      make(map[string]time.Time),
		failAt:       make(map[string]time.Time),
		probeErr:     make(map[string]int),
		done:         make(map[string]struct{}),
		lobbyAlertAt: make(map[string]time.Time),
		recorder:     make(map[int64]string),
		recFailAt:    make(map[int64]time.Time),
		recFailN:     make(map[int64]int),
		noBBBAdmin:   make(map[int64]struct{}),
		audioMiss:    make(map[string]int),
		joining:      make(map[string]struct{}),
	}
	logx.Debugf("bbb", "NewWorker: joiner=%T hogs=%T", j, hogs)
	return w
}

// loadProxies reads the SOCKS5 list; every browser tab then gets its own exit.
func loadProxies(path string) *proxyrelay.Pool {
	list, err := proxyrelay.ParseFile(path)
	if err != nil {
		logx.Warnf("bbb", "loadProxies: %v — работаю напрямую", err)
		return nil
	}
	if len(list) == 0 {
		logx.Infof("bbb", "loadProxies: пусто (%s) — работаю напрямую", path)
		return nil
	}
	return proxyrelay.NewPool(list)
}

func (w *Worker) Run(ctx context.Context) error {
	logx.Infof("bbb", "Run: start")
	// Сессий ещё нет: «в комнате» от прошлого процесса (рестарт, краш) — враньё.
	if err := w.Store.ClearAllPresence(); err != nil {
		logx.Warnf("bbb", "Run: clear presence: %v", err)
	}
	w.tick(ctx)
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			logx.Infof("bbb", "Run: ctx done, closing")
			w.closeAll()
			return nil
		case <-t.C:
			w.tick(ctx)
		}
	}
}

func (w *Worker) tick(ctx context.Context) {
	if w == nil {
		return
	}
	now := time.Now().In(w.Loc)
	wanted := make(map[string]struct{})
	logx.Debugf("bbb", "tick: now=%s", now.Format(time.RFC3339))

	var joins []pending
	needLecture := false
	validLessons := make(map[int64]struct{})
	// dbErr: тик видел неполную картину — живые сессии не трогаем, иначе
	// один сбой SQLite выкинет всех из комнат и оборвёт запись.
	dbErr := false

	lessons, err := w.Store.LessonsInJoinWindow(now, JoinEarlyYes)
	if err != nil {
		logx.Errorf("bbb", "lessons: %v", err)
		dbErr = true
	} else if users, err := w.Store.ListUsers(); err != nil {
		logx.Errorf("bbb", "users: %v", err)
		dbErr = true
	} else {
		logx.Debugf("bbb", "tick: lessons=%d users=%d", len(lessons), len(users))
		for _, lesson := range lessons {
			validLessons[lesson.ID] = struct{}{}
			url, _, err := w.Store.LessonBBB(lesson.ID)
			if err != nil {
				// Сбой БД — не «нет ссылки»: не шлём ложное «пришли ссылку».
				logx.Errorf("bbb", "tick: lesson bbb lesson=%d: %v", lesson.ID, err)
				dbErr = true
				continue
			}
			url = strings.TrimSpace(url)
			var here []pending
			for i := range users {
				u := users[i]
				if !u.Active(now) || !lesson.MatchesSubgroup(u.Subgroup) {
					continue
				}
				intent, err := w.Store.GetIntent(u.TelegramID, lesson.ID)
				if err != nil {
					logx.Errorf("bbb", "tick: intent tg=%d lesson=%d: %v", u.TelegramID, lesson.ID, err)
					dbErr = true
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
				if w.isDone(key) {
					continue
				}
				leave := w.ensureLeave(key, lesson.Finish)
				if w.isRecorder(lesson.ID, key) {
					// Пишущая вкладка сидит до звонка: хвост лекции не теряем.
					leave = lesson.Finish
				}
				if !ShouldBeInRoom(now, EnterAt(lesson.Begin, intent), leave) {
					if !now.Before(leave) {
						w.leave(ctx, u.TelegramID, lesson.ID, key, "time")
						w.markDone(key)
					}
					continue
				}
				if w.isBlocked(key, intent) {
					logx.Debugf("bbb", "tick: key=%s blocked, skip", key)
					continue
				}
				here = append(here, pending{u: u, lesson: lesson, url: url, key: key})
			}
			if len(here) == 0 {
				continue
			}
			if model.IsLecture(lesson.Type) {
				if i := w.chooseRecorder(lesson, here, now); i >= 0 {
					here[i].record = true
					w.promoteRecorder(ctx, here[i])
				}
			}
			needLecture = true
			joins = append(joins, here...)
		}
	}

	w.mu.Lock()
	w.lectureNow = needLecture
	w.mu.Unlock()

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

	if dbErr {
		logx.Warnf("bbb", "tick: db error, keep live sessions as is")
		return
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

	w.forget(ctx, validLessons)

	w.maybeHarvest(ctx, now)
	w.maybeNotes(ctx, now)
	w.maybePublish(ctx, now)
}

func (w *Worker) ensureLeave(key string, finish time.Time) time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	if t, ok := w.leaveAt[key]; ok {
		logx.Debugf("bbb", "ensureLeave: key=%s cached=%s", key, t.Format(time.RFC3339))
		return t
	}
	early := time.Duration(rand.Intn(int(maxLeaveEarly) + 1))
	t := LeaveAt(finish, early)
	w.leaveAt[key] = t
	logx.Debugf("bbb", "ensureLeave: key=%s finish=%s early=%s leaveAt=%s", key, finish.Format(time.RFC3339), early, t.Format(time.RFC3339))
	return t
}

const (
	maxJoinFails   = 3                // неудачных заходов подряд до стопа «до JoinYes»
	joinRetryBase  = 30 * time.Second // бэкофф: 30с, 60с
	maxProbeErrs   = 3                // ошибок проверки подряд, чтобы счесть сессию мёртвой
	maxDrops       = 2                // вылетов за пару до стопа
	lobbyAlertWait = 2 * time.Minute  // лобби после начала пары дольше этого — алерт
	lobbyAlertGap  = 20 * time.Minute // повтор алерта админу
	recRetryGap    = 2 * time.Minute  // запись не стартовала — новая попытка не раньше
	maxRecFails    = 3                // столько неудач подряд — пара остаётся без записи
)

type pending struct {
	u      model.User
	lesson model.Lesson
	url    string
	key    string
	record bool
}

func (w *Worker) missingBBB(ctx context.Context, u model.User, lesson model.Lesson, key string) {
	logx.Debugf("bbb", "missingBBB: key=%s lesson=%d tg=%d", key, lesson.ID, u.TelegramID)
	w.mu.Lock()
	_, seen := w.noBBB[key]
	if !seen {
		w.noBBB[key] = struct{}{}
	}
	_, adminSeen := w.noBBBAdmin[lesson.ID]
	w.noBBBAdmin[lesson.ID] = struct{}{}
	w.mu.Unlock()
	if seen {
		return
	}
	if err := w.Store.SetPresence(model.Presence{
		TelegramID: u.TelegramID,
		LessonID:   lesson.ID,
		State:      model.PresenceError,
		Message:    "нет ссылки BBB",
		UpdatedAt:  time.Now(),
	}); err != nil {
		logx.Errorf("bbb", "missingBBB: set presence tg=%d: %v", u.TelegramID, err)
	}
	logx.Warnf("bbb", "no bbb link lesson=%d tg=%d %q", lesson.ID, u.TelegramID, lesson.Discipline)
	if err := w.Store.AddEvent(model.Event{
		At: time.Now(), Type: model.EventNoBBB,
		TelegramID: u.TelegramID, LessonID: lesson.ID,
		Message: lesson.Discipline,
	}); err != nil {
		logx.Errorf("bbb", "missingBBB: add event tg=%d: %v", u.TelegramID, err)
	}
	// Админу — одно сообщение на пару, и не дублируем, если админ сам студент.
	if !adminSeen && u.TelegramID != w.adminID() {
		notify.Admin(ctx, w.Cfg, fmt.Sprintf("нет ссылки BBB: %s %s", lesson.Discipline, lesson.SlotLabel()))
	}
	notify.User(ctx, w.Cfg, u.TelegramID, fmt.Sprintf(
		"Нет ссылки на комнату «%s» (%s). Пришли bbb.ssau.ru/b/… сюда — привяжу и запомню для этого предмета.",
		lesson.Discipline, lesson.SlotLabel(),
	))
}

// chooseRecorder: одна пишущая вкладка на пару. Живой рекордер среди тех, кто
// сейчас должен быть в комнате, остаётся; иначе берём кандидата с меньшим id.
func (w *Worker) chooseRecorder(lesson model.Lesson, here []pending, now time.Time) int {
	w.mu.Lock()
	cur := w.recorder[lesson.ID]
	_, curLive := w.sessions[cur]
	failAt, failed := w.recFailAt[lesson.ID]
	fails := w.recFailN[lesson.ID]
	w.mu.Unlock()
	if cur != "" && curLive {
		for i := range here {
			if here[i].key == cur {
				return i
			}
		}
	}
	// Каждая новая попытка — перезаход вкладки на глазах у преподавателя.
	// Запись не стартует раз за разом — сидим до конца пары просто присутствием.
	if fails >= maxRecFails {
		return -1
	}
	if failed && now.Sub(failAt) < recRetryGap {
		return -1
	}
	best := -1
	for i := range here {
		if best < 0 || here[i].u.TelegramID < here[best].u.TelegramID {
			best = i
		}
	}
	return best
}

// promoteRecorder: выбранный рекордер сидит без звука (presence) — переоткрываем
// его вкладку с записью. Если он уже пишет, ничего не делаем.
func (w *Worker) promoteRecorder(ctx context.Context, p pending) {
	w.mu.Lock()
	_, live := w.sessions[p.key]
	isRec := w.recorder[p.lesson.ID] == p.key
	w.mu.Unlock()
	if !live || isRec {
		return
	}
	logx.Infof("bbb", "recorder handover lesson=%d -> %s", p.lesson.ID, p.key)
	w.closeSession(ctx, p.u.TelegramID, p.lesson.ID, p.key, "recorder handover", false)
}

func (w *Worker) isRecorder(lessonID int64, key string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.recorder[lessonID] == key
}

func (w *Worker) isDone(key string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.done[key]
	return ok
}

func (w *Worker) markDone(key string) {
	w.mu.Lock()
	w.done[key] = struct{}{}
	w.mu.Unlock()
}

func (w *Worker) ensureIn(ctx context.Context, u model.User, lesson model.Lesson, url, key string, now time.Time, record bool) {
	logx.Debugf("bbb", "ensureIn: key=%s tg=%d lesson=%d record=%v url=%s", key, u.TelegramID, lesson.ID, record, redactURL(url))
	if ctx.Err() != nil {
		return
	}
	w.mu.Lock()
	_, live := w.sessions[key]
	_, blocked := w.blockedAt[key]
	w.mu.Unlock()
	if blocked {
		logx.Debugf("bbb", "ensureIn: key=%s blocked", key)
		return
	}
	if live {
		w.watchLobby(ctx, u, lesson, key, now)
		return
	}
	if !w.beginJoin(key) {
		logx.Debugf("bbb", "ensureIn: key=%s join already in flight", key)
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
	logx.Infof("bbb", "join key=%s tg=%d fio=%q record=%v url=%s", key, u.TelegramID, u.FIO, record, redactURL(url))
	sess, err := w.Joiner.Join(ctx, JoinReq{URL: url, FIO: u.FIO, Role: role})
	if err != nil {
		if ctx.Err() != nil {
			// Остановка процесса посреди захода — не неудача захода.
			logx.Infof("bbb", "ensureIn: join key=%s canceled", key)
			return
		}
		logx.Errorf("bbb", "ensureIn: join key=%s: %v", key, err)
		w.joinFail(ctx, u, lesson, key, now, err.Error())
		return
	}
	closeWith := func(why string) {
		if cerr := sess.Close(); cerr != nil {
			logx.Debugf("bbb", "ensureIn: close after %s: %v", why, cerr)
		}
	}
	// Заход длится до минуты: за это время могли нажать «Не сегодня».
	if in, err := w.Store.GetIntent(u.TelegramID, lesson.ID); err == nil && !WantsJoin(in) {
		logx.Infof("bbb", "ensureIn: key=%s declined while joining", key)
		closeWith("decline")
		return
	}
	lobby, err := sess.InLobby(ctx)
	if err != nil {
		closeWith("lobby err")
		logx.Errorf("bbb", "ensureIn: InLobby key=%s: %v", key, err)
		w.joinFail(ctx, u, lesson, key, now, err.Error())
		return
	}
	room, err := sess.InRoom(ctx)
	if err != nil {
		closeWith("room err")
		logx.Errorf("bbb", "ensureIn: InRoom key=%s: %v", key, err)
		w.joinFail(ctx, u, lesson, key, now, err.Error())
		return
	}
	if !lobby && !room {
		closeWith("unknown seat")
		logx.Warnf("bbb", "ensureIn: key=%s not room and not lobby", key)
		w.joinFail(ctx, u, lesson, key, now, "страница не комната и не лобби")
		return
	}
	logx.Debugf("bbb", "ensureIn: key=%s lobby=%v room=%v", key, lobby, room)
	w.hogs().Hold()
	recording := false
	if record {
		sess, recording = w.attachRecorder(ctx, sess, lesson, url)
	}
	state := model.PresenceRoom
	if lobby {
		state = model.PresenceLobby
	}
	recGaveUp := false
	w.mu.Lock()
	w.sessions[key] = sess
	delete(w.failN, key)
	delete(w.retryAt, key)
	delete(w.probeErr, key)
	if record {
		if recording {
			w.recorder[lesson.ID] = key
			delete(w.recFailAt, lesson.ID)
			delete(w.recFailN, lesson.ID)
		} else {
			w.recFailAt[lesson.ID] = now
			w.recFailN[lesson.ID]++
			recGaveUp = w.recFailN[lesson.ID] == maxRecFails
		}
	}
	if state == model.PresenceLobby {
		w.lobbyAt[key] = now
	}
	w.mu.Unlock()
	logx.Infof("bbb", "seated key=%s state=%s tg=%d record=%v", key, state, u.TelegramID, recording)
	if recGaveUp {
		logx.Warnf("bbb", "lesson=%d: запись не стартовала %d раз — больше не пробую, пара без записи", lesson.ID, maxRecFails)
		notify.Admin(ctx, w.Cfg, fmt.Sprintf("«%s» %s: запись не стартовала %d раз подряд — больше не перезахожу, пара останется без записи и конспекта.", lesson.Discipline, lesson.SlotLabel(), maxRecFails))
	}
	if err := w.Store.SetPresence(model.Presence{
		TelegramID: u.TelegramID, LessonID: lesson.ID,
		State: state, Message: "join", UpdatedAt: now,
	}); err != nil {
		logx.Errorf("bbb", "ensureIn: set presence key=%s: %v", key, err)
	}
	if err := w.Store.AddEvent(model.Event{
		At: now, Type: model.EventJoin,
		TelegramID: u.TelegramID, LessonID: lesson.ID, Message: lesson.Discipline,
	}); err != nil {
		logx.Errorf("bbb", "ensureIn: add event key=%s: %v", key, err)
	}
	if state == model.PresenceLobby {
		return
	}
	w.greet(ctx, sess)
}

// isBlocked: стоп «до JoinYes» после серии неудач или пауза бэкоффа до retryAt.
// JoinYes, нажатый после последней неудачи, снимает и то и другое.
func (w *Worker) isBlocked(key string, intent *model.JoinIntent) bool {
	w.mu.Lock()
	_, blocked := w.blockedAt[key]
	retry, backoff := w.retryAt[key]
	failAt := w.failAt[key]
	w.mu.Unlock()
	if !blocked && !backoff {
		return false
	}
	// DecidedAt в БД с точностью до секунды — сравниваем с усечённым failAt.
	if intent != nil && intent.Decision == model.JoinYes && intent.DecidedAt != nil && !intent.DecidedAt.Before(failAt.Truncate(time.Second)) {
		w.mu.Lock()
		delete(w.blockedAt, key)
		delete(w.retryAt, key)
		delete(w.failN, key)
		delete(w.failAt, key)
		delete(w.dropRetry, key)
		w.mu.Unlock()
		logx.Debugf("bbb", "isBlocked: key=%s unblocked by JoinYes intent=%s", key, intent.DecidedAt.Format(time.RFC3339))
		return false
	}
	if blocked {
		logx.Debugf("bbb", "isBlocked: key=%s blocked since=%s", key, failAt.Format(time.RFC3339))
		return true
	}
	return time.Now().Before(retry)
}

// joinFail: неудачный заход. До maxJoinFails — пауза и новая попытка (комнату
// часто ещё не открыли), потом стоп до JoinYes. Пользователю — без сырых ошибок,
// причину видит админ.
func (w *Worker) joinFail(ctx context.Context, u model.User, lesson model.Lesson, key string, now time.Time, reason string) {
	logx.Debugf("bbb", "joinFail: key=%s tg=%d lesson=%d reason=%q", key, u.TelegramID, lesson.ID, reason)
	w.mu.Lock()
	w.failN[key]++
	n := w.failN[key]
	w.failAt[key] = time.Now()
	stop := n >= maxJoinFails
	if stop {
		w.blockedAt[key] = time.Now()
		delete(w.retryAt, key)
	} else {
		w.retryAt[key] = time.Now().Add(joinRetryBase * time.Duration(1<<(n-1)))
	}
	w.mu.Unlock()
	msg := reason
	if !stop {
		msg = fmt.Sprintf("попытка %d/%d не удалась, пробую ещё", n, maxJoinFails)
	}
	if err := w.Store.SetPresence(model.Presence{
		TelegramID: u.TelegramID, LessonID: lesson.ID,
		State: model.PresenceError, Message: msg, UpdatedAt: now,
	}); err != nil {
		logx.Errorf("bbb", "joinFail: set presence key=%s: %v", key, err)
	}
	if err := w.Store.AddEvent(model.Event{
		At: now, Type: model.EventError,
		TelegramID: u.TelegramID, LessonID: lesson.ID, Message: reason,
	}); err != nil {
		logx.Errorf("bbb", "joinFail: add event key=%s: %v", key, err)
	}
	logx.Warnf("bbb", "join fail %s tg=%d attempt=%d/%d: %s", key, u.TelegramID, n, maxJoinFails, reason)
	switch {
	case n == 1:
		notify.User(ctx, w.Cfg, u.TelegramID, fmt.Sprintf("Не получилось зайти на «%s» — пробую ещё раз.", lesson.Discipline))
	case stop:
		notify.User(ctx, w.Cfg, u.TelegramID, fmt.Sprintf(
			"Не зашёл на «%s» после %d попыток. Нажми «Зайти» в карточке пары — попробую снова.",
			lesson.Discipline, maxJoinFails))
		notify.Admin(ctx, w.Cfg, fmt.Sprintf("не зашёл: %s / %s / %d: %s", lesson.Discipline, u.FIO, u.TelegramID, reason))
	}
}

func (w *Worker) watchLobby(ctx context.Context, u model.User, lesson model.Lesson, key string, now time.Time) {
	logx.Debugf("bbb", "watchLobby: key=%s tg=%d", key, u.TelegramID)
	w.mu.Lock()
	sess := w.sessions[key]
	since, wasLobby := w.lobbyAt[key]
	w.mu.Unlock()
	if sess == nil {
		logx.Debugf("bbb", "watchLobby: key=%s no session", key)
		return
	}
	lobby, err := sess.InLobby(ctx)
	room := false
	if err == nil {
		room, err = sess.InRoom(ctx)
	}
	if err != nil || (!lobby && !room) {
		if ctx.Err() != nil {
			return
		}
		reason := "страница не комната"
		if err != nil {
			reason = err.Error()
		}
		// Одна медленная проверка CDP на загруженной VDS — ещё не вылет.
		w.mu.Lock()
		w.probeErr[key]++
		n := w.probeErr[key]
		w.mu.Unlock()
		logx.Warnf("bbb", "watchLobby: key=%s probe %d/%d: %s", key, n, maxProbeErrs, reason)
		if n >= maxProbeErrs {
			w.dropDead(ctx, u, lesson, key, reason)
		}
		return
	}
	w.mu.Lock()
	delete(w.probeErr, key)
	w.mu.Unlock()
	if !lobby {
		w.checkAudio(ctx, lesson, key, sess)
		if !wasLobby {
			return // уже в комнате, ничего не изменилось
		}
		logx.Infof("bbb", "watchLobby: key=%s promoted to room", key)
		if err := w.Store.SetPresence(model.Presence{
			TelegramID: u.TelegramID, LessonID: lesson.ID,
			State: model.PresenceRoom, Message: "join", UpdatedAt: now,
		}); err != nil {
			logx.Errorf("bbb", "watchLobby: set presence key=%s: %v", key, err)
		}
		w.mu.Lock()
		delete(w.lobbyAt, key)
		delete(w.lobbyAlertAt, key)
		w.mu.Unlock()
		w.greet(ctx, sess)
		return
	}
	if !wasLobby {
		w.mu.Lock()
		w.lobbyAt[key] = now
		w.mu.Unlock()
		since = now
		if err := w.Store.SetPresence(model.Presence{
			TelegramID: u.TelegramID, LessonID: lesson.ID,
			State: model.PresenceLobby, Message: "waiting room", UpdatedAt: now,
		}); err != nil {
			logx.Errorf("bbb", "watchLobby: set lobby presence key=%s: %v", key, err)
		}
	}
	// До начала пары лобби — норма: преподаватель ещё не открыл комнату.
	from := since
	if lesson.Begin.After(from) {
		from = lesson.Begin
	}
	if now.Sub(from) < lobbyAlertWait {
		return
	}
	w.mu.Lock()
	last, alerted := w.lobbyAlertAt[key]
	if alerted && now.Sub(last) < lobbyAlertGap {
		w.mu.Unlock()
		return
	}
	w.lobbyAlertAt[key] = now
	w.mu.Unlock()
	logx.Warnf("bbb", "lobby wait key=%s tg=%d fio=%q", key, u.TelegramID, u.FIO)
	notify.Admin(ctx, w.Cfg, fmt.Sprintf("не пустили из лобби: %s / %s / %d", lesson.Discipline, u.FIO, u.TelegramID))
	if !alerted {
		notify.User(ctx, w.Cfg, u.TelegramID, fmt.Sprintf(
			"На «%s» всё ещё лобби — модератор пока не пускает.",
			lesson.Discipline,
		))
		if err := w.Store.AddEvent(model.Event{
			At: now, Type: model.EventLobby,
			TelegramID: u.TelegramID, LessonID: lesson.ID, Message: "лобби >2 мин",
		}); err != nil {
			logx.Errorf("bbb", "watchLobby: add event key=%s: %v", key, err)
		}
	}
}

// audioAlertAfter — сколько тиков подряд пишущая вкладка не в аудио до алерта.
const audioAlertAfter = 4

// checkAudio: пишущая вкладка в комнате должна быть в аудио — иначе пишем тишину.
// Каждый тик короткая попытка подключить, через ~минуту — одно сообщение админу.
func (w *Worker) checkAudio(ctx context.Context, lesson model.Lesson, key string, sess Session) {
	if !w.isRecorder(lesson.ID, key) {
		return
	}
	ac, ok := sess.(AudioChecker)
	if !ok {
		return
	}
	if ac.EnsureAudio(ctx) {
		w.mu.Lock()
		was := w.audioMiss[key]
		delete(w.audioMiss, key)
		w.mu.Unlock()
		if was >= audioAlertAfter {
			notify.Admin(ctx, w.Cfg, fmt.Sprintf("запись «%s»: звук подключился", lesson.Discipline))
		}
		return
	}
	w.mu.Lock()
	w.audioMiss[key]++
	n := w.audioMiss[key]
	w.mu.Unlock()
	logx.Warnf("bbb", "checkAudio: key=%s not in audio (%d)", key, n)
	if n == audioAlertAfter {
		notify.Admin(ctx, w.Cfg, fmt.Sprintf("запись «%s»: вкладка в комнате, но не подключилась к звуку — запись может быть пустой", lesson.Discipline))
	}
}

// dropDead: сессия умерла. Первый вылет — тихий перезаход, maxDrops-й — стоп до JoinYes.
func (w *Worker) dropDead(ctx context.Context, u model.User, lesson model.Lesson, key, reason string) {
	logx.Debugf("bbb", "dropDead: key=%s tg=%d reason=%q", key, u.TelegramID, reason)
	w.mu.Lock()
	_, ok := w.sessions[key]
	if !ok {
		// Нет живой сессии — это не вылет, счётчик не трогаем.
		w.mu.Unlock()
		logx.Debugf("bbb", "dropDead: key=%s no live session", key)
		return
	}
	w.dropRetry[key]++
	n := w.dropRetry[key]
	delete(w.probeErr, key)
	w.mu.Unlock()
	w.closeSession(ctx, u.TelegramID, lesson.ID, key, "drop: "+reason, false)
	now := time.Now()
	if n < maxDrops {
		logx.Warnf("bbb", "drop retry key=%s tg=%d: %s", key, u.TelegramID, reason)
		if err := w.Store.AddEvent(model.Event{
			At: now, Type: model.EventError,
			TelegramID: u.TelegramID, LessonID: lesson.ID, Message: "retry: " + reason,
		}); err != nil {
			logx.Errorf("bbb", "dropDead: add retry event key=%s: %v", key, err)
		}
		return
	}
	w.mu.Lock()
	w.failN[key] = maxJoinFails - 1 // следующий joinFail — стоп
	w.mu.Unlock()
	w.joinFail(ctx, u, lesson, key, now, reason)
}

// leave — выход из пары (время, «Не сегодня», конец слота). Состояние ключа
// (done, блок, leaveAt) живёт до конца пары и чистится в forget, иначе после
// выхода по времени бот зайдёт заново с новым случайным leaveAt.
func (w *Worker) leave(ctx context.Context, telegramID, lessonID int64, key, reason string) {
	w.closeSession(ctx, telegramID, lessonID, key, reason, true)
}

// closeSession закрывает вкладку (с записью — склейка сегментов) и снимает
// presence этой пары. event=false — служебное закрытие (вылет, передача записи).
func (w *Worker) closeSession(ctx context.Context, telegramID, lessonID int64, key, reason string, event bool) {
	logx.Debugf("bbb", "closeSession: key=%s tg=%d lesson=%d reason=%q", key, telegramID, lessonID, reason)
	w.mu.Lock()
	sess, ok := w.sessions[key]
	delete(w.sessions, key)
	delete(w.lobbyAt, key)
	delete(w.lobbyAlertAt, key)
	delete(w.probeErr, key)
	delete(w.audioMiss, key)
	if w.recorder[lessonID] == key {
		delete(w.recorder, lessonID)
	}
	w.mu.Unlock()
	if err := w.Store.ClearPresenceFor(telegramID, lessonID); err != nil {
		logx.Errorf("bbb", "closeSession: clear presence tg=%d: %v", telegramID, err)
	}
	if !ok {
		logx.Debugf("bbb", "closeSession: key=%s no session", key)
		return
	}
	if err := sess.Close(); err != nil {
		logx.Debugf("bbb", "closeSession: close key=%s: %v", key, err)
	}
	if event {
		if err := w.Store.AddEvent(model.Event{
			At: time.Now(), Type: model.EventLeave,
			TelegramID: telegramID, LessonID: lessonID, Message: reason,
		}); err != nil {
			logx.Errorf("bbb", "closeSession: add event key=%s: %v", key, err)
		}
	}
	w.hogs().Release()
	logx.Infof("bbb", "left key=%s tg=%d reason=%s", key, telegramID, reason)
}

// forget чистит состояние ключей пар, ушедших из окна входа, и снимает их
// залипшие presence (ошибки «нет ссылки» / «не зашёл»).
func (w *Worker) forget(ctx context.Context, valid map[int64]struct{}) {
	gone := func(key string) bool {
		_, lessonID := splitKey(key)
		_, ok := valid[lessonID]
		return !ok
	}
	type stale struct{ tg, lesson int64 }
	var drop []stale
	w.mu.Lock()
	seen := map[string]struct{}{}
	for _, m := range []map[string]time.Time{w.leaveAt, w.lobbyAt, w.blockedAt, w.retryAt, w.failAt, w.lobbyAlertAt} {
		for key := range m {
			if key != testSessionKey && gone(key) {
				delete(m, key)
				seen[key] = struct{}{}
			}
		}
	}
	for _, m := range []map[string]int{w.dropRetry, w.failN, w.probeErr, w.audioMiss} {
		for key := range m {
			if key != testSessionKey && gone(key) {
				delete(m, key)
				seen[key] = struct{}{}
			}
		}
	}
	for _, m := range []map[string]struct{}{w.done, w.noBBB} {
		for key := range m {
			if gone(key) {
				delete(m, key)
				seen[key] = struct{}{}
			}
		}
	}
	for id := range w.noBBBAdmin {
		if _, ok := valid[id]; !ok {
			delete(w.noBBBAdmin, id)
		}
	}
	for id := range w.recFailAt {
		if _, ok := valid[id]; !ok {
			delete(w.recFailAt, id)
			delete(w.recFailN, id)
		}
	}
	for id, key := range w.recorder {
		if _, live := w.sessions[key]; !live {
			delete(w.recorder, id)
		}
	}
	w.mu.Unlock()
	for key := range seen {
		tg, lesson := splitKey(key)
		drop = append(drop, stale{tg, lesson})
	}
	for _, d := range drop {
		if err := w.Store.ClearPresenceFor(d.tg, d.lesson); err != nil {
			logx.Debugf("bbb", "forget: clear presence tg=%d lesson=%d: %v", d.tg, d.lesson, err)
		}
	}
}

func (w *Worker) closeAll() {
	logx.Debugf("bbb", "closeAll: enter")
	w.joinWG.Wait()
	w.jobWG.Wait()
	w.mu.Lock()
	for k, s := range w.sessions {
		if err := s.Close(); err != nil {
			logx.Debugf("bbb", "closeAll: close key=%s: %v", k, err)
		}
		delete(w.sessions, k)
	}
	if c, ok := w.Joiner.(io.Closer); ok {
		if err := c.Close(); err != nil {
			logx.Debugf("bbb", "closeAll: joiner close: %v", err)
		}
	}
	w.mu.Unlock()
	if err := w.Store.ClearAllPresence(); err != nil {
		logx.Debugf("bbb", "closeAll: clear presence: %v", err)
	}
	w.hogs().Reset()
	logx.Debugf("bbb", "closeAll: done")
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
		logx.Debugf("bbb", "beginJoin: key=%s already joining", key)
		return false
	}
	if w.joining == nil {
		w.joining = make(map[string]struct{})
	}
	w.joining[key] = struct{}{}
	logx.Debugf("bbb", "beginJoin: key=%s acquired", key)
	return true
}

func (w *Worker) endJoin(key string) {
	if w == nil || key == "" {
		return
	}
	w.mu.Lock()
	delete(w.joining, key)
	w.mu.Unlock()
	logx.Debugf("bbb", "endJoin: key=%s released", key)
}

func (w *Worker) WaitIdle() {
	if w == nil {
		return
	}
	logx.Debugf("bbb", "WaitIdle: enter")
	w.joinWG.Wait()
	w.jobWG.Wait()
	logx.Debugf("bbb", "WaitIdle: done")
}

func splitKey(key string) (int64, int64) {
	var a, b int64
	fmt.Sscanf(key, "%d:%d", &a, &b)
	logx.Debugf("bbb", "splitKey: %q -> %d:%d", key, a, b)
	return a, b
}

func (w *Worker) greet(ctx context.Context, sess Session) {
	if sess == nil {
		return
	}
	logx.Debugf("bbb", "greet: enter")
	if err := sess.Greet(ctx); err != nil {
		logx.Warnf("bbb", "hello: %v", err)
	}
}
