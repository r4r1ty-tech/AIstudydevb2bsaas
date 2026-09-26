package tg

import (
	"context"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

func (b *Bot) t15Loop(ctx context.Context) {
	logx.Debugf("tg", "t15Loop: start")
	b.tickT15()
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			logx.Debugf("tg", "t15Loop: ctx done")
			return
		case <-ticker.C:
			b.tickT15()
		}
	}
}

func (b *Bot) tickT15() {
	if !b.t15Mu.TryLock() {
		logx.Debugf("tg", "tickT15: already running")
		return
	}
	defer b.t15Mu.Unlock()

	now := b.now()
	until := now.Add(15 * time.Minute)
	lessons, err := b.st.UpcomingOnline(now, until)
	if err != nil {
		logx.Errorf("tg", "tickT15: upcoming: %v", err)
		return
	}
	users, err := b.st.ListUsers()
	if err != nil {
		logx.Errorf("tg", "tickT15: list users: %v", err)
		return
	}
	logx.Debugf("tg", "tickT15: now=%s lessons=%d users=%d", now.Format(time.RFC3339), len(lessons), len(users))

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
				if err != nil {
					logx.Errorf("tg", "tickT15: get intent tg=%d lesson=%d: %v", u.TelegramID, lesson.ID, err)
				}
				continue
			}
			if err := b.sendT15Card(u, lesson); err != nil {
				logx.Warnf("tg", "t15 card tg=%d lesson=%d: %v", u.TelegramID, lesson.ID, err)
				continue
			}
			if err := b.st.PutIntent(model.JoinIntent{
				TelegramID: u.TelegramID,
				LessonID:   lesson.ID,
				Decision:   model.JoinPending,
				AskedAt:    now,
			}); err != nil {
				logx.Errorf("tg", "t15 intent tg=%d lesson=%d: %v", u.TelegramID, lesson.ID, err)
				continue
			}
			logx.Infof("tg", "t15 sent tg=%d lesson=%d %q", u.TelegramID, lesson.ID, lesson.Discipline)
			if err := b.st.AddEvent(model.Event{
				At:         now,
				Type:       model.EventT15,
				TelegramID: u.TelegramID,
				LessonID:   lesson.ID,
				Message:    lesson.Discipline,
			}); err != nil {
				logx.Warnf("tg", "t15 event tg=%d lesson=%d: %v", u.TelegramID, lesson.ID, err)
			}
			b.rememberT15(u.TelegramID, lesson.ID)
		}
	}
}

func (b *Bot) sendT15Card(u model.User, lesson model.Lesson) error {
	hasLink := strings.TrimSpace(b.lookupBBB(lesson.ID)) != ""
	logx.Debugf("tg", "sendT15Card: tg=%d lesson=%d hasLink=%v", u.TelegramID, lesson.ID, hasLink)
	text := formatT15Card(lesson, b.now(), b.loc, hasLink)
	mk := t15Keyboard(lesson.ID)
	return b.send(u.TelegramID, text, &gotgbot.SendMessageOpts{ReplyMarkup: mk})
}
