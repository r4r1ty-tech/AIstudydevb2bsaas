package webapp

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

// Демо-режим (PANEL_DEMO=1): та же панель и те же обработчики, но вместо sqlite —
// demoSource в памяти. Переключатели, режим дня, профиль и вход в Moodle меняют
// это состояние; после перезапуска всё возвращается к исходному.

// Демо не читает реальные ID из конфига: все идентификаторы синтетические.
const (
	demoAdminID   int64 = 700000001
	demoGroupID   int64 = 100000000
	demoGroupCode       = "0000-000000D"
)

var demoWhitelist = []int64{demoAdminID, 700000002, 700000003, 700000004, 700000005}

// sanitizeDemoConfig обнуляет все реальные API-ключи, токены и пути к БД
// в копии конфига для демо-режима.
func sanitizeDemoConfig(c *config.Config) {
	if c == nil {
		return
	}
	c.BotToken = ""
	c.PanelPassword = ""
	c.LLMAPIKey = ""
	c.LLMAPIURL = ""
	c.LLMModel = ""
	c.GeminiAPIKeys = nil
	c.OpenCodeAPIKey = ""
	c.OpenCodeAPIKeys = nil
	c.AssemblyAPIKey = ""
	c.DeepgramAPIKey = ""
	c.FishStudioAPIKey = ""
	c.GrokAPIKey = ""
	c.GroqAPIKey = ""
	c.GitHubToken = ""
	c.SecretKey = ""
	c.SecretKeyFile = ""
	c.TavilyAPIKey = ""
	c.ExaAPIKey = ""
	c.SearXNGURL = ""
	c.DBPath = ""
}

// NewDemo собирает панель на демо-данных. Telegram, база и ключи не нужны.
func NewDemo(cfg *config.Config) *Server {
	c := config.Config{ListenAddr: config.DefaultListen, Timezone: config.DefaultTimezone}
	if cfg != nil {
		c = *cfg
	}
	sanitizeDemoConfig(&c)
	c.AdminID = demoAdminID
	c.Whitelist = append([]int64(nil), demoWhitelist...)
	c.PanelDemo = true
	c.GroupID = demoGroupID
	c.GroupCode = demoGroupCode
	if c.GitHubOwner == "" {
		c.GitHubOwner = "r4r1ty-tech"
	}
	if c.GitHubRepo == "" {
		c.GitHubRepo = "LectionsSSAU"
	}
	loc := loadLocation(c.Timezone)
	clock := demoClock(loc, time.Now())
	d := newDemoSource(loc, clock)
	logx.Infof("webapp", "demo: панель на тестовых данных, часы сдвинуты на %s", time.Until(clock()).Round(time.Minute))
	return buildServer(&c, d, d.moodle, d.refresh, "", clock, true)
}

// demoClock — живые часы демо. Если сейчас ночь или вечер, время дня сдвигается
// в 09:50–12:40, чтобы на экране «Сегодня» всегда шла пара. Дата не меняется.
func demoClock(loc *time.Location, start time.Time) func() time.Time {
	start = start.In(loc)
	midnight := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, loc)
	tod := start.Sub(midnight)
	lo, hi := 9*time.Hour+50*time.Minute, 12*time.Hour+40*time.Minute
	target := tod
	if target < lo {
		target = lo
	}
	if target > hi {
		target = hi
	}
	offset := target - tod
	return func() time.Time { return time.Now().Add(offset).In(loc) }
}

type demoSource struct {
	mu    sync.Mutex
	loc   *time.Location
	clock func() time.Time

	users    map[int64]model.User
	lessons  []model.Lesson
	links    map[string]model.BBBLink
	intents  map[[2]int64]model.JoinIntent
	presence map[int64]model.Presence
	events   []model.Event
	test     model.TestJoin
	packs    []model.LecturePack
	topics   map[int64]string
	sizes    map[int64]int64
	run      model.ParseRun
	nextID   int64

	moodle *demoMoodle
}

var _ Source = (*demoSource)(nil)
var _ packDetails = (*demoSource)(nil)

func (d *demoSource) lock() func() {
	d.mu.Lock()
	return d.mu.Unlock
}

// ---------- пользователи ----------

func (d *demoSource) ListUsers() ([]model.User, error) {
	defer d.lock()()
	out := make([]model.User, 0, len(d.users))
	for _, u := range d.users {
		out = append(out, copyUser(u))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TelegramID < out[j].TelegramID })
	return out, nil
}

func (d *demoSource) GetUser(id int64) (*model.User, error) {
	defer d.lock()()
	u, ok := d.users[id]
	if !ok {
		return nil, nil
	}
	c := copyUser(u)
	return &c, nil
}

func (d *demoSource) UpsertUser(u *model.User) error {
	defer d.lock()()
	if u == nil {
		return nil
	}
	c := copyUser(*u)
	if c.CreatedAt.IsZero() {
		c.CreatedAt = d.clock().UTC()
	}
	d.users[c.TelegramID] = c
	return nil
}

func (d *demoSource) update(id int64, fn func(u *model.User)) error {
	defer d.lock()()
	u, ok := d.users[id]
	if !ok {
		return sql.ErrNoRows
	}
	fn(&u)
	d.users[id] = u
	return nil
}

func (d *demoSource) SetEnabled(id int64, on bool) error {
	return d.update(id, func(u *model.User) { u.Enabled = on })
}

func (d *demoSource) SetDisabledUntil(id int64, until *time.Time) error {
	return d.update(id, func(u *model.User) {
		if until == nil {
			u.DisabledUntil = nil
			return
		}
		t := *until
		u.DisabledUntil = &t
	})
}

func (d *demoSource) SetFIO(id int64, fio string) error {
	return d.update(id, func(u *model.User) { u.FIO = strings.TrimSpace(fio) })
}

func (d *demoSource) SetSubgroup(id int64, n int) error {
	return d.update(id, func(u *model.User) { u.Subgroup = n })
}

func (d *demoSource) SetExtraWords(id int64, words []string) error {
	return d.update(id, func(u *model.User) { u.ExtraWords = append([]string{}, words...) })
}

func copyUser(u model.User) model.User {
	if u.DisabledUntil != nil {
		t := *u.DisabledUntil
		u.DisabledUntil = &t
	}
	u.ExtraWords = append([]string{}, u.ExtraWords...)
	return u
}

// ---------- пары ----------

func (d *demoSource) ListLessons() ([]model.Lesson, error) {
	defer d.lock()()
	return append([]model.Lesson{}, d.lessons...), nil
}

func (d *demoSource) lessonByID(id int64) *model.Lesson {
	for i := range d.lessons {
		if d.lessons[i].ID == id {
			l := d.lessons[i]
			return &l
		}
	}
	return nil
}

func (d *demoSource) LessonByID(id int64) (*model.Lesson, error) {
	defer d.lock()()
	return d.lessonByID(id), nil
}

func (d *demoSource) CurrentLesson(now time.Time) (*model.Lesson, error) {
	defer d.lock()()
	for _, l := range d.lessons {
		if l.Online && !l.Begin.After(now) && l.Finish.After(now) {
			c := l
			return &c, nil
		}
	}
	return nil, nil
}

func (d *demoSource) NextLesson(now time.Time) (*model.Lesson, error) {
	defer d.lock()()
	for _, l := range d.lessons {
		if l.Online && l.Begin.After(now) {
			c := l
			return &c, nil
		}
	}
	return nil, nil
}

func (d *demoSource) LastParseRun() (*model.ParseRun, error) {
	defer d.lock()()
	r := d.run
	return &r, nil
}

// refresh — «обновить расписание»: в демо данные не меняются, только отметка прогона.
func (d *demoSource) refresh(ctx context.Context) (model.ParseRun, error) {
	select {
	case <-ctx.Done():
		return model.ParseRun{}, ctx.Err()
	case <-time.After(1200 * time.Millisecond):
	}
	defer d.lock()()
	now := d.clock().UTC()
	d.nextID++
	d.run.ID = d.nextID
	d.run.At = now
	d.run.OK = true
	d.run.Status = "ok"
	d.run.Diff = "Изменений нет."
	d.addEvent(model.Event{At: now, Type: model.EventReparse, Message: "расписание обновлено вручную: изменений нет"})
	return d.run, nil
}

// ---------- ссылки BBB ----------

func (d *demoSource) GetLessonBBB(lessonID int64) string {
	defer d.lock()()
	if b, ok := d.links[model.BBBLessonKey(lessonID)]; ok {
		return strings.TrimSpace(b.URL)
	}
	l := d.lessonByID(lessonID)
	if l == nil {
		return ""
	}
	u, _ := model.ResolveBBB(*l, d.linkList())
	return u
}

func (d *demoSource) SetLessonBBB(lessonID int64, url string) error {
	defer d.lock()()
	url = strings.TrimSpace(url)
	now := d.clock().UTC()
	d.links[model.BBBLessonKey(lessonID)] = model.BBBLink{Key: model.BBBLessonKey(lessonID), URL: url, UpdatedAt: now}
	if url == "" {
		return nil
	}
	if l := d.lessonByID(lessonID); l != nil {
		key := model.BBBRoomKey(l.Discipline, l.Teacher, l.Type)
		d.links[key] = model.BBBLink{Key: key, URL: url, UpdatedAt: now}
	}
	return nil
}

func (d *demoSource) GetBBB(key string) (*model.BBBLink, error) {
	defer d.lock()()
	b, ok := d.links[key]
	if !ok {
		return nil, nil
	}
	return &b, nil
}

func (d *demoSource) SetBBB(key, url string) error {
	defer d.lock()()
	d.links[key] = model.BBBLink{Key: key, URL: strings.TrimSpace(url), UpdatedAt: d.clock().UTC()}
	return nil
}

func (d *demoSource) ListBBB() ([]model.BBBLink, error) {
	defer d.lock()()
	return d.linkList(), nil
}

func (d *demoSource) linkList() []model.BBBLink {
	out := make([]model.BBBLink, 0, len(d.links))
	for _, b := range d.links {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// ---------- решения и присутствие ----------

func (d *demoSource) GetIntent(tg, lessonID int64) (*model.JoinIntent, error) {
	defer d.lock()()
	in, ok := d.intents[[2]int64{tg, lessonID}]
	if !ok {
		return nil, nil
	}
	return &in, nil
}

// PutIntent заодно изображает воркер: «не заходить» на идущей паре — бот выходит,
// вернули «заходить» — снова в комнате.
func (d *demoSource) PutIntent(in model.JoinIntent) error {
	defer d.lock()()
	d.intents[[2]int64{in.TelegramID, in.LessonID}] = in
	l := d.lessonByID(in.LessonID)
	now := d.clock()
	if l == nil || !l.Online || l.Begin.After(now) || !l.Finish.After(now) {
		return nil
	}
	p := d.presence[in.TelegramID]
	switch {
	case in.Decision == model.JoinNo && p.LessonID == l.ID && p.State != model.PresenceNone:
		d.presence[in.TelegramID] = model.Presence{TelegramID: in.TelegramID, LessonID: l.ID, State: model.PresenceNone, Message: "вышел: вы отказались от пары", UpdatedAt: now.UTC()}
		d.addEvent(model.Event{At: now.UTC(), Type: model.EventLeave, TelegramID: in.TelegramID, LessonID: l.ID, Message: "вышел по решению из панели"})
	case in.Decision != model.JoinNo && (p.LessonID != l.ID || p.State == model.PresenceNone):
		d.presence[in.TelegramID] = model.Presence{TelegramID: in.TelegramID, LessonID: l.ID, State: model.PresenceRoom, Message: "в комнате", UpdatedAt: now.UTC()}
		d.addEvent(model.Event{At: now.UTC(), Type: model.EventJoin, TelegramID: in.TelegramID, LessonID: l.ID, Message: "зашёл: " + l.Discipline})
	}
	return nil
}

func (d *demoSource) ListPresence() ([]model.Presence, error) {
	defer d.lock()()
	out := make([]model.Presence, 0, len(d.presence))
	for _, p := range d.presence {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TelegramID < out[j].TelegramID })
	return out, nil
}

func (d *demoSource) addEvent(e model.Event) {
	d.nextID++
	e.ID = d.nextID
	d.events = append(d.events, e)
}

func (d *demoSource) ListEvents(limit int) ([]model.Event, error) {
	defer d.lock()()
	out := append([]model.Event{}, d.events...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ---------- тестовый заход ----------

// GetTestJoin изображает воркер: через пару секунд после «захожу» бот в комнате.
func (d *demoSource) GetTestJoin() (model.TestJoin, error) {
	defer d.lock()()
	now := d.clock().UTC()
	if d.test.Status == model.TestJoining && now.Sub(d.test.UpdatedAt) > 3*time.Second {
		d.test.Status = model.TestRoom
		d.test.Mode = d.test.Want
		d.test.Message = "в комнате"
		d.test.UpdatedAt = now
	}
	return d.test, nil
}

func (d *demoSource) PutTestJoin(j model.TestJoin) error {
	defer d.lock()()
	j.UpdatedAt = d.clock().UTC()
	d.test = j
	return nil
}

// ---------- паки ----------

func (d *demoSource) ListPacks() ([]model.LecturePack, error) {
	defer d.lock()()
	return append([]model.LecturePack{}, d.packs...), nil
}

func (d *demoSource) AnyRecording() (bool, error) {
	defer d.lock()()
	for _, p := range d.packs {
		if p.Status == model.PackRecording {
			return true, nil
		}
	}
	return false, nil
}

func (d *demoSource) PackSize(p model.LecturePack) int64 {
	defer d.lock()()
	return d.sizes[p.ID]
}

func (d *demoSource) PackTopic(p model.LecturePack) string {
	defer d.lock()()
	return d.topics[p.ID]
}

// ---------- Moodle ----------

type demoMoodle struct {
	mu        sync.Mutex
	clock     func() time.Time
	connected bool
	login     string
	syncedAt  time.Time
	list      []Deadline
}

func (m *demoMoodle) Status() MoodleStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := MoodleStatus{Connected: m.connected, Available: true}
	if m.connected {
		st.Login = m.login
		t := m.syncedAt
		st.SyncedAt = &t
	} else {
		st.Message = "Moodle не подключён."
	}
	return st
}

func (m *demoMoodle) Deadlines(context.Context, time.Time) ([]Deadline, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.connected {
		return []Deadline{}, nil
	}
	return append([]Deadline{}, m.list...), nil
}

func (m *demoMoodle) Login(ctx context.Context, login, password string) (MoodleStatus, error) {
	select {
	case <-ctx.Done():
		return m.Status(), ctx.Err()
	case <-time.After(700 * time.Millisecond):
	}
	if len([]rune(password)) < 4 {
		return m.Status(), ErrMoodleBadLogin
	}
	m.mu.Lock()
	m.connected = true
	m.login = login
	m.syncedAt = m.clock().UTC()
	m.mu.Unlock()
	return m.Status(), nil
}

func (m *demoMoodle) Logout() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connected = false
	m.login = ""
	return nil
}
