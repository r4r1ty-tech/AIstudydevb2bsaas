package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

const lessonCols = `id, date, start, end, begin, finish, discipline, teacher, place, subgroup, type, online`

func (s *Store) ReplaceLessons(lessons []model.Lesson) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: replace lessons begin: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.Query(`SELECT ` + lessonCols + ` FROM lessons`)
	if err != nil {
		return fmt.Errorf("store: replace lessons list: %w", err)
	}
	oldByIdent := make(map[string]int64)
	for rows.Next() {
		l, err := scanLesson(rows)
		if err != nil {
			rows.Close()
			return fmt.Errorf("store: replace lessons scan: %w", err)
		}
		oldByIdent[l.Identity()] = l.ID
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("store: replace lessons rows: %w", err)
	}
	rows.Close()

	upd, err := tx.Prepare(`UPDATE lessons SET date=?, start=?, end=?, begin=?, finish=?, discipline=?, teacher=?, place=?, subgroup=?, type=?, online=? WHERE id=?`)
	if err != nil {
		return fmt.Errorf("store: replace lessons update: %w", err)
	}
	defer upd.Close()
	ins, err := tx.Prepare(`INSERT INTO lessons (date, start, end, begin, finish, discipline, teacher, place, subgroup, type, online)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("store: replace lessons prepare: %w", err)
	}
	defer ins.Close()

	keep := make(map[int64]struct{}, len(lessons))
	for _, l := range lessons {
		args := []any{
			l.Date, l.Start, l.End, timeArg(l.Begin), timeArg(l.Finish),
			l.Discipline, l.Teacher, l.Place, l.Subgroup, l.Type, btoi(l.Online),
		}
		if id, ok := oldByIdent[l.Identity()]; ok {
			if _, err := upd.Exec(append(args, id)...); err != nil {
				return fmt.Errorf("store: replace lessons update row: %w", err)
			}
			keep[id] = struct{}{}
			continue
		}
		if _, err := ins.Exec(args...); err != nil {
			return fmt.Errorf("store: replace lessons insert: %w", err)
		}
	}

	for _, id := range oldByIdent {
		if _, ok := keep[id]; ok {
			continue
		}
		if _, err := tx.Exec(`DELETE FROM lessons WHERE id = ?`, id); err != nil {
			return fmt.Errorf("store: replace lessons prune: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: replace lessons commit: %w", err)
	}
	return nil
}

func (s *Store) ListLessons() ([]model.Lesson, error) {
	rows, err := s.db.Query(`SELECT ` + lessonCols + ` FROM lessons ORDER BY begin, id`)
	if err != nil {
		return nil, fmt.Errorf("store: list lessons: %w", err)
	}
	defer rows.Close()
	out := make([]model.Lesson, 0)
	for rows.Next() {
		l, err := scanLesson(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list lessons: %w", err)
		}
		out = append(out, *l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list lessons: %w", err)
	}
	return out, nil
}

func (s *Store) LessonByID(id int64) (*model.Lesson, error) {
	l, err := scanLesson(s.db.QueryRow(`SELECT `+lessonCols+` FROM lessons WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: lesson by id: %w", err)
	}
	return l, nil
}

func (s *Store) CurrentLesson(now time.Time) (*model.Lesson, error) {
	t := timeArg(now)
	l, err := scanLesson(s.db.QueryRow(
		`SELECT `+lessonCols+` FROM lessons
		 WHERE online = 1 AND begin <= ? AND finish > ?
		 ORDER BY begin, id LIMIT 1`,
		t, t,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: current lesson: %w", err)
	}
	return l, nil
}

func (s *Store) NextLesson(now time.Time) (*model.Lesson, error) {
	l, err := scanLesson(s.db.QueryRow(
		`SELECT `+lessonCols+` FROM lessons
		 WHERE online = 1 AND begin > ?
		 ORDER BY begin, id LIMIT 1`,
		timeArg(now),
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: next lesson: %w", err)
	}
	return l, nil
}

func (s *Store) UpcomingOnline(now, until time.Time) ([]model.Lesson, error) {
	rows, err := s.db.Query(
		`SELECT `+lessonCols+` FROM lessons
		 WHERE online = 1 AND begin > ? AND begin <= ?
		 ORDER BY begin, id`,
		timeArg(now), timeArg(until),
	)
	if err != nil {
		return nil, fmt.Errorf("store: upcoming online: %w", err)
	}
	defer rows.Close()
	out := make([]model.Lesson, 0)
	for rows.Next() {
		l, err := scanLesson(rows)
		if err != nil {
			return nil, fmt.Errorf("store: upcoming online: %w", err)
		}
		out = append(out, *l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: upcoming online: %w", err)
	}
	return out, nil
}

func (s *Store) SaveParseRun(r model.ParseRun) error {
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
		return fmt.Errorf("store: save parse run: %w", err)
	}
	return nil
}

func (s *Store) LastParseRun() (*model.ParseRun, error) {
	r, err := scanParseRun(s.db.QueryRow(
		`SELECT id, at, ok, status, lesson_count, online_count, diff
		 FROM parse_runs ORDER BY at DESC, id DESC LIMIT 1`,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: last parse run: %w", err)
	}
	return r, nil
}
