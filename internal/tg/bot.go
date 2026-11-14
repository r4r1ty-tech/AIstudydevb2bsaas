// Package tg — Telegram-бот. Умеет только две вещи: выдать личный пароль в
// панель и слать уведомления. Всё остальное (пары, решения, профиль, Moodle,
// конспекты, тест BBB) — в панели. Уведомления от bbb и rasp уходят напрямую
// через internal/notify; здесь — напоминания о сроках Moodle и кнопка PDF
// под уведомлением «конспект готов».
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
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/vault"
)

// botOpts is nil in production; tests point it at a fake Bot API.
var botOpts *gotgbot.BotOpts

type Bot struct {
	cfg     *config.Config
	st      *store.Store
	loc     *time.Location
	api     *gotgbot.Bot
	updater *ext.Updater
	disp    *ext.Dispatcher

	mu     sync.Mutex
	webURL string
	vault  *vault.Vault
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
			logx.Warnf("tg", "New: load timezone %q: %v", config.DefaultTimezone, err)
			loc = time.FixedZone("Samara", 4*3600)
		}
	}

	api, err := gotgbot.NewBot(cfg.BotToken, botOpts)
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
	b := &Bot{
		cfg:     cfg,
		st:      st,
		loc:     loc,
		api:     api,
		updater: ext.NewUpdater(dispatcher, nil),
		disp:    dispatcher,
	}
	if cfg.SecretKey != "" || cfg.SecretKeyFile != "" {
		if v, err := vault.Load(cfg.SecretKey, cfg.SecretKeyFile); err != nil {
			logx.Errorf("tg", "New: vault: %v — пароли в панель не выдаются", err)
		} else {
			b.vault = v
		}
	}

	dispatcher.AddHandler(handlers.NewCommand("start", b.onStart))
	dispatcher.AddHandler(handlers.NewCommand("pass", b.onPass))
	dispatcher.AddHandler(handlers.NewCallback(callbackquery.Prefix("nt:"), b.onNotesCallback))
	dispatcher.AddHandler(handlers.NewMessage(message.Text, b.onText))
	return b, nil
}

// Start polls Telegram until ctx is done.
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
		logx.Errorf("tg", "Start: polling: %v", err)
		return fmt.Errorf("tg: polling: %w", err)
	}

	b.publishProfile()
	logx.Infof("tg", "webapp=%s", b.webAppURL())

	loopCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go b.webappLoop(loopCtx)
	go b.deadlineLoop(loopCtx)

	<-ctx.Done()
	cancel()
	if err := b.updater.Stop(); err != nil {
		logx.Errorf("tg", "Start: stop: %v", err)
		return err
	}
	return nil
}

func (b *Bot) NotifyAdmin(ctx context.Context, text string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := b.api.SendMessageWithContext(ctx, b.cfg.AdminID, text, nil); err != nil {
		logx.Errorf("tg", "NotifyAdmin: admin=%d: %v", b.cfg.AdminID, err)
		return fmt.Errorf("tg: notify admin: %w", err)
	}
	return nil
}

func (b *Bot) now() time.Time { return time.Now().In(b.loc) }

// allowed — вайтлист или админ; чужим бот молчит.
func (b *Bot) allowed(ctx *ext.Context) *gotgbot.User {
	if ctx == nil || ctx.EffectiveUser == nil {
		return nil
	}
	u := ctx.EffectiveUser
	if !b.cfg.IsAllowed(u.Id) && !b.cfg.IsAdmin(u.Id) {
		logx.Warnf("tg", "allowed: rejected tg=%d username=%q", u.Id, u.Username)
		return nil
	}
	return u
}

func (b *Bot) send(chatID int64, text string, opts *gotgbot.SendMessageOpts) error {
	if opts == nil {
		opts = &gotgbot.SendMessageOpts{}
	}
	if opts.ParseMode == "" {
		opts.ParseMode = htmlMode
	}
	if _, err := b.api.SendMessage(chatID, text, opts); err != nil {
		logx.Errorf("tg", "send: chat=%d: %v", chatID, err)
		return err
	}
	return nil
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
	logx.Infof("tg", "webapp url=%s", u)
	for _, id := range b.panelIDs() {
		b.setMenuButton(id)
	}
}

// panelIDs — все, кому положена панель: вайтлист и админ.
func (b *Bot) panelIDs() []int64 {
	ids := append([]int64(nil), b.cfg.Whitelist...)
	if b.cfg.AdminID != 0 {
		ids = append(ids, b.cfg.AdminID)
	}
	return ids
}

// setMenuButton ставит человеку кнопку «Панель» слева от поля ввода.
// Тем, кто ещё не писал боту, Telegram откажет — это не ошибка.
func (b *Bot) setMenuButton(id int64) {
	url := b.webAppURL()
	if url == "" || id == 0 {
		return
	}
	if _, err := b.api.SetChatMenuButton(&gotgbot.SetChatMenuButtonOpts{
		ChatId:     &id,
		MenuButton: gotgbot.MenuButtonWebApp{Text: "Панель", WebApp: gotgbot.WebAppInfo{Url: url}},
	}); err != nil {
		logx.Debugf("tg", "setMenuButton tg=%d: %v", id, err)
	}
}

func (b *Bot) publishProfile() {
	cmds := []gotgbot.BotCommand{
		{Command: "pass", Description: "Пароль в панель"},
	}
	if _, err := b.api.SetMyCommands(cmds, nil); err != nil {
		logx.Warnf("tg", "setMyCommands: %v", err)
	}
	// Старая версия ставила админу свой набор (/test, /panel) — убрать.
	if admin := b.cfg.AdminID; admin != 0 {
		if _, err := b.api.DeleteMyCommands(&gotgbot.DeleteMyCommandsOpts{
			Scope: gotgbot.BotCommandScopeChat{ChatId: admin},
		}); err != nil {
			logx.Debugf("tg", "deleteMyCommands admin: %v", err)
		}
	}
	if _, err := b.api.SetMyShortDescription(&gotgbot.SetMyShortDescriptionOpts{ShortDescription: botShortDesc}); err != nil {
		logx.Warnf("tg", "setMyShortDescription: %v", err)
	}
	if _, err := b.api.SetMyDescription(&gotgbot.SetMyDescriptionOpts{Description: botDescription}); err != nil {
		logx.Warnf("tg", "setMyDescription: %v", err)
	}
}
