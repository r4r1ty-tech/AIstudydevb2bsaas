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
		return b.sendAskFIO(chatID, from.Id, introText)
	}

	u.Username = from.Username
	u.FirstName = from.FirstName
	u.LastName = from.LastName
	if err := b.st.UpsertUser(u); err != nil {
		return err
	}

	if u.Onboarded {
		b.clearAwait(from.Id)
		if b.cfg.IsAdmin(from.Id) {
			b.setAdminMenuButton()
		}
		return b.sendToday(u, chatID)
	}
	if strings.TrimSpace(u.FIO) == "" {
		return b.sendAskFIO(chatID, from.Id, askFIO)
	}
	if u.OnboardStage == model.StageWords {
		return b.sendInline(chatID, askWords, skipWordsKeyboard())
	}
	return b.sendInline(chatID, askSub, subgroupKeyboard())
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
	return b.send(chatID, "Панель — кнопка под этим сообщением. В браузере не открывай: Telegram тогда не даёт сессию.", &gotgbot.SendMessageOpts{ReplyMarkup: *mk})
}

func (b *Bot) onHelp(_ *gotgbot.Bot, ctx *ext.Context) error {
	from := b.allowed(ctx)
	if from == nil || ctx.EffectiveMessage == nil {
		return nil
	}
	b.clearAwait(from.Id)
	return b.sendMain(ctx.EffectiveMessage.Chat.Id, helpText)
}

func (b *Bot) onToday(_ *gotgbot.Bot, ctx *ext.Context) error {
	from := b.allowed(ctx)
	if from == nil || ctx.EffectiveMessage == nil {
		return nil
	}
	u, err := b.st.GetUser(from.Id)
	if err != nil {
		return err
	}
	if u == nil {
		return b.sendAskFIO(ctx.EffectiveMessage.Chat.Id, from.Id, askFIO)
	}
	b.clearAwait(from.Id)
	return b.sendToday(u, ctx.EffectiveMessage.Chat.Id)
}

func (b *Bot) onSettings(_ *gotgbot.Bot, ctx *ext.Context) error {
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
		return b.sendAskFIO(chatID, from.Id, askFIO)
	}
	if !u.Onboarded {
		return b.continueOnboarding(u, chatID, "")
	}
	b.clearAwait(from.Id)
	return b.sendSettings(u, chatID)
}

func (b *Bot) onText(_ *gotgbot.Bot, ctx *ext.Context) error {
	from := b.allowed(ctx)
	if from == nil || ctx.EffectiveMessage == nil {
		return nil
	}
	text := strings.TrimSpace(ctx.EffectiveMessage.GetText())
	chatID := ctx.EffectiveMessage.Chat.Id

	if url := extractBBBURL(text); url != "" {
		b.clearAwait(from.Id)
		return b.handleBBBURL(from.Id, chatID, url)
	}
	if isCommandText(text) {
		return nil
	}

	u, err := b.st.GetUser(from.Id)
	if err != nil {
		return err
	}
	if u == nil || !u.Onboarded {
		if u == nil {
			return b.sendAskFIO(chatID, from.Id, askFIO)
		}
		if isMenuLabel(text) {
			return b.continueOnboarding(u, chatID, "")
		}
		return b.continueOnboarding(u, chatID, text)
	}

	if kind := b.peekAwait(from.Id); kind != awaitNone && !isMenuLabel(text) {
		return b.handleAwait(u, chatID, text, kind)
	}
	b.clearAwait(from.Id)

	switch text {
	case btnToday:
		return b.sendToday(u, chatID)
	case btnLinks:
		return b.sendLinks(u, chatID)
	case btnSettings:
		return b.sendSettings(u, chatID)
	case btnWords:
		return b.sendAskWords(u, chatID)
	case btnHelp:
		return b.sendMain(chatID, helpText)
	default:
		return b.sendMain(chatID, fallbackText)
	}
}

func (b *Bot) sendAskFIO(chatID, userID int64, text string) error {
	b.setAwait(userID, awaitFIO)
	if strings.TrimSpace(text) == "" {
		text = askFIO
	}
	return b.send(chatID, text, &gotgbot.SendMessageOpts{ReplyMarkup: fioForceReply()})
}

func (b *Bot) sendAskWords(u *model.User, chatID int64) error {
	b.setAwait(u.TelegramID, awaitWords)
	return b.send(chatID, formatWakeReply(*u)+"\n\n"+askWordsNext, &gotgbot.SendMessageOpts{
		ReplyMarkup: wordsForceReply(),
	})
}

func (b *Bot) sendSettings(u *model.User, chatID int64) error {
	return b.send(chatID, formatSettings(*u), &gotgbot.SendMessageOpts{
		ReplyMarkup: settingsKeyboard(u.Subgroup),
	})
}

func (b *Bot) handleAwait(u *model.User, chatID int64, text string, kind awaitKind) error {
	switch kind {
	case awaitFIO:
		fio := strings.TrimSpace(text)
		if fio == "" {
			return b.sendAskFIO(chatID, u.TelegramID, askFIO)
		}
		if err := b.st.SetFIO(u.TelegramID, fio); err != nil {
			return err
		}
		b.clearAwait(u.TelegramID)
		u.FIO = fio
		return b.sendMain(chatID, formatSettings(*u))
	case awaitWords:
		var words []string
		if !isClearWords(text) && !model.SkipWakeWords(text) {
			words = model.ParseWakeWords(text)
		}
		if err := b.st.SetExtraWords(u.TelegramID, words); err != nil {
			return err
		}
		b.clearAwait(u.TelegramID)
		u.ExtraWords = words
		return b.sendMain(chatID, formatSettings(*u))
	default:
		b.clearAwait(u.TelegramID)
		return b.sendMain(chatID, fallbackText)
	}
}

func (b *Bot) continueOnboarding(u *model.User, chatID int64, text string) error {
	if strings.TrimSpace(u.FIO) == "" {
		fio := strings.TrimSpace(text)
		if fio == "" {
			return b.sendAskFIO(chatID, u.TelegramID, askFIO)
		}
		b.clearAwait(u.TelegramID)
		if err := b.st.SetFIO(u.TelegramID, fio); err != nil {
			return err
		}
		if err := b.st.SetOnboardStage(u.TelegramID, model.StageSub); err != nil {
			return err
		}
		return b.sendInline(chatID, askSub, subgroupKeyboard())
	}

	if u.OnboardStage == model.StageWords {
		if strings.TrimSpace(text) == "" {
			return b.sendInline(chatID, askWords, skipWordsKeyboard())
		}
		return b.finishOnboarding(u, chatID, text)
	}

	n, ok := parseSubgroup(text)
	if !ok {
		return b.sendInline(chatID, askSub, subgroupKeyboard())
	}
	return b.setSubgroupAndAskWords(u, chatID, n)
}

func (b *Bot) setSubgroupAndAskWords(u *model.User, chatID int64, n int) error {
	if err := b.st.SetSubgroup(u.TelegramID, n); err != nil {
		return err
	}
	if err := b.st.SetOnboardStage(u.TelegramID, model.StageWords); err != nil {
		return err
	}
	u.Subgroup = n
	u.OnboardStage = model.StageWords
	return b.sendInline(chatID, askWords, skipWordsKeyboard())
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
	b.clearAwait(u.TelegramID)
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
	return b.sendMain(chatID, formatOnboardDone(*fresh))
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
		return b.sendAskFIO(chatID, from.Id, askFIO)
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
	if payload == "" && u.Onboarded {
		return b.sendAskWords(u, chatID)
	}
	if u.Onboarded {
		return b.sendMain(chatID, formatSettings(*u))
	}
	return b.send(chatID, formatWakeReply(*u)+"\n\n"+formatWordsHint(), nil)
}

func (b *Bot) onLink(_ *gotgbot.Bot, ctx *ext.Context) error {
	from := b.allowed(ctx)
	if from == nil || ctx.EffectiveMessage == nil {
		return nil
	}
	u, err := b.st.GetUser(from.Id)
	if err != nil {
		return err
	}
	if u == nil {
		u = &model.User{TelegramID: from.Id, Subgroup: 1}
	}
	return b.sendLinks(u, ctx.EffectiveMessage.Chat.Id)
}

func answerToast(bot *gotgbot.Bot, ctx *ext.Context, text string) {
	if bot == nil || ctx == nil || ctx.CallbackQuery == nil {
		return
	}
	opts := &gotgbot.AnswerCallbackQueryOpts{}
	if strings.TrimSpace(text) != "" {
		opts.Text = text
	}
	_, _ = ctx.CallbackQuery.Answer(bot, opts)
}

func (b *Bot) sendLinks(u *model.User, chatID int64) error {
	text, err := b.formatLinkReply(u)
	if err != nil {
		return err
	}
	if u != nil && u.Onboarded {
		return b.sendMain(chatID, text)
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
	upcoming := make([]string, 0)
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
			mark = "ссылка есть"
		}
		upcoming = append(upcoming, fmt.Sprintf("• %s · %s — %s", l.Discipline, l.SlotLabel(), mark))
	}
	urls := make([]string, 0, len(saved))
	for _, link := range saved {
		urls = append(urls, "• "+link.URL)
	}
	return formatLinkList(upcoming, urls), nil
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
		if u.Onboarded {
			return b.sendMain(chatID, noBBBTarget)
		}
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
	text := formatSavedLink(lesson.Discipline)
	if u.Onboarded {
		return b.sendMain(chatID, text)
	}
	return b.send(chatID, text, nil)
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

func (b *Bot) sendToday(u *model.User, chatID int64) error {
	text, err := b.formatTodayReply(u)
	if err != nil {
		return err
	}
	return b.sendMain(chatID, text)
}

func (b *Bot) formatTodayReply(u *model.User) (string, error) {
	lessons, err := b.st.ListLessons()
	if err != nil {
		return "", err
	}
	pres, err := b.st.ListPresence()
	if err != nil {
		return "", err
	}
	byLesson := map[int64]model.Presence{}
	for _, p := range pres {
		if p.TelegramID == u.TelegramID {
			byLesson[p.LessonID] = p
		}
	}
	now := b.now()
	day := now.Format("2006-01-02")
	rows := make([]todayRow, 0)
	for _, l := range lessons {
		if !l.Online || !l.MatchesSubgroup(u.Subgroup) {
			continue
		}
		if l.Date != day && (l.Begin.IsZero() || l.Begin.In(b.loc).Format("2006-01-02") != day) {
			continue
		}
		if !l.Finish.IsZero() && !now.Before(l.Finish) {
			continue
		}
		row := todayRow{
			Lesson:  l,
			HasLink: b.lookupBBB(l.Discipline, l.Teacher) != "",
		}
		if p, ok := byLesson[l.ID]; ok {
			row.Presence = p.State
			row.Detail = p.Message
		}
		if in, err := b.st.GetIntent(u.TelegramID, l.ID); err == nil && in != nil {
			row.Decision = in.Decision
		}
		rows = append(rows, row)
	}
	return formatToday(now, b.loc, u.FIO, rows), nil
}

func (b *Bot) onOnboardCallback(bot *gotgbot.Bot, ctx *ext.Context) error {
	if ctx == nil || ctx.CallbackQuery == nil {
		return nil
	}
	from := b.allowed(ctx)
	if from == nil {
		answerToast(bot, ctx, "")
		return nil
	}
	kind, n, ok := parseOnboardCallback(ctx.CallbackQuery.Data)
	if !ok {
		answerToast(bot, ctx, "")
		return nil
	}
	toast := "Пропуск"
	if kind == "sub" {
		toast = fmt.Sprintf("Подгруппа %d", n)
	}
	answerToast(bot, ctx, toast)
	u, err := b.st.GetUser(from.Id)
	if err != nil || u == nil || u.Onboarded {
		return err
	}
	chatID := from.Id
	if ctx.EffectiveChat != nil {
		chatID = ctx.EffectiveChat.Id
	}

	switch kind {
	case "sub":
		if strings.TrimSpace(u.FIO) == "" {
			return b.sendAskFIO(chatID, from.Id, askFIO)
		}
		if ctx.CallbackQuery.Message != nil {
			_, _, _ = ctx.CallbackQuery.Message.EditText(bot, &gotgbot.EditMessageTextOpts{
				Text:        fmt.Sprintf("Подгруппа %d", n),
				ReplyMarkup: emptyInline(),
			})
		}
		return b.setSubgroupAndAskWords(u, chatID, n)
	case "skipw":
		if ctx.CallbackQuery.Message != nil {
			_, _, _ = ctx.CallbackQuery.Message.EditText(bot, &gotgbot.EditMessageTextOpts{
				Text:        "Свои слова пропускаю.",
				ReplyMarkup: emptyInline(),
			})
		}
		return b.finishOnboarding(u, chatID, "-")
	}
	return nil
}

func (b *Bot) onSettingsCallback(bot *gotgbot.Bot, ctx *ext.Context) error {
	if ctx == nil || ctx.CallbackQuery == nil {
		return nil
	}
	from := b.allowed(ctx)
	if from == nil {
		answerToast(bot, ctx, "")
		return nil
	}
	kind, n, ok := parseSettingsCallback(ctx.CallbackQuery.Data)
	if !ok {
		answerToast(bot, ctx, "")
		return nil
	}
	u, err := b.st.GetUser(from.Id)
	if err != nil || u == nil {
		answerToast(bot, ctx, "")
		return err
	}
	chatID := from.Id
	if ctx.EffectiveChat != nil {
		chatID = ctx.EffectiveChat.Id
	}
	if !u.Onboarded {
		answerToast(bot, ctx, "")
		return b.continueOnboarding(u, chatID, "")
	}

	switch kind {
	case "fio":
		answerToast(bot, ctx, "ФИО")
		return b.sendAskFIO(chatID, from.Id, askFIO)
	case "words":
		answerToast(bot, ctx, "Слова")
		return b.sendAskWords(u, chatID)
	case "sub":
		answerToast(bot, ctx, fmt.Sprintf("Подгруппа %d", n))
		if err := b.st.SetSubgroup(from.Id, n); err != nil {
			return err
		}
		u.Subgroup = n
		if ctx.CallbackQuery.Message != nil {
			_, _, _ = ctx.CallbackQuery.Message.EditText(bot, &gotgbot.EditMessageTextOpts{
				Text:        formatSettings(*u),
				ReplyMarkup: settingsKeyboard(n),
			})
		}
		return nil
	}
	answerToast(bot, ctx, "")
	return nil
}

func (b *Bot) onJoinCallback(bot *gotgbot.Bot, ctx *ext.Context) error {
	if ctx == nil || ctx.CallbackQuery == nil {
		return nil
	}
	from := b.allowed(ctx)
	if from == nil {
		answerToast(bot, ctx, "")
		return nil
	}
	yes, lessonID, ok := parseJoinCallback(ctx.CallbackQuery.Data)
	if !ok {
		answerToast(bot, ctx, "")
		return nil
	}
	toast := "Пропускаю"
	if yes {
		toast = "Захожу"
	}
	answerToast(bot, ctx, toast)

	decision := model.JoinNo
	if yes {
		decision = model.JoinYes
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

	lesson, _ := b.st.LessonByID(lessonID)
	u, _ := b.st.GetUser(from.Id)
	fio := ""
	if u != nil {
		fio = u.FIO
	}
	hasLink := false
	if lesson != nil {
		hasLink = b.lookupBBB(lesson.Discipline, lesson.Teacher) != ""
	}
	reply := formatSkipAck(lesson, b.loc)
	if yes {
		reply = formatJoinAck(lesson, b.loc, fio, hasLink)
	}

	if ctx.CallbackQuery.Message != nil {
		_, _, _ = ctx.CallbackQuery.Message.EditText(bot, &gotgbot.EditMessageTextOpts{
			Text:        reply,
			ReplyMarkup: t15Keyboard(lessonID),
		})
	}
	return nil
}
