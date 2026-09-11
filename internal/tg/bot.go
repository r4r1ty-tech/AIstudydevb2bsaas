package tg

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
	"github.com/PaulSonOfLars/gotgbot/v2/ext/handlers"
	"github.com/PaulSonOfLars/gotgbot/v2/ext/handlers/filters/callbackquery"
	"github.com/PaulSonOfLars/gotgbot/v2/ext/handlers/filters/message"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
)

type Bot struct {
	cfg     *config.Config
	st      *store.Store
	loc     *time.Location
	api     *gotgbot.Bot
	updater *ext.Updater

	mu      sync.Mutex
	t15Mu   sync.Mutex
	lastT15 map[int64]int64 // telegram id → last T-15 lesson id
}

func New(cfg *config.Config, st *store.Store, loc *time.Location) (*Bot, error) {
	if cfg == nil {
		return nil, fmt.Errorf("tg: nil config")
	}
	if st == nil {
		return nil, fmt.Errorf("tg: nil store")
	}
	if loc == nil {
		var err error
		loc, err = time.LoadLocation(config.DefaultTimezone)
		if err != nil || loc == nil {
			loc = time.FixedZone("Samara", 4*3600)
		}
	}

	api, err := gotgbot.NewBot(cfg.BotToken, nil)
	if err != nil {
		return nil, fmt.Errorf("tg: new bot: %w", err)
	}

	dispatcher := ext.NewDispatcher(&ext.DispatcherOpts{
		Error: func(_ *gotgbot.Bot, _ *ext.Context, err error) ext.DispatcherAction {
			log.Printf("tg: handler: %v", err)
			return ext.DispatcherActionNoop
		},
	})
	updater := ext.NewUpdater(dispatcher, nil)

	b := &Bot{
		cfg:     cfg,
		st:      st,
		loc:     loc,
		api:     api,
		updater: updater,
		lastT15: make(map[int64]int64),
	}

	dispatcher.AddHandler(handlers.NewCommand("start", b.onStart))
	dispatcher.AddHandler(handlers.NewCommand("panel", b.onPanel))
	dispatcher.AddHandler(handlers.NewCallback(callbackquery.Prefix("j:"), b.onJoinCallback))
	dispatcher.AddHandler(handlers.NewMessage(message.Text, b.onText))

	return b, nil
}

func (b *Bot) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	err := b.updater.StartPolling(b.api, &ext.PollingOpts{
		DropPendingUpdates: true,
		GetUpdatesOpts: &gotgbot.GetUpdatesOpts{
			Timeout:     30,
			RequestOpts: &gotgbot.RequestOpts{Timeout: 35 * time.Second},
		},
	})
	if err != nil {
		return fmt.Errorf("tg: polling: %w", err)
	}

	b.setAdminMenuButton()

	t15Ctx, cancelT15 := context.WithCancel(ctx)
	defer cancelT15()
	go b.t15Loop(t15Ctx)

	<-ctx.Done()
	cancelT15()
	if err := b.updater.Stop(); err != nil {
		return err
	}
	return nil
}

func (b *Bot) NotifyAdmin(ctx context.Context, text string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	_, err := b.api.SendMessageWithContext(ctx, b.cfg.AdminID, text, nil)
	return err
}

func (b *Bot) now() time.Time {
	return time.Now().In(b.loc)
}

func (b *Bot) allowed(ctx *ext.Context) *gotgbot.User {
	if ctx == nil || ctx.EffectiveUser == nil {
		return nil
	}
	if !b.cfg.IsAllowed(ctx.EffectiveUser.Id) {
		return nil
	}
	return ctx.EffectiveUser
}

func (b *Bot) send(chatID int64, text string, opts *gotgbot.SendMessageOpts) error {
	_, err := b.api.SendMessage(chatID, text, opts)
	return err
}

func (b *Bot) setAdminMenuButton() {
	if b.cfg.WebAppURL == "" {
		return
	}
	admin := b.cfg.AdminID
	_, _ = b.api.SetChatMenuButton(&gotgbot.SetChatMenuButtonOpts{
		ChatId: &admin,
		MenuButton: gotgbot.MenuButtonWebApp{
			Text:   "Панель",
			WebApp: gotgbot.WebAppInfo{Url: b.cfg.WebAppURL},
		},
	})
}

func (b *Bot) panelMarkup() *gotgbot.InlineKeyboardMarkup {
	if b.cfg.WebAppURL == "" {
		return nil
	}
	return &gotgbot.InlineKeyboardMarkup{
		InlineKeyboard: [][]gotgbot.InlineKeyboardButton{{{
			Text:   "Панель",
			WebApp: &gotgbot.WebAppInfo{Url: b.cfg.WebAppURL},
		}}},
	}
}

func (b *Bot) rememberT15(telegramID, lessonID int64) {
	b.mu.Lock()
	b.lastT15[telegramID] = lessonID
	b.mu.Unlock()
}

func (b *Bot) lastT15Lesson(telegramID int64) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastT15[telegramID]
}
