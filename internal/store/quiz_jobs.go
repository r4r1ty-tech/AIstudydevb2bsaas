package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

// Статусы задачи решения теста (таблица quiz_jobs).
const (
	QuizJobQueued  = "queued"
	QuizJobRunning = "running"
	QuizJobDone    = "done"
	QuizJobFailed  = "failed"
)

// QuizMaxClaims — сколько раз задачу можно захватить. Больше — задача падает в failed:
// скорее всего, процесс умирает на ней раз за разом (память, падение при решении).
const QuizMaxClaims = 5

// QuizClaimLimitError — last_error задачи, превысившей QuizMaxClaims захватов.
const QuizClaimLimitError = "слишком много перезахватов (возможно, падение процесса)"

// QuizJob — задача решения теста в очереди сервиса ssau-quiz.
// Deadline — время закрытия теста из Moodle (nil, если неизвестно).
type QuizJob struct {
	ID         int64
	TelegramID int64
	CMID       int64
	Course     string
	Title      string
	Forced     bool
	Status     string
	Deadline   *time.Time
	Attempts   int
	ClaimedBy  string
	LeaseUntil *time.Time
	LastError  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

const quizJobCols = `id, telegram_id, cmid, course, title, forced, status, deadline, attempts, claimed_by, lease_until, last_error, created_at, updated_at`

func scanQuizJob(sc scanner) (*QuizJob, error) {
	var j QuizJob
	var forced int
	var deadline, lease, created, updated sql.NullString
	if err := sc.Scan(&j.ID, &j.TelegramID, &j.CMID, &j.Course, &j.Title, &forced, &j.Status, &deadline,
		&j.Attempts, &j.ClaimedBy, &lease, &j.LastError, &created, &updated); err != nil {
		return nil, err
	}
	j.Forced = forced != 0
	var err error
	if j.Deadline, err = parseNullTime(deadline); err != nil {
		return nil, fmt.Errorf("store: quiz job deadline: %w", err)
	}
	if j.LeaseUntil, err = parseNullTime(lease); err != nil {
		return nil, fmt.Errorf("store: quiz job lease: %w", err)
	}
	if j.CreatedAt, err = parseTime(nullStr(created)); err != nil {
		return nil, fmt.Errorf("store: quiz job created_at: %w", err)
	}
	if j.UpdatedAt, err = parseTime(nullStr(updated)); err != nil {
		return nil, fmt.Errorf("store: quiz job updated_at: %w", err)
	}
	return &j, nil
}

// EnqueueQuizJob ставит решение теста в очередь. Если для пары (аккаунт, тест) уже есть задача
// в статусе queued или running, новая не создаётся и возвращается created=false.
// При j.Forced («Пройти») существующая задача получает forced=1: приоритет поднимается.
func (s *Store) EnqueueQuizJob(j QuizJob, now time.Time) (bool, error) {
	logx.Debugf("store", "EnqueueQuizJob: enter tg=%d cmid=%d forced=%v", j.TelegramID, j.CMID, j.Forced)
	if s == nil || s.db == nil {
		return false, errors.New("store: nil")
	}
	if j.TelegramID == 0 || j.CMID == 0 {
		return false, errors.New("store: enqueue quiz: пустой аккаунт или тест")
	}
	nowS := timeArg(now)

	tx, err := s.db.Begin()
	if err != nil {
		logx.Errorf("store", "EnqueueQuizJob: begin: %v", err)
		return false, fmt.Errorf("store: enqueue quiz begin: %w", err)
	}
	defer func() {
		if rerr := tx.Rollback(); rerr != nil && rerr != sql.ErrTxDone {
			logx.Warnf("store", "EnqueueQuizJob: rollback: %v", rerr)
		}
	}()

	res, err := tx.Exec(
		`INSERT INTO quiz_jobs (telegram_id, cmid, course, title, forced, status, deadline, attempts, claimed_by, lease_until, last_error, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, 'queued', ?, 0, '', NULL, '', ?, ?)
		 ON CONFLICT (telegram_id, cmid) WHERE status IN ('queued', 'running') DO NOTHING`,
		j.TelegramID, j.CMID, j.Course, j.Title, btoi(j.Forced), nullTimeArg(j.Deadline), nowS, nowS,
	)
	if err != nil {
		logx.Errorf("store", "EnqueueQuizJob: insert tg=%d cmid=%d: %v", j.TelegramID, j.CMID, err)
		return false, fmt.Errorf("store: enqueue quiz: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: enqueue quiz rows: %w", err)
	}
	if n == 1 {
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("store: enqueue quiz commit: %w", err)
		}
		logx.Debugf("store", "EnqueueQuizJob: queued tg=%d cmid=%d", j.TelegramID, j.CMID)
		return true, nil
	}

	// Задача уже активна: «Пройти» поднимает её приоритет (forced), остальное не трогаем.
	if j.Forced {
		if _, err := tx.Exec(
			`UPDATE quiz_jobs SET forced = 1, updated_at = ?
			 WHERE telegram_id = ? AND cmid = ? AND status IN ('queued', 'running') AND forced = 0`,
			nowS, j.TelegramID, j.CMID,
		); err != nil {
			logx.Errorf("store", "EnqueueQuizJob: поднять forced tg=%d cmid=%d: %v", j.TelegramID, j.CMID, err)
			return false, fmt.Errorf("store: enqueue quiz raise forced: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("store: enqueue quiz commit: %w", err)
	}
	logx.Debugf("store", "EnqueueQuizJob: уже активна tg=%d cmid=%d forced=%v", j.TelegramID, j.CMID, j.Forced)
	return false, nil
}

// ClaimQuizJob атомарно берёт следующую задачу для воркера worker с арендой lease.
// Порядок: forced («Пройти») впереди; затем задачи с известным сроком закрытия (раньше — раньше);
// затем по времени постановки. Задача аккаунта не выдаётся, если у него уже есть running-задача
// с живой арендой: один тест на аккаунт одновременно. Просроченная аренда (воркер умер)
// перехватывается, попытка Moodle продолжится той же. Нет подходящей задачи — nil, nil.
func (s *Store) ClaimQuizJob(worker string, now time.Time, lease time.Duration) (*QuizJob, error) {
	return s.ClaimQuizJobExcept(worker, now, lease, nil)
}

// ClaimQuizJobExcept — ClaimQuizJob, который не выдаёт задачи из exclude: воркер передаёт id задач,
// которые его же горутины ещё решают, чтобы не решать одну задачу дважды после истечения аренды.
func (s *Store) ClaimQuizJobExcept(worker string, now time.Time, lease time.Duration, exclude []int64) (*QuizJob, error) {
	logx.Debugf("store", "ClaimQuizJob: enter worker=%s lease=%s исключено=%d", worker, lease, len(exclude))
	if s == nil || s.db == nil {
		return nil, errors.New("store: nil")
	}
	nowUTC := now.UTC()
	nowS := timeArg(nowUTC)
	leaseS := timeArg(nowUTC.Add(lease))

	tx, err := s.db.Begin()
	if err != nil {
		logx.Errorf("store", "ClaimQuizJob: begin: %v", err)
		return nil, fmt.Errorf("store: claim quiz begin: %w", err)
	}
	defer func() {
		if rerr := tx.Rollback(); rerr != nil && rerr != sql.ErrTxDone {
			logx.Warnf("store", "ClaimQuizJob: rollback: %v", rerr)
		}
	}()

	// Задачи, которые уже захватывали QuizMaxClaims раз (процесс падал на каждой попытке),
	// в очередь больше не отдаём: переводим в failed с понятной причиной.
	swept, err := tx.Exec(
		`UPDATE quiz_jobs
		 SET status = 'failed', last_error = ?, lease_until = NULL, claimed_by = '', updated_at = ?
		 WHERE (status = 'queued' OR (status = 'running' AND lease_until <= ?)) AND attempts >= ?`,
		QuizClaimLimitError, nowS, nowS, QuizMaxClaims,
	)
	if err != nil {
		logx.Errorf("store", "ClaimQuizJob: перевод перехваченных задач в failed: %v", err)
		return nil, fmt.Errorf("store: claim quiz sweep: %w", err)
	}
	if n, _ := swept.RowsAffected(); n > 0 {
		logx.Warnf("store", "ClaimQuizJob: %d задач переведено в failed: %s", n, QuizClaimLimitError)
	}

	notIn := ""
	args := []any{worker, leaseS, nowS, nowS, QuizMaxClaims, nowS}
	if len(exclude) > 0 {
		marks := make([]string, len(exclude))
		for i, id := range exclude {
			marks[i] = "?"
			args = append(args, id)
		}
		notIn = " AND j.id NOT IN (" + strings.Join(marks, ",") + ")"
	}
	row := tx.QueryRow(
		`UPDATE quiz_jobs
		 SET status = 'running', claimed_by = ?, lease_until = ?, attempts = attempts + 1, updated_at = ?
		 WHERE id = (
			SELECT j.id FROM quiz_jobs j
			WHERE (j.status = 'queued' OR (j.status = 'running' AND j.lease_until <= ?))
			  AND j.attempts < ?
			  AND NOT EXISTS (
				SELECT 1 FROM quiz_jobs r
				WHERE r.telegram_id = j.telegram_id AND r.status = 'running' AND r.lease_until > ?
			  )`+notIn+`
			ORDER BY j.forced DESC, j.deadline IS NULL, j.deadline, j.created_at, j.id
			LIMIT 1
		 )
		 RETURNING `+quizJobCols,
		args...,
	)
	j, err := scanQuizJob(row)
	if cerr := tx.Commit(); cerr != nil {
		logx.Errorf("store", "ClaimQuizJob: commit: %v", cerr)
		return nil, fmt.Errorf("store: claim quiz commit: %w", cerr)
	}
	if errors.Is(err, sql.ErrNoRows) {
		logx.Debugf("store", "ClaimQuizJob: очередь пуста для worker=%s", worker)
		return nil, nil
	}
	if err != nil {
		logx.Errorf("store", "ClaimQuizJob: worker=%s: %v", worker, err)
		return nil, fmt.Errorf("store: claim quiz job: %w", err)
	}
	logx.Infof("store", "ClaimQuizJob: job=%d tg=%d cmid=%d worker=%s попытка=%d", j.ID, j.TelegramID, j.CMID, worker, j.Attempts)
	return j, nil
}

// RenewQuizLease продлевает аренду задачи id, если её держит worker. false — аренду потеряли.
func (s *Store) RenewQuizLease(id int64, worker string, now time.Time, lease time.Duration) (bool, error) {
	logx.Debugf("store", "RenewQuizLease: job=%d worker=%s", id, worker)
	if s == nil || s.db == nil {
		return false, errors.New("store: nil")
	}
	res, err := s.db.Exec(
		`UPDATE quiz_jobs SET lease_until = ?, updated_at = ?
		 WHERE id = ? AND claimed_by = ? AND status = 'running'`,
		timeArg(now.UTC().Add(lease)), timeArg(now.UTC()), id, worker,
	)
	if err != nil {
		logx.Errorf("store", "RenewQuizLease: job=%d: %v", id, err)
		return false, fmt.Errorf("store: renew quiz lease: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: renew quiz lease rows: %w", err)
	}
	return n == 1, nil
}

// FinishQuizJob завершает задачу статусом done или failed (lastErr — причина для failed).
// false — задача уже не у worker (аренда потеряна, другой воркер её перехватил).
func (s *Store) FinishQuizJob(id int64, worker, status, lastErr string, now time.Time) (bool, error) {
	logx.Debugf("store", "FinishQuizJob: job=%d worker=%s status=%s", id, worker, status)
	if s == nil || s.db == nil {
		return false, errors.New("store: nil")
	}
	if status != QuizJobDone && status != QuizJobFailed {
		return false, fmt.Errorf("store: finish quiz job: статус %q не финальный", status)
	}
	res, err := s.db.Exec(
		`UPDATE quiz_jobs SET status = ?, last_error = ?, lease_until = NULL, claimed_by = '', updated_at = ?
		 WHERE id = ? AND claimed_by = ? AND status = 'running'`,
		status, lastErr, timeArg(now.UTC()), id, worker,
	)
	if err != nil {
		logx.Errorf("store", "FinishQuizJob: job=%d: %v", id, err)
		return false, fmt.Errorf("store: finish quiz job: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: finish quiz job rows: %w", err)
	}
	return n == 1, nil
}

// ReleaseQuizJob возвращает задачу в queued (остановка воркера). Попытка Moodle при
// следующем захвате продолжится той же. false — задача уже не у worker.
func (s *Store) ReleaseQuizJob(id int64, worker string, now time.Time) (bool, error) {
	logx.Debugf("store", "ReleaseQuizJob: job=%d worker=%s", id, worker)
	if s == nil || s.db == nil {
		return false, errors.New("store: nil")
	}
	res, err := s.db.Exec(
		`UPDATE quiz_jobs SET status = 'queued', lease_until = NULL, claimed_by = '', updated_at = ?
		 WHERE id = ? AND claimed_by = ? AND status = 'running'`,
		timeArg(now.UTC()), id, worker,
	)
	if err != nil {
		logx.Errorf("store", "ReleaseQuizJob: job=%d: %v", id, err)
		return false, fmt.Errorf("store: release quiz job: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: release quiz job rows: %w", err)
	}
	return n == 1, nil
}

// QuizJobByID возвращает задачу по id или nil, если её нет.
func (s *Store) QuizJobByID(id int64) (*QuizJob, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("store: nil")
	}
	j, err := scanQuizJob(s.db.QueryRow(`SELECT `+quizJobCols+` FROM quiz_jobs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		logx.Errorf("store", "QuizJobByID: id=%d: %v", id, err)
		return nil, fmt.Errorf("store: quiz job by id: %w", err)
	}
	return j, nil
}

// ActiveQuizJob возвращает задачу аккаунта по тесту, если она queued или running; иначе nil.
func (s *Store) ActiveQuizJob(telegramID, cmid int64) (*QuizJob, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("store: nil")
	}
	j, err := scanQuizJob(s.db.QueryRow(
		`SELECT `+quizJobCols+` FROM quiz_jobs
		 WHERE telegram_id = ? AND cmid = ? AND status IN ('queued', 'running')`,
		telegramID, cmid,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		logx.Errorf("store", "ActiveQuizJob: tg=%d cmid=%d: %v", telegramID, cmid, err)
		return nil, fmt.Errorf("store: active quiz job: %w", err)
	}
	return j, nil
}

// PruneQuizJobs удаляет завершённые задачи (done и failed), обновлённые раньше before.
// Активные задачи (queued, running) не трогает. Возвращает число удалённых строк.
func (s *Store) PruneQuizJobs(before time.Time) (int64, error) {
	logx.Debugf("store", "PruneQuizJobs: enter before=%s", before.UTC().Format(time.RFC3339))
	if s == nil || s.db == nil {
		return 0, errors.New("store: nil")
	}
	res, err := s.db.Exec(
		`DELETE FROM quiz_jobs WHERE status IN ('done', 'failed') AND updated_at < ?`,
		timeArg(before),
	)
	if err != nil {
		logx.Errorf("store", "PruneQuizJobs: %v", err)
		return 0, fmt.Errorf("store: prune quiz jobs: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: prune quiz jobs rows: %w", err)
	}
	return n, nil
}
