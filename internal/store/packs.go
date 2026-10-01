package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/archive"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

const packCols = `id, lesson_id, discipline, number, date, dir, bbb_url, status, audio, transcript, notes_pdf, err, publish_status, published_at, cleaned_at, created_at, updated_at`

func (s *Store) EnsurePack(l model.Lesson, bbbURL, root string) (*model.LecturePack, error) {
	logx.Debugf("store", "EnsurePack: enter lesson_id=%d discipline=%s bbb=%v root=%s", l.ID, l.Discipline, bbbURL != "", root)
	if s == nil {
		logx.Errorf("store", "EnsurePack: nil store lesson_id=%d", l.ID)
		return nil, fmt.Errorf("store: nil")
	}
	got, err := s.PackByLesson(l.ID)
	if err != nil {
		logx.Errorf("store", "EnsurePack: pack by lesson lesson_id=%d: %v", l.ID, err)
		return nil, err
	}
	if got != nil {
		if bbbURL != "" && got.BBBURL == "" {
			got.BBBURL = bbbURL
			if err := s.SavePack(got); err != nil {
				logx.Errorf("store", "EnsurePack: save bbb lesson_id=%d pack_id=%d: %v", l.ID, got.ID, err)
				return nil, err
			}
			logx.Debugf("store", "EnsurePack: bbb backfilled pack_id=%d lesson_id=%d", got.ID, l.ID)
		}
		logx.Debugf("store", "EnsurePack: existing pack_id=%d lesson_id=%d number=%d", got.ID, l.ID, got.Number)
		return got, nil
	}
	n, err := s.nextLectureNumber(l.Discipline)
	if err != nil {
		logx.Errorf("store", "EnsurePack: next number discipline=%s: %v", l.Discipline, err)
		return nil, err
	}
	rel := archive.Rel(l.Discipline, n)
	abs := archive.Abs(root, l.Discipline, n)
	if err := os.MkdirAll(archive.SlidesDir(abs), 0755); err != nil {
		logx.Errorf("store", "EnsurePack: mkdir discipline=%s number=%d: %v", l.Discipline, n, err)
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
		logx.Errorf("store", "EnsurePack: insert lesson_id=%d discipline=%s: %v", l.ID, l.Discipline, err)
		return nil, fmt.Errorf("store: insert pack: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		logx.Warnf("store", "EnsurePack: last insert id lesson_id=%d: %v", l.ID, err)
	}
	p.ID = id
	logx.Infof("store", "EnsurePack: created pack_id=%d lesson_id=%d number=%d discipline=%s", p.ID, l.ID, n, l.Discipline)
	return p, nil
}

func (s *Store) nextLectureNumber(discipline string) (int, error) {
	logx.Debugf("store", "nextLectureNumber: enter discipline=%s", discipline)
	var n sql.NullInt64
	err := s.db.QueryRow(`SELECT MAX(number) FROM lecture_packs WHERE discipline = ?`, discipline).Scan(&n)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		logx.Errorf("store", "nextLectureNumber: query discipline=%s: %v", discipline, err)
		return 0, fmt.Errorf("store: next lecture number: %w", err)
	}
	if !n.Valid {
		logx.Debugf("store", "nextLectureNumber: out discipline=%s number=1", discipline)
		return 1, nil
	}
	out := int(n.Int64) + 1
	logx.Debugf("store", "nextLectureNumber: out discipline=%s number=%d", discipline, out)
	return out, nil
}

func (s *Store) PackByLesson(lessonID int64) (*model.LecturePack, error) {
	logx.Debugf("store", "PackByLesson: enter lesson_id=%d", lessonID)
	p, err := scanPack(s.db.QueryRow(`SELECT `+packCols+` FROM lecture_packs WHERE lesson_id = ?`, lessonID))
	if errors.Is(err, sql.ErrNoRows) {
		logx.Debugf("store", "PackByLesson: not found lesson_id=%d", lessonID)
		return nil, nil
	}
	if err != nil {
		logx.Errorf("store", "PackByLesson: lesson_id=%d: %v", lessonID, err)
		return nil, fmt.Errorf("store: pack by lesson: %w", err)
	}
	logx.Debugf("store", "PackByLesson: out pack_id=%d lesson_id=%d", p.ID, lessonID)
	return p, nil
}

func (s *Store) PackByID(id int64) (*model.LecturePack, error) {
	logx.Debugf("store", "PackByID: enter id=%d", id)
	p, err := scanPack(s.db.QueryRow(`SELECT `+packCols+` FROM lecture_packs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		logx.Debugf("store", "PackByID: not found id=%d", id)
		return nil, nil
	}
	if err != nil {
		logx.Errorf("store", "PackByID: id=%d: %v", id, err)
		return nil, fmt.Errorf("store: pack by id: %w", err)
	}
	logx.Debugf("store", "PackByID: out id=%d lesson_id=%d", p.ID, p.LessonID)
	return p, nil
}

func (s *Store) SavePack(p *model.LecturePack) error {
	if p == nil {
		logx.Errorf("store", "SavePack: nil pack")
		return fmt.Errorf("store: nil pack")
	}
	logx.Debugf("store", "SavePack: enter pack_id=%d lesson_id=%d status=%s", p.ID, p.LessonID, p.Status)
	p.UpdatedAt = time.Now().UTC()
	_, err := s.db.Exec(
		`UPDATE lecture_packs SET discipline=?, number=?, date=?, dir=?, bbb_url=?, status=?, audio=?, transcript=?, notes_pdf=?, err=?, publish_status=?, published_at=?, cleaned_at=?, updated_at=? WHERE id=?`,
		p.Discipline, p.Number, p.Date, p.Dir, p.BBBURL, p.Status, p.Audio, p.Transcript, p.NotesPDF, p.Err,
		p.PublishStatus, nullTimeArg(p.PublishedAt), nullTimeArg(p.CleanedAt), timeArg(p.UpdatedAt), p.ID,
	)
	if err != nil {
		logx.Errorf("store", "SavePack: pack_id=%d lesson_id=%d: %v", p.ID, p.LessonID, err)
		return fmt.Errorf("store: save pack: %w", err)
	}
	logx.Debugf("store", "SavePack: done pack_id=%d status=%s", p.ID, p.Status)
	return nil
}

func (s *Store) ListPacks() ([]model.LecturePack, error) {
	logx.Debugf("store", "ListPacks: enter")
	rows, err := s.db.Query(`SELECT ` + packCols + ` FROM lecture_packs ORDER BY date DESC, number DESC, id DESC`)
	if err != nil {
		logx.Errorf("store", "ListPacks: query: %v", err)
		return nil, fmt.Errorf("store: list packs: %w", err)
	}
	defer rows.Close()
	out := make([]model.LecturePack, 0)
	for rows.Next() {
		p, err := scanPack(rows)
		if err != nil {
			logx.Errorf("store", "ListPacks: scan: %v", err)
			return nil, fmt.Errorf("store: list packs: %w", err)
		}
		out = append(out, *p)
	}
	if err := rows.Err(); err != nil {
		logx.Errorf("store", "ListPacks: rows: %v", err)
		return out, err
	}
	logx.Debugf("store", "ListPacks: out count=%d", len(out))
	return out, nil
}

func (s *Store) PacksByDate(date string) ([]model.LecturePack, error) {
	logx.Debugf("store", "PacksByDate: enter date=%s", date)
	rows, err := s.db.Query(`SELECT `+packCols+` FROM lecture_packs WHERE date = ? ORDER BY number, id`, date)
	if err != nil {
		logx.Errorf("store", "PacksByDate: query date=%s: %v", date, err)
		return nil, fmt.Errorf("store: packs by date: %w", err)
	}
	defer rows.Close()
	out := make([]model.LecturePack, 0)
	for rows.Next() {
		p, err := scanPack(rows)
		if err != nil {
			logx.Errorf("store", "PacksByDate: scan date=%s: %v", date, err)
			return nil, fmt.Errorf("store: packs by date: %w", err)
		}
		out = append(out, *p)
	}
	if err := rows.Err(); err != nil {
		logx.Errorf("store", "PacksByDate: rows date=%s: %v", date, err)
		return out, err
	}
	logx.Debugf("store", "PacksByDate: out count=%d date=%s", len(out), date)
	return out, nil
}

func (s *Store) AnyRecording() (bool, error) {
	logx.Debugf("store", "AnyRecording: enter")
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM lecture_packs WHERE status = ?`, model.PackRecording).Scan(&n)
	if err != nil {
		logx.Errorf("store", "AnyRecording: query: %v", err)
		return false, fmt.Errorf("store: any recording: %w", err)
	}
	logx.Debugf("store", "AnyRecording: out count=%d recording=%v", n, n > 0)
	return n > 0, nil
}

func scanPack(sc scanner) (*model.LecturePack, error) {
	logx.Debugf("store", "scanPack: enter")
	var p model.LecturePack
	var disc, date, dir, url, status, audio, tr, pdf, errMsg, pubStatus, created, updated sql.NullString
	var pubAt, cleaned sql.NullString
	if err := sc.Scan(
		&p.ID, &p.LessonID, &disc, &p.Number, &date, &dir, &url, &status,
		&audio, &tr, &pdf, &errMsg, &pubStatus, &pubAt, &cleaned, &created, &updated,
	); err != nil {
		logScanErr("scanPack", err)
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
		logx.Errorf("store", "scanPack: parse published_at id=%d: %v", p.ID, err)
		return nil, err
	} else {
		p.PublishedAt = pub
	}
	if cl, err := parseNullTime(cleaned); err != nil {
		logx.Errorf("store", "scanPack: parse cleaned_at id=%d: %v", p.ID, err)
		return nil, err
	} else {
		p.CleanedAt = cl
	}
	if created.Valid && created.String != "" {
		t, err := parseTime(created.String)
		if err != nil {
			logx.Errorf("store", "scanPack: parse created_at id=%d: %v", p.ID, err)
			return nil, err
		}
		p.CreatedAt = t
	}
	if updated.Valid && updated.String != "" {
		t, err := parseTime(updated.String)
		if err != nil {
			logx.Errorf("store", "scanPack: parse updated_at id=%d: %v", p.ID, err)
			return nil, err
		}
		p.UpdatedAt = t
	}
	logx.Debugf("store", "scanPack: out id=%d lesson_id=%d status=%s", p.ID, p.LessonID, p.Status)
	return &p, nil
}
