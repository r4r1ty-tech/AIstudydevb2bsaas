package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/archive"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

const packCols = `id, lesson_id, discipline, number, date, dir, bbb_url, meeting_key, status, audio, transcript, notes_pdf, err, publish_status, published_at, cleaned_at, claimed_at, created_at, updated_at`

// PackClaimLease определяет длительность захвата сборки конспекта одним воркером.
const PackClaimLease = 45 * time.Minute

// EnsurePack находит существующий пак лекции или атомарно создает новый.
// Для исключения дублей поиск выполняется как по lesson_id, так и по meeting_key BBB-встречи.
func (s *Store) EnsurePack(l model.Lesson, bbbURL, root string) (*model.LecturePack, error) {
	logx.Debugf("store", "EnsurePack: enter lesson_id=%d discipline=%s bbb=%v root=%s", l.ID, l.Discipline, bbbURL != "", root)
	if s == nil || s.db == nil {
		logx.Errorf("store", "EnsurePack: nil store lesson_id=%d", l.ID)
		return nil, fmt.Errorf("store: nil")
	}

	mkey := model.MeetingKey(l, bbbURL)
	logx.Debugf("store", "EnsurePack: lesson_id=%d mkey=%q", l.ID, mkey)

	// 1. Сначала ищем по уроку (через таблицу псевдонимов или lecture_packs)
	got, err := s.PackByLesson(l.ID)
	if err != nil {
		logx.Errorf("store", "EnsurePack: pack by lesson lesson_id=%d: %v", l.ID, err)
		return nil, err
	}

	// 2. Если по уроку нет, но есть ключ встречи — ищем по ключу встречи
	if got == nil && mkey != "" {
		got, err = s.PackByMeetingKey(mkey)
		if err != nil {
			logx.Errorf("store", "EnsurePack: pack by meeting key lesson_id=%d mkey=%s: %v", l.ID, mkey, err)
			return nil, err
		}
	}

	// 3. Если пак найден — связываем урок с паком и при необходимости обновляем поля
	if got != nil {
		// Если урок был привязан к паку без meeting_key, но уже существует канонический пак с этим meeting_key —
		// перепривязываем урок к каноническому паку вместо попытки записать дублирующий meeting_key.
		if got.MeetingKey == "" && mkey != "" {
			canon, err := s.PackByMeetingKey(mkey)
			if err != nil {
				logx.Errorf("store", "EnsurePack: check canonical pack lesson_id=%d mkey=%s: %v", l.ID, mkey, err)
				return nil, err
			}
			if canon != nil && canon.ID != got.ID {
				if l.ID > 0 {
					if err := s.BindLessonToPack(l.ID, canon.ID); err != nil {
						logx.Errorf("store", "EnsurePack: rebind lesson_id=%d to canonical pack_id=%d: %v", l.ID, canon.ID, err)
						return nil, err
					}
				}
				logx.Infof("store", "EnsurePack: rebound lesson_id=%d from empty pack_id=%d to canonical pack_id=%d key=%q", l.ID, got.ID, canon.ID, mkey)
				got = canon
			}
		}
		if l.ID > 0 {
			if _, err := s.db.Exec(`INSERT INTO pack_lessons (lesson_id, pack_id) VALUES (?, ?) ON CONFLICT(lesson_id) DO NOTHING`, l.ID, got.ID); err != nil {
				logx.Warnf("store", "EnsurePack: bind lesson_id=%d pack_id=%d: %v", l.ID, got.ID, err)
			}
		}
		var needsSave bool
		if bbbURL != "" && got.BBBURL == "" {
			got.BBBURL = bbbURL
			needsSave = true
		}
		if mkey != "" && got.MeetingKey == "" {
			got.MeetingKey = mkey
			needsSave = true
		}
		if needsSave {
			if err := s.SavePack(got); err != nil {
				logx.Errorf("store", "EnsurePack: save pack lesson_id=%d pack_id=%d: %v", l.ID, got.ID, err)
				return nil, err
			}
			logx.Debugf("store", "EnsurePack: backfilled pack_id=%d lesson_id=%d", got.ID, l.ID)
		}
		abs := archive.Abs(root, got.Discipline, got.Number)
		if err := os.MkdirAll(archive.SlidesDir(abs), 0755); err != nil {
			logx.Errorf("store", "EnsurePack: mkdir existing discipline=%s number=%d: %v", got.Discipline, got.Number, err)
			return nil, fmt.Errorf("store: mkdir pack: %w", err)
		}
		logx.Debugf("store", "EnsurePack: existing pack_id=%d lesson_id=%d number=%d", got.ID, l.ID, got.Number)
		return got, nil
	}

	// 4. Пака нет — атомарная выдача номера и создание в транзакции
	tx, err := s.db.Begin()
	if err != nil {
		logx.Errorf("store", "EnsurePack: begin tx lesson_id=%d: %v", l.ID, err)
		return nil, fmt.Errorf("store: ensure pack begin: %w", err)
	}
	defer func() {
		if rerr := tx.Rollback(); rerr != nil && rerr != sql.ErrTxDone {
			logx.Warnf("store", "EnsurePack: rollback: %v", rerr)
		}
	}()

	// Проверяем внутри транзакции на случай параллельной гонки
	var existingPackID int64
	if l.ID > 0 {
		var pid int64
		if err := tx.QueryRow(`SELECT pack_id FROM pack_lessons WHERE lesson_id = ?`, l.ID).Scan(&pid); err == nil && pid > 0 {
			existingPackID = pid
		} else if err := tx.QueryRow(`SELECT id FROM lecture_packs WHERE lesson_id = ?`, l.ID).Scan(&pid); err == nil && pid > 0 {
			existingPackID = pid
		}
	}
	if existingPackID == 0 && mkey != "" {
		var pid int64
		if err := tx.QueryRow(`SELECT id FROM lecture_packs WHERE meeting_key = ?`, mkey).Scan(&pid); err == nil && pid > 0 {
			existingPackID = pid
		}
	}

	if existingPackID > 0 {
		found, err := scanPack(tx.QueryRow(`SELECT `+packCols+` FROM lecture_packs WHERE id = ?`, existingPackID))
		if err != nil {
			logx.Errorf("store", "EnsurePack: scan existing pack_id=%d: %v", existingPackID, err)
			return nil, err
		}
		if found.MeetingKey == "" && mkey != "" {
			var canonID int64
			if err := tx.QueryRow(`SELECT id FROM lecture_packs WHERE meeting_key = ?`, mkey).Scan(&canonID); err == nil && canonID > 0 && canonID != found.ID {
				canon, cerr := scanPack(tx.QueryRow(`SELECT `+packCols+` FROM lecture_packs WHERE id = ?`, canonID))
				if cerr != nil {
					logx.Errorf("store", "EnsurePack: scan canonical tx pack_id=%d: %v", canonID, cerr)
					return nil, cerr
				}
				if l.ID > 0 {
					if _, berr := tx.Exec(`INSERT INTO pack_lessons (lesson_id, pack_id) VALUES (?, ?) ON CONFLICT(lesson_id) DO UPDATE SET pack_id = excluded.pack_id`, l.ID, canonID); berr != nil {
						logx.Errorf("store", "EnsurePack: tx rebind lesson_id=%d to canonical pack_id=%d: %v", l.ID, canonID, berr)
						return nil, berr
					}
				}
				found = canon
			}
		}
		if l.ID > 0 {
			if _, err := tx.Exec(`INSERT INTO pack_lessons (lesson_id, pack_id) VALUES (?, ?) ON CONFLICT(lesson_id) DO NOTHING`, l.ID, found.ID); err != nil {
				logx.Warnf("store", "EnsurePack: tx bind existing pack_id=%d lesson_id=%d: %v", found.ID, l.ID, err)
			}
		}
		var needsSave bool
		if bbbURL != "" && found.BBBURL == "" {
			found.BBBURL = bbbURL
			needsSave = true
		}
		if mkey != "" && found.MeetingKey == "" {
			found.MeetingKey = mkey
			needsSave = true
		}
		if needsSave {
			found.UpdatedAt = time.Now().UTC()
			if _, err := tx.Exec(
				`UPDATE lecture_packs SET bbb_url=?, meeting_key=?, updated_at=? WHERE id=?`,
				found.BBBURL, found.MeetingKey, timeArg(found.UpdatedAt), found.ID,
			); err != nil {
				logx.Errorf("store", "EnsurePack: update existing in tx pack_id=%d: %v", found.ID, err)
				return nil, err
			}
		}
		if err := tx.Commit(); err != nil {
			logx.Errorf("store", "EnsurePack: commit existing tx: %v", err)
			return nil, err
		}
		abs := archive.Abs(root, found.Discipline, found.Number)
		if err := os.MkdirAll(archive.SlidesDir(abs), 0755); err != nil {
			logx.Errorf("store", "EnsurePack: mkdir discipline=%s number=%d: %v", found.Discipline, found.Number, err)
			return nil, fmt.Errorf("store: mkdir pack: %w", err)
		}
		return found, nil
	}

	// Вычисляем следующий номер лекции внутри транзакции
	var maxNum sql.NullInt64
	err = tx.QueryRow(`SELECT MAX(number) FROM lecture_packs WHERE discipline = ?`, l.Discipline).Scan(&maxNum)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		logx.Errorf("store", "EnsurePack: next number discipline=%s: %v", l.Discipline, err)
		return nil, fmt.Errorf("store: next lecture number: %w", err)
	}
	n := 1
	if maxNum.Valid {
		n = int(maxNum.Int64) + 1
	}

	rel := archive.Rel(l.Discipline, n)
	now := time.Now().UTC()
	p := &model.LecturePack{
		LessonID:   l.ID,
		Discipline: l.Discipline,
		Number:     n,
		Date:       l.Date,
		Dir:        rel,
		BBBURL:     bbbURL,
		MeetingKey: mkey,
		Status:     model.PackRecording,
		Audio:      filepath.ToSlash(filepath.Join(rel, "audio.ogg")),
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	res, err := tx.Exec(
		`INSERT INTO lecture_packs (lesson_id, discipline, number, date, dir, bbb_url, meeting_key, status, audio, transcript, notes_pdf, err, publish_status, published_at, cleaned_at, claimed_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT DO NOTHING`,
		p.LessonID, p.Discipline, p.Number, p.Date, p.Dir, p.BBBURL, p.MeetingKey, p.Status, p.Audio, p.Transcript, p.NotesPDF, p.Err,
		p.PublishStatus, nullTimeArg(p.PublishedAt), nullTimeArg(p.CleanedAt), nullTimeArg(p.ClaimedAt),
		timeArg(p.CreatedAt), timeArg(p.UpdatedAt),
	)
	if err != nil {
		logx.Errorf("store", "EnsurePack: insert lesson_id=%d discipline=%s: %v", l.ID, l.Discipline, err)
		return nil, fmt.Errorf("store: insert pack: %w", err)
	}

	aff, _ := res.RowsAffected()
	if aff == 0 {
		// Конфликт по lesson_id (включая lesson_id = 0) или meeting_key — считываем существующий пак
		var conflictPack *model.LecturePack
		if mkey != "" {
			conflictPack, _ = scanPack(tx.QueryRow(`SELECT `+packCols+` FROM lecture_packs WHERE meeting_key = ?`, mkey))
		}
		if conflictPack == nil {
			conflictPack, _ = scanPack(tx.QueryRow(`SELECT `+packCols+` FROM lecture_packs WHERE lesson_id = ?`, l.ID))
		}
		if conflictPack == nil {
			return nil, fmt.Errorf("store: pack insert conflict (lesson_id=%d, mkey=%q) but existing pack not found", l.ID, mkey)
		}
		p = conflictPack
	} else {
		id, err := res.LastInsertId()
		if err != nil {
			logx.Warnf("store", "EnsurePack: last insert id lesson_id=%d: %v", l.ID, err)
		}
		p.ID = id
	}

	if l.ID > 0 && p.ID > 0 {
		if _, err := tx.Exec(`INSERT INTO pack_lessons (lesson_id, pack_id) VALUES (?, ?) ON CONFLICT(lesson_id) DO NOTHING`, l.ID, p.ID); err != nil {
			logx.Warnf("store", "EnsurePack: tx bind pack_id=%d lesson_id=%d: %v", p.ID, l.ID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		logx.Errorf("store", "EnsurePack: commit tx: %v", err)
		return nil, fmt.Errorf("store: commit pack: %w", err)
	}

	abs := archive.Abs(root, p.Discipline, p.Number)
	if err := os.MkdirAll(archive.SlidesDir(abs), 0755); err != nil {
		logx.Errorf("store", "EnsurePack: mkdir discipline=%s number=%d: %v", p.Discipline, p.Number, err)
		return nil, fmt.Errorf("store: mkdir pack: %w", err)
	}

	logx.Infof("store", "EnsurePack: ready pack_id=%d lesson_id=%d number=%d discipline=%s meeting_key=%q", p.ID, l.ID, p.Number, p.Discipline, p.MeetingKey)
	return p, nil
}

// PackByLesson ищет пак лекции по lesson_id.
// Сначала проверяется таблица псевдонимов pack_lessons, затем основная таблица lecture_packs.
func (s *Store) PackByLesson(lessonID int64) (*model.LecturePack, error) {
	logx.Debugf("store", "PackByLesson: enter lesson_id=%d", lessonID)
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("store: nil")
	}
	if lessonID <= 0 {
		return nil, nil
	}

	var packID int64
	err := s.db.QueryRow(`SELECT pack_id FROM pack_lessons WHERE lesson_id = ?`, lessonID).Scan(&packID)
	if err == nil {
		p, err := s.PackByID(packID)
		if err != nil {
			logx.Errorf("store", "PackByLesson: pack by id pack_id=%d lesson_id=%d: %v", packID, lessonID, err)
			return nil, err
		}
		if p != nil {
			logx.Debugf("store", "PackByLesson: found via pack_lessons pack_id=%d lesson_id=%d", p.ID, lessonID)
			return p, nil
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		logx.Errorf("store", "PackByLesson: query pack_lessons lesson_id=%d: %v", lessonID, err)
		return nil, fmt.Errorf("store: pack by lesson alias: %w", err)
	}

	p, err := scanPack(s.db.QueryRow(`SELECT `+packCols+` FROM lecture_packs WHERE lesson_id = ?`, lessonID))
	if errors.Is(err, sql.ErrNoRows) {
		logx.Debugf("store", "PackByLesson: not found lesson_id=%d", lessonID)
		return nil, nil
	}
	if err != nil {
		logx.Errorf("store", "PackByLesson: lesson_id=%d: %v", lessonID, err)
		return nil, fmt.Errorf("store: pack by lesson: %w", err)
	}

	// Подстраховочно записываем связь в pack_lessons
	if _, berr := s.db.Exec(`INSERT INTO pack_lessons (lesson_id, pack_id) VALUES (?, ?) ON CONFLICT(lesson_id) DO NOTHING`, lessonID, p.ID); berr != nil {
		logx.Warnf("store", "PackByLesson: backfill pack_lessons lesson_id=%d pack_id=%d: %v", lessonID, p.ID, berr)
	}
	logx.Debugf("store", "PackByLesson: out pack_id=%d lesson_id=%d", p.ID, lessonID)
	return p, nil
}

// PackByID ищет пак лекции по его уникальному идентификатору id.
func (s *Store) PackByID(id int64) (*model.LecturePack, error) {
	logx.Debugf("store", "PackByID: enter id=%d", id)
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("store: nil")
	}
	if id <= 0 {
		return nil, nil
	}
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

// PackByMeetingKey ищет пак лекции по уникальному ключу встречи BBB.
func (s *Store) PackByMeetingKey(meetingKey string) (*model.LecturePack, error) {
	logx.Debugf("store", "PackByMeetingKey: enter key=%q", meetingKey)
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("store: nil")
	}
	key := strings.TrimSpace(meetingKey)
	if key == "" {
		logx.Debugf("store", "PackByMeetingKey: empty key")
		return nil, nil
	}
	p, err := scanPack(s.db.QueryRow(`SELECT `+packCols+` FROM lecture_packs WHERE meeting_key = ?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		logx.Debugf("store", "PackByMeetingKey: not found key=%q", key)
		return nil, nil
	}
	if err != nil {
		logx.Errorf("store", "PackByMeetingKey: key=%q: %v", key, err)
		return nil, fmt.Errorf("store: pack by meeting key: %w", err)
	}
	logx.Debugf("store", "PackByMeetingKey: out pack_id=%d key=%q", p.ID, key)
	return p, nil
}

// BindLessonToPack связывает lesson_id с pack_id в таблице псевдонимов pack_lessons.
func (s *Store) BindLessonToPack(lessonID, packID int64) error {
	logx.Debugf("store", "BindLessonToPack: enter lesson_id=%d pack_id=%d", lessonID, packID)
	if s == nil || s.db == nil {
		return fmt.Errorf("store: nil")
	}
	if lessonID <= 0 || packID <= 0 {
		return fmt.Errorf("store: invalid lesson_id=%d or pack_id=%d", lessonID, packID)
	}
	_, err := s.db.Exec(
		`INSERT INTO pack_lessons (lesson_id, pack_id) VALUES (?, ?)
		 ON CONFLICT(lesson_id) DO UPDATE SET pack_id = excluded.pack_id`,
		lessonID, packID,
	)
	if err != nil {
		logx.Errorf("store", "BindLessonToPack: lesson_id=%d pack_id=%d: %v", lessonID, packID, err)
		return fmt.Errorf("store: bind lesson to pack: %w", err)
	}
	logx.Debugf("store", "BindLessonToPack: done lesson_id=%d pack_id=%d", lessonID, packID)
	return nil
}

// PackLessonIDs возвращает все lesson_id, привязанные к данному паку (из pack_lessons и lecture_packs).
func (s *Store) PackLessonIDs(packID int64) ([]int64, error) {
	logx.Debugf("store", "PackLessonIDs: enter pack_id=%d", packID)
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("store: nil")
	}
	if packID <= 0 {
		return []int64{}, nil
	}
	rows, err := s.db.Query(`
		SELECT lesson_id FROM pack_lessons WHERE pack_id = ?
		UNION
		SELECT lesson_id FROM lecture_packs WHERE id = ? AND lesson_id > 0
		ORDER BY 1
	`, packID, packID)
	if err != nil {
		logx.Errorf("store", "PackLessonIDs: query pack_id=%d: %v", packID, err)
		return nil, fmt.Errorf("store: pack lesson ids: %w", err)
	}
	defer rows.Close()

	out := make([]int64, 0)
	for rows.Next() {
		var lid int64
		if err := rows.Scan(&lid); err != nil {
			logx.Errorf("store", "PackLessonIDs: scan pack_id=%d: %v", packID, err)
			return nil, fmt.Errorf("store: pack lesson ids scan: %w", err)
		}
		if lid > 0 {
			out = append(out, lid)
		}
	}
	if err := rows.Err(); err != nil {
		logx.Errorf("store", "PackLessonIDs: rows pack_id=%d: %v", packID, err)
		return nil, err
	}
	logx.Debugf("store", "PackLessonIDs: out pack_id=%d count=%d", packID, len(out))
	return out, nil
}

// ClaimPackForNotes выполняет атомарный захват сборки конспекта с lease 45 минут.
// Возвращает true, если пак был успешно захвачен, false если пак уже захвачен другим процессом
// или находится в неподходящем статусе.
func (s *Store) ClaimPackForNotes(packID int64, now time.Time) (bool, error) {
	logx.Debugf("store", "ClaimPackForNotes: enter pack_id=%d now=%s", packID, now.Format(time.RFC3339))
	if s == nil || s.db == nil {
		return false, fmt.Errorf("store: nil")
	}
	if packID <= 0 {
		return false, nil
	}

	nowUTC := now.UTC()
	threshold := nowUTC.Add(-PackClaimLease)

	res, err := s.db.Exec(
		`UPDATE lecture_packs
		 SET claimed_at = ?, updated_at = ?
		 WHERE id = ?
		   AND status IN ('slides', 'recorded', 'notes', 'transcribe', 'error')
		   AND (claimed_at IS NULL OR claimed_at = '' OR claimed_at < ?)`,
		timeArg(nowUTC), timeArg(nowUTC), packID, timeArg(threshold),
	)
	if err != nil {
		logx.Errorf("store", "ClaimPackForNotes: pack_id=%d: %v", packID, err)
		return false, fmt.Errorf("store: claim pack for notes: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		logx.Errorf("store", "ClaimPackForNotes: rows affected pack_id=%d: %v", packID, err)
		return false, err
	}

	claimed := n == 1
	logx.Debugf("store", "ClaimPackForNotes: pack_id=%d claimed=%v", packID, claimed)
	return claimed, nil
}

// ReleasePackClaim сбрасывает захват сборки конспекта для пака.
func (s *Store) ReleasePackClaim(packID int64) error {
	logx.Debugf("store", "ReleasePackClaim: enter pack_id=%d", packID)
	if s == nil || s.db == nil {
		return fmt.Errorf("store: nil")
	}
	if packID <= 0 {
		return nil
	}
	now := time.Now().UTC()
	_, err := s.db.Exec(`UPDATE lecture_packs SET claimed_at = '', updated_at = ? WHERE id = ?`, timeArg(now), packID)
	if err != nil {
		logx.Errorf("store", "ReleasePackClaim: pack_id=%d: %v", packID, err)
		return fmt.Errorf("store: release pack claim: %w", err)
	}
	logx.Debugf("store", "ReleasePackClaim: done pack_id=%d", packID)
	return nil
}

// SavePack сохраняет изменения в паке лекции.
// При переходе в финальные статусы (done, empty, error) захват claimed_at сбрасывается.
func (s *Store) SavePack(p *model.LecturePack) error {
	if p == nil {
		logx.Errorf("store", "SavePack: nil pack")
		return fmt.Errorf("store: nil pack")
	}
	logx.Debugf("store", "SavePack: enter pack_id=%d lesson_id=%d status=%s", p.ID, p.LessonID, p.Status)
	p.UpdatedAt = time.Now().UTC()
	if p.Status == model.PackDone || p.Status == model.PackEmpty || p.Status == model.PackError {
		p.ClaimedAt = nil
	}
	_, err := s.db.Exec(
		`UPDATE lecture_packs SET discipline=?, number=?, date=?, dir=?, bbb_url=?, meeting_key=?, status=?, audio=?, transcript=?, notes_pdf=?, err=?, publish_status=?, published_at=?, cleaned_at=?, claimed_at=?, updated_at=? WHERE id=?`,
		p.Discipline, p.Number, p.Date, p.Dir, p.BBBURL, p.MeetingKey, p.Status, p.Audio, p.Transcript, p.NotesPDF, p.Err,
		p.PublishStatus, nullTimeArg(p.PublishedAt), nullTimeArg(p.CleanedAt), nullTimeArg(p.ClaimedAt), timeArg(p.UpdatedAt), p.ID,
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
	var disc, date, dir, url, mkey, status, audio, tr, pdf, errMsg, pubStatus, created, updated sql.NullString
	var pubAt, cleaned, claimed sql.NullString
	if err := sc.Scan(
		&p.ID, &p.LessonID, &disc, &p.Number, &date, &dir, &url, &mkey, &status,
		&audio, &tr, &pdf, &errMsg, &pubStatus, &pubAt, &cleaned, &claimed, &created, &updated,
	); err != nil {
		logScanErr("scanPack", err)
		return nil, err
	}
	p.Discipline = nullStr(disc)
	p.Date = nullStr(date)
	p.Dir = nullStr(dir)
	p.BBBURL = nullStr(url)
	p.MeetingKey = nullStr(mkey)
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
	if clm, err := parseNullTime(claimed); err != nil {
		logx.Errorf("store", "scanPack: parse claimed_at id=%d: %v", p.ID, err)
		return nil, err
	} else {
		p.ClaimedAt = clm
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
	logx.Debugf("store", "scanPack: out id=%d lesson_id=%d status=%s meeting_key=%q", p.ID, p.LessonID, p.Status, p.MeetingKey)
	return &p, nil
}

func packStatusRank(status string) int {
	switch status {
	case model.PackDone:
		return 8
	case model.PackNotes:
		return 7
	case model.PackTranscribe:
		return 6
	case model.PackSlides:
		return 5
	case model.PackRecorded:
		return 4
	case model.PackRecording:
		return 3
	case model.PackError:
		return 2
	case model.PackEmpty:
		return 1
	default:
		return 0
	}
}

type packMigrateRow struct {
	id         int64
	lessonID   int64
	packDate   string
	bbbURL     string
	status     string
	meetingKey string
	lessDate   string
	lessStart  string
	lessBBB    string
}

// migrateDeduplicatePacks выполняет склейку дублей паков лекций для одинаковых встреч BBB.
func (s *Store) migrateDeduplicatePacks() error {
	logx.Debugf("store", "migrateDeduplicatePacks: enter")
	if s == nil || s.db == nil {
		return fmt.Errorf("store: nil")
	}

	tx, err := s.db.Begin()
	if err != nil {
		logx.Errorf("store", "migrateDeduplicatePacks: begin tx: %v", err)
		return fmt.Errorf("store: migrate deduplicate packs begin: %w", err)
	}
	defer func() {
		if rerr := tx.Rollback(); rerr != nil && rerr != sql.ErrTxDone {
			logx.Warnf("store", "migrateDeduplicatePacks: rollback: %v", rerr)
		}
	}()

	rows, err := tx.Query(`
		SELECT lp.id, IFNULL(lp.lesson_id, 0), IFNULL(lp.date, ''), IFNULL(lp.bbb_url, ''), IFNULL(lp.status, ''), IFNULL(lp.meeting_key, ''),
		       IFNULL(l.date, ''), IFNULL(l.start, ''), IFNULL(l.bbb_url, '')
		FROM lecture_packs lp
		LEFT JOIN lessons l ON lp.lesson_id = l.id
		ORDER BY lp.id ASC
	`)
	if err != nil {
		logx.Errorf("store", "migrateDeduplicatePacks: query: %v", err)
		return fmt.Errorf("store: migrate deduplicate packs query: %w", err)
	}

	var items []packMigrateRow
	for rows.Next() {
		var it packMigrateRow
		if err := rows.Scan(&it.id, &it.lessonID, &it.packDate, &it.bbbURL, &it.status, &it.meetingKey, &it.lessDate, &it.lessStart, &it.lessBBB); err != nil {
			rows.Close()
			logx.Errorf("store", "migrateDeduplicatePacks: scan: %v", err)
			return fmt.Errorf("store: migrate deduplicate packs scan: %w", err)
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		logx.Errorf("store", "migrateDeduplicatePacks: rows: %v", err)
		return err
	}
	rows.Close()

	// Группируем по meeting_key
	groups := make(map[string][]*packMigrateRow)
	for i := range items {
		it := &items[i]
		mkey := strings.TrimSpace(it.meetingKey)
		if mkey == "" {
			// Если урок уже удален из расписания (lessDate == "" или lessStart == ""),
			// MeetingKey вернет "", чтобы исторические паки разных недель не склеивались.
			l := model.Lesson{
				ID:     it.lessonID,
				Date:   it.lessDate,
				Start:  it.lessStart,
				BBBURL: it.lessBBB,
			}
			mkey = model.MeetingKey(l, it.bbbURL)
			it.meetingKey = mkey
		}
		if mkey != "" {
			groups[mkey] = append(groups[mkey], it)
		} else {
			if it.lessonID > 0 {
				if _, err := tx.Exec(`INSERT INTO pack_lessons (lesson_id, pack_id) VALUES (?, ?) ON CONFLICT(lesson_id) DO NOTHING`, it.lessonID, it.id); err != nil {
					logx.Errorf("store", "migrateDeduplicatePacks: bind no-key pack_id=%d lesson_id=%d: %v", it.id, it.lessonID, err)
					return err
				}
			}
		}
	}

	for mkey, group := range groups {
		if len(group) == 1 {
			it := group[0]
			if _, err := tx.Exec(`UPDATE lecture_packs SET meeting_key = ? WHERE id = ?`, mkey, it.id); err != nil {
				logx.Errorf("store", "migrateDeduplicatePacks: update single pack_id=%d: %v", it.id, err)
				return err
			}
			if it.lessonID > 0 {
				if _, err := tx.Exec(`INSERT INTO pack_lessons (lesson_id, pack_id) VALUES (?, ?) ON CONFLICT(lesson_id) DO NOTHING`, it.lessonID, it.id); err != nil {
					logx.Errorf("store", "migrateDeduplicatePacks: bind single pack_id=%d lesson_id=%d: %v", it.id, it.lessonID, err)
					return err
				}
			}
			continue
		}

		// Выбираем канонический пак:
		// done > notes > transcribe > slides > recorded > recording > error > empty, а при равенстве — с меньшим id.
		best := group[0]
		for _, cur := range group[1:] {
			rBest := packStatusRank(best.status)
			rCur := packStatusRank(cur.status)
			if rCur > rBest {
				best = cur
			} else if rCur == rBest && cur.id < best.id {
				best = cur
			}
		}

		// Сначала сбрасываем meeting_key у всех дублей и переводим их уроки на канонический пак,
		// чтобы при повторной миграции (с уже созданным частичным уникальным индексом) смена канонического пака не вызывала UNIQUE constraint failed.
		for _, dup := range group {
			if dup.id == best.id {
				continue
			}
			if _, err := tx.Exec(`UPDATE lecture_packs SET meeting_key = '' WHERE id = ?`, dup.id); err != nil {
				logx.Errorf("store", "migrateDeduplicatePacks: clear dup pack_id=%d: %v", dup.id, err)
				return err
			}
			if _, err := tx.Exec(`UPDATE pack_lessons SET pack_id = ? WHERE pack_id = ?`, best.id, dup.id); err != nil {
				logx.Errorf("store", "migrateDeduplicatePacks: move pack_lessons dup_id=%d canonical_id=%d: %v", dup.id, best.id, err)
				return err
			}
			if dup.lessonID > 0 {
				if _, err := tx.Exec(`INSERT INTO pack_lessons (lesson_id, pack_id) VALUES (?, ?) ON CONFLICT(lesson_id) DO UPDATE SET pack_id = excluded.pack_id`, dup.lessonID, best.id); err != nil {
					logx.Errorf("store", "migrateDeduplicatePacks: bind dup lesson_id=%d canonical_id=%d: %v", dup.lessonID, best.id, err)
					return err
				}
			}
			logx.Infof("store", "migrateDeduplicatePacks: deduplicated pack_id=%d -> canonical pack_id=%d key=%q", dup.id, best.id, mkey)
		}

		// Затем обновляем meeting_key у канонического пака и привязываем его lesson_id
		if _, err := tx.Exec(`UPDATE lecture_packs SET meeting_key = ? WHERE id = ?`, mkey, best.id); err != nil {
			logx.Errorf("store", "migrateDeduplicatePacks: update canonical pack_id=%d: %v", best.id, err)
			return err
		}
		if best.lessonID > 0 {
			if _, err := tx.Exec(`INSERT INTO pack_lessons (lesson_id, pack_id) VALUES (?, ?) ON CONFLICT(lesson_id) DO UPDATE SET pack_id = excluded.pack_id`, best.lessonID, best.id); err != nil {
				logx.Errorf("store", "migrateDeduplicatePacks: bind canonical pack_id=%d lesson_id=%d: %v", best.id, best.lessonID, err)
				return err
			}
		}
	}

	if err := tx.Commit(); err != nil {
		logx.Errorf("store", "migrateDeduplicatePacks: commit tx: %v", err)
		return fmt.Errorf("store: migrate deduplicate packs commit: %w", err)
	}

	logx.Infof("store", "migrateDeduplicatePacks: completed groups=%d total_packs=%d", len(groups), len(items))
	return nil
}
