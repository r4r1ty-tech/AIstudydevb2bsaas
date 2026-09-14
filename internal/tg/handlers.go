package tg

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

const (
	introText   = "Этот бот заходит на онлайн-лекции вместо тебя: в BBB в списке будет твоё ФИО, как в журнале. На паре слушает вейкворды и пишет в личку. Это не публичный сервис — только наша группа.\n\nКак тебя записать в BBB? Строка как в журнале (Фамилия Имя Отчество)."
	askFIO      = "Как тебя записать в BBB? Строка как в журнале (Фамилия Имя Отчество)."
	askSub      = "Какая подгруппа? 1 или 2, по умолчанию 1."
	whitelisted = "Ты в вайтлисте, профиль есть."
	askBBBLink  = "Ссылка нужна на ЭТУ пару, не на предмет на семестр. Пришли bbb.ssau.ru/b/… — без неё не зайду. Тестовую комнату сюда не кидай: только /test."
)

func (b *Bot) onStart(_ *gotgbot.Bot, ctx *ext.Context) error {
	from := b.allowed(ctx)
	if from == nil {
		return nil
	}
	chatID := from.Id
	if ctx.EffectiveChat != nil {
		chatID = ctx.EffectiveChat.Id
	}

	u, err := b.st.GetUser(from.Id)
	if err != nil {
		return err
	}
	if u == nil {
		u = &model.User{
			TelegramID: from.Id,
			Username:   from.Username,
			FirstName:  from.FirstName,
			LastName:   from.LastName,
			Enabled:    true,
			Subgroup:   1,
			Onboarded:  false,
			CreatedAt:  b.now(),
		}
		if err := b.st.UpsertUser(u); err != nil {
			return err
		}
		return b.send(chatID, introText, nil)
	}

	u.Username = from.Username
	u.FirstName = from.FirstName
	u.LastName = from.LastName
	if err := b.st.UpsertUser(u); err != nil {
		return err
	}

	if u.Onboarded {
		return b.send(chatID, whitelisted, b.adminStartOpts(from.Id))
	}
	if strings.TrimSpace(u.FIO) == "" {
		return b.send(chatID, askFIO, nil)
	}
	return b.send(chatID, askSub, nil)
}

func (b *Bot) adminStartOpts(userID int64) *gotgbot.SendMessageOpts {
	if !b.cfg.IsAdmin(userID) || b.cfg.WebAppURL == "" {
		return nil
	}
	b.setAdminMenuButton()
	mk := b.panelMarkup()
	if mk == nil {
		return nil
	}
	return &gotgbot.SendMessageOpts{ReplyMarkup: *mk}
}

func (b *Bot) onPanel(_ *gotgbot.Bot, ctx *ext.Context) error {
	from := b.allowed(ctx)
	if from == nil || !b.cfg.IsAdmin(from.Id) {
		return nil
	}
	chatID := from.Id
	if ctx.EffectiveChat != nil {
		chatID = ctx.EffectiveChat.Id
	}
	if b.cfg.WebAppURL == "" {
		return b.send(chatID, "Поставь WEBAPP_PUBLIC_URL в .env", nil)
	}
	mk := b.panelMarkup()
	b.setAdminMenuButton()
	return b.send(chatID, "Панель", &gotgbot.SendMessageOpts{ReplyMarkup: *mk})
}

func (b *Bot) onText(_ *gotgbot.Bot, ctx *ext.Context) error {
	from := b.allowed(ctx)
	if from == nil || ctx.EffectiveMessage == nil {
		return nil
	}
	text := strings.TrimSpace(ctx.EffectiveMessage.GetText())
	chatID := ctx.EffectiveMessage.Chat.Id

	if url := extractBBBURL(text); url != "" {
		return b.handleBBBURL(from.Id, chatID, url)
	}
	if isCommandText(text) {
		return nil
	}

	u, err := b.st.GetUser(from.Id)
	if err != nil {
		return err
	}
	if u == nil || u.Onboarded {
		return nil
	}
	return b.continueOnboarding(u, chatID, text)
}

func (b *Bot) continueOnboarding(u *model.User, chatID int64, text string) error {
	if strings.TrimSpace(u.FIO) == "" {
		fio := strings.TrimSpace(text)
		if fio == "" {
			return b.send(chatID, askFIO, nil)
		}
		if err := b.st.SetFIO(u.TelegramID, fio); err != nil {
			return err
		}
		return b.send(chatID, askSub, nil)
	}

	n, ok := parseSubgroup(text)
	if !ok {
		return b.send(chatID, askSub, nil)
	}
	if err := b.st.SetSubgroup(u.TelegramID, n); err != nil {
		return err
	}
	fresh, err := b.st.GetUser(u.TelegramID)
	if err != nil {
		return err
	}
	if fresh == nil {
		fresh = u
		fresh.Subgroup = n
	}
	fresh.Onboarded = true
	if err := b.st.UpsertUser(fresh); err != nil {
		return err
	}
	if err := b.st.AddEvent(model.Event{
		At:         b.now(),
		Type:       model.EventOnboard,
		TelegramID: u.TelegramID,
		Message:    fresh.FIO,
	}); err != nil {
		return err
	}
	return b.send(chatID, fmt.Sprintf("Ок, %s, подгруппа %d.", fresh.FIO, n), nil)
}

func (b *Bot) handleBBBURL(userID, chatID int64, url string) error {
	u, err := b.st.GetUser(userID)
	if err != nil {
		return err
	}
	if u == nil {
		u = &model.User{TelegramID: userID, Subgroup: 1}
	}
	lesson := b.pickBBBTarget(u, b.now())
	if lesson == nil {
		return b.send(chatID, "Не понял, к какой паре ссылка. Пришли bbb.ssau.ru/b/… ближе к паре или после карточки за 15 мин.", nil)
	}
	// Только lesson:{id}. Старые ключи «предмет на семестр» воркер больше не читает.
	if err := b.st.SetLessonBBB(lesson.ID, url); err != nil {
		return err
	}
	if err := b.st.AddEvent(model.Event{
		At:         b.now(),
		Type:       "bbb",
		TelegramID: userID,
		LessonID:   lesson.ID,
		Message:    url,
	}); err != nil {
		return err
	}
	return b.send(chatID, fmt.Sprintf(
		"Привязал ссылку к «%s» %s %s.\nНа другую пару этот URL не пойдёт — перед следующей кинь заново.",
		lesson.Discipline, lesson.SlotLabel(), lesson.Date,
	), nil)
}

func (b *Bot) lookupBBB(lessonID int64) string {
	if b == nil || b.st == nil {
		return ""
	}
	return b.st.GetLessonBBB(lessonID)
}

func (b *Bot) pickBBBTarget(u *model.User, now time.Time) *model.Lesson {
	lessons, err := b.st.ListLessons()
	if err != nil {
		lessons = nil
	}
	// Ответ на T-15 привязываем к той паре, пока она не закончилась.
	if lid := b.lastT15Lesson(u.TelegramID); lid != 0 {
		if l, err := b.st.LessonByID(lid); err == nil && l != nil && !now.After(l.Finish) {
			return l
		}
	}
	hasLink := func(lessonID int64) bool {
		return strings.TrimSpace(b.lookupBBB(lessonID)) != ""
	}
	intents := make(map[int64]time.Time)
	for _, l := range lessons {
		in, err := b.st.GetIntent(u.TelegramID, l.ID)
		if err == nil && in != nil {
			intents[l.ID] = in.AskedAt
		}
	}
	if lid := b.lastT15Lesson(u.TelegramID); lid != 0 {
		if _, ok := intents[lid]; !ok {
			intents[lid] = now
		}
	}
	return pickLessonForBBB(now, u.Subgroup, lessons, hasLink, intents)
}

func (b *Bot) onLeaveCallback(bot *gotgbot.Bot, ctx *ext.Context) error {
	from := b.allowed(ctx)
	if from == nil || ctx.CallbackQuery == nil {
		return nil
	}
	lessonID, ok := parseLeaveCallback(ctx.CallbackQuery.Data)
	if !ok {
		_, _ = ctx.CallbackQuery.Answer(bot, nil)
		return nil
	}
	now := b.now()
	err := b.st.SetIntentDecision(from.Id, lessonID, model.JoinNo)
	if errors.Is(err, sql.ErrNoRows) {
		err = b.st.PutIntent(model.JoinIntent{
			TelegramID: from.Id,
			LessonID:   lessonID,
			Decision:   model.JoinNo,
			AskedAt:    now,
			DecidedAt:  &now,
		})
	}
	if err != nil {
		_, _ = ctx.CallbackQuery.Answer(bot, &gotgbot.AnswerCallbackQueryOpts{Text: "не вышло", ShowAlert: true})
		return err
	}
	_ = b.st.AddEvent(model.Event{
		At: now, Type: model.EventLeave,
		TelegramID: from.Id, LessonID: lessonID, Message: "кнопка",
	})
	_, _ = ctx.CallbackQuery.Answer(bot, &gotgbot.AnswerCallbackQueryOpts{Text: "выхожу"})
	if ctx.CallbackQuery.Message != nil {
		_, _, _ = ctx.CallbackQuery.Message.EditText(bot, &gotgbot.EditMessageTextOpts{
			Text:        "Выхожу из комнаты… Через несколько секунд отключусь.",
			ReplyMarkup: gotgbot.InlineKeyboardMarkup{InlineKeyboard: [][]gotgbot.InlineKeyboardButton{}},
		})
	}
	b.mu.Lock()
	delete(b.live, from.Id)
	b.mu.Unlock()
	return nil
}

func (b *Bot) onJoinCallback(bot *gotgbot.Bot, ctx *ext.Context) error {
	from := b.allowed(ctx)
	if from == nil || ctx.CallbackQuery == nil {
		return nil
	}
	_, _ = ctx.CallbackQuery.Answer(bot, nil)

	yes, lessonID, ok := parseJoinCallback(ctx.CallbackQuery.Data)
	if !ok {
		return nil
	}

	decision := model.JoinNo
	reply := "ок, сегодня пропуск"
	if yes {
		decision = model.JoinYes
		reply = "ок, захожу"
	}

	now := b.now()
	err := b.st.SetIntentDecision(from.Id, lessonID, decision)
	if errors.Is(err, sql.ErrNoRows) {
		err = b.st.PutIntent(model.JoinIntent{
			TelegramID: from.Id,
			LessonID:   lessonID,
			Decision:   decision,
			AskedAt:    now,
			DecidedAt:  &now,
		})
	}
	if err != nil {
		return err
	}

	if !yes {
		if err := b.st.AddEvent(model.Event{
			At:         now,
			Type:       model.EventSkip,
			TelegramID: from.Id,
			LessonID:   lessonID,
			Message:    "пропуск",
		}); err != nil {
			return err
		}
	}

	if ctx.CallbackQuery.Message != nil {
		_, _, _ = ctx.CallbackQuery.Message.EditText(bot, &gotgbot.EditMessageTextOpts{
			Text:        reply,
			ReplyMarkup: gotgbot.InlineKeyboardMarkup{InlineKeyboard: [][]gotgbot.InlineKeyboardButton{}},
		})
	}
	return nil
}
