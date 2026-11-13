package tg

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/lk"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodle"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/uni"
)

// Аккаунт универа в боте: подключение ЛК + Moodle, список «Сдать», напоминания о сроках.

const (
	uniBoth   = "both"
	uniMoodle = "moodle"
)

type uniPending struct {
	target string
	login  string
}

var (
	deadlineTick = 5 * time.Minute
	remindStages = []struct {
		name string
		left time.Duration
	}{{"3h", 3 * time.Hour}, {"24h", 24 * time.Hour}}
	loginTimeout = 60 * time.Second
)

func (b *Bot) setUniPending(id int64, p *uniPending) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if p == nil {
		delete(b.uniPend, id)
		return
	}
	b.uniPend[id] = *p
}

func (b *Bot) getUniPending(id int64) (uniPending, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p, ok := b.uniPend[id]
	return p, ok
}

func (b *Bot) onConnect(_ *gotgbot.Bot, ctx *ext.Context) error {
	from := b.allowed(ctx)
	if from == nil || ctx.EffectiveMessage == nil {
		return nil
	}
	target := uniBoth
	if strings.Contains(strings.ToLower(commandPayload(ctx.EffectiveMessage.GetText())), "moodle") {
		target = uniMoodle
	}
	return b.startConnect(ctx.EffectiveMessage.Chat.Id, from.Id, target)
}

func (b *Bot) startConnect(chatID, userID int64, target string) error {
	if b.vault == nil {
		return b.send(chatID, uniNoVault, nil)
	}
	b.setUniPending(userID, &uniPending{target: target})
	b.setAwait(userID, awaitUniLogin)
	text := uniAskLogin
	if target == uniMoodle {
		text = uniAskMoodleLogin
	}
	return b.send(chatID, text, &gotgbot.SendMessageOpts{ParseMode: htmlMode, ReplyMarkup: cancelKeyboard()})
}

func (b *Bot) handleUniLogin(u *model.User, chatID int64, text string) error {
	p, ok := b.getUniPending(u.TelegramID)
	login := strings.TrimSpace(text)
	if !ok || login == "" || strings.ContainsAny(login, " \n") {
		return b.send(chatID, uniBadLoginInput, &gotgbot.SendMessageOpts{ReplyMarkup: cancelKeyboard()})
	}
	p.login = login
	b.setUniPending(u.TelegramID, &p)
	b.setAwait(u.TelegramID, awaitUniPassword)
	return b.send(chatID, uniAskPassword, &gotgbot.SendMessageOpts{ParseMode: htmlMode, ReplyMarkup: cancelKeyboard()})
}

// handleUniPassword — пароль сразу удаляем из чата, проверяем на сайтах и
// сохраняем зашифрованным только туда, куда он подошёл.
func (b *Bot) handleUniPassword(userID int64, msg *gotgbot.Message) error {
	chatID := msg.Chat.Id
	password := msg.GetText()
	if _, err := b.api.DeleteMessage(chatID, msg.MessageId, nil); err != nil {
		logx.Warnf("tg", "uni: delete password message: %v", err)
	}
	p, ok := b.getUniPending(userID)
	b.clearAwait(userID)
	b.setUniPending(userID, nil)
	if !ok || p.login == "" || strings.TrimSpace(password) == "" {
		return b.send(chatID, uniBadLoginInput, nil)
	}
	_ = b.send(chatID, uniChecking, nil)

	ctx, cancel := context.WithTimeout(context.Background(), loginTimeout)
	defer cancel()
	sealedPass, err := b.vault.Seal(password)
	if err != nil {
		logx.Errorf("tg", "uni: seal: %v", err)
		return b.send(chatID, uniNoVault, nil)
	}

	var lines []string
	lkOK, moodleOK := false, false
	if p.target == uniBoth {
		res, err := b.tryLK(ctx, userID, p.login, password, sealedPass)
		lkOK = err == nil
		lines = append(lines, res)
	}
	res, err := b.tryMoodle(ctx, userID, p.login, password, sealedPass)
	moodleOK = err == nil
	lines = append(lines, res)

	if lkOK {
		if err := b.st.RequestRaspRefresh(); err != nil {
			logx.Warnf("tg", "uni: rasp kick: %v", err)
		}
		lines = append(lines, "Расписание пересоберу из ЛК в ближайшую минуту: подгруппы, ИОТ и ссылки на онлайн-пары.")
	}
	if moodleOK {
		if err := uni.Kick(b.st, userID); err != nil {
			logx.Warnf("tg", "uni: moodle kick: %v", err)
		}
		lines = append(lines, "Задания Moodle соберу за пару минут — потом жми «"+btnTasks+"».")
	}
	mk := uniMenuKeyboard(lkOK || p.target == uniMoodle, moodleOK)
	return b.send(chatID, strings.Join(lines, "\n"), &gotgbot.SendMessageOpts{ParseMode: htmlMode, ReplyMarkup: mk})
}

func (b *Bot) tryLK(ctx context.Context, userID int64, login, password, sealedPass string) (string, error) {
	c, err := lk.New(b.cfg.LKBaseURL)
	if err != nil {
		return "ЛК: ❌ " + esc(err.Error()), err
	}
	if err := c.Login(ctx, login, password); err != nil {
		logx.Infof("tg", "uni: lk login tg=%d: %v", userID, err)
		if errors.Is(err, lk.ErrBadLogin) {
			return "ЛК: ❌ не пустил — неверный логин или пароль", err
		}
		return "ЛК: ❌ сайт не ответил, попробуй позже", err
	}
	return "ЛК: ✅ подключён", b.saveAccount(userID, model.AccountLK, login, sealedPass, c.Session())
}

func (b *Bot) tryMoodle(ctx context.Context, userID int64, login, password, sealedPass string) (string, error) {
	c, err := moodle.New(b.cfg.MoodleBaseURL)
	if err != nil {
		return "Moodle: ❌ " + esc(err.Error()), err
	}
	if err := c.Login(ctx, login, password); err != nil {
		logx.Infof("tg", "uni: moodle login tg=%d: %v", userID, err)
		if errors.Is(err, moodle.ErrBadLogin) {
			return "Moodle: ❌ не пустил — если там другой логин, нажми «Другой логин для Moodle»", err
		}
		return "Moodle: ❌ сайт не ответил, попробуй позже", err
	}
	return "Moodle: ✅ подключён", b.saveAccount(userID, model.AccountMoodle, login, sealedPass, c.Session())
}

func (b *Bot) saveAccount(userID int64, kind, login, sealedPass, session string) error {
	sealedSess, err := b.vault.Seal(session)
	if err != nil {
		return err
	}
	return b.st.PutAccount(model.Account{
		TelegramID: userID, Kind: kind, Login: login, Secret: sealedPass, Session: sealedSess, Status: model.AccountPending,
	})
}

// ---------- меню аккаунта ----------

func uniMenuKeyboard(lkOn, moodleOn bool) gotgbot.InlineKeyboardMarkup {
	rows := [][]gotgbot.InlineKeyboardButton{}
	if moodleOn {
		rows = append(rows, []gotgbot.InlineKeyboardButton{{Text: "📋 Что сдать", CallbackData: "un:list"}, {Text: "🔄 Обновить", CallbackData: "un:sync"}})
	}
	label := "Подключить ЛК и Moodle"
	if lkOn || moodleOn {
		label = "Сменить логин/пароль"
	}
	rows = append(rows, []gotgbot.InlineKeyboardButton{{Text: label, CallbackData: "un:connect"}})
	rows = append(rows, []gotgbot.InlineKeyboardButton{{Text: "Другой логин для Moodle", CallbackData: "un:moodle"}})
	if lkOn || moodleOn {
		rows = append(rows, []gotgbot.InlineKeyboardButton{{Text: "Отключить и забыть пароль", CallbackData: "un:off"}})
	}
	return gotgbot.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func accountLine(name string, a *model.Account, loc *time.Location) string {
	if a == nil {
		return name + ": не подключён"
	}
	s := name + ": " + esc(a.Login) + " — "
	switch a.Status {
	case model.AccountOK:
		s += "✅ работает"
	case model.AccountBadLogin:
		s += "❌ пароль больше не подходит"
	case model.AccountError:
		s += "⚠️ " + esc(a.Message)
	default:
		s += "⏳ первый синк"
	}
	if a.SyncedAt != nil {
		s += ", обновлено " + a.SyncedAt.In(loc).Format("02.01 15:04")
	}
	return s
}

func (b *Bot) sendUniMenu(chatID, userID int64) error {
	lkAcc, err := b.st.GetAccount(userID, model.AccountLK)
	if err != nil {
		return err
	}
	mAcc, err := b.st.GetAccount(userID, model.AccountMoodle)
	if err != nil {
		return err
	}
	text := "<b>Аккаунт универа</b>\n" + accountLine("ЛК", lkAcc, b.loc) + "\n" + accountLine("Moodle", mAcc, b.loc) + "\n\n" + uniMenuHint
	return b.send(chatID, text, &gotgbot.SendMessageOpts{ParseMode: htmlMode, ReplyMarkup: uniMenuKeyboard(lkAcc != nil, mAcc != nil)})
}

func (b *Bot) onUniCallback(bot *gotgbot.Bot, ctx *ext.Context) error {
	if ctx == nil || ctx.CallbackQuery == nil {
		return nil
	}
	from := b.allowed(ctx)
	if from == nil {
		answerToast(bot, ctx, "")
		return nil
	}
	data := strings.TrimPrefix(ctx.CallbackQuery.Data, "un:")
	chatID := from.Id
	switch {
	case data == "menu":
		answerToast(bot, ctx, "")
		return b.sendUniMenu(chatID, from.Id)
	case data == "connect":
		answerToast(bot, ctx, "")
		return b.startConnect(chatID, from.Id, uniBoth)
	case data == "moodle":
		answerToast(bot, ctx, "")
		return b.startConnect(chatID, from.Id, uniMoodle)
	case data == "sync":
		if err := uni.Kick(b.st, from.Id); err != nil {
			answerToast(bot, ctx, "Не получилось")
			return err
		}
		answerToast(bot, ctx, "Обновлю задания в ближайшие минуты")
		return nil
	case data == "list":
		answerToast(bot, ctx, "")
		return b.sendTasks(chatID, from.Id)
	case data == "off":
		for _, k := range []string{model.AccountLK, model.AccountMoodle} {
			if err := b.st.DeleteAccount(from.Id, k); err != nil {
				answerToast(bot, ctx, "Не получилось")
				return err
			}
		}
		answerToast(bot, ctx, "Отключил")
		return b.send(chatID, "Отключил ЛК и Moodle, пароль и задания удалены.", nil)
	case strings.HasPrefix(data, "i:"):
		answerToast(bot, ctx, "")
		cmid, _ := strconv.ParseInt(strings.TrimPrefix(data, "i:"), 10, 64)
		return b.sendTask(chatID, from.Id, cmid)
	}
	answerToast(bot, ctx, "")
	return nil
}

// ---------- «Сдать» ----------

func (b *Bot) onTasks(_ *gotgbot.Bot, ctx *ext.Context) error {
	from := b.allowed(ctx)
	if from == nil || ctx.EffectiveMessage == nil {
		return nil
	}
	return b.sendTasks(ctx.EffectiveMessage.Chat.Id, from.Id)
}

// openTasks — несданное: просрочка за 14 дней, всё со сроком впереди, потом без срока.
func openTasks(items []model.MoodleItem, now time.Time) (overdue, upcoming, noDue []model.MoodleItem) {
	for _, it := range items {
		if it.Finished() {
			continue
		}
		switch {
		case it.Due == nil:
			if it.Opens == nil || it.Opens.Before(now) {
				noDue = append(noDue, it)
			}
		case it.Due.Before(now):
			if now.Sub(*it.Due) <= 14*24*time.Hour {
				overdue = append(overdue, it)
			}
		default:
			upcoming = append(upcoming, it)
		}
	}
	return
}

func (b *Bot) sendTasks(chatID, userID int64) error {
	acc, err := b.st.GetAccount(userID, model.AccountMoodle)
	if err != nil {
		return err
	}
	if acc == nil {
		return b.send(chatID, uniNeedMoodle, &gotgbot.SendMessageOpts{ReplyMarkup: uniMenuKeyboard(false, false)})
	}
	items, err := b.st.ListMoodleItems(userID)
	if err != nil {
		return err
	}
	now := b.now()
	overdue, upcoming, noDue := openTasks(items, now)
	var sb strings.Builder
	sb.WriteString("<b>Что сдать</b>")
	if acc.SyncedAt != nil {
		sb.WriteString(" · обновлено " + acc.SyncedAt.In(b.loc).Format("02.01 15:04"))
	}
	var buttons [][]gotgbot.InlineKeyboardButton
	n := 0
	section := func(title string, list []model.MoodleItem, limit int) {
		if len(list) == 0 {
			return
		}
		sb.WriteString("\n\n<b>" + title + "</b>")
		for i, it := range list {
			if i >= limit {
				sb.WriteString(fmt.Sprintf("\n… и ещё %d", len(list)-limit))
				break
			}
			n++
			line := fmt.Sprintf("\n%d. ", n)
			if it.Due != nil {
				line += it.Due.In(b.loc).Format("02.01 15:04") + " · "
			}
			line += uni.KindLabel(it.Kind) + " · " + esc(shortCourse(it.Course)) + " — " + esc(it.Title)
			if it.Status == model.MoodleDraft {
				line += " ✏️"
			}
			if it.Due != nil {
				line += " <i>(" + uni.Until(*it.Due, now) + ")</i>"
			}
			sb.WriteString(line)
			if len(buttons) < 12 {
				buttons = append(buttons, []gotgbot.InlineKeyboardButton{{
					Text:         fmt.Sprintf("%d. %s", n, trimRunes(it.Title, 40)),
					CallbackData: "un:i:" + strconv.FormatInt(it.CMID, 10),
				}})
			}
		}
	}
	section("Просрочено", overdue, 10)
	section("Впереди", upcoming, 15)
	section("Без срока", noDue, 5)
	if n == 0 {
		if len(items) == 0 && acc.SyncedAt == nil {
			sb.WriteString("\n\nЕщё собираю задания из Moodle — загляни через пару минут.")
		} else {
			sb.WriteString("\n\nВсё сдано 🎉")
		}
	} else {
		sb.WriteString("\n\nНажми на пункт — покажу, что именно надо сделать.")
	}
	buttons = append(buttons, []gotgbot.InlineKeyboardButton{{Text: "🔄 Обновить", CallbackData: "un:sync"}})
	return b.send(chatID, sb.String(), &gotgbot.SendMessageOpts{
		ParseMode:          htmlMode,
		ReplyMarkup:        gotgbot.InlineKeyboardMarkup{InlineKeyboard: buttons},
		LinkPreviewOptions: &gotgbot.LinkPreviewOptions{IsDisabled: true},
	})
}

func (b *Bot) sendTask(chatID, userID, cmid int64) error {
	items, err := b.st.ListMoodleItems(userID)
	if err != nil {
		return err
	}
	for _, it := range items {
		if it.CMID == cmid {
			return b.send(chatID, uni.FormatItem(it, b.now(), b.loc), &gotgbot.SendMessageOpts{
				ParseMode:          htmlMode,
				LinkPreviewOptions: &gotgbot.LinkPreviewOptions{IsDisabled: true},
			})
		}
	}
	return b.send(chatID, "Этого задания уже нет в списке.", nil)
}

func shortCourse(s string) string { return trimRunes(s, 32) }

func trimRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n-1]) + "…"
}

// ---------- напоминания ----------

func (b *Bot) deadlineLoop(ctx context.Context) {
	t := time.NewTicker(deadlineTick)
	defer t.Stop()
	for {
		b.remindDeadlines(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// remindDeadlines — за сутки и за 3 часа до срока несданного, по одному разу.
func (b *Bot) remindDeadlines(ctx context.Context) {
	accs, err := b.st.ListAccounts(model.AccountMoodle)
	if err != nil {
		logx.Warnf("tg", "remind: accounts: %v", err)
		return
	}
	now := b.now()
	for _, acc := range accs {
		if ctx.Err() != nil {
			return
		}
		items, err := b.st.ListMoodleItems(acc.TelegramID)
		if err != nil {
			continue
		}
		for _, it := range items {
			if it.Finished() || it.Due == nil || !it.Due.After(now) {
				continue
			}
			left := it.Due.Sub(now)
			for _, st := range remindStages {
				if left > st.left {
					continue
				}
				key := fmt.Sprintf("dl_sent:%d:%d:%s:%d", acc.TelegramID, it.CMID, st.name, it.Due.Unix())
				if v, ok, _ := b.st.GetSetting(key); ok && v != "" {
					break
				}
				text := "⏰ <b>Срок " + uni.Until(*it.Due, now) + "</b>\n" + uni.FormatItem(it, now, b.loc)
				if err := b.send(acc.TelegramID, text, &gotgbot.SendMessageOpts{
					ParseMode:          htmlMode,
					LinkPreviewOptions: &gotgbot.LinkPreviewOptions{IsDisabled: true},
				}); err != nil {
					logx.Warnf("tg", "remind tg=%d cmid=%d: %v", acc.TelegramID, it.CMID, err)
					break
				}
				// Отметить этот этап и более ранние (24 ч после 3 ч уже не нужен).
				for _, all := range remindStages {
					if all.left < st.left {
						continue
					}
					_ = b.st.SetSetting(fmt.Sprintf("dl_sent:%d:%d:%s:%d", acc.TelegramID, it.CMID, all.name, it.Due.Unix()), "1")
				}
				break
			}
		}
	}
}
