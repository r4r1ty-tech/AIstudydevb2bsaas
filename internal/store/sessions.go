package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

// WebSessionsPerUser — сколько живых входов в панель держим на одного человека.
// Сверх лимита самые старые удаляются: таблица не растёт от бесконечных входов.
const WebSessionsPerUser = 20

// Времена хранятся целыми секундами unix — их сравнивает сам SQLite.

// CreateWebSession сохраняет сессию (hash — sha256 токена в hex) и заодно
// чистит просроченные и лишние сессии этого же человека.
func (s *Store) CreateWebSession(hash string, telegramID int64, created, expires time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: create web session: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`INSERT INTO web_sessions (token_hash, telegram_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		hash, telegramID, created.Unix(), expires.Unix()); err != nil {
		return fmt.Errorf("store: create web session: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM web_sessions WHERE expires_at <= ?`, created.Unix()); err != nil {
		return fmt.Errorf("store: purge web sessions: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM web_sessions WHERE telegram_id = ? AND token_hash NOT IN (
			SELECT token_hash FROM web_sessions WHERE telegram_id = ? ORDER BY created_at DESC, rowid DESC LIMIT ?)`,
		telegramID, telegramID, WebSessionsPerUser); err != nil {
		return fmt.Errorf("store: trim web sessions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: create web session: %w", err)
	}
	logx.Debugf("store", "CreateWebSession: tg=%d expires=%s", telegramID, expires.UTC().Format(time.RFC3339))
	return nil
}

// GetWebSession возвращает живую сессию; нет такой или срок вышел на момент now — nil.
func (s *Store) GetWebSession(hash string, now time.Time) (*model.WebSession, error) {
	var tg, created, expires int64
	err := s.db.QueryRow(`SELECT telegram_id, created_at, expires_at FROM web_sessions WHERE token_hash = ?`, hash).
		Scan(&tg, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get web session: %w", err)
	}
	if expires <= now.Unix() {
		return nil, nil
	}
	return &model.WebSession{
		TokenHash:  hash,
		TelegramID: tg,
		CreatedAt:  time.Unix(created, 0),
		ExpiresAt:  time.Unix(expires, 0),
	}, nil
}

// ExtendWebSession продлевает срок того же токена до expires. Срок только
// растёт (MAX): параллельные запросы с разными «сейчас» не откатывают друг друга.
func (s *Store) ExtendWebSession(hash string, expires time.Time) error {
	if _, err := s.db.Exec(`UPDATE web_sessions SET expires_at = MAX(expires_at, ?) WHERE token_hash = ?`,
		expires.Unix(), hash); err != nil {
		return fmt.Errorf("store: extend web session: %w", err)
	}
	return nil
}

// DeleteWebSession — выход: сессия забывается на сервере.
func (s *Store) DeleteWebSession(hash string) error {
	if _, err := s.db.Exec(`DELETE FROM web_sessions WHERE token_hash = ?`, hash); err != nil {
		return fmt.Errorf("store: delete web session: %w", err)
	}
	return nil
}

// DeleteUserWebSessions — выйти человеку со всех устройств.
func (s *Store) DeleteUserWebSessions(telegramID int64) error {
	if _, err := s.db.Exec(`DELETE FROM web_sessions WHERE telegram_id = ?`, telegramID); err != nil {
		return fmt.Errorf("store: delete user web sessions: %w", err)
	}
	return nil
}

// PurgeWebSessions удаляет просроченные сессии и возвращает, сколько их было.
func (s *Store) PurgeWebSessions(now time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM web_sessions WHERE expires_at <= ?`, now.Unix())
	if err != nil {
		return 0, fmt.Errorf("store: purge web sessions: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
