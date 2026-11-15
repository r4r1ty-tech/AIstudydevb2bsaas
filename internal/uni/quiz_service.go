package uni

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodle"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
)

// Режимы решения тестов (переменная QUIZ_MODE).
const (
	// QuizModeService — тесты ставятся в очередь в базе, решает отдельный сервис ssau-quiz. Режим по умолчанию.
	QuizModeService = "service"
	// QuizModeInline — пул горутин в процессе rasp (прежнее поведение): для локальной разработки и тестов.
	QuizModeInline = "inline"
)

const (
	defaultQuizParallel  = 50
	defaultQuizMoodleRPS = 3.0
)

// Сроки очереди. Переменные, а не константы — тесты сокращают их (как tick и kickTick в sync.go).
var (
	quizLeaseDur    = 10 * time.Minute    // аренда задачи: воркер, умерший без продления, теряет её через это время
	quizRenewEvery  = 2 * time.Minute     // период продления аренды
	quizPollEvery   = 3 * time.Second     // пауза между опросами пустой очереди
	quizStartJitter = 5 * time.Second     // случайный разнос старта задач: 0..quizStartJitter
	quizPruneEvery  = time.Hour           // как часто чистить завершённые задачи из quiz_jobs
	quizPruneKeep   = 14 * 24 * time.Hour // завершённые задачи старше этого срока удаляются
)

// QuizModeFromEnv возвращает режим из QUIZ_MODE. Пусто — service; неизвестное значение — service с предупреждением.
func QuizModeFromEnv() string {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("QUIZ_MODE")))
	switch v {
	case "", QuizModeService:
		return QuizModeService
	case QuizModeInline:
		return QuizModeInline
	}
	logx.Warnf("uni", "QUIZ_MODE=%q не поддерживается, используем %s", v, QuizModeService)
	return QuizModeService
}

// QuizMaxParallel — сколько тестов решает воркер очереди одновременно (QUIZ_MAX_PARALLEL, по умолчанию 50).
func QuizMaxParallel() int {
	return envWorkers("QUIZ_MAX_PARALLEL", defaultQuizParallel)
}

// QuizMoodleRPS — сколько входов и стартов попыток Moodle в секунду допускает воркер очереди
// (QUIZ_MOODLE_RPS, по умолчанию 3). Ограничитель общий для всех тестов процесса.
func QuizMoodleRPS() float64 {
	val := strings.TrimSpace(os.Getenv("QUIZ_MOODLE_RPS"))
	if val == "" {
		return defaultQuizMoodleRPS
	}
	f, err := strconv.ParseFloat(val, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f <= 0 {
		return defaultQuizMoodleRPS
	}
	return f
}

// quizServiceMode — решает ли тесты отдельный сервис через очередь (true) или пул процесса (false).
func (s *Syncer) quizServiceMode() bool {
	mode := s.QuizMode
	if mode == "" {
		mode = QuizModeFromEnv()
	}
	return mode != QuizModeInline
}

// dispatchQuiz ставит решение теста: в очередь сервиса (режим service) или в пул процесса (inline).
// queued=false — тест уже решается или ждёт в очереди (или авто-постановка заблокирована после неудач).
// err — очередь в базе недоступна: только для режима service.
func (s *Syncer) dispatchQuiz(ctx context.Context, acc model.Account, c *moodle.Client, it model.MoodleItem, forced bool) (bool, error) {
	if s.quizServiceMode() {
		return s.enqueueQuiz(acc, it, forced)
	}
	return s.scheduleQuiz(ctx, acc, c, it, forced), nil
}

// enqueueQuiz ставит тест в очередь сервиса ssau-quiz. Дубль (тест уже queued или running)
// не создаётся; forced поднимает приоритет существующей задачи. Авто-постановка не идёт,
// пока тест не исчерпал quizMaxAttempts неудач подряд (как и в scheduleQuiz).
func (s *Syncer) enqueueQuiz(acc model.Account, it model.MoodleItem, forced bool) (bool, error) {
	if !forced && s.quizFailCount(acc.TelegramID, it.CMID) >= quizMaxAttempts {
		logx.Infof("uni", "quiz tg=%d cmid=%d: %d неудачных попыток подряд, авто-решение не ставлю, ждём «Пройти»",
			acc.TelegramID, it.CMID, quizMaxAttempts)
		return false, nil
	}
	created, err := s.Store.EnqueueQuizJob(store.QuizJob{
		TelegramID: acc.TelegramID,
		CMID:       it.CMID,
		Course:     it.Course,
		Title:      it.Title,
		Forced:     forced,
		Deadline:   it.Due,
	}, s.now())
	if err != nil {
		logx.Warnf("uni", "quiz tg=%d cmid=%d: постановка в очередь: %v", acc.TelegramID, it.CMID, err)
		return false, err
	}
	if created {
		logx.Infof("uni", "quiz tg=%d cmid=%d %q: поставлен в очередь решения, forced=%v", acc.TelegramID, it.CMID, it.Title, forced)
	} else {
		logx.Infof("uni", "quiz tg=%d cmid=%d: тест уже решается или ждёт в очереди, пропуск", acc.TelegramID, it.CMID)
	}
	return created, nil
}

// quizPartialKey — настройка «часть страниц отправлена, попытка не завершена» (ставит processQuizErr).
func quizPartialKey(tg, cmid int64) string {
	return fmt.Sprintf("quiz_partial:%d:%d", tg, cmid)
}

// quizPartial возвращает описание частичной отправки текущей попытки или "".
func (s *Syncer) quizPartial(tg, cmid int64) string {
	v, ok, err := s.Store.GetSetting(quizPartialKey(tg, cmid))
	if err != nil || !ok {
		return ""
	}
	return v
}

// quizSolvedAt — тест уже отмечен решённым (quiz_solved), см. processQuizErr.
func (s *Syncer) quizSolvedAt(tg, cmid int64) bool {
	v, ok, err := s.Store.GetSetting(fmt.Sprintf("quiz_solved:%d:%d", tg, cmid))
	return err == nil && ok && v != ""
}

// RunQuizWorker — воркер очереди решения тестов (сервис ssau-quiz). Цикл: захват задачи
// (ClaimQuizJob) → решение тем же solveQuiz, что в inline-режиме → завершение задачи.
// Одновременно решается до QUIZ_MAX_PARALLEL тестов; у каждой задачи своя аренда с продлением.
// По отмене ctx незавершённые задачи возвращаются в очередь (ReleaseQuizJob), и метод
// возвращается только после них. Попытка Moodle при следующем захвате продолжится той же.
func (s *Syncer) RunQuizWorker(ctx context.Context) {
	if s.Store == nil || s.Vault == nil {
		logx.Errorf("uni", "quiz-worker: нужны база и ключ шифрования, воркер не запущен")
		return
	}
	if s.quizPacer == nil {
		s.quizPacer = newMoodlePacer(QuizMoodleRPS())
	}
	parallel := s.QuizParallel
	if parallel <= 0 {
		parallel = QuizMaxParallel()
	}
	worker := quizWorkerID()
	logx.Infof("uni", "quiz-worker %s: старт, одновременно до %d тестов, вход и старт попытки в Moodle не чаще %.2f в секунду",
		worker, parallel, QuizMoodleRPS())

	slots := make(chan struct{}, parallel)
	inflight := &jobSet{ids: map[int64]bool{}}
	var wg sync.WaitGroup
	defer wg.Wait()
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.pruneQuizLoop(ctx)
	}()
	for {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		if ctx.Err() != nil {
			<-slots
			return
		}
		// Задачи, которые этот процесс решает сейчас, не отдаём повторно: даже при истёкшей аренде
		// (например, при недоступной базе) вторая горутина на той же попытке не нужна.
		job, err := s.Store.ClaimQuizJobExcept(worker, s.now(), quizLeaseDur, inflight.list())
		if err != nil {
			<-slots
			logx.Errorf("uni", "quiz-worker %s: захват задачи: %v", worker, err)
			sleep(ctx, quizPollEvery)
			continue
		}
		if job == nil {
			<-slots
			sleep(ctx, quizPollEvery)
			continue
		}
		inflight.add(job.ID)
		wg.Add(1)
		go func(job store.QuizJob) {
			defer wg.Done()
			defer func() { <-slots }()
			defer inflight.remove(job.ID)
			s.runQuizJob(ctx, worker, job)
		}(*job)
	}
}

// jobSet — задачи, которые решает этот процесс прямо сейчас.
type jobSet struct {
	mu  sync.Mutex
	ids map[int64]bool
}

func (j *jobSet) add(id int64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.ids[id] = true
}

func (j *jobSet) remove(id int64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	delete(j.ids, id)
}

func (j *jobSet) list() []int64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]int64, 0, len(j.ids))
	for id := range j.ids {
		out = append(out, id)
	}
	return out
}

// runQuizJob ведёт одну задачу: решение с продлением аренды, затем завершение или возврат в очередь.
func (s *Syncer) runQuizJob(ctx context.Context, worker string, job store.QuizJob) {
	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	stopRenew := make(chan struct{})
	renewDone := make(chan struct{})
	// Отсчёт истечения аренды начинается здесь, до запуска горутины, а не внутри неё.
	lastOK := s.now()
	go func() {
		defer close(renewDone)
		s.renewQuizLease(jobCtx, cancel, job.ID, worker, lastOK, stopRenew)
	}()

	logx.Infof("uni", "quiz-job %d tg=%d cmid=%d: решение, попытка очереди %d, forced=%v", job.ID, job.TelegramID, job.CMID, job.Attempts, job.Forced)
	err := s.solveJob(jobCtx, job)
	close(stopRenew)
	<-renewDone

	if jobCtx.Err() != nil {
		// Остановка сервиса или потеря аренды: задача возвращается в очередь, если она ещё наша.
		if ok, rerr := s.Store.ReleaseQuizJob(job.ID, worker, s.now()); rerr != nil {
			logx.Warnf("uni", "quiz-job %d: возврат в очередь: %v", job.ID, rerr)
		} else if ok {
			logx.Infof("uni", "quiz-job %d tg=%d cmid=%d: остановка, задача возвращена в очередь", job.ID, job.TelegramID, job.CMID)
		} else {
			logx.Infof("uni", "quiz-job %d: аренда уже не наша, в очередь не возвращаю", job.ID)
		}
		return
	}

	if s.quizSolvedAt(job.TelegramID, job.CMID) {
		if ok, ferr := s.Store.FinishQuizJob(job.ID, worker, store.QuizJobDone, "", s.now()); ferr != nil {
			logx.Warnf("uni", "quiz-job %d: завершение: %v", job.ID, ferr)
		} else if ok {
			logx.Infof("uni", "quiz-job %d tg=%d cmid=%d: готово", job.ID, job.TelegramID, job.CMID)
		}
		return
	}
	msg := "попытка решения не завершилась, повтор на следующем синке"
	if err != nil {
		msg = err.Error()
	}
	if partial := s.quizPartial(job.TelegramID, job.CMID); partial != "" {
		// Часть страниц уже в Moodle, попытка открыта: отдельная понятная причина, а не общая.
		msg = "частично решён (" + partial + "), попытка не завершена: повтор доберёт остальные страницы"
	}
	if ok, ferr := s.Store.FinishQuizJob(job.ID, worker, store.QuizJobFailed, msg, s.now()); ferr != nil {
		logx.Warnf("uni", "quiz-job %d: завершение с неудачей: %v", job.ID, ferr)
	} else if ok {
		logx.Infof("uni", "quiz-job %d tg=%d cmid=%d: не решён, причина: %s", job.ID, job.TelegramID, job.CMID, msg)
	}
}

// solveJob решает задачу очереди тем же solveQuiz, что и inline-режим: вход по сохранённой сессии,
// перелогин при протухании, повторы через quiz_fail. Паника не роняет воркер: тест помечается неудачей.
func (s *Syncer) solveJob(ctx context.Context, job store.QuizJob) (err error) {
	it := s.jobItem(job)
	// Сигнал частичной отправки — только от этой попытки: старое значение сбрасываем.
	_ = s.Store.SetSetting(quizPartialKey(job.TelegramID, job.CMID), "")
	defer func() {
		if r := recover(); r != nil {
			logx.Errorf("uni", "quiz-job %d tg=%d cmid=%d: паника в решении: %v\n%s", job.ID, job.TelegramID, job.CMID, r, debug.Stack())
			s.markQuizRetry(ctx, job.TelegramID, it)
			err = fmt.Errorf("паника в решении: %v", r)
		}
	}()
	acc, gerr := s.Store.GetAccount(job.TelegramID, model.AccountMoodle)
	if gerr != nil {
		return fmt.Errorf("аккаунт Moodle: %w", gerr)
	}
	if acc == nil {
		return errors.New("аккаунт Moodle не найден")
	}
	// Случайный разнос старта: сотни задач не должны войти в Moodle в одну секунду с одного IP.
	if quizStartJitter > 0 {
		sleep(ctx, rand.N(quizStartJitter)) //nolint:gosec // джиттер, криптостойкость не нужна
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	s.solveQuiz(ctx, *acc, it, job.Forced)
	return nil
}

// jobItem — элемент Moodle для задачи: из кэша синка (ссылка, актуальные название и курс),
// а если его нет — из полей задачи.
func (s *Syncer) jobItem(job store.QuizJob) model.MoodleItem {
	it := model.MoodleItem{
		TelegramID: job.TelegramID, CMID: job.CMID, Kind: model.MoodleQuiz,
		Course: job.Course, Title: job.Title, Due: job.Deadline,
	}
	items, err := s.Store.ListMoodleItems(job.TelegramID)
	if err != nil {
		logx.Warnf("uni", "quiz-job %d: элементы Moodle: %v", job.ID, err)
		return it
	}
	for _, m := range items {
		if m.CMID == job.CMID {
			if m.Kind == "" {
				m.Kind = model.MoodleQuiz
			}
			return m
		}
	}
	return it
}

// renewQuizLease продлевает аренду задачи, пока идёт решение. Если аренда потеряна
// (другой воркер перехватил задачу) — отменяет решение: два воркера не решают один тест.
// lastOK — время аренды (захвата или последнего успешного продления), от него считается истечение.
func (s *Syncer) renewQuizLease(ctx context.Context, cancel context.CancelFunc, id int64, worker string, lastOK time.Time, stop <-chan struct{}) {
	t := time.NewTicker(quizRenewEvery)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-t.C:
		}
		ok, err := s.Store.RenewQuizLease(id, worker, s.now(), quizLeaseDur)
		if err != nil {
			logx.Warnf("uni", "quiz-job %d: продление аренды: %v", id, err)
			// База не отвечает дольше срока аренды: другой воркер мог её перехватить. Не решаем параллельно.
			if quizLeaseStale(lastOK, s.now()) {
				logx.Warnf("uni", "quiz-job %d: аренда не продлена дольше %s, решение останавливаю", id, quizLeaseDur)
				cancel()
				return
			}
			continue
		}
		if !ok {
			logx.Warnf("uni", "quiz-job %d: аренда потеряна, решение останавливаю", id)
			cancel()
			return
		}
		lastOK = s.now()
	}
}

// quizLeaseStale — аренда не продлена дольше срока аренды: её уже могли перехватить.
func quizLeaseStale(lastOK, now time.Time) bool {
	return now.Sub(lastOK) > quizLeaseDur
}

// pruneQuizLoop сразу и затем раз в quizPruneEvery удаляет завершённые задачи старше quizPruneKeep,
// чтобы таблица quiz_jobs не росла бесконечно. Активные задачи не трогаются.
func (s *Syncer) pruneQuizLoop(ctx context.Context) {
	t := time.NewTicker(quizPruneEvery)
	defer t.Stop()
	s.pruneQuizJobs()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.pruneQuizJobs()
		}
	}
}

// pruneQuizJobs — одна чистка: завершённые задачи старше quizPruneKeep.
func (s *Syncer) pruneQuizJobs() {
	before := s.now().Add(-quizPruneKeep)
	n, err := s.Store.PruneQuizJobs(before)
	if err != nil {
		logx.Warnf("uni", "quiz-worker: чистка очереди: %v", err)
		return
	}
	if n > 0 {
		logx.Infof("uni", "quiz-worker: удалено завершённых задач старше %s: %d", quizPruneKeep, n)
	}
}

// quizWorkerID — имя воркера в базе: хост, pid и время старта (после перезапуска имя другое).
func quizWorkerID() string {
	host, _ := os.Hostname()
	if host == "" {
		host = "quiz"
	}
	return fmt.Sprintf("%s:%d:%d", host, os.Getpid(), time.Now().UnixNano())
}

// moodlePacer — общий в процессе ограничитель темпа: резервирует слоты, не чаще одного в interval.
type moodlePacer struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

func newMoodlePacer(rps float64) *moodlePacer {
	if !(rps > 0) {
		rps = defaultQuizMoodleRPS
	}
	return &moodlePacer{interval: time.Duration(float64(time.Second) / rps)}
}

// wait резервирует очередной слот и ждёт его. Отмена ctx прерывает ожидание с ошибкой.
func (p *moodlePacer) wait(ctx context.Context) error {
	p.mu.Lock()
	at := time.Now()
	if p.next.After(at) {
		at = p.next
	}
	p.next = at.Add(p.interval)
	p.mu.Unlock()

	d := time.Until(at)
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// paceMoodle ждёт слот общего ограничителя перед входом или стартом попытки.
// В inline-режиме ограничителя нет.
func (s *Syncer) paceMoodle(ctx context.Context) error {
	if s.quizPacer == nil {
		return nil
	}
	return s.quizPacer.wait(ctx)
}

// pacedMoodle — клиент Moodle, у которого вход проходит через ограничитель темпа.
type pacedMoodle struct {
	*moodle.Client
	pace func(context.Context) error
}

// Login ждёт слот ограничителя и входит по логину.
func (p pacedMoodle) Login(ctx context.Context, login, password string) error {
	if err := p.pace(ctx); err != nil {
		return err
	}
	return p.Client.Login(ctx, login, password)
}
