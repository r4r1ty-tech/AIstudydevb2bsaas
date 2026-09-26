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
	logx.Debugf("tg", "New: enter")
	if cfg == nil {
		logx.Errorf("tg", "New: nil config")
		return nil, fmt.Errorf("tg: nil config")
	}
	if st == nil {
		logx.Errorf("tg", "New: nil store")
		return nil, fmt.Errorf("tg: nil store")
	}
	if loc == nil {
		var err error
		loc, err = time.LoadLocation(config.DefaultTimezone)
		if err != nil || loc == nil {
			logx.Warnf("tg", "New: load timezone %q: %v", config.DefaultTimezone, err)
			loc = time.FixedZone("Samara", 4*3600)
		}
	}

	api, err := gotgbot.NewBot(cfg.BotToken, nil)
	if err != nil {
		logx.Errorf("tg", "New: new bot: %v", err)
		return nil, fmt.Errorf("tg: new bot: %w", err)
	}
	logx.Debugf("tg", "New: bot user=@%s id=%d", api.Username, api.Id)

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

	logx.Debugf("tg", "New: handlers registered")
	return b, nil
}

func (b *Bot) Start(ctx context.Context) error {
	logx.Debugf("tg", "Start: enter")
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		logx.Errorf("tg", "Start: ctx: %v", err)
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
		logx.Errorf("tg", "Start: polling: %v", err)
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
	logx.Debugf("tg", "Start: ctx done, stopping")
	cancelT15()
	if err := b.updater.Stop(); err != nil {
		logx.Errorf("tg", "Start: stop: %v", err)
		return err
	}
	logx.Debugf("tg", "Start: exit")
	return nil
}

func (b *Bot) NotifyAdmin(ctx context.Context, text string) error {
	logx.Debugf("tg", "NotifyAdmin: admin=%d len=%d", b.cfg.AdminID, len(text))
	if ctx == nil {
		ctx = context.Background()
	}
	_, err := b.api.SendMessageWithContext(ctx, b.cfg.AdminID, text, nil)
	if err != nil {
		logx.Errorf("tg", "NotifyAdmin: admin=%d: %v", b.cfg.AdminID, err)
		return fmt.Errorf("tg: notify admin: %w", err)
	}
	logx.Debugf("tg", "NotifyAdmin: sent admin=%d", b.cfg.AdminID)
	return nil
}

func (b *Bot) now() time.Time {
	t := time.Now().In(b.loc)
	logx.Debugf("tg", "now: %s", t.Format(time.RFC3339))
	return t
}

func (b *Bot) allowed(ctx *ext.Context) *gotgbot.User {
	if ctx == nil || ctx.EffectiveUser == nil {
		logx.Debugf("tg", "allowed: no effective user")
		return nil
	}
	u := ctx.EffectiveUser
	if !b.cfg.IsAllowed(u.Id) && !b.cfg.IsAdmin(u.Id) {
		logx.Warnf("tg", "allowed: rejected tg=%d username=%q", u.Id, u.Username)
		return nil
	}
	logx.Debugf("tg", "allowed: ok tg=%d username=%q", u.Id, u.Username)
	return u
}

func (b *Bot) send(chatID int64, text string, opts *gotgbot.SendMessageOpts) error {
	logx.Debugf("tg", "send: chat=%d len=%d", chatID, len(text))
	if opts == nil {
		opts = &gotgbot.SendMessageOpts{}
	}
	if opts.ParseMode == "" {
		opts.ParseMode = htmlMode
	}
	_, err := b.api.SendMessage(chatID, text, opts)
	if err != nil {
		logx.Errorf("tg", "send: chat=%d: %v", chatID, err)
		return err
	}
	logx.Debugf("tg", "send: ok chat=%d", chatID)
	return nil
}

func (b *Bot) sendMain(chatID int64, text string) error {
	logx.Debugf("tg", "sendMain: chat=%d", chatID)
	if err := b.send(chatID, text, &gotgbot.SendMessageOpts{ReplyMarkup: mainKeyboard()}); err != nil {
		logx.Errorf("tg", "sendMain: chat=%d: %v", chatID, err)
		return err
	}
	return nil
}

func (b *Bot) sendInline(chatID int64, text string, mk gotgbot.InlineKeyboardMarkup) error {
	logx.Debugf("tg", "sendInline: chat=%d buttons=%d", chatID, len(mk.InlineKeyboard))
	if err := b.send(chatID, text, &gotgbot.SendMessageOpts{ReplyMarkup: mk}); err != nil {
		logx.Errorf("tg", "sendInline: chat=%d: %v", chatID, err)
		return err
	}
	return nil
}

func (b *Bot) webAppURL() string {
	if b == nil || b.cfg == nil {
		logx.Debugf("tg", "webAppURL: nil bot/cfg")
		return ""
	}
	u := b.cfg.ResolveWebAppURL()
	if u == "" {
		logx.Debugf("tg", "webAppURL: empty")
		return ""
	}
	logx.Debugf("tg", "webAppURL: %s/", u)
	return u + "/"
}

func (b *Bot) webappLoop(ctx context.Context) {
	logx.Debugf("tg", "webappLoop: start")
	b.syncWebAppURL()
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			logx.Debugf("tg", "webappLoop: ctx done")
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
		logx.Debugf("tg", "syncWebAppURL: unchanged url=%q", u)
		return
	}
	logx.Infof("tg", "syncWebAppURL: changed prev=%q url=%q", prev, u)
	b.setAdminMenuButton()
	logx.Infof("tg", "webapp url=%s", u)
}

func (b *Bot) publishProfile() {
	logx.Debugf("tg", "publishProfile: enter")
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
	logx.Debugf("tg", "publishProfile: done")
}

func (b *Bot) setAdminMenuButton() {
	url := b.webAppURL()
	if url == "" {
		logx.Debugf("tg", "setAdminMenuButton: no url")
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
		return
	}
	logx.Debugf("tg", "setAdminMenuButton: admin=%d url=%s", admin, url)
}

func (b *Bot) setAwait(id int64, kind awaitKind) {
	logx.Debugf("tg", "setAwait: tg=%d kind=%d", id, kind)
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
	k := b.awaiting[id]
	logx.Debugf("tg", "peekAwait: tg=%d kind=%d", id, k)
	return k
}

func (b *Bot) clearAwait(id int64) {
	logx.Debugf("tg", "clearAwait: tg=%d", id)
	b.setAwait(id, awaitNone)
}

func (b *Bot) panelMarkup() *gotgbot.InlineKeyboardMarkup {
	url := b.webAppURL()
	if url == "" {
		logx.Debugf("tg", "panelMarkup: no url")
		return nil
	}
	logx.Debugf("tg", "panelMarkup: url=%s", url)
	return &gotgbot.InlineKeyboardMarkup{
		InlineKeyboard: [][]gotgbot.InlineKeyboardButton{{{
			Text:   "Панель",
			WebApp: &gotgbot.WebAppInfo{Url: url},
		}}},
	}
}

func (b *Bot) rememberT15(telegramID, lessonID int64) {
	logx.Debugf("tg", "rememberT15: tg=%d lesson=%d", telegramID, lessonID)
	b.mu.Lock()
	b.lastT15[telegramID] = lessonID
	b.mu.Unlock()
}

func (b *Bot) lastT15Lesson(telegramID int64) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.lastT15[telegramID]
	logx.Debugf("tg", "lastT15Lesson: tg=%d lesson=%d", telegramID, id)
	return id
}
