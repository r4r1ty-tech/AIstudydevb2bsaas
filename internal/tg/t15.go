package tg

import (
	"context"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

func (b *Bot) t15Loop(ctx context.Context) {
	b.tickT15()
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.tickT15()
		}
	}
}

func (b *Bot) tickT15() {
	if !b.t15Mu.TryLock() {
		return
	}
	defer b.t15Mu.Unlock()

	now := b.now()
	until := now.Add(15 * time.Minute)
	lessons, err := b.st.UpcomingOnline(now, until)
	if err != nil {
		return
	}
	users, err := b.st.ListUsers()
	if err != nil {
		return
	}

	for _, lesson := range lessons {
		if !lesson.Online || !lesson.Begin.After(now) || lesson.Begin.Sub(now) > 15*time.Minute {
			continue
		}
		for i := range users {
			u := users[i]
			if !u.Active(now) || !lesson.MatchesSubgroup(u.Subgroup) {
				continue
			}
			intent, err := b.st.GetIntent(u.TelegramID, lesson.ID)
			if err != nil || intent != nil {
				continue
			}
			if err := b.sendT15Card(u, lesson); err != nil {
				continue
			}
			if err := b.st.PutIntent(model.JoinIntent{
				TelegramID: u.TelegramID,
				LessonID:   lesson.ID,
				Decision:   model.JoinPending,
				AskedAt:    now,
			}); err != nil {
				continue
			}
			_ = b.st.AddEvent(model.Event{
				At:         now,
				Type:       model.EventT15,
				TelegramID: u.TelegramID,
				LessonID:   lesson.ID,
				Message:    lesson.Discipline,
			})
			b.rememberT15(u.TelegramID, lesson.ID)
		}
	}
}

func (b *Bot) sendT15Card(u model.User, lesson model.Lesson) error {
	hasLink := strings.TrimSpace(b.lookupBBB(lesson.ID)) != ""
	text := formatT15Card(lesson, b.now(), b.loc, hasLink)
	mk := t15Keyboard(lesson.ID)
	return b.send(u.TelegramID, text, &gotgbot.SendMessageOpts{ReplyMarkup: mk})
}
