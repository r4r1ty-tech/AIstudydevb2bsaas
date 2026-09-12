package tg

import (
	"strconv"
	"strings"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

func mainKeyboard() gotgbot.ReplyKeyboardMarkup {
	return gotgbot.ReplyKeyboardMarkup{
		Keyboard: [][]gotgbot.KeyboardButton{
			{{Text: btnToday}, {Text: btnLinks}},
			{{Text: btnSettings}, {Text: btnHelp}},
		},
		IsPersistent:          true,
		ResizeKeyboard:        true,
		InputFieldPlaceholder: inputHint,
	}
}

func fioForceReply() gotgbot.ForceReply {
	return gotgbot.ForceReply{
		ForceReply:            true,
		InputFieldPlaceholder: fioHint,
	}
}

func wordsForceReply() gotgbot.ForceReply {
	return gotgbot.ForceReply{
		ForceReply:            true,
		InputFieldPlaceholder: "лаба, зачёт",
	}
}

func subgroupKeyboard() gotgbot.InlineKeyboardMarkup {
	return gotgbot.InlineKeyboardMarkup{
		InlineKeyboard: [][]gotgbot.InlineKeyboardButton{{
			{Text: "1", CallbackData: "ob:sub:1"},
			{Text: "2", CallbackData: "ob:sub:2"},
		}},
	}
}

func skipWordsKeyboard() gotgbot.InlineKeyboardMarkup {
	return gotgbot.InlineKeyboardMarkup{
		InlineKeyboard: [][]gotgbot.InlineKeyboardButton{{
			{Text: "Пропустить", CallbackData: "ob:skipw"},
		}},
	}
}

func t15Keyboard(lessonID int64) gotgbot.InlineKeyboardMarkup {
	return gotgbot.InlineKeyboardMarkup{
		InlineKeyboard: [][]gotgbot.InlineKeyboardButton{{
			{Text: "Зайти", CallbackData: joinCallbackData(true, lessonID)},
			{Text: "Пропустить", CallbackData: joinCallbackData(false, lessonID)},
		}},
	}
}

func settingsKeyboard(sub int) gotgbot.InlineKeyboardMarkup {
	t1, t2 := "Подгруппа 1", "Подгруппа 2"
	if sub == 1 {
		t1 = "Подгруппа 1 ✓"
	} else if sub == 2 {
		t2 = "Подгруппа 2 ✓"
	}
	return gotgbot.InlineKeyboardMarkup{
		InlineKeyboard: [][]gotgbot.InlineKeyboardButton{
			{{Text: "Сменить ФИО", CallbackData: "st:fio"}},
			{
				{Text: t1, CallbackData: "st:sub:1"},
				{Text: t2, CallbackData: "st:sub:2"},
			},
			{{Text: "Свои слова", CallbackData: "st:words"}},
		},
	}
}

func emptyInline() gotgbot.InlineKeyboardMarkup {
	return gotgbot.InlineKeyboardMarkup{InlineKeyboard: [][]gotgbot.InlineKeyboardButton{}}
}

func parseOnboardCallback(data string) (kind string, n int, ok bool) {
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
	switch {
	case data == "st:fio":
		return "fio", 0, true
	case data == "st:words":
		return "words", 0, true
	case strings.HasPrefix(data, "st:sub:"):
		n, err := strconv.Atoi(strings.TrimPrefix(data, "st:sub:"))
		if err != nil || (n != 1 && n != 2) {
			return "", 0, false
		}
		return "sub", n, true
	default:
		return "", 0, false
	}
}
