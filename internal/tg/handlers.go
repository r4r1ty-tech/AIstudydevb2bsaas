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
	askWords    = "Свои вейкворды через запятую. Общие уже есть: тест, контрольная, мудл, moodle + фамилия. Или «-» если своих не надо. Потом можно /words."
	whitelisted = "Ты в вайтлисте. /words — вейкворды, /link — ссылки BBB. Кинь bbb.ssau.ru/b/… сюда, привяжу к паре."
	askBBBLink  = "Кинь ссылку на подключение (bbb.ssau.ru/b/…). Без неё не зайду."
	doneHint    = "На паре пиши Да/Нет. Ссылку bbb.ssau.ru/b/… кидай сюда. /words — вейкворды, /link — какие ссылки уже есть."
	noBBBTarget = "не понял к какой паре. Ближайших online без ссылки нет. Напиши /link и кинь bbb.ssau.ru/b/… ещё раз ближе к паре."
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
	if u.OnboardStage == model.StageWords {
		return b.send(chatID, askWords, nil)
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
	return b.send(chatID, "Жми кнопку «Панель» под этим сообщением. Ссылку в браузере не открывай.", &gotgbot.SendMessageOpts{ReplyMarkup: *mk})
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
		if err := b.st.SetOnboardStage(u.TelegramID, model.StageSub); err != nil {
			return err
		}
		return b.send(chatID, askSub, nil)
	}

	if u.OnboardStage == model.StageWords {
		return b.finishOnboarding(u, chatID, text)
	}

	n, ok := parseSubgroup(text)
	if !ok {
		return b.send(chatID, askSub, nil)
	}
	if err := b.st.SetSubgroup(u.TelegramID, n); err != nil {
		return err
	}
	if err := b.st.SetOnboardStage(u.TelegramID, model.StageWords); err != nil {
		return err
	}
	return b.send(chatID, askWords, nil)
}

func (b *Bot) finishOnboarding(u *model.User, chatID int64, text string) error {
	words := []string(nil)
	if !model.SkipWakeWords(text) {
		words = model.ParseWakeWords(text)
	}
	if err := b.st.SetExtraWords(u.TelegramID, words); err != nil {
		return err
	}
	if err := b.st.SetOnboardStage(u.TelegramID, model.StageDone); err != nil {
		return err
	}
	fresh, err := b.st.GetUser(u.TelegramID)
	if err != nil {
		return err
	}
	if fresh == nil {
		fresh = u
		fresh.ExtraWords = words
	}
	fresh.Onboarded = true
	fresh.OnboardStage = model.StageDone
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
	return b.send(chatID, fmt.Sprintf("Ок, %s, подгруппа %d.\n%s\n\n%s", fresh.FIO, fresh.Subgroup, formatWakeReply(*fresh), doneHint), nil)
}

func formatWakeReply(u model.User) string {
	list := u.WakeList()
	extra := model.FormatWakeWords(u.ExtraWords)
	if extra == "" {
		extra = "нет"
	}
	return fmt.Sprintf("Слушаю: %s\nСвои: %s", strings.Join(list, ", "), extra)
}

func commandPayload(text string) string {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return text
	}
	i := strings.IndexAny(text, " \n")
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(text[i+1:])
}

func isClearWords(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "clear", "очистить", "-", "—":
		return true
	default:
		return false
	}
}

func (b *Bot) onWords(_ *gotgbot.Bot, ctx *ext.Context) error {
	from := b.allowed(ctx)
	if from == nil || ctx.EffectiveMessage == nil {
		return nil
	}
	chatID := ctx.EffectiveMessage.Chat.Id
	u, err := b.st.GetUser(from.Id)
	if err != nil {
		return err
	}
	if u == nil {
		return b.send(chatID, askFIO, nil)
	}

	payload := commandPayload(ctx.EffectiveMessage.GetText())
	if payload != "" {
		var words []string
		if !isClearWords(payload) {
			words = model.MergeWakeWords(u.ExtraWords, model.ParseWakeWords(payload))
		}
		if err := b.st.SetExtraWords(u.TelegramID, words); err != nil {
			return err
		}
		u.ExtraWords = words
	}

	if !u.Onboarded && u.OnboardStage == model.StageWords && payload != "" {
		return b.finishOnboarding(u, chatID, payload)
	}
	return b.send(chatID, formatWakeReply(*u)+"\n\nДобавить: /words лаба, зачёт\nСбросить свои: /words clear", nil)
}

func (b *Bot) onLink(_ *gotgbot.Bot, ctx *ext.Context) error {
	from := b.allowed(ctx)
	if from == nil || ctx.EffectiveMessage == nil {
		return nil
	}
	chatID := ctx.EffectiveMessage.Chat.Id
	u, err := b.st.GetUser(from.Id)
	if err != nil {
		return err
	}
	if u == nil {
		u = &model.User{TelegramID: from.Id, Subgroup: 1}
	}
	text, err := b.formatLinkReply(u)
	if err != nil {
		return err
	}
	return b.send(chatID, text, nil)
}

func (b *Bot) formatLinkReply(u *model.User) (string, error) {
	lessons, err := b.st.ListLessons()
	if err != nil {
		return "", err
	}
	saved, err := b.st.ListBBB()
	if err != nil {
		return "", err
	}
	now := b.now()
	var bld strings.Builder
	bld.WriteString("Ближайшие online:\n")
	n := 0
	for _, l := range lessons {
		if !l.Online || !l.MatchesSubgroup(u.Subgroup) || !l.Begin.After(now) {
			continue
		}
		n++
		if n > 8 {
			break
		}
		mark := "нет ссылки"
		if url := b.lookupBBB(l.Discipline, l.Teacher); url != "" {
			mark = url
		}
		fmt.Fprintf(&bld, "• %s %s — %s\n", l.Discipline, l.SlotLabel(), mark)
	}
	if n == 0 {
		bld.WriteString("нет ближайших\n")
	}
	if len(saved) > 0 {
		bld.WriteString("\nЗапомнил:\n")
		for _, link := range saved {
			fmt.Fprintf(&bld, "• %s\n", link.URL)
		}
	}
	bld.WriteString("\nКинь bbb.ssau.ru/b/… сюда — привяжу к ближайшей паре без ссылки.")
	return bld.String(), nil
}

func (b *Bot) lookupBBB(discipline, teacher string) string {
	link, err := b.st.GetBBB(model.BBBKey(b.cfg.GroupID, discipline, teacher))
	if err != nil || link == nil {
		return ""
	}
	return strings.TrimSpace(link.URL)
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
		return b.send(chatID, noBBBTarget, nil)
	}
	key := model.BBBKey(b.cfg.GroupID, lesson.Discipline, lesson.Teacher)
	if err := b.st.SetBBB(key, url); err != nil {
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
	return b.send(chatID, fmt.Sprintf("запомнил ссылку на %s", lesson.Discipline), nil)
}

func (b *Bot) pickBBBTarget(u *model.User, now time.Time) *model.Lesson {
	lessons, err := b.st.ListLessons()
	if err != nil {
		lessons = nil
	}
	hasLink := func(discipline, teacher string) bool {
		link, err := b.st.GetBBB(model.BBBKey(b.cfg.GroupID, discipline, teacher))
		return err == nil && link != nil && strings.TrimSpace(link.URL) != ""
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
	if got := pickLessonForBBB(now, u.Subgroup, lessons, hasLink, intents); got != nil {
		return got
	}
	if lid := b.lastT15Lesson(u.TelegramID); lid != 0 {
		l, err := b.st.LessonByID(lid)
		if err == nil && l != nil {
			return l
		}
	}
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
