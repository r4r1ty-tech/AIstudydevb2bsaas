package notify

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

var (
	botMu   sync.Mutex
	botTok  string
	botInst *gotgbot.Bot
)

// botFor переиспользует один и тот же Bot на процесс вместо создания нового
// клиента на каждое сообщение. gotgbot.Bot безопасен для конкурентных вызовов.
func botFor(token string) (*gotgbot.Bot, error) {
	botMu.Lock()
	defer botMu.Unlock()
	if botInst != nil && botTok == token {
		return botInst, nil
	}
	b, err := gotgbot.NewBot(token, nil)
	if err != nil {
		return nil, err
	}
	botInst, botTok = b, token
	return b, nil
}

func Admin(ctx context.Context, cfg *config.Config, text string) {
	if cfg == nil {
		return
	}
	User(ctx, cfg, cfg.AdminID, text)
}

func User(ctx context.Context, cfg *config.Config, telegramID int64, text string) {
	UserMarkup(ctx, cfg, telegramID, text, nil)
}

func NotesButton(packID int64) *gotgbot.InlineKeyboardMarkup {
	if packID <= 0 {
		return nil
	}
	return &gotgbot.InlineKeyboardMarkup{
		InlineKeyboard: [][]gotgbot.InlineKeyboardButton{{{
			Text:         "Скачать PDF",
			CallbackData: fmt.Sprintf("nt:%d", packID),
		}}},
	}
}

func UserMarkup(ctx context.Context, cfg *config.Config, telegramID int64, text string, mk *gotgbot.InlineKeyboardMarkup) {
	if cfg == nil || cfg.BotToken == "" || telegramID == 0 || text == "" {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	bot, err := botFor(cfg.BotToken)
	if err != nil {
		logx.Warnf("notify", "%v", err)
		return
	}
	opts := &gotgbot.SendMessageOpts{}
	if mk != nil {
		opts.ReplyMarkup = *mk
	}
	if _, err := bot.SendMessageWithContext(ctx, telegramID, text, opts); err != nil {
		logx.Warnf("notify", "send %d: %v", telegramID, err)
	}
}

func Document(ctx context.Context, cfg *config.Config, telegramID int64, path, caption, filename string) error {
	if cfg == nil || cfg.BotToken == "" || telegramID == 0 {
		return fmt.Errorf("notify: нет токена")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if filename == "" {
		filename = filepath.Base(path)
	}
	bot, err := botFor(cfg.BotToken)
	if err != nil {
		return err
	}
	_, err = bot.SendDocumentWithContext(ctx, telegramID, gotgbot.InputFileByReader(filename, f), &gotgbot.SendDocumentOpts{
		Caption: strings.TrimSpace(caption),
	})
	if err != nil {
		logx.Warnf("notify", "document %d: %v", telegramID, err)
	}
	return err
}
