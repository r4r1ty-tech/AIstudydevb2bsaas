package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

// VerifiedAnswer — ответ из базы проверенных ответов решателя тестов.
// Ответ хранится в AnswerJSON по текстам вариантов (не по индексам: варианты перемешиваются).
// Поиск идёт по QHash глобально; Course и CMID — только для справки.
type VerifiedAnswer struct {
	ID           int64
	Course       string
	CMID         int64
	QHash        string
	QType        string
	QuestionText string
	AnswerJSON   string
	VerifiedAt   time.Time
	SourceTG     int64
	Hits         int64
	CreatedAt    time.Time
}

const quizAnswerCols = `id, course, cmid, qhash, qtype, question_text, answer_json, verified_at, source_tg, hits, created_at`

// SaveVerifiedAnswer записывает ответ, который Moodle подтвердил как правильный.
// Если ответ на тот же qhash уже есть, он заменяется последним подтверждённым;
// счётчик попаданий и дата создания сохраняются.
func (s *Store) SaveVerifiedAnswer(a VerifiedAnswer) error {
	if a.QHash == "" || a.AnswerJSON == "" {
		return errors.New("store: save verified answer: пустой qhash или ответ")
	}
	now := time.Now()
	if a.VerifiedAt.IsZero() {
		a.VerifiedAt = now
	}
	_, err := s.db.Exec(
		`INSERT INTO quiz_answers (course, cmid, qhash, qtype, question_text, answer_json, verified_at, source_tg, hits, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?)
		 ON CONFLICT(qhash) DO UPDATE SET
			course = excluded.course,
			cmid = excluded.cmid,
			qtype = excluded.qtype,
			question_text = excluded.question_text,
			answer_json = excluded.answer_json,
			verified_at = excluded.verified_at,
			source_tg = excluded.source_tg`,
		a.Course, a.CMID, a.QHash, a.QType, a.QuestionText, a.AnswerJSON,
		formatTime(a.VerifiedAt), a.SourceTG, formatTime(now),
	)
	if err != nil {
		logx.Errorf("store", "SaveVerifiedAnswer: qhash=%s: %v", a.QHash, err)
		return fmt.Errorf("store: save verified answer: %w", err)
	}
	logx.Debugf("store", "SaveVerifiedAnswer: qhash=%s cmid=%d tg=%d", a.QHash, a.CMID, a.SourceTG)
	return nil
}

// LookupAnswer ищет ответ по qhash. Найденному ответу увеличивает счётчик попаданий.
// Возвращает ok=false, если ответа нет.
func (s *Store) LookupAnswer(qhash string) (VerifiedAnswer, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		logx.Errorf("store", "LookupAnswer: begin qhash=%s: %v", qhash, err)
		return VerifiedAnswer{}, false, fmt.Errorf("store: lookup answer begin: %w", err)
	}
	defer func() {
		if rerr := tx.Rollback(); rerr != nil && rerr != sql.ErrTxDone {
			logx.Warnf("store", "LookupAnswer: rollback: %v", rerr)
		}
	}()

	res, err := tx.Exec(`UPDATE quiz_answers SET hits = hits + 1 WHERE qhash = ?`, qhash)
	if err != nil {
		logx.Errorf("store", "LookupAnswer: hits qhash=%s: %v", qhash, err)
		return VerifiedAnswer{}, false, fmt.Errorf("store: lookup answer hits: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		logx.Debugf("store", "LookupAnswer: нет ответа qhash=%s", qhash)
		return VerifiedAnswer{}, false, nil
	}
	a, err := scanQuizAnswer(tx.QueryRow(`SELECT `+quizAnswerCols+` FROM quiz_answers WHERE qhash = ?`, qhash))
	if err != nil {
		logx.Errorf("store", "LookupAnswer: читаю qhash=%s: %v", qhash, err)
		return VerifiedAnswer{}, false, err
	}
	if err := tx.Commit(); err != nil {
		logx.Errorf("store", "LookupAnswer: commit qhash=%s: %v", qhash, err)
		return VerifiedAnswer{}, false, fmt.Errorf("store: lookup answer commit: %w", err)
	}
	logx.Debugf("store", "LookupAnswer: найден qhash=%s hits=%d", qhash, a.Hits)
	return a, true, nil
}

func scanQuizAnswer(sc scanner) (VerifiedAnswer, error) {
	var a VerifiedAnswer
	var verified, created string
	if err := sc.Scan(&a.ID, &a.Course, &a.CMID, &a.QHash, &a.QType, &a.QuestionText, &a.AnswerJSON,
		&verified, &a.SourceTG, &a.Hits, &created); err != nil {
		logScanErr("scanQuizAnswer", err)
		return VerifiedAnswer{}, err
	}
	var err error
	if a.VerifiedAt, err = parseTime(verified); err != nil {
		return VerifiedAnswer{}, fmt.Errorf("store: parse quiz_answers.verified_at: %w", err)
	}
	if a.CreatedAt, err = parseTime(created); err != nil {
		return VerifiedAnswer{}, fmt.Errorf("store: parse quiz_answers.created_at: %w", err)
	}
	return a, nil
}

// PeekAnswer ищет ответ по qhash без учёта попаданий: для проверки «есть ли решение из базы»
// до того, как страница решается.
func (s *Store) PeekAnswer(qhash string) (VerifiedAnswer, bool, error) {
	a, err := scanQuizAnswer(s.db.QueryRow(`SELECT `+quizAnswerCols+` FROM quiz_answers WHERE qhash = ?`, qhash))
	if errors.Is(err, sql.ErrNoRows) {
		return VerifiedAnswer{}, false, nil
	}
	if err != nil {
		logx.Errorf("store", "PeekAnswer: qhash=%s: %v", qhash, err)
		return VerifiedAnswer{}, false, fmt.Errorf("store: peek answer: %w", err)
	}
	return a, true, nil
}

// SaveWrongAnswer запоминает ответ, который Moodle отметил как неверный на вопрос с таким qhash.
// Повтор того же ответа не дублируется.
func (s *Store) SaveWrongAnswer(qhash, answerJSON string, cmid, sourceTG int64) error {
	if qhash == "" || answerJSON == "" {
		return errors.New("store: save wrong answer: пустой qhash или ответ")
	}
	_, err := s.db.Exec(
		`INSERT INTO quiz_wrong_answers (qhash, answer_json, cmid, source_tg, created_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(qhash, answer_json) DO NOTHING`,
		qhash, answerJSON, cmid, sourceTG, formatTime(time.Now()),
	)
	if err != nil {
		logx.Errorf("store", "SaveWrongAnswer: qhash=%s: %v", qhash, err)
		return fmt.Errorf("store: save wrong answer: %w", err)
	}
	return nil
}

// ListWrongAnswers — все известные неверные ответы на вопрос (answer_json), старые первыми.
func (s *Store) ListWrongAnswers(qhash string) ([]string, error) {
	rows, err := s.db.Query(`SELECT answer_json FROM quiz_wrong_answers WHERE qhash = ? ORDER BY id`, qhash)
	if err != nil {
		logx.Errorf("store", "ListWrongAnswers: qhash=%s: %v", qhash, err)
		return nil, fmt.Errorf("store: list wrong answers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			logScanErr("ListWrongAnswers", err)
			return nil, fmt.Errorf("store: scan wrong answer: %w", err)
		}
		out = append(out, raw)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list wrong answers rows: %w", err)
	}
	return out, nil
}

// DeleteVerifiedAnswer удаляет верный ответ на вопрос (после того, как Moodle отметил
// такой же ответ неверным). true — запись была и удалена.
func (s *Store) DeleteVerifiedAnswer(qhash string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM quiz_answers WHERE qhash = ?`, qhash)
	if err != nil {
		logx.Errorf("store", "DeleteVerifiedAnswer: qhash=%s: %v", qhash, err)
		return false, fmt.Errorf("store: delete verified answer: %w", err)
	}
	n, _ := res.RowsAffected()
	logx.Debugf("store", "DeleteVerifiedAnswer: qhash=%s удалено=%d", qhash, n)
	return n > 0, nil
}

// DeleteWrongAnswer удаляет запись о неверном ответе (точный answer_json). true — была и удалена.
func (s *Store) DeleteWrongAnswer(qhash, answerJSON string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM quiz_wrong_answers WHERE qhash = ? AND answer_json = ?`, qhash, answerJSON)
	if err != nil {
		logx.Errorf("store", "DeleteWrongAnswer: qhash=%s: %v", qhash, err)
		return false, fmt.Errorf("store: delete wrong answer: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// CountVerifiedForCMID — сколько верных ответов в базе записано по этому тесту (cmid).
// Нужно, чтобы понять, знает ли база этот тест, без проверки каждого вопроса.
func (s *Store) CountVerifiedForCMID(cmid int64) (int64, error) {
	var n int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM quiz_answers WHERE cmid = ?`, cmid).Scan(&n); err != nil {
		logx.Errorf("store", "CountVerifiedForCMID: cmid=%d: %v", cmid, err)
		return 0, fmt.Errorf("store: count verified answers: %w", err)
	}
	return n, nil
}
