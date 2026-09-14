package tg

import (
	"strings"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

const askTestName = "Как подписывать тестового гостя в BBB?\nНапиши имя одной строкой. «-» — снова «тест»."

func (b *Bot) onTest(_ *gotgbot.Bot, ctx *ext.Context) error {
	from := b.allowed(ctx)
	if from == nil || !b.cfg.IsAdmin(from.Id) || ctx.EffectiveMessage == nil {
		return nil
	}
	chatID := ctx.EffectiveMessage.Chat.Id
	payload := commandPayload(ctx.EffectiveMessage.GetText())
	if url := extractBBBURL(payload); url != "" {
		return b.armTestURL(chatID, url, "")
	}
	if name, ok := parseTestNamePayload(payload); ok {
		return b.setTestGuestName(chatID, name)
	}
	return b.sendTestCard(chatID)
}

func parseTestNamePayload(payload string) (string, bool) {
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
	j, err := b.st.GetTestJoin()
	if err != nil {
		return err
	}
	j.Name = normalizeTestName(raw)
	if err := b.st.PutTestJoin(j); err != nil {
		return err
	}
	b.clearAwait(chatID)
	return b.send(chatID, formatTestCard(j), &gotgbot.SendMessageOpts{ReplyMarkup: b.testMarkup(j)})
}

func normalizeTestName(raw string) string {
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
	j, err := b.st.GetTestJoin()
	if err != nil {
		return err
	}
	msg, err := b.api.SendMessage(chatID, formatTestCard(j), &gotgbot.SendMessageOpts{
		ReplyMarkup:        b.testMarkup(j),
		LinkPreviewOptions: &gotgbot.LinkPreviewOptions{IsDisabled: true},
	})
	if err != nil {
		return err
	}
	if msg != nil {
		b.rememberTestLive(chatID, msg.MessageId, j)
	}
	return nil
}

func (b *Bot) armTestURL(chatID int64, url, extra string) error {
	j, err := b.st.GetTestJoin()
	if err != nil {
		return err
	}
	j.URL = url
	if j.Want == model.TestWantOff {
		j.Status = model.TestIdle
		j.Mode = ""
		j.Message = ""
	}
	if err := b.st.PutTestJoin(j); err != nil {
		return err
	}
	text := formatTestCard(j)
	if extra != "" {
		text = extra + "\n\n" + text
	}
	msg, err := b.api.SendMessage(chatID, text, &gotgbot.SendMessageOpts{
		ReplyMarkup:        b.testMarkup(j),
		LinkPreviewOptions: &gotgbot.LinkPreviewOptions{IsDisabled: true},
	})
	if err != nil {
		return err
	}
	if msg != nil {
		b.rememberTestLive(chatID, msg.MessageId, j)
	}
	return nil
}

func (b *Bot) onTestCallback(bot *gotgbot.Bot, ctx *ext.Context) error {
	if ctx == nil || ctx.CallbackQuery == nil {
		return nil
	}
	from := b.allowed(ctx)
	if from == nil || !b.cfg.IsAdmin(from.Id) {
		answerToast(bot, ctx, "")
		return nil
	}
	want := strings.TrimPrefix(ctx.CallbackQuery.Data, "tx:")
	j, err := b.st.GetTestJoin()
	if err != nil {
		return err
	}
	switch want {
	case "name":
		b.setAwait(from.Id, awaitTestName)
		answerToast(bot, ctx, "имя")
		return b.send(from.Id, askTestName, nil)
	case "dummy":
		if strings.TrimSpace(j.URL) == "" {
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
		answerToast(bot, ctx, "")
		return nil
	}
	if err := b.st.PutTestJoin(j); err != nil {
		return err
	}
	if ctx.CallbackQuery.Message != nil {
		_, _, _ = ctx.CallbackQuery.Message.EditText(bot, &gotgbot.EditMessageTextOpts{
			Text:        formatTestCard(j),
			ReplyMarkup: b.testMarkup(j),
			LinkPreviewOptions: &gotgbot.LinkPreviewOptions{IsDisabled: true},
		})
		b.rememberTestLive(from.Id, ctx.CallbackQuery.Message.GetMessageId(), j)
	}
	return nil
}
