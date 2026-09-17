package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

const lessonCols = `id, date, start, end, begin, finish, discipline, teacher, place, subgroup, type, online`

func (s *Store) ReplaceLessons(lessons []model.Lesson) error {
	logx.Debugf("store", "ReplaceLessons: enter count=%d", len(lessons))
	tx, err := s.db.Begin()
	if err != nil {
		logx.Errorf("store", "ReplaceLessons: begin: %v", err)
		return fmt.Errorf("store: replace lessons begin: %w", err)
	}
	defer func() {
		if rerr := tx.Rollback(); rerr != nil && rerr != sql.ErrTxDone {
			logx.Warnf("store", "ReplaceLessons: rollback: %v", rerr)
		}
	}()

	rows, err := tx.Query(`SELECT ` + lessonCols + ` FROM lessons`)
	if err != nil {
		logx.Errorf("store", "ReplaceLessons: query old: %v", err)
		return fmt.Errorf("store: replace lessons list: %w", err)
	}
	oldByIdent := make(map[string]int64)
	for rows.Next() {
		l, err := scanLesson(rows)
		if err != nil {
			rows.Close()
			logx.Errorf("store", "ReplaceLessons: scan old: %v", err)
			return fmt.Errorf("store: replace lessons scan: %w", err)
		}
		oldByIdent[l.Identity()] = l.ID
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		logx.Errorf("store", "ReplaceLessons: old rows: %v", err)
		return fmt.Errorf("store: replace lessons rows: %w", err)
	}
	rows.Close()
	logx.Debugf("store", "ReplaceLessons: existing count=%d", len(oldByIdent))

	upd, err := tx.Prepare(`UPDATE lessons SET date=?, start=?, end=?, begin=?, finish=?, discipline=?, teacher=?, place=?, subgroup=?, type=?, online=? WHERE id=?`)
	if err != nil {
		logx.Errorf("store", "ReplaceLessons: prepare update: %v", err)
		return fmt.Errorf("store: replace lessons update: %w", err)
	}
	defer upd.Close()
	ins, err := tx.Prepare(`INSERT INTO lessons (date, start, end, begin, finish, discipline, teacher, place, subgroup, type, online)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		logx.Errorf("store", "ReplaceLessons: prepare insert: %v", err)
		return fmt.Errorf("store: replace lessons prepare: %w", err)
	}
	defer ins.Close()

	keep := make(map[int64]struct{}, len(lessons))
	updated, inserted := 0, 0
	for i, l := range lessons {
		args := []any{
			l.Date, l.Start, l.End, timeArg(l.Begin), timeArg(l.Finish),
			l.Discipline, l.Teacher, l.Place, l.Subgroup, l.Type, btoi(l.Online),
		}
		if id, ok := oldByIdent[l.Identity()]; ok {
			if _, err := upd.Exec(append(args, id)...); err != nil {
				logx.Errorf("store", "ReplaceLessons: update row %d id=%d: %v", i, id, err)
				return fmt.Errorf("store: replace lessons update row: %w", err)
			}
			keep[id] = struct{}{}
			updated++
			continue
		}
		if _, err := ins.Exec(args...); err != nil {
			logx.Errorf("store", "ReplaceLessons: insert row %d discipline=%s: %v", i, l.Discipline, err)
			return fmt.Errorf("store: replace lessons insert: %w", err)
		}
		inserted++
	}

	pruned := 0
	for _, id := range oldByIdent {
		if _, ok := keep[id]; ok {
			continue
		}
		if _, err := tx.Exec(`DELETE FROM lessons WHERE id = ?`, id); err != nil {
			logx.Errorf("store", "ReplaceLessons: prune id=%d: %v", id, err)
			return fmt.Errorf("store: replace lessons prune: %w", err)
		}
		pruned++
	}

	if err := tx.Commit(); err != nil {
		logx.Errorf("store", "ReplaceLessons: commit: %v", err)
		return fmt.Errorf("store: replace lessons commit: %w", err)
	}
	logx.Infof("store", "ReplaceLessons: committed total=%d updated=%d inserted=%d pruned=%d", len(lessons), updated, inserted, pruned)
	return nil
}

func (s *Store) ListLessons() ([]model.Lesson, error) {
	logx.Debugf("store", "ListLessons: enter")
	rows, err := s.db.Query(`SELECT ` + lessonCols + ` FROM lessons ORDER BY begin, id`)
	if err != nil {
		logx.Errorf("store", "ListLessons: query: %v", err)
		return nil, fmt.Errorf("store: list lessons: %w", err)
	}
	defer rows.Close()
	out := make([]model.Lesson, 0)
	for rows.Next() {
		l, err := scanLesson(rows)
		if err != nil {
			logx.Errorf("store", "ListLessons: scan: %v", err)
			return nil, fmt.Errorf("store: list lessons: %w", err)
		}
		out = append(out, *l)
	}
	if err := rows.Err(); err != nil {
		logx.Errorf("store", "ListLessons: rows: %v", err)
		return nil, fmt.Errorf("store: list lessons: %w", err)
	}
	logx.Debugf("store", "ListLessons: out count=%d", len(out))
	return out, nil
}

func (s *Store) LessonByID(id int64) (*model.Lesson, error) {
	logx.Debugf("store", "LessonByID: enter id=%d", id)
	l, err := scanLesson(s.db.QueryRow(`SELECT `+lessonCols+` FROM lessons WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		logx.Debugf("store", "LessonByID: not found id=%d", id)
		return nil, nil
	}
	if err != nil {
		logx.Errorf("store", "LessonByID: id=%d: %v", id, err)
		return nil, fmt.Errorf("store: lesson by id: %w", err)
	}
	logx.Debugf("store", "LessonByID: out id=%d discipline=%s", l.ID, l.Discipline)
	return l, nil
}

func (s *Store) CurrentLesson(now time.Time) (*model.Lesson, error) {
	logx.Debugf("store", "CurrentLesson: enter now=%s", now.UTC().Format(time.RFC3339))
	t := timeArg(now)
	l, err := scanLesson(s.db.QueryRow(
		`SELECT `+lessonCols+` FROM lessons
		 WHERE online = 1 AND begin <= ? AND finish > ?
		 ORDER BY begin, id LIMIT 1`,
		t, t,
	))
	if errors.Is(err, sql.ErrNoRows) {
		logx.Debugf("store", "CurrentLesson: none now=%s", t)
		return nil, nil
	}
	if err != nil {
		logx.Errorf("store", "CurrentLesson: now=%s: %v", t, err)
		return nil, fmt.Errorf("store: current lesson: %w", err)
	}
	logx.Debugf("store", "CurrentLesson: out id=%d discipline=%s", l.ID, l.Discipline)
	return l, nil
}

func (s *Store) NextLesson(now time.Time) (*model.Lesson, error) {
	logx.Debugf("store", "NextLesson: enter now=%s", now.UTC().Format(time.RFC3339))
	l, err := scanLesson(s.db.QueryRow(
		`SELECT `+lessonCols+` FROM lessons
		 WHERE online = 1 AND begin > ?
		 ORDER BY begin, id LIMIT 1`,
		timeArg(now),
	))
	if errors.Is(err, sql.ErrNoRows) {
		logx.Debugf("store", "NextLesson: none now=%s", timeArg(now))
		return nil, nil
	}
	if err != nil {
		logx.Errorf("store", "NextLesson: now=%s: %v", timeArg(now), err)
		return nil, fmt.Errorf("store: next lesson: %w", err)
	}
	logx.Debugf("store", "NextLesson: out id=%d discipline=%s", l.ID, l.Discipline)
	return l, nil
}

func (s *Store) UpcomingOnline(now, until time.Time) ([]model.Lesson, error) {
	logx.Debugf("store", "UpcomingOnline: enter now=%s until=%s", now.UTC().Format(time.RFC3339), until.UTC().Format(time.RFC3339))
	rows, err := s.db.Query(
		`SELECT `+lessonCols+` FROM lessons
		 WHERE online = 1 AND begin > ? AND begin <= ?
		 ORDER BY begin, id`,
		timeArg(now), timeArg(until),
	)
	if err != nil {
		logx.Errorf("store", "UpcomingOnline: query now=%s until=%s: %v", timeArg(now), timeArg(until), err)
		return nil, fmt.Errorf("store: upcoming online: %w", err)
	}
	defer rows.Close()
	out := make([]model.Lesson, 0)
	for rows.Next() {
		l, err := scanLesson(rows)
		if err != nil {
			logx.Errorf("store", "UpcomingOnline: scan: %v", err)
			return nil, fmt.Errorf("store: upcoming online: %w", err)
		}
		out = append(out, *l)
	}
	if err := rows.Err(); err != nil {
		logx.Errorf("store", "UpcomingOnline: rows: %v", err)
		return nil, fmt.Errorf("store: upcoming online: %w", err)
	}
	logx.Debugf("store", "UpcomingOnline: out count=%d", len(out))
	return out, nil
}

func (s *Store) SaveParseRun(r model.ParseRun) error {
	logx.Debugf("store", "SaveParseRun: enter ok=%v status=%s lessons=%d online=%d", r.OK, r.Status, r.LessonCount, r.OnlineCount)
	at := r.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	_, err := s.db.Exec(
		`INSERT INTO parse_runs (at, ok, status, lesson_count, online_count, diff)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		timeArg(at), btoi(r.OK), r.Status, r.LessonCount, r.OnlineCount, r.Diff,
	)
	if err != nil {
		logx.Errorf("store", "SaveParseRun: insert status=%s: %v", r.Status, err)
		return fmt.Errorf("store: save parse run: %w", err)
	}
	logx.Debugf("store", "SaveParseRun: done status=%s", r.Status)
	return nil
}

func (s *Store) LastParseRun() (*model.ParseRun, error) {
	logx.Debugf("store", "LastParseRun: enter")
	r, err := scanParseRun(s.db.QueryRow(
		`SELECT id, at, ok, status, lesson_count, online_count, diff
		 FROM parse_runs ORDER BY at DESC, id DESC LIMIT 1`,
	))
	if errors.Is(err, sql.ErrNoRows) {
		logx.Debugf("store", "LastParseRun: none")
		return nil, nil
	}
	if err != nil {
		logx.Errorf("store", "LastParseRun: query: %v", err)
		return nil, fmt.Errorf("store: last parse run: %w", err)
	}
	logx.Debugf("store", "LastParseRun: out id=%d ok=%v lessons=%d", r.ID, r.OK, r.LessonCount)
	return r, nil
}
