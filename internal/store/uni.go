package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

// ---------- аккаунты универа ----------

const accountCols = `telegram_id, kind, login, secret, session, status, message, synced_at, updated_at`

func scanAccount(sc scanner) (*model.Account, error) {
	var a model.Account
	var login, secret, session, status, message, synced, updated sql.NullString
	if err := sc.Scan(&a.TelegramID, &a.Kind, &login, &secret, &session, &status, &message, &synced, &updated); err != nil {
		return nil, err
	}
	a.Login, a.Secret, a.Session = nullStr(login), nullStr(secret), nullStr(session)
	a.Status, a.Message = nullStr(status), nullStr(message)
	if synced.Valid && synced.String != "" {
		if t, err := parseTime(synced.String); err == nil {
			a.SyncedAt = &t
		}
	}
	if updated.Valid && updated.String != "" {
		if t, err := parseTime(updated.String); err == nil {
			a.UpdatedAt = t
		}
	}
	return &a, nil
}

// PutAccount сохраняет логин и зашифрованный пароль; сессия и статус сбрасываются.
func (s *Store) PutAccount(a model.Account) error {
	if a.Status == "" {
		a.Status = model.AccountPending
	}
	_, err := s.db.Exec(`INSERT INTO accounts (`+accountCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, ?)
		ON CONFLICT(telegram_id, kind) DO UPDATE SET login=excluded.login, secret=excluded.secret,
		session=excluded.session, status=excluded.status, message=excluded.message, synced_at=NULL, updated_at=excluded.updated_at`,
		a.TelegramID, a.Kind, a.Login, a.Secret, a.Session, a.Status, a.Message, formatTime(time.Now()))
	if err != nil {
		logx.Errorf("store", "PutAccount tg=%d kind=%s: %v", a.TelegramID, a.Kind, err)
		return fmt.Errorf("store: put account: %w", err)
	}
	return nil
}

func (s *Store) GetAccount(telegramID int64, kind string) (*model.Account, error) {
	a, err := scanAccount(s.db.QueryRow(`SELECT `+accountCols+` FROM accounts WHERE telegram_id = ? AND kind = ?`, telegramID, kind))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get account: %w", err)
	}
	return a, nil
}

func (s *Store) ListAccounts(kind string) ([]model.Account, error) {
	rows, err := s.db.Query(`SELECT `+accountCols+` FROM accounts WHERE kind = ? ORDER BY telegram_id`, kind)
	if err != nil {
		return nil, fmt.Errorf("store: list accounts: %w", err)
	}
	defer rows.Close()
	var out []model.Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan account: %w", err)
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// SetAccountSession — куки после входа (зашифрованные).
func (s *Store) SetAccountSession(telegramID int64, kind, session string) error {
	_, err := s.db.Exec(`UPDATE accounts SET session = ?, updated_at = ? WHERE telegram_id = ? AND kind = ?`,
		session, formatTime(time.Now()), telegramID, kind)
	if err != nil {
		return fmt.Errorf("store: set account session: %w", err)
	}
	return nil
}

// SetAccountStatus — итог синка; ok двигает synced_at.
func (s *Store) SetAccountStatus(telegramID int64, kind, status, message string) error {
	now := formatTime(time.Now())
	q := `UPDATE accounts SET status = ?, message = ?, updated_at = ? WHERE telegram_id = ? AND kind = ?`
	args := []any{status, message, now, telegramID, kind}
	if status == model.AccountOK {
		q = `UPDATE accounts SET status = ?, message = ?, updated_at = ?, synced_at = ? WHERE telegram_id = ? AND kind = ?`
		args = []any{status, message, now, now, telegramID, kind}
	}
	if _, err := s.db.Exec(q, args...); err != nil {
		return fmt.Errorf("store: set account status: %w", err)
	}
	return nil
}

// DeleteAccount забывает аккаунт; для Moodle заодно его задания.
func (s *Store) DeleteAccount(telegramID int64, kind string) error {
	if _, err := s.db.Exec(`DELETE FROM accounts WHERE telegram_id = ? AND kind = ?`, telegramID, kind); err != nil {
		return fmt.Errorf("store: delete account: %w", err)
	}
	if kind == model.AccountMoodle {
		if _, err := s.db.Exec(`DELETE FROM moodle_items WHERE telegram_id = ?`, telegramID); err != nil {
			return fmt.Errorf("store: delete moodle items: %w", err)
		}
	}
	return nil
}

// ---------- задания и тесты Moodle ----------

const moodleCols = `telegram_id, cmid, course_id, course, section, kind, title, url, opens, due, status, todo, updated_at`

func optTime(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return formatTime(*t)
}

func scanMoodle(sc scanner) (*model.MoodleItem, error) {
	var m model.MoodleItem
	var course, section, kind, title, url, opens, due, status, todo, updated sql.NullString
	var courseID sql.NullInt64
	if err := sc.Scan(&m.TelegramID, &m.CMID, &courseID, &course, &section, &kind, &title, &url, &opens, &due, &status, &todo, &updated); err != nil {
		return nil, err
	}
	m.CourseID = courseID.Int64
	m.Course, m.Section, m.Kind, m.Title, m.URL = nullStr(course), nullStr(section), nullStr(kind), nullStr(title), nullStr(url)
	m.Status, m.Todo = nullStr(status), nullStr(todo)
	for _, p := range []struct {
		raw sql.NullString
		dst **time.Time
	}{{opens, &m.Opens}, {due, &m.Due}} {
		if p.raw.Valid && p.raw.String != "" {
			if t, err := parseTime(p.raw.String); err == nil {
				*p.dst = &t
			}
		}
	}
	if updated.Valid && updated.String != "" {
		if t, err := parseTime(updated.String); err == nil {
			m.UpdatedAt = t
		}
	}
	return &m, nil
}

// ReplaceMoodleItems — полный снимок заданий пользователя (то, чего нет в items, удаляется).
func (s *Store) ReplaceMoodleItems(telegramID int64, items []model.MoodleItem) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: moodle begin: %w", err)
	}
	defer func() {
		if rerr := tx.Rollback(); rerr != nil && rerr != sql.ErrTxDone {
			logx.Warnf("store", "ReplaceMoodleItems: rollback: %v", rerr)
		}
	}()
	if _, err := tx.Exec(`DELETE FROM moodle_items WHERE telegram_id = ?`, telegramID); err != nil {
		return fmt.Errorf("store: moodle clear: %w", err)
	}
	ins, err := tx.Prepare(`INSERT OR REPLACE INTO moodle_items (` + moodleCols + `) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("store: moodle prepare: %w", err)
	}
	defer ins.Close()
	now := formatTime(time.Now())
	for _, m := range items {
		if _, err := ins.Exec(telegramID, m.CMID, m.CourseID, m.Course, m.Section, m.Kind, m.Title, m.URL,
			optTime(m.Opens), optTime(m.Due), m.Status, m.Todo, now); err != nil {
			return fmt.Errorf("store: moodle insert cmid=%d: %w", m.CMID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: moodle commit: %w", err)
	}
	return nil
}

func (s *Store) ListMoodleItems(telegramID int64) ([]model.MoodleItem, error) {
	rows, err := s.db.Query(`SELECT `+moodleCols+` FROM moodle_items WHERE telegram_id = ?
		ORDER BY CASE WHEN due IS NULL THEN 1 ELSE 0 END, due, course, title`, telegramID)
	if err != nil {
		return nil, fmt.Errorf("store: list moodle: %w", err)
	}
	defer rows.Close()
	var out []model.MoodleItem
	for rows.Next() {
		m, err := scanMoodle(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan moodle: %w", err)
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}
