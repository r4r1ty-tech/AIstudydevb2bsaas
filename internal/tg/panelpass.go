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

// onPass — /pass: личный пароль в панель (строго в личные сообщения).
func (b *Bot) onPass(_ *gotgbot.Bot, ctx *ext.Context) error {
	from := b.allowed(ctx)
	if from == nil {
		return nil
	}
	return b.sendPanelPass(from.Id, chatOf(ctx, from), isPrivateChat(ctx, from))
}

// sendPanelPass выдаёт личный часовой пароль и кнопку открытия панели.
// Пароль считается из ключа хранилища так же, как его проверяет панель,
// и всегда отправляется строго в личный чат пользователю (id).
func (b *Bot) sendPanelPass(id, chatID int64, private bool) error {
	inGroup := !private || (chatID != 0 && chatID != id)
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
		"Действует до %s — нужен только чтобы войти: панель запомнит браузер на 30 дней. Нужно снова — жми «%s», пароль каждый час новый.\n"+
		"Пароль личный: по нему панель знает, что это ты. Никому не пересылай.",
		who, pw, until.In(b.loc).Format("15:04"), btnPanelPass)
	opts := &gotgbot.SendMessageOpts{}
	if url := b.webAppURL(); url != "" {
		opts.ReplyMarkup = gotgbot.InlineKeyboardMarkup{InlineKeyboard: [][]gotgbot.InlineKeyboardButton{{
			{Text: "Открыть панель", WebApp: &gotgbot.WebAppInfo{Url: url}},
		}}}
	}
	logx.Infof("tg", "panel pass issued tg=%d until=%s group=%v", id, until.Format("15:04"), inGroup)
	if err := b.send(id, text, opts); err != nil {
		if inGroup {
			_ = b.send(chatID, "Не удалось отправить пароль в личные сообщения. Открой личный чат с ботом и нажми /start.", nil)
		}
		return err
	}
	if inGroup {
		return b.send(chatID, "Отправил в личные сообщения.", nil)
	}
	return nil
}
