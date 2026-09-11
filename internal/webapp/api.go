package webapp

import (
	"database/sql"
	"errors"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

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
	SOCKS5       *string `json:"socks5"`
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

type settingsPatch struct {
	GroupID *int64 `json:"group_id"`
}

func (s *Server) handleNow(w http.ResponseWriter, r *http.Request) {
	cards, err := s.personCards()
	if err != nil {
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
	cur, err := s.st.CurrentLesson(now)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeErr(w, http.StatusInternalServerError, "store error")
		return
	}
	next, err := s.st.NextLesson(now)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeErr(w, http.StatusInternalServerError, "store error")
		return
	}

	writeJSON(w, nowResponse{
		Accounts:  accounts,
		Current:   s.lessonWithBBB(cur),
		Next:      s.lessonWithBBB(next),
		Recording: false,
	})
}

func (s *Server) lessonWithBBB(l *model.Lesson) *lessonDTO {
	if l == nil {
		return nil
	}
	dto := &lessonDTO{Lesson: *l}
	if u := s.lookupBBB(l.Discipline, l.Teacher); u != "" {
		dto.BBBURL = u
	}
	return dto
}

func (s *Server) lookupBBB(discipline, teacher string) string {
	if s.st == nil {
		return ""
	}
	b, err := s.st.GetBBB(model.BBBKey(s.cfg.GroupID, discipline, teacher))
	if err != nil || b == nil {
		return ""
	}
	return b.URL
}

func (s *Server) handlePeople(w http.ResponseWriter, r *http.Request) {
	cards, err := s.personCards()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store error")
		return
	}
	writeJSON(w, peopleResponse{People: cards})
}

func (s *Server) handlePeoplePatch(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id == 0 {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}

	var patch peoplePatch
	if err := decodeJSON(r.Body, &patch); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}

	if !s.cfg.IsAllowed(id) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if err := s.ensureUser(id); err != nil {
		writeStoreErr(w, err)
		return
	}

	if patch.Enabled != nil {
		if err := s.st.SetEnabled(id, *patch.Enabled); err != nil {
			writeStoreErr(w, err)
			return
		}
	}
	if patch.FIO != nil {
		if err := s.st.SetFIO(id, *patch.FIO); err != nil {
			writeStoreErr(w, err)
			return
		}
	}
	if patch.Subgroup != nil {
		if err := s.st.SetSubgroup(id, *patch.Subgroup); err != nil {
			writeStoreErr(w, err)
			return
		}
	}
	if patch.SOCKS5 != nil {
		if err := s.st.SetSOCKS5(id, *patch.SOCKS5); err != nil {
			writeStoreErr(w, err)
			return
		}
	}
	if patch.ExtraWords != nil {
		if err := s.st.SetExtraWords(id, model.ParseWakeWords(*patch.ExtraWords)); err != nil {
			writeStoreErr(w, err)
			return
		}
	}
	if patch.DisableToday != nil {
		var until *time.Time
		if *patch.DisableToday {
			n := time.Now().In(s.loc)
			end := time.Date(n.Year(), n.Month(), n.Day(), 23, 59, 59, 0, s.loc)
			until = &end
		}
		if err := s.st.SetDisabledUntil(id, until); err != nil {
			writeStoreErr(w, err)
			return
		}
	}

	u, err := s.st.GetUser(id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	if u == nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, u)
}

func (s *Server) handleLessons(w http.ResponseWriter, r *http.Request) {
	lessons, err := s.st.ListLessons()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store error")
		return
	}
	bbb, err := s.st.ListBBB()
	if err != nil {
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
		if u, ok := byKey[model.BBBKey(s.cfg.GroupID, l.Discipline, l.Teacher)]; ok {
			dto.BBBURL = u
		}
		out = append(out, dto)
	}
	if bbb == nil {
		bbb = []model.BBBLink{}
	}
	writeJSON(w, lessonsResponse{
		Lessons:    out,
		BBB:        bbb,
		Recordings: listRecordings(s.recordingsDir),
		GroupID:    s.cfg.GroupID,
	})
}

func (s *Server) handleBBB(w http.ResponseWriter, r *http.Request) {
	var req bbbRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	if !bbbHostOK(req.URL) {
		writeErr(w, http.StatusBadRequest, "url must be https://bbb.ssau.ru/b/...")
		return
	}
	key := strings.TrimSpace(req.Key)
	if key == "" {
		if strings.TrimSpace(req.Discipline) == "" && strings.TrimSpace(req.Teacher) == "" {
			writeErr(w, http.StatusBadRequest, "need key or discipline+teacher")
			return
		}
		key = model.BBBKey(s.cfg.GroupID, req.Discipline, req.Teacher)
	}
	if err := s.st.SetBBB(key, req.URL); err != nil {
		writeStoreErr(w, err)
		return
	}
	link, err := s.st.GetBBB(key)
	if err != nil || link == nil {
		writeJSON(w, model.BBBLink{Key: key, URL: req.URL, UpdatedAt: time.Now()})
		return
	}
	writeJSON(w, link)
}

func (s *Server) handleParser(w http.ResponseWriter, r *http.Request) {
	run, err := s.st.LastParseRun()
	if err != nil || run == nil {
		writeJSON(w, model.ParseRun{})
		return
	}
	writeJSON(w, run)
}

func (s *Server) handleParserRefresh(w http.ResponseWriter, r *http.Request) {
	if s.refresh == nil {
		writeErr(w, http.StatusServiceUnavailable, "refresh unavailable")
		return
	}
	run, err := s.refresh(r.Context())
	if err != nil {
		writeJSONStatus(w, http.StatusBadGateway, run)
		return
	}
	writeJSON(w, run)
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	limit := 200
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
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
		writeErr(w, http.StatusInternalServerError, "store error")
		return
	}
	sort.SliceStable(events, func(i, j int) bool {
		return events[i].At.After(events[j].At)
	})
	if events == nil {
		events = []model.Event{}
	}
	writeJSON(w, logsResponse{Events: events})
}

func (s *Server) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, settingsResponse{
		GroupID:   s.cfg.GroupID,
		GroupCode: s.cfg.GroupCode,
		Timezone:  s.cfg.Timezone,
	})
}

func (s *Server) handleSettingsPost(w http.ResponseWriter, r *http.Request) {
	var patch settingsPatch
	if err := decodeJSON(r.Body, &patch); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if patch.GroupID != nil {
		if err := s.st.SetSetting("group_id", strconv.FormatInt(*patch.GroupID, 10)); err != nil {
			writeStoreErr(w, err)
			return
		}
	}
	s.handleSettingsGet(w, r)
}

func (s *Server) personCards() ([]model.PersonCard, error) {
	users, err := s.st.ListUsers()
	if err != nil {
		return nil, err
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
	return cards, nil
}

func (s *Server) ensureUser(id int64) error {
	u, err := s.st.GetUser(id)
	if err != nil {
		return err
	}
	if u != nil {
		return nil
	}
	return s.st.UpsertUser(&model.User{
		TelegramID: id,
		Enabled:    true,
		Subgroup:   1,
	})
}

func writeStoreErr(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	writeErr(w, http.StatusInternalServerError, "store error")
}

func listRecordings(dir string) []model.Recording {
	out := []model.Recording{}
	if strings.TrimSpace(dir) == "" {
		return out
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, model.Recording{
			Name: e.Name(),
			Size: info.Size(),
			Mod:  info.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Mod.After(out[j].Mod)
	})
	return out
}
