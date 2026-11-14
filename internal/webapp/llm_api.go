package webapp

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/access"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/llm"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/quizlog"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/uni"
)

// ИИ для тестов. Каждый выбирает себе провайдера, модель и ключ (ключи
// подписаны владельцами, секреты в панель не уходят; хвосты ключей видит
// только админ, cheaperinference доступен только админу). Не ответил выбранный
// ключ — остальные ключи той же модели, потом общая цепочка.

type llmState struct {
	Me        string         `json:"me"` // ФИО из ЛК / имя для BBB
	Admin     bool           `json:"admin"`
	Providers []llm.Provider `json:"providers"`
	Choice    llm.Choice     `json:"choice"`
	Chain     []string       `json:"chain"`       // мои тесты — по порядку
	Default   []string       `json:"default"`     // общая цепочка
	NotesWith []string       `json:"notes_chain"` // конспекты — общая цепочка
}

func chainNames(ts []llm.Target) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.String())
	}
	return out
}

func (s *Server) needStore(w http.ResponseWriter) bool {
	if s.st == nil {
		writeErr(w, http.StatusServiceUnavailable, "в демо нет базы")
		return false
	}
	return true
}

// visibleProviders — провайдеры, доступные тому, кто вошёл.
func (s *Server) visibleProviders() []llm.Provider {
	admin := s.viewerIsAdmin()
	var out []llm.Provider
	for _, p := range llm.Providers(s.cfg) {
		if p.AdminOnly && !admin {
			continue
		}
		out = append(out, p.Public(admin))
	}
	return out
}

func (s *Server) llmStateFor(id int64) llmState {
	def := llm.DefaultChain(s.cfg)
	return llmState{
		Me:        access.Name(s.st, id),
		Admin:     s.viewerIsAdmin(),
		Providers: s.visibleProviders(),
		Choice:    access.GetChoice(s.st, id),
		Chain:     chainNames(access.QuizChain(s.cfg, s.st, id)),
		Default:   chainNames(def),
		NotesWith: chainNames(def),
	}
}

func (s *Server) handleLLMGet(w http.ResponseWriter, r *http.Request) {
	if !s.needStore(w) {
		return
	}
	writeJSON(w, s.llmStateFor(s.myID()))
}

// allowedProvider — провайдер есть и доступен вошедшему.
func (s *Server) allowedProvider(id string) (llm.Provider, bool) {
	p, ok := llm.Find(s.cfg, id)
	if !ok || (p.AdminOnly && !s.viewerIsAdmin()) {
		return llm.Provider{}, false
	}
	return p, true
}

func (s *Server) handleLLMModels(w http.ResponseWriter, r *http.Request) {
	p, ok := s.allowedProvider(r.URL.Query().Get("provider"))
	if !ok {
		writeErr(w, http.StatusBadRequest, "нет такого провайдера")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	models, err := llm.ListModels(ctx, p)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "список моделей: "+err.Error())
		return
	}
	sort.Strings(models)
	writeJSON(w, map[string]any{"provider": p.ID, "models": models, "recommended": p.Models})
}

func (s *Server) decodeChoice(w http.ResponseWriter, r *http.Request) (llm.Choice, bool) {
	var ch llm.Choice
	if err := decodeJSON(r.Body, &ch); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return ch, false
	}
	if ch.Provider == "" {
		return ch, true
	}
	p, ok := s.allowedProvider(ch.Provider)
	if !ok {
		writeErr(w, http.StatusBadRequest, "нет такого провайдера")
		return ch, false
	}
	if ch.Key < 0 || ch.Key > len(p.Keys) {
		writeErr(w, http.StatusBadRequest, "нет такого ключа")
		return ch, false
	}
	return ch, true
}

func (s *Server) handleLLMChoice(w http.ResponseWriter, r *http.Request) {
	if !s.needStore(w) {
		return
	}
	ch, ok := s.decodeChoice(w, r)
	if !ok {
		return
	}
	if err := access.SetChoice(s.st, s.myID(), ch); err != nil {
		writeStoreErr(w, err)
		return
	}
	logx.Infof("panel", "llm choice tg=%d provider=%q model=%q key=%d", s.myID(), ch.Provider, ch.Model, ch.Key)
	writeJSON(w, s.llmStateFor(s.myID()))
}

func (s *Server) handleLLMTest(w http.ResponseWriter, r *http.Request) {
	ch, ok := s.decodeChoice(w, r)
	if !ok {
		return
	}
	if ch.Provider == "" {
		writeErr(w, http.StatusBadRequest, "выбери провайдера")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	res, err := llm.Test(ctx, s.cfg, ch)
	out := map[string]any{"ok": err == nil, "attempts": res.Attempts, "text": res.Text}
	if err != nil {
		out["error"] = err.Error()
	}
	writeJSON(w, out)
}

type personLLM struct {
	TelegramID int64      `json:"telegram_id"`
	Name       string     `json:"name"`
	Admin      bool       `json:"admin"`
	Choice     llm.Choice `json:"choice"`
	First      string     `json:"first"` // с чего начнётся его цепочка
}

// handleLLMPeople — админ: у кого что выбрано.
func (s *Server) handleLLMPeople(w http.ResponseWriter, r *http.Request) {
	if !s.needStore(w) {
		return
	}
	out := []personLLM{}
	for _, id := range s.panelIDs() {
		chain := access.QuizChain(s.cfg, s.st, id)
		first := ""
		if len(chain) > 0 {
			first = chain[0].String()
		}
		out = append(out, personLLM{TelegramID: id, Name: access.Name(s.st, id), Admin: access.IsAdmin(s.cfg, s.st, id),
			Choice: access.GetChoice(s.st, id), First: first})
	}
	writeJSON(w, map[string]any{"people": out, "providers": s.visibleProviders()})
}

func (s *Server) quizDir() string { return quizlog.Dir(s.cfg.DBPath) }

type runView struct {
	quizlog.Run
	Who string `json:"who"`
}

func (s *Server) handleQuizRuns(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if n <= 0 || n > quizlog.Keep {
		n = 50
	}
	runs, err := quizlog.List(s.quizDir(), n)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]runView, 0, len(runs))
	for _, run := range runs {
		out = append(out, runView{Run: run, Who: access.Name(s.st, run.TelegramID)})
	}
	writeJSON(w, map[string]any{"runs": out})
}

func (s *Server) handleQuizRun(w http.ResponseWriter, r *http.Request) {
	run, err := quizlog.Read(s.quizDir(), r.PathValue("name"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "нет такого прогона")
		return
	}
	writeJSON(w, runView{Run: *run, Who: access.Name(s.st, run.TelegramID)})
}

// requeueQuiz — снять отметки «решён / неудачи / прислан текст», поставить
// «пройти принудительно» и пнуть синк Moodle (подхватит за ~5 с).
func (s *Server) requeueQuiz(id, cmid int64) error {
	for _, k := range []string{
		fmt.Sprintf("quiz_solved:%d:%d", id, cmid),
		fmt.Sprintf("quiz_fail:%d:%d", id, cmid),
		fmt.Sprintf("quiz_parsed:%d:%d", id, cmid),
	} {
		if err := s.st.SetSetting(k, ""); err != nil {
			return err
		}
	}
	if err := s.st.SetSetting(uni.QuizForceKey(id, cmid), "1"); err != nil {
		return err
	}
	return uni.Kick(s.st, id)
}

type quizReq struct {
	CMID       int64 `json:"cmid"`
	TelegramID int64 `json:"telegram_id"`
}

// handleQuizForce — «Пройти»: свой тест, в том числе просроченный.
func (s *Server) handleQuizForce(w http.ResponseWriter, r *http.Request) {
	if !s.needStore(w) {
		return
	}
	var req quizReq
	if err := decodeJSON(r.Body, &req); err != nil || req.CMID <= 0 {
		writeErr(w, http.StatusBadRequest, "нужен cmid")
		return
	}
	if err := s.requeueQuiz(s.myID(), req.CMID); err != nil {
		writeStoreErr(w, err)
		return
	}
	logx.Infof("panel", "quiz force tg=%d cmid=%d", s.myID(), req.CMID)
	writeJSON(w, map[string]any{"ok": true, "message": "Запускаю: бот зайдёт в тест в течение минуты, результат придёт в Telegram."})
}

// handleQuizResolve — админ: перерешать тест любого человека.
func (s *Server) handleQuizResolve(w http.ResponseWriter, r *http.Request) {
	if !s.needStore(w) {
		return
	}
	var req quizReq
	if err := decodeJSON(r.Body, &req); err != nil || req.CMID <= 0 {
		writeErr(w, http.StatusBadRequest, "нужен cmid")
		return
	}
	id := req.TelegramID
	if id == 0 {
		id = s.myID()
	}
	if err := s.requeueQuiz(id, req.CMID); err != nil {
		writeStoreErr(w, err)
		return
	}
	logx.Infof("panel", "quiz resolve tg=%d cmid=%d by=%d", id, req.CMID, s.myID())
	writeJSON(w, map[string]any{"ok": true, "message": "Перерешаю: синк Moodle подхватит в течение минуты."})
}
