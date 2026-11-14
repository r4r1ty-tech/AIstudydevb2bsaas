package webapp

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/access"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/llm"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/panelpass"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/vault"
)

type syncLogBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncLogBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncLogBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newLLMKeysTestServer(t *testing.T, geminiEnvKeys []string) (*Server, *store.Store, []byte) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "bot.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		access.InitLLMBridge(nil, nil)
		_ = st.Close()
	})
	keyFile := filepath.Join(dir, "secret.key")
	s := New(&config.Config{
		ListenAddr:    config.DefaultListen,
		Timezone:      config.DefaultTimezone,
		AdminID:       config.DefaultAdminID,
		Whitelist:     append([]int64(nil), config.DefaultWhitelist...),
		SecretKeyFile: keyFile,
		DBPath:        dbPath,
		GeminiAPIKeys: geminiEnvKeys,
	}, st, nil, "")
	v, err := vault.Load("", keyFile)
	if err != nil {
		t.Fatal(err)
	}
	return s, st, v.Derive(panelpass.Label)
}

func TestLLMKeysPutGetShareDeleteAndNoSecretLeak(t *testing.T) {
	var logs syncLogBuf
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	origURL := llm.GeminiURL
	t.Cleanup(func() { llm.GeminiURL = origURL })

	var probeCalls atomic.Int32
	badKey := "bad-secret-9999-fail"
	goodKey := "abcd-secret-key-wxyz"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probeCalls.Add(1)
		auth := r.Header.Get("Authorization")
		if auth == "Bearer "+badKey {
			// Специально возвращаем ключ в теле ошибки, чтобы проверить очистку.
			http.Error(w, `{"error":"invalid api key `+badKey+`"}`, http.StatusUnauthorized)
			return
		}
		if auth == "Bearer "+goodKey {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"работает"}}],"usage":{"prompt_tokens":10,"completion_tokens":3}}`))
			return
		}
		http.Error(w, `{"error":"unexpected key"}`, http.StatusForbidden)
	}))
	defer srv.Close()
	llm.GeminiURL = srv.URL

	s, st, passKey := newLLMKeysTestServer(t, nil)
	student := config.DefaultWhitelist[1]

	// 1. Запрещено сохранять ключ для cheaper.
	if rec := reqAs(s, passKey, student, http.MethodPut, "/api/llm/keys/cheaper", `{"key":"abcd-secret-key-wxyz"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT cheaper = %d, ждали 400", rec.Code)
	}

	// 2. При ошибке пробы ключ НЕ сохраняется, секрет не утекает ни в ответ, ни в лог.
	rec := reqAs(s, passKey, student, http.MethodPut, "/api/llm/keys/gemini", `{"key":"`+badKey+`","share_pool":true}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT плохой ключ = %d %s, ждали 400", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), badKey) {
		t.Fatalf("плохой секрет утёк в ответ: %s", rec.Body)
	}
	if got, err := st.GetLLMKey(student, llm.Gemini); err != nil || got != nil {
		t.Fatalf("ключ не должен был сохраниться при провале пробы: %+v, err=%v", got, err)
	}

	// 3. При успешной пробе ключ сохраняется зашифрованным, а GET отдаёт только маску.
	rec = reqAs(s, passKey, student, http.MethodPut, "/api/llm/keys/gemini", `{"key":"`+goodKey+`","share_pool":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT хороший ключ = %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), goodKey) {
		t.Fatalf("секрет утёк в ответ PUT: %s", rec.Body)
	}
	dbRec, err := st.GetLLMKey(student, llm.Gemini)
	if err != nil || dbRec == nil {
		t.Fatalf("ключ не сохранён в БД: %v %v", dbRec, err)
	}
	if dbRec.SecretEnc == goodKey || dbRec.Mask != "abcd...wxyz" || dbRec.SharePool {
		t.Fatalf("некорректная запись в БД: %+v", dbRec)
	}

	rec = reqAs(s, passKey, student, http.MethodGet, "/api/llm/keys", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/llm/keys = %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), goodKey) {
		t.Fatalf("полный секрет утёк в GET /api/llm/keys: %s", rec.Body)
	}
	var keysResp struct {
		Keys []llmKeyView `json:"keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &keysResp); err != nil {
		t.Fatalf("unmarshal keys: %v", err)
	}
	if len(keysResp.Keys) != 2 || !keysResp.Keys[0].Configured || keysResp.Keys[0].Mask != "abcd...wxyz" || keysResp.Keys[0].SharePool {
		t.Fatalf("неожиданный GET /api/llm/keys: %+v", keysResp.Keys)
	}

	// 4. Переключение share_pool при key == "" без повторной пробы.
	callsBefore := probeCalls.Load()
	rec = reqAs(s, passKey, student, http.MethodPut, "/api/llm/keys/gemini", `{"key":"","share_pool":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT share_pool toggle = %d %s", rec.Code, rec.Body)
	}
	if probeCalls.Load() != callsBefore {
		t.Fatalf("переключение share_pool вызвало повторную пробу: до=%d после=%d", callsBefore, probeCalls.Load())
	}
	dbRec, _ = st.GetLLMKey(student, llm.Gemini)
	if dbRec == nil || !dbRec.SharePool {
		t.Fatalf("share_pool не обновился в БД: %+v", dbRec)
	}

	// 5. Личный ключ отображается в /api/llm без секрета (даже если в .env нет ключей Gemini).
	rec = reqAs(s, passKey, student, http.MethodGet, "/api/llm", "")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), goodKey) {
		t.Fatalf("GET /api/llm = %d %s", rec.Code, rec.Body)
	}
	var stView llmState
	_ = json.Unmarshal(rec.Body.Bytes(), &stView)
	if len(stView.Providers) != 1 || len(stView.Providers[0].KeyInfo) != 1 || stView.Providers[0].KeyInfo[0].Tail != "abcd...wxyz" {
		t.Fatalf("личный ключ должен быть виден в /api/llm с маской: %+v", stView.Providers)
	}

	// 6. Удаление ключа.
	if rec := reqAs(s, passKey, student, http.MethodDelete, "/api/llm/keys/gemini", ""); rec.Code != http.StatusOK {
		t.Fatalf("DELETE /api/llm/keys/gemini = %d %s", rec.Code, rec.Body)
	}
	rec = reqAs(s, passKey, student, http.MethodGet, "/api/llm/keys", "")
	keysResp.Keys = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &keysResp); err != nil {
		t.Fatalf("unmarshal keys after delete: %v", err)
	}
	if keysResp.Keys[0].Configured || keysResp.Keys[0].Mask != "" {
		t.Fatalf("ключ не удалён: %+v", keysResp.Keys[0])
	}

	// 7. Полный ключ ни разу не попал в лог.
	logged := logs.String()
	if strings.Contains(logged, badKey) || strings.Contains(logged, goodKey) {
		t.Fatalf("полный секрет утёк в логи: %s", logged)
	}
}

func TestLLMModelsIsolationAndOwnKey(t *testing.T) {
	origURL := llm.GeminiURL
	t.Cleanup(func() { llm.GeminiURL = origURL })

	var modelRequests atomic.Int32
	var lastAuth atomic.Value
	strangerKey := "stranger-env-secret-1"
	ownKey := "myown-gemini-key-5678"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			modelRequests.Add(1)
			lastAuth.Store(r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"data":[{"id":"models/gemini-4-hydrogen"},{"id":"models/gemini-3.6-flash"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"работает"}}]}`))
	}))
	defer srv.Close()
	llm.GeminiURL = srv.URL

	// В .env есть чужой ключ strangerKey, но у студента своих ключей нет.
	s, _, passKey := newLLMKeysTestServer(t, []string{"Чужой:" + strangerKey})
	student := config.DefaultWhitelist[1]

	// Без своего ключа студент получает 403, чужой p.Keys[0] НЕ тратится.
	rec := reqAs(s, passKey, student, http.MethodGet, "/api/llm/models?provider=gemini", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("GET /api/llm/models без своего ключа = %d %s, ждали 403", rec.Code, rec.Body)
	}
	if modelRequests.Load() != 0 {
		t.Fatalf("при 403 не должно быть запросов к провайдеру, было %d", modelRequests.Load())
	}

	// Добавляем студенту свой ключ.
	rec = reqAs(s, passKey, student, http.MethodPut, "/api/llm/keys/gemini", `{"key":"`+ownKey+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT свой ключ = %d %s", rec.Code, rec.Body)
	}

	// Теперь /api/llm/models идёт строго со своим ключом студента.
	rec = reqAs(s, passKey, student, http.MethodGet, "/api/llm/models?provider=gemini", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/llm/models со своим ключом = %d %s", rec.Code, rec.Body)
	}
	if modelRequests.Load() != 1 {
		t.Fatalf("ожидали 1 запрос /models, получили %d", modelRequests.Load())
	}
	if gotAuth, _ := lastAuth.Load().(string); gotAuth != "Bearer "+ownKey {
		t.Fatalf("/models ушёл с чужим ключом %q вместо %q", gotAuth, "Bearer "+ownKey)
	}
}

func TestLLMLimitsUsageAndCooldownAfter429(t *testing.T) {
	origURL := llm.GeminiURL
	t.Cleanup(func() { llm.GeminiURL = origURL })

	var mode atomic.Int32 // 0 = ok (probe), 1 = ok with tokens (test), 2 = 429
	myKey := "limit-gemini-key-9999"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode.Load() {
		case 0:
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"работает"}}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`))
		case 1:
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"работает"}}],"usage":{"prompt_tokens":250,"completion_tokens":75}}`))
		case 2:
			w.Header().Set("Retry-After", "120")
			http.Error(w, `{"error":"quota exceeded"}`, http.StatusTooManyRequests)
		}
	}))
	defer srv.Close()
	llm.GeminiURL = srv.URL

	s, _, passKey := newLLMKeysTestServer(t, nil)
	student := config.DefaultWhitelist[1]

	// Сохраняем ключ (проба с OwnerID=0 ещё не пишет usage, пока ключ не в БД).
	rec := reqAs(s, passKey, student, http.MethodPut, "/api/llm/keys/gemini", `{"key":"`+myKey+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT key = %d %s", rec.Code, rec.Body)
	}

	// Успешный вызов через /api/llm/test увеличивает requests и токены.
	mode.Store(1)
	rec = reqAs(s, passKey, student, http.MethodPost, "/api/llm/test", `{"provider":"gemini","model":"gemini-3.6-flash","key":1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/llm/test = %d %s", rec.Code, rec.Body)
	}

	var limResp struct {
		Day       string         `json:"day"`
		Providers []llmLimitView `json:"providers"`
	}
	rec = reqAs(s, passKey, student, http.MethodGet, "/api/llm/limits", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/llm/limits = %d %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &limResp); err != nil {
		t.Fatalf("unmarshal limits: %v", err)
	}
	if len(limResp.Providers) != 2 {
		t.Fatalf("ожидали 2 провайдера в limits, получили %d", len(limResp.Providers))
	}
	gem := limResp.Providers[0]
	if gem.Requests != 1 || gem.InputTokens != 250 || gem.OutputTokens != 75 || gem.CooldownSec != 0 {
		t.Fatalf("после успешного запроса limits = %+v", gem)
	}
	if gem.EstimatedDailyRequests <= 0 || gem.RemainingEstimate != gem.EstimatedDailyRequests-1 {
		t.Fatalf("оценка остатка некорректна: %+v", gem)
	}

	// Вызов с 429 переводит ключ в cooldown и отражается в /api/llm/limits.
	mode.Store(2)
	_ = reqAs(s, passKey, student, http.MethodPost, "/api/llm/test", `{"provider":"gemini","model":"gemini-3.6-flash","key":1}`)

	rec = reqAs(s, passKey, student, http.MethodGet, "/api/llm/limits", "")
	_ = json.Unmarshal(rec.Body.Bytes(), &limResp)
	gem = limResp.Providers[0]
	if gem.CooldownSec <= 0 || gem.CooldownUntil == "" {
		t.Fatalf("после 429 ожидался активный cooldown в limits: %+v", gem)
	}
}
