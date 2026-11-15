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
	recorder     map[string]string    // ключ встречи → ключ сессии, которая пишет звук
	recJoining   map[string]string    // ключ встречи → ключ сессии, для которой идёт заход/старт записи
	recFailAt    map[string]time.Time // ключ встречи → когда не стартовала запись
	recFailN     map[string]int       // ключ встречи → сколько раз подряд запись не стартовала
	noBBBAdmin   map[int64]struct{}   // пара → админу про «нет ссылки» уже писали
	audioMiss    map[string]int       // пишущая сессия не в аудио, тиков подряд
	joining      map[string]struct{}
	seatedAt     map[string]time.Time // когда сессия села (лобби или комната) — для лога выхода
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
		dj := NewDirectJoiner()
		if cfg != nil {
			dj.Proxies = loadProxies(cfg.ProxyFile)
		}
		j = dj
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
		recorder:     make(map[string]string),
		recJoining:   make(map[string]string),
		recFailAt:    make(map[string]time.Time),
		recFailN:     make(map[string]int),
		noBBBAdmin:   make(map[int64]struct{}),
		audioMiss:    make(map[string]int),
		joining:      make(map[string]struct{}),
		seatedAt:     make(map[string]time.Time),
	}
	logx.Debugf("bbb", "NewWorker: joiner=%T hogs=%T", j, hogs)
	return w
}

// meetingGroupKey возвращает ключ встречи для группировки нескольких строк расписания
// (подгруппы, личные ИОТ) на один рекордер.
func meetingGroupKey(lesson model.Lesson, url string) string {
	if k := model.MeetingKey(lesson, url); k != "" {
		return k
	}
	return fmt.Sprintf("lesson:%d", lesson.ID)
}

// lessonTag — «lesson=<id> <предмет>»: по нему грепается одна пара от захода до конспекта.
func lessonTag(l model.Lesson) string {
	return fmt.Sprintf("lesson=%d %s", l.ID, l.Discipline)
}

// packTag — «pack=<Dir> lesson=<id> <предмет>»: метка лекции для строк про пак.
func packTag(p *model.LecturePack) string {
	if p == nil {
		return "pack=?"
	}
	return fmt.Sprintf("pack=%s lesson=%d %s", p.Dir, p.LessonID, p.Discipline)
}

// loadProxies reads the SOCKS5 list; every session then gets its own exit.
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
	logx.Infof("bbb", "loadProxies: прокси=%d из %s", len(list), path)
	return proxyrelay.NewPool(list)
}

func (w *Worker) Run(ctx context.Context) error {
	logx.Infof("bbb", "Run: старт, dry_run=%t joiner=%T", w.Cfg != nil && w.Cfg.BBBDryRun, w.Joiner)
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
	validMeetings := make(map[string]struct{})
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
			validMeetings[fmt.Sprintf("lesson:%d", lesson.ID)] = struct{}{}
			url, _, err := w.Store.LessonBBB(lesson.ID)
			if err != nil {
				// Сбой БД — не «нет ссылки»: не шлём ложное «пришли ссылку».
				logx.Errorf("bbb", "tick: lesson bbb lesson=%d: %v", lesson.ID, err)
				dbErr = true
				continue
			}
			url = strings.TrimSpace(url)
			var meetKey string
			if url != "" {
				meetKey = meetingGroupKey(lesson, url)
				validMeetings[meetKey] = struct{}{}
			}
			var here []pending
			for i := range users {
				u := users[i]
				if !u.Active(now) || !lesson.For(u) {
					logx.Debugf("bbb", "tick: %s tg=%d пропуск: не активен или не его пара (active=%v)", lessonTag(lesson), u.TelegramID, u.Active(now))
					continue
				}
				intent, err := w.Store.GetIntent(u.TelegramID, lesson.ID)
				if err != nil {
					logx.Errorf("bbb", "tick: intent tg=%d lesson=%d: %v", u.TelegramID, lesson.ID, err)
					dbErr = true
					continue
				}
				if !WantsJoin(intent) {
					logx.Debugf("bbb", "tick: %s tg=%d пропуск: не хочет заходить (intent)", lessonTag(lesson), u.TelegramID)
					continue
				}
				key := sessionKey(u.TelegramID, lesson.ID)
				if url == "" {
					w.missingBBB(ctx, u, lesson, key)
					continue
				}
				if w.isDone(key) {
					logx.Debugf("bbb", "tick: %s tg=%d пропуск: уже вышел по времени, до конца пары не захожу", lessonTag(lesson), u.TelegramID)
					continue
				}
				leave := w.ensureLeave(key, lesson.Finish)
				if w.isRecorder(meetKey, key) {
					// Пишущая сессия сидит до звонка: хвост лекции не теряем.
					leave = lesson.Finish
				}
				if !ShouldBeInRoom(now, EnterAt(lesson.Begin, intent), leave) {
					logx.Debugf("bbb", "tick: %s tg=%d вне окна захода: now=%s вход=%s выход=%s",
						lessonTag(lesson), u.TelegramID, now.Format("15:04:05"), EnterAt(lesson.Begin, intent).Format("15:04:05"), leave.Format("15:04:05"))
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
				here = append(here, pending{
					u:       u,
					lesson:  lesson,
					url:     url,
					key:     key,
					meetKey: meetKey,
					intent:  intent,
				})
			}
			if len(here) == 0 {
				continue
			}
			needLecture = true
			joins = append(joins, here...)
		}

		// На одну встречу BBB (даже если в расписании несколько строк подгрупп/ИОТ)
		// выбираем ровно одного рекордера в стабильном порядке.
		var lectureKeys []string
		lectureGroups := make(map[string][]int)
		for idx := range joins {
			if !model.IsLecture(joins[idx].lesson.Type) {
				continue
			}
			mk := joins[idx].meetKey
			if _, exists := lectureGroups[mk]; !exists {
				lectureKeys = append(lectureKeys, mk)
			}
			lectureGroups[mk] = append(lectureGroups[mk], idx)
		}
		for _, mk := range lectureKeys {
			idxs := lectureGroups[mk]
			group := make([]pending, len(idxs))
			for g, jIdx := range idxs {
				group[g] = joins[jIdx]
			}
			if win := w.chooseRecorder(mk, group, now); win >= 0 {
				winIdx := idxs[win]
				joins[winIdx].record = true
				w.promoteRecorder(ctx, mk, joins[winIdx])
				w.reserveRecorder(mk, joins[winIdx].key)
			}
		}
	}

	w.mu.Lock()
	w.lectureNow = needLecture
	w.mu.Unlock()

	// Тест — отдельная сущность (settings.test_join). Пара важнее:
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

	w.forget(ctx, validLessons, validMeetings)

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
	u       model.User
	lesson  model.Lesson
	url     string
	key     string
	meetKey string
	intent  *model.JoinIntent
	record  bool
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
	if w.Store != nil {
		_ = w.Store.RequestRaspRefresh()
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
		"Нет ссылки на комнату «%s» (%s). Добавь её в панели: «Ещё → Комнаты BBB» — запомню для этого предмета.",
		lesson.Discipline, lesson.SlotLabel(),
	))
}

// chooseRecorder: одна пишущая сессия на встречу. Живой рекордер среди тех,
// кто сейчас должен быть в комнате, остаётся; иначе выбираем того, кто зашёл
// раньше (сначала уже сидящих в комнате по seatedAt, затем по EnterAt),
// при равенстве — детерминированный тай-брейк по TelegramID и lesson.ID.
func (w *Worker) chooseRecorder(meetKey string, here []pending, now time.Time) int {
	if len(here) == 0 {
		return -1
	}
	w.mu.Lock()
	cur := w.recorder[meetKey]
	_, curLive := w.sessions[cur]
	rj := w.recJoining[meetKey]
	_, rjJoining := w.joining[rj]
	failAt, failed := w.recFailAt[meetKey]
	fails := w.recFailN[meetKey]
	live := make([]bool, len(here))
	seated := make([]time.Time, len(here))
	for i := range here {
		_, live[i] = w.sessions[here[i].key]
		seated[i] = w.seatedAt[here[i].key]
	}
	w.mu.Unlock()
	if cur != "" && curLive {
		for i := range here {
			if here[i].key == cur {
				return i
			}
		}
	}
	// Пока выбранный рекордер заходит или запускает запись, не отдаём запись
	// другому кандидату на повторных тиках.
	if rj != "" && rjJoining {
		for i := range here {
			if here[i].key == rj {
				return i
			}
		}
		return -1
	}
	// Каждая новая попытка — перезаход сессии на глазах у преподавателя.
	// Запись не стартует раз за разом — сидим до конца пары просто присутствием.
	if fails >= maxRecFails {
		return -1
	}
	if failed && now.Sub(failAt) < recRetryGap {
		return -1
	}
	enter := make([]time.Time, len(here))
	for i := range here {
		enter[i] = EnterAt(here[i].lesson.Begin, here[i].intent)
	}
	better := func(i, j int) bool {
		if live[i] != live[j] {
			return live[i]
		}
		if live[i] {
			si, sj := !seated[i].IsZero(), !seated[j].IsZero()
			if si != sj {
				return si
			}
			if si && !seated[i].Equal(seated[j]) {
				return seated[i].Before(seated[j])
			}
		}
		if !enter[i].Equal(enter[j]) {
			return enter[i].Before(enter[j])
		}
		if here[i].u.TelegramID != here[j].u.TelegramID {
			return here[i].u.TelegramID < here[j].u.TelegramID
		}
		return here[i].lesson.ID < here[j].lesson.ID
	}
	best := 0
	for i := 1; i < len(here); i++ {
		if better(i, best) {
			best = i
		}
	}
	return best
}

// promoteRecorder: выбранный рекордер сидит без звука (presence) — переоткрываем
// его сессию с записью. Если он уже пишет, ничего не делаем.
func (w *Worker) promoteRecorder(ctx context.Context, meetKey string, p pending) {
	w.mu.Lock()
	_, live := w.sessions[p.key]
	isRec := w.recorder[meetKey] == p.key
	w.mu.Unlock()
	if !live || isRec {
		return
	}
	logx.Infof("bbb", "recorder handover meet=%s lesson=%d -> %s", meetKey, p.lesson.ID, p.key)
	w.closeSession(ctx, p.u.TelegramID, p.lesson.ID, p.key, "recorder handover", false)
}

func (w *Worker) reserveRecorder(meetKey, key string) {
	if w == nil || meetKey == "" || key == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	// Если этот ключ уже является живым рекордером, ensureIn уйдёт в watchLobby
	// без beginJoin/endJoin, поэтому резерв в recJoining не нужен.
	if w.recorder[meetKey] == key {
		if _, live := w.sessions[key]; live {
			return
		}
	}
	if w.recJoining == nil {
		w.recJoining = make(map[string]string)
	}
	w.recJoining[meetKey] = key
}

func (w *Worker) clearRecJoiningIfIdle(meetKey, key string) {
	if w == nil || meetKey == "" || key == "" {
		return
	}
	w.mu.Lock()
	if w.recJoining[meetKey] == key {
		if _, joining := w.joining[key]; !joining {
			delete(w.recJoining, meetKey)
		}
	}
	w.mu.Unlock()
}

func (w *Worker) isRecorder(meetKey, key string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.recorder[meetKey] == key
}

func (w *Worker) isRecorderSession(key string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, rk := range w.recorder {
		if rk == key {
			return true
		}
	}
	return false
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
	meetKey := meetingGroupKey(lesson, url)
	if ctx.Err() != nil {
		if record {
			w.clearRecJoiningIfIdle(meetKey, key)
		}
		return
	}
	w.mu.Lock()
	_, live := w.sessions[key]
	_, blocked := w.blockedAt[key]
	w.mu.Unlock()
	if blocked {
		logx.Debugf("bbb", "ensureIn: key=%s blocked", key)
		if record {
			w.clearRecJoiningIfIdle(meetKey, key)
		}
		return
	}
	if live {
		if record {
			w.clearRecJoiningIfIdle(meetKey, key)
		}
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
	logx.Infof("bbb", "join: %s key=%s tg=%d роль=%s гость=%q url=%s", lessonTag(lesson), key, u.TelegramID, role, u.FIO, redactURL(url))
	joinStart := time.Now()
	sess, err := w.Joiner.Join(ctx, JoinReq{URL: url, FIO: u.FIO, Role: role})
	if err != nil {
		if ctx.Err() != nil {
			// Остановка процесса посреди захода — не неудача захода.
			logx.Infof("bbb", "ensureIn: join %s key=%s отменён после %s", lessonTag(lesson), key, time.Since(joinStart).Round(time.Millisecond))
			return
		}
		logx.Errorf("bbb", "ensureIn: join %s key=%s не вошёл за %s: %v", lessonTag(lesson), key, time.Since(joinStart).Round(time.Millisecond), err)
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
	shouldRecord := false
	if record {
		w.mu.Lock()
		cur := w.recorder[meetKey]
		_, curLive := w.sessions[cur]
		rj := w.recJoining[meetKey]
		_, rjJoining := w.joining[rj]
		if (cur != "" && cur != key && curLive) || (rj != "" && rj != key && rjJoining) {
			logx.Warnf("bbb", "ensureIn: meet=%s рекордер уже активен/заходит (rec=%q joining=%q), пропускаю attachRecorder для %s", meetKey, cur, rj, key)
			role = RolePresence
		} else {
			if w.recJoining == nil {
				w.recJoining = make(map[string]string)
			}
			w.recJoining[meetKey] = key
			shouldRecord = true
		}
		w.mu.Unlock()
	}
	if shouldRecord {
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
	if shouldRecord {
		if recording {
			w.recorder[meetKey] = key
			delete(w.recFailAt, meetKey)
			delete(w.recFailN, meetKey)
		} else {
			w.recFailAt[meetKey] = now
			w.recFailN[meetKey]++
			recGaveUp = w.recFailN[meetKey] == maxRecFails
		}
		if w.recJoining[meetKey] == key {
			delete(w.recJoining, meetKey)
		}
	}
	if state == model.PresenceLobby {
		w.lobbyAt[key] = now
	}
	if w.seatedAt == nil {
		w.seatedAt = make(map[string]time.Time)
	}
	w.seatedAt[key] = time.Now()
	w.mu.Unlock()
	logx.Infof("bbb", "seated: %s key=%s tg=%d состояние=%s (%s) роль=%s запись=%v вход=%s",
		lessonTag(lesson), key, u.TelegramID, state, seatName(lobby, room, false), role, recording, time.Since(joinStart).Round(time.Millisecond))
	if recGaveUp {
		logx.Warnf("bbb", "meet=%s lesson=%d: запись не стартовала %d раз — больше не пробую, пара без записи", meetKey, lesson.ID, maxRecFails)
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
	next := "следующая попытка через " + (joinRetryBase * time.Duration(1<<(n-1))).String()
	if stop {
		next = "стоп до «Заходить» в панели"
	}
	logx.Warnf("bbb", "join fail: %s key=%s tg=%d попытка %d/%d: %s (%s)", lessonTag(lesson), key, u.TelegramID, n, maxJoinFails, reason, next)
	switch {
	case n == 1:
		notify.User(ctx, w.Cfg, u.TelegramID, fmt.Sprintf("Не получилось зайти на «%s» — пробую ещё раз.", lesson.Discipline))
	case stop:
		notify.User(ctx, w.Cfg, u.TelegramID, fmt.Sprintf(
			"Не зашёл на «%s» после %d попыток. Переключи у пары в панели «Не заходить» и обратно «Заходить» — попробую снова.",
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
		// Кратковременный сбой WS/сети — ещё не вылет.
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
		logx.Infof("bbb", "lobby → комната: %s key=%s tg=%d, в лобби был %s", lessonTag(lesson), key, u.TelegramID, time.Since(since).Round(time.Second))
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
	logx.Warnf("bbb", "lobby wait: %s key=%s tg=%d гость=%q, в лобби %s (с начала пары %s)", lessonTag(lesson), key, u.TelegramID, u.FIO,
		now.Sub(since).Round(time.Second), now.Sub(lesson.Begin).Round(time.Second))
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

// audioAlertAfter — сколько тиков подряд пишущая сессия не в аудио до алерта.
const audioAlertAfter = 4

// checkAudio: пишущая сессия в комнате должна быть в аудио — иначе пишем тишину.
// Каждый тик короткая попытка подключить, через ~минуту — одно сообщение админу.
func (w *Worker) checkAudio(ctx context.Context, lesson model.Lesson, key string, sess Session) {
	if !w.isRecorderSession(key) {
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
		notify.Admin(ctx, w.Cfg, fmt.Sprintf("запись «%s»: сессия в комнате, но не подключилась к звуку — запись может быть пустой", lesson.Discipline))
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

// closeSession закрывает сессию (с записью — склейка сегментов) и снимает
// presence этой пары. event=false — служебное закрытие (вылет, передача записи).
func (w *Worker) closeSession(ctx context.Context, telegramID, lessonID int64, key, reason string, event bool) {
	logx.Debugf("bbb", "closeSession: key=%s tg=%d lesson=%d reason=%q", key, telegramID, lessonID, reason)
	w.mu.Lock()
	sess, ok := w.sessions[key]
	seatedAt := w.seatedAt[key]
	delete(w.seatedAt, key)
	delete(w.sessions, key)
	delete(w.lobbyAt, key)
	delete(w.lobbyAlertAt, key)
	delete(w.probeErr, key)
	delete(w.audioMiss, key)
	for mk, rk := range w.recorder {
		if rk == key {
			delete(w.recorder, mk)
		}
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
	var inRoom time.Duration
	if !seatedAt.IsZero() {
		inRoom = time.Since(seatedAt).Round(time.Second)
	}
	logx.Infof("bbb", "left: lesson=%d key=%s tg=%d причина=%q, сидели %s, событие=%v", lessonID, key, telegramID, reason, inRoom, event)
}

// forget чистит состояние ключей пар, ушедших из окна входа, и снимает их
// залипшие presence (ошибки «нет ссылки» / «не зашёл»).
func (w *Worker) forget(ctx context.Context, valid map[int64]struct{}, validMeetings map[string]struct{}) {
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
	for mk := range w.recFailAt {
		if _, ok := validMeetings[mk]; !ok {
			delete(w.recFailAt, mk)
			delete(w.recFailN, mk)
		}
	}
	for mk := range w.recFailN {
		if _, ok := validMeetings[mk]; !ok {
			delete(w.recFailN, mk)
		}
	}
	for mk, key := range w.recorder {
		if _, live := w.sessions[key]; !live {
			delete(w.recorder, mk)
		}
	}
	for mk, key := range w.recJoining {
		if _, joining := w.joining[key]; !joining {
			if _, ok := validMeetings[mk]; !ok {
				delete(w.recJoining, mk)
			}
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
	for mk, rk := range w.recJoining {
		if rk == key {
			delete(w.recJoining, mk)
		}
	}
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
