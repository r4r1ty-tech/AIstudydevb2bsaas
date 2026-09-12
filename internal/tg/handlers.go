package tg

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

const (
	introText   = "Этот бот заходит на онлайн-лекции вместо тебя: в BBB в списке будет твоё ФИО, как в журнале. На паре слушает вейкворды и пишет в личку. Это не публичный сервис — только наша группа.\n\nКак тебя записать в BBB? Строка как в журнале (Фамилия Имя Отчество)."
	askFIO      = "Как тебя записать в BBB? Строка как в журнале (Фамилия Имя Отчество)."
	askSub      = "Какая подгруппа? 1 или 2, по умолчанию 1."
	whitelisted = "Ты в вайтлисте, профиль есть."
	askBBBLink  = "Кинь ссылку на подключение (bbb.ssau.ru/b/…). Без неё не зайду."
)

func (b *Bot) onStart(_ *gotgbot.Bot, ctx *ext.Context) error {
	from := b.allowed(ctx)
	if from == nil {
		return nil
	}
	chatID := from.Id
	if ctx.EffectiveChat != nil {
		chatID = ctx.EffectiveChat.Id
	}

	u, err := b.st.GetUser(from.Id)
	if err != nil {
		return err
	}
	if u == nil {
		u = &model.User{
			TelegramID: from.Id,
			Username:   from.Username,
			FirstName:  from.FirstName,
			LastName:   from.LastName,
			Enabled:    true,
			Subgroup:   1,
			Onboarded:  false,
			CreatedAt:  b.now(),
		}
		if err := b.st.UpsertUser(u); err != nil {
			return err
		}
		return b.send(chatID, introText, nil)
	}

	u.Username = from.Username
	u.FirstName = from.FirstName
	u.LastName = from.LastName
	if err := b.st.UpsertUser(u); err != nil {
		return err
	}

	if u.Onboarded {
		kb := b.MainReplyKeyboard()
		return b.send(chatID, whitelisted, &gotgbot.SendMessageOpts{ReplyMarkup: kb})
	}
	if strings.TrimSpace(u.FIO) == "" {
		return b.send(chatID, askFIO, nil)
	}
	return b.send(chatID, askSub, nil)
}

func (b *Bot) adminStartOpts(userID int64) *gotgbot.SendMessageOpts {
	if !b.cfg.IsAdmin(userID) || b.cfg.WebAppURL == "" {
		return nil
	}
	b.setAdminMenuButton()
	mk := b.panelMarkup()
	if mk == nil {
		return nil
	}
	return &gotgbot.SendMessageOpts{ReplyMarkup: *mk}
}

func (b *Bot) onPanel(_ *gotgbot.Bot, ctx *ext.Context) error {
	from := b.allowed(ctx)
	if from == nil || !b.cfg.IsAdmin(from.Id) {
		return nil
	}
	chatID := from.Id
	if ctx.EffectiveChat != nil {
		chatID = ctx.EffectiveChat.Id
	}
	if b.cfg.WebAppURL == "" {
		return b.send(chatID, "Поставь WEBAPP_PUBLIC_URL в .env", nil)
	}
	mk := b.panelMarkup()
	b.setAdminMenuButton()
	return b.send(chatID, "Панель", &gotgbot.SendMessageOpts{ReplyMarkup: *mk})
}

func (b *Bot) onText(_ *gotgbot.Bot, ctx *ext.Context) error {
	from := b.allowed(ctx)
	if from == nil || ctx.EffectiveMessage == nil {
		return nil
	}
	text := strings.TrimSpace(ctx.EffectiveMessage.GetText())
	chatID := ctx.EffectiveMessage.Chat.Id

	switch {
	case strings.Contains(text, "Сегодня"):
		return b.handleTodaySchedule(chatID)
	case strings.Contains(text, "Ссылки"):
		return b.handleSavedLinks(chatID)
	case strings.Contains(text, "Архив") || strings.Contains(text, "Записи"):
		return b.showDisciplinesMenu(chatID)
	case strings.Contains(text, "Настройки"):
		return b.handleUserSettings(from.Id, chatID)
	case strings.Contains(text, "Помощь") || strings.Contains(text, "Справка"):
		return b.send(chatID, introText, nil)
	}

	if url := extractBBBURL(text); url != "" {
		return b.handleBBBURL(from.Id, chatID, url)
	}
	if isCommandText(text) {
		return nil
	}

	u, err := b.st.GetUser(from.Id)
	if err != nil {
		return err
	}
	if u == nil || u.Onboarded {
		return nil
	}
	return b.continueOnboarding(u, chatID, text)
}

func (b *Bot) continueOnboarding(u *model.User, chatID int64, text string) error {
	if strings.TrimSpace(u.FIO) == "" {
		fio := strings.TrimSpace(text)
		if fio == "" {
			return b.send(chatID, askFIO, nil)
		}
		if err := b.st.SetFIO(u.TelegramID, fio); err != nil {
			return err
		}
		return b.send(chatID, askSub, nil)
	}

	n, ok := parseSubgroup(text)
	if !ok {
		return b.send(chatID, askSub, nil)
	}
	if err := b.st.SetSubgroup(u.TelegramID, n); err != nil {
		return err
	}
	fresh, err := b.st.GetUser(u.TelegramID)
	if err != nil {
		return err
	}
	if fresh == nil {
		fresh = u
		fresh.Subgroup = n
	}
	fresh.Onboarded = true
	if err := b.st.UpsertUser(fresh); err != nil {
		return err
	}
	if err := b.st.AddEvent(model.Event{
		At:         b.now(),
		Type:       model.EventOnboard,
		TelegramID: u.TelegramID,
		Message:    fresh.FIO,
	}); err != nil {
		return err
	}
	return b.send(chatID, fmt.Sprintf("Ок, %s, подгруппа %d.", fresh.FIO, n), nil)
}

func (b *Bot) handleBBBURL(userID, chatID int64, url string) error {
	u, err := b.st.GetUser(userID)
	if err != nil {
		return err
	}
	if u == nil {
		u = &model.User{TelegramID: userID, Subgroup: 1}
	}
	lesson := b.pickBBBTarget(u, b.now())
	if lesson == nil {
		return nil
	}
	key := model.BBBKey(b.cfg.GroupID, lesson.Discipline, lesson.Teacher)
	if err := b.st.SetBBB(key, url); err != nil {
		return err
	}
	if err := b.st.AddEvent(model.Event{
		At:         b.now(),
		Type:       "bbb",
		TelegramID: userID,
		LessonID:   lesson.ID,
		Message:    url,
	}); err != nil {
		return err
	}
	return b.send(chatID, fmt.Sprintf("запомнил ссылку на %s", lesson.Discipline), nil)
}

func (b *Bot) pickBBBTarget(u *model.User, now time.Time) *model.Lesson {
	lessons, err := b.st.ListLessons()
	if err != nil {
		lessons = nil
	}
	hasLink := func(discipline, teacher string) bool {
		link, err := b.st.GetBBB(model.BBBKey(b.cfg.GroupID, discipline, teacher))
		return err == nil && link != nil && strings.TrimSpace(link.URL) != ""
	}
	intents := make(map[int64]time.Time)
	for _, l := range lessons {
		in, err := b.st.GetIntent(u.TelegramID, l.ID)
		if err == nil && in != nil {
			intents[l.ID] = in.AskedAt
		}
	}
	if lid := b.lastT15Lesson(u.TelegramID); lid != 0 {
		if _, ok := intents[lid]; !ok {
			intents[lid] = now
		}
	}
	if got := pickLessonForBBB(now, u.Subgroup, lessons, hasLink, intents); got != nil {
		return got
	}
	if lid := b.lastT15Lesson(u.TelegramID); lid != 0 {
		l, err := b.st.LessonByID(lid)
		if err == nil && l != nil {
			return l
		}
	}
	return nil
}

func (b *Bot) onJoinCallback(bot *gotgbot.Bot, ctx *ext.Context) error {
	from := b.allowed(ctx)
	if from == nil || ctx.CallbackQuery == nil {
		return nil
	}
	_, _ = ctx.CallbackQuery.Answer(bot, nil)

	yes, lessonID, ok := parseJoinCallback(ctx.CallbackQuery.Data)
	if !ok {
		return nil
	}

	decision := model.JoinNo
	reply := "ок, сегодня пропуск"
	if yes {
		decision = model.JoinYes
		reply = "ок, захожу"
	}

	now := b.now()
	err := b.st.SetIntentDecision(from.Id, lessonID, decision)
	if errors.Is(err, sql.ErrNoRows) {
		err = b.st.PutIntent(model.JoinIntent{
			TelegramID: from.Id,
			LessonID:   lessonID,
			Decision:   decision,
			AskedAt:    now,
			DecidedAt:  &now,
		})
	}
	if err != nil {
		return err
	}

	if !yes {
		if err := b.st.AddEvent(model.Event{
			At:         now,
			Type:       model.EventSkip,
			TelegramID: from.Id,
			LessonID:   lessonID,
			Message:    "пропуск",
		}); err != nil {
			return err
		}
	}

	if ctx.CallbackQuery.Message != nil {
		_, _, _ = ctx.CallbackQuery.Message.EditText(bot, &gotgbot.EditMessageTextOpts{
			Text:        reply,
			ReplyMarkup: gotgbot.InlineKeyboardMarkup{InlineKeyboard: [][]gotgbot.InlineKeyboardButton{}},
		})
	}
	return nil
}

func (b *Bot) handleTodaySchedule(chatID int64) error {
	todayStr := b.now().Format("02.01.2006")
	lessons, err := b.st.ListLessons()
	if err != nil {
		return b.send(chatID, "Ошибка получения расписания", nil)
	}

	var todayLessons []model.Lesson
	for _, l := range lessons {
		if l.Date == todayStr {
			todayLessons = append(todayLessons, l)
		}
	}

	if len(todayLessons) == 0 {
		return b.send(chatID, fmt.Sprintf("📅 <b>Расписание на сегодня (%s):</b>\n\nСегодня пар нет или день свободный! 🎉", todayStr), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📅 <b>Расписание на сегодня (%s):</b>\n\n", todayStr))
	for _, l := range todayLessons {
		mode := "Очно"
		if l.Online {
			mode = "💻 Online BBB"
		}
		sb.WriteString(fmt.Sprintf("⏰ <b>%s - %s</b> | %s\n📚 %s (%s)\n👨‍🏫 %s\n\n",
			l.Start, l.End, mode, l.Discipline, l.Type, l.Teacher))
	}

	return b.send(chatID, sb.String(), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
}

func (b *Bot) handleSavedLinks(chatID int64) error {
	lessons, err := b.st.ListLessons()
	if err != nil {
		return b.send(chatID, "Ошибка загрузки ссылок", nil)
	}

	var sb strings.Builder
	sb.WriteString("🔗 <b>Сохраненные ссылки на комнаты BBB:</b>\n\n")

	var count int
	seen := make(map[string]bool)
	for _, l := range lessons {
		if !l.Online {
			continue
		}
		key := model.BBBKey(b.cfg.GroupID, l.Discipline, l.Teacher)
		if seen[key] {
			continue
		}
		seen[key] = true

		link, err := b.st.GetBBB(key)
		if err == nil && link != nil && strings.TrimSpace(link.URL) != "" {
			count++
			sb.WriteString(fmt.Sprintf("📚 <b>%s</b> (%s)\n🔗 <code>%s</code>\n\n", l.Discipline, l.Teacher, link.URL))
		}
	}

	if count == 0 {
		return b.send(chatID, "🔗 Сохраненных ссылок BBB пока нет.\n\nКогда бот спросит ссылку за 15 минут до онлайн-пары, просто пришли её в чат!", nil)
	}

	return b.send(chatID, sb.String(), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
}

func (b *Bot) handleUserSettings(userID int64, chatID int64) error {
	u, err := b.st.GetUser(userID)
	if err != nil || u == nil {
		return b.send(chatID, "Профиль не найден", nil)
	}

	status := "✅ Включен"
	if !u.Enabled {
		status = "⏸️ Выключен"
	}

	socks := u.SOCKS5
	if socks == "" {
		socks = "Не назначен (прямой вход)"
	}

	msg := fmt.Sprintf(`⚙️ <b>Твои настройки профиля:</b>

👤 <b>ФИО в BBB:</b> %s
👥 <b>Подгруппа:</b> %d
📊 <b>Статус захода:</b> %s
🌐 <b>SOCKS5 прокси:</b> <code>%s</code>

💡 <i>Изменить ФИО или подгруппу можно через админ-панель (/panel) или написав администратору.</i>`,
		u.FIO, u.Subgroup, status, socks)

	return b.send(chatID, msg, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
}
