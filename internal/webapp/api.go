package webapp

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

type nowAccount struct {
	model.User
	InBot       bool   `json:"in_bot"`
	InBBB       string `json:"in_bbb"`
	HasProfile  bool   `json:"has_profile"`
	InWhitelist bool   `json:"in_whitelist"`
}

type nowResponse struct {
	Accounts  []nowAccount `json:"accounts"`
	Current   *lessonDTO   `json:"current"`
	Next      *lessonDTO   `json:"next"`
	Recording bool         `json:"recording"`
}

type peopleResponse struct {
	People []model.PersonCard `json:"people"`
}

type peoplePatch struct {
	Enabled      *bool   `json:"enabled"`
	FIO          *string `json:"fio"`
	Subgroup     *int    `json:"subgroup"`
	ExtraWords   *string `json:"extra_words"`
	DisableToday *bool   `json:"disable_today"`
}

type lessonDTO struct {
	model.Lesson
	BBBURL string `json:"bbb_url,omitempty"`
}

type lessonsResponse struct {
	Lessons    []lessonDTO       `json:"lessons"`
	BBB        []model.BBBLink   `json:"bbb"`
	Recordings []model.Recording `json:"recordings"`
	GroupID    int64             `json:"group_id"`
}

type bbbRequest struct {
	Key        string `json:"key"`
	URL        string `json:"url"`
	LessonID   int64  `json:"lesson_id"`
	Discipline string `json:"discipline"`
	Teacher    string `json:"teacher"`
}

type logsResponse struct {
	Events []model.Event `json:"events"`
}

type settingsResponse struct {
	GroupID   int64  `json:"group_id"`
	GroupCode string `json:"group_code"`
	Timezone  string `json:"timezone"`
}

func (s *Server) handleNow(w http.ResponseWriter, r *http.Request) {
	logx.Debugf("webapp", "handleNow: enter %s %s", r.Method, r.URL.Path)
	cards, err := s.personCards()
	if err != nil {
		logx.Errorf("webapp", "handleNow: personCards: %v", err)
		writeErr(w, http.StatusInternalServerError, "store error")
		return
	}
	presByID := map[int64]string{}
	if list, err := s.st.ListPresence(); err == nil {
		for _, p := range list {
			stt := p.State
			if stt == "" {
				stt = "none"
			}
			presByID[p.TelegramID] = stt
		}
	} else {
		logx.Errorf("webapp", "handleNow: ListPresence: %v", err)
	}

	accounts := make([]nowAccount, 0, len(cards))
	for _, c := range cards {
		a := nowAccount{
			InBBB:       "none",
			HasProfile:  c.HasProfile,
			InWhitelist: c.InWhitelist,
		}
		if c.User != nil {
			a.User = *c.User
			a.InBot = c.User.Enabled && c.User.Onboarded
		} else {
			a.TelegramID = c.TelegramID
		}
		id := a.TelegramID
		if id == 0 && c.User != nil {
			id = c.User.TelegramID
		}
		if stt, ok := presByID[id]; ok {
			a.InBBB = stt
		}
		accounts = append(accounts, a)
	}

	now := time.Now().In(s.loc)
	logx.Debugf("webapp", "handleNow: now=%s accounts=%d", now.Format(time.RFC3339), len(accounts))
	cur, err := s.st.CurrentLesson(now)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		logx.Errorf("webapp", "handleNow: CurrentLesson: %v", err)
		writeErr(w, http.StatusInternalServerError, "store error")
		return
	}
	if err != nil {
		logx.Debugf("webapp", "handleNow: CurrentLesson none: %v", err)
	}
	next, err := s.st.NextLesson(now)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		logx.Errorf("webapp", "handleNow: NextLesson: %v", err)
		writeErr(w, http.StatusInternalServerError, "store error")
		return
	}
	if err != nil {
		logx.Debugf("webapp", "handleNow: NextLesson none: %v", err)
	}

	logx.Debugf("webapp", "handleNow: current=%v next=%v", cur != nil, next != nil)
	writeJSON(w, nowResponse{
		Accounts:  accounts,
		Current:   s.lessonWithBBB(cur),
		Next:      s.lessonWithBBB(next),
		Recording: s.anyRecording(),
	})
}

func (s *Server) lessonWithBBB(l *model.Lesson) *lessonDTO {
	if l == nil {
		logx.Debugf("webapp", "lessonWithBBB: nil lesson")
		return nil
	}
	logx.Debugf("webapp", "lessonWithBBB: lesson=%d", l.ID)
	dto := &lessonDTO{Lesson: *l}
	if u := s.lookupBBB(l.ID); u != "" {
		dto.BBBURL = u
		logx.Debugf("webapp", "lessonWithBBB: lesson=%d bbb_url set", l.ID)
	}
	return dto
}

func (s *Server) lookupBBB(lessonID int64) string {
	if s.st == nil {
		logx.Debugf("webapp", "lookupBBB: nil store lesson=%d", lessonID)
		return ""
	}
	u := s.st.GetLessonBBB(lessonID)
	logx.Debugf("webapp", "lookupBBB: lesson=%d found=%v", lessonID, u != "")
	return u
}

func (s *Server) handlePeople(w http.ResponseWriter, r *http.Request) {
	logx.Debugf("webapp", "handlePeople: enter %s %s", r.Method, r.URL.Path)
	cards, err := s.personCards()
	if err != nil {
		logx.Errorf("webapp", "handlePeople: personCards: %v", err)
		writeErr(w, http.StatusInternalServerError, "store error")
		return
	}
	logx.Debugf("webapp", "handlePeople: cards=%d", len(cards))
	writeJSON(w, peopleResponse{People: cards})
}

func (s *Server) handlePeoplePatch(w http.ResponseWriter, r *http.Request) {
	logx.Debugf("webapp", "handlePeoplePatch: enter %s %s id=%q", r.Method, r.URL.Path, r.PathValue("id"))
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id == 0 {
		logx.Warnf("webapp", "handlePeoplePatch: bad id=%q err=%v", r.PathValue("id"), err)
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}

	var patch peoplePatch
	if err := decodeJSON(r.Body, &patch); err != nil && !errors.Is(err, io.EOF) {
		logx.Warnf("webapp", "handlePeoplePatch: bad json id=%d: %v", id, err)
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}

	if !s.cfg.IsAllowed(id) && !s.cfg.IsAdmin(id) {
		logx.Warnf("webapp", "handlePeoplePatch: id=%d not allowed and not admin", id)
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if err := s.ensureUser(id); err != nil {
		logx.Errorf("webapp", "handlePeoplePatch: ensureUser id=%d: %v", id, err)
		writeStoreErr(w, err)
		return
	}

	if patch.Enabled != nil {
		logx.Debugf("webapp", "handlePeoplePatch: id=%d set enabled=%v", id, *patch.Enabled)
		if err := s.st.SetEnabled(id, *patch.Enabled); err != nil {
			logx.Errorf("webapp", "handlePeoplePatch: SetEnabled id=%d: %v", id, err)
			writeStoreErr(w, err)
			return
		}
	}
	if patch.FIO != nil {
		logx.Debugf("webapp", "handlePeoplePatch: id=%d set fio=%q", id, *patch.FIO)
		if err := s.st.SetFIO(id, *patch.FIO); err != nil {
			logx.Errorf("webapp", "handlePeoplePatch: SetFIO id=%d: %v", id, err)
			writeStoreErr(w, err)
			return
		}
	}
	if patch.Subgroup != nil {
		logx.Debugf("webapp", "handlePeoplePatch: id=%d set subgroup=%d", id, *patch.Subgroup)
		if err := s.st.SetSubgroup(id, *patch.Subgroup); err != nil {
			logx.Errorf("webapp", "handlePeoplePatch: SetSubgroup id=%d: %v", id, err)
			writeStoreErr(w, err)
			return
		}
	}
	if patch.ExtraWords != nil {
		logx.Debugf("webapp", "handlePeoplePatch: id=%d set extra_words=%q", id, *patch.ExtraWords)
		if err := s.st.SetExtraWords(id, model.ParseWakeWords(*patch.ExtraWords)); err != nil {
			logx.Errorf("webapp", "handlePeoplePatch: SetExtraWords id=%d: %v", id, err)
			writeStoreErr(w, err)
			return
		}
	}
	if patch.DisableToday != nil {
		logx.Debugf("webapp", "handlePeoplePatch: id=%d disable_today=%v", id, *patch.DisableToday)
		var until *time.Time
		if *patch.DisableToday {
			n := time.Now().In(s.loc)
			end := time.Date(n.Year(), n.Month(), n.Day(), 23, 59, 59, 0, s.loc)
			until = &end
			logx.Debugf("webapp", "handlePeoplePatch: id=%d disabled_until=%s", id, end.Format(time.RFC3339))
		}
		if err := s.st.SetDisabledUntil(id, until); err != nil {
			logx.Errorf("webapp", "handlePeoplePatch: SetDisabledUntil id=%d: %v", id, err)
			writeStoreErr(w, err)
			return
		}
	}

	u, err := s.st.GetUser(id)
	if err != nil {
		logx.Errorf("webapp", "handlePeoplePatch: GetUser id=%d: %v", id, err)
		writeStoreErr(w, err)
		return
	}
	if u == nil {
		logx.Warnf("webapp", "handlePeoplePatch: GetUser id=%d not found after patch", id)
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	logx.Debugf("webapp", "handlePeoplePatch: id=%d done fio=%q enabled=%v", id, u.FIO, u.Enabled)
	writeJSON(w, u)
}

func (s *Server) handleLessons(w http.ResponseWriter, r *http.Request) {
	logx.Debugf("webapp", "handleLessons: enter %s %s", r.Method, r.URL.Path)
	lessons, err := s.st.ListLessons()
	if err != nil {
		logx.Errorf("webapp", "handleLessons: ListLessons: %v", err)
		writeErr(w, http.StatusInternalServerError, "store error")
		return
	}
	bbb, err := s.st.ListBBB()
	if err != nil {
		logx.Errorf("webapp", "handleLessons: ListBBB: %v", err)
		writeErr(w, http.StatusInternalServerError, "store error")
		return
	}
	byKey := make(map[string]string, len(bbb))
	for _, b := range bbb {
		byKey[b.Key] = b.URL
	}
	out := make([]lessonDTO, 0, len(lessons))
	for _, l := range lessons {
		dto := lessonDTO{Lesson: l}
		if u, ok := byKey[model.BBBLessonKey(l.ID)]; ok {
			dto.BBBURL = u
		}
		out = append(out, dto)
	}
	if bbb == nil {
		bbb = []model.BBBLink{}
	}
	logx.Debugf("webapp", "handleLessons: lessons=%d bbb=%d recordings=%d", len(out), len(bbb), len(s.listPackRecordings()))
	writeJSON(w, lessonsResponse{
		Lessons:    out,
		BBB:        bbb,
		Recordings: s.listPackRecordings(),
		GroupID:    s.cfg.GroupID,
	})
}

func (s *Server) handleBBB(w http.ResponseWriter, r *http.Request) {
	logx.Debugf("webapp", "handleBBB: enter %s %s", r.Method, r.URL.Path)
	var req bbbRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		logx.Warnf("webapp", "handleBBB: bad json: %v", err)
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	if !bbbHostOK(req.URL) {
		logx.Warnf("webapp", "handleBBB: bad url=%q", req.URL)
		writeErr(w, http.StatusBadRequest, "url must be https://bbb.ssau.ru/b/...")
		return
	}
	key := strings.TrimSpace(req.Key)
	if req.LessonID > 0 {
		key = model.BBBLessonKey(req.LessonID)
	}
	if key == "" {
		if strings.TrimSpace(req.Discipline) == "" && strings.TrimSpace(req.Teacher) == "" {
			logx.Warnf("webapp", "handleBBB: no key/lesson/discipline+teacher")
			writeErr(w, http.StatusBadRequest, "need lesson_id or key or discipline+teacher")
			return
		}
		key = model.BBBKey(s.cfg.GroupID, req.Discipline, req.Teacher)
	}
	logx.Debugf("webapp", "handleBBB: key=%q lesson_id=%d", key, req.LessonID)
	if err := s.st.SetBBB(key, req.URL); err != nil {
		logx.Errorf("webapp", "handleBBB: SetBBB key=%q: %v", key, err)
		writeStoreErr(w, err)
		return
	}
	link, err := s.st.GetBBB(key)
	if err != nil || link == nil {
		logx.Debugf("webapp", "handleBBB: GetBBB key=%q err=%v nil=%v, returning request echo", key, err, link == nil)
		writeJSON(w, model.BBBLink{Key: key, URL: req.URL, UpdatedAt: time.Now()})
		return
	}
	logx.Debugf("webapp", "handleBBB: stored key=%q", key)
	writeJSON(w, link)
}

func (s *Server) handleParser(w http.ResponseWriter, r *http.Request) {
	logx.Debugf("webapp", "handleParser: enter %s %s", r.Method, r.URL.Path)
	run, err := s.st.LastParseRun()
	if err != nil || run == nil {
		logx.Debugf("webapp", "handleParser: no run err=%v nil=%v", err, run == nil)
		writeJSON(w, model.ParseRun{})
		return
	}
	logx.Debugf("webapp", "handleParser: run id=%d ok=%v lessons=%d", run.ID, run.OK, run.LessonCount)
	writeJSON(w, run)
}

func (s *Server) handleParserRefresh(w http.ResponseWriter, r *http.Request) {
	logx.Debugf("webapp", "handleParserRefresh: enter %s %s refresh_nil=%v", r.Method, r.URL.Path, s.refresh == nil)
	if s.refresh == nil {
		logx.Warnf("webapp", "handleParserRefresh: refresh unavailable")
		writeErr(w, http.StatusServiceUnavailable, "refresh unavailable")
		return
	}
	run, err := s.refresh(r.Context())
	if err != nil {
		logx.Errorf("webapp", "handleParserRefresh: refresh: %v", err)
		writeJSONStatus(w, http.StatusBadGateway, run)
		return
	}
	logx.Infof("webapp", "handleParserRefresh: ok lessons=%d online=%d", run.LessonCount, run.OnlineCount)
	writeJSON(w, run)
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	logx.Debugf("webapp", "handleLogs: enter %s %s limit=%q", r.Method, r.URL.Path, r.URL.Query().Get("limit"))
	limit := 200
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			logx.Warnf("webapp", "handleLogs: bad limit=%q err=%v", v, err)
			writeErr(w, http.StatusBadRequest, "bad limit")
			return
		}
		limit = n
		if limit > 1000 {
			limit = 1000
		}
	}
	events, err := s.st.ListEvents(limit)
	if err != nil {
		logx.Errorf("webapp", "handleLogs: ListEvents limit=%d: %v", limit, err)
		writeErr(w, http.StatusInternalServerError, "store error")
		return
	}
	sort.SliceStable(events, func(i, j int) bool {
		return events[i].At.After(events[j].At)
	})
	if events == nil {
		events = []model.Event{}
	}
	logx.Debugf("webapp", "handleLogs: limit=%d events=%d", limit, len(events))
	writeJSON(w, logsResponse{Events: events})
}

type testJoinPatch struct {
	URL  string  `json:"url"`
	Want string  `json:"want"`
	Name *string `json:"name"`
}

func (s *Server) handleTestGet(w http.ResponseWriter, r *http.Request) {
	logx.Debugf("webapp", "handleTestGet: enter %s %s", r.Method, r.URL.Path)
	j, err := s.st.GetTestJoin()
	if err != nil {
		logx.Errorf("webapp", "handleTestGet: GetTestJoin: %v", err)
		writeStoreErr(w, err)
		return
	}
	logx.Debugf("webapp", "handleTestGet: want=%q status=%q", j.Want, j.Status)
	writeJSON(w, j)
}

func (s *Server) handleTestPost(w http.ResponseWriter, r *http.Request) {
	logx.Debugf("webapp", "handleTestPost: enter %s %s", r.Method, r.URL.Path)
	var req testJoinPatch
	if err := decodeJSON(r.Body, &req); err != nil && !errors.Is(err, io.EOF) {
		logx.Warnf("webapp", "handleTestPost: bad json: %v", err)
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	j, err := s.st.GetTestJoin()
	if err != nil {
		logx.Errorf("webapp", "handleTestPost: GetTestJoin: %v", err)
		writeStoreErr(w, err)
		return
	}
	if url := strings.TrimSpace(req.URL); url != "" {
		if !bbbHostOK(url) {
			logx.Warnf("webapp", "handleTestPost: bad url=%q", url)
			writeErr(w, http.StatusBadRequest, "url must be https://bbb.ssau.ru/b/...")
			return
		}
		j.URL = url
	}
	if req.Name != nil {
		j.Name = normalizeTestGuestName(*req.Name)
	}
	switch want := strings.TrimSpace(req.Want); want {
	case model.TestWantDummy, model.TestWantListen:
		if strings.TrimSpace(j.URL) == "" {
			logx.Warnf("webapp", "handleTestPost: want=%q without url", want)
			writeErr(w, http.StatusBadRequest, "сначала ссылка")
			return
		}
		if j.Want != want || (j.Status != model.TestJoining && j.Status != model.TestLobby && j.Status != model.TestRoom) {
			j.Want = want
			j.Status = model.TestJoining
			j.Message = "захожу"
		}
	case "off", "leave":
		j.Want = model.TestWantOff
		j.Status = model.TestIdle
		j.Mode = ""
		j.Message = "выхожу"
	}
	logx.Debugf("webapp", "handleTestPost: want=%q status=%q url=%q", j.Want, j.Status, j.URL)
	if err := s.st.PutTestJoin(j); err != nil {
		logx.Errorf("webapp", "handleTestPost: PutTestJoin: %v", err)
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, j)
}

func normalizeTestGuestName(raw string) string {
	logx.Debugf("webapp", "normalizeTestGuestName: raw=%q", raw)
	name := strings.TrimSpace(raw)
	if name == "" || name == "-" || name == "—" {
		logx.Debugf("webapp", "normalizeTestGuestName: raw=%q -> default", raw)
		return model.TestGuestName
	}
	runes := []rune(name)
	if len(runes) > 64 {
		name = string(runes[:64])
	}
	logx.Debugf("webapp", "normalizeTestGuestName: raw=%q -> %q", raw, name)
	return name
}

func (s *Server) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	logx.Debugf("webapp", "handleSettingsGet: enter %s %s", r.Method, r.URL.Path)
	// Группа задаётся в env (GROUP_ID), панель только показывает её.
	writeJSON(w, settingsResponse{
		GroupID:   s.cfg.GroupID,
		GroupCode: s.cfg.GroupCode,
		Timezone:  s.cfg.Timezone,
	})
}

func (s *Server) personCards() ([]model.PersonCard, error) {
	logx.Debugf("webapp", "personCards: enter whitelist=%d", len(s.cfg.Whitelist))
	users, err := s.st.ListUsers()
	if err != nil {
		logx.Errorf("webapp", "personCards: ListUsers: %v", err)
		return nil, fmt.Errorf("personCards: ListUsers: %w", err)
	}
	byID := make(map[int64]model.User, len(users))
	for _, u := range users {
		byID[u.TelegramID] = u
	}
	seen := make(map[int64]bool, len(s.cfg.Whitelist)+len(users))
	cards := make([]model.PersonCard, 0, len(s.cfg.Whitelist)+len(users))
	for _, id := range s.cfg.Whitelist {
		if seen[id] {
			continue
		}
		seen[id] = true
		card := model.PersonCard{
			TelegramID:  id,
			InWhitelist: true,
		}
		if u, ok := byID[id]; ok {
			uu := u
			card.User = &uu
			card.HasProfile = true
		}
		cards = append(cards, card)
	}
	for _, u := range users {
		if seen[u.TelegramID] {
			continue
		}
		seen[u.TelegramID] = true
		uu := u
		cards = append(cards, model.PersonCard{
			User:        &uu,
			TelegramID:  u.TelegramID,
			InWhitelist: s.cfg.IsAllowed(u.TelegramID),
			HasProfile:  true,
		})
	}
	logx.Debugf("webapp", "personCards: users=%d cards=%d", len(users), len(cards))
	return cards, nil
}

func (s *Server) ensureUser(id int64) error {
	logx.Debugf("webapp", "ensureUser: enter id=%d", id)
	u, err := s.st.GetUser(id)
	if err != nil {
		logx.Errorf("webapp", "ensureUser: GetUser id=%d: %v", id, err)
		return fmt.Errorf("ensureUser: GetUser %d: %w", id, err)
	}
	if u != nil {
		logx.Debugf("webapp", "ensureUser: id=%d exists", id)
		return nil
	}
	if err := s.st.UpsertUser(&model.User{
		TelegramID: id,
		Enabled:    true,
		Subgroup:   1,
	}); err != nil {
		logx.Errorf("webapp", "ensureUser: UpsertUser id=%d: %v", id, err)
		return fmt.Errorf("ensureUser: UpsertUser %d: %w", id, err)
	}
	logx.Infof("webapp", "ensureUser: created id=%d", id)
	return nil
}

func writeStoreErr(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		logx.Debugf("webapp", "writeStoreErr: not found: %v", err)
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	logx.Errorf("webapp", "writeStoreErr: store error: %v", err)
	writeErr(w, http.StatusInternalServerError, "store error")
}

func (s *Server) anyRecording() bool {
	if s == nil || s.st == nil {
		logx.Debugf("webapp", "anyRecording: nil server/store -> false")
		return false
	}
	ok, err := s.st.AnyRecording()
	if err != nil {
		logx.Errorf("webapp", "anyRecording: %v", err)
	}
	logx.Debugf("webapp", "anyRecording: -> %v", ok)
	return ok
}

func (s *Server) listPackRecordings() []model.Recording {
	logx.Debugf("webapp", "listPackRecordings: enter")
	out := []model.Recording{}
	if s == nil || s.st == nil {
		logx.Debugf("webapp", "listPackRecordings: nil server/store -> empty")
		return out
	}
	packs, err := s.st.ListPacks()
	if err != nil {
		logx.Errorf("webapp", "listPackRecordings: ListPacks: %v", err)
		return out
	}
	for _, p := range packs {
		abs := filepath.Join(s.recordingsDir, p.Dir)
		mod := p.UpdatedAt
		if st, err := os.Stat(abs); err == nil {
			mod = st.ModTime()
		} else {
			logx.Debugf("webapp", "listPackRecordings: stat %s: %v", abs, err)
		}
		out = append(out, model.Recording{
			Name:       p.Dir,
			Rel:        p.Dir,
			Size:       dirSize(abs),
			Mod:        mod,
			Status:     p.Status,
			Number:     p.Number,
			Discipline: p.Discipline,
		})
	}
	logx.Debugf("webapp", "listPackRecordings: packs=%d -> recordings=%d", len(packs), len(out))
	return out
}

func dirSize(root string) int64 {
	logx.Debugf("webapp", "dirSize: root=%s", root)
	var n int64
	if err := filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			n += info.Size()
		}
		return nil
	}); err != nil {
		logx.Errorf("webapp", "dirSize: walk %s: %v", root, err)
	}
	logx.Debugf("webapp", "dirSize: root=%s -> %d", root, n)
	return n
}
