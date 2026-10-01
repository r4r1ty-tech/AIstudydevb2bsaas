package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	logx.Debugf("store", "Open: enter path=%s", path)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		logx.Errorf("store", "Open: mkdir %s: %v", dir, err)
		return nil, fmt.Errorf("store: mkdir %s: %w", dir, err)
	}
	logx.Debugf("store", "Open: dir ready dir=%s", dir)
	dsn := "file:" + path + "?_pragma=busy_timeout(8000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		logx.Errorf("store", "Open: open sqlite path=%s: %v", path, err)
		return nil, fmt.Errorf("store: open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		if cerr := db.Close(); cerr != nil {
			logx.Warnf("store", "Open: close after ping failure path=%s: %v", path, cerr)
		}
		logx.Errorf("store", "Open: ping sqlite path=%s: %v", path, err)
		return nil, fmt.Errorf("store: ping sqlite: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		if cerr := db.Close(); cerr != nil {
			logx.Warnf("store", "Open: close after migrate failure path=%s: %v", path, cerr)
		}
		logx.Errorf("store", "Open: migrate path=%s: %v", path, err)
		return nil, err
	}
	logx.Infof("store", "Open: ready path=%s", path)
	return s, nil
}

func (s *Store) Close() error {
	logx.Debugf("store", "Close: enter")
	if s == nil || s.db == nil {
		logx.Debugf("store", "Close: no db")
		return nil
	}
	// db не зануляем: после Close sql.DB отвечает «database is closed», а
	// nil-указатель ронял бы паникой запоздалый тик T-15/лобби при остановке.
	err := s.db.Close()
	if err != nil {
		logx.Errorf("store", "Close: close db: %v", err)
		return err
	}
	logx.Debugf("store", "Close: done")
	return nil
}

func (s *Store) migrate() error {
	logx.Debugf("store", "migrate: enter")
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
	logx.Debugf("store", "migrate: statements=%d", len(stmts))
	tx, err := s.db.Begin()
	if err != nil {
		logx.Errorf("store", "migrate: begin: %v", err)
		return fmt.Errorf("store: migrate begin: %w", err)
	}
	defer func() {
		if rerr := tx.Rollback(); rerr != nil && rerr != sql.ErrTxDone {
			logx.Warnf("store", "migrate: rollback: %v", rerr)
		}
	}()
	for i, q := range stmts {
		if _, err := tx.Exec(q); err != nil {
			logx.Errorf("store", "migrate: stmt %d: %v", i, err)
			return fmt.Errorf("store: migrate: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		logx.Errorf("store", "migrate: commit: %v", err)
		return fmt.Errorf("store: migrate commit: %w", err)
	}
	logx.Infof("store", "migrate: schema applied statements=%d", len(stmts))
	if err := s.ensureUserColumns(); err != nil {
		logx.Errorf("store", "migrate: ensure user columns: %v", err)
		return err
	}
	if err := s.ensurePackColumns(); err != nil {
		logx.Errorf("store", "migrate: ensure pack columns: %v", err)
		return err
	}
	logx.Debugf("store", "migrate: done")
	return nil
}

func (s *Store) ensurePackColumns() error {
	logx.Debugf("store", "ensurePackColumns: enter")
	alters := []string{
		`ALTER TABLE lecture_packs ADD COLUMN publish_status TEXT`,
		`ALTER TABLE lecture_packs ADD COLUMN published_at TEXT`,
		`ALTER TABLE lecture_packs ADD COLUMN cleaned_at TEXT`,
	}
	for i, q := range alters {
		if _, err := s.db.Exec(q); err != nil {
			if !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
				logx.Errorf("store", "ensurePackColumns: alter %d: %v", i, err)
				return fmt.Errorf("store: migrate packs: %w", err)
			}
			logx.Debugf("store", "ensurePackColumns: alter %d already present", i)
			continue
		}
		logx.Debugf("store", "ensurePackColumns: alter %d applied", i)
	}
	logx.Debugf("store", "ensurePackColumns: done")
	return nil
}

func (s *Store) ensureUserColumns() error {
	logx.Debugf("store", "ensureUserColumns: enter")
	alters := []string{
		`ALTER TABLE users ADD COLUMN wake_words TEXT`,
		`ALTER TABLE users ADD COLUMN onboard_stage INTEGER DEFAULT 0`,
	}
	for i, q := range alters {
		if _, err := s.db.Exec(q); err != nil {
			if !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
				logx.Errorf("store", "ensureUserColumns: alter %d: %v", i, err)
				return fmt.Errorf("store: migrate users: %w", err)
			}
			logx.Debugf("store", "ensureUserColumns: alter %d already present", i)
			continue
		}
		logx.Debugf("store", "ensureUserColumns: alter %d applied", i)
	}
	if _, err := s.db.Exec(`UPDATE users SET onboard_stage = 3 WHERE onboarded = 1 AND IFNULL(onboard_stage, 0) = 0`); err != nil {
		logx.Errorf("store", "ensureUserColumns: backfill onboard_stage: %v", err)
		return fmt.Errorf("store: backfill onboard_stage: %w", err)
	}
	logx.Debugf("store", "ensureUserColumns: done")
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func formatTime(t time.Time) string {
	logx.Debugf("store", "formatTime: enter t=%s", t.UTC().Format(time.RFC3339))
	out := t.UTC().Format(time.RFC3339)
	logx.Debugf("store", "formatTime: out=%s", out)
	return out
}

func timeArg(t time.Time) string {
	logx.Debugf("store", "timeArg: enter t=%s", t.UTC().Format(time.RFC3339))
	out := formatTime(t)
	logx.Debugf("store", "timeArg: out=%s", out)
	return out
}

func nullTimeArg(t *time.Time) any {
	if t == nil {
		logx.Debugf("store", "nullTimeArg: nil")
		return nil
	}
	out := formatTime(*t)
	logx.Debugf("store", "nullTimeArg: out=%s", out)
	return out
}

func parseTime(s string) (time.Time, error) {
	logx.Debugf("store", "parseTime: enter s=%q", s)
	if s == "" {
		logx.Debugf("store", "parseTime: empty")
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t, err = time.Parse(time.RFC3339Nano, s)
		if err != nil {
			logx.Errorf("store", "parseTime: parse %q: %v", s, err)
			return time.Time{}, err
		}
	}
	out := t.UTC()
	logx.Debugf("store", "parseTime: out=%s", out.Format(time.RFC3339Nano))
	return out, nil
}

func parseNullTime(ns sql.NullString) (*time.Time, error) {
	logx.Debugf("store", "parseNullTime: enter valid=%v value=%q", ns.Valid, ns.String)
	if !ns.Valid || ns.String == "" {
		logx.Debugf("store", "parseNullTime: null")
		return nil, nil
	}
	t, err := parseTime(ns.String)
	if err != nil {
		logx.Errorf("store", "parseNullTime: parse %q: %v", ns.String, err)
		return nil, err
	}
	logx.Debugf("store", "parseNullTime: out=%s", t.Format(time.RFC3339Nano))
	return &t, nil
}

func nullStr(ns sql.NullString) string {
	if !ns.Valid {
		logx.Debugf("store", "nullStr: invalid")
		return ""
	}
	logx.Debugf("store", "nullStr: value=%q", ns.String)
	return ns.String
}

func btoi(b bool) int {
	if b {
		logx.Debugf("store", "btoi: true")
		return 1
	}
	logx.Debugf("store", "btoi: false")
	return 0
}

func scanUser(sc scanner) (*model.User, error) {
	logx.Debugf("store", "scanUser: enter")
	var u model.User
	var username, firstName, lastName, fio, createdAt, wakeWords sql.NullString
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
		&onboarded,
		&createdAt,
		&wakeWords,
		&onboardStage,
	); err != nil {
		logScanErr("scanUser", err)
		return nil, err
	}
	u.Username = nullStr(username)
	u.FirstName = nullStr(firstName)
	u.LastName = nullStr(lastName)
	u.FIO = nullStr(fio)
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
		logx.Errorf("store", "scanUser: parse disabled_until telegram_id=%d: %v", u.TelegramID, err)
		return nil, fmt.Errorf("store: parse disabled_until: %w", err)
	}
	u.DisabledUntil = until
	if createdAt.Valid && createdAt.String != "" {
		t, err := parseTime(createdAt.String)
		if err != nil {
			logx.Errorf("store", "scanUser: parse created_at telegram_id=%d: %v", u.TelegramID, err)
			return nil, fmt.Errorf("store: parse created_at: %w", err)
		}
		u.CreatedAt = t
	}
	logx.Debugf("store", "scanUser: out telegram_id=%d", u.TelegramID)
	return &u, nil
}

func scanLesson(sc scanner) (*model.Lesson, error) {
	logx.Debugf("store", "scanLesson: enter")
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
		logScanErr("scanLesson", err)
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
			logx.Errorf("store", "scanLesson: parse begin id=%d: %v", l.ID, err)
			return nil, fmt.Errorf("store: parse begin: %w", err)
		}
		l.Begin = t
	}
	if finish.Valid && finish.String != "" {
		t, err := parseTime(finish.String)
		if err != nil {
			logx.Errorf("store", "scanLesson: parse finish id=%d: %v", l.ID, err)
			return nil, fmt.Errorf("store: parse finish: %w", err)
		}
		l.Finish = t
	}
	logx.Debugf("store", "scanLesson: out id=%d online=%v", l.ID, l.Online)
	return &l, nil
}

func scanParseRun(sc scanner) (*model.ParseRun, error) {
	logx.Debugf("store", "scanParseRun: enter")
	var r model.ParseRun
	var at, status, diff sql.NullString
	var ok, lessonCount, onlineCount sql.NullInt64
	if err := sc.Scan(&r.ID, &at, &ok, &status, &lessonCount, &onlineCount, &diff); err != nil {
		logScanErr("scanParseRun", err)
		return nil, err
	}
	if at.Valid && at.String != "" {
		t, err := parseTime(at.String)
		if err != nil {
			logx.Errorf("store", "scanParseRun: parse at id=%d: %v", r.ID, err)
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
	logx.Debugf("store", "scanParseRun: out id=%d ok=%v lessons=%d", r.ID, r.OK, r.LessonCount)
	return &r, nil
}

func scanBBB(sc scanner) (*model.BBBLink, error) {
	logx.Debugf("store", "scanBBB: enter")
	var b model.BBBLink
	var key, url, updated sql.NullString
	if err := sc.Scan(&key, &url, &updated); err != nil {
		logScanErr("scanBBB", err)
		return nil, err
	}
	b.Key = nullStr(key)
	b.URL = nullStr(url)
	if updated.Valid && updated.String != "" {
		t, err := parseTime(updated.String)
		if err != nil {
			logx.Errorf("store", "scanBBB: parse updated_at key=%s: %v", b.Key, err)
			return nil, fmt.Errorf("store: parse bbb.updated_at: %w", err)
		}
		b.UpdatedAt = t
	}
	logx.Debugf("store", "scanBBB: out key=%s", b.Key)
	return &b, nil
}

func scanEvent(sc scanner) (*model.Event, error) {
	logx.Debugf("store", "scanEvent: enter")
	var e model.Event
	var at, typ, message sql.NullString
	var telegramID, lessonID sql.NullInt64
	if err := sc.Scan(&e.ID, &at, &typ, &telegramID, &lessonID, &message); err != nil {
		logScanErr("scanEvent", err)
		return nil, err
	}
	if at.Valid && at.String != "" {
		t, err := parseTime(at.String)
		if err != nil {
			logx.Errorf("store", "scanEvent: parse at id=%d: %v", e.ID, err)
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
	logx.Debugf("store", "scanEvent: out id=%d type=%s telegram_id=%d lesson_id=%d", e.ID, e.Type, e.TelegramID, e.LessonID)
	return &e, nil
}

func scanIntent(sc scanner) (*model.JoinIntent, error) {
	logx.Debugf("store", "scanIntent: enter")
	var i model.JoinIntent
	var decision, askedAt, decidedAt sql.NullString
	if err := sc.Scan(&i.TelegramID, &i.LessonID, &decision, &askedAt, &decidedAt); err != nil {
		logScanErr("scanIntent", err)
		return nil, err
	}
	i.Decision = model.JoinDecision(nullStr(decision))
	if askedAt.Valid && askedAt.String != "" {
		t, err := parseTime(askedAt.String)
		if err != nil {
			logx.Errorf("store", "scanIntent: parse asked_at telegram_id=%d lesson_id=%d: %v", i.TelegramID, i.LessonID, err)
			return nil, fmt.Errorf("store: parse intent.asked_at: %w", err)
		}
		i.AskedAt = t
	}
	until, err := parseNullTime(decidedAt)
	if err != nil {
		logx.Errorf("store", "scanIntent: parse decided_at telegram_id=%d lesson_id=%d: %v", i.TelegramID, i.LessonID, err)
		return nil, fmt.Errorf("store: parse intent.decided_at: %w", err)
	}
	i.DecidedAt = until
	logx.Debugf("store", "scanIntent: out telegram_id=%d lesson_id=%d decision=%s", i.TelegramID, i.LessonID, i.Decision)
	return &i, nil
}

// logScanErr: «строки нет» — штатный ответ, его разбирают вызывающие; ERROR только на настоящие сбои.
func logScanErr(fn string, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		logx.Debugf("store", "%s: no rows", fn)
		return
	}
	logx.Errorf("store", "%s: scan: %v", fn, err)
}
