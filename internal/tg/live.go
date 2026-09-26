package tg

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
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
	logx.Debugf("tg", "liveLoop: start")
	b.tickLive()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			logx.Debugf("tg", "liveLoop: ctx done")
			return
		case <-ticker.C:
			b.tickLive()
		}
	}
}

func (b *Bot) tickLive() {
	list, err := b.st.ListPresence()
	if err != nil {
		logx.Errorf("tg", "tickLive: list presence: %v", err)
		return
	}
	logx.Debugf("tg", "tickLive: presence=%d", len(list))
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
		logx.Infof("tg", "live card gone tg=%d", tgID)
		b.clearLiveCard(tgID, "Вышел из комнаты.")
	}
	b.syncTestLive()
}

func (b *Bot) syncLiveCard(p model.Presence, now time.Time) {
	logx.Debugf("tg", "syncLiveCard: tg=%d lesson=%d state=%s", p.TelegramID, p.LessonID, p.State)
	lesson, err := b.st.LessonByID(p.LessonID)
	if err != nil || lesson == nil {
		logx.Warnf("tg", "syncLiveCard: lesson tg=%d lesson=%d err=%v", p.TelegramID, p.LessonID, err)
		return
	}
	u, err := b.st.GetUser(p.TelegramID)
	if err != nil || u == nil {
		logx.Warnf("tg", "syncLiveCard: user tg=%d err=%v", p.TelegramID, err)
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
			ParseMode:          htmlMode,
			ChatId:             prev.chatID,
			MessageId:          prev.messageID,
			Text:               text,
			ReplyMarkup:        mk,
			LinkPreviewOptions: &gotgbot.LinkPreviewOptions{IsDisabled: true},
		})
		if err == nil {
			logx.Debugf("tg", "syncLiveCard: edited tg=%d msg=%d lesson=%d", p.TelegramID, prev.messageID, p.LessonID)
			b.mu.Lock()
			b.live[p.TelegramID] = liveSnap{
				lessonID: p.LessonID, state: p.State, url: url, fio: fio, leftKey: left,
				messageID: prev.messageID, chatID: prev.chatID,
			}
			b.mu.Unlock()
			return
		}
		logx.Warnf("tg", "syncLiveCard: edit failed tg=%d msg=%d: %v", p.TelegramID, prev.messageID, err)
	}

	msg, err := b.api.SendMessage(p.TelegramID, text, &gotgbot.SendMessageOpts{
		ParseMode:          htmlMode,
		ReplyMarkup:        mk,
		LinkPreviewOptions: &gotgbot.LinkPreviewOptions{IsDisabled: true},
	})
	if err != nil || msg == nil {
		logx.Errorf("tg", "syncLiveCard: send tg=%d lesson=%d: %v", p.TelegramID, p.LessonID, err)
		return
	}
	logx.Infof("tg", "live card sent tg=%d lesson=%d state=%s msg=%d", p.TelegramID, p.LessonID, p.State, msg.MessageId)
	b.mu.Lock()
	b.live[p.TelegramID] = liveSnap{
		lessonID: p.LessonID, state: p.State, url: url, fio: fio, leftKey: left,
		messageID: msg.MessageId, chatID: p.TelegramID,
	}
	b.mu.Unlock()
}

func (b *Bot) clearLiveCard(telegramID int64, text string) {
	logx.Debugf("tg", "clearLiveCard: tg=%d text=%q", telegramID, text)
	b.mu.Lock()
	prev, ok := b.live[telegramID]
	delete(b.live, telegramID)
	b.mu.Unlock()
	if !ok || prev.messageID == 0 {
		return
	}
	_, _, err := b.api.EditMessageText(&gotgbot.EditMessageTextOpts{
		ParseMode:          htmlMode,
		ChatId:             prev.chatID,
		MessageId:          prev.messageID,
		Text:               text,
		ReplyMarkup:        gotgbot.InlineKeyboardMarkup{},
		LinkPreviewOptions: &gotgbot.LinkPreviewOptions{IsDisabled: true},
	})
	if err != nil {
		logx.Errorf("tg", "clearLiveCard: edit tg=%d msg=%d: %v", telegramID, prev.messageID, err)
		return
	}
	logx.Debugf("tg", "clearLiveCard: cleared tg=%d msg=%d", telegramID, prev.messageID)
}

func formatLiveCard(l model.Lesson, url, fio, state string, now time.Time, loc *time.Location) string {
	logx.Debugf("tg", "formatLiveCard: lesson=%d sub=%d state=%s", l.ID, l.Subgroup, state)
	if loc == nil {
		loc = time.Local
	}
	var b strings.Builder
	b.WriteString("<b>Сейчас на паре</b>\n\n")
	b.WriteString("Предмет: ")
	b.WriteString(bold(dashOr(l.Discipline)))
	b.WriteString("\nСсылка: ")
	if strings.TrimSpace(url) == "" {
		b.WriteString("нет")
	} else {
		b.WriteString(hlink(strings.TrimSpace(url), strings.TrimSpace(url)))
	}
	b.WriteString("\nОсталось: ")
	b.WriteString(remainPhrase(now, l.Finish))
	if !l.Finish.IsZero() {
		b.WriteString(" (до ")
		b.WriteString(l.Finish.In(loc).Format("15:04"))
		b.WriteString(")")
	}
	b.WriteString("\nСтатус: ")
	b.WriteString(bold(presenceLabel(state)))
	b.WriteString("\nИмя в BBB: ")
	if strings.TrimSpace(fio) == "" {
		b.WriteString("не задано")
	} else {
		b.WriteString(bold(fio))
	}
	return b.String()
}

func presenceLabel(state string) string {
	logx.Debugf("tg", "presenceLabel: %q", state)
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
	logx.Debugf("tg", "remainPhrase: now=%s finish=%s", now.Format(time.RFC3339), finish.Format(time.RFC3339))
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
	logx.Debugf("tg", "leaveKeyboard: lesson=%d", lessonID)
	return gotgbot.InlineKeyboardMarkup{
		InlineKeyboard: [][]gotgbot.InlineKeyboardButton{{
			{Text: "Отключиться", CallbackData: leaveCallbackData(lessonID)},
		}},
	}
}

func leaveCallbackData(lessonID int64) string {
	logx.Debugf("tg", "leaveCallbackData: lesson=%d", lessonID)
	return "x:" + strconv.FormatInt(lessonID, 10)
}

func parseLeaveCallback(data string) (lessonID int64, ok bool) {
	logx.Debugf("tg", "parseLeaveCallback: data=%q", data)
	parts := strings.Split(data, ":")
	if len(parts) != 2 || parts[0] != "x" {
		return 0, false
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id == 0 {
		logx.Debugf("tg", "parseLeaveCallback: bad id err=%v", err)
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
	logx.Debugf("tg", "testLiveKey: status=%s want=%s mode=%s", j.Status, j.Want, j.Mode)
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
		logx.Errorf("tg", "syncTestLive: get test join: %v", err)
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
			ParseMode:          htmlMode,
			ChatId:             prev.chatID,
			MessageId:          prev.messageID,
			Text:               text,
			ReplyMarkup:        mk,
			LinkPreviewOptions: &gotgbot.LinkPreviewOptions{IsDisabled: true},
		})
		if err == nil {
			logx.Debugf("tg", "syncTestLive: edited msg=%d status=%s", prev.messageID, j.Status)
			b.rememberTestLive(prev.chatID, prev.messageID, j)
			return
		}
		logx.Warnf("tg", "syncTestLive: edit msg=%d: %v", prev.messageID, err)
	}

	msg, err := b.api.SendMessage(admin, text, &gotgbot.SendMessageOpts{
		ParseMode:          htmlMode,
		ReplyMarkup:        mk,
		LinkPreviewOptions: &gotgbot.LinkPreviewOptions{IsDisabled: true},
	})
	if err != nil || msg == nil {
		logx.Errorf("tg", "syncTestLive: send admin=%d: %v", admin, err)
		return
	}
	logx.Infof("tg", "test live card sent admin=%d msg=%d status=%s", admin, msg.MessageId, j.Status)
	b.rememberTestLive(admin, msg.MessageId, j)
}

func (b *Bot) rememberTestLive(chatID, messageID int64, j model.TestJoin) {
	logx.Debugf("tg", "rememberTestLive: chat=%d msg=%d status=%s", chatID, messageID, j.Status)
	b.mu.Lock()
	b.testLive = &testLiveSnap{key: testLiveKey(j), messageID: messageID, chatID: chatID}
	b.mu.Unlock()
}

func (b *Bot) clearTestLive(text string) {
	logx.Debugf("tg", "clearTestLive: text=%q", text)
	b.mu.Lock()
	prev := b.testLive
	b.testLive = nil
	b.mu.Unlock()
	if prev == nil || prev.messageID == 0 || text == "" {
		return
	}
	_, _, err := b.api.EditMessageText(&gotgbot.EditMessageTextOpts{
		ParseMode:          htmlMode,
		ChatId:             prev.chatID,
		MessageId:          prev.messageID,
		Text:               text,
		ReplyMarkup:        gotgbot.InlineKeyboardMarkup{},
		LinkPreviewOptions: &gotgbot.LinkPreviewOptions{IsDisabled: true},
	})
	if err != nil {
		logx.Errorf("tg", "clearTestLive: edit msg=%d: %v", prev.messageID, err)
		return
	}
	logx.Debugf("tg", "clearTestLive: cleared msg=%d", prev.messageID)
}
