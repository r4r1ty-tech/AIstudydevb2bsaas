package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

// LLMKeyRecord описывает зашифрованный личный API-ключ пользователя для провайдера ИИ.
type LLMKeyRecord struct {
	TelegramID int64
	Provider   string
	SecretEnc  string
	Mask       string
	SharePool  bool
	UpdatedAt  time.Time
}

// LLMUsageRecord хранит суточный расход запросов/токенов и активный cooldown пользователя.
type LLMUsageRecord struct {
	TelegramID    int64
	Provider      string
	Day           string
	Requests      int
	InputTokens   int
	OutputTokens  int
	CooldownUntil *time.Time
	UpdatedAt     time.Time
}

const (
	llmKeyCols   = `telegram_id, provider, secret_enc, mask, share_pool, updated_at`
	llmUsageCols = `telegram_id, provider, day, requests, input_tokens, output_tokens, cooldown_until, updated_at`
)

// MaskKey возвращает безопасную маску ключа (первые 4 и последние 4 символа).
// Для коротких строк длиной <= 8 рун всегда возвращает "****", чтобы не раскрыть ключ целиком.
func MaskKey(raw string) string {
	runes := []rune(strings.TrimSpace(raw))
	if len(runes) <= 8 {
		return "****"
	}
	return string(runes[:4]) + "..." + string(runes[len(runes)-4:])
}

func scanLLMKey(sc scanner) (*LLMKeyRecord, error) {
	var rec LLMKeyRecord
	var provider, secretEnc, mask, updated string
	var sharePool int64
	if err := sc.Scan(&rec.TelegramID, &provider, &secretEnc, &mask, &sharePool, &updated); err != nil {
		logScanErr("scanLLMKey", err)
		return nil, err
	}
	rec.Provider = provider
	rec.SecretEnc = secretEnc
	rec.Mask = mask
	rec.SharePool = sharePool != 0
	if updated != "" {
		t, err := parseTime(updated)
		if err != nil {
			return nil, fmt.Errorf("store: parse llm_keys.updated_at: %w", err)
		}
		rec.UpdatedAt = t
	}
	return &rec, nil
}

func scanLLMUsage(sc scanner) (*LLMUsageRecord, error) {
	var rec LLMUsageRecord
	var provider, day, updated string
	var cooldown sql.NullString
	if err := sc.Scan(
		&rec.TelegramID,
		&provider,
		&day,
		&rec.Requests,
		&rec.InputTokens,
		&rec.OutputTokens,
		&cooldown,
		&updated,
	); err != nil {
		logScanErr("scanLLMUsage", err)
		return nil, err
	}
	rec.Provider = provider
	rec.Day = day
	until, err := parseNullTime(cooldown)
	if err != nil {
		return nil, fmt.Errorf("store: parse llm_usage.cooldown_until: %w", err)
	}
	rec.CooldownUntil = until
	if updated != "" {
		t, err := parseTime(updated)
		if err != nil {
			return nil, fmt.Errorf("store: parse llm_usage.updated_at: %w", err)
		}
		rec.UpdatedAt = t
	}
	return &rec, nil
}

// PutLLMKey сохраняет или обновляет зашифрованный личный ключ пользователя по (telegram_id, provider).
func (s *Store) PutLLMKey(rec LLMKeyRecord) error {
	provider := strings.TrimSpace(rec.Provider)
	if provider == "" {
		return errors.New("store: put llm key: empty provider")
	}
	updatedAt := rec.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now()
	}
	_, err := s.db.Exec(`INSERT INTO llm_keys (`+llmKeyCols+`) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(telegram_id, provider) DO UPDATE SET
			secret_enc = excluded.secret_enc,
			mask = excluded.mask,
			share_pool = excluded.share_pool,
			updated_at = excluded.updated_at`,
		rec.TelegramID, provider, rec.SecretEnc, rec.Mask, btoi(rec.SharePool), formatTime(updatedAt))
	if err != nil {
		logx.Errorf("store", "PutLLMKey tg=%d provider=%s: %v", rec.TelegramID, provider, err)
		return fmt.Errorf("store: put llm key: %w", err)
	}
	return nil
}

// GetLLMKey возвращает личный ключ пользователя для провайдера или nil, если ключ не задан.
func (s *Store) GetLLMKey(telegramID int64, provider string) (*LLMKeyRecord, error) {
	rec, err := scanLLMKey(s.db.QueryRow(
		`SELECT `+llmKeyCols+` FROM llm_keys WHERE telegram_id = ? AND provider = ?`,
		telegramID, strings.TrimSpace(provider),
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get llm key: %w", err)
	}
	return rec, nil
}

// ListLLMKeysByUser возвращает все личные ключи пользователя, отсортированные по провайдеру.
func (s *Store) ListLLMKeysByUser(telegramID int64) ([]LLMKeyRecord, error) {
	return s.queryLLMKeys(`SELECT `+llmKeyCols+` FROM llm_keys WHERE telegram_id = ? ORDER BY provider`, telegramID)
}

// ListSharedLLMKeys возвращает расшаренные ключи (share_pool = 1), с необязательным фильтром по провайдеру.
func (s *Store) ListSharedLLMKeys(provider string) ([]LLMKeyRecord, error) {
	p := strings.TrimSpace(provider)
	if p != "" {
		return s.queryLLMKeys(
			`SELECT `+llmKeyCols+` FROM llm_keys WHERE share_pool = 1 AND provider = ? ORDER BY telegram_id, provider`,
			p,
		)
	}
	return s.queryLLMKeys(`SELECT ` + llmKeyCols + ` FROM llm_keys WHERE share_pool = 1 ORDER BY telegram_id, provider`)
}

// ListLLMKeysByProvider возвращает все личные ключи указанного провайдера (для сопоставления владельца по секрету).
func (s *Store) ListLLMKeysByProvider(provider string) ([]LLMKeyRecord, error) {
	p := strings.TrimSpace(provider)
	if p != "" {
		return s.queryLLMKeys(
			`SELECT `+llmKeyCols+` FROM llm_keys WHERE provider = ? ORDER BY telegram_id, provider`,
			p,
		)
	}
	return s.queryLLMKeys(`SELECT ` + llmKeyCols + ` FROM llm_keys ORDER BY telegram_id, provider`)
}

func (s *Store) queryLLMKeys(query string, args ...any) ([]LLMKeyRecord, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list llm keys: %w", err)
	}
	defer rows.Close()
	var out []LLMKeyRecord
	for rows.Next() {
		rec, err := scanLLMKey(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan llm key: %w", err)
		}
		out = append(out, *rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: rows llm keys: %w", err)
	}
	return out, nil
}

// DeleteLLMKey удаляет личный ключ пользователя для указанного провайдера.
func (s *Store) DeleteLLMKey(telegramID int64, provider string) error {
	if _, err := s.db.Exec(
		`DELETE FROM llm_keys WHERE telegram_id = ? AND provider = ?`,
		telegramID, strings.TrimSpace(provider),
	); err != nil {
		return fmt.Errorf("store: delete llm key: %w", err)
	}
	return nil
}

// AddLLMUsage атомарно увеличивает счётчики запросов и токенов за день для (telegram_id, provider, day).
func (s *Store) AddLLMUsage(telegramID int64, provider, day string, reqs, inTok, outTok int) error {
	p := strings.TrimSpace(provider)
	d := strings.TrimSpace(day)
	if p == "" || d == "" {
		return errors.New("store: add llm usage: empty provider or day")
	}
	now := formatTime(time.Now())
	_, err := s.db.Exec(`INSERT INTO llm_usage (`+llmUsageCols+`) VALUES (?, ?, ?, ?, ?, ?, '', ?)
		ON CONFLICT(telegram_id, provider, day) DO UPDATE SET
			requests = llm_usage.requests + excluded.requests,
			input_tokens = llm_usage.input_tokens + excluded.input_tokens,
			output_tokens = llm_usage.output_tokens + excluded.output_tokens,
			updated_at = excluded.updated_at`,
		telegramID, p, d, reqs, inTok, outTok, now)
	if err != nil {
		logx.Errorf("store", "AddLLMUsage tg=%d provider=%s day=%s: %v", telegramID, p, d, err)
		return fmt.Errorf("store: add llm usage: %w", err)
	}
	return nil
}

// SetLLMCooldown сохраняет или обновляет время окончания cooldown в llm_usage.
func (s *Store) SetLLMCooldown(telegramID int64, provider, day string, until time.Time) error {
	p := strings.TrimSpace(provider)
	d := strings.TrimSpace(day)
	if p == "" || d == "" {
		return errors.New("store: set llm cooldown: empty provider or day")
	}
	untilStr := ""
	if !until.IsZero() {
		untilStr = formatTime(until)
	}
	now := formatTime(time.Now())
	_, err := s.db.Exec(`INSERT INTO llm_usage (`+llmUsageCols+`) VALUES (?, ?, ?, 0, 0, 0, ?, ?)
		ON CONFLICT(telegram_id, provider, day) DO UPDATE SET
			cooldown_until = excluded.cooldown_until,
			updated_at = excluded.updated_at`,
		telegramID, p, d, untilStr, now)
	if err != nil {
		logx.Errorf("store", "SetLLMCooldown tg=%d provider=%s day=%s: %v", telegramID, p, d, err)
		return fmt.Errorf("store: set llm cooldown: %w", err)
	}
	return nil
}

// ListLLMUsageByUser возвращает статистику расхода пользователя за указанный день (или за все дни, если day == "").
func (s *Store) ListLLMUsageByUser(telegramID int64, day string) ([]LLMUsageRecord, error) {
	d := strings.TrimSpace(day)
	var (
		rows *sql.Rows
		err  error
	)
	if d != "" {
		rows, err = s.db.Query(
			`SELECT `+llmUsageCols+` FROM llm_usage WHERE telegram_id = ? AND day = ? ORDER BY provider`,
			telegramID, d,
		)
	} else {
		rows, err = s.db.Query(
			`SELECT `+llmUsageCols+` FROM llm_usage WHERE telegram_id = ? ORDER BY day DESC, provider`,
			telegramID,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("store: list llm usage: %w", err)
	}
	defer rows.Close()
	var out []LLMUsageRecord
	for rows.Next() {
		rec, err := scanLLMUsage(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan llm usage: %w", err)
		}
		out = append(out, *rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: rows llm usage: %w", err)
	}
	return out, nil
}

// GetLLMUsage возвращает запись расхода и cooldown по (telegram_id, provider, day) или nil, если записи нет.
func (s *Store) GetLLMUsage(telegramID int64, provider, day string) (*LLMUsageRecord, error) {
	rec, err := scanLLMUsage(s.db.QueryRow(
		`SELECT `+llmUsageCols+` FROM llm_usage WHERE telegram_id = ? AND provider = ? AND day = ?`,
		telegramID, strings.TrimSpace(provider), strings.TrimSpace(day),
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get llm usage: %w", err)
	}
	return rec, nil
}
