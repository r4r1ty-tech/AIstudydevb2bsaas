package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("store: mkdir %s: %w", dir, err)
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(8000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: ping sqlite: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (
			telegram_id INTEGER PRIMARY KEY,
			username TEXT,
			first_name TEXT,
			last_name TEXT,
			fio TEXT,
			subgroup INTEGER DEFAULT 1,
			enabled INTEGER DEFAULT 1,
			disabled_until TEXT NULL,
			socks5 TEXT,
			onboarded INTEGER DEFAULT 0,
			created_at TEXT,
			wake_words TEXT,
			onboard_stage INTEGER DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS lessons (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			date TEXT,
			start TEXT,
			end TEXT,
			begin TEXT,
			finish TEXT,
			discipline TEXT,
			teacher TEXT,
			place TEXT,
			subgroup INTEGER,
			type TEXT,
			online INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS parse_runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			at TEXT,
			ok INTEGER,
			status TEXT,
			lesson_count INTEGER,
			online_count INTEGER,
			diff TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS bbb_links (
			key TEXT PRIMARY KEY,
			url TEXT,
			updated_at TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			at TEXT,
			type TEXT,
			telegram_id INTEGER,
			lesson_id INTEGER,
			message TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS join_intents (
			telegram_id INTEGER,
			lesson_id INTEGER,
			decision TEXT,
			asked_at TEXT,
			decided_at TEXT NULL,
			PRIMARY KEY(telegram_id, lesson_id)
		)`,
		`CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS presence (
			telegram_id INTEGER PRIMARY KEY,
			lesson_id INTEGER,
			state TEXT,
			message TEXT,
			updated_at TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS lecture_packs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			lesson_id INTEGER UNIQUE,
			discipline TEXT,
			number INTEGER,
			date TEXT,
			dir TEXT,
			bbb_url TEXT,
			status TEXT,
			audio TEXT,
			transcript TEXT,
			notes_pdf TEXT,
			err TEXT,
			publish_status TEXT,
			published_at TEXT,
			cleaned_at TEXT,
			created_at TEXT,
			updated_at TEXT
		)`,
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: migrate begin: %w", err)
	}
	defer tx.Rollback()
	for _, q := range stmts {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("store: migrate: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: migrate commit: %w", err)
	}
	if err := s.ensureUserColumns(); err != nil {
		return err
	}
	return s.ensurePackColumns()
}

func (s *Store) ensurePackColumns() error {
	alters := []string{
		`ALTER TABLE lecture_packs ADD COLUMN publish_status TEXT`,
		`ALTER TABLE lecture_packs ADD COLUMN published_at TEXT`,
		`ALTER TABLE lecture_packs ADD COLUMN cleaned_at TEXT`,
	}
	for _, q := range alters {
		if _, err := s.db.Exec(q); err != nil {
			if !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
				return fmt.Errorf("store: migrate packs: %w", err)
			}
		}
	}
	return nil
}

func (s *Store) ensureUserColumns() error {
	alters := []string{
		`ALTER TABLE users ADD COLUMN wake_words TEXT`,
		`ALTER TABLE users ADD COLUMN onboard_stage INTEGER DEFAULT 0`,
	}
	for _, q := range alters {
		if _, err := s.db.Exec(q); err != nil {
			if !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
				return fmt.Errorf("store: migrate users: %w", err)
			}
		}
	}
	_, err := s.db.Exec(`UPDATE users SET onboard_stage = 3 WHERE onboarded = 1 AND IFNULL(onboard_stage, 0) = 0`)
	if err != nil {
		return fmt.Errorf("store: backfill onboard_stage: %w", err)
	}
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func timeArg(t time.Time) string {
	return formatTime(t)
}

func nullTimeArg(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}

func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t, err = time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return time.Time{}, err
		}
	}
	return t.UTC(), nil
}

func parseNullTime(ns sql.NullString) (*time.Time, error) {
	if !ns.Valid || ns.String == "" {
		return nil, nil
	}
	t, err := parseTime(ns.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func nullStr(ns sql.NullString) string {
	if !ns.Valid {
		return ""
	}
	return ns.String
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func scanUser(sc scanner) (*model.User, error) {
	var u model.User
	var username, firstName, lastName, fio, socks5, createdAt, wakeWords sql.NullString
	var disabledUntil sql.NullString
	var subgroup, enabled, onboarded, onboardStage sql.NullInt64
	if err := sc.Scan(
		&u.TelegramID,
		&username,
		&firstName,
		&lastName,
		&fio,
		&subgroup,
		&enabled,
		&disabledUntil,
		&socks5,
		&onboarded,
		&createdAt,
		&wakeWords,
		&onboardStage,
	); err != nil {
		return nil, err
	}
	u.Username = nullStr(username)
	u.FirstName = nullStr(firstName)
	u.LastName = nullStr(lastName)
	u.FIO = nullStr(fio)
	u.SOCKS5 = nullStr(socks5)
	u.ExtraWords = model.ParseWakeWords(nullStr(wakeWords))
	if subgroup.Valid {
		u.Subgroup = int(subgroup.Int64)
	}
	if onboardStage.Valid {
		u.OnboardStage = int(onboardStage.Int64)
	}
	u.Enabled = enabled.Valid && enabled.Int64 != 0
	u.Onboarded = onboarded.Valid && onboarded.Int64 != 0
	until, err := parseNullTime(disabledUntil)
	if err != nil {
		return nil, fmt.Errorf("store: parse disabled_until: %w", err)
	}
	u.DisabledUntil = until
	if createdAt.Valid && createdAt.String != "" {
		t, err := parseTime(createdAt.String)
		if err != nil {
			return nil, fmt.Errorf("store: parse created_at: %w", err)
		}
		u.CreatedAt = t
	}
	return &u, nil
}

func scanLesson(sc scanner) (*model.Lesson, error) {
	var l model.Lesson
	var date, start, end, begin, finish, discipline, teacher, place, typ sql.NullString
	var subgroup, online sql.NullInt64
	if err := sc.Scan(
		&l.ID,
		&date,
		&start,
		&end,
		&begin,
		&finish,
		&discipline,
		&teacher,
		&place,
		&subgroup,
		&typ,
		&online,
	); err != nil {
		return nil, err
	}
	l.Date = nullStr(date)
	l.Start = nullStr(start)
	l.End = nullStr(end)
	l.Discipline = nullStr(discipline)
	l.Teacher = nullStr(teacher)
	l.Place = nullStr(place)
	l.Type = nullStr(typ)
	if subgroup.Valid {
		l.Subgroup = int(subgroup.Int64)
	}
	l.Online = online.Valid && online.Int64 != 0
	if begin.Valid && begin.String != "" {
		t, err := parseTime(begin.String)
		if err != nil {
			return nil, fmt.Errorf("store: parse begin: %w", err)
		}
		l.Begin = t
	}
	if finish.Valid && finish.String != "" {
		t, err := parseTime(finish.String)
		if err != nil {
			return nil, fmt.Errorf("store: parse finish: %w", err)
		}
		l.Finish = t
	}
	return &l, nil
}

func scanParseRun(sc scanner) (*model.ParseRun, error) {
	var r model.ParseRun
	var at, status, diff sql.NullString
	var ok, lessonCount, onlineCount sql.NullInt64
	if err := sc.Scan(&r.ID, &at, &ok, &status, &lessonCount, &onlineCount, &diff); err != nil {
		return nil, err
	}
	if at.Valid && at.String != "" {
		t, err := parseTime(at.String)
		if err != nil {
			return nil, fmt.Errorf("store: parse parse_run.at: %w", err)
		}
		r.At = t
	}
	r.OK = ok.Valid && ok.Int64 != 0
	r.Status = nullStr(status)
	if lessonCount.Valid {
		r.LessonCount = int(lessonCount.Int64)
	}
	if onlineCount.Valid {
		r.OnlineCount = int(onlineCount.Int64)
	}
	r.Diff = nullStr(diff)
	return &r, nil
}

func scanBBB(sc scanner) (*model.BBBLink, error) {
	var b model.BBBLink
	var key, url, updated sql.NullString
	if err := sc.Scan(&key, &url, &updated); err != nil {
		return nil, err
	}
	b.Key = nullStr(key)
	b.URL = nullStr(url)
	if updated.Valid && updated.String != "" {
		t, err := parseTime(updated.String)
		if err != nil {
			return nil, fmt.Errorf("store: parse bbb.updated_at: %w", err)
		}
		b.UpdatedAt = t
	}
	return &b, nil
}

func scanEvent(sc scanner) (*model.Event, error) {
	var e model.Event
	var at, typ, message sql.NullString
	var telegramID, lessonID sql.NullInt64
	if err := sc.Scan(&e.ID, &at, &typ, &telegramID, &lessonID, &message); err != nil {
		return nil, err
	}
	if at.Valid && at.String != "" {
		t, err := parseTime(at.String)
		if err != nil {
			return nil, fmt.Errorf("store: parse event.at: %w", err)
		}
		e.At = t
	}
	e.Type = nullStr(typ)
	if telegramID.Valid {
		e.TelegramID = telegramID.Int64
	}
	if lessonID.Valid {
		e.LessonID = lessonID.Int64
	}
	e.Message = nullStr(message)
	return &e, nil
}

func scanIntent(sc scanner) (*model.JoinIntent, error) {
	var i model.JoinIntent
	var decision, askedAt, decidedAt sql.NullString
	if err := sc.Scan(&i.TelegramID, &i.LessonID, &decision, &askedAt, &decidedAt); err != nil {
		return nil, err
	}
	i.Decision = model.JoinDecision(nullStr(decision))
	if askedAt.Valid && askedAt.String != "" {
		t, err := parseTime(askedAt.String)
		if err != nil {
			return nil, fmt.Errorf("store: parse intent.asked_at: %w", err)
		}
		i.AskedAt = t
	}
	until, err := parseNullTime(decidedAt)
	if err != nil {
		return nil, fmt.Errorf("store: parse intent.decided_at: %w", err)
	}
	i.DecidedAt = until
	return &i, nil
}
