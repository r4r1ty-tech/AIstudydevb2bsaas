package webapp

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/access"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/llm"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/quizlog"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
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

var personalProviders = []string{llm.Gemini, llm.OpenCode}

const (
	// Ориентировочные суточные лимиты бесплатных тарифов (только как подсказка в UI).
	estimatedGeminiDailyRequests   = 1500
	estimatedOpenCodeDailyRequests = 200
)

func isPersonalKeyProvider(provider string) bool {
	return provider == llm.Gemini || provider == llm.OpenCode
}

func (s *Server) personalKeyMap(id int64) map[string]store.LLMKeyRecord {
	if s.st == nil || id == 0 {
		return nil
	}
	recs, err := s.st.ListLLMKeysByUser(id)
	if err != nil || len(recs) == 0 {
		return nil
	}
	out := make(map[string]store.LLMKeyRecord, len(recs))
	for _, rec := range recs {
		if isPersonalKeyProvider(rec.Provider) {
			out[rec.Provider] = rec
		}
	}
	return out
}

// visibleProviders — провайдеры, доступные тому, кто вошёл. Не админ видит
// только свои ключи (подпись = его фамилия или личный ключ из БД) и только провайдеров, где они есть.
func (s *Server) visibleProviders() []llm.Provider {
	admin := s.viewerIsAdmin()
	id := s.myID()
	owner := access.KeyOwner(s.st, id)
	dbKeys := s.personalKeyMap(id)

	provs := llm.Providers(s.cfg)
	seenProv := make(map[string]bool, len(provs))
	for _, p := range provs {
		seenProv[p.ID] = true
	}
	for _, pid := range personalProviders {
		if _, hasDB := dbKeys[pid]; hasDB && !seenProv[pid] {
			if bp, ok := llm.BuiltinProvider(pid); ok {
				provs = append(provs, bp)
				seenProv[pid] = true
			}
		}
	}

	var out []llm.Provider
	for _, p := range provs {
		if p.AdminOnly && !admin {
			continue
		}
		pub := p.Public(admin)
		if !admin {
			var mine []llm.KeyInfo
			for i, ki := range pub.KeyInfo {
				if llm.OwnsKey(p.Keys[i], owner) {
					mine = append(mine, ki)
				}
			}
			pub.KeyInfo = mine
		}
		if rec, hasDB := dbKeys[p.ID]; hasDB {
			pub.KeyInfo = append(pub.KeyInfo, llm.KeyInfo{
				N:     len(p.Keys) + 1,
				Label: "личный",
				Tail:  rec.Mask,
			})
		}
		if !admin && len(pub.KeyInfo) == 0 {
			continue
		}
		out = append(out, pub)
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
		Chain:     chainNames(access.QuizChainWithVault(s.cfg, s.st, s.vault, id)),
		Default:   chainNames(def),
		NotesWith: chainNames(llm.PoolChain(s.cfg)),
	}
}

func (s *Server) handleLLMGet(w http.ResponseWriter, r *http.Request) {
	if !s.needStore(w) {
		return
	}
	writeJSON(w, s.llmStateFor(s.myID()))
}

// providerFor — провайдер есть (в конфиге или встроенный бесплатный) и доступен вошедшему.
func (s *Server) providerFor(id string) (llm.Provider, bool) {
	if p, ok := llm.Find(s.cfg, id); ok {
		if p.AdminOnly && !s.viewerIsAdmin() {
			return llm.Provider{}, false
		}
		return p, true
	}
	return llm.BuiltinProvider(id)
}

func (s *Server) handleLLMModels(w http.ResponseWriter, r *http.Request) {
	if s.demo {
		writeErr(w, http.StatusForbidden, "в демо-режиме запросы к внешним LLM API отключены")
		return
	}
	p, ok := s.providerFor(r.URL.Query().Get("provider"))
	if !ok {
		writeErr(w, http.StatusBadRequest, "нет такого провайдера")
		return
	}
	id := s.myID()
	var userSecret string
	if s.st != nil && s.vault != nil && isPersonalKeyProvider(p.ID) {
		if rec, err := s.st.GetLLMKey(id, p.ID); err == nil && rec != nil {
			if plain, oerr := s.vault.Open(rec.SecretEnc); oerr == nil {
				userSecret = strings.TrimSpace(plain)
			}
		}
	}
	// Свой ключ из .env (подпись — фамилия однозначная или свой telegram_id).
	// Сначала свой ключ из .env; админу ниже есть фолбэк на ключ провайдера.
	if userSecret == "" {
		owner := access.KeyOwner(s.st, id)
		for _, k := range p.Keys {
			if llm.OwnsKey(k, owner) {
				userSecret = k.Secret
				break
			}
		}
	}
	// Фолбэк только админу, и только когда своего ключа нет (решение заказчика):
	// AdminOnly-провайдер (cheaper) — ключ админа LLM_API_KEY; обычный провайдер —
	// его первый ключ, как было раньше. Не-админу фолбэка нет.
	if userSecret == "" && s.viewerIsAdmin() && len(p.Keys) > 0 {
		userSecret = p.Keys[0].Secret
	}
	if userSecret == "" {
		writeErr(w, http.StatusForbidden, "сначала добавь свой ключ для этого провайдера: личный в панели или подписанный в .env своим telegram_id")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	models, err := llm.ListModelsWithKey(ctx, p, userSecret)
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
	p, ok := s.providerFor(ch.Provider)
	if !ok {
		writeErr(w, http.StatusBadRequest, "нет такого провайдера")
		return ch, false
	}
	hasDBKey := false
	if s.st != nil && isPersonalKeyProvider(p.ID) {
		if rec, err := s.st.GetLLMKey(s.myID(), p.ID); err == nil && rec != nil {
			hasDBKey = true
		}
	}
	maxKey := len(p.Keys)
	if hasDBKey {
		maxKey++
	}
	if ch.Key < 0 || ch.Key > maxKey {
		writeErr(w, http.StatusBadRequest, "нет такого ключа")
		return ch, false
	}
	// Не админ — только свои ключи (из БД или подписанные в .env).
	if !s.viewerIsAdmin() {
		owner := access.KeyOwner(s.st, s.myID())
		if ch.Key > 0 {
			isDBIndex := hasDBKey && ch.Key == len(p.Keys)+1
			if !isDBIndex && !llm.OwnsKey(p.Keys[ch.Key-1], owner) {
				writeErr(w, http.StatusForbidden, "это не твой ключ")
				return ch, false
			}
		}
		own := hasDBKey
		for _, k := range p.Keys {
			own = own || llm.OwnsKey(k, owner)
		}
		if !own {
			writeErr(w, http.StatusForbidden, "у тебя нет ключей этого провайдера")
			return ch, false
		}
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
	if s.demo {
		writeErr(w, http.StatusForbidden, "в демо-режиме проверка LLM API отключена")
		return
	}
	ch, ok := s.decodeChoice(w, r)
	if !ok {
		return
	}
	if ch.Provider == "" {
		writeErr(w, http.StatusBadRequest, "выбери провайдера")
		return
	}
	id := s.myID()
	userKeys := access.UserKeysFromVault(s.st, s.vault, id)
	chain := llm.ChainForTest(s.cfg, ch, s.viewerIsAdmin(), access.KeyOwner(s.st, id), userKeys)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	res, err := llm.Ask(ctx, chain, "", "Ответь одним словом: работает")
	out := map[string]any{"ok": err == nil, "attempts": res.Attempts, "text": res.Text}
	if err != nil {
		out["error"] = err.Error()
	}
	writeJSON(w, out)
}

type llmKeyView struct {
	Provider      string `json:"provider"`
	Name          string `json:"name"`
	Configured    bool   `json:"configured"`
	Mask          string `json:"mask,omitempty"`
	SharePool     bool   `json:"share_pool"`
	UpdatedAt     string `json:"updated_at,omitempty"`
	CooldownUntil string `json:"cooldown_until,omitempty"`
	CooldownSec   int    `json:"cooldown_sec"`
}

type llmLimitView struct {
	Provider               string `json:"provider"`
	Name                   string `json:"name"`
	Day                    string `json:"day"`
	Configured             bool   `json:"configured"`
	Requests               int    `json:"requests"`
	InputTokens            int    `json:"input_tokens"`
	OutputTokens           int    `json:"output_tokens"`
	EstimatedDailyRequests int    `json:"estimated_daily_requests"`
	RemainingEstimate      int    `json:"remaining_estimate"`
	EstimatedNote          string `json:"estimated_note"`
	CooldownUntil          string `json:"cooldown_until,omitempty"`
	CooldownSec            int    `json:"cooldown_sec"`
}

func (s *Server) effectiveCooldown(id int64, provider, secret, day string, now time.Time) (time.Time, int) {
	var until time.Time
	if secret != "" {
		if memUntil, active := llm.KeyCooldownUntil(secret); active && memUntil.After(now) {
			until = memUntil
		}
	}
	if s.st != nil {
		if u, err := s.st.GetLLMUsage(id, provider, day); err == nil && u != nil && u.CooldownUntil != nil && u.CooldownUntil.After(now) {
			if until.IsZero() || u.CooldownUntil.After(until) {
				until = *u.CooldownUntil
			}
		}
	}
	if until.IsZero() || !until.After(now) {
		return time.Time{}, 0
	}
	sec := int(math.Ceil(until.Sub(now).Seconds()))
	if sec < 1 {
		sec = 1
	}
	return until, sec
}

func (s *Server) buildLLMKeyView(id int64, pid string, rec *store.LLMKeyRecord, day string, now time.Time) llmKeyView {
	bp, _ := llm.BuiltinProvider(pid)
	view := llmKeyView{
		Provider: pid,
		Name:     bp.Name,
	}
	var secret string
	if rec != nil {
		view.Configured = true
		view.Mask = rec.Mask
		view.SharePool = rec.SharePool
		if !rec.UpdatedAt.IsZero() {
			view.UpdatedAt = rec.UpdatedAt.UTC().Format(time.RFC3339)
		}
		if s.vault != nil {
			if plain, err := s.vault.Open(rec.SecretEnc); err == nil {
				secret = strings.TrimSpace(plain)
			}
		}
	}
	until, sec := s.effectiveCooldown(id, pid, secret, day, now)
	if sec > 0 {
		view.CooldownUntil = until.UTC().Format(time.RFC3339)
		view.CooldownSec = sec
	}
	return view
}

func (s *Server) handleLLMKeysList(w http.ResponseWriter, r *http.Request) {
	if !s.needStore(w) {
		return
	}
	id := s.myID()
	now := s.clock().UTC()
	// Единый день в UTC для согласованности с access.InitLLMBridge.
	day := now.Format("2006-01-02")
	dbKeys := s.personalKeyMap(id)

	out := make([]llmKeyView, 0, len(personalProviders))
	for _, pid := range personalProviders {
		var recPtr *store.LLMKeyRecord
		if rec, ok := dbKeys[pid]; ok {
			rCopy := rec
			recPtr = &rCopy
		}
		out = append(out, s.buildLLMKeyView(id, pid, recPtr, day, now))
	}
	writeJSON(w, map[string]any{"keys": out, "vault_ready": s.vault != nil})
}

func (s *Server) handleLLMKeyPut(w http.ResponseWriter, r *http.Request) {
	if s.demo {
		writeErr(w, http.StatusForbidden, "в демо-режиме управление API-ключами отключено")
		return
	}
	if !s.needStore(w) {
		return
	}
	if s.vault == nil {
		writeErr(w, http.StatusServiceUnavailable, "шифрование ключей не настроено")
		return
	}
	provider := strings.ToLower(strings.TrimSpace(r.PathValue("provider")))
	if !isPersonalKeyProvider(provider) {
		writeErr(w, http.StatusBadRequest, "личный ключ доступен только для gemini или opencode")
		return
	}
	var req struct {
		Key       string `json:"key"`
		SharePool bool   `json:"share_pool"`
	}
	if err := decodeJSON(r.Body, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}

	id := s.myID()
	now := s.clock().UTC()
	day := now.Format("2006-01-02")
	key := strings.TrimSpace(req.Key)

	if key == "" {
		existing, err := s.st.GetLLMKey(id, provider)
		if err != nil {
			writeStoreErr(w, err)
			return
		}
		if existing == nil {
			writeErr(w, http.StatusBadRequest, "введи API-ключ")
			return
		}
		existing.SharePool = req.SharePool
		existing.UpdatedAt = now
		if err := s.st.PutLLMKey(*existing); err != nil {
			writeStoreErr(w, err)
			return
		}
		logx.Infof("panel", "llm key share tg=%d provider=%s share=%v", id, provider, req.SharePool)
		writeJSON(w, map[string]any{"ok": true, "key": s.buildLLMKeyView(id, provider, existing, day, now)})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	if _, err := llm.TestProviderKey(ctx, s.cfg, provider, key); err != nil {
		msg := strings.ReplaceAll(err.Error(), key, "***")
		logx.Warnf("panel", "llm key probe failed tg=%d provider=%s: %s", id, provider, msg)
		writeErr(w, http.StatusBadRequest, "проверка ключа не прошла: "+msg)
		return
	}

	enc, err := s.vault.Seal(key)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "ошибка шифрования ключа")
		return
	}
	rec := store.LLMKeyRecord{
		TelegramID: id,
		Provider:   provider,
		SecretEnc:  enc,
		Mask:       store.MaskKey(key),
		SharePool:  req.SharePool,
		UpdatedAt:  now,
	}
	if err := s.st.PutLLMKey(rec); err != nil {
		writeStoreErr(w, err)
		return
	}
	logx.Infof("panel", "llm key saved tg=%d provider=%s mask=%s share=%v", id, provider, rec.Mask, rec.SharePool)
	writeJSON(w, map[string]any{"ok": true, "key": s.buildLLMKeyView(id, provider, &rec, day, now)})
}

func (s *Server) handleLLMKeyDelete(w http.ResponseWriter, r *http.Request) {
	if s.demo {
		writeErr(w, http.StatusForbidden, "в демо-режиме управление API-ключами отключено")
		return
	}
	if !s.needStore(w) {
		return
	}
	provider := strings.ToLower(strings.TrimSpace(r.PathValue("provider")))
	if !isPersonalKeyProvider(provider) {
		writeErr(w, http.StatusBadRequest, "неизвестный провайдер")
		return
	}
	id := s.myID()
	if err := s.st.DeleteLLMKey(id, provider); err != nil {
		writeStoreErr(w, err)
		return
	}
	logx.Infof("panel", "llm key deleted tg=%d provider=%s", id, provider)
	writeJSON(w, map[string]any{"ok": true, "provider": provider})
}

func (s *Server) handleLLMLimits(w http.ResponseWriter, r *http.Request) {
	if !s.needStore(w) {
		return
	}
	id := s.myID()
	now := s.clock().UTC()
	// Единый день в UTC для согласованности с access.InitLLMBridge.
	day := now.Format("2006-01-02")
	dbKeys := s.personalKeyMap(id)

	out := make([]llmLimitView, 0, len(personalProviders))
	for _, pid := range personalProviders {
		bp, _ := llm.BuiltinProvider(pid)
		estDaily := estimatedGeminiDailyRequests
		if pid == llm.OpenCode {
			estDaily = estimatedOpenCodeDailyRequests
		}
		item := llmLimitView{
			Provider:               pid,
			Name:                   bp.Name,
			Day:                    day,
			EstimatedDailyRequests: estDaily,
			RemainingEstimate:      estDaily,
			EstimatedNote:          "ориентировочная оценка бесплатного тарифа",
		}
		var secret string
		if rec, ok := dbKeys[pid]; ok {
			item.Configured = true
			if s.vault != nil {
				if plain, err := s.vault.Open(rec.SecretEnc); err == nil {
					secret = strings.TrimSpace(plain)
				}
			}
		}
		if u, err := s.st.GetLLMUsage(id, pid, day); err == nil && u != nil {
			item.Requests = u.Requests
			item.InputTokens = u.InputTokens
			item.OutputTokens = u.OutputTokens
			rem := estDaily - u.Requests
			if rem < 0 {
				rem = 0
			}
			item.RemainingEstimate = rem
		}
		until, sec := s.effectiveCooldown(id, pid, secret, day, now)
		if sec > 0 {
			item.CooldownUntil = until.UTC().Format(time.RFC3339)
			item.CooldownSec = sec
		}
		out = append(out, item)
	}
	writeJSON(w, map[string]any{"day": day, "providers": out})
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
		chain := access.QuizChainWithVault(s.cfg, s.st, s.vault, id)
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

// markQuizForce — снять отметки «решён / неудачи / прислан текст» и поставить
// «пройти принудительно». Пинок синка здесь не делается: массовая постановка
// пинает один раз на всех.
func (s *Server) markQuizForce(id, cmid int64) error {
	for _, k := range []string{
		fmt.Sprintf("quiz_solved:%d:%d", id, cmid),
		fmt.Sprintf("quiz_fail:%d:%d", id, cmid),
		fmt.Sprintf("quiz_parsed:%d:%d", id, cmid),
	} {
		if err := s.st.SetSetting(k, ""); err != nil {
			return err
		}
	}
	return s.st.SetSetting(uni.QuizForceKey(id, cmid), "1")
}

// requeueQuiz — markQuizForce и пнуть синк Moodle (подхватит за ~5 с).
func (s *Server) requeueQuiz(id, cmid int64) error {
	if err := s.markQuizForce(id, cmid); err != nil {
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
	if s.quizNotOpen(r.Context(), req.CMID) {
		writeErr(w, http.StatusConflict, "Тест ещё не открыт: «Пройти» станет доступно, когда он откроется.")
		return
	}
	if err := s.requeueQuiz(s.myID(), req.CMID); err != nil {
		writeStoreErr(w, err)
		return
	}
	logx.Infof("panel", "quiz force tg=%d cmid=%d", s.myID(), req.CMID)
	writeJSON(w, map[string]any{"ok": true, "message": "Запускаю: бот зайдёт в тест в течение минуты, результат придёт в Telegram."})
}

// quizNotOpen — тест с этим cmid в «Сдать» ещё не открыт. Если его в списке нет,
// ответ «нет» (как раньше): решает то же правило, что и в списке.
func (s *Server) quizNotOpen(ctx context.Context, cmid int64) bool {
	if !s.moodle.Status().Connected {
		return false
	}
	list, err := s.moodle.Deadlines(ctx, s.now())
	if err != nil {
		return false
	}
	for _, d := range list {
		if d.ID == strconv.FormatInt(cmid, 10) {
			return d.NotOpen
		}
	}
	return false
}

// handleQuizForceAll — «Пройти все»: поставить на авторешение каждый несданный
// тест из «Сдать». Список тот же, что у /api/deadlines, поэтому число совпадает
// с кнопкой. Пинок синка один раз, если что-то поставлено.
func (s *Server) handleQuizForceAll(w http.ResponseWriter, r *http.Request) {
	if !s.needStore(w) {
		return
	}
	id := s.myID()
	n := 0
	if s.moodle.Status().Connected {
		list, err := s.moodle.Deadlines(r.Context(), s.now())
		if err != nil {
			writeStoreErr(w, err)
			return
		}
		for _, d := range list {
			cmid, perr := strconv.ParseInt(d.ID, 10, 64)
			// Ещё не открытые тесты в авторешение не ставим: откроются — тогда и решим.
			if d.Kind != DeadlineQuiz || d.Status == DeadlineSubmitted || d.Closed || d.NotOpen || perr != nil || cmid <= 0 {
				continue
			}
			if err := s.markQuizForce(id, cmid); err != nil {
				writeStoreErr(w, err)
				return
			}
			n++
		}
	}
	if n > 0 {
		if err := uni.Kick(s.st, id); err != nil {
			writeStoreErr(w, err)
			return
		}
	}
	logx.Infof("panel", "quiz force all tg=%d n=%d", id, n)
	writeJSON(w, map[string]any{"ok": true, "count": n})
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
