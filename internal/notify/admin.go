package notify

import (
	"context"
	"log"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
)

func Admin(ctx context.Context, cfg *config.Config, text string) {
	if cfg == nil {
		return
	}
	User(ctx, cfg, cfg.AdminID, text)
}

func User(ctx context.Context, cfg *config.Config, telegramID int64, text string) {
	if cfg == nil || cfg.BotToken == "" || telegramID == 0 || text == "" {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	bot, err := gotgbot.NewBot(cfg.BotToken, nil)
	if err != nil {
		log.Printf("notify: %v", err)
		return
	}
	if _, err := bot.SendMessageWithContext(ctx, telegramID, text, nil); err != nil {
		log.Printf("notify: send %d: %v", telegramID, err)
	}
}
