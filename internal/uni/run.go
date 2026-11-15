package uni

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
)

// KickKey — флаг «синхронизировать Moodle сейчас» (панель и бот ставят, rasp снимает).
func KickKey(telegramID int64) string { return fmt.Sprintf("moodle_kick:%d", telegramID) }

// WakeKickKey — флаг «синхронизировать Moodle немедленно из-за вейкворда на паре».
const WakeKickKey = "moodle_wake_kick"

const (
	defaultMoodleWorkers = 4
	defaultQuizWorkers   = 4
)

// envWorkers читает число воркеров из переменной окружения name (int > 0), иначе def.
func envWorkers(name string, def int) int {
	val := strings.TrimSpace(os.Getenv(name))
	if val == "" {
		return def
	}
	n, err := strconv.Atoi(val)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// MoodleWorkersLimit возвращает число параллельных воркеров для обхода Moodle.
// Читает переменную окружения MOODLE_WORKERS (int > 0), по умолчанию 4.
func MoodleWorkersLimit() int {
	return envWorkers("MOODLE_WORKERS", defaultMoodleWorkers)
}

// QuizWorkersLimit возвращает, сколько тестов решается одновременно.
// Читает переменную окружения MOODLE_QUIZ_WORKERS (int > 0), по умолчанию 4.
// Это отдельный пул от MOODLE_WORKERS: решение тестов занимает десятки минут
// и не должно держать слоты обхода аккаунтов.
func QuizWorkersLimit() int {
	return envWorkers("MOODLE_QUIZ_WORKERS", defaultQuizWorkers)
}

func (s *Syncer) workersLimit() int {
	if s.MoodleWorkers > 0 {
		return s.MoodleWorkers
	}
	return MoodleWorkersLimit()
}

func (s *Syncer) getSem() chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sem == nil {
		s.sem = make(chan struct{}, s.workersLimit())
	}
	return s.sem
}

// getQuizSem — семафор пула решения тестов (отдельно от семафора обхода аккаунтов).
func (s *Syncer) getQuizSem() chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.quizSem == nil {
		n := s.QuizWorkers
		if n <= 0 {
			n = QuizWorkersLimit()
		}
		s.quizSem = make(chan struct{}, n)
	}
	return s.quizSem
}

// tryLockAccount пытается заблокировать аккаунт для синхронизации.
// Возвращает true, если аккаунт успешно заблокирован (не был занят).
func (s *Syncer) tryLockAccount(tgID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runningAcc == nil {
		s.runningAcc = make(map[int64]bool)
	}
	if s.runningAcc[tgID] {
		return false
	}
	s.runningAcc[tgID] = true
	return true
}

// unlockAccount освобождает аккаунт после завершения синхронизации.
func (s *Syncer) unlockAccount(tgID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runningAcc != nil {
		delete(s.runningAcc, tgID)
	}
}

// TryLockQuiz пытается заблокировать тест для предотвращения одновременного решения.
func (s *Syncer) TryLockQuiz(tgID int64, quizID string) bool {
	return s.tryLockQuiz(tgID, quizID)
}

func (s *Syncer) tryLockQuiz(tgID int64, quizID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runningQuiz == nil {
		s.runningQuiz = make(map[string]bool)
	}
	key := fmt.Sprintf("%d:%s", tgID, quizID)
	if s.runningQuiz[key] {
		return false
	}
	s.runningQuiz[key] = true
	return true
}

// UnlockQuiz освобождает тест после завершения решения.
func (s *Syncer) UnlockQuiz(tgID int64, quizID string) {
	s.unlockQuiz(tgID, quizID)
}

func (s *Syncer) unlockQuiz(tgID int64, quizID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runningQuiz != nil {
		key := fmt.Sprintf("%d:%s", tgID, quizID)
		delete(s.runningQuiz, key)
	}
}

// Wait ждёт завершения всех активных фоновых горутин обхода Moodle.
func (s *Syncer) Wait() {
	s.wg.Wait()
}

// Kick просит rasp пересобрать задания пользователя на ближайшем тике.
func Kick(st *store.Store, telegramID int64) error {
	logx.Infof("uni", "moodle tg=%d: запрошен синк (kick) из %s", telegramID, callerOf())
	if err := st.SetSetting(KickKey(telegramID), "1"); err != nil {
		logx.Warnf("uni", "moodle tg=%d: kick не записан: %v", telegramID, err)
		return err
	}
	return nil
}

// WakeKick просит rasp немедленно проверить и синхронизировать Moodle при обнаружении вейкворда.
func WakeKick(st *store.Store) error {
	logx.Infof("uni", "moodle: запрошен внеочередной синк всех аккаунтов (wake_kick) из %s", callerOf())
	if err := st.SetSetting(WakeKickKey, "1"); err != nil {
		logx.Warnf("uni", "wake_kick не записан: %v", err)
		return err
	}
	return nil
}

// callerOf — имя функции, которая вызвала Kick/WakeKick, для журнала.
func callerOf() string {
	pc, _, _, ok := runtime.Caller(2)
	if !ok {
		return "?"
	}
	name := runtime.FuncForPC(pc).Name()
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return name
}

// triggerKey — причина синка Moodle, которую кладём в контекст (для журнала).
type triggerKey struct{}

func withTrigger(ctx context.Context, trigger string) context.Context {
	return context.WithValue(ctx, triggerKey{}, trigger)
}

// syncTrigger — что запустило синк: пинок, вейкворд, таймер; def — если причина не задана.
func syncTrigger(ctx context.Context, def string) string {
	if t, ok := ctx.Value(triggerKey{}).(string); ok && t != "" {
		return t
	}
	return def
}

// wakeKickedKey — флаг в контексте, что синк Moodle запущен по сигналу вейкворда (wake_kick).
type wakeKickedKey struct{}

func withWakeKicked(ctx context.Context) context.Context {
	return context.WithValue(ctx, wakeKickedKey{}, true)
}

func isWakeKicked(ctx context.Context) bool {
	v, ok := ctx.Value(wakeKickedKey{}).(bool)
	return ok && v
}

func consumeKickValue(st *store.Store, telegramID int64) string {
	v, ok, err := st.GetSetting(KickKey(telegramID))
	if err != nil || !ok || v == "" {
		return ""
	}
	if err := st.SetSetting(KickKey(telegramID), ""); err != nil {
		logx.Warnf("uni", "consume kick: %v", err)
	}
	return v
}

func hasWakeKick(st *store.Store) bool {
	v, ok, err := st.GetSetting(WakeKickKey)
	return err == nil && ok && v != ""
}

// clearWakeKick сбрасывает флаг внеочередной синхронизации Moodle.
func clearWakeKick(st *store.Store) {
	if err := st.SetSetting(WakeKickKey, ""); err != nil {
		logx.Warnf("uni", "clear wake kick: %v", err)
	}
}

func consumeWakeKick(st *store.Store) bool {
	if !hasWakeKick(st) {
		return false
	}
	clearWakeKick(st)
	return true
}

type moodleTask struct {
	acc        model.Account
	kicked     bool
	wakeKicked bool
	full       bool
	trigger    string
}

// sortMoodleTasks сортирует задачи так, чтобы те, по кому пришёл сигнал (kicked или wakeKick),
// шли первыми перед плановыми (scheduled), чтобы первыми захватывать слоты воркеров.
func sortMoodleTasks(tasks []moodleTask) {
	sort.SliceStable(tasks, func(i, j int) bool {
		if tasks[i].kicked && !tasks[j].kicked {
			return true
		}
		if !tasks[i].kicked && tasks[j].kicked {
			return false
		}
		return false
	})
}

// Run — фоновый цикл Moodle: проверка киков каждые 5 секунд, плановый обход
// раз в 30 минут (полный обход курсов раз в 6 часов) или сразу по kick / wake_kick.
func (s *Syncer) Run(ctx context.Context) {
	t := time.NewTicker(kickTick)
	defer t.Stop()
	for {
		s.moodleTick(ctx)
		select {
		case <-ctx.Done():
			s.wg.Wait()
			return
		case <-t.C:
		}
	}
}

func (s *Syncer) moodleTick(ctx context.Context) {
	accs, err := s.Store.ListAccounts(model.AccountMoodle)
	if err != nil {
		logx.Warnf("uni", "moodle accounts: %v", err)
		return
	}
	if len(accs) == 0 {
		return
	}

	// Снятие флага wake_kick до запуска обхода аккаунтов, чтобы новые сигналы
	// во время долгого обхода не терялись.
	wakeKicked := false
	if hasWakeKick(s.Store) {
		wakeKicked = true
		clearWakeKick(s.Store)
		logx.Infof("uni", "moodle: вейкворд на паре — внеочередной синк Moodle для %d аккаунтов", len(accs))
	}

	now := s.now()
	var tasks []moodleTask

	for _, acc := range accs {
		if ctx.Err() != nil {
			for _, t := range tasks {
				s.unlockAccount(t.acc.TelegramID)
			}
			return
		}

		// Если аккаунт уже синхронизируется прямо сейчас, не запускаем его повторно.
		// Важно: если для занятого аккаунта пришёл kick, не читаем и не сбрасываем его,
		// чтобы пинок остался в базе и сработал на следующем тике после освобождения аккаунта.
		// При глобальном wake_kick ставим занятому аккаунту персональный кик со значением "wake",
		// чтобы сигнал вейкворда не потерялся после снятия общего WakeKickKey.
		if !s.tryLockAccount(acc.TelegramID) {
			if wakeKicked {
				if err := s.Store.SetSetting(KickKey(acc.TelegramID), "wake"); err != nil {
					logx.Warnf("uni", "moodle tg=%d: не удалось сохранить отложенный wake_kick: %v", acc.TelegramID, err)
				}
			}
			continue
		}

		kickVal := consumeKickValue(s.Store, acc.TelegramID)
		accWakeKicked := wakeKicked || kickVal == "wake"
		kickedByUser := kickVal != "" && kickVal != "wake"
		due := s.moodleDue(acc, now)
		if !kickedByUser && !accWakeKicked && !due {
			s.unlockAccount(acc.TelegramID)
			continue
		}

		s.mu.Lock()
		last := s.lastFull[acc.TelegramID]
		s.mu.Unlock()

		full := kickedByUser || accWakeKicked || now.Sub(last) >= moodleFull
		trigger := "по таймеру, раз в 30 мин"
		switch {
		case kickedByUser:
			trigger = "пинок kick (панель или бот)"
		case accWakeKicked:
			trigger = "вейкворд на паре (wake_kick)"
		case full:
			trigger = "по таймеру, полный обход раз в 6 ч"
		}

		tasks = append(tasks, moodleTask{
			acc:        acc,
			kicked:     kickedByUser || accWakeKicked,
			wakeKicked: accWakeKicked,
			full:       full,
			trigger:    trigger,
		})
	}

	// Сигнальные аккаунты (kicked или wakeKick) идут первыми перед плановыми,
	// чтобы первыми захватывать слоты воркеров.
	sortMoodleTasks(tasks)

	sem := s.getSem()
	for _, task := range tasks {
		t := task
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer s.unlockAccount(t.acc.TelegramID)

			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			if ctx.Err() != nil {
				return
			}
			syncCtx := withTrigger(ctx, t.trigger)
			if t.wakeKicked {
				syncCtx = withWakeKicked(syncCtx)
			}
			// Ошибку SyncMoodle уже записал с контекстом (tg, что делали).
			_ = s.SyncMoodle(syncCtx, t.acc, t.full)
		}()
	}
}

func (s *Syncer) moodleDue(acc model.Account, now time.Time) bool {
	switch acc.Status {
	case model.AccountBadLogin:
		return false // ждём новый пароль: PutAccount сбросит статус
	case model.AccountError:
		return now.Sub(acc.UpdatedAt) >= retryAfterErr
	case model.AccountPending, "":
		return true
	}
	return acc.SyncedAt == nil || now.Sub(*acc.SyncedAt) >= moodleEvery
}
