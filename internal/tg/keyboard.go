package tg

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

func mainKeyboard() gotgbot.ReplyKeyboardMarkup {
	logx.Debugf("tg", "mainKeyboard: build")
	return gotgbot.ReplyKeyboardMarkup{
		Keyboard: [][]gotgbot.KeyboardButton{
			{{Text: btnToday}, {Text: btnNotes}},
			{{Text: btnSettings}},
		},
		IsPersistent:          true,
		ResizeKeyboard:        true,
		InputFieldPlaceholder: inputHint,
	}
}

func subgroupKeyboard() gotgbot.InlineKeyboardMarkup {
	logx.Debugf("tg", "subgroupKeyboard: build")
	return gotgbot.InlineKeyboardMarkup{
		InlineKeyboard: [][]gotgbot.InlineKeyboardButton{{
			{Text: "1", CallbackData: "ob:sub:1"},
			{Text: "2", CallbackData: "ob:sub:2"},
		}},
	}
}

func skipWordsKeyboard() gotgbot.InlineKeyboardMarkup {
	logx.Debugf("tg", "skipWordsKeyboard: build")
	return gotgbot.InlineKeyboardMarkup{
		InlineKeyboard: [][]gotgbot.InlineKeyboardButton{{
			{Text: "Пропустить", CallbackData: "ob:skipw"},
		}},
	}
}

func (b *Bot) testMarkup(j model.TestJoin) gotgbot.InlineKeyboardMarkup {
	logx.Debugf("tg", "testMarkup: status=%s want=%s", j.Status, j.Want)
	kb := testKeyboard(j)
	if b == nil {
		return kb
	}
	url := b.webAppURL()
	if url == "" {
		logx.Debugf("tg", "testMarkup: no webapp url")
		return kb
	}
	kb.InlineKeyboard = append(kb.InlineKeyboard, []gotgbot.InlineKeyboardButton{{
		Text:   "Пульт",
		WebApp: &gotgbot.WebAppInfo{Url: url},
	}})
	return kb
}

func testAlready(j model.TestJoin, want string) bool {
	ok := false
	if j.Want != want {
		ok = false
	} else {
		switch j.Status {
		case model.TestJoining, model.TestLobby, model.TestRoom:
			ok = true
		default:
			ok = false
		}
	}
	logx.Debugf("tg", "testAlready: want=%s current_want=%s status=%s -> %v", want, j.Want, j.Status, ok)
	return ok
}

func testKeyboard(j model.TestJoin) gotgbot.InlineKeyboardMarkup {
	logx.Debugf("tg", "testKeyboard: status=%s want=%s", j.Status, j.Want)
	in := j.Want != model.TestWantOff && (j.Status == model.TestJoining || j.Status == model.TestLobby || j.Status == model.TestRoom)
	nameBtn := gotgbot.InlineKeyboardButton{Text: "Имя", CallbackData: "tx:name"}
	urlBtn := gotgbot.InlineKeyboardButton{Text: "Ссылка", CallbackData: "tx:url"}
	if !in {
		return gotgbot.InlineKeyboardMarkup{
			InlineKeyboard: [][]gotgbot.InlineKeyboardButton{
				{
					{Text: "Болванчик", CallbackData: "tx:dummy"},
					{Text: "Со звуком", CallbackData: "tx:listen"},
				},
				{urlBtn, nameBtn},
			},
		}
	}
	row := []gotgbot.InlineKeyboardButton{{Text: "Выйти", CallbackData: "tx:leave"}}
	if j.Want == model.TestWantListen {
		row = append(row, gotgbot.InlineKeyboardButton{Text: "Болванчик", CallbackData: "tx:dummy"})
	} else {
		row = append(row, gotgbot.InlineKeyboardButton{Text: "Со звуком", CallbackData: "tx:listen"})
	}
	return gotgbot.InlineKeyboardMarkup{InlineKeyboard: [][]gotgbot.InlineKeyboardButton{row, {urlBtn, nameBtn}}}
}

func t15Keyboard(lessonID int64) gotgbot.InlineKeyboardMarkup {
	logx.Debugf("tg", "t15Keyboard: lesson=%d", lessonID)
	return gotgbot.InlineKeyboardMarkup{
		InlineKeyboard: [][]gotgbot.InlineKeyboardButton{{
			{Text: "Зайти за меня", CallbackData: joinCallbackData(true, lessonID)},
			{Text: "Не сегодня", CallbackData: joinCallbackData(false, lessonID)},
		}},
	}
}

func settingsKeyboard(sub int) gotgbot.InlineKeyboardMarkup {
	logx.Debugf("tg", "settingsKeyboard: sub=%d", sub)
	t1, t2 := "Подгруппа 1", "Подгруппа 2"
	if sub == 1 {
		t1 = "Подгруппа 1 ✓"
	} else if sub == 2 {
		t2 = "Подгруппа 2 ✓"
	}
	return gotgbot.InlineKeyboardMarkup{
		InlineKeyboard: [][]gotgbot.InlineKeyboardButton{
			{{Text: "Изменить имя в журнале", CallbackData: "st:fio"}},
			{
				{Text: t1, CallbackData: "st:sub:1"},
				{Text: t2, CallbackData: "st:sub:2"},
			},
			{{Text: "Слова для пинга на паре", CallbackData: "st:words"}},
			{{Text: "Комнаты BBB", CallbackData: "st:rooms"}},
			{{Text: "Как это работает", CallbackData: "st:help"}},
		},
	}
}

func cancelKeyboard() gotgbot.InlineKeyboardMarkup {
	logx.Debugf("tg", "cancelKeyboard: build")
	return gotgbot.InlineKeyboardMarkup{
		InlineKeyboard: [][]gotgbot.InlineKeyboardButton{{
			{Text: "Отмена", CallbackData: "st:cancel"},
		}},
	}
}

func backToProfileKeyboard() gotgbot.InlineKeyboardMarkup {
	logx.Debugf("tg", "backToProfileKeyboard: build")
	return gotgbot.InlineKeyboardMarkup{
		InlineKeyboard: [][]gotgbot.InlineKeyboardButton{{
			{Text: "← К профилю", CallbackData: "st:back"},
		}},
	}
}

func notesKeyboard(ids []int64, labels []string) gotgbot.InlineKeyboardMarkup {
	logx.Debugf("tg", "notesKeyboard: ids=%d labels=%d", len(ids), len(labels))
	rows := make([][]gotgbot.InlineKeyboardButton, 0, len(ids))
	for i := range ids {
		if ids[i] <= 0 {
			continue
		}
		label := "Скачать PDF"
		if i < len(labels) && strings.TrimSpace(labels[i]) != "" {
			label = labels[i]
		}
		if len([]rune(label)) > 60 {
			r := []rune(label)
			label = string(r[:57]) + "…"
		}
		rows = append(rows, []gotgbot.InlineKeyboardButton{{
			Text:         label,
			CallbackData: fmt.Sprintf("nt:%d", ids[i]),
		}})
		if len(rows) >= 10 {
			break
		}
	}
	return gotgbot.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func emptyInline() gotgbot.InlineKeyboardMarkup {
	logx.Debugf("tg", "emptyInline: build")
	return gotgbot.InlineKeyboardMarkup{InlineKeyboard: [][]gotgbot.InlineKeyboardButton{}}
}

func parseNotesCallback(data string) (int64, bool) {
	logx.Debugf("tg", "parseNotesCallback: data=%q", data)
	if !strings.HasPrefix(data, "nt:") {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(data, "nt:"), 10, 64)
	if err != nil || n <= 0 {
		logx.Debugf("tg", "parseNotesCallback: bad id err=%v", err)
		return 0, false
	}
	return n, true
}

func parseOnboardCallback(data string) (kind string, n int, ok bool) {
	logx.Debugf("tg", "parseOnboardCallback: data=%q", data)
	switch data {
	case "ob:sub:1":
		return "sub", 1, true
	case "ob:sub:2":
		return "sub", 2, true
	case "ob:skipw":
		return "skipw", 0, true
	default:
		return "", 0, false
	}
}

func parseSettingsCallback(data string) (kind string, n int, ok bool) {
	logx.Debugf("tg", "parseSettingsCallback: data=%q", data)
	switch {
	case data == "st:fio":
		return "fio", 0, true
	case data == "st:words":
		return "words", 0, true
	case data == "st:rooms":
		return "rooms", 0, true
	case data == "st:help":
		return "help", 0, true
	case data == "st:back", data == "st:cancel":
		return "back", 0, true
	case strings.HasPrefix(data, "st:sub:"):
		n, err := strconv.Atoi(strings.TrimPrefix(data, "st:sub:"))
		if err != nil || (n != 1 && n != 2) {
			logx.Debugf("tg", "parseSettingsCallback: bad sub err=%v", err)
			return "", 0, false
		}
		return "sub", n, true
	default:
		return "", 0, false
	}
}
