package tg

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

func (b *Bot) onStart(_ *gotgbot.Bot, ctx *ext.Context) error {
	logx.Debugf("tg", "onStart: enter")
	from := b.allowed(ctx)
	if from == nil {
		return nil
	}
	chatID := from.Id
	if ctx.EffectiveChat != nil {
		chatID = ctx.EffectiveChat.Id
	}
	logx.Debugf("tg", "onStart: tg=%d chat=%d username=%q", from.Id, chatID, from.Username)

	u, err := b.st.GetUser(from.Id)
	if err != nil {
		logx.Errorf("tg", "onStart: get user tg=%d: %v", from.Id, err)
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
			logx.Errorf("tg", "onStart: upsert new user tg=%d: %v", from.Id, err)
			return err
		}
		logx.Infof("tg", "onboard created tg=%d username=%q", from.Id, from.Username)
		return b.sendAskFIO(chatID, from.Id, introText)
	}

	u.Username = from.Username
	u.FirstName = from.FirstName
	u.LastName = from.LastName
	if err := b.st.UpsertUser(u); err != nil {
		logx.Errorf("tg", "onStart: upsert user tg=%d: %v", from.Id, err)
		return err
	}

	if u.Onboarded {
		logx.Debugf("tg", "onStart: onboarded tg=%d", from.Id)
		b.clearAwait(from.Id)
		if b.cfg.IsAdmin(from.Id) {
			b.setAdminMenuButton()
		}
		return b.sendToday(u, chatID)
	}
	if strings.TrimSpace(u.FIO) == "" {
		logx.Debugf("tg", "onStart: awaits fio tg=%d", from.Id)
		return b.sendAskFIO(chatID, from.Id, askFIO)
	}
	if u.OnboardStage == model.StageWords {
		logx.Debugf("tg", "onStart: awaits words tg=%d", from.Id)
		return b.sendInline(chatID, askWords, skipWordsKeyboard())
	}
	logx.Debugf("tg", "onStart: awaits subgroup tg=%d", from.Id)
	return b.sendInline(chatID, askSub, subgroupKeyboard())
}

func (b *Bot) onPanel(_ *gotgbot.Bot, ctx *ext.Context) error {
	logx.Debugf("tg", "onPanel: enter")
	from := b.allowed(ctx)
	if from == nil || !b.cfg.IsAdmin(from.Id) {
		logx.Warnf("tg", "onPanel: not admin")
		return nil
	}
	chatID := from.Id
	if ctx.EffectiveChat != nil {
		chatID = ctx.EffectiveChat.Id
	}
	logx.Debugf("tg", "onPanel: tg=%d chat=%d", from.Id, chatID)
	if b.webAppURL() == "" {
		logx.Warnf("tg", "onPanel: no webapp url")
		return b.send(chatID, "Поставь WEBAPP_PUBLIC_URL в .env", nil)
	}
	mk := b.panelMarkup()
	b.setAdminMenuButton()
	logx.Infof("tg", "panel opened tg=%d", from.Id)
	return b.send(chatID, "Панель — кнопка под этим сообщением. В браузере не открывай: Telegram тогда не даёт сессию.", &gotgbot.SendMessageOpts{ReplyMarkup: *mk})
}

func (b *Bot) onCancel(_ *gotgbot.Bot, ctx *ext.Context) error {
	logx.Debugf("tg", "onCancel: enter")
	from := b.allowed(ctx)
	if from == nil || ctx.EffectiveMessage == nil {
		logx.Warnf("tg", "onCancel: rejected")
		return nil
	}
	logx.Debugf("tg", "onCancel: tg=%d chat=%d", from.Id, ctx.EffectiveMessage.Chat.Id)
	b.clearAwait(from.Id)
	return b.sendMain(ctx.EffectiveMessage.Chat.Id, cancelText)
}

func (b *Bot) onHelp(_ *gotgbot.Bot, ctx *ext.Context) error {
	logx.Debugf("tg", "onHelp: enter")
	from := b.allowed(ctx)
	if from == nil || ctx.EffectiveMessage == nil {
		logx.Warnf("tg", "onHelp: rejected")
		return nil
	}
	logx.Debugf("tg", "onHelp: tg=%d chat=%d", from.Id, ctx.EffectiveMessage.Chat.Id)
	b.clearAwait(from.Id)
	return b.sendMain(ctx.EffectiveMessage.Chat.Id, helpText)
}

func (b *Bot) onToday(_ *gotgbot.Bot, ctx *ext.Context) error {
	logx.Debugf("tg", "onToday: enter")
	from := b.allowed(ctx)
	if from == nil || ctx.EffectiveMessage == nil {
		logx.Warnf("tg", "onToday: rejected")
		return nil
	}
	logx.Debugf("tg", "onToday: tg=%d chat=%d", from.Id, ctx.EffectiveMessage.Chat.Id)
	u, err := b.st.GetUser(from.Id)
	if err != nil {
		logx.Errorf("tg", "onToday: get user tg=%d: %v", from.Id, err)
		return err
	}
	if u == nil {
		logx.Debugf("tg", "onToday: no user tg=%d", from.Id)
		return b.sendAskFIO(ctx.EffectiveMessage.Chat.Id, from.Id, askFIO)
	}
	b.clearAwait(from.Id)
	return b.sendToday(u, ctx.EffectiveMessage.Chat.Id)
}

func (b *Bot) onSettings(_ *gotgbot.Bot, ctx *ext.Context) error {
	logx.Debugf("tg", "onSettings: enter")
	from := b.allowed(ctx)
	if from == nil || ctx.EffectiveMessage == nil {
		logx.Warnf("tg", "onSettings: rejected")
		return nil
	}
	chatID := ctx.EffectiveMessage.Chat.Id
	logx.Debugf("tg", "onSettings: tg=%d chat=%d", from.Id, chatID)
	u, err := b.st.GetUser(from.Id)
	if err != nil {
		logx.Errorf("tg", "onSettings: get user tg=%d: %v", from.Id, err)
		return err
	}
	if u == nil {
		logx.Debugf("tg", "onSettings: no user tg=%d", from.Id)
		return b.sendAskFIO(chatID, from.Id, askFIO)
	}
	if !u.Onboarded {
		logx.Debugf("tg", "onSettings: continue onboarding tg=%d stage=%d", from.Id, u.OnboardStage)
		return b.continueOnboarding(u, chatID, "")
	}
	b.clearAwait(from.Id)
	return b.sendSettings(u, chatID)
}

func (b *Bot) onText(_ *gotgbot.Bot, ctx *ext.Context) error {
	logx.Debugf("tg", "onText: enter")
	from := b.allowed(ctx)
	if from == nil || ctx.EffectiveMessage == nil {
		logx.Warnf("tg", "onText: rejected")
		return nil
	}
	text := strings.TrimSpace(ctx.EffectiveMessage.GetText())
	chatID := ctx.EffectiveMessage.Chat.Id
	logx.Debugf("tg", "onText: tg=%d chat=%d text=%q", from.Id, chatID, text)

	if url := extractBBBURL(text); url != "" {
		if b.peekAwait(from.Id) == awaitTestURL {
			logx.Debugf("tg", "onText: test url tg=%d", from.Id)
			b.clearAwait(from.Id)
			return b.armTestURL(chatID, url, "")
		}
		b.clearAwait(from.Id)
		logx.Debugf("tg", "onText: bbb url tg=%d", from.Id)
		return b.handleBBBURL(from.Id, chatID, url)
	}
	if isCommandText(text) {
		if knownCommand(text) {
			logx.Debugf("tg", "onText: known command %q", text)
			return nil
		}
		logx.Debugf("tg", "onText: unknown command %q", text)
		return b.sendMain(chatID, fallbackText)
	}
	// Тест BBB — админская штука, онбординг для неё не нужен: иначе админ-не-
	// студент вводит имя гостя, а бот начинает спрашивать его ФИО.
	if kind := b.peekAwait(from.Id); b.cfg.IsAdmin(from.Id) && (kind == awaitTestName || kind == awaitTestURL) && !isMenuLabel(text) {
		logx.Debugf("tg", "onText: admin test await tg=%d kind=%d", from.Id, kind)
		return b.handleAwait(&model.User{TelegramID: from.Id}, chatID, text, kind)
	}

	u, err := b.st.GetUser(from.Id)
	if err != nil {
		logx.Errorf("tg", "onText: get user tg=%d: %v", from.Id, err)
		return err
	}
	if u == nil || !u.Onboarded {
		if u == nil {
			logx.Debugf("tg", "onText: no user tg=%d", from.Id)
			return b.sendAskFIO(chatID, from.Id, askFIO)
		}
		if isMenuLabel(text) {
			logx.Debugf("tg", "onText: onboarding menu tg=%d", from.Id)
			return b.continueOnboarding(u, chatID, "")
		}
		logx.Debugf("tg", "onText: onboarding input tg=%d", from.Id)
		return b.continueOnboarding(u, chatID, text)
	}

	if kind := b.peekAwait(from.Id); kind != awaitNone && !isMenuLabel(text) {
		logx.Debugf("tg", "onText: await tg=%d kind=%d", from.Id, kind)
		return b.handleAwait(u, chatID, text, kind)
	}
	b.clearAwait(from.Id)

	switch text {
	case btnToday, btnTodayOld:
		return b.sendToday(u, chatID)
	case btnNotes:
		return b.sendNotes(chatID)
	case btnLinks:
		return b.sendRooms(u, chatID)
	case btnSettings, btnSettingsOld:
		return b.sendSettings(u, chatID)
	case btnWords:
		return b.sendAskWords(u, chatID)
	case btnHelp:
		return b.sendMain(chatID, helpText)
	default:
		logx.Debugf("tg", "onText: fallback tg=%d text=%q", from.Id, text)
		return b.sendMain(chatID, fallbackText)
	}
}

func (b *Bot) sendAskFIO(chatID, userID int64, text string) error {
	logx.Debugf("tg", "sendAskFIO: chat=%d tg=%d", chatID, userID)
	b.setAwait(userID, awaitFIO)
	if strings.TrimSpace(text) == "" {
		text = askFIO
	}
	return b.send(chatID, text, &gotgbot.SendMessageOpts{ReplyMarkup: cancelKeyboard()})
}

func (b *Bot) sendAskWords(u *model.User, chatID int64) error {
	logx.Debugf("tg", "sendAskWords: tg=%d chat=%d", u.TelegramID, chatID)
	b.setAwait(u.TelegramID, awaitWords)
	return b.send(chatID, formatWakeReply(*u)+"\n\n"+askWordsNext, &gotgbot.SendMessageOpts{
		ParseMode:   htmlMode,
		ReplyMarkup: cancelKeyboard(),
	})
}

func (b *Bot) sendSettings(u *model.User, chatID int64) error {
	logx.Debugf("tg", "sendSettings: tg=%d chat=%d subgroup=%d", u.TelegramID, chatID, u.Subgroup)
	return b.send(chatID, formatSettings(*u), &gotgbot.SendMessageOpts{
		ParseMode:   htmlMode,
		ReplyMarkup: settingsKeyboard(u.Subgroup),
	})
}

func (b *Bot) handleAwait(u *model.User, chatID int64, text string, kind awaitKind) error {
	logx.Debugf("tg", "handleAwait: tg=%d chat=%d kind=%d text=%q", u.TelegramID, chatID, kind, text)
	switch kind {
	case awaitFIO:
		fio, ok := normalizeFIO(text)
		if !ok {
			logx.Warnf("tg", "handleAwait: bad fio tg=%d", u.TelegramID)
			return b.sendAskFIO(chatID, u.TelegramID, badFIO)
		}
		if err := b.st.SetFIO(u.TelegramID, fio); err != nil {
			logx.Errorf("tg", "handleAwait: set fio tg=%d: %v", u.TelegramID, err)
			return err
		}
		b.clearAwait(u.TelegramID)
		u.FIO = fio
		logx.Infof("tg", "fio saved tg=%d fio=%q", u.TelegramID, fio)
		return b.sendSettings(u, chatID)
	case awaitWords:
		var words []string
		if !isClearWords(text) && !model.SkipWakeWords(text) {
			words = model.ParseWakeWords(text)
		}
		if err := b.st.SetExtraWords(u.TelegramID, words); err != nil {
			logx.Errorf("tg", "handleAwait: set words tg=%d: %v", u.TelegramID, err)
			return err
		}
		b.clearAwait(u.TelegramID)
		u.ExtraWords = words
		logx.Infof("tg", "words saved tg=%d words=%v", u.TelegramID, words)
		return b.sendSettings(u, chatID)
	case awaitTestName:
		return b.setTestGuestName(chatID, text)
	case awaitTestURL:
		url := extractBBBURL(text)
		if url == "" {
			logx.Warnf("tg", "handleAwait: no test url tg=%d text=%q", u.TelegramID, text)
			return b.send(chatID, askTestURL, nil)
		}
		b.clearAwait(u.TelegramID)
		return b.armTestURL(chatID, url, "")
	default:
		logx.Debugf("tg", "handleAwait: default tg=%d", u.TelegramID)
		b.clearAwait(u.TelegramID)
		return b.sendMain(chatID, fallbackText)
	}
}

func (b *Bot) continueOnboarding(u *model.User, chatID int64, text string) error {
	logx.Debugf("tg", "continueOnboarding: tg=%d chat=%d stage=%d text=%q", u.TelegramID, chatID, u.OnboardStage, text)
	if strings.TrimSpace(u.FIO) == "" {
		if strings.TrimSpace(text) == "" {
			logx.Debugf("tg", "continueOnboarding: ask fio tg=%d", u.TelegramID)
			return b.sendAskFIO(chatID, u.TelegramID, askFIO)
		}
		fio, ok := normalizeFIO(text)
		if !ok {
			logx.Debugf("tg", "continueOnboarding: bad fio tg=%d", u.TelegramID)
			return b.sendAskFIO(chatID, u.TelegramID, badFIO)
		}
		b.clearAwait(u.TelegramID)
		if err := b.st.SetFIO(u.TelegramID, fio); err != nil {
			logx.Errorf("tg", "continueOnboarding: set fio tg=%d: %v", u.TelegramID, err)
			return err
		}
		if err := b.st.SetOnboardStage(u.TelegramID, model.StageSub); err != nil {
			logx.Errorf("tg", "continueOnboarding: set stage sub tg=%d: %v", u.TelegramID, err)
			return err
		}
		logx.Infof("tg", "onboard fio tg=%d", u.TelegramID)
		return b.sendInline(chatID, askSub, subgroupKeyboard())
	}

	if u.OnboardStage == model.StageWords {
		if strings.TrimSpace(text) == "" {
			logx.Debugf("tg", "continueOnboarding: ask words tg=%d", u.TelegramID)
			return b.sendInline(chatID, askWords, skipWordsKeyboard())
		}
		logx.Debugf("tg", "continueOnboarding: finish words tg=%d", u.TelegramID)
		return b.finishOnboarding(u, chatID, text)
	}

	if strings.TrimSpace(text) == "" {
		return b.sendInline(chatID, askSub, subgroupKeyboard())
	}
	n, ok := parseSubgroup(text)
	if !ok {
		logx.Warnf("tg", "continueOnboarding: bad subgroup tg=%d text=%q", u.TelegramID, text)
		return b.sendInline(chatID, askSub, subgroupKeyboard())
	}
	return b.setSubgroupAndAskWords(u, chatID, n)
}

func (b *Bot) setSubgroupAndAskWords(u *model.User, chatID int64, n int) error {
	logx.Debugf("tg", "setSubgroupAndAskWords: tg=%d chat=%d sub=%d", u.TelegramID, chatID, n)
	if err := b.st.SetSubgroup(u.TelegramID, n); err != nil {
		logx.Errorf("tg", "setSubgroupAndAskWords: set subgroup tg=%d: %v", u.TelegramID, err)
		return err
	}
	if err := b.st.SetOnboardStage(u.TelegramID, model.StageWords); err != nil {
		logx.Errorf("tg", "setSubgroupAndAskWords: set stage words tg=%d: %v", u.TelegramID, err)
		return err
	}
	u.Subgroup = n
	u.OnboardStage = model.StageWords
	logx.Infof("tg", "onboard subgroup tg=%d sub=%d", u.TelegramID, n)
	return b.sendInline(chatID, askWords, skipWordsKeyboard())
}

func (b *Bot) finishOnboarding(u *model.User, chatID int64, text string) error {
	logx.Debugf("tg", "finishOnboarding: tg=%d chat=%d text=%q", u.TelegramID, chatID, text)
	words := []string(nil)
	if !model.SkipWakeWords(text) {
		words = model.ParseWakeWords(text)
	}
	if err := b.st.SetExtraWords(u.TelegramID, words); err != nil {
		logx.Errorf("tg", "finishOnboarding: set words tg=%d: %v", u.TelegramID, err)
		return err
	}
	if err := b.st.SetOnboardStage(u.TelegramID, model.StageDone); err != nil {
		logx.Errorf("tg", "finishOnboarding: set stage done tg=%d: %v", u.TelegramID, err)
		return err
	}
	fresh, err := b.st.GetUser(u.TelegramID)
	if err != nil {
		logx.Errorf("tg", "finishOnboarding: get user tg=%d: %v", u.TelegramID, err)
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
		logx.Errorf("tg", "finishOnboarding: upsert user tg=%d: %v", u.TelegramID, err)
		return err
	}
	if err := b.st.AddEvent(model.Event{
		At:         b.now(),
		Type:       model.EventOnboard,
		TelegramID: u.TelegramID,
		Message:    fresh.FIO,
	}); err != nil {
		logx.Errorf("tg", "finishOnboarding: add event tg=%d: %v", u.TelegramID, err)
		return err
	}
	logx.Infof("tg", "onboard done tg=%d fio=%q subgroup=%d", fresh.TelegramID, fresh.FIO, fresh.Subgroup)
	return b.sendMain(chatID, formatOnboardDone(*fresh))
}

func commandPayload(text string) string {
	logx.Debugf("tg", "commandPayload: %q", strings.TrimSpace(text))
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
	ok := false
	switch s {
	case "clear", "очистить", "-", "—":
		ok = true
	default:
		ok = false
	}
	logx.Debugf("tg", "isClearWords: %q -> %v", s, ok)
	return ok
}

func (b *Bot) onWords(_ *gotgbot.Bot, ctx *ext.Context) error {
	logx.Debugf("tg", "onWords: enter")
	from := b.allowed(ctx)
	if from == nil || ctx.EffectiveMessage == nil {
		logx.Warnf("tg", "onWords: rejected")
		return nil
	}
	chatID := ctx.EffectiveMessage.Chat.Id
	logx.Debugf("tg", "onWords: tg=%d chat=%d", from.Id, chatID)
	u, err := b.st.GetUser(from.Id)
	if err != nil {
		logx.Errorf("tg", "onWords: get user tg=%d: %v", from.Id, err)
		return err
	}
	if u == nil {
		logx.Debugf("tg", "onWords: no user tg=%d", from.Id)
		return b.sendAskFIO(chatID, from.Id, askFIO)
	}

	payload := commandPayload(ctx.EffectiveMessage.GetText())
	if payload != "" {
		var words []string
		if !isClearWords(payload) {
			words = model.MergeWakeWords(u.ExtraWords, model.ParseWakeWords(payload))
		}
		if err := b.st.SetExtraWords(u.TelegramID, words); err != nil {
			logx.Errorf("tg", "onWords: set words tg=%d: %v", u.TelegramID, err)
			return err
		}
		u.ExtraWords = words
		logx.Infof("tg", "words updated tg=%d words=%v", u.TelegramID, words)
	}

	if !u.Onboarded && u.OnboardStage == model.StageWords && payload != "" {
		return b.finishOnboarding(u, chatID, payload)
	}
	if payload == "" && u.Onboarded {
		return b.sendAskWords(u, chatID)
	}
	if u.Onboarded {
		return b.sendSettings(u, chatID)
	}
	return b.send(chatID, formatWakeReply(*u)+"\n\n"+formatWordsHint(), nil)
}

func (b *Bot) onLink(_ *gotgbot.Bot, ctx *ext.Context) error {
	logx.Debugf("tg", "onLink: enter")
	from := b.allowed(ctx)
	if from == nil || ctx.EffectiveMessage == nil {
		logx.Warnf("tg", "onLink: rejected")
		return nil
	}
	u, err := b.st.GetUser(from.Id)
	if err != nil {
		logx.Errorf("tg", "onLink: get user tg=%d: %v", from.Id, err)
		return err
	}
	if u == nil {
		u = &model.User{TelegramID: from.Id, Subgroup: 1}
	}
	logx.Debugf("tg", "onLink: tg=%d chat=%d", from.Id, ctx.EffectiveMessage.Chat.Id)
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
	if _, err := ctx.CallbackQuery.Answer(bot, opts); err != nil {
		logx.Warnf("tg", "answerToast: %q: %v", text, err)
		return
	}
	logx.Debugf("tg", "answerToast: %q", text)
}

func (b *Bot) sendLinks(u *model.User, chatID int64) error {
	logx.Debugf("tg", "sendLinks: tg=%d chat=%d", u.TelegramID, chatID)
	return b.sendRooms(u, chatID)
}

func (b *Bot) sendRooms(u *model.User, chatID int64) error {
	logx.Debugf("tg", "sendRooms: tg=%d chat=%d", u.TelegramID, chatID)
	text, err := b.formatLinkReply(u)
	if err != nil {
		logx.Errorf("tg", "sendRooms: format tg=%d: %v", u.TelegramID, err)
		return err
	}
	if u != nil && u.Onboarded {
		return b.send(chatID, text, &gotgbot.SendMessageOpts{ReplyMarkup: backToProfileKeyboard()})
	}
	return b.send(chatID, text, nil)
}

func (b *Bot) formatLinkReply(u *model.User) (string, error) {
	logx.Debugf("tg", "formatLinkReply: tg=%d", u.TelegramID)
	lessons, err := b.st.ListLessons()
	if err != nil {
		logx.Errorf("tg", "formatLinkReply: list lessons: %v", err)
		return "", err
	}
	saved, err := b.st.ListBBB()
	if err != nil {
		logx.Errorf("tg", "formatLinkReply: list bbb: %v", err)
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
		if url, own, err := b.st.LessonBBB(l.ID); err == nil && url != "" {
			mark = "ссылка есть"
			if !own {
				mark = "ссылка с прошлой недели"
			}
		}
		upcoming = append(upcoming, fmt.Sprintf("• %s · %s — %s", l.Discipline, l.SlotLabel(), mark))
	}
	urls := make([]string, 0, len(saved))
	for _, link := range saved {
		urls = append(urls, "• "+link.URL)
	}
	logx.Debugf("tg", "formatLinkReply: lessons=%d saved=%d upcoming=%d", len(lessons), len(saved), len(upcoming))
	return formatLinkList(upcoming, urls), nil
}

func (b *Bot) handleBBBURL(userID, chatID int64, url string) error {
	logx.Debugf("tg", "handleBBBURL: tg=%d chat=%d url=%s", userID, chatID, url)
	u, err := b.st.GetUser(userID)
	if err != nil {
		logx.Errorf("tg", "handleBBBURL: get user tg=%d: %v", userID, err)
		return err
	}
	if u == nil {
		u = &model.User{TelegramID: userID, Subgroup: 1}
	}
	lesson := b.pickBBBTarget(u, b.now())
	if lesson == nil {
		logx.Warnf("tg", "handleBBBURL: no target tg=%d url=%s", userID, url)
		if u.Onboarded {
			return b.sendMain(chatID, noBBBTarget)
		}
		return b.send(chatID, noBBBTarget, nil)
	}
	// lesson:{id} плюс комната предмета у препода этого типа — на следующих неделях подставится сама.
	if err := b.st.SetLessonBBB(lesson.ID, url); err != nil {
		logx.Errorf("tg", "handleBBBURL: set lesson bbb lesson=%d: %v", lesson.ID, err)
		return err
	}
	logx.Infof("tg", "bbb link lesson=%d tg=%d %q url=%s", lesson.ID, userID, lesson.Discipline, url)
	if err := b.st.AddEvent(model.Event{
		At:         b.now(),
		Type:       "bbb",
		TelegramID: userID,
		LessonID:   lesson.ID,
		Message:    url,
	}); err != nil {
		logx.Errorf("tg", "handleBBBURL: add event lesson=%d tg=%d: %v", lesson.ID, userID, err)
		return err
	}
	text := formatSavedLink(*lesson)
	if u.Onboarded {
		return b.sendMain(chatID, text)
	}
	return b.send(chatID, text, nil)
}

func (b *Bot) lookupBBB(lessonID int64) string {
	if b == nil || b.st == nil {
		return ""
	}
	url := b.st.GetLessonBBB(lessonID)
	logx.Debugf("tg", "lookupBBB: lesson=%d found=%v", lessonID, url != "")
	return url
}

func (b *Bot) pickBBBTarget(u *model.User, now time.Time) *model.Lesson {
	logx.Debugf("tg", "pickBBBTarget: tg=%d now=%s", u.TelegramID, now.Format(time.RFC3339))
	lessons, err := b.st.ListLessons()
	if err != nil {
		logx.Errorf("tg", "pickBBBTarget: list lessons: %v", err)
		lessons = nil
	}
	// Ответ на T-15 привязываем к той паре, пока она не закончилась.
	if lid := b.lastT15Lesson(u.TelegramID); lid != 0 {
		if l, err := b.st.LessonByID(lid); err == nil && l != nil && !now.After(l.Finish) {
			logx.Debugf("tg", "pickBBBTarget: last t15 lesson=%d", lid)
			return l
		}
	}
	// Только своя ссылка пары: унаследованная от комнаты предмета не мешает
	// привязать присланный URL к ближайшей паре.
	hasLink := func(lessonID int64) bool {
		url, own, err := b.st.LessonBBB(lessonID)
		return err == nil && own && strings.TrimSpace(url) != ""
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
	l := pickLessonForBBB(now, u.Subgroup, lessons, hasLink, intents)
	if l != nil {
		logx.Debugf("tg", "pickBBBTarget: chosen lesson=%d", l.ID)
	} else {
		logx.Debugf("tg", "pickBBBTarget: none tg=%d", u.TelegramID)
	}
	return l
}

func (b *Bot) onLeaveCallback(bot *gotgbot.Bot, ctx *ext.Context) error {
	logx.Debugf("tg", "onLeaveCallback: enter")
	from := b.allowed(ctx)
	if from == nil || ctx.CallbackQuery == nil {
		logx.Warnf("tg", "onLeaveCallback: rejected")
		return nil
	}
	lessonID, ok := parseLeaveCallback(ctx.CallbackQuery.Data)
	if !ok {
		logx.Warnf("tg", "onLeaveCallback: bad callback data=%q", ctx.CallbackQuery.Data)
		if _, err := ctx.CallbackQuery.Answer(bot, nil); err != nil {
			logx.Warnf("tg", "onLeaveCallback: answer: %v", err)
		}
		return nil
	}
	logx.Debugf("tg", "onLeaveCallback: tg=%d lesson=%d", from.Id, lessonID)
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
		logx.Errorf("tg", "onLeaveCallback: decision tg=%d lesson=%d: %v", from.Id, lessonID, err)
		if _, aerr := ctx.CallbackQuery.Answer(bot, &gotgbot.AnswerCallbackQueryOpts{Text: "не вышло", ShowAlert: true}); aerr != nil {
			logx.Warnf("tg", "onLeaveCallback: alert answer: %v", aerr)
		}
		return err
	}
	logx.Infof("tg", "leave button tg=%d lesson=%d", from.Id, lessonID)
	if err := b.st.AddEvent(model.Event{
		At: now, Type: model.EventLeave,
		TelegramID: from.Id, LessonID: lessonID, Message: "кнопка",
	}); err != nil {
		logx.Warnf("tg", "onLeaveCallback: add event tg=%d lesson=%d: %v", from.Id, lessonID, err)
	}
	if _, err := ctx.CallbackQuery.Answer(bot, &gotgbot.AnswerCallbackQueryOpts{Text: "выхожу"}); err != nil {
		logx.Warnf("tg", "onLeaveCallback: answer: %v", err)
	}
	if ctx.CallbackQuery.Message != nil {
		if _, _, err := ctx.CallbackQuery.Message.EditText(bot, &gotgbot.EditMessageTextOpts{
			ParseMode:   htmlMode,
			Text:        "Выхожу из комнаты… Через несколько секунд отключусь.",
			ReplyMarkup: gotgbot.InlineKeyboardMarkup{InlineKeyboard: [][]gotgbot.InlineKeyboardButton{}},
		}); err != nil {
			logx.Warnf("tg", "onLeaveCallback: edit message: %v", err)
		}
	}
	b.mu.Lock()
	delete(b.live, from.Id)
	b.mu.Unlock()
	logx.Debugf("tg", "onLeaveCallback: done tg=%d lesson=%d", from.Id, lessonID)
	return nil
}

func (b *Bot) sendToday(u *model.User, chatID int64) error {
	logx.Debugf("tg", "sendToday: tg=%d chat=%d", u.TelegramID, chatID)
	text, err := b.formatTodayReply(u)
	if err != nil {
		logx.Errorf("tg", "sendToday: format tg=%d: %v", u.TelegramID, err)
		return err
	}
	return b.sendMain(chatID, text)
}

// lessonDay — самарский день пары: по Begin (точное время), Date — запасной.
func (b *Bot) lessonDay(l model.Lesson) string {
	if !l.Begin.IsZero() {
		return l.Begin.In(b.loc).Format("2006-01-02")
	}
	return l.Date
}

// dayRows — пары подгруппы пользователя по дням, с решением, presence и ссылкой.
func (b *Bot) dayRows(u *model.User) (map[string][]todayRow, error) {
	lessons, err := b.st.ListLessons()
	if err != nil {
		logx.Errorf("tg", "dayRows: list lessons: %v", err)
		return nil, err
	}
	pres, err := b.st.ListPresence()
	if err != nil {
		logx.Errorf("tg", "dayRows: list presence: %v", err)
		return nil, err
	}
	links, err := b.st.ListBBB()
	if err != nil {
		logx.Errorf("tg", "dayRows: list bbb: %v", err)
		return nil, err
	}
	byLesson := map[int64]model.Presence{}
	for _, p := range pres {
		if p.TelegramID == u.TelegramID {
			byLesson[p.LessonID] = p
		}
	}
	out := map[string][]todayRow{}
	for _, l := range lessons {
		if !l.MatchesSubgroup(u.Subgroup) {
			continue
		}
		row := todayRow{Lesson: l}
		if l.Online {
			url, _ := model.ResolveBBB(l, links)
			row.HasLink = url != ""
			if p, ok := byLesson[l.ID]; ok {
				row.Presence = p.State
				row.Detail = p.Message
			}
			if in, err := b.st.GetIntent(u.TelegramID, l.ID); err == nil && in != nil {
				row.Decision = in.Decision
			}
		}
		day := b.lessonDay(l)
		out[day] = append(out[day], row)
	}
	for d := range out {
		rows := out[d]
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].Lesson.Begin.Before(rows[j].Lesson.Begin) })
	}
	return out, nil
}

func (b *Bot) formatTodayReply(u *model.User) (string, error) {
	logx.Debugf("tg", "formatTodayReply: tg=%d subgroup=%d", u.TelegramID, u.Subgroup)
	days, err := b.dayRows(u)
	if err != nil {
		return "", err
	}
	now := b.now().In(b.loc)
	today := now.Format("2006-01-02")
	rows := days[today]
	text := formatToday(now, b.loc, u.FIO, rows)
	upcoming := false
	for _, r := range rows {
		if r.Lesson.Finish.IsZero() || now.Before(r.Lesson.Finish) {
			upcoming = true
			break
		}
	}
	if !upcoming {
		// На сегодня всё — показываем ближайший день с парами.
		for i := 1; i <= 8; i++ {
			d := now.AddDate(0, 0, i)
			if next := days[d.Format("2006-01-02")]; len(next) > 0 {
				title := "Ближайшие: " + dayTitle(d)
				if i == 1 {
					title = "Завтра, " + dayTitle(d)
				}
				text += "\n\n" + formatDayBlock(title, next, b.loc)
				break
			}
		}
	}
	logx.Debugf("tg", "formatTodayReply: rows=%d", len(rows))
	return text, nil
}

// formatWeekReply — семь дней начиная с сегодня.
func (b *Bot) formatWeekReply(u *model.User) (string, error) {
	days, err := b.dayRows(u)
	if err != nil {
		return "", err
	}
	now := b.now().In(b.loc)
	var parts []string
	for i := 0; i < 7; i++ {
		d := now.AddDate(0, 0, i)
		rows := days[d.Format("2006-01-02")]
		if len(rows) == 0 {
			continue
		}
		parts = append(parts, formatDayBlock(dayTitle(d), rows, b.loc))
	}
	if len(parts) == 0 {
		return "На ближайшую неделю пар нет.", nil
	}
	return strings.Join(parts, "\n\n"), nil
}

func (b *Bot) onWeek(_ *gotgbot.Bot, ctx *ext.Context) error {
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
	text, err := b.formatWeekReply(u)
	if err != nil {
		return err
	}
	return b.sendMain(ctx.EffectiveMessage.Chat.Id, text)
}

func (b *Bot) onOnboardCallback(bot *gotgbot.Bot, ctx *ext.Context) error {
	logx.Debugf("tg", "onOnboardCallback: enter")
	if ctx == nil || ctx.CallbackQuery == nil {
		return nil
	}
	from := b.allowed(ctx)
	if from == nil {
		logx.Warnf("tg", "onOnboardCallback: rejected")
		answerToast(bot, ctx, "")
		return nil
	}
	kind, n, ok := parseOnboardCallback(ctx.CallbackQuery.Data)
	if !ok {
		logx.Warnf("tg", "onOnboardCallback: bad callback data=%q", ctx.CallbackQuery.Data)
		answerToast(bot, ctx, "")
		return nil
	}
	logx.Debugf("tg", "onOnboardCallback: tg=%d kind=%s n=%d", from.Id, kind, n)
	u, err := b.st.GetUser(from.Id)
	if err != nil || u == nil || u.Onboarded {
		if err != nil {
			logx.Errorf("tg", "onOnboardCallback: get user tg=%d: %v", from.Id, err)
		} else {
			logx.Debugf("tg", "onOnboardCallback: skip tg=%d u_nil=%v", from.Id, u == nil)
		}
		// Старая кнопка онбординга у уже настроенного: ничего не меняем и честно говорим.
		answerToast(bot, ctx, "Уже настроено — меняй в Профиле")
		return err
	}
	if kind == "skipw" && (u.OnboardStage != model.StageWords || strings.TrimSpace(u.FIO) == "") {
		answerToast(bot, ctx, "Кнопка устарела")
		return nil
	}
	toast := "Пропуск"
	if kind == "sub" {
		toast = fmt.Sprintf("Подгруппа %d", n)
	}
	answerToast(bot, ctx, toast)
	chatID := from.Id
	if ctx.EffectiveChat != nil {
		chatID = ctx.EffectiveChat.Id
	}

	switch kind {
	case "sub":
		if strings.TrimSpace(u.FIO) == "" {
			logx.Debugf("tg", "onOnboardCallback: no fio tg=%d", from.Id)
			return b.sendAskFIO(chatID, from.Id, askFIO)
		}
		if ctx.CallbackQuery.Message != nil {
			if _, _, err := ctx.CallbackQuery.Message.EditText(bot, &gotgbot.EditMessageTextOpts{
				ParseMode:   htmlMode,
				Text:        fmt.Sprintf("Подгруппа %d", n),
				ReplyMarkup: emptyInline(),
			}); err != nil {
				logx.Warnf("tg", "onOnboardCallback: edit sub: %v", err)
			}
		}
		return b.setSubgroupAndAskWords(u, chatID, n)
	case "skipw":
		if ctx.CallbackQuery.Message != nil {
			if _, _, err := ctx.CallbackQuery.Message.EditText(bot, &gotgbot.EditMessageTextOpts{
				ParseMode:   htmlMode,
				Text:        "Свои слова пропускаю.",
				ReplyMarkup: emptyInline(),
			}); err != nil {
				logx.Warnf("tg", "onOnboardCallback: edit skipw: %v", err)
			}
		}
		return b.finishOnboarding(u, chatID, "-")
	}
	logx.Debugf("tg", "onOnboardCallback: unhandled kind=%s", kind)
	return nil
}

func (b *Bot) onSettingsCallback(bot *gotgbot.Bot, ctx *ext.Context) error {
	logx.Debugf("tg", "onSettingsCallback: enter")
	if ctx == nil || ctx.CallbackQuery == nil {
		return nil
	}
	from := b.allowed(ctx)
	if from == nil {
		logx.Warnf("tg", "onSettingsCallback: rejected")
		answerToast(bot, ctx, "")
		return nil
	}
	kind, n, ok := parseSettingsCallback(ctx.CallbackQuery.Data)
	if !ok {
		logx.Warnf("tg", "onSettingsCallback: bad callback data=%q", ctx.CallbackQuery.Data)
		answerToast(bot, ctx, "")
		return nil
	}
	logx.Debugf("tg", "onSettingsCallback: tg=%d kind=%s n=%d", from.Id, kind, n)
	u, err := b.st.GetUser(from.Id)
	if err != nil || u == nil {
		if err != nil {
			logx.Errorf("tg", "onSettingsCallback: get user tg=%d: %v", from.Id, err)
		}
		answerToast(bot, ctx, "")
		return err
	}
	chatID := from.Id
	if ctx.EffectiveChat != nil {
		chatID = ctx.EffectiveChat.Id
	}
	if !u.Onboarded {
		logx.Debugf("tg", "onSettingsCallback: onboarding tg=%d", from.Id)
		answerToast(bot, ctx, "")
		return b.continueOnboarding(u, chatID, "")
	}

	switch kind {
	case "fio":
		logx.Debugf("tg", "onSettingsCallback: fio tg=%d", from.Id)
		answerToast(bot, ctx, "Имя")
		return b.sendAskFIO(chatID, from.Id, askFIO)
	case "words":
		answerToast(bot, ctx, "Слова")
		return b.sendAskWords(u, chatID)
	case "rooms":
		answerToast(bot, ctx, "Комнаты")
		return b.sendRooms(u, chatID)
	case "help":
		answerToast(bot, ctx, "")
		return b.send(chatID, helpText, &gotgbot.SendMessageOpts{ReplyMarkup: backToProfileKeyboard()})
	case "back":
		answerToast(bot, ctx, "")
		b.clearAwait(from.Id)
		if ctx.CallbackQuery.Message != nil {
			_, _, err := ctx.CallbackQuery.Message.EditText(bot, &gotgbot.EditMessageTextOpts{
				ParseMode:   htmlMode,
				Text:        formatSettings(*u),
				ReplyMarkup: settingsKeyboard(u.Subgroup),
			})
			if err == nil {
				return nil
			}
			logx.Warnf("tg", "onSettingsCallback: edit back tg=%d: %v", from.Id, err)
		}
		return b.sendSettings(u, chatID)
	case "sub":
		logx.Infof("tg", "settings subgroup tg=%d sub=%d", from.Id, n)
		answerToast(bot, ctx, fmt.Sprintf("Подгруппа %d", n))
		if err := b.st.SetSubgroup(from.Id, n); err != nil {
			logx.Errorf("tg", "onSettingsCallback: set subgroup tg=%d: %v", from.Id, err)
			return err
		}
		u.Subgroup = n
		if ctx.CallbackQuery.Message != nil {
			if _, _, err := ctx.CallbackQuery.Message.EditText(bot, &gotgbot.EditMessageTextOpts{
				ParseMode:   htmlMode,
				Text:        formatSettings(*u),
				ReplyMarkup: settingsKeyboard(n),
			}); err != nil {
				logx.Warnf("tg", "onSettingsCallback: edit sub tg=%d: %v", from.Id, err)
			}
		}
		return nil
	}
	logx.Debugf("tg", "onSettingsCallback: unhandled kind=%s", kind)
	answerToast(bot, ctx, "")
	return nil
}

func (b *Bot) onJoinCallback(bot *gotgbot.Bot, ctx *ext.Context) error {
	logx.Debugf("tg", "onJoinCallback: enter")
	if ctx == nil || ctx.CallbackQuery == nil {
		return nil
	}
	from := b.allowed(ctx)
	if from == nil {
		logx.Warnf("tg", "onJoinCallback: rejected")
		answerToast(bot, ctx, "")
		return nil
	}
	yes, lessonID, ok := parseJoinCallback(ctx.CallbackQuery.Data)
	if !ok {
		logx.Warnf("tg", "onJoinCallback: bad callback data=%q", ctx.CallbackQuery.Data)
		answerToast(bot, ctx, "")
		return nil
	}
	logx.Debugf("tg", "onJoinCallback: tg=%d lesson=%d yes=%v", from.Id, lessonID, yes)
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
		logx.Errorf("tg", "onJoinCallback: decision tg=%d lesson=%d: %v", from.Id, lessonID, err)
		return err
	}
	logx.Infof("tg", "t15 decision tg=%d lesson=%d join=%v", from.Id, lessonID, yes)

	if !yes {
		if err := b.st.AddEvent(model.Event{
			At:         now,
			Type:       model.EventSkip,
			TelegramID: from.Id,
			LessonID:   lessonID,
			Message:    "пропуск",
		}); err != nil {
			logx.Errorf("tg", "onJoinCallback: add skip event tg=%d lesson=%d: %v", from.Id, lessonID, err)
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
		hasLink = b.lookupBBB(lesson.ID) != ""
	}
	reply := formatSkipAck(lesson, b.loc)
	if yes {
		reply = formatJoinAck(lesson, b.loc, fio, hasLink)
	}

	if ctx.CallbackQuery.Message != nil {
		if _, _, err := ctx.CallbackQuery.Message.EditText(bot, &gotgbot.EditMessageTextOpts{
			ParseMode:   htmlMode,
			Text:        reply,
			ReplyMarkup: t15Keyboard(lessonID),
		}); err != nil {
			logx.Warnf("tg", "onJoinCallback: edit message tg=%d lesson=%d: %v", from.Id, lessonID, err)
		}
	}
	logx.Debugf("tg", "onJoinCallback: done tg=%d lesson=%d yes=%v", from.Id, lessonID, yes)
	return nil
}
