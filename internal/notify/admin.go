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
	logx.Debugf("notify", "botFor: enter cached=%v token_present=%v", botInst != nil, token != "")
	botMu.Lock()
	defer botMu.Unlock()
	if botInst != nil && botTok == token {
		logx.Debugf("notify", "botFor: reuse cached bot")
		return botInst, nil
	}
	b, err := gotgbot.NewBot(token, nil)
	if err != nil {
		logx.Errorf("notify", "botFor: NewBot: %v", err)
		return nil, fmt.Errorf("botFor: NewBot: %w", err)
	}
	botInst, botTok = b, token
	logx.Infof("notify", "botFor: created bot username=%s", b.User.Username)
	return b, nil
}

func Admin(ctx context.Context, cfg *config.Config, text string) {
	logx.Debugf("notify", "Admin: enter cfg_nil=%v text_len=%d", cfg == nil, len(text))
	if cfg == nil {
		logx.Debugf("notify", "Admin: nil cfg, skip")
		return
	}
	logx.Debugf("notify", "Admin: -> User admin_id=%d", cfg.AdminID)
	User(ctx, cfg, cfg.AdminID, text)
}

func User(ctx context.Context, cfg *config.Config, telegramID int64, text string) {
	logx.Debugf("notify", "User: enter id=%d text_len=%d", telegramID, len(text))
	UserMarkup(ctx, cfg, telegramID, text, nil)
}

// Broadcast sends text to every id and reports how many were delivered.
func Broadcast(ctx context.Context, cfg *config.Config, ids []int64, text string) (sent, failed int) {
	logx.Debugf("notify", "Broadcast: enter ids=%d text_len=%d", len(ids), len(text))
	if cfg == nil || cfg.BotToken == "" {
		logx.Warnf("notify", "Broadcast: guard cfg_nil=%v token_present=%v", cfg == nil, cfg != nil && cfg.BotToken != "")
		return 0, 0
	}
	if strings.TrimSpace(text) == "" {
		logx.Warnf("notify", "Broadcast: empty text")
		return 0, 0
	}
	if ctx == nil {
		ctx = context.Background()
	}
	bot, err := botFor(cfg.BotToken)
	if err != nil {
		logx.Errorf("notify", "Broadcast: botFor: %v", err)
		return 0, len(ids)
	}
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, err := bot.SendMessageWithContext(ctx, id, text, nil); err != nil {
			logx.Warnf("notify", "Broadcast: send id=%d: %v", id, err)
			failed++
			continue
		}
		sent++
	}
	logx.Infof("notify", "Broadcast: done sent=%d failed=%d", sent, failed)
	return sent, failed
}

func NotesButton(packID int64) *gotgbot.InlineKeyboardMarkup {
	logx.Debugf("notify", "NotesButton: pack=%d", packID)
	if packID <= 0 {
		logx.Debugf("notify", "NotesButton: pack=%d -> nil", packID)
		return nil
	}
	logx.Debugf("notify", "NotesButton: pack=%d -> nt:%d", packID, packID)
	return &gotgbot.InlineKeyboardMarkup{
		InlineKeyboard: [][]gotgbot.InlineKeyboardButton{{{
			Text:         "Скачать PDF",
			CallbackData: fmt.Sprintf("nt:%d", packID),
		}}},
	}
}

func UserMarkup(ctx context.Context, cfg *config.Config, telegramID int64, text string, mk *gotgbot.InlineKeyboardMarkup) {
	logx.Debugf("notify", "UserMarkup: enter id=%d text_len=%d has_markup=%v", telegramID, len(text), mk != nil)
	if cfg == nil || cfg.BotToken == "" || telegramID == 0 || text == "" {
		logx.Debugf("notify", "UserMarkup: guard skip cfg_nil=%v token_present=%v id=%d text_empty=%v", cfg == nil, cfg != nil && cfg.BotToken != "", telegramID, text == "")
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	bot, err := botFor(cfg.BotToken)
	if err != nil {
		logx.Warnf("notify", "%v", err)
		logx.Errorf("notify", "UserMarkup: botFor id=%d: %v", telegramID, err)
		return
	}
	opts := &gotgbot.SendMessageOpts{}
	if mk != nil {
		opts.ReplyMarkup = *mk
	}
	logx.Debugf("notify", "UserMarkup: sending id=%d", telegramID)
	if _, err := bot.SendMessageWithContext(ctx, telegramID, text, opts); err != nil {
		logx.Warnf("notify", "send %d: %v", telegramID, err)
		logx.Errorf("notify", "UserMarkup: send id=%d: %v", telegramID, err)
		return
	}
	logx.Debugf("notify", "UserMarkup: sent id=%d", telegramID)
}

func Document(ctx context.Context, cfg *config.Config, telegramID int64, path, caption, filename string) error {
	logx.Debugf("notify", "Document: enter id=%d path=%q caption_len=%d filename=%q", telegramID, path, len(caption), filename)
	if cfg == nil || cfg.BotToken == "" || telegramID == 0 {
		logx.Warnf("notify", "Document: guard id=%d cfg_nil=%v token_present=%v", telegramID, cfg == nil, cfg != nil && cfg.BotToken != "")
		return fmt.Errorf("notify: нет токена")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	f, err := os.Open(path)
	if err != nil {
		logx.Errorf("notify", "Document: open %q id=%d: %v", path, telegramID, err)
		return fmt.Errorf("Document: open %q: %w", path, err)
	}
	defer f.Close()
	if filename == "" {
		filename = filepath.Base(path)
	}
	bot, err := botFor(cfg.BotToken)
	if err != nil {
		logx.Errorf("notify", "Document: botFor id=%d: %v", telegramID, err)
		return fmt.Errorf("Document: botFor: %w", err)
	}
	logx.Debugf("notify", "Document: sending id=%d filename=%q", telegramID, filename)
	_, err = bot.SendDocumentWithContext(ctx, telegramID, gotgbot.InputFileByReader(filename, f), &gotgbot.SendDocumentOpts{
		Caption: strings.TrimSpace(caption),
	})
	if err != nil {
		logx.Warnf("notify", "document %d: %v", telegramID, err)
		logx.Errorf("notify", "Document: send id=%d: %v", telegramID, err)
		return fmt.Errorf("Document: send %d: %w", telegramID, err)
	}
	logx.Debugf("notify", "Document: sent id=%d", telegramID)
	return nil
}
