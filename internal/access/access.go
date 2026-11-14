// Package access — кто админ и какую модель ИИ он выбрал для своих тестов.
// Админ — ADMIN_TELEGRAM_ID или тот, у кого аккаунт универа (ЛК или Moodle)
// с логином на ADMIN_LOGIN_SUFFIX (по умолчанию «528»).
package access

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/llm"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

// Store — то, что нужно от базы.
type Store interface {
	GetAccount(telegramID int64, kind string) (*model.Account, error)
	GetSetting(key string) (string, bool, error)
	SetSetting(key, val string) error
}

// IsAdmin — админ по Telegram id или по логину аккаунта универа.
func IsAdmin(cfg *config.Config, st Store, id int64) bool {
	if cfg == nil || id == 0 {
		return false
	}
	if cfg.IsAdmin(id) {
		return true
	}
	suffix := strings.TrimSpace(cfg.AdminLoginSuffix)
	if suffix == "" || st == nil {
		return false
	}
	for _, kind := range []string{model.AccountLK, model.AccountMoodle} {
		acc, err := st.GetAccount(id, kind)
		if err == nil && acc != nil && strings.HasSuffix(strings.TrimSpace(acc.Login), suffix) {
			return true
		}
	}
	return false
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

// QuizChain — цепочка для тестов человека id: его выбор (провайдер, модель,
// ключ) первым, дальше остальные ключи и общая цепочка. cheaper — только админу.
func QuizChain(cfg *config.Config, st Store, id int64) []llm.Target {
	return llm.ChainFor(cfg, GetChoice(st, id), IsAdmin(cfg, st, id))
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
