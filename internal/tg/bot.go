package tg

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
	"github.com/PaulSonOfLars/gotgbot/v2/ext/handlers"
	"github.com/PaulSonOfLars/gotgbot/v2/ext/handlers/filters/callbackquery"
	"github.com/PaulSonOfLars/gotgbot/v2/ext/handlers/filters/message"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
)

type awaitKind int

const (
	awaitNone awaitKind = iota
	awaitFIO
	awaitWords
	awaitTestName
	awaitTestURL
)

type Bot struct {
	cfg     *config.Config
	st      *store.Store
	loc     *time.Location
	api     *gotgbot.Bot
	updater *ext.Updater

	mu       sync.Mutex
	t15Mu    sync.Mutex
	lastT15  map[int64]int64 // telegram id → last T-15 lesson id
	awaiting map[int64]awaitKind
	live     map[int64]liveSnap
	testLive *testLiveSnap
	webURL   string
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
			logx.Errorf("tg", "handler: %v", err)
			return ext.DispatcherActionNoop
		},
	})
	updater := ext.NewUpdater(dispatcher, nil)

	b := &Bot{
		cfg:      cfg,
		st:       st,
		loc:      loc,
		api:      api,
		updater:  updater,
		lastT15:  make(map[int64]int64),
		awaiting: make(map[int64]awaitKind),
		live:     make(map[int64]liveSnap),
	}

	dispatcher.AddHandler(handlers.NewCommand("start", b.onStart))
	dispatcher.AddHandler(handlers.NewCommand("cancel", b.onCancel))
	dispatcher.AddHandler(handlers.NewCommand("panel", b.onPanel))
	dispatcher.AddHandler(handlers.NewCommand("test", b.onTest))
	dispatcher.AddHandler(handlers.NewCommand("help", b.onHelp))
	dispatcher.AddHandler(handlers.NewCommand("today", b.onToday))
	dispatcher.AddHandler(handlers.NewCommand("notes", b.onNotes))
	dispatcher.AddHandler(handlers.NewCommand("settings", b.onSettings))
	dispatcher.AddHandler(handlers.NewCommand("words", b.onWords))
	dispatcher.AddHandler(handlers.NewCommand("link", b.onLink))
	dispatcher.AddHandler(handlers.NewCommand("links", b.onLink))
	dispatcher.AddHandler(handlers.NewCallback(callbackquery.Prefix("j:"), b.onJoinCallback))
	dispatcher.AddHandler(handlers.NewCallback(callbackquery.Prefix("x:"), b.onLeaveCallback))
	dispatcher.AddHandler(handlers.NewCallback(callbackquery.Prefix("ob:"), b.onOnboardCallback))
	dispatcher.AddHandler(handlers.NewCallback(callbackquery.Prefix("st:"), b.onSettingsCallback))
	dispatcher.AddHandler(handlers.NewCallback(callbackquery.Prefix("tx:"), b.onTestCallback))
	dispatcher.AddHandler(handlers.NewCallback(callbackquery.Prefix("nt:"), b.onNotesCallback))
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

	b.publishProfile()
	logx.Infof("tg", "webapp=%s", b.webAppURL())

	t15Ctx, cancelT15 := context.WithCancel(ctx)
	defer cancelT15()
	go b.t15Loop(t15Ctx)
	go b.liveLoop(t15Ctx)
	go b.webappLoop(t15Ctx)

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
	if opts == nil {
		opts = &gotgbot.SendMessageOpts{}
	}
	if opts.ParseMode == "" {
		opts.ParseMode = htmlMode
	}
	_, err := b.api.SendMessage(chatID, text, opts)
	return err
}

func (b *Bot) sendMain(chatID int64, text string) error {
	return b.send(chatID, text, &gotgbot.SendMessageOpts{ReplyMarkup: mainKeyboard()})
}

func (b *Bot) sendInline(chatID int64, text string, mk gotgbot.InlineKeyboardMarkup) error {
	return b.send(chatID, text, &gotgbot.SendMessageOpts{ReplyMarkup: mk})
}

func (b *Bot) webAppURL() string {
	if b == nil || b.cfg == nil {
		return ""
	}
	u := b.cfg.ResolveWebAppURL()
	if u == "" {
		return ""
	}
	return u + "/"
}

func (b *Bot) webappLoop(ctx context.Context) {
	b.syncWebAppURL()
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.syncWebAppURL()
		}
	}
}

func (b *Bot) syncWebAppURL() {
	u := b.webAppURL()
	b.mu.Lock()
	prev := b.webURL
	b.webURL = u
	b.mu.Unlock()
	if u == "" || u == prev {
		return
	}
	b.setAdminMenuButton()
	logx.Infof("tg", "webapp url=%s", u)
}

func (b *Bot) publishProfile() {
	cmds := []gotgbot.BotCommand{
		{Command: "start", Description: "Пары и статус"},
		{Command: "today", Description: "Пары на сегодня"},
		{Command: "notes", Description: "Конспекты PDF"},
		{Command: "settings", Description: "Профиль: имя, подгруппа, пинг"},
		{Command: "cancel", Description: "Отменить ввод"},
		{Command: "help", Description: "Как это работает"},
	}
	if _, err := b.api.SetMyCommands(cmds, nil); err != nil {
		logx.Warnf("tg", "setMyCommands: %v", err)
	}
	if admin := b.cfg.AdminID; admin != 0 {
		adminCmds := append(append([]gotgbot.BotCommand{}, cmds...),
			gotgbot.BotCommand{Command: "test", Description: "Тест BBB: ссылка, болванчик, звук"},
			gotgbot.BotCommand{Command: "panel", Description: "Админ-панель"},
		)
		if _, err := b.api.SetMyCommands(adminCmds, &gotgbot.SetMyCommandsOpts{
			Scope: gotgbot.BotCommandScopeChat{ChatId: admin},
		}); err != nil {
			logx.Warnf("tg", "setMyCommands admin: %v", err)
		}
	}
	if _, err := b.api.SetChatMenuButton(&gotgbot.SetChatMenuButtonOpts{
		MenuButton: gotgbot.MenuButtonCommands{},
	}); err != nil {
		logx.Warnf("tg", "setChatMenuButton default: %v", err)
	}
	if _, err := b.api.SetMyShortDescription(&gotgbot.SetMyShortDescriptionOpts{
		ShortDescription: botShortDesc,
	}); err != nil {
		logx.Warnf("tg", "setMyShortDescription: %v", err)
	}
	if _, err := b.api.SetMyDescription(&gotgbot.SetMyDescriptionOpts{
		Description: botDescription,
	}); err != nil {
		logx.Warnf("tg", "setMyDescription: %v", err)
	}
	b.setAdminMenuButton()
}

func (b *Bot) setAdminMenuButton() {
	url := b.webAppURL()
	if url == "" {
		return
	}
	b.mu.Lock()
	b.webURL = url
	b.mu.Unlock()
	admin := b.cfg.AdminID
	_, err := b.api.SetChatMenuButton(&gotgbot.SetChatMenuButtonOpts{
		ChatId: &admin,
		MenuButton: gotgbot.MenuButtonWebApp{
			Text:   "Панель",
			WebApp: gotgbot.WebAppInfo{Url: url},
		},
	})
	if err != nil {
		logx.Warnf("tg", "setChatMenuButton: %v", err)
	}
}

func (b *Bot) setAwait(id int64, kind awaitKind) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if kind == awaitNone {
		delete(b.awaiting, id)
		return
	}
	b.awaiting[id] = kind
}

func (b *Bot) peekAwait(id int64) awaitKind {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.awaiting[id]
}

func (b *Bot) clearAwait(id int64) {
	b.setAwait(id, awaitNone)
}

func (b *Bot) panelMarkup() *gotgbot.InlineKeyboardMarkup {
	url := b.webAppURL()
	if url == "" {
		return nil
	}
	return &gotgbot.InlineKeyboardMarkup{
		InlineKeyboard: [][]gotgbot.InlineKeyboardButton{{{
			Text:   "Панель",
			WebApp: &gotgbot.WebAppInfo{Url: url},
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
