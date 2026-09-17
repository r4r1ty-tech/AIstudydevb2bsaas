package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

const userCols = `telegram_id, username, first_name, last_name, fio, subgroup, enabled, disabled_until, onboarded, created_at, wake_words, onboard_stage`

func (s *Store) GetUser(id int64) (*model.User, error) {
	logx.Debugf("store", "GetUser: enter telegram_id=%d", id)
	u, err := scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE telegram_id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		logx.Debugf("store", "GetUser: not found telegram_id=%d", id)
		return nil, nil
	}
	if err != nil {
		logx.Errorf("store", "GetUser: scan telegram_id=%d: %v", id, err)
		return nil, fmt.Errorf("store: get user: %w", err)
	}
	logx.Debugf("store", "GetUser: out telegram_id=%d enabled=%v onboarded=%v", u.TelegramID, u.Enabled, u.Onboarded)
	return u, nil
}

func (s *Store) UpsertUser(u *model.User) error {
	if u == nil {
		logx.Errorf("store", "UpsertUser: nil user")
		return fmt.Errorf("store: upsert user: nil user")
	}
	logx.Debugf("store", "UpsertUser: enter telegram_id=%d subgroup=%d enabled=%v onboarded=%v stage=%d words=%d",
		u.TelegramID, u.Subgroup, u.Enabled, u.Onboarded, u.OnboardStage, len(u.ExtraWords))
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
		logx.Errorf("store", "UpsertUser: exec telegram_id=%d: %v", u.TelegramID, err)
		return fmt.Errorf("store: upsert user: %w", err)
	}
	logx.Debugf("store", "UpsertUser: done telegram_id=%d", u.TelegramID)
	return nil
}

func (s *Store) ListUsers() ([]model.User, error) {
	logx.Debugf("store", "ListUsers: enter")
	rows, err := s.db.Query(`SELECT ` + userCols + ` FROM users ORDER BY telegram_id`)
	if err != nil {
		logx.Errorf("store", "ListUsers: query: %v", err)
		return nil, fmt.Errorf("store: list users: %w", err)
	}
	defer rows.Close()
	out := make([]model.User, 0)
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			logx.Errorf("store", "ListUsers: scan: %v", err)
			return nil, fmt.Errorf("store: list users: %w", err)
		}
		out = append(out, *u)
	}
	if err := rows.Err(); err != nil {
		logx.Errorf("store", "ListUsers: rows: %v", err)
		return nil, fmt.Errorf("store: list users: %w", err)
	}
	logx.Debugf("store", "ListUsers: out count=%d", len(out))
	return out, nil
}

func (s *Store) SetEnabled(id int64, enabled bool) error {
	logx.Debugf("store", "SetEnabled: enter telegram_id=%d enabled=%v", id, enabled)
	err := s.updateUser(id, `UPDATE users SET enabled = ? WHERE telegram_id = ? RETURNING telegram_id`, btoi(enabled), id)
	if err != nil {
		logx.Errorf("store", "SetEnabled: telegram_id=%d: %v", id, err)
		return err
	}
	logx.Debugf("store", "SetEnabled: done telegram_id=%d", id)
	return nil
}

func (s *Store) SetDisabledUntil(id int64, until *time.Time) error {
	logx.Debugf("store", "SetDisabledUntil: enter telegram_id=%d", id)
	err := s.updateUser(id, `UPDATE users SET disabled_until = ? WHERE telegram_id = ? RETURNING telegram_id`, nullTimeArg(until), id)
	if err != nil {
		logx.Errorf("store", "SetDisabledUntil: telegram_id=%d: %v", id, err)
		return err
	}
	logx.Debugf("store", "SetDisabledUntil: done telegram_id=%d", id)
	return nil
}

func (s *Store) SetFIO(id int64, fio string) error {
	logx.Debugf("store", "SetFIO: enter telegram_id=%d", id)
	err := s.updateUser(id, `UPDATE users SET fio = ? WHERE telegram_id = ? RETURNING telegram_id`, fio, id)
	if err != nil {
		logx.Errorf("store", "SetFIO: telegram_id=%d: %v", id, err)
		return err
	}
	logx.Debugf("store", "SetFIO: done telegram_id=%d", id)
	return nil
}

func (s *Store) SetSubgroup(id int64, n int) error {
	logx.Debugf("store", "SetSubgroup: enter telegram_id=%d subgroup=%d", id, n)
	err := s.updateUser(id, `UPDATE users SET subgroup = ? WHERE telegram_id = ? RETURNING telegram_id`, n, id)
	if err != nil {
		logx.Errorf("store", "SetSubgroup: telegram_id=%d: %v", id, err)
		return err
	}
	logx.Debugf("store", "SetSubgroup: done telegram_id=%d", id)
	return nil
}

func (s *Store) SetExtraWords(id int64, words []string) error {
	logx.Debugf("store", "SetExtraWords: enter telegram_id=%d count=%d", id, len(words))
	err := s.updateUser(id, `UPDATE users SET wake_words = ? WHERE telegram_id = ? RETURNING telegram_id`, model.FormatWakeWords(words), id)
	if err != nil {
		logx.Errorf("store", "SetExtraWords: telegram_id=%d: %v", id, err)
		return err
	}
	logx.Debugf("store", "SetExtraWords: done telegram_id=%d", id)
	return nil
}

func (s *Store) SetOnboardStage(id int64, n int) error {
	logx.Debugf("store", "SetOnboardStage: enter telegram_id=%d stage=%d", id, n)
	err := s.updateUser(id, `UPDATE users SET onboard_stage = ? WHERE telegram_id = ? RETURNING telegram_id`, n, id)
	if err != nil {
		logx.Errorf("store", "SetOnboardStage: telegram_id=%d: %v", id, err)
		return err
	}
	logx.Debugf("store", "SetOnboardStage: done telegram_id=%d", id)
	return nil
}

func (s *Store) updateUser(id int64, query string, args ...any) error {
	logx.Debugf("store", "updateUser: enter telegram_id=%d", id)
	var got int64
	err := s.db.QueryRow(query, args...).Scan(&got)
	if errors.Is(err, sql.ErrNoRows) {
		logx.Debugf("store", "updateUser: no rows telegram_id=%d", id)
		return sql.ErrNoRows
	}
	if err != nil {
		logx.Errorf("store", "updateUser: telegram_id=%d: %v", id, err)
		return fmt.Errorf("store: update user %d: %w", id, err)
	}
	logx.Debugf("store", "updateUser: done telegram_id=%d", got)
	return nil
}
