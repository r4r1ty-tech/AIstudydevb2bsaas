package tg

import (
	"fmt"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/access"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/panelpass"
)

const panelPassNoKey = "Пароли в панель сейчас не выдаются: на сервере нет ключа (SECRET_KEY_FILE)."

// onPass — /pass: личный пароль в панель.
func (b *Bot) onPass(_ *gotgbot.Bot, ctx *ext.Context) error {
	from := b.allowed(ctx)
	if from == nil {
		return nil
	}
	chatID := from.Id
	if ctx.EffectiveChat != nil {
		chatID = ctx.EffectiveChat.Id
	}
	return b.sendPanelPass(from.Id, chatID)
}

// sendPanelPass выдаёт личный часовой пароль и кнопку открытия панели.
// Пароль считается из ключа хранилища так же, как его проверяет панель.
func (b *Bot) sendPanelPass(id, chatID int64) error {
	if b.vault == nil {
		logx.Warnf("tg", "panel pass: нет vault tg=%d", id)
		return b.send(chatID, panelPassNoKey, nil)
	}
	b.setMenuButton(id)
	pw, until := panelpass.Issue(b.vault.Derive(panelpass.Label), id, b.now())
	who := ""
	if n := access.LKName(b.st, id); n != "" {
		who = " для " + bold(n)
	}
	text := fmt.Sprintf("🔑 Пароль в панель%s: <code>%s</code>\n"+
		"Действует до %s. Потом жми «%s» ещё раз — каждый час новый.\n"+
		"Пароль личный: по нему панель знает, что это ты. Никому не пересылай.",
		who, pw, until.In(b.loc).Format("15:04"), btnPanelPass)
	opts := &gotgbot.SendMessageOpts{}
	if url := b.webAppURL(); url != "" {
		opts.ReplyMarkup = gotgbot.InlineKeyboardMarkup{InlineKeyboard: [][]gotgbot.InlineKeyboardButton{{
			{Text: "Открыть панель", WebApp: &gotgbot.WebAppInfo{Url: url}},
		}}}
	}
	logx.Infof("tg", "panel pass issued tg=%d until=%s", id, until.Format("15:04"))
	return b.send(chatID, text, opts)
}
