// Package access — кто админ и какую модель ИИ он выбрал для своих тестов.
// Админ определяется строго по ADMIN_TELEGRAM_ID (cfg.IsAdmin).
package access

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/llm"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/vault"
)

// Store — то, что нужно от базы.
type Store interface {
	GetAccount(telegramID int64, kind string) (*model.Account, error)
	GetSetting(key string) (string, bool, error)
	SetSetting(key, val string) error
}

// IsAdmin проверяет права администратора строго по cfg.IsAdmin(id).
func IsAdmin(cfg *config.Config, _ Store, id int64) bool {
	if cfg == nil || id == 0 {
		return false
	}
	return cfg.IsAdmin(id)
}

func choiceKey(id int64) string { return fmt.Sprintf("llm_choice:%d", id) }

// GetChoice — выбранные провайдер, модель и ключ для тестов; пусто — общая цепочка.
func GetChoice(st Store, id int64) llm.Choice {
	var ch llm.Choice
	if st == nil {
		return ch
	}
	if v, ok, err := st.GetSetting(choiceKey(id)); err == nil && ok && v != "" {
		_ = json.Unmarshal([]byte(v), &ch)
	}
	return ch
}

// SetChoice сохраняет выбор; пустой провайдер — вернуться к общей цепочке.
func SetChoice(st Store, id int64, ch llm.Choice) error {
	ch.Provider = strings.TrimSpace(ch.Provider)
	ch.Model = strings.TrimSpace(ch.Model)
	if ch.Provider == "" {
		return st.SetSetting(choiceKey(id), "")
	}
	b, _ := json.Marshal(ch)
	return st.SetSetting(choiceKey(id), string(b))
}

// QuizChain — цепочка для тестов человека id (см. llm.ChainFor): у
// обычного — только его ключи, у админа — выбор, потом cheaper и все ключи.
func QuizChain(cfg *config.Config, st Store, id int64) []llm.Target {
	return llm.ChainForWithUserKeys(cfg, GetChoice(st, id), IsAdmin(cfg, st, id), KeyOwner(st, id), nil)
}

type llmKeyStore interface {
	ListLLMKeysByUser(telegramID int64) ([]store.LLMKeyRecord, error)
}

// UserKeysFromVault читает и расшифровывает личные ключи пользователя из БД.
// Если хранилище не поддерживает llm_keys или v == nil, возвращает nil.
func UserKeysFromVault(st Store, v *vault.Vault, id int64) map[string]llm.Key {
	if st == nil || v == nil || id == 0 {
		return nil
	}
	ks, ok := st.(llmKeyStore)
	if !ok {
		return nil
	}
	recs, err := ks.ListLLMKeysByUser(id)
	if err != nil || len(recs) == 0 {
		return nil
	}
	out := make(map[string]llm.Key, len(recs))
	for _, rec := range recs {
		if rec.Provider != llm.Gemini && rec.Provider != llm.OpenCode {
			continue
		}
		plain, err := v.Open(rec.SecretEnc)
		if err != nil {
			logx.Warnf("access", "UserKeysFromVault: ошибка расшифровки ключа tg=%d provider=%s: %v", id, rec.Provider, err)
			continue
		}
		plain = strings.TrimSpace(plain)
		if plain == "" {
			continue
		}
		out[rec.Provider] = llm.Key{
			Label:   "личный",
			Secret:  plain,
			OwnerID: id,
		}
	}
	return out
}

// QuizChainWithVault строит цепочку для тестов пользователя id с учётом его личных
// ключей из таблицы llm_keys. При v == nil работает как QuizChain.
func QuizChainWithVault(cfg *config.Config, st Store, v *vault.Vault, id int64) []llm.Target {
	if v == nil {
		return QuizChain(cfg, st, id)
	}
	userKeys := UserKeysFromVault(st, v, id)
	return llm.ChainForWithUserKeys(cfg, GetChoice(st, id), IsAdmin(cfg, st, id), KeyOwner(st, id), userKeys)
}

const ownerCacheTTL = 5 * time.Minute

type providerOwners struct {
	sig     [32]byte
	builtAt time.Time
	owners  map[[32]byte]int64
}

type ownerLookupCache struct {
	mu         sync.Mutex
	ttl        time.Duration
	nowFn      func() time.Time
	listKeys   func(provider string) ([]store.LLMKeyRecord, error)
	openSecret func(sealed string) (string, error)
	byProvider map[string]providerOwners
}

func newOwnerLookupCache(st *store.Store, v *vault.Vault) *ownerLookupCache {
	return &ownerLookupCache{
		ttl:        ownerCacheTTL,
		nowFn:      time.Now,
		listKeys:   st.ListLLMKeysByProvider,
		openSecret: v.Open,
		byProvider: make(map[string]providerOwners),
	}
}

func llmKeysSignature(recs []store.LLMKeyRecord) [32]byte {
	h := sha256.New()
	var buf [8]byte
	for _, rec := range recs {
		binary.LittleEndian.PutUint64(buf[:], uint64(rec.TelegramID))
		h.Write(buf[:])
		h.Write([]byte(rec.SecretEnc))
		h.Write([]byte{0})
	}
	var sum [32]byte
	h.Sum(sum[:0])
	return sum
}

func (c *ownerLookupCache) lookup(provider string, secretHash [32]byte) int64 {
	if c == nil || c.listKeys == nil || c.openSecret == nil {
		return 0
	}
	recs, err := c.listKeys(provider)
	if err != nil {
		return 0
	}
	sig := llmKeysSignature(recs)
	now := c.nowFn()

	c.mu.Lock()
	if entry, ok := c.byProvider[provider]; ok && entry.sig == sig && now.Sub(entry.builtAt) < c.ttl {
		ownerID := entry.owners[secretHash]
		c.mu.Unlock()
		return ownerID
	}
	c.mu.Unlock()

	owners := make(map[[32]byte]int64, len(recs))
	for _, rec := range recs {
		plain, oerr := c.openSecret(rec.SecretEnc)
		if oerr != nil {
			continue
		}
		plain = strings.TrimSpace(plain)
		if plain == "" {
			continue
		}
		h := sha256.Sum256([]byte(plain))
		if _, exists := owners[h]; !exists {
			owners[h] = rec.TelegramID
		}
	}

	c.mu.Lock()
	if c.byProvider == nil {
		c.byProvider = make(map[string]providerOwners)
	}
	cur, ok := c.byProvider[provider]
	if !ok || cur.sig != sig || now.Sub(cur.builtAt) >= c.ttl {
		c.byProvider[provider] = providerOwners{
			sig:     sig,
			builtAt: now,
			owners:  owners,
		}
	} else {
		owners = cur.owners
	}
	c.mu.Unlock()
	return owners[secretHash]
}

// InitLLMBridge связывает пакет llm с таблицами llm_keys и llm_usage:
// подмешивает расшаренные личные ключи в PoolChain и сохраняет расход/cooldown после запросов.
func InitLLMBridge(st *store.Store, v *vault.Vault) {
	if st == nil || v == nil {
		llm.RegisterSharedPoolKeys(nil)
		llm.RegisterUsageHook(nil)
		return
	}

	ownerCache := newOwnerLookupCache(st, v)

	llm.RegisterSharedPoolKeys(func(provider string) []llm.Key {
		if provider != llm.Gemini && provider != llm.OpenCode {
			return nil
		}
		recs, err := st.ListSharedLLMKeys(provider)
		if err != nil {
			logx.Warnf("access", "InitLLMBridge: чтение расшаренных ключей provider=%s: %v", provider, err)
			return nil
		}
		out := make([]llm.Key, 0, len(recs))
		for _, rec := range recs {
			plain, err := v.Open(rec.SecretEnc)
			if err != nil {
				logx.Warnf("access", "InitLLMBridge: расшифровка расшаренного ключа tg=%d provider=%s: %v", rec.TelegramID, rec.Provider, err)
				continue
			}
			plain = strings.TrimSpace(plain)
			if plain == "" {
				continue
			}
			out = append(out, llm.Key{
				Label:   "личный",
				Secret:  plain,
				OwnerID: rec.TelegramID,
			})
		}
		return out
	})

	llm.RegisterUsageHook(func(t llm.Target, status int, inTok, outTok int, cooldownUntil time.Time) {
		ownerID := t.OwnerID
		if ownerID == 0 {
			if secretHash, ok := t.SecretHash(); ok {
				ownerID = ownerCache.lookup(t.Provider, secretHash)
			}
		}
		if ownerID == 0 {
			return
		}
		day := time.Now().UTC().Format("2006-01-02")
		if status >= http.StatusOK && status < http.StatusMultipleChoices {
			if err := st.AddLLMUsage(ownerID, t.Provider, day, 1, inTok, outTok); err != nil {
				logx.Warnf("access", "InitLLMBridge: AddLLMUsage tg=%d provider=%s: %v", ownerID, t.Provider, err)
			}
		}
		if !cooldownUntil.IsZero() {
			if err := st.SetLLMCooldown(ownerID, t.Provider, day, cooldownUntil); err != nil {
				logx.Warnf("access", "InitLLMBridge: SetLLMCooldown tg=%d provider=%s: %v", ownerID, t.Provider, err)
			}
		}
	})
}

// accountLister — список аккаунтов из базы (нужен, чтобы проверить однозначность фамилии).
type accountLister interface {
	ListAccounts(kind string) ([]model.Account, error)
}

// warnedSurnames — фамилии, по которым уже писали предупреждение (один раз на процесс).
var warnedSurnames sync.Map

// KeyOwner — чьи ключи из .env у человека id. Подпись из одних цифр — его
// telegram_id (такой ключ точно его). Иная подпись — фамилия из ФИО ЛК
// (ключи в .env подписаны фамилией: «Агеев:ключ»), и она засчитывается, только
// если среди аккаунтов ровно один человек с этой фамилией. Однофамильцы не
// делят ключи: при неоднозначности владельца по фамилии нет.
func KeyOwner(st Store, id int64) llm.Owner {
	o := llm.Owner{ID: id}
	f := strings.Fields(LKName(st, id))
	if len(f) == 0 {
		return o
	}
	surname := f[0]
	n, known := surnameHolders(st, id, surname)
	if !known {
		return o
	}
	if n == 1 {
		o.Surname = surname
		return o
	}
	warnAmbiguousSurname(surname, n)
	return o
}

// surnameHolders — сколько людей с фамилией surname среди аккаунтов ЛК и Moodle;
// сам id учитывается всегда. Однофамилец, у которого есть только аккаунт ЛК,
// тоже виден. known=false — любой список аккаунтов не получен: однозначность
// не проверить, тогда по фамилии никому не назначаем (fail-closed).
func surnameHolders(st Store, id int64, surname string) (n int, known bool) {
	ls, ok := st.(accountLister)
	if !ok {
		return 0, false
	}
	ids := map[int64]struct{}{id: {}}
	for _, kind := range []string{model.AccountLK, model.AccountMoodle} {
		accs, err := ls.ListAccounts(kind)
		if err != nil {
			logx.Warnf("access", "KeyOwner: список аккаунтов kind=%s: %v", kind, err)
			return 0, false
		}
		for _, a := range accs {
			ids[a.TelegramID] = struct{}{}
		}
	}
	want := llm.NormSurname(surname)
	for tg := range ids {
		if tg == id {
			n++
			continue
		}
		f := strings.Fields(LKName(st, tg))
		if len(f) > 0 && llm.NormSurname(f[0]) == want {
			n++
		}
	}
	return n, true
}

// warnAmbiguousSurname пишет в лог, что ключи с подписью-фамилией никому не
// назначены. Секретов в записи нет; один раз на фамилию за процесс.
func warnAmbiguousSurname(surname string, n int) {
	key := llm.NormSurname(surname)
	if _, dup := warnedSurnames.LoadOrStore(key, true); dup {
		return
	}
	logx.Warnf("access", "ключи с подписью «%s» никому не назначены: фамилию носят %d аккаунта(ов), подпишите ключ telegram_id", surname, n)
}

// LKName — ФИО из ЛК (uni.LKNameKey, без импорта uni), пусто — не знаем.
func LKName(st Store, id int64) string {
	if st == nil {
		return ""
	}
	v, ok, err := st.GetSetting(fmt.Sprintf("lk_name:%d", id))
	if err != nil || !ok {
		return ""
	}
	return strings.TrimSpace(v)
}

type userStore interface {
	GetUser(id int64) (*model.User, error)
}

// Name — как подписывать человека в панели, боте и журналах: ФИО из ЛК,
// иначе имя для BBB, иначе @ник, иначе id.
func Name(st Store, id int64) string {
	if n := LKName(st, id); n != "" {
		return n
	}
	if us, ok := st.(userStore); ok {
		if u, err := us.GetUser(id); err == nil && u != nil {
			switch {
			case strings.TrimSpace(u.FIO) != "":
				return strings.TrimSpace(u.FIO)
			case u.Username != "":
				return "@" + u.Username
			case u.FirstName != "":
				return strings.TrimSpace(u.FirstName + " " + u.LastName)
			}
		}
	}
	return fmt.Sprint(id)
}
