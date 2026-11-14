package uni

import (
	"context"
	"fmt"
	"runtime"
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

func consumeKick(st *store.Store, telegramID int64) bool {
	v, ok, err := st.GetSetting(KickKey(telegramID))
	if err != nil || !ok || v == "" {
		return false
	}
	if err := st.SetSetting(KickKey(telegramID), ""); err != nil {
		logx.Warnf("uni", "consume kick: %v", err)
	}
	return true
}

func hasWakeKick(st *store.Store) bool {
	v, ok, err := st.GetSetting(WakeKickKey)
	return err == nil && ok && v != ""
}

func consumeWakeKick(st *store.Store) bool {
	if !hasWakeKick(st) {
		return false
	}
	if err := st.SetSetting(WakeKickKey, ""); err != nil {
		logx.Warnf("uni", "consume wake kick: %v", err)
	}
	return true
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
	now := s.now()
	wakeKicked := hasWakeKick(s.Store)
	if wakeKicked {
		logx.Infof("uni", "moodle: вейкворд на паре — внеочередной синк Moodle для %d аккаунтов", len(accs))
		defer consumeWakeKick(s.Store)
	}
	for _, acc := range accs {
		if ctx.Err() != nil {
			return
		}
		kickedByUser := consumeKick(s.Store, acc.TelegramID)
		if !kickedByUser && !wakeKicked && !s.moodleDue(acc, now) {
			continue
		}
		s.mu.Lock()
		last := s.lastFull[acc.TelegramID]
		s.mu.Unlock()
		full := kickedByUser || wakeKicked || now.Sub(last) >= moodleFull
		trigger := "по таймеру, раз в 30 мин"
		switch {
		case kickedByUser:
			trigger = "пинок kick (панель или бот)"
		case wakeKicked:
			trigger = "вейкворд на паре (wake_kick)"
		case full:
			trigger = "по таймеру, полный обход раз в 6 ч"
		}
		// Ошибку SyncMoodle уже записал с контекстом (tg, что делали).
		_ = s.SyncMoodle(withTrigger(ctx, trigger), acc, full)
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
