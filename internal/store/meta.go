package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

func (s *Store) GetLessonBBB(lessonID int64) string {
	if s == nil || lessonID <= 0 {
		return ""
	}
	b, err := s.GetBBB(model.BBBLessonKey(lessonID))
	if err != nil || b == nil {
		return ""
	}
	return strings.TrimSpace(b.URL)
}

func (s *Store) SetLessonBBB(lessonID int64, url string) error {
	if lessonID <= 0 {
		return fmt.Errorf("store: lesson bbb: empty lesson id")
	}
	return s.SetBBB(model.BBBLessonKey(lessonID), strings.TrimSpace(url))
}

func (s *Store) GetBBB(key string) (*model.BBBLink, error) {
	b, err := scanBBB(s.db.QueryRow(`SELECT key, url, updated_at FROM bbb_links WHERE key = ?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get bbb: %w", err)
	}
	return b, nil
}

func (s *Store) SetBBB(key, url string) error {
	_, err := s.db.Exec(
		`INSERT INTO bbb_links (key, url, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET url = excluded.url, updated_at = excluded.updated_at`,
		key, url, timeArg(time.Now()),
	)
	if err != nil {
		return fmt.Errorf("store: set bbb: %w", err)
	}
	return nil
}

func (s *Store) ListBBB() ([]model.BBBLink, error) {
	rows, err := s.db.Query(`SELECT key, url, updated_at FROM bbb_links ORDER BY key`)
	if err != nil {
		return nil, fmt.Errorf("store: list bbb: %w", err)
	}
	defer rows.Close()
	out := make([]model.BBBLink, 0)
	for rows.Next() {
		b, err := scanBBB(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list bbb: %w", err)
		}
		out = append(out, *b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list bbb: %w", err)
	}
	return out, nil
}

func (s *Store) AddEvent(e model.Event) error {
	at := e.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	_, err := s.db.Exec(
		`INSERT INTO events (at, type, telegram_id, lesson_id, message) VALUES (?, ?, ?, ?, ?)`,
		timeArg(at), e.Type, e.TelegramID, e.LessonID, e.Message,
	)
	if err != nil {
		return fmt.Errorf("store: add event: %w", err)
	}
	return nil
}

func (s *Store) ListEvents(limit int) ([]model.Event, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.Query(
		`SELECT id, at, type, telegram_id, lesson_id, message FROM events
		 ORDER BY at DESC, id DESC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list events: %w", err)
	}
	defer rows.Close()
	out := make([]model.Event, 0)
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list events: %w", err)
		}
		out = append(out, *e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list events: %w", err)
	}
	return out, nil
}

func (s *Store) GetIntent(telegramID, lessonID int64) (*model.JoinIntent, error) {
	i, err := scanIntent(s.db.QueryRow(
		`SELECT telegram_id, lesson_id, decision, asked_at, decided_at
		 FROM join_intents WHERE telegram_id = ? AND lesson_id = ?`,
		telegramID, lessonID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get intent: %w", err)
	}
	return i, nil
}

func (s *Store) PutIntent(i model.JoinIntent) error {
	asked := i.AskedAt
	if asked.IsZero() {
		asked = time.Now().UTC()
	}
	_, err := s.db.Exec(
		`INSERT INTO join_intents (telegram_id, lesson_id, decision, asked_at, decided_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(telegram_id, lesson_id) DO UPDATE SET
			decision = excluded.decision,
			asked_at = excluded.asked_at,
			decided_at = excluded.decided_at`,
		i.TelegramID, i.LessonID, string(i.Decision), timeArg(asked), nullTimeArg(i.DecidedAt),
	)
	if err != nil {
		return fmt.Errorf("store: put intent: %w", err)
	}
	return nil
}

func (s *Store) SetIntentDecision(telegramID, lessonID int64, d model.JoinDecision) error {
	var got int64
	err := s.db.QueryRow(
		`UPDATE join_intents SET decision = ?, decided_at = ?
		 WHERE telegram_id = ? AND lesson_id = ? RETURNING telegram_id`,
		string(d), timeArg(time.Now()), telegramID, lessonID,
	).Scan(&got)
	if errors.Is(err, sql.ErrNoRows) {
		return sql.ErrNoRows
	}
	if err != nil {
		return fmt.Errorf("store: set intent decision: %w", err)
	}
	return nil
}

func (s *Store) GetSetting(key string) (string, bool, error) {
	var val sql.NullString
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&val)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store: get setting: %w", err)
	}
	return nullStr(val), true, nil
}

func (s *Store) SetSetting(key, val string) error {
	_, err := s.db.Exec(
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, val,
	)
	if err != nil {
		return fmt.Errorf("store: set setting: %w", err)
	}
	return nil
}

const raspKickKey = "rasp_refresh"

func (s *Store) RequestRaspRefresh() error {
	return s.SetSetting(raspKickKey, timeArg(time.Now()))
}

func (s *Store) ConsumeRaspRefresh() (bool, error) {
	v, ok, err := s.GetSetting(raspKickKey)
	if err != nil {
		return false, err
	}
	if !ok || strings.TrimSpace(v) == "" {
		return false, nil
	}
	if err := s.SetSetting(raspKickKey, ""); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) SetPresence(p model.Presence) error {
	at := p.UpdatedAt
	if at.IsZero() {
		at = time.Now().UTC()
	}
	state := p.State
	if state == "" {
		state = model.PresenceNone
	}
	_, err := s.db.Exec(
		`INSERT INTO presence (telegram_id, lesson_id, state, message, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(telegram_id) DO UPDATE SET
			lesson_id = excluded.lesson_id,
			state = excluded.state,
			message = excluded.message,
			updated_at = excluded.updated_at`,
		p.TelegramID, p.LessonID, state, p.Message, timeArg(at),
	)
	if err != nil {
		return fmt.Errorf("store: set presence: %w", err)
	}
	return nil
}

func (s *Store) ClearPresence(telegramID int64) error {
	_, err := s.db.Exec(`DELETE FROM presence WHERE telegram_id = ?`, telegramID)
	if err != nil {
		return fmt.Errorf("store: clear presence: %w", err)
	}
	return nil
}

func (s *Store) GetPresence(telegramID int64) (*model.Presence, error) {
	if telegramID == 0 {
		return nil, nil
	}
	var p model.Presence
	var state, msg, updated sql.NullString
	err := s.db.QueryRow(
		`SELECT telegram_id, lesson_id, state, message, updated_at FROM presence WHERE telegram_id = ?`,
		telegramID,
	).Scan(&p.TelegramID, &p.LessonID, &state, &msg, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get presence: %w", err)
	}
	p.State = nullStr(state)
	p.Message = nullStr(msg)
	if updated.Valid && updated.String != "" {
		t, err := parseTime(updated.String)
		if err != nil {
			return nil, fmt.Errorf("store: parse presence.updated_at: %w", err)
		}
		p.UpdatedAt = t
	}
	return &p, nil
}

func (s *Store) ListPresence() ([]model.Presence, error) {
	rows, err := s.db.Query(`SELECT telegram_id, lesson_id, state, message, updated_at FROM presence`)
	if err != nil {
		return nil, fmt.Errorf("store: list presence: %w", err)
	}
	defer rows.Close()
	out := make([]model.Presence, 0)
	for rows.Next() {
		var p model.Presence
		var state, msg, updated sql.NullString
		if err := rows.Scan(&p.TelegramID, &p.LessonID, &state, &msg, &updated); err != nil {
			return nil, fmt.Errorf("store: list presence: %w", err)
		}
		p.State = nullStr(state)
		p.Message = nullStr(msg)
		if updated.Valid && updated.String != "" {
			t, err := parseTime(updated.String)
			if err != nil {
				return nil, fmt.Errorf("store: parse presence.updated_at: %w", err)
			}
			p.UpdatedAt = t
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list presence: %w", err)
	}
	return out, nil
}

func (s *Store) LessonsHappening(now time.Time) ([]model.Lesson, error) {
	t := timeArg(now)
	rows, err := s.db.Query(
		`SELECT `+lessonCols+` FROM lessons
		 WHERE online = 1 AND begin <= ? AND finish > ?
		 ORDER BY begin, id`,
		t, t,
	)
	if err != nil {
		return nil, fmt.Errorf("store: lessons happening: %w", err)
	}
	defer rows.Close()
	out := make([]model.Lesson, 0)
	for rows.Next() {
		l, err := scanLesson(rows)
		if err != nil {
			return nil, fmt.Errorf("store: lessons happening: %w", err)
		}
		out = append(out, *l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: lessons happening: %w", err)
	}
	return out, nil
}
