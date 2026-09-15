package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

const userCols = `telegram_id, username, first_name, last_name, fio, subgroup, enabled, disabled_until, onboarded, created_at, wake_words, onboard_stage`

func (s *Store) GetUser(id int64) (*model.User, error) {
	u, err := scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE telegram_id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get user: %w", err)
	}
	return u, nil
}

func (s *Store) UpsertUser(u *model.User) error {
	if u == nil {
		return fmt.Errorf("store: upsert user: nil user")
	}
	created := u.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	_, err := s.db.Exec(
		`INSERT INTO users (`+userCols+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(telegram_id) DO UPDATE SET
			username = excluded.username,
			first_name = excluded.first_name,
			last_name = excluded.last_name,
			fio = excluded.fio,
			subgroup = excluded.subgroup,
			enabled = excluded.enabled,
			disabled_until = excluded.disabled_until,
			onboarded = excluded.onboarded,
			wake_words = excluded.wake_words,
			onboard_stage = excluded.onboard_stage`,
		u.TelegramID,
		u.Username,
		u.FirstName,
		u.LastName,
		u.FIO,
		u.Subgroup,
		btoi(u.Enabled),
		nullTimeArg(u.DisabledUntil),
		btoi(u.Onboarded),
		timeArg(created),
		model.FormatWakeWords(u.ExtraWords),
		u.OnboardStage,
	)
	if err != nil {
		return fmt.Errorf("store: upsert user: %w", err)
	}
	return nil
}

func (s *Store) ListUsers() ([]model.User, error) {
	rows, err := s.db.Query(`SELECT ` + userCols + ` FROM users ORDER BY telegram_id`)
	if err != nil {
		return nil, fmt.Errorf("store: list users: %w", err)
	}
	defer rows.Close()
	out := make([]model.User, 0)
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list users: %w", err)
		}
		out = append(out, *u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list users: %w", err)
	}
	return out, nil
}

func (s *Store) SetEnabled(id int64, enabled bool) error {
	return s.updateUser(id, `UPDATE users SET enabled = ? WHERE telegram_id = ? RETURNING telegram_id`, btoi(enabled), id)
}

func (s *Store) SetDisabledUntil(id int64, until *time.Time) error {
	return s.updateUser(id, `UPDATE users SET disabled_until = ? WHERE telegram_id = ? RETURNING telegram_id`, nullTimeArg(until), id)
}

func (s *Store) SetFIO(id int64, fio string) error {
	return s.updateUser(id, `UPDATE users SET fio = ? WHERE telegram_id = ? RETURNING telegram_id`, fio, id)
}

func (s *Store) SetSubgroup(id int64, n int) error {
	return s.updateUser(id, `UPDATE users SET subgroup = ? WHERE telegram_id = ? RETURNING telegram_id`, n, id)
}

func (s *Store) SetExtraWords(id int64, words []string) error {
	return s.updateUser(id, `UPDATE users SET wake_words = ? WHERE telegram_id = ? RETURNING telegram_id`, model.FormatWakeWords(words), id)
}

func (s *Store) SetOnboardStage(id int64, n int) error {
	return s.updateUser(id, `UPDATE users SET onboard_stage = ? WHERE telegram_id = ? RETURNING telegram_id`, n, id)
}

func (s *Store) updateUser(id int64, query string, args ...any) error {
	var got int64
	err := s.db.QueryRow(query, args...).Scan(&got)
	if errors.Is(err, sql.ErrNoRows) {
		return sql.ErrNoRows
	}
	if err != nil {
		return fmt.Errorf("store: update user %d: %w", id, err)
	}
	return nil
}
