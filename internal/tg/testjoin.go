package tg

import (
	"strings"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

const askTestName = "Как подписывать тестового гостя в BBB?\nНапиши имя одной строкой. «-» — снова «тест»."
const askTestURL = "Кинь ссылку bbb.ssau.ru/b/… — привяжу только к тесту, не к паре."

func (b *Bot) onTest(_ *gotgbot.Bot, ctx *ext.Context) error {
	logx.Debugf("tg", "onTest: enter")
	from := b.allowed(ctx)
	if from == nil || !b.cfg.IsAdmin(from.Id) || ctx.EffectiveMessage == nil {
		logx.Warnf("tg", "onTest: rejected")
		return nil
	}
	chatID := ctx.EffectiveMessage.Chat.Id
	payload := commandPayload(ctx.EffectiveMessage.GetText())
	logx.Debugf("tg", "onTest: tg=%d chat=%d payload=%q", from.Id, chatID, payload)
	if url := extractBBBURL(payload); url != "" {
		logx.Debugf("tg", "onTest: url payload tg=%d", from.Id)
		return b.armTestURL(chatID, url, "")
	}
	if name, ok := parseTestNamePayload(payload); ok {
		logx.Debugf("tg", "onTest: name payload tg=%d name=%q", from.Id, name)
		return b.setTestGuestName(chatID, name)
	}
	return b.sendTestCard(chatID)
}

func parseTestNamePayload(payload string) (string, bool) {
	logx.Debugf("tg", "parseTestNamePayload: %q", payload)
	payload = strings.TrimSpace(payload)
	if payload == "" || extractBBBURL(payload) != "" {
		return "", false
	}
	lower := strings.ToLower(payload)
	for _, p := range []string{"имя ", "name ", "как "} {
		if strings.HasPrefix(lower, p) {
			return strings.TrimSpace(payload[len(p):]), true
		}
	}
	// /test Иванов — любое не-URL тело считаем именем
	return payload, true
}

func (b *Bot) setTestGuestName(chatID int64, raw string) error {
	logx.Debugf("tg", "setTestGuestName: chat=%d raw=%q", chatID, raw)
	j, err := b.st.GetTestJoin()
	if err != nil {
		logx.Errorf("tg", "setTestGuestName: get test join: %v", err)
		return err
	}
	j.Name = normalizeTestName(raw)
	if err := b.st.PutTestJoin(j); err != nil {
		logx.Errorf("tg", "setTestGuestName: put test join: %v", err)
		return err
	}
	b.clearAwait(chatID)
	logx.Infof("tg", "test guest name=%q", j.Name)
	return b.send(chatID, formatTestCard(j), &gotgbot.SendMessageOpts{ReplyMarkup: b.testMarkup(j)})
}

func normalizeTestName(raw string) string {
	logx.Debugf("tg", "normalizeTestName: %q", raw)
	name := strings.TrimSpace(raw)
	if name == "" || name == "-" || name == "—" {
		return model.TestGuestName
	}
	runes := []rune(name)
	if len(runes) > 64 {
		name = string(runes[:64])
	}
	return name
}

func (b *Bot) sendTestCard(chatID int64) error {
	logx.Debugf("tg", "sendTestCard: chat=%d", chatID)
	j, err := b.st.GetTestJoin()
	if err != nil {
		logx.Errorf("tg", "sendTestCard: get test join: %v", err)
		return err
	}
	msg, err := b.api.SendMessage(chatID, formatTestCard(j), &gotgbot.SendMessageOpts{
		ParseMode:          htmlMode,
		ReplyMarkup:        b.testMarkup(j),
		LinkPreviewOptions: &gotgbot.LinkPreviewOptions{IsDisabled: true},
	})
	if err != nil {
		logx.Errorf("tg", "sendTestCard: send chat=%d: %v", chatID, err)
		return err
	}
	if msg != nil {
		b.rememberTestLive(chatID, msg.MessageId, j)
	}
	return nil
}

func (b *Bot) armTestURL(chatID int64, url, extra string) error {
	logx.Debugf("tg", "armTestURL: chat=%d url=%s extra_len=%d", chatID, url, len(extra))
	j, err := b.st.GetTestJoin()
	if err != nil {
		logx.Errorf("tg", "armTestURL: get test join: %v", err)
		return err
	}
	j.URL = url
	if j.Want == model.TestWantOff {
		j.Status = model.TestIdle
		j.Mode = ""
		j.Message = ""
	}
	if err := b.st.PutTestJoin(j); err != nil {
		logx.Errorf("tg", "armTestURL: put test join: %v", err)
		return err
	}
	logx.Infof("tg", "test url armed url=%s", url)
	b.clearAwait(chatID)
	text := formatTestCard(j)
	if extra != "" {
		text = extra + "\n\n" + text
	}
	msg, err := b.api.SendMessage(chatID, text, &gotgbot.SendMessageOpts{
		ParseMode:          htmlMode,
		ReplyMarkup:        b.testMarkup(j),
		LinkPreviewOptions: &gotgbot.LinkPreviewOptions{IsDisabled: true},
	})
	if err != nil {
		logx.Errorf("tg", "armTestURL: send chat=%d: %v", chatID, err)
		return err
	}
	if msg != nil {
		b.rememberTestLive(chatID, msg.MessageId, j)
	}
	return nil
}

func (b *Bot) onTestCallback(bot *gotgbot.Bot, ctx *ext.Context) error {
	logx.Debugf("tg", "onTestCallback: enter")
	if ctx == nil || ctx.CallbackQuery == nil {
		return nil
	}
	from := b.allowed(ctx)
	if from == nil || !b.cfg.IsAdmin(from.Id) {
		logx.Warnf("tg", "onTestCallback: rejected")
		answerToast(bot, ctx, "")
		return nil
	}
	want := strings.TrimPrefix(ctx.CallbackQuery.Data, "tx:")
	logx.Debugf("tg", "onTestCallback: tg=%d want=%s", from.Id, want)
	j, err := b.st.GetTestJoin()
	if err != nil {
		logx.Errorf("tg", "onTestCallback: get test join: %v", err)
		return err
	}
	switch want {
	case "url":
		b.setAwait(from.Id, awaitTestURL)
		answerToast(bot, ctx, "ссылка")
		return b.send(from.Id, askTestURL, nil)
	case "name":
		b.setAwait(from.Id, awaitTestName)
		answerToast(bot, ctx, "имя")
		return b.send(from.Id, askTestName, nil)
	case "dummy":
		if strings.TrimSpace(j.URL) == "" {
			logx.Warnf("tg", "onTestCallback: no url tg=%d", from.Id)
			answerToast(bot, ctx, "сначала ссылка")
			return b.send(from.Id, testNeedURL, nil)
		}
		if !testAlready(j, model.TestWantDummy) {
			j.Want = model.TestWantDummy
			j.Status = model.TestJoining
			j.Message = "захожу"
		}
		answerToast(bot, ctx, "болванчик")
	case "listen":
		if strings.TrimSpace(j.URL) == "" {
			logx.Warnf("tg", "onTestCallback: no url tg=%d", from.Id)
			answerToast(bot, ctx, "сначала ссылка")
			return b.send(from.Id, testNeedURL, nil)
		}
		if !testAlready(j, model.TestWantListen) {
			j.Want = model.TestWantListen
			j.Status = model.TestJoining
			j.Message = "захожу со звуком"
		}
		answerToast(bot, ctx, "со звуком")
	case "leave":
		j.Want = model.TestWantOff
		j.Status = model.TestIdle
		j.Mode = ""
		j.Message = "выхожу"
		answerToast(bot, ctx, "выхожу")
	default:
		logx.Debugf("tg", "onTestCallback: unknown want=%s", want)
		answerToast(bot, ctx, "")
		return nil
	}
	if err := b.st.PutTestJoin(j); err != nil {
		logx.Errorf("tg", "onTestCallback: put test join: %v", err)
		return err
	}
	logx.Infof("tg", "test callback want=%s status=%s", j.Want, j.Status)
	if ctx.CallbackQuery.Message != nil {
		if _, _, err := ctx.CallbackQuery.Message.EditText(bot, &gotgbot.EditMessageTextOpts{
			ParseMode:          htmlMode,
			Text:               formatTestCard(j),
			ReplyMarkup:        b.testMarkup(j),
			LinkPreviewOptions: &gotgbot.LinkPreviewOptions{IsDisabled: true},
		}); err != nil {
			logx.Warnf("tg", "onTestCallback: edit message: %v", err)
		}
		b.rememberTestLive(from.Id, ctx.CallbackQuery.Message.GetMessageId(), j)
	}
	return nil
}
