package webapp

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/archive"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

// ---------- Дедлайны и Moodle ----------

const (
	dueOverdue = "overdue"
	dueSoon    = "soon" // сегодня и завтра
	dueWeek    = "week" // до конца этой недели
	dueLater   = "later"
	dueDone    = "done"    // сдано
	dueNone    = "nodue"   // срока нет
	dueClosed  = "closed"  // тест закрыт для попыток и не пройден
	dueNotOpen = "notopen" // ещё не открыт: Opens в будущем
)

type deadlineView struct {
	Deadline
	Group string `json:"group"`
}

type deadlinesResponse struct {
	Moodle    MoodleStatus   `json:"moodle"`
	Now       time.Time      `json:"now"`
	Deadlines []deadlineView `json:"deadlines"`
	Counts    map[string]int `json:"counts"`
}

func dueGroup(d Deadline, now time.Time) string {
	if d.Status == DeadlineSubmitted {
		return dueDone
	}
	if d.Closed {
		return dueClosed
	}
	if d.NotOpen {
		return dueNotOpen
	}
	if d.Due.IsZero() {
		return dueNone
	}
	if d.Due.Before(now) {
		return dueOverdue
	}
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	if d.Due.Before(day.AddDate(0, 0, 2)) {
		return dueSoon
	}
	daysToMonday := (8 - int(day.Weekday())) % 7
	if daysToMonday == 0 {
		daysToMonday = 7
	}
	if d.Due.Before(day.AddDate(0, 0, daysToMonday)) {
		return dueWeek
	}
	return dueLater
}

// sortTime — по чему сортировать срок: у ещё не открытого — момент открытия,
// иначе срок. Без времени (ни срока, ни открытия) — ok=false, уйдёт в конец.
func sortTime(d Deadline) (time.Time, bool) {
	if d.NotOpen && !d.Opens.IsZero() {
		return d.Opens, true
	}
	return d.Due, !d.Due.IsZero()
}

func (s *Server) handleDeadlines(w http.ResponseWriter, r *http.Request) {
	now := s.now()
	resp := deadlinesResponse{
		Moodle:    s.moodle.Status(),
		Now:       now,
		Deadlines: []deadlineView{},
		Counts:    map[string]int{dueOverdue: 0, dueSoon: 0, dueWeek: 0, dueLater: 0, dueDone: 0, dueNone: 0, dueClosed: 0, dueNotOpen: 0},
	}
	if resp.Moodle.Connected {
		list, err := s.moodle.Deadlines(r.Context(), now)
		if err != nil {
			logx.Errorf("webapp", "handleDeadlines: %v", err)
			writeErr(w, http.StatusBadGateway, "Moodle не ответил, попробуйте позже.")
			return
		}
		sort.SliceStable(list, func(i, j int) bool {
			ti, oi := sortTime(list[i])
			tj, oj := sortTime(list[j])
			if oi != oj {
				return oi
			}
			return ti.Before(tj)
		})
		for _, d := range list {
			v := deadlineView{Deadline: d, Group: dueGroup(d, now)}
			if !v.Due.IsZero() {
				v.Due = v.Due.In(s.loc)
			}
			if !v.Opens.IsZero() {
				v.Opens = v.Opens.In(s.loc)
			}
			resp.Counts[v.Group]++
			resp.Deadlines = append(resp.Deadlines, v)
		}
	}
	logx.Debugf("webapp", "handleDeadlines: connected=%v n=%d", resp.Moodle.Connected, len(resp.Deadlines))
	writeJSON(w, resp)
}

type moodleLoginRequest struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

type moodleErrorResponse struct {
	Error  string       `json:"error"`
	Moodle MoodleStatus `json:"moodle"`
}

// handleMoodleLogin. Пароль проверяется входом в Moodle и сохраняется в
// accounts только зашифрованным (vault, AES-GCM); в лог и в ответ не попадает.
// Ошибку разбора тела тоже не логируем — там пароль.
func (s *Server) handleMoodleLogin(w http.ResponseWriter, r *http.Request) {
	var req moodleLoginRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	login := strings.TrimSpace(req.Login)
	if login == "" || req.Password == "" {
		writeErr(w, http.StatusBadRequest, "Введите логин и пароль от личного кабинета.")
		return
	}
	st, err := s.moodle.Login(r.Context(), login, req.Password)
	req.Password = ""
	switch {
	case errors.Is(err, ErrMoodleNotReady):
		logx.Infof("webapp", "moodle login: not implemented yet")
		writeJSONStatus(w, http.StatusNotImplemented, moodleErrorResponse{
			Error:  "Вход в Moodle пока не работает: он появится в следующей версии панели. Пароль никуда не сохранён.",
			Moodle: st,
		})
	case errors.Is(err, ErrMoodleBadLogin):
		logx.Infof("webapp", "moodle login: rejected")
		writeJSONStatus(w, http.StatusUnprocessableEntity, moodleErrorResponse{
			Error:  "Moodle не принял логин или пароль.",
			Moodle: st,
		})
	case err != nil:
		logx.Warnf("webapp", "moodle login: failed")
		writeJSONStatus(w, http.StatusBadGateway, moodleErrorResponse{
			Error:  "Moodle не ответил, попробуйте позже.",
			Moodle: st,
		})
	default:
		logx.Infof("webapp", "moodle login: connected")
		writeJSON(w, st)
	}
}

func (s *Server) handleMoodleLogout(w http.ResponseWriter, r *http.Request) {
	if err := s.moodle.Logout(); err != nil {
		logx.Errorf("webapp", "handleMoodleLogout: %v", err)
		writeErr(w, http.StatusInternalServerError, "Не получилось отключить Moodle.")
		return
	}
	writeJSON(w, s.moodle.Status())
}

// ---------- Конспекты ----------

const (
	noteReady     = "ready"
	noteBuilding  = "building"
	noteRecording = "recording"
	noteEmpty     = "empty"
	noteError     = "error"
)

type noteView struct {
	ID        int64  `json:"id"`
	LessonID  int64  `json:"lesson_id"`
	Number    int    `json:"number"`
	Date      string `json:"date"`
	Type      string `json:"type"`
	Kind      string `json:"kind"`
	Topic     string `json:"topic,omitempty"`
	Status    string `json:"status"`
	Stage     string `json:"stage"`
	Published bool   `json:"published"`
	PDFURL    string `json:"pdf_url,omitempty"`
	FolderURL string `json:"folder_url,omitempty"`
	Error     string `json:"error,omitempty"`
}

type noteSubject struct {
	Discipline string     `json:"discipline"`
	Latest     string     `json:"latest"`
	Notes      []noteView `json:"notes"`
}

type notesResponse struct {
	Subjects []noteSubject `json:"subjects"`
	RepoURL  string        `json:"repo_url,omitempty"`
	Total    int           `json:"total"`
}

func noteStatus(packStatus string) string {
	switch packStatus {
	case model.PackDone:
		return noteReady
	case model.PackRecording:
		return noteRecording
	case model.PackEmpty:
		return noteEmpty
	case model.PackError:
		return noteError
	default:
		return noteBuilding
	}
}

func escapePath(p string) string {
	parts := strings.Split(strings.Trim(filepath.ToSlash(p), "/"), "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

func (s *Server) repoURL() string {
	if s.cfg == nil || strings.TrimSpace(s.cfg.GitHubOwner) == "" || strings.TrimSpace(s.cfg.GitHubRepo) == "" {
		return ""
	}
	return "https://github.com/" + url.PathEscape(s.cfg.GitHubOwner) + "/" + url.PathEscape(s.cfg.GitHubRepo)
}

func (s *Server) githubLinks(p model.LecturePack) (pdf, folder string) {
	base := s.repoURL()
	if base == "" || p.PublishStatus != "published" || strings.TrimSpace(p.Dir) == "" {
		return "", ""
	}
	branch := strings.TrimSpace(s.cfg.GitHubBranch)
	if branch == "" {
		branch = "main"
	}
	dir := escapePath(p.Dir)
	folder = base + "/tree/" + url.PathEscape(branch) + "/" + dir
	pdf = base + "/blob/" + url.PathEscape(branch) + "/" + dir + "/notes.pdf"
	return pdf, folder
}

var topicLine = regexp.MustCompile(`(?i)^#*\s*\**тема\**\s*[:.—-]\s*(.+)$`)

// readTopic достаёт тему лекции из notes.md: строка «Тема: …» или текст после
// заголовка «Тема». Файла нет или темы нет — пусто.
func readTopic(root string, p model.LecturePack) string {
	if strings.TrimSpace(p.Dir) == "" {
		return ""
	}
	abs, err := archive.UnderRoot(root, path.Join(filepath.ToSlash(p.Dir), "notes.md"))
	if err != nil {
		return ""
	}
	f, err := os.Open(abs)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(io.LimitReader(f, 16<<10))
	afterHeading := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if m := topicLine.FindStringSubmatch(line); m != nil {
			return clipTopic(m[1])
		}
		bare := strings.ToLower(strings.Trim(line, "#*: "))
		if strings.HasPrefix(line, "#") && bare == "тема" {
			afterHeading = true
			continue
		}
		if afterHeading {
			if strings.HasPrefix(line, "#") {
				return ""
			}
			return clipTopic(line)
		}
	}
	return ""
}

func clipTopic(s string) string {
	s = strings.TrimSpace(strings.Trim(s, "*_ "))
	if utf8.RuneCountInString(s) > 140 {
		r := []rune(s)
		s = strings.TrimSpace(string(r[:139])) + "…"
	}
	return s
}

func (s *Server) handleNotes(w http.ResponseWriter, r *http.Request) {
	packs, err := s.src.ListPacks()
	if err != nil {
		logx.Errorf("webapp", "handleNotes: ListPacks: %v", err)
		writeErr(w, http.StatusInternalServerError, "store error")
		return
	}
	lessons, err := s.src.ListLessons()
	if err != nil {
		logx.Errorf("webapp", "handleNotes: ListLessons: %v", err)
		writeErr(w, http.StatusInternalServerError, "store error")
		return
	}
	byID := lessonsByID(lessons)
	details, _ := s.src.(packDetails)
	idx := map[string]int{}
	resp := notesResponse{Subjects: []noteSubject{}, RepoURL: s.repoURL()}
	for _, p := range packs {
		ok, typ := s.packVisible(p, byID)
		if !ok {
			continue
		}
		resp.Total++
		v := noteView{
			ID: p.ID, LessonID: p.LessonID, Number: p.Number, Date: p.Date,
			Type: typ, Kind: lessonKind(typ), Status: noteStatus(p.Status), Stage: p.Status,
			Published: p.PublishStatus == "published",
		}
		v.PDFURL, v.FolderURL = s.githubLinks(p)
		if v.Status == noteError {
			v.Error = p.Err
		}
		if details != nil {
			v.Topic = details.PackTopic(p)
		} else if v.Status == noteReady {
			v.Topic = readTopic(s.recordingsDir, p)
		}
		name := strings.TrimSpace(p.Discipline)
		i, ok := idx[name]
		if !ok {
			i = len(resp.Subjects)
			idx[name] = i
			resp.Subjects = append(resp.Subjects, noteSubject{Discipline: name, Notes: []noteView{}})
		}
		resp.Subjects[i].Notes = append(resp.Subjects[i].Notes, v)
		if p.Date > resp.Subjects[i].Latest {
			resp.Subjects[i].Latest = p.Date
		}
	}
	for i := range resp.Subjects {
		n := resp.Subjects[i].Notes
		sort.SliceStable(n, func(a, b int) bool {
			if n[a].Date != n[b].Date {
				return n[a].Date > n[b].Date
			}
			return n[a].Number > n[b].Number
		})
	}
	sort.SliceStable(resp.Subjects, func(a, b int) bool {
		if resp.Subjects[a].Latest != resp.Subjects[b].Latest {
			return resp.Subjects[a].Latest > resp.Subjects[b].Latest
		}
		return resp.Subjects[a].Discipline < resp.Subjects[b].Discipline
	})
	logx.Debugf("webapp", "handleNotes: packs=%d subjects=%d", len(packs), len(resp.Subjects))
	writeJSON(w, resp)
}

// ---------- Профиль ----------

type mePatch struct {
	FIO        *string `json:"fio"`
	Subgroup   *int    `json:"subgroup"`
	ExtraWords *string `json:"extra_words"`
	Enabled    *bool   `json:"enabled"`
	// Onboard — первый заход в панель: имя и подгруппа заданы, настройка закончена
	// (как онбординг в Telegram), бот начинает заходить.
	Onboard bool `json:"onboard"`
}

func (s *Server) currentMe() (meView, error) {
	u, err := s.src.GetUser(s.myID())
	if err != nil {
		return meView{}, err
	}
	return buildMe(s.myID(), u, s.now(), s.loc), nil
}

func (s *Server) handleMeGet(w http.ResponseWriter, r *http.Request) {
	me, err := s.currentMe()
	if err != nil {
		logx.Errorf("webapp", "handleMeGet: %v", err)
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, me)
}

func (s *Server) handleMePost(w http.ResponseWriter, r *http.Request) {
	var p mePatch
	if err := decodeJSON(r.Body, &p); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	var fio string
	if p.FIO != nil {
		fio = strings.Join(strings.Fields(*p.FIO), " ")
		if fio == "" {
			writeErr(w, http.StatusBadRequest, "Имя для BBB не может быть пустым.")
			return
		}
		if utf8.RuneCountInString(fio) > 64 {
			writeErr(w, http.StatusBadRequest, "Имя для BBB длиннее 64 символов.")
			return
		}
	}
	if p.Subgroup != nil && (*p.Subgroup < 1 || *p.Subgroup > 4) {
		writeErr(w, http.StatusBadRequest, "Подгруппа — число от 1 до 4.")
		return
	}
	id := s.myID()
	if id == 0 {
		writeErr(w, http.StatusBadRequest, "ADMIN_TELEGRAM_ID не задан")
		return
	}
	if err := s.ensureUser(id); err != nil {
		writeStoreErr(w, err)
		return
	}
	steps := []func() error{}
	if p.FIO != nil {
		steps = append(steps, func() error { return s.src.SetFIO(id, fio) })
	}
	if p.Subgroup != nil {
		n := *p.Subgroup
		steps = append(steps, func() error { return s.src.SetSubgroup(id, n) })
	}
	if p.ExtraWords != nil {
		words := model.ParseWakeWords(*p.ExtraWords)
		steps = append(steps, func() error { return s.src.SetExtraWords(id, words) })
	}
	if p.Enabled != nil {
		on := *p.Enabled
		steps = append(steps, func() error { return s.src.SetEnabled(id, on) })
	}
	if p.Onboard {
		if p.FIO == nil || p.Subgroup == nil {
			writeErr(w, http.StatusBadRequest, "Для первого входа нужны имя для BBB и подгруппа.")
			return
		}
		steps = append(steps, func() error {
			u, err := s.src.GetUser(id)
			if err != nil || u == nil {
				return err
			}
			if u.Onboarded && u.OnboardStage == model.StageDone {
				return nil
			}
			u.Onboarded, u.OnboardStage = true, model.StageDone
			return s.src.UpsertUser(u)
		})
	}
	for _, step := range steps {
		if err := step(); err != nil {
			logx.Errorf("webapp", "handleMePost: %v", err)
			writeStoreErr(w, err)
			return
		}
	}
	logx.Infof("webapp", "me: profile updated fields=%d", len(steps))
	me, err := s.currentMe()
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, me)
}

// ---------- Комнаты BBB ----------

type roomView struct {
	Key        string `json:"key"`
	Discipline string `json:"discipline"`
	Teacher    string `json:"teacher"`
	Type       string `json:"type"`
	Kind       string `json:"kind"`
	URL        string `json:"url"`
	Lessons    int    `json:"lessons"`
}

type roomsResponse struct {
	Rooms []roomView `json:"rooms"`
}

// handleRooms — комнаты по предметам: онлайн-пары, сведённые по предмету,
// преподу и типу (ключ room|…). Сохраняются через POST /api/bbb {key, url}.
func (s *Server) handleRooms(w http.ResponseWriter, r *http.Request) {
	lessons, err := s.src.ListLessons()
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	links, err := s.src.ListBBB()
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	idx := map[string]int{}
	resp := roomsResponse{Rooms: []roomView{}}
	for _, l := range lessons {
		if !l.Online || !s.lessonVisible(l) {
			continue
		}
		key := model.BBBRoomKey(l.Discipline, l.Teacher, l.Type)
		if i, ok := idx[key]; ok {
			resp.Rooms[i].Lessons++
			continue
		}
		probe := l
		probe.ID = 0
		u, _ := model.ResolveBBB(probe, links)
		idx[key] = len(resp.Rooms)
		resp.Rooms = append(resp.Rooms, roomView{
			Key: key, Discipline: strings.TrimSpace(l.Discipline), Teacher: strings.TrimSpace(l.Teacher),
			Type: strings.TrimSpace(l.Type), Kind: lessonKind(l.Type), URL: u, Lessons: 1,
		})
	}
	sort.SliceStable(resp.Rooms, func(a, b int) bool {
		if resp.Rooms[a].Discipline != resp.Rooms[b].Discipline {
			return resp.Rooms[a].Discipline < resp.Rooms[b].Discipline
		}
		return resp.Rooms[a].Kind < resp.Rooms[b].Kind
	})
	writeJSON(w, resp)
}
