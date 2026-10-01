package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

// GetLessonBBB — ссылка пары или "" (ошибки БД тоже дают "", см. LessonBBB).
func (s *Store) GetLessonBBB(lessonID int64) string {
	url, _, err := s.LessonBBB(lessonID)
	if err != nil {
		logx.Warnf("store", "GetLessonBBB: lesson_id=%d: %v", lessonID, err)
		return ""
	}
	return url
}

// LessonBBB разрешает ссылку пары через model.ResolveBBB. own — ссылка привязана
// к самой паре, а не унаследована от комнаты предмета.
func (s *Store) LessonBBB(lessonID int64) (url string, own bool, err error) {
	logx.Debugf("store", "LessonBBB: enter lesson_id=%d", lessonID)
	if s == nil || lessonID <= 0 {
		return "", false, nil
	}
	b, err := s.GetBBB(model.BBBLessonKey(lessonID))
	if err != nil {
		return "", false, err
	}
	if b != nil {
		return strings.TrimSpace(b.URL), true, nil
	}
	l, err := s.LessonByID(lessonID)
	if err != nil || l == nil {
		return "", false, err
	}
	links, err := s.ListBBB()
	if err != nil {
		return "", false, err
	}
	url, own = model.ResolveBBB(*l, links)
	logx.Debugf("store", "LessonBBB: lesson_id=%d present=%v own=%v", lessonID, url != "", own)
	return url, own, nil
}

// SetLessonBBB привязывает ссылку к паре и запоминает её как комнату предмета
// у этого препода для этого типа пары — на следующих неделях подставится сама.
// Пустой url снимает ссылку только с этой пары.
func (s *Store) SetLessonBBB(lessonID int64, url string) error {
	logx.Debugf("store", "SetLessonBBB: enter lesson_id=%d url_empty=%v", lessonID, strings.TrimSpace(url) == "")
	if lessonID <= 0 {
		logx.Errorf("store", "SetLessonBBB: empty lesson id")
		return fmt.Errorf("store: lesson bbb: empty lesson id")
	}
	url = strings.TrimSpace(url)
	if err := s.SetBBB(model.BBBLessonKey(lessonID), url); err != nil {
		logx.Errorf("store", "SetLessonBBB: set bbb lesson_id=%d: %v", lessonID, err)
		return err
	}
	if url == "" {
		return nil
	}
	l, err := s.LessonByID(lessonID)
	if err != nil {
		logx.Warnf("store", "SetLessonBBB: lesson_id=%d: %v", lessonID, err)
		return nil
	}
	if l != nil {
		if err := s.SetBBB(model.BBBRoomKey(l.Discipline, l.Teacher, l.Type), url); err != nil {
			logx.Errorf("store", "SetLessonBBB: set room lesson_id=%d: %v", lessonID, err)
			return err
		}
	}
	logx.Debugf("store", "SetLessonBBB: done lesson_id=%d", lessonID)
	return nil
}

func (s *Store) GetBBB(key string) (*model.BBBLink, error) {
	logx.Debugf("store", "GetBBB: enter key=%s", key)
	b, err := scanBBB(s.db.QueryRow(`SELECT key, url, updated_at FROM bbb_links WHERE key = ?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		logx.Debugf("store", "GetBBB: not found key=%s", key)
		return nil, nil
	}
	if err != nil {
		logx.Errorf("store", "GetBBB: key=%s: %v", key, err)
		return nil, fmt.Errorf("store: get bbb: %w", err)
	}
	logx.Debugf("store", "GetBBB: out key=%s", b.Key)
	return b, nil
}

func (s *Store) SetBBB(key, url string) error {
	logx.Debugf("store", "SetBBB: enter key=%s url_empty=%v", key, strings.TrimSpace(url) == "")
	_, err := s.db.Exec(
		`INSERT INTO bbb_links (key, url, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET url = excluded.url, updated_at = excluded.updated_at`,
		key, url, timeArg(time.Now()),
	)
	if err != nil {
		logx.Errorf("store", "SetBBB: key=%s: %v", key, err)
		return fmt.Errorf("store: set bbb: %w", err)
	}
	logx.Debugf("store", "SetBBB: done key=%s", key)
	return nil
}

func (s *Store) ListBBB() ([]model.BBBLink, error) {
	logx.Debugf("store", "ListBBB: enter")
	rows, err := s.db.Query(`SELECT key, url, updated_at FROM bbb_links ORDER BY key`)
	if err != nil {
		logx.Errorf("store", "ListBBB: query: %v", err)
		return nil, fmt.Errorf("store: list bbb: %w", err)
	}
	defer rows.Close()
	out := make([]model.BBBLink, 0)
	for rows.Next() {
		b, err := scanBBB(rows)
		if err != nil {
			logx.Errorf("store", "ListBBB: scan: %v", err)
			return nil, fmt.Errorf("store: list bbb: %w", err)
		}
		out = append(out, *b)
	}
	if err := rows.Err(); err != nil {
		logx.Errorf("store", "ListBBB: rows: %v", err)
		return nil, fmt.Errorf("store: list bbb: %w", err)
	}
	logx.Debugf("store", "ListBBB: out count=%d", len(out))
	return out, nil
}

func (s *Store) AddEvent(e model.Event) error {
	logx.Debugf("store", "AddEvent: enter type=%s telegram_id=%d lesson_id=%d", e.Type, e.TelegramID, e.LessonID)
	at := e.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	_, err := s.db.Exec(
		`INSERT INTO events (at, type, telegram_id, lesson_id, message) VALUES (?, ?, ?, ?, ?)`,
		timeArg(at), e.Type, e.TelegramID, e.LessonID, e.Message,
	)
	if err != nil {
		logx.Errorf("store", "AddEvent: type=%s telegram_id=%d lesson_id=%d: %v", e.Type, e.TelegramID, e.LessonID, err)
		return fmt.Errorf("store: add event: %w", err)
	}
	logx.Debugf("store", "AddEvent: done type=%s telegram_id=%d", e.Type, e.TelegramID)
	return nil
}

func (s *Store) ListEvents(limit int) ([]model.Event, error) {
	logx.Debugf("store", "ListEvents: enter limit=%d", limit)
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.Query(
		`SELECT id, at, type, telegram_id, lesson_id, message FROM events
		 ORDER BY at DESC, id DESC LIMIT ?`,
		limit,
	)
	if err != nil {
		logx.Errorf("store", "ListEvents: query limit=%d: %v", limit, err)
		return nil, fmt.Errorf("store: list events: %w", err)
	}
	defer rows.Close()
	out := make([]model.Event, 0)
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			logx.Errorf("store", "ListEvents: scan: %v", err)
			return nil, fmt.Errorf("store: list events: %w", err)
		}
		out = append(out, *e)
	}
	if err := rows.Err(); err != nil {
		logx.Errorf("store", "ListEvents: rows: %v", err)
		return nil, fmt.Errorf("store: list events: %w", err)
	}
	logx.Debugf("store", "ListEvents: out count=%d limit=%d", len(out), limit)
	return out, nil
}

func (s *Store) GetIntent(telegramID, lessonID int64) (*model.JoinIntent, error) {
	logx.Debugf("store", "GetIntent: enter telegram_id=%d lesson_id=%d", telegramID, lessonID)
	i, err := scanIntent(s.db.QueryRow(
		`SELECT telegram_id, lesson_id, decision, asked_at, decided_at
		 FROM join_intents WHERE telegram_id = ? AND lesson_id = ?`,
		telegramID, lessonID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		logx.Debugf("store", "GetIntent: not found telegram_id=%d lesson_id=%d", telegramID, lessonID)
		return nil, nil
	}
	if err != nil {
		logx.Errorf("store", "GetIntent: telegram_id=%d lesson_id=%d: %v", telegramID, lessonID, err)
		return nil, fmt.Errorf("store: get intent: %w", err)
	}
	logx.Debugf("store", "GetIntent: out telegram_id=%d lesson_id=%d decision=%s", i.TelegramID, i.LessonID, i.Decision)
	return i, nil
}

func (s *Store) PutIntent(i model.JoinIntent) error {
	logx.Debugf("store", "PutIntent: enter telegram_id=%d lesson_id=%d decision=%s", i.TelegramID, i.LessonID, i.Decision)
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
		logx.Errorf("store", "PutIntent: telegram_id=%d lesson_id=%d: %v", i.TelegramID, i.LessonID, err)
		return fmt.Errorf("store: put intent: %w", err)
	}
	logx.Debugf("store", "PutIntent: done telegram_id=%d lesson_id=%d", i.TelegramID, i.LessonID)
	return nil
}

func (s *Store) SetIntentDecision(telegramID, lessonID int64, d model.JoinDecision) error {
	logx.Debugf("store", "SetIntentDecision: enter telegram_id=%d lesson_id=%d decision=%s", telegramID, lessonID, d)
	var got int64
	err := s.db.QueryRow(
		`UPDATE join_intents SET decision = ?, decided_at = ?
		 WHERE telegram_id = ? AND lesson_id = ? RETURNING telegram_id`,
		string(d), timeArg(time.Now()), telegramID, lessonID,
	).Scan(&got)
	if errors.Is(err, sql.ErrNoRows) {
		logx.Debugf("store", "SetIntentDecision: no rows telegram_id=%d lesson_id=%d", telegramID, lessonID)
		return sql.ErrNoRows
	}
	if err != nil {
		logx.Errorf("store", "SetIntentDecision: telegram_id=%d lesson_id=%d: %v", telegramID, lessonID, err)
		return fmt.Errorf("store: set intent decision: %w", err)
	}
	logx.Debugf("store", "SetIntentDecision: done telegram_id=%d lesson_id=%d", telegramID, lessonID)
	return nil
}

func (s *Store) GetSetting(key string) (string, bool, error) {
	logx.Debugf("store", "GetSetting: enter key=%s", key)
	var val sql.NullString
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&val)
	if errors.Is(err, sql.ErrNoRows) {
		logx.Debugf("store", "GetSetting: not found key=%s", key)
		return "", false, nil
	}
	if err != nil {
		logx.Errorf("store", "GetSetting: key=%s: %v", key, err)
		return "", false, fmt.Errorf("store: get setting: %w", err)
	}
	logx.Debugf("store", "GetSetting: out key=%s found=true value_empty=%v", key, nullStr(val) == "")
	return nullStr(val), true, nil
}

func (s *Store) SetSetting(key, val string) error {
	logx.Debugf("store", "SetSetting: enter key=%s value_len=%d", key, len(val))
	_, err := s.db.Exec(
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, val,
	)
	if err != nil {
		logx.Errorf("store", "SetSetting: key=%s: %v", key, err)
		return fmt.Errorf("store: set setting: %w", err)
	}
	logx.Debugf("store", "SetSetting: done key=%s", key)
	return nil
}

const raspKickKey = "rasp_refresh"

func (s *Store) RequestRaspRefresh() error {
	logx.Debugf("store", "RequestRaspRefresh: enter")
	err := s.SetSetting(raspKickKey, timeArg(time.Now()))
	if err != nil {
		logx.Errorf("store", "RequestRaspRefresh: set setting: %v", err)
		return err
	}
	logx.Debugf("store", "RequestRaspRefresh: done")
	return nil
}

func (s *Store) ConsumeRaspRefresh() (bool, error) {
	logx.Debugf("store", "ConsumeRaspRefresh: enter")
	v, ok, err := s.GetSetting(raspKickKey)
	if err != nil {
		logx.Errorf("store", "ConsumeRaspRefresh: get setting: %v", err)
		return false, err
	}
	if !ok || strings.TrimSpace(v) == "" {
		logx.Debugf("store", "ConsumeRaspRefresh: nothing to consume")
		return false, nil
	}
	if err := s.SetSetting(raspKickKey, ""); err != nil {
		logx.Errorf("store", "ConsumeRaspRefresh: clear setting: %v", err)
		return false, err
	}
	logx.Debugf("store", "ConsumeRaspRefresh: consumed")
	return true, nil
}

func (s *Store) SetPresence(p model.Presence) error {
	logx.Debugf("store", "SetPresence: enter telegram_id=%d lesson_id=%d state=%s", p.TelegramID, p.LessonID, p.State)
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
		logx.Errorf("store", "SetPresence: telegram_id=%d lesson_id=%d: %v", p.TelegramID, p.LessonID, err)
		return fmt.Errorf("store: set presence: %w", err)
	}
	logx.Debugf("store", "SetPresence: done telegram_id=%d state=%s", p.TelegramID, state)
	return nil
}

func (s *Store) ClearPresence(telegramID int64) error {
	logx.Debugf("store", "ClearPresence: enter telegram_id=%d", telegramID)
	_, err := s.db.Exec(`DELETE FROM presence WHERE telegram_id = ?`, telegramID)
	if err != nil {
		logx.Errorf("store", "ClearPresence: telegram_id=%d: %v", telegramID, err)
		return fmt.Errorf("store: clear presence: %w", err)
	}
	logx.Debugf("store", "ClearPresence: done telegram_id=%d", telegramID)
	return nil
}

// ClearPresenceFor снимает presence, только если она про эту пару: выход из
// одной пары не стирает состояние соседней.
func (s *Store) ClearPresenceFor(telegramID, lessonID int64) error {
	if _, err := s.db.Exec(`DELETE FROM presence WHERE telegram_id = ? AND lesson_id = ?`, telegramID, lessonID); err != nil {
		logx.Errorf("store", "ClearPresenceFor: tg=%d lesson=%d: %v", telegramID, lessonID, err)
		return fmt.Errorf("store: clear presence: %w", err)
	}
	return nil
}

// ClearAllPresence — на старте и остановке bbb: живых сессий нет, старые
// «в комнате» после рестарта или краша врут.
func (s *Store) ClearAllPresence() error {
	if _, err := s.db.Exec(`DELETE FROM presence`); err != nil {
		logx.Errorf("store", "ClearAllPresence: %v", err)
		return fmt.Errorf("store: clear all presence: %w", err)
	}
	return nil
}

func (s *Store) GetPresence(telegramID int64) (*model.Presence, error) {
	logx.Debugf("store", "GetPresence: enter telegram_id=%d", telegramID)
	if telegramID == 0 {
		logx.Debugf("store", "GetPresence: zero telegram_id")
		return nil, nil
	}
	var p model.Presence
	var state, msg, updated sql.NullString
	err := s.db.QueryRow(
		`SELECT telegram_id, lesson_id, state, message, updated_at FROM presence WHERE telegram_id = ?`,
		telegramID,
	).Scan(&p.TelegramID, &p.LessonID, &state, &msg, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		logx.Debugf("store", "GetPresence: not found telegram_id=%d", telegramID)
		return nil, nil
	}
	if err != nil {
		logx.Errorf("store", "GetPresence: telegram_id=%d: %v", telegramID, err)
		return nil, fmt.Errorf("store: get presence: %w", err)
	}
	p.State = nullStr(state)
	p.Message = nullStr(msg)
	if updated.Valid && updated.String != "" {
		t, err := parseTime(updated.String)
		if err != nil {
			logx.Errorf("store", "GetPresence: parse updated_at telegram_id=%d: %v", telegramID, err)
			return nil, fmt.Errorf("store: parse presence.updated_at: %w", err)
		}
		p.UpdatedAt = t
	}
	logx.Debugf("store", "GetPresence: out telegram_id=%d lesson_id=%d state=%s", p.TelegramID, p.LessonID, p.State)
	return &p, nil
}

func (s *Store) ListPresence() ([]model.Presence, error) {
	logx.Debugf("store", "ListPresence: enter")
	rows, err := s.db.Query(`SELECT telegram_id, lesson_id, state, message, updated_at FROM presence`)
	if err != nil {
		logx.Errorf("store", "ListPresence: query: %v", err)
		return nil, fmt.Errorf("store: list presence: %w", err)
	}
	defer rows.Close()
	out := make([]model.Presence, 0)
	for rows.Next() {
		var p model.Presence
		var state, msg, updated sql.NullString
		if err := rows.Scan(&p.TelegramID, &p.LessonID, &state, &msg, &updated); err != nil {
			logx.Errorf("store", "ListPresence: scan: %v", err)
			return nil, fmt.Errorf("store: list presence: %w", err)
		}
		p.State = nullStr(state)
		p.Message = nullStr(msg)
		if updated.Valid && updated.String != "" {
			t, err := parseTime(updated.String)
			if err != nil {
				logx.Errorf("store", "ListPresence: parse updated_at telegram_id=%d: %v", p.TelegramID, err)
				return nil, fmt.Errorf("store: parse presence.updated_at: %w", err)
			}
			p.UpdatedAt = t
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		logx.Errorf("store", "ListPresence: rows: %v", err)
		return nil, fmt.Errorf("store: list presence: %w", err)
	}
	logx.Debugf("store", "ListPresence: out count=%d", len(out))
	return out, nil
}

func (s *Store) LessonsHappening(now time.Time) ([]model.Lesson, error) {
	logx.Debugf("store", "LessonsHappening: enter now=%s", now.UTC().Format(time.RFC3339))
	out, err := s.LessonsInJoinWindow(now, 0)
	if err != nil {
		logx.Errorf("store", "LessonsHappening: now=%s: %v", now.UTC().Format(time.RFC3339), err)
		return nil, err
	}
	logx.Debugf("store", "LessonsHappening: out count=%d", len(out))
	return out, nil
}

func (s *Store) LessonsInJoinWindow(now time.Time, early time.Duration) ([]model.Lesson, error) {
	logx.Debugf("store", "LessonsInJoinWindow: enter now=%s early=%s", now.UTC().Format(time.RFC3339), early)
	if early < 0 {
		early = 0
	}
	rows, err := s.db.Query(
		`SELECT `+lessonCols+` FROM lessons
		 WHERE online = 1 AND begin <= ? AND finish > ?
		 ORDER BY begin, id`,
		timeArg(now.Add(early)), timeArg(now),
	)
	if err != nil {
		logx.Errorf("store", "LessonsInJoinWindow: query now=%s early=%s: %v", timeArg(now), early, err)
		return nil, fmt.Errorf("store: lessons join window: %w", err)
	}
	defer rows.Close()
	out := make([]model.Lesson, 0)
	for rows.Next() {
		l, err := scanLesson(rows)
		if err != nil {
			logx.Errorf("store", "LessonsInJoinWindow: scan: %v", err)
			return nil, fmt.Errorf("store: lessons join window: %w", err)
		}
		out = append(out, *l)
	}
	if err := rows.Err(); err != nil {
		logx.Errorf("store", "LessonsInJoinWindow: rows: %v", err)
		return nil, fmt.Errorf("store: lessons join window: %w", err)
	}
	logx.Debugf("store", "LessonsInJoinWindow: out count=%d", len(out))
	return out, nil
}
