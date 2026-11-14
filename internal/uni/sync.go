// Package uni синхронизирует данные универа по сохранённым аккаунтам:
// расписание из ЛК, задания и тесты из Moodle.
// Работает в процессе rasp; бот и панель читают результат из sqlite.
package uni

import (
	"context"
	"errors"
	"fmt"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/websearch"
	"html"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/lk"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodle"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/vault"
)

// ErrNoAccount — нет ни одного рабочего аккаунта ЛК: расписание берём с сайта.
var ErrNoAccount = errors.New("uni: no lk account")

var (
	tick          = time.Minute
	kickTick      = 5 * time.Second
	moodleEvery   = 30 * time.Minute
	moodleFull    = 6 * time.Hour
	retryAfterErr = 10 * time.Minute
	pageDelay     = 150 * time.Millisecond
	nowFn         = time.Now
)

type Syncer struct {
	Store   *store.Store
	Vault   *vault.Vault
	Loc     *time.Location
	GroupID int64 // группа бота; у ЛК её id тот же, что на ssau.ru/rasp
	AdminID int64
	// Notify шлёт пользователю HTML-сообщение в Telegram.
	Notify func(ctx context.Context, telegramID int64, text string)

	LKBase     string // для тестов
	MoodleBase string

	Cfg *config.Config
	// Web — веб-поиск решателя тестов; nil — собрать из Cfg.
	Web *websearch.Searcher
	// DescribeImage — текст рисунка вопроса (для тестов); nil — Moodle + vision.
	DescribeImage func(ctx context.Context, src string) (string, error)

	mu       sync.Mutex
	lastFull map[int64]time.Time
}

func (s *Syncer) now() time.Time { return nowFn().In(s.loc()) }

func (s *Syncer) loc() *time.Location {
	if s.Loc != nil {
		return s.Loc
	}
	return time.UTC
}

func (s *Syncer) notify(ctx context.Context, id int64, text string) {
	if s.Notify != nil {
		s.Notify(ctx, id, text)
	}
}

// ---------- сессии ----------

// session поднимает сохранённые куки, а если они протухли — входит по паролю.
// После любого исхода пишет статус аккаунта.
type authClient interface {
	Session() string
	Restore(string)
	Login(ctx context.Context, login, password string) error
}

func (s *Syncer) withSession(ctx context.Context, acc model.Account, c authClient, badLogin, unauthorized error, fn func() error) error {
	if s.Vault == nil {
		return errors.New("uni: no vault")
	}
	if raw, err := s.Vault.Open(acc.Session); err == nil {
		c.Restore(raw)
	}
	login := func() error {
		pass, err := s.Vault.Open(acc.Secret)
		if err != nil {
			return fmt.Errorf("uni: decrypt password: %w", err)
		}
		return c.Login(ctx, acc.Login, pass)
	}
	err := errors.New("no session")
	if acc.Session != "" {
		err = fn()
	}
	if acc.Session == "" || errors.Is(err, unauthorized) {
		logx.Infof("uni", "%s tg=%d: login", acc.Kind, acc.TelegramID)
		if err = login(); err == nil {
			err = fn()
		}
	}
	if sess, serr := s.Vault.Seal(c.Session()); serr == nil && sess != "" {
		if perr := s.Store.SetAccountSession(acc.TelegramID, acc.Kind, sess); perr != nil {
			logx.Warnf("uni", "save session: %v", perr)
		}
	}
	switch {
	case err == nil:
		s.setStatus(acc, model.AccountOK, "")
	case errors.Is(err, badLogin):
		s.setStatus(acc, model.AccountBadLogin, "Сайт не принял логин или пароль")
	case ctx.Err() != nil:
	default:
		s.setStatus(acc, model.AccountError, shortErr(err))
	}
	return err
}

func (s *Syncer) setStatus(acc model.Account, status, msg string) {
	if err := s.Store.SetAccountStatus(acc.TelegramID, acc.Kind, status, msg); err != nil {
		logx.Warnf("uni", "set status: %v", err)
	}
}

func shortErr(err error) string {
	m := err.Error()
	if r := []rune(m); len(r) > 200 {
		m = string(r[:200])
	}
	return m
}

func (s *Syncer) usable(kind string) ([]model.Account, error) {
	all, err := s.Store.ListAccounts(kind)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, a := range all {
		if a.Status != model.AccountBadLogin {
			out = append(out, a)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TelegramID == s.AdminID && out[j].TelegramID != s.AdminID })
	return out, nil
}

// ---------- расписание ----------

// Lessons — расписание группы на эту и следующую неделю из ЛК плюс пары ИОТ
// каждого, кто подключил ЛК. Для rasp.Refresher.Source.
func (s *Syncer) Lessons(ctx context.Context) ([]model.Lesson, error) {
	accs, err := s.usable(model.AccountLK)
	if err != nil {
		return nil, err
	}
	if len(accs) == 0 {
		return nil, ErrNoAccount
	}
	var out []model.Lesson
	var firstErr error
	group := false
	for _, acc := range accs {
		ls, err := s.accountLessons(ctx, acc, !group)
		if err != nil {
			logx.Warnf("uni", "lessons tg=%d: %v", acc.TelegramID, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if !group {
			group = true
			out = append(out, ls...)
			continue
		}
		for _, l := range ls { // у остальных берём только их ИОТ
			if l.Owner != 0 {
				out = append(out, l)
			}
		}
	}
	if !group {
		return nil, firstErr
	}
	return out, nil
}

func (s *Syncer) lkClient() (*lk.Client, error) { return lk.New(s.LKBase) }

func (s *Syncer) accountLessons(ctx context.Context, acc model.Account, withGroup bool) ([]model.Lesson, error) {
	c, err := s.lkClient()
	if err != nil {
		return nil, err
	}
	var out []model.Lesson
	err = s.withSession(ctx, acc, c, lk.ErrBadLogin, lk.ErrUnauthorized, func() error {
		out = nil
		groups, err := c.Groups(ctx)
		if err != nil {
			return err
		}
		if len(groups) == 0 {
			return errors.New("в ЛК нет группы")
		}
		s.refreshLKName(ctx, c, acc.TelegramID)
		gid := groups[0].ID
		for _, g := range groups {
			if g.ID == s.GroupID {
				gid = g.ID
			}
		}
		years, err := c.Years(ctx)
		if err != nil {
			return err
		}
		now := s.now()
		yid, ok := lk.CurrentYearID(years, now)
		if !ok {
			return errors.New("в ЛК нет учебного года")
		}
		start, err := s.yearStart(ctx, c, yid, gid)
		if err != nil {
			return err
		}
		week, err := lk.WeekOf(start, now, s.loc())
		if err != nil {
			return err
		}
		for _, w := range []int{week, week + 1} {
			tt, err := c.Timetable(ctx, yid, w, gid)
			if err != nil {
				return err
			}
			ls, err := tt.ToLessons(w, gid, acc.TelegramID, s.loc())
			if err != nil {
				return err
			}
			for _, l := range ls {
				if withGroup || l.Owner != 0 {
					out = append(out, l)
				}
			}
		}
		return nil
	})
	return out, err
}

// LKNameKey — ФИО человека из ЛК (страница /account), по Telegram id.
func LKNameKey(telegramID int64) string { return "lk_name:" + strconv.FormatInt(telegramID, 10) }

func lkNameAtKey(telegramID int64) string { return "lk_name_at:" + strconv.FormatInt(telegramID, 10) }

// refreshLKName — раз в сутки берёт ФИО из ЛК: оно связывает человека в
// панели, боте и журналах. Пустое «имя для BBB» заполняется им же.
func (s *Syncer) refreshLKName(ctx context.Context, c *lk.Client, id int64) {
	now := s.now()
	if v, ok, _ := s.Store.GetSetting(lkNameAtKey(id)); ok && v != "" {
		if at, err := time.Parse(time.RFC3339, v); err == nil && now.Sub(at) < 24*time.Hour {
			return
		}
	}
	name, err := c.FullName(ctx)
	if err != nil {
		logx.Warnf("uni", "lk name tg=%d: %v", id, err)
		return
	}
	_ = s.Store.SetSetting(LKNameKey(id), name)
	_ = s.Store.SetSetting(lkNameAtKey(id), now.Format(time.RFC3339))
	if u, err := s.Store.GetUser(id); err == nil && u != nil && strings.TrimSpace(u.FIO) == "" {
		if err := s.Store.SetFIO(id, name); err != nil {
			logx.Warnf("uni", "lk name -> fio tg=%d: %v", id, err)
		}
	}
	logx.Infof("uni", "lk name tg=%d: %s", id, name)
}

// yearStart — дата начала учебного года (кэш в settings, чтобы не дёргать лишнюю неделю).
func (s *Syncer) yearStart(ctx context.Context, c *lk.Client, yearID, groupID int64) (string, error) {
	key := "lk_year_start:" + strconv.FormatInt(yearID, 10)
	if v, ok, err := s.Store.GetSetting(key); err == nil && ok && v != "" {
		return v, nil
	}
	tt, err := c.Timetable(ctx, yearID, 1, groupID)
	if err != nil {
		return "", err
	}
	start := tt.CurrentYear.StartDate
	if start == "" {
		return "", errors.New("ЛК не отдал начало года")
	}
	if err := s.Store.SetSetting(key, start); err != nil {
		logx.Warnf("uni", "cache year start: %v", err)
	}
	return start, nil
}

// ---------- Moodle ----------

func (s *Syncer) moodleClient() (*moodle.Client, error) { return moodle.New(s.MoodleBase) }

// SyncMoodle обновляет задания пользователя. full — обойти все курсы целиком;
// иначе перепроверить только ленту сроков и несданное с близким сроком.
func (s *Syncer) SyncMoodle(ctx context.Context, acc model.Account, full bool) error {
	c, err := s.moodleClient()
	if err != nil {
		return err
	}
	prev, err := s.Store.ListMoodleItems(acc.TelegramID)
	if err != nil {
		return err
	}
	if len(prev) == 0 {
		full = true
	}
	var items []model.MoodleItem
	err = s.withSession(ctx, acc, c, moodle.ErrBadLogin, moodle.ErrUnauthorized, func() error {
		var err error
		items, err = s.collectMoodle(ctx, c, prev, full)
		if err != nil {
			return err
		}
		s.detectAndHandleOpenedQuizzes(ctx, acc, c, prev, items)
		return nil
	})
	if err != nil {
		return err
	}
	if err := s.Store.ReplaceMoodleItems(acc.TelegramID, items); err != nil {
		return err
	}
	if full {
		s.mu.Lock()
		if s.lastFull == nil {
			s.lastFull = map[int64]time.Time{}
		}
		s.lastFull[acc.TelegramID] = s.now()
		s.mu.Unlock()
	}
	if len(prev) > 0 {
		s.announceNew(ctx, acc.TelegramID, prev, items)
	}
	logx.Infof("uni", "moodle tg=%d full=%v items=%d", acc.TelegramID, full, len(items))
	return nil
}

func (s *Syncer) collectMoodle(ctx context.Context, c *moodle.Client, prev []model.MoodleItem, full bool) ([]model.MoodleItem, error) {
	now := s.now()
	courses, err := c.Courses(ctx)
	if err != nil {
		return nil, err
	}
	courseName := map[int64]string{}
	for _, cr := range courses {
		courseName[cr.ID] = cr.FullName
	}
	events, err := c.Timeline(ctx, now.AddDate(0, 0, -14), time.Time{})
	if err != nil {
		return nil, err
	}

	prevBy := map[int64]model.MoodleItem{}
	for _, p := range prev {
		prevBy[p.CMID] = p
	}
	byCM := map[int64]model.MoodleItem{}
	var acts []moodle.Activity
	if full {
		for _, cr := range courses {
			list, err := c.CourseActivities(ctx, cr)
			if err != nil {
				if errors.Is(err, moodle.ErrUnauthorized) {
					return nil, err
				}
				logx.Warnf("uni", "course %d: %v", cr.ID, err)
				continue
			}
			acts = append(acts, list...)
			sleep(ctx, pageDelay)
		}
	} else {
		for _, p := range prev {
			byCM[p.CMID] = p
		}
	}
	// Лента: несданное с ближайшими сроками — добавляем то, чего не нашли в курсах.
	inTimeline := map[int64]moodle.Event{}
	known := map[int64]bool{}
	for _, a := range acts {
		known[a.CMID] = true
	}
	for _, p := range prev {
		if !full {
			known[p.CMID] = true
		}
	}
	for _, e := range events {
		cm := e.CMID()
		if cm == 0 {
			continue
		}
		inTimeline[cm] = e
		if !known[cm] {
			known[cm] = true
			course := e.Course.FullName
			if course == "" {
				course = courseName[e.Course.ID]
			}
			acts = append(acts, moodle.Activity{CMID: cm, CourseID: e.Course.ID, Course: course, Kind: e.ModuleName,
				Name: strings.TrimSpace(strings.TrimSuffix(e.Name, " закрывается")), URL: e.URL})
		}
	}
	if !full {
		// Перепроверяем: всё из ленты и несданное со сроком в окне −3…+14 дней.
		for _, p := range prev {
			_, inTL := inTimeline[p.CMID]
			near := p.Due != nil && p.Due.After(now.AddDate(0, 0, -3)) && p.Due.Before(now.AddDate(0, 0, 14))
			if inTL || (!p.Finished() && near) {
				acts = append(acts, moodle.Activity{CMID: p.CMID, CourseID: p.CourseID, Course: p.Course,
					Section: p.Section, Kind: p.Kind, Name: p.Title, URL: p.URL})
			}
		}
	}

	seen := map[int64]bool{}
	for _, a := range acts {
		if seen[a.CMID] {
			continue
		}
		seen[a.CMID] = true
		d, err := c.ActivityDetail(ctx, a, s.loc())
		if err != nil {
			if errors.Is(err, moodle.ErrUnauthorized) || ctx.Err() != nil {
				return nil, err
			}
			logx.Warnf("uni", "activity %d: %v", a.CMID, err)
			if old, ok := prevBy[a.CMID]; ok {
				byCM[a.CMID] = old // оставляем прежнее
			}
			continue
		}
		item := model.MoodleItem{
			CMID: a.CMID, CourseID: a.CourseID, Course: a.Course, Section: a.Section,
			Kind: a.Kind, Title: a.Name, URL: a.URL, Opens: d.Opens, Due: d.Due, Status: d.Status,
			Todo: strings.Join(d.Todo, "\n"),
		}
		if item.Course == "" {
			item.Course = courseName[a.CourseID]
		}
		if e, ok := inTimeline[a.CMID]; ok && item.Due == nil && e.TimeSort > 0 {
			t := time.Unix(e.TimeSort, 0).In(s.loc())
			item.Due = &t
		}
		if old, ok := prevBy[a.CMID]; ok && item.Section == "" {
			item.Section = old.Section
		}
		byCM[a.CMID] = item
		sleep(ctx, pageDelay)
	}
	out := make([]model.MoodleItem, 0, len(byCM))
	for _, it := range byCM {
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CMID < out[j].CMID })
	return out, nil
}

// announceNew — новое задание со сроком в будущем: короткое сообщение.
func (s *Syncer) announceNew(ctx context.Context, id int64, prev, cur []model.MoodleItem) {
	old := map[int64]bool{}
	for _, p := range prev {
		old[p.CMID] = true
	}
	now := s.now()
	n := 0
	for _, it := range cur {
		if it.Kind == model.MoodleQuiz {
			continue // открывшиеся тесты обрабатываются в detectAndHandleOpenedQuizzes
		}
		if old[it.CMID] || it.Finished() || it.Due == nil || it.Due.Before(now) {
			continue
		}
		if n++; n > 5 {
			break
		}
		s.notify(ctx, id, "🆕 <b>Новое в Moodle</b>\n"+FormatItem(it, now, s.loc()))
	}
}

// FormatItem — карточка задания для Telegram (HTML).
func FormatItem(it model.MoodleItem, now time.Time, loc *time.Location) string {
	var b strings.Builder
	b.WriteString("<b>" + html.EscapeString(it.Title) + "</b>")
	b.WriteString(" · " + KindLabel(it.Kind))
	b.WriteString("\n" + html.EscapeString(it.Course))
	if it.Due != nil {
		b.WriteString("\n⏰ до " + it.Due.In(loc).Format("02.01 15:04") + " (" + Until(*it.Due, now) + ")")
	}
	b.WriteString("\n" + StatusLabel(it.Status))
	if t := strings.TrimSpace(it.Todo); t != "" {
		lines := strings.Split(t, "\n")
		if len(lines) > 4 {
			lines = lines[:4]
		}
		for _, l := range lines {
			b.WriteString("\n• " + html.EscapeString(l))
		}
	}
	if it.URL != "" {
		b.WriteString("\n<a href=\"" + html.EscapeString(it.URL) + "\">Открыть в Moodle</a>")
	}
	return b.String()
}

func KindLabel(k string) string {
	switch k {
	case model.MoodleQuiz:
		return "тест"
	case model.MoodleAssign:
		return "задание"
	case model.MoodleLesson:
		return "лекция-урок"
	case model.MoodleWorkshop:
		return "семинар"
	}
	return k
}

func StatusLabel(s string) string {
	switch s {
	case model.MoodleDraft:
		return "✏️ черновик / попытка не завершена"
	case model.MoodleSubmitted:
		return "✅ отправлено"
	case model.MoodleDone:
		return "✅ выполнено"
	}
	return "❗ не сдано"
}

// Until — «через 2 д 3 ч» / «просрочено 5 ч».
func Until(due, now time.Time) string {
	d := due.Sub(now)
	prefix := "через "
	if d < 0 {
		prefix, d = "просрочено ", -d
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%s%d д %d ч", prefix, days, hours)
	case hours > 0:
		return fmt.Sprintf("%s%d ч %d мин", prefix, hours, mins)
	default:
		return fmt.Sprintf("%s%d мин", prefix, mins)
	}
}

func sleep(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
