package tg

import (
	"context"
	"fmt"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/uni"
)

// Напоминания о сроках Moodle — уведомление, за сутки и за 3 часа до срока.

var (
	deadlineTick = 5 * time.Minute
	remindStages = []struct {
		name string
		left time.Duration
	}{{"3h", 3 * time.Hour}, {"24h", 24 * time.Hour}}
)

func (b *Bot) deadlineLoop(ctx context.Context) {
	t := time.NewTicker(deadlineTick)
	defer t.Stop()
	for {
		b.remindDeadlines(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// remindDeadlines — по одному разу на этап для несданного с близким сроком.
func (b *Bot) remindDeadlines(ctx context.Context) {
	accs, err := b.st.ListAccounts(model.AccountMoodle)
	if err != nil {
		logx.Warnf("tg", "remind: accounts: %v", err)
		return
	}
	now := b.now()
	for _, acc := range accs {
		if ctx.Err() != nil {
			return
		}
		items, err := b.st.ListMoodleItems(acc.TelegramID)
		if err != nil {
			continue
		}
		for _, it := range items {
			if it.Finished() || it.Due == nil || !it.Due.After(now) {
				continue
			}
			left := it.Due.Sub(now)
			for _, st := range remindStages {
				if left > st.left {
					continue
				}
				key := fmt.Sprintf("dl_sent:%d:%d:%s:%d", acc.TelegramID, it.CMID, st.name, it.Due.Unix())
				if v, ok, _ := b.st.GetSetting(key); ok && v != "" {
					break
				}
				text := "⏰ <b>Срок " + uni.Until(*it.Due, now) + "</b>\n" + uni.FormatItem(it, now, b.loc)
				if err := b.send(acc.TelegramID, text, &gotgbot.SendMessageOpts{
					LinkPreviewOptions: &gotgbot.LinkPreviewOptions{IsDisabled: true},
				}); err != nil {
					logx.Warnf("tg", "remind tg=%d cmid=%d: %v", acc.TelegramID, it.CMID, err)
					break
				}
				// Отметить этот этап и более ранние (24 ч после 3 ч уже не нужен).
				for _, all := range remindStages {
					if all.left < st.left {
						continue
					}
					_ = b.st.SetSetting(fmt.Sprintf("dl_sent:%d:%d:%s:%d", acc.TelegramID, it.CMID, all.name, it.Due.Unix()), "1")
				}
				break
			}
		}
	}
}
