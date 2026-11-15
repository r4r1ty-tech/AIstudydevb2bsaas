package access

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/llm"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/vault"
)

func openAccessTestEnv(t *testing.T) (*store.Store, *vault.Vault) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bot.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	v, err := vault.Load("", filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatalf("vault.Load: %v", err)
	}
	return st, v
}

func TestQuizChainWithVaultIsolation(t *testing.T) {
	st, v := openAccessTestEnv(t)
	cfg := &config.Config{
		AdminID:       1,
		GeminiAPIKeys: []string{"Админ:adm-env-key"},
	}

	const userA int64 = 1001
	const userB int64 = 1002

	encA, err := v.Seal("secret-user-a-12345")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if err := st.PutLLMKey(store.LLMKeyRecord{
		TelegramID: userA,
		Provider:   llm.Gemini,
		SecretEnc:  encA,
		Mask:       store.MaskKey("secret-user-a-12345"),
		SharePool:  false,
	}); err != nil {
		t.Fatalf("PutLLMKey: %v", err)
	}

	chainA := QuizChainWithVault(cfg, st, v, userA)
	if len(chainA) == 0 {
		t.Fatal("у пользователя A ожидалась непустая цепочка с личным ключом")
	}
	for _, tgt := range chainA {
		if !tgt.MatchesSecret("secret-user-a-12345") || tgt.OwnerID != userA {
			t.Fatalf("неожиданный таргет в цепочке A: %+v", tgt)
		}
	}

	// У пользователя B своего ключа нет: ключ A к нему попасть не должен.
	chainB := QuizChainWithVault(cfg, st, v, userB)
	if len(chainB) != 0 {
		t.Fatalf("у пользователя B без ключей цепочка должна быть пустой, получили: %+v", chainB)
	}

	// При v == nil работает как старая QuizChain (только ключи .env).
	if noVault := QuizChainWithVault(cfg, st, nil, userA); len(noVault) != 0 {
		t.Fatalf("при v==nil личный ключ из БД не должен расшифровываться: %+v", noVault)
	}
}

func TestInitLLMBridgeSharedPoolAndUsage(t *testing.T) {
	st, v := openAccessTestEnv(t)
	InitLLMBridge(st, v)
	t.Cleanup(func() { InitLLMBridge(nil, nil) })

	origURL := llm.GeminiURL
	t.Cleanup(func() { llm.GeminiURL = origURL })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if strings.Contains(auth, "secret-rate-limited") {
			w.Header().Set("Retry-After", "90")
			http.Error(w, `{"error":"rate limit"}`, http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ответ"}}],"usage":{"prompt_tokens":44,"completion_tokens":18}}`))
	}))
	defer srv.Close()
	llm.GeminiURL = srv.URL

	const privateUser int64 = 2001
	const sharedUser int64 = 2002

	encPriv, _ := v.Seal("secret-private-only-0001")
	encShared, _ := v.Seal("secret-shared-pool-0002")
	encRate, _ := v.Seal("secret-rate-limited-0003")

	if err := st.PutLLMKey(store.LLMKeyRecord{
		TelegramID: privateUser,
		Provider:   llm.Gemini,
		SecretEnc:  encPriv,
		Mask:       store.MaskKey("secret-private-only-0001"),
		SharePool:  false,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutLLMKey(store.LLMKeyRecord{
		TelegramID: sharedUser,
		Provider:   llm.Gemini,
		SecretEnc:  encShared,
		Mask:       store.MaskKey("secret-shared-pool-0002"),
		SharePool:  true,
	}); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{}
	poolChain := llm.PoolChain(cfg)
	if len(poolChain) != len(llm.GeminiModels) {
		t.Fatalf("в PoolChain должен попасть только 1 расшаренный ключ (%d моделей), получили %d", len(llm.GeminiModels), len(poolChain))
	}
	for _, tgt := range poolChain {
		if tgt.MatchesSecret("secret-private-only-0001") {
			t.Fatal("нерасшаренный ключ попал в PoolChain")
		}
		if !tgt.MatchesSecret("secret-shared-pool-0002") || tgt.OwnerID != sharedUser {
			t.Fatalf("в PoolChain ожидался ключ sharedUser, получили %+v", tgt)
		}
	}

	// Запрос через личный ключ с OwnerID=sharedUser фиксирует токены и запросы в llm_usage.
	pool := llm.NewKeyPool()
	if _, err := llm.AskWithPool(context.Background(), pool, poolChain[:1], "", "тест"); err != nil {
		t.Fatalf("AskWithPool: %v", err)
	}
	day := time.Now().UTC().Format("2006-01-02")
	u, err := st.GetLLMUsage(sharedUser, llm.Gemini, day)
	if err != nil || u == nil {
		t.Fatalf("GetLLMUsage: %v %v", u, err)
	}
	if u.Requests != 1 || u.InputTokens != 44 || u.OutputTokens != 18 {
		t.Fatalf("неожиданный расход в llm_usage: %+v", u)
	}

	// Проверяем поиск владельца по секрету при OwnerID == 0 и запись cooldown после 429.
	const rateUser int64 = 2003
	if err := st.PutLLMKey(store.LLMKeyRecord{
		TelegramID: rateUser,
		Provider:   llm.Gemini,
		SecretEnc:  encRate,
		Mask:       store.MaskKey("secret-rate-limited-0003"),
		SharePool:  false,
	}); err != nil {
		t.Fatal(err)
	}
	rateTarget, ok := llm.ProviderWithUserKey(cfg, llm.Gemini, "secret-rate-limited-0003", 0)
	if !ok {
		t.Fatal("ProviderWithUserKey вернул false")
	}
	if _, err := llm.AskWithPool(context.Background(), pool, []llm.Target{rateTarget}, "", "тест"); err == nil {
		t.Fatal("ожидали ошибку 429")
	}
	uRate, err := st.GetLLMUsage(rateUser, llm.Gemini, day)
	if err != nil || uRate == nil || uRate.CooldownUntil == nil || !uRate.CooldownUntil.After(time.Now().UTC()) {
		t.Fatalf("cooldown не записался в llm_usage для rateUser: %+v, err=%v", uRate, err)
	}
}

func TestIsAdminIgnoresLoginSuffix(t *testing.T) {
	st, _ := openAccessTestEnv(t)
	cfg := &config.Config{
		AdminID:          100,
		AdminLoginSuffix: "528",
	}
	const userID int64 = 200
	if err := st.PutAccount(model.Account{TelegramID: userID, Kind: model.AccountMoodle, Login: "2021-000123"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutAccount(model.Account{TelegramID: userID, Kind: model.AccountLK, Login: "2021-000123"}); err != nil {
		t.Fatal(err)
	}

	if IsAdmin(cfg, st, userID) {
		t.Fatal("логин с суффиксом 528 не должен давать права администратора")
	}
	if !IsAdmin(cfg, st, 100) {
		t.Fatal("AdminID из конфига должен быть администратором")
	}
	if IsAdmin(cfg, st, 0) {
		t.Fatal("id=0 не должен быть администратором")
	}
}

func TestOwnerLookupCacheAvoidsRepeatedDecrypt(t *testing.T) {
	st, v := openAccessTestEnv(t)
	encUser, err := v.Seal("user-db-secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutLLMKey(store.LLMKeyRecord{
		TelegramID: 3001,
		Provider:   llm.Gemini,
		SecretEnc:  encUser,
		Mask:       store.MaskKey("user-db-secret"),
	}); err != nil {
		t.Fatal(err)
	}

	var openCalls atomic.Int32
	now := time.Unix(1_700_000_000, 0).UTC()
	cache := newOwnerLookupCache(st, v)
	cache.nowFn = func() time.Time { return now }
	cache.openSecret = func(sealed string) (string, error) {
		openCalls.Add(1)
		return v.Open(sealed)
	}

	envHash := sha256.Sum256([]byte("env-only-secret"))
	userHash := sha256.Sum256([]byte("user-db-secret"))

	// Первый поиск строит кэш (1 вызов v.Open).
	if got := cache.lookup(llm.Gemini, envHash); got != 0 {
		t.Fatalf("для .env-ключа ожидали ownerID=0, получили %d", got)
	}
	if openCalls.Load() != 1 {
		t.Fatalf("ожидали 1 вызов Open при построении кэша, получили %d", openCalls.Load())
	}

	// Повторные запросы (и с .env-ключом, и с ключом из БД) не вызывают v.Open.
	for i := 0; i < 5; i++ {
		if got := cache.lookup(llm.Gemini, envHash); got != 0 {
			t.Fatalf("повтор .env-ключа: %d", got)
		}
		if got := cache.lookup(llm.Gemini, userHash); got != 3001 {
			t.Fatalf("повтор личного ключа: получили %d, ждали 3001", got)
		}
	}
	if openCalls.Load() != 1 {
		t.Fatalf("при неизменной таблице Open не должен вызываться повторно, было %d", openCalls.Load())
	}

	// Добавление нового ключа в БД сразу инвалидирует кэш.
	encSecond, _ := v.Seal("second-db-secret")
	if err := st.PutLLMKey(store.LLMKeyRecord{
		TelegramID: 3002,
		Provider:   llm.Gemini,
		SecretEnc:  encSecond,
		Mask:       store.MaskKey("second-db-secret"),
	}); err != nil {
		t.Fatal(err)
	}
	secondHash := sha256.Sum256([]byte("second-db-secret"))
	if got := cache.lookup(llm.Gemini, secondHash); got != 3002 {
		t.Fatalf("новый ключ после инвалидации не найден: %d", got)
	}
	if openCalls.Load() != 3 { // 1 + 2 записи при пересборке
		t.Fatalf("ожидали 3 вызова Open после пересборки, получили %d", openCalls.Load())
	}

	// По истечении TTL кэш перестраивается.
	now = now.Add(ownerCacheTTL + time.Second)
	if got := cache.lookup(llm.Gemini, envHash); got != 0 {
		t.Fatalf("после TTL для .env-ключа ожидали 0, получили %d", got)
	}
	if openCalls.Load() != 5 {
		t.Fatalf("после TTL ожидали 5 вызовов Open, получили %d", openCalls.Load())
	}
}

// Аккаунт с ФИО из ЛК: фамилия из lk_name попадает в KeyOwner.
func addOwnerAccount(t *testing.T, st *store.Store, id int64, fio string) {
	t.Helper()
	if err := st.PutAccount(model.Account{TelegramID: id, Kind: model.AccountMoodle, Login: "login"}); err != nil {
		t.Fatalf("PutAccount: %v", err)
	}
	if err := st.SetSetting(fmt.Sprintf("lk_name:%d", id), fio); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
}

func envKeyLabels(chain []llm.Target) map[string]bool {
	out := map[string]bool{}
	for _, tgt := range chain {
		if tgt.KeyLabel != "" {
			out[tgt.KeyLabel] = true
		}
	}
	return out
}

// Однофамильцы: ключ «Зуев:…» не достаётся ни одному из двух.
func TestKeyOwnerTwoSameSurnamesGetNothing(t *testing.T) {
	st, v := openAccessTestEnv(t)
	cfg := &config.Config{AdminID: 1, GeminiAPIKeys: []string{"Зуев:g-ageev-env"}}
	const a, b int64 = 2001, 2002
	addOwnerAccount(t, st, a, "Зуев Иван Петрович")
	addOwnerAccount(t, st, b, "Зуев Пётр Иванович")

	for _, id := range []int64{a, b} {
		if got := KeyOwner(st, id); got.Surname != "" {
			t.Fatalf("tg=%d: фамилия однофамильцев не должна назначать владельца: %+v", id, got)
		}
		if chain := QuizChainWithVault(cfg, st, v, id); len(chain) != 0 {
			t.Fatalf("tg=%d: ключ однофамильца не должен попасть в цепочку: %+v", id, chain)
		}
	}
}

// Один носитель фамилии — ключ по фамилии достаётся ему.
func TestKeyOwnerUniqueSurnameGetsKey(t *testing.T) {
	st, v := openAccessTestEnv(t)
	cfg := &config.Config{AdminID: 1, GeminiAPIKeys: []string{"Зуев:g-ageev-env", "Пушкин:g-kostin-env"}}
	const a, other int64 = 2011, 2012
	addOwnerAccount(t, st, a, "Зуев Иван Петрович")
	addOwnerAccount(t, st, other, "Пушкин Пётр Иванович")

	if got := KeyOwner(st, a); got.Surname != "Зуев" || got.ID != a {
		t.Fatalf("владелец: %+v", got)
	}
	labels := envKeyLabels(QuizChainWithVault(cfg, st, v, a))
	if !labels["Зуев"] || labels["Пушкин"] {
		t.Fatalf("у Зуева должен быть только его ключ: %v", labels)
	}
}

// Подпись из цифр — точный telegram_id, даже когда фамилия неоднозначна.
func TestKeyOwnerNumericLabelIsExactID(t *testing.T) {
	st, v := openAccessTestEnv(t)
	cfg := &config.Config{AdminID: 1, GeminiAPIKeys: []string{"2021:g-by-id"}}
	const a, b int64 = 2021, 2022
	addOwnerAccount(t, st, a, "Зуев Иван Петрович")
	addOwnerAccount(t, st, b, "Зуев Пётр Иванович")

	if labels := envKeyLabels(QuizChainWithVault(cfg, st, v, a)); !labels["2021"] {
		t.Fatalf("ключ с подписью telegram_id своего владельца должен достаться ему: %v", labels)
	}
	if labels := envKeyLabels(QuizChainWithVault(cfg, st, v, b)); labels["2021"] {
		t.Fatalf("чужой telegram_id не должен давать ключ: %v", labels)
	}
}

// Однофамилец, виденный только по аккаунту ЛК (без Moodle), тоже считается.
func TestKeyOwnerSurnameHolderWithOnlyLKAccountIsSeen(t *testing.T) {
	st, v := openAccessTestEnv(t)
	cfg := &config.Config{AdminID: 1, GeminiAPIKeys: []string{"Зуев:g-ageev-env"}}
	const a, b int64 = 2031, 2032
	if err := st.PutAccount(model.Account{TelegramID: a, Kind: model.AccountLK, Login: "lk"}); err != nil {
		t.Fatalf("PutAccount LK: %v", err)
	}
	if err := st.SetSetting(fmt.Sprintf("lk_name:%d", a), "Зуев Андрей Анатольевич"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	addOwnerAccount(t, st, b, "Зуев Борис Борисович")

	if got := KeyOwner(st, b); got.Surname != "" {
		t.Fatalf("Зуев Б с аккаунтом Moodle видит однофамильца ЛК: владелец %+v", got)
	}
	if chain := QuizChainWithVault(cfg, st, v, b); len(chain) != 0 {
		t.Fatalf("ключ однофамильца не должен достаться Б: %+v", chain)
	}
	if got := KeyOwner(st, a); got.Surname != "" {
		t.Fatalf("Зуев А (только ЛК) тоже не должен получать фамилию: %+v", got)
	}
}

// Ошибка чтения любого списка аккаунтов — fail-closed: по фамилии никому.
type failingAccountsStore struct {
	*store.Store
	failKind string
}

func (f failingAccountsStore) ListAccounts(kind string) ([]model.Account, error) {
	if kind == f.failKind {
		return nil, fmt.Errorf("сбой чтения %s", kind)
	}
	return f.Store.ListAccounts(kind)
}

func TestKeyOwnerFailsClosedOnListError(t *testing.T) {
	st, _ := openAccessTestEnv(t)
	const b int64 = 2041
	addOwnerAccount(t, st, b, "Зуев Борис Борисович")
	for _, kind := range []string{model.AccountLK, model.AccountMoodle} {
		if got := KeyOwner(failingAccountsStore{Store: st, failKind: kind}, b); got.Surname != "" {
			t.Fatalf("сбой ListAccounts(%s): владелец должен быть пуст, получили %+v", kind, got)
		}
	}
}

// «ё» и «е» — одна фамилия: два однофамильца с разным написанием неоднозначны.
func TestKeyOwnerYoAndEAreSameSurname(t *testing.T) {
	st, v := openAccessTestEnv(t)
	cfg := &config.Config{AdminID: 1, GeminiAPIKeys: []string{"Петр:g-petr-env", "Елкин:g-elkin-env"}}
	const a, b, c int64 = 2051, 2052, 2053
	addOwnerAccount(t, st, a, "Пётр Иван Олегович")
	addOwnerAccount(t, st, b, "Петр Сергей Львович")
	addOwnerAccount(t, st, c, "Ёлкин Павел Игоревич")

	for _, id := range []int64{a, b} {
		if got := KeyOwner(st, id); got.Surname != "" {
			t.Fatalf("tg=%d: Пётр/Петр неоднозначны, владелец должен быть пуст: %+v", id, got)
		}
		if chain := QuizChainWithVault(cfg, st, v, id); len(chain) != 0 {
			t.Fatalf("tg=%d: ключ Петр не должен попасть в цепочку: %+v", id, chain)
		}
	}
	// «Ёлкин» (единственный) получает ключ «Елкин:…».
	if labels := envKeyLabels(QuizChainWithVault(cfg, st, v, c)); !labels["Елкин"] {
		t.Fatalf("Ёлкин должен получить ключ Елкин: %v", labels)
	}
}
