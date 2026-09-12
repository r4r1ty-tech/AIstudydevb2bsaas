package notify

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
)

var DefaultTestKeywords = []string{
	"тест",
	"контрольн",
	"мудл",
	"moodle",
	"самостоятельн",
	"квиз",
	"экзамен",
	"зачет",
	"зачёт",
}

// DetectTestKeywords returns matched keywords from speech text.
func DetectTestKeywords(text string) []string {
	low := strings.ToLower(text)
	var matched []string
	for _, kw := range DefaultTestKeywords {
		if strings.Contains(low, kw) {
			matched = append(matched, kw)
		}
	}
	return matched
}

// SendTestAlert sends high-priority Telegram alerts to all whitelisted users.
func SendTestAlert(ctx context.Context, cfg *config.Config, recipients []int64, discipline, keyword, timeInfo string) error {
	if cfg == nil || cfg.BotToken == "" {
		return fmt.Errorf("bot token missing")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	bot, err := gotgbot.NewBot(cfg.BotToken, nil)
	if err != nil {
		return fmt.Errorf("new bot error: %w", err)
	}

	if len(recipients) == 0 {
		recipients = cfg.Whitelist
	}

	msg := fmt.Sprintf(`🚨 <b>ВНИМАНИЕ! СРОЧНЫЙ ТЕСТ / ЗАДАНИЕ</b> 🚨

📚 <b>Предмет:</b> %s
🔑 <b>Триггерное слово:</b> %s
⏱️ <b>Время на паре:</b> %s

💡 <i>Преподаватель упомянул тест/Moodle. Подключайтесь к паре или заходите в Moodle прямо сейчас!</i>`,
		discipline, keyword, timeInfo)

	opts := &gotgbot.SendMessageOpts{
		ParseMode: "HTML",
	}

	var errCount int
	for _, userID := range recipients {
		if _, err := bot.SendMessageWithContext(ctx, userID, msg, opts); err != nil {
			log.Printf("alerter: failed to send to user %d: %v", userID, err)
			errCount++
		}
	}

	if errCount > 0 && errCount == len(recipients) {
		return fmt.Errorf("failed to send alerts to all recipients")
	}

	return nil
}
