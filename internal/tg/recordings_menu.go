package tg

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
)

func (b *Bot) MainReplyKeyboard() gotgbot.ReplyKeyboardMarkup {
	return gotgbot.ReplyKeyboardMarkup{
		Keyboard: [][]gotgbot.KeyboardButton{
			{
				{Text: "📅 Сегодня"},
				{Text: "🔗 Ссылки"},
			},
			{
				{Text: "📁 Архив лекций и отчетов"},
			},
			{
				{Text: "⚙️ Настройки"},
				{Text: "❓ Помощь"},
			},
		},
		ResizeKeyboard: true,
	}
}

func (b *Bot) onRecordingsCommand(_ *gotgbot.Bot, ctx *ext.Context) error {
	from := b.allowed(ctx)
	if from == nil {
		return nil
	}
	chatID := ctx.EffectiveChat.Id
	return b.showDisciplinesMenu(chatID)
}

func (b *Bot) showDisciplinesMenu(chatID int64) error {
	disciplines, err := b.st.GetDisciplines(context.Background())
	if err != nil {
		return b.send(chatID, "Ошибка при получении списка дисциплин", nil)
	}

	if len(disciplines) == 0 {
		return b.send(chatID, "📹 Записей лекций пока нет в архиве.", nil)
	}

	var rows [][]gotgbot.InlineKeyboardButton
	for _, d := range disciplines {
		rows = append(rows, []gotgbot.InlineKeyboardButton{
			{
				Text:         "📚 " + d,
				CallbackData: "rec_sub:" + d,
			},
		})
	}

	mk := gotgbot.InlineKeyboardMarkup{InlineKeyboard: rows}
	return b.send(chatID, "Выберите предмет для просмотра лекций:", &gotgbot.SendMessageOpts{
		ReplyMarkup: mk,
	})
}

func (b *Bot) onRecordingsCallback(bot *gotgbot.Bot, ctx *ext.Context) error {
	query := ctx.CallbackQuery
	if query == nil {
		return nil
	}
	data := query.Data
	chatID := query.Message.GetChat().Id

	if strings.HasPrefix(data, "rec_sub:") {
		discipline := strings.TrimPrefix(data, "rec_sub:")
		return b.showLecturesMenu(bot, query, chatID, discipline)
	}

	if strings.HasPrefix(data, "rec_id:") {
		var id int64
		if _, err := fmt.Sscanf(data, "rec_id:%d", &id); err != nil {
			return err
		}
		return b.sendLecturePackage(bot, query, chatID, id)
	}

	return nil
}

func (b *Bot) showLecturesMenu(bot *gotgbot.Bot, query *gotgbot.CallbackQuery, chatID int64, discipline string) error {
	recs, err := b.st.GetRecordingsByDiscipline(context.Background(), discipline)
	if err != nil {
		_, _ = query.Answer(bot, &gotgbot.AnswerCallbackQueryOpts{Text: "Ошибка загрузки лекций"})
		return err
	}

	if len(recs) == 0 {
		_, _ = query.Answer(bot, &gotgbot.AnswerCallbackQueryOpts{Text: "По этому предмету нет записей"})
		return nil
	}

	var rows [][]gotgbot.InlineKeyboardButton
	for _, r := range recs {
		btnText := fmt.Sprintf("Лекция №%d (%s)", r.LectureNum, r.Date)
		if r.HasTestAlert {
			btnText = "🚨 " + btnText + " (Тест!)"
		}
		rows = append(rows, []gotgbot.InlineKeyboardButton{
			{
				Text:         btnText,
				CallbackData: fmt.Sprintf("rec_id:%d", r.ID),
			},
		})
	}

	rows = append(rows, []gotgbot.InlineKeyboardButton{
		{
			Text:         "« Назад к предметам",
			CallbackData: "rec_back_sub",
		},
	})

	mk := gotgbot.InlineKeyboardMarkup{InlineKeyboard: rows}
	_, _, err = query.Message.EditText(bot, &gotgbot.EditMessageTextOpts{
		Text:        fmt.Sprintf("📚 <b>Предмет:</b> %s\n\nВыберите лекцию:", discipline),
		ParseMode:   "HTML",
		ReplyMarkup: mk,
	})
	return err
}

func (b *Bot) sendLecturePackage(bot *gotgbot.Bot, query *gotgbot.CallbackQuery, chatID int64, recID int64) error {
	rec, err := b.st.GetRecordingByID(context.Background(), recID)
	if err != nil || rec == nil {
		_, _ = query.Answer(bot, &gotgbot.AnswerCallbackQueryOpts{Text: "Запись не найдена"})
		return err
	}

	_, _ = query.Answer(bot, &gotgbot.AnswerCallbackQueryOpts{Text: "Загружаю материалы лекции..."})

	var msgText strings.Builder
	msgText.WriteString(fmt.Sprintf("📹 <b>Лекция №%d по дисциплине %s</b> (%s)\n\n", rec.LectureNum, rec.Discipline, rec.Date))

	if rec.HasTestAlert {
		msgText.WriteString("⚠️ <b>ВНИМАНИЕ: На этой лекции проводился ТЕСТ / Moodle!</b>\n\n")
	}

	if rec.SummaryText != "" {
		msgText.WriteString("📝 <b>Отчет и конспект от LLM:</b>\n\n")
		msgText.WriteString(rec.SummaryText)
	} else {
		msgText.WriteString("📝 <i>Отчет LLM готовится...</i>")
	}

	opts := &gotgbot.SendMessageOpts{
		ParseMode: "HTML",
	}

	// 1. Send Report text
	_, err = bot.SendMessage(chatID, msgText.String(), opts)
	if err != nil {
		// Fallback without HTML formatting if parsing error
		opts.ParseMode = ""
		_, _ = bot.SendMessage(chatID, msgText.String(), opts)
	}

	// 2. Send Video File directly from VDS disk or Telegram file ID
	if rec.TelegramFileID != "" {
		_, _ = bot.SendVideo(chatID, gotgbot.InputFileByID(rec.TelegramFileID), &gotgbot.SendVideoOpts{
			Caption: fmt.Sprintf("📹 Видеозапись: %s (Лекция №%d)", rec.Discipline, rec.LectureNum),
		})
	} else if rec.VideoPath != "" {
		f, err := os.Open(rec.VideoPath)
		if err == nil {
			defer f.Close()
			videoFile := gotgbot.InputFileByReader(filepath.Base(rec.VideoPath), f)
			_, _ = bot.SendVideo(chatID, videoFile, &gotgbot.SendVideoOpts{
				Caption: fmt.Sprintf("📹 Видеозапись с VDS: %s (Лекция №%d)", rec.Discipline, rec.LectureNum),
			})
		} else {
			_, _ = bot.SendMessage(chatID, fmt.Sprintf("📁 Путь к видео на VDS: <code>%s</code>", rec.VideoPath), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
		}
	}

	// 3. Send Transcript Document if transcript text is non-empty
	if rec.TranscriptText != "" {
		doc := gotgbot.InputFileByReader("transcript.txt", strings.NewReader(rec.TranscriptText))
		_, _ = bot.SendDocument(chatID, doc, &gotgbot.SendDocumentOpts{
			Caption: "📄 Полная текстовая расшифровка (Fish Studio STT)",
		})
	}

	return nil
}
