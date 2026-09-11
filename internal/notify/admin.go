package notify

import (
	"context"
	"log"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
)

func Admin(ctx context.Context, cfg *config.Config, text string) {
	if cfg == nil || cfg.BotToken == "" || text == "" {
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
	if _, err := bot.SendMessageWithContext(ctx, cfg.AdminID, text, nil); err != nil {
		log.Printf("notify: send: %v", err)
	}
}
