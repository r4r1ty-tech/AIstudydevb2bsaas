package uni

import (
	"context"
	"fmt"
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
	return st.SetSetting(KickKey(telegramID), "1")
}

// WakeKick просит rasp немедленно проверить и синхронизировать Moodle при обнаружении вейкворда.
func WakeKick(st *store.Store) error {
	return st.SetSetting(WakeKickKey, "1")
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
		defer consumeWakeKick(s.Store)
	}
	for _, acc := range accs {
		if ctx.Err() != nil {
			return
		}
		kicked := consumeKick(s.Store, acc.TelegramID) || wakeKicked
		if !kicked && !s.moodleDue(acc, now) {
			continue
		}
		s.mu.Lock()
		last := s.lastFull[acc.TelegramID]
		s.mu.Unlock()
		full := kicked || now.Sub(last) >= moodleFull
		if err := s.SyncMoodle(ctx, acc, full); err != nil {
			logx.Warnf("uni", "moodle tg=%d: %v", acc.TelegramID, err)
		}
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
