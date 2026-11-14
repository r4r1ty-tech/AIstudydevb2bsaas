package tg

import (
	"strings"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
)

const (
	btnPanelPass   = "🔑 Пароль в панель"
	botShortDesc   = "Пароль в панель и уведомления СГАУ-бота"
	botDescription = "Здесь только пароль в панель и уведомления: тест решён или упал, не получилось зайти на пару, конспект готов, скоро срок в Moodle. Всё остальное — в панели."
	startText      = "Здесь только пароль в панель и уведомления.\n\n" +
		"Жми «" + btnPanelPass + "» внизу — пришлю личный пароль на час. " +
		"Пары, решения «заходить или нет», профиль, Moodle и конспекты — в панели."
	onlyPassText = "Бот только выдаёт пароль и присылает уведомления. Всё остальное — в панели: жми «" + btnPanelPass + "»."
)

func mainKeyboard() gotgbot.ReplyKeyboardMarkup {
	return gotgbot.ReplyKeyboardMarkup{
		Keyboard:       [][]gotgbot.KeyboardButton{{{Text: btnPanelPass}}},
		IsPersistent:   true,
		ResizeKeyboard: true,
	}
}

func chatOf(ctx *ext.Context, from *gotgbot.User) int64 {
	if ctx.EffectiveChat != nil {
		return ctx.EffectiveChat.Id
	}
	return from.Id
}

func (b *Bot) onStart(_ *gotgbot.Bot, ctx *ext.Context) error {
	from := b.allowed(ctx)
	if from == nil {
		return nil
	}
	b.setMenuButton(from.Id)
	return b.send(chatOf(ctx, from), startText, &gotgbot.SendMessageOpts{ReplyMarkup: mainKeyboard()})
}

func (b *Bot) onText(_ *gotgbot.Bot, ctx *ext.Context) error {
	from := b.allowed(ctx)
	if from == nil || ctx.EffectiveMessage == nil {
		return nil
	}
	chatID := chatOf(ctx, from)
	text := strings.TrimSpace(ctx.EffectiveMessage.GetText())
	if text == btnPanelPass {
		return b.sendPanelPass(from.Id, chatID)
	}
	if strings.HasPrefix(text, "/") {
		return nil // /start и /pass ловят свои обработчики
	}
	return b.send(chatID, onlyPassText, &gotgbot.SendMessageOpts{ReplyMarkup: mainKeyboard()})
}
