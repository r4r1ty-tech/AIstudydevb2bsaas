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

// Kick просит rasp пересобрать задания пользователя на ближайшем тике.
func Kick(st *store.Store, telegramID int64) error {
	return st.SetSetting(KickKey(telegramID), "1")
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

// Run — фоновый цикл Moodle: раз в 30 минут (полный обход курсов раз в 6 часов)
// или сразу по kick. Уведомления ЛК не берём — там рассылки без пользы.
func (s *Syncer) Run(ctx context.Context) {
	t := time.NewTicker(tick)
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
	for _, acc := range accs {
		if ctx.Err() != nil {
			return
		}
		kicked := consumeKick(s.Store, acc.TelegramID)
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
