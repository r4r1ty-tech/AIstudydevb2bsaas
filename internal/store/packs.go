package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/archive"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

const packCols = `id, lesson_id, discipline, number, date, dir, bbb_url, status, audio, transcript, notes_pdf, err, publish_status, published_at, cleaned_at, created_at, updated_at`

func (s *Store) EnsurePack(l model.Lesson, bbbURL, root string) (*model.LecturePack, error) {
	if s == nil {
		return nil, fmt.Errorf("store: nil")
	}
	got, err := s.PackByLesson(l.ID)
	if err != nil {
		return nil, err
	}
	if got != nil {
		if bbbURL != "" && got.BBBURL == "" {
			got.BBBURL = bbbURL
			_ = s.SavePack(got)
		}
		return got, nil
	}
	n, err := s.nextLectureNumber(l.Discipline)
	if err != nil {
		return nil, err
	}
	rel := archive.Rel(l.Discipline, n)
	abs := archive.Abs(root, l.Discipline, n)
	if err := os.MkdirAll(archive.SlidesDir(abs), 0755); err != nil {
		return nil, fmt.Errorf("store: mkdir pack: %w", err)
	}
	now := time.Now().UTC()
	p := &model.LecturePack{
		LessonID:   l.ID,
		Discipline: l.Discipline,
		Number:     n,
		Date:       l.Date,
		Dir:        rel,
		BBBURL:     bbbURL,
		Status:     model.PackRecording,
		Audio:      filepath.ToSlash(filepath.Join(rel, "audio.ogg")),
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	res, err := s.db.Exec(
		`INSERT INTO lecture_packs (lesson_id, discipline, number, date, dir, bbb_url, status, audio, transcript, notes_pdf, err, publish_status, published_at, cleaned_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.LessonID, p.Discipline, p.Number, p.Date, p.Dir, p.BBBURL, p.Status, p.Audio, p.Transcript, p.NotesPDF, p.Err,
		p.PublishStatus, nullTimeArg(p.PublishedAt), nullTimeArg(p.CleanedAt),
		timeArg(p.CreatedAt), timeArg(p.UpdatedAt),
	)
	if err != nil {
		return nil, fmt.Errorf("store: insert pack: %w", err)
	}
	p.ID, _ = res.LastInsertId()
	return p, nil
}

func (s *Store) nextLectureNumber(discipline string) (int, error) {
	var n sql.NullInt64
	err := s.db.QueryRow(`SELECT MAX(number) FROM lecture_packs WHERE discipline = ?`, discipline).Scan(&n)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("store: next lecture number: %w", err)
	}
	if !n.Valid {
		return 1, nil
	}
	return int(n.Int64) + 1, nil
}

func (s *Store) PackByLesson(lessonID int64) (*model.LecturePack, error) {
	p, err := scanPack(s.db.QueryRow(`SELECT `+packCols+` FROM lecture_packs WHERE lesson_id = ?`, lessonID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: pack by lesson: %w", err)
	}
	return p, nil
}

func (s *Store) PackByID(id int64) (*model.LecturePack, error) {
	p, err := scanPack(s.db.QueryRow(`SELECT `+packCols+` FROM lecture_packs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: pack by id: %w", err)
	}
	return p, nil
}

func (s *Store) SavePack(p *model.LecturePack) error {
	if p == nil {
		return fmt.Errorf("store: nil pack")
	}
	p.UpdatedAt = time.Now().UTC()
	_, err := s.db.Exec(
		`UPDATE lecture_packs SET discipline=?, number=?, date=?, dir=?, bbb_url=?, status=?, audio=?, transcript=?, notes_pdf=?, err=?, publish_status=?, published_at=?, cleaned_at=?, updated_at=? WHERE id=?`,
		p.Discipline, p.Number, p.Date, p.Dir, p.BBBURL, p.Status, p.Audio, p.Transcript, p.NotesPDF, p.Err,
		p.PublishStatus, nullTimeArg(p.PublishedAt), nullTimeArg(p.CleanedAt), timeArg(p.UpdatedAt), p.ID,
	)
	if err != nil {
		return fmt.Errorf("store: save pack: %w", err)
	}
	return nil
}

func (s *Store) ListPacks() ([]model.LecturePack, error) {
	rows, err := s.db.Query(`SELECT ` + packCols + ` FROM lecture_packs ORDER BY date DESC, number DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: list packs: %w", err)
	}
	defer rows.Close()
	out := make([]model.LecturePack, 0)
	for rows.Next() {
		p, err := scanPack(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list packs: %w", err)
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *Store) PacksByDate(date string) ([]model.LecturePack, error) {
	rows, err := s.db.Query(`SELECT `+packCols+` FROM lecture_packs WHERE date = ? ORDER BY number, id`, date)
	if err != nil {
		return nil, fmt.Errorf("store: packs by date: %w", err)
	}
	defer rows.Close()
	out := make([]model.LecturePack, 0)
	for rows.Next() {
		p, err := scanPack(rows)
		if err != nil {
			return nil, fmt.Errorf("store: packs by date: %w", err)
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *Store) AnyRecording() (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM lecture_packs WHERE status = ?`, model.PackRecording).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("store: any recording: %w", err)
	}
	return n > 0, nil
}

func scanPack(sc scanner) (*model.LecturePack, error) {
	var p model.LecturePack
	var disc, date, dir, url, status, audio, tr, pdf, errMsg, pubStatus, created, updated sql.NullString
	var pubAt, cleaned sql.NullString
	if err := sc.Scan(
		&p.ID, &p.LessonID, &disc, &p.Number, &date, &dir, &url, &status,
		&audio, &tr, &pdf, &errMsg, &pubStatus, &pubAt, &cleaned, &created, &updated,
	); err != nil {
		return nil, err
	}
	p.Discipline = nullStr(disc)
	p.Date = nullStr(date)
	p.Dir = nullStr(dir)
	p.BBBURL = nullStr(url)
	p.Status = nullStr(status)
	p.Audio = nullStr(audio)
	p.Transcript = nullStr(tr)
	p.NotesPDF = nullStr(pdf)
	p.Err = nullStr(errMsg)
	p.PublishStatus = nullStr(pubStatus)
	if pub, err := parseNullTime(pubAt); err != nil {
		return nil, err
	} else {
		p.PublishedAt = pub
	}
	if cl, err := parseNullTime(cleaned); err != nil {
		return nil, err
	} else {
		p.CleanedAt = cl
	}
	if created.Valid && created.String != "" {
		t, err := parseTime(created.String)
		if err != nil {
			return nil, err
		}
		p.CreatedAt = t
	}
	if updated.Valid && updated.String != "" {
		t, err := parseTime(updated.String)
		if err != nil {
			return nil, err
		}
		p.UpdatedAt = t
	}
	return &p, nil
}
