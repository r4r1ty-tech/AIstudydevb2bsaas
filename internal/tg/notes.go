// Кнопка «Скачать PDF» под уведомлением «конспект готов» (notify.NotesButton).
package tg

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/archive"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

func (b *Bot) onNotesCallback(bot *gotgbot.Bot, ctx *ext.Context) error {
	logx.Debugf("tg", "onNotesCallback: enter")
	from := b.allowed(ctx)
	if from == nil || ctx.CallbackQuery == nil {
		logx.Warnf("tg", "onNotesCallback: rejected")
		return nil
	}
	id, ok := parseNotesCallback(ctx.CallbackQuery.Data)
	if !ok {
		logx.Warnf("tg", "onNotesCallback: bad callback data=%q", ctx.CallbackQuery.Data)
		answerToast(bot, ctx, "не та кнопка")
		return nil
	}
	chatID := from.Id
	if ctx.EffectiveChat != nil {
		chatID = ctx.EffectiveChat.Id
	}
	logx.Debugf("tg", "onNotesCallback: tg=%d pack=%d chat=%d", from.Id, id, chatID)
	p, err := b.st.PackByID(id)
	if err != nil {
		logx.Errorf("tg", "onNotesCallback: pack id=%d: %v", id, err)
		return err
	}
	if p == nil || p.Status != model.PackDone {
		logx.Warnf("tg", "onNotesCallback: pack %d not ready status=%v", id, p)
		answerToast(bot, ctx, "PDF ещё не готов")
		return nil
	}
	answerToast(bot, ctx, "отправляю")
	if err := b.sendPackPDF(chatID, p); err != nil {
		logx.Errorf("tg", "onNotesCallback: send pdf chat=%d pack=%d: %v", chatID, id, err)
		return b.send(chatID, "Не смог отправить PDF: "+err.Error(), nil)
	}
	logx.Infof("tg", "notes pdf sent chat=%d pack=%d", chatID, id)
	return nil
}

func (b *Bot) sendPackPDF(chatID int64, p *model.LecturePack) error {
	logx.Debugf("tg", "sendPackPDF: chat=%d pack=%v", chatID, p)
	if p == nil {
		logx.Errorf("tg", "sendPackPDF: nil pack chat=%d", chatID)
		return fmt.Errorf("нет конспекта")
	}
	rel := strings.TrimSpace(p.NotesPDF)
	if rel == "" {
		rel = filepath.ToSlash(filepath.Join(p.Dir, "notes.pdf"))
	}
	root := b.cfg.RecordingsDir
	abs, err := archive.UnderRoot(root, rel)
	if err != nil {
		logx.Errorf("tg", "sendPackPDF: under root rel=%q: %v", rel, err)
		return err
	}
	st, err := os.Stat(abs)
	if err != nil || st.Size() < 100 {
		logx.Errorf("tg", "sendPackPDF: stat %s: %v", abs, err)
		return fmt.Errorf("файла ещё нет на диске")
	}
	f, err := os.Open(abs)
	if err != nil {
		logx.Errorf("tg", "sendPackPDF: open %s: %v", abs, err)
		return err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			logx.Warnf("tg", "sendPackPDF: close %s: %v", abs, cerr)
		}
	}()
	name := archive.PDFFileName(p.Discipline, p.Number)
	_, err = b.api.SendDocument(chatID, gotgbot.InputFileByReader(name, f), &gotgbot.SendDocumentOpts{
		ParseMode: htmlMode,
		Caption:   archive.Label(p.Discipline, p.Number),
	})
	if err != nil {
		logx.Errorf("tg", "sendPackPDF: send document chat=%d name=%s: %v", chatID, name, err)
		return err
	}
	logx.Infof("tg", "sendPackPDF: sent chat=%d name=%s", chatID, name)
	return nil
}

func parseNotesCallback(data string) (int64, bool) {
	if !strings.HasPrefix(data, "nt:") {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(data, "nt:"), 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func answerToast(bot *gotgbot.Bot, ctx *ext.Context, text string) {
	if bot == nil || ctx == nil || ctx.CallbackQuery == nil {
		return
	}
	opts := &gotgbot.AnswerCallbackQueryOpts{}
	if strings.TrimSpace(text) != "" {
		opts.Text = text
	}
	if _, err := ctx.CallbackQuery.Answer(bot, opts); err != nil {
		logx.Warnf("tg", "answerToast: %q: %v", text, err)
	}
}
