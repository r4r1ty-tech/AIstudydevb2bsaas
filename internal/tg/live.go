package tg

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

type liveSnap struct {
	lessonID  int64
	state     string
	url       string
	fio       string
	leftKey   string
	messageID int64
	chatID    int64
}

func (b *Bot) liveLoop(ctx context.Context) {
	b.tickLive()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.tickLive()
		}
	}
}

func (b *Bot) tickLive() {
	list, err := b.st.ListPresence()
	if err != nil {
		return
	}
	now := b.now()
	alive := make(map[int64]struct{}, len(list))
	for _, p := range list {
		if p.State != model.PresenceLobby && p.State != model.PresenceRoom {
			continue
		}
		alive[p.TelegramID] = struct{}{}
		b.syncLiveCard(p, now)
	}

	b.mu.Lock()
	var gone []int64
	for tgID := range b.live {
		if _, ok := alive[tgID]; !ok {
			gone = append(gone, tgID)
		}
	}
	b.mu.Unlock()
	for _, tgID := range gone {
		b.clearLiveCard(tgID, "Вышел из комнаты.")
	}
	b.syncTestLive()
}

func (b *Bot) syncLiveCard(p model.Presence, now time.Time) {
	lesson, err := b.st.LessonByID(p.LessonID)
	if err != nil || lesson == nil {
		return
	}
	u, err := b.st.GetUser(p.TelegramID)
	if err != nil || u == nil {
		return
	}
	url := b.lookupBBB(lesson.ID)
	fio := strings.TrimSpace(u.FIO)
	left := remainPhrase(now, lesson.Finish)
	text := formatLiveCard(*lesson, url, fio, p.State, now, b.loc)
	mk := leaveKeyboard(lesson.ID)

	b.mu.Lock()
	prev, ok := b.live[p.TelegramID]
	b.mu.Unlock()

	if ok && prev.messageID != 0 &&
		prev.lessonID == p.LessonID && prev.state == p.State &&
		prev.url == url && prev.fio == fio && prev.leftKey == left {
		return
	}

	if ok && prev.messageID != 0 {
		_, _, err := b.api.EditMessageText(&gotgbot.EditMessageTextOpts{
			ChatId:      prev.chatID,
			MessageId:   prev.messageID,
			Text:        text,
			ReplyMarkup: mk,
			LinkPreviewOptions: &gotgbot.LinkPreviewOptions{IsDisabled: true},
		})
		if err == nil {
			b.mu.Lock()
			b.live[p.TelegramID] = liveSnap{
				lessonID: p.LessonID, state: p.State, url: url, fio: fio, leftKey: left,
				messageID: prev.messageID, chatID: prev.chatID,
			}
			b.mu.Unlock()
			return
		}
	}

	msg, err := b.api.SendMessage(p.TelegramID, text, &gotgbot.SendMessageOpts{
		ReplyMarkup:        mk,
		LinkPreviewOptions: &gotgbot.LinkPreviewOptions{IsDisabled: true},
	})
	if err != nil || msg == nil {
		return
	}
	b.mu.Lock()
	b.live[p.TelegramID] = liveSnap{
		lessonID: p.LessonID, state: p.State, url: url, fio: fio, leftKey: left,
		messageID: msg.MessageId, chatID: p.TelegramID,
	}
	b.mu.Unlock()
}

func (b *Bot) clearLiveCard(telegramID int64, text string) {
	b.mu.Lock()
	prev, ok := b.live[telegramID]
	delete(b.live, telegramID)
	b.mu.Unlock()
	if !ok || prev.messageID == 0 {
		return
	}
	_, _, _ = b.api.EditMessageText(&gotgbot.EditMessageTextOpts{
		ChatId:      prev.chatID,
		MessageId:   prev.messageID,
		Text:        text,
		ReplyMarkup: gotgbot.InlineKeyboardMarkup{},
		LinkPreviewOptions: &gotgbot.LinkPreviewOptions{IsDisabled: true},
	})
}

func formatLiveCard(l model.Lesson, url, fio, state string, now time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.Local
	}
	var b strings.Builder
	b.WriteString("Сейчас на паре\n\n")
	b.WriteString("Предмет: ")
	if strings.TrimSpace(l.Discipline) == "" {
		b.WriteString("—")
	} else {
		b.WriteString(strings.TrimSpace(l.Discipline))
	}
	b.WriteString("\nСсылка: ")
	if strings.TrimSpace(url) == "" {
		b.WriteString("нет")
	} else {
		b.WriteString(strings.TrimSpace(url))
	}
	b.WriteString("\nОсталось: ")
	b.WriteString(remainPhrase(now, l.Finish))
	if !l.Finish.IsZero() {
		b.WriteString(" (до ")
		b.WriteString(l.Finish.In(loc).Format("15:04"))
		b.WriteString(")")
	}
	b.WriteString("\nСтатус: ")
	b.WriteString(presenceLabel(state))
	b.WriteString("\nИмя в BBB: ")
	if strings.TrimSpace(fio) == "" {
		b.WriteString("не задано")
	} else {
		b.WriteString(strings.TrimSpace(fio))
	}
	return b.String()
}

func presenceLabel(state string) string {
	switch state {
	case model.PresenceLobby:
		return "лобби — ждём модератора"
	case model.PresenceRoom:
		return "в комнате"
	case model.PresenceError:
		return "ошибка"
	default:
		return state
	}
}

func remainPhrase(now, finish time.Time) string {
	if finish.IsZero() {
		return "неизвестно"
	}
	d := finish.Sub(now)
	if d <= 0 {
		return "заканчивается"
	}
	mins := int((d + time.Minute/2) / time.Minute)
	if mins < 1 {
		mins = 1
	}
	if mins < 60 {
		return fmt.Sprintf("%d мин", mins)
	}
	h := mins / 60
	m := mins % 60
	if m == 0 {
		return fmt.Sprintf("%d ч", h)
	}
	return fmt.Sprintf("%d ч %d мин", h, m)
}

func leaveKeyboard(lessonID int64) gotgbot.InlineKeyboardMarkup {
	return gotgbot.InlineKeyboardMarkup{
		InlineKeyboard: [][]gotgbot.InlineKeyboardButton{{
			{Text: "Отключиться", CallbackData: leaveCallbackData(lessonID)},
		}},
	}
}

func leaveCallbackData(lessonID int64) string {
	return "x:" + strconv.FormatInt(lessonID, 10)
}

func parseLeaveCallback(data string) (lessonID int64, ok bool) {
	parts := strings.Split(data, ":")
	if len(parts) != 2 || parts[0] != "x" {
		return 0, false
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id == 0 {
		return 0, false
	}
	return id, true
}

type testLiveSnap struct {
	key       string
	messageID int64
	chatID    int64
}

func testLiveKey(j model.TestJoin) string {
	return j.Status + "|" + strings.TrimSpace(j.URL) + "|" + j.GuestName() + "|" + j.Want + "|" + j.Mode
}

func (b *Bot) syncTestLive() {
	if b == nil || b.st == nil || b.cfg == nil {
		return
	}
	admin := b.cfg.AdminID
	if admin == 0 {
		return
	}
	j, err := b.st.GetTestJoin()
	if err != nil {
		return
	}
	active := j.Want != model.TestWantOff &&
		(j.Status == model.TestJoining || j.Status == model.TestLobby || j.Status == model.TestRoom)
	if !active {
		b.clearTestLive("Тест: вышел из комнаты.")
		return
	}
	text := formatTestCard(j)
	mk := b.testMarkup(j)
	key := testLiveKey(j)

	b.mu.Lock()
	prev := b.testLive
	b.mu.Unlock()
	if prev != nil && prev.messageID != 0 && prev.key == key {
		return
	}

	if prev != nil && prev.messageID != 0 {
		_, _, err := b.api.EditMessageText(&gotgbot.EditMessageTextOpts{
			ChatId:      prev.chatID,
			MessageId:   prev.messageID,
			Text:        text,
			ReplyMarkup: mk,
			LinkPreviewOptions: &gotgbot.LinkPreviewOptions{IsDisabled: true},
		})
		if err == nil {
			b.rememberTestLive(prev.chatID, prev.messageID, j)
			return
		}
	}

	msg, err := b.api.SendMessage(admin, text, &gotgbot.SendMessageOpts{
		ReplyMarkup:        mk,
		LinkPreviewOptions: &gotgbot.LinkPreviewOptions{IsDisabled: true},
	})
	if err != nil || msg == nil {
		return
	}
	b.rememberTestLive(admin, msg.MessageId, j)
}

func (b *Bot) rememberTestLive(chatID, messageID int64, j model.TestJoin) {
	b.mu.Lock()
	b.testLive = &testLiveSnap{key: testLiveKey(j), messageID: messageID, chatID: chatID}
	b.mu.Unlock()
}

func (b *Bot) clearTestLive(text string) {
	b.mu.Lock()
	prev := b.testLive
	b.testLive = nil
	b.mu.Unlock()
	if prev == nil || prev.messageID == 0 || text == "" {
		return
	}
	_, _, _ = b.api.EditMessageText(&gotgbot.EditMessageTextOpts{
		ChatId:      prev.chatID,
		MessageId:   prev.messageID,
		Text:        text,
		ReplyMarkup: gotgbot.InlineKeyboardMarkup{},
		LinkPreviewOptions: &gotgbot.LinkPreviewOptions{IsDisabled: true},
	})
}

