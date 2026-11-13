package webapp

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

// Личная панель: «я» — пользователь с telegram id cfg.AdminID. Решения по парам
// живут в join_intents, как у кнопок T-15 в боте; воркер bbb читает их сам.

const (
	decisionYes  = "yes"
	decisionNo   = "no"
	decisionAuto = "auto" // в базе — pending или отсутствие записи: «молчание = заходить»

	dayModeAuto  = "auto"
	dayModeAll   = "all"
	dayModeNone  = "none"
	dayModeMixed = "mixed" // решения по парам разные
	dayModeEmpty = "empty" // онлайн-пар моей подгруппы нет

	phasePast  = "past"
	phaseNow   = "now"
	phaseNext  = "next"
	phaseLater = "later"

	kindLecture  = "lecture"
	kindPractice = "practice"
	kindLab      = "lab"
	kindOther    = "other"

	dateLayout = "2006-01-02"
)

type presenceView struct {
	State     string    `json:"state"`
	Message   string    `json:"message,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type lessonView struct {
	model.Lesson
	Kind      string        `json:"kind"`
	Mine      bool          `json:"mine"`
	Decision  string        `json:"decision"`
	WillJoin  bool          `json:"will_join"`
	HasBBB    bool          `json:"has_bbb"`
	BBBURL    string        `json:"bbb_url,omitempty"`
	Phase     string        `json:"phase"`
	Presence  *presenceView `json:"presence,omitempty"`
	Recording bool          `json:"recording"`
}

type meView struct {
	TelegramID    int64      `json:"telegram_id"`
	FIO           string     `json:"fio"`
	Subgroup      int        `json:"subgroup"`
	Enabled       bool       `json:"enabled"`
	DisabledUntil *time.Time `json:"disabled_until,omitempty"`
	Onboarded     bool       `json:"onboarded"`
	Active        bool       `json:"active"`
	Reason        string     `json:"reason,omitempty"`
	ExtraWords    []string   `json:"extra_words"`
	CommonWords   []string   `json:"common_words"`
	WakeWords     []string   `json:"wake_words"`
}

type dayResponse struct {
	Date      string       `json:"date"`
	Today     string       `json:"today"`
	Now       time.Time    `json:"now"`
	Mode      string       `json:"mode"`
	Lessons   []lessonView `json:"lessons"`
	CurrentID int64        `json:"current_id"`
	NextID    int64        `json:"next_id"`
	Upcoming  *lessonView  `json:"upcoming,omitempty"`
	Me        meView       `json:"me"`
	Demo      bool         `json:"demo"`
}

type weekDay struct {
	Date    string       `json:"date"`
	Mode    string       `json:"mode"`
	Lessons []lessonView `json:"lessons"`
}

type weekResponse struct {
	Today string    `json:"today"`
	Now   time.Time `json:"now"`
	Days  []weekDay `json:"days"`
	Me    meView    `json:"me"`
	Demo  bool      `json:"demo"`
}

type intentRequest struct {
	LessonID int64  `json:"lesson_id"`
	Decision string `json:"decision"`
}

type intentResponse struct {
	Lesson lessonView `json:"lesson"`
	Mode   string     `json:"mode"`
}

type dayModeRequest struct {
	Date string `json:"date"`
	Mode string `json:"mode"`
}

// mine — всё про «меня», что нужно для разметки пар; собирается раз на запрос.
type mine struct {
	now       time.Time
	user      *model.User
	me        meView
	links     []model.BBBLink
	presence  *model.Presence
	recording map[int64]bool
}

func (s *Server) myID() int64 {
	if s.cfg == nil {
		return 0
	}
	return s.cfg.AdminID
}

func (s *Server) loadMine() (*mine, error) {
	m := &mine{now: s.now(), recording: map[int64]bool{}}
	u, err := s.src.GetUser(s.myID())
	if err != nil {
		return nil, fmt.Errorf("loadMine: GetUser: %w", err)
	}
	m.user = u
	m.me = buildMe(s.myID(), u, m.now, s.loc)
	if m.links, err = s.src.ListBBB(); err != nil {
		return nil, fmt.Errorf("loadMine: ListBBB: %w", err)
	}
	pres, err := s.src.ListPresence()
	if err != nil {
		return nil, fmt.Errorf("loadMine: ListPresence: %w", err)
	}
	for i := range pres {
		if pres[i].TelegramID == s.myID() {
			p := pres[i]
			m.presence = &p
		}
	}
	packs, err := s.src.ListPacks()
	if err != nil {
		return nil, fmt.Errorf("loadMine: ListPacks: %w", err)
	}
	for _, p := range packs {
		if p.Status == model.PackRecording && p.LessonID > 0 {
			m.recording[p.LessonID] = true
		}
	}
	return m, nil
}

func buildMe(id int64, u *model.User, now time.Time, loc *time.Location) meView {
	me := meView{
		TelegramID:  id,
		ExtraWords:  []string{},
		CommonWords: append([]string{}, model.CommonWakeWords...),
		WakeWords:   append([]string{}, model.CommonWakeWords...),
	}
	if u == nil {
		me.Reason = "Профиль ещё не создан: впишите имя для BBB в разделе «Ещё»."
		return me
	}
	me.FIO = u.FIO
	me.Subgroup = u.Subgroup
	me.Enabled = u.Enabled
	me.Onboarded = u.Onboarded
	if u.DisabledUntil != nil && now.Before(*u.DisabledUntil) {
		d := *u.DisabledUntil
		me.DisabledUntil = &d
	}
	if u.ExtraWords != nil {
		me.ExtraWords = append([]string{}, u.ExtraWords...)
	}
	me.WakeWords = u.WakeList()
	me.Active = u.Active(now)
	switch {
	case me.Active:
	case !u.Enabled:
		me.Reason = "Бот выключен для вас и никуда не заходит."
	case me.DisabledUntil != nil:
		me.Reason = "Бот выключен до " + me.DisabledUntil.In(loc).Format("02.01 15:04") + "."
	case strings.TrimSpace(u.FIO) == "":
		me.Reason = "Не задано имя для BBB — без него бот не зайдёт."
	case !u.Onboarded:
		me.Reason = "Закончите настройку в Telegram-боте, тогда он начнёт заходить."
	}
	return me
}

func (m *mine) isMine(l model.Lesson) bool {
	if m.user == nil || m.user.Subgroup == 0 {
		return true
	}
	return l.MatchesSubgroup(m.user.Subgroup)
}

func lessonKind(typ string) string {
	t := strings.ToLower(strings.TrimSpace(typ))
	switch {
	case model.IsLecture(typ):
		return kindLecture
	case strings.Contains(t, "лаб") || strings.Contains(t, "lab"):
		return kindLab
	case strings.Contains(t, "практ") || strings.Contains(t, "семин") || strings.Contains(t, "practice"):
		return kindPractice
	default:
		return kindOther
	}
}

func decisionOf(in *model.JoinIntent) string {
	if in == nil {
		return decisionAuto
	}
	switch in.Decision {
	case model.JoinYes:
		return decisionYes
	case model.JoinNo:
		return decisionNo
	default:
		return decisionAuto
	}
}

func (s *Server) viewLesson(m *mine, l model.Lesson) (lessonView, error) {
	v := lessonView{Lesson: l, Kind: lessonKind(l.Type), Mine: m.isMine(l), Decision: decisionAuto}
	switch {
	case !l.Finish.After(m.now):
		v.Phase = phasePast
	case !l.Begin.After(m.now):
		v.Phase = phaseNow
	default:
		v.Phase = phaseLater
	}
	if l.Online {
		in, err := s.src.GetIntent(s.myID(), l.ID)
		if err != nil {
			return v, fmt.Errorf("viewLesson: GetIntent %d: %w", l.ID, err)
		}
		v.Decision = decisionOf(in)
		v.BBBURL, _ = model.ResolveBBB(l, m.links)
		v.HasBBB = v.BBBURL != ""
		v.WillJoin = v.Mine && m.me.Active && v.Decision != decisionNo && v.Phase != phasePast
		if m.presence != nil && m.presence.LessonID == l.ID {
			st := m.presence.State
			if st == "" {
				st = model.PresenceNone
			}
			v.Presence = &presenceView{State: st, Message: m.presence.Message, UpdatedAt: m.presence.UpdatedAt}
		}
		v.Recording = m.recording[l.ID]
	}
	return v, nil
}

func (s *Server) viewLessons(m *mine, lessons []model.Lesson) ([]lessonView, error) {
	out := make([]lessonView, 0, len(lessons))
	for _, l := range lessons {
		v, err := s.viewLesson(m, l)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// dayMode сводит решения по онлайн-парам моей подгруппы в режим дня. Прошедшие
// пары не в счёт, пока впереди есть хоть одна.
func dayMode(views []lessonView) string {
	var pool, live []lessonView
	for _, v := range views {
		if !v.Online || !v.Mine {
			continue
		}
		pool = append(pool, v)
		if v.Phase != phasePast {
			live = append(live, v)
		}
	}
	if len(pool) == 0 {
		return dayModeEmpty
	}
	if len(live) > 0 {
		pool = live
	}
	first := pool[0].Decision
	for _, v := range pool[1:] {
		if v.Decision != first {
			return dayModeMixed
		}
	}
	switch first {
	case decisionYes:
		return dayModeAll
	case decisionNo:
		return dayModeNone
	default:
		return dayModeAuto
	}
}

func (s *Server) today() string {
	return s.now().Format(dateLayout)
}

func validDate(d string) bool {
	_, err := time.Parse(dateLayout, d)
	return err == nil
}

func lessonsOn(all []model.Lesson, date string) []model.Lesson {
	out := make([]model.Lesson, 0)
	for _, l := range all {
		if l.Date == date {
			out = append(out, l)
		}
	}
	return out
}

func (s *Server) buildDay(date string) (dayResponse, error) {
	all, err := s.src.ListLessons()
	if err != nil {
		return dayResponse{}, fmt.Errorf("buildDay: ListLessons: %w", err)
	}
	m, err := s.loadMine()
	if err != nil {
		return dayResponse{}, err
	}
	views, err := s.viewLessons(m, lessonsOn(all, date))
	if err != nil {
		return dayResponse{}, err
	}
	resp := dayResponse{
		Date:    date,
		Today:   m.now.Format(dateLayout),
		Now:     m.now,
		Mode:    dayMode(views),
		Lessons: views,
		Me:      m.me,
		Demo:    s.demo,
	}
	for i := range views {
		v := &views[i]
		if resp.CurrentID == 0 && v.Phase == phaseNow && v.Mine {
			resp.CurrentID = v.ID
		}
		if resp.NextID == 0 && v.Phase == phaseLater && v.Mine {
			resp.NextID = v.ID
			v.Phase = phaseNext
		}
	}
	if resp.CurrentID == 0 {
		for _, v := range views {
			if v.Phase == phaseNow {
				resp.CurrentID = v.ID
				break
			}
		}
	}
	for _, l := range all {
		if !l.Begin.After(m.now) || !m.isMine(l) {
			continue
		}
		v, err := s.viewLesson(m, l)
		if err != nil {
			return dayResponse{}, err
		}
		resp.Upcoming = &v
		break
	}
	return resp, nil
}

func (s *Server) handleDay(w http.ResponseWriter, r *http.Request) {
	date := strings.TrimSpace(r.URL.Query().Get("date"))
	if date == "" {
		date = s.today()
	}
	if !validDate(date) {
		writeErr(w, http.StatusBadRequest, "bad date: want YYYY-MM-DD")
		return
	}
	resp, err := s.buildDay(date)
	if err != nil {
		logx.Errorf("webapp", "handleDay: %v", err)
		writeErr(w, http.StatusInternalServerError, "store error")
		return
	}
	logx.Debugf("webapp", "handleDay: date=%s lessons=%d mode=%s", date, len(resp.Lessons), resp.Mode)
	writeJSON(w, resp)
}

func (s *Server) handleWeek(w http.ResponseWriter, r *http.Request) {
	all, err := s.src.ListLessons()
	if err != nil {
		logx.Errorf("webapp", "handleWeek: ListLessons: %v", err)
		writeErr(w, http.StatusInternalServerError, "store error")
		return
	}
	m, err := s.loadMine()
	if err != nil {
		logx.Errorf("webapp", "handleWeek: %v", err)
		writeErr(w, http.StatusInternalServerError, "store error")
		return
	}
	resp := weekResponse{Today: m.now.Format(dateLayout), Now: m.now, Days: []weekDay{}, Me: m.me, Demo: s.demo}
	idx := map[string]int{}
	for _, l := range all {
		v, err := s.viewLesson(m, l)
		if err != nil {
			logx.Errorf("webapp", "handleWeek: %v", err)
			writeErr(w, http.StatusInternalServerError, "store error")
			return
		}
		i, ok := idx[l.Date]
		if !ok {
			i = len(resp.Days)
			idx[l.Date] = i
			resp.Days = append(resp.Days, weekDay{Date: l.Date, Lessons: []lessonView{}})
		}
		resp.Days[i].Lessons = append(resp.Days[i].Lessons, v)
	}
	for i := range resp.Days {
		resp.Days[i].Mode = dayMode(resp.Days[i].Lessons)
	}
	logx.Debugf("webapp", "handleWeek: days=%d lessons=%d", len(resp.Days), len(all))
	writeJSON(w, resp)
}

// setIntent пишет моё решение. "auto" возвращает пару к молчанию: есть запись —
// pending (asked_at сохраняется, повторной карточки T-15 не будет), нет — ничего.
func (s *Server) setIntent(lessonID int64, decision string, now time.Time) error {
	cur, err := s.src.GetIntent(s.myID(), lessonID)
	if err != nil {
		return err
	}
	now = now.UTC()
	switch decision {
	case decisionAuto:
		if cur == nil || cur.Decision == model.JoinPending {
			return nil
		}
		next := *cur
		next.Decision = model.JoinPending
		next.DecidedAt = nil
		return s.src.PutIntent(next)
	case decisionYes, decisionNo:
		d := model.JoinYes
		if decision == decisionNo {
			d = model.JoinNo
		}
		asked := now
		if cur != nil && !cur.AskedAt.IsZero() {
			asked = cur.AskedAt
		}
		return s.src.PutIntent(model.JoinIntent{
			TelegramID: s.myID(),
			LessonID:   lessonID,
			Decision:   d,
			AskedAt:    asked,
			DecidedAt:  &now,
		})
	}
	return fmt.Errorf("setIntent: unknown decision %q", decision)
}

func (s *Server) handleIntent(w http.ResponseWriter, r *http.Request) {
	var req intentRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	switch req.Decision {
	case decisionYes, decisionNo, decisionAuto:
	default:
		writeErr(w, http.StatusBadRequest, "decision must be yes, no or auto")
		return
	}
	if req.LessonID <= 0 {
		writeErr(w, http.StatusBadRequest, "need lesson_id")
		return
	}
	l, err := s.src.LessonByID(req.LessonID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	if l == nil {
		writeErr(w, http.StatusNotFound, "lesson not found")
		return
	}
	if !l.Online {
		writeErr(w, http.StatusBadRequest, "Это очная пара — бот на неё не заходит.")
		return
	}
	if err := s.setIntent(l.ID, req.Decision, s.now()); err != nil {
		logx.Errorf("webapp", "handleIntent: lesson=%d: %v", l.ID, err)
		writeStoreErr(w, err)
		return
	}
	logx.Infof("webapp", "intent: lesson=%d decision=%s", l.ID, req.Decision)
	day, err := s.buildDay(l.Date)
	if err != nil {
		logx.Errorf("webapp", "handleIntent: %v", err)
		writeErr(w, http.StatusInternalServerError, "store error")
		return
	}
	for _, v := range day.Lessons {
		if v.ID == l.ID {
			writeJSON(w, intentResponse{Lesson: v, Mode: day.Mode})
			return
		}
	}
	writeErr(w, http.StatusNotFound, "lesson not found")
}

func (s *Server) handleDayMode(w http.ResponseWriter, r *http.Request) {
	var req dayModeRequest
	if err := decodeJSON(r.Body, &req); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	date := strings.TrimSpace(req.Date)
	if date == "" {
		date = s.today()
	}
	if !validDate(date) {
		writeErr(w, http.StatusBadRequest, "bad date: want YYYY-MM-DD")
		return
	}
	var decision string
	switch req.Mode {
	case dayModeAuto:
		decision = decisionAuto
	case dayModeAll:
		decision = decisionYes
	case dayModeNone:
		decision = decisionNo
	default:
		writeErr(w, http.StatusBadRequest, "mode must be auto, all or none")
		return
	}
	all, err := s.src.ListLessons()
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	m, err := s.loadMine()
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	n := 0
	for _, l := range lessonsOn(all, date) {
		if !l.Online || !m.isMine(l) || !l.Finish.After(m.now) {
			continue
		}
		if err := s.setIntent(l.ID, decision, m.now); err != nil {
			logx.Errorf("webapp", "handleDayMode: lesson=%d: %v", l.ID, err)
			writeStoreErr(w, err)
			return
		}
		n++
	}
	logx.Infof("webapp", "day mode: date=%s mode=%s lessons=%d", date, req.Mode, n)
	resp, err := s.buildDay(date)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, resp)
}
