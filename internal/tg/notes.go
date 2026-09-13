package tg

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/archive"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

func (b *Bot) onNotes(_ *gotgbot.Bot, ctx *ext.Context) error {
	from := b.allowed(ctx)
	if from == nil || ctx.EffectiveMessage == nil {
		return nil
	}
	return b.sendNotes(ctx.EffectiveMessage.Chat.Id)
}

func (b *Bot) sendNotes(chatID int64) error {
	ready, pending, err := b.notePacks()
	if err != nil {
		return err
	}
	text := formatNotesList(ready, pending)
	if len(ready) == 0 {
		return b.sendMain(chatID, text)
	}
	ids := make([]int64, 0, len(ready))
	labels := make([]string, 0, len(ready))
	for _, p := range ready {
		ids = append(ids, p.ID)
		labels = append(labels, notesButtonLabel(p))
	}
	return b.send(chatID, text, &gotgbot.SendMessageOpts{ReplyMarkup: notesKeyboard(ids, labels)})
}

func (b *Bot) notePacks() (ready, pending []model.LecturePack, err error) {
	if b.st == nil {
		return nil, nil, nil
	}
	all, err := b.st.ListPacks()
	if err != nil {
		return nil, nil, err
	}
	for _, p := range all {
		if p.Status == model.PackDone {
			if len(ready) < 10 {
				ready = append(ready, p)
			}
			continue
		}
		if p.Status == model.PackError {
			pending = append(pending, p)
			continue
		}
		if len(pending) < 8 {
			pending = append(pending, p)
		}
	}
	return ready, pending, nil
}

func (b *Bot) onNotesCallback(bot *gotgbot.Bot, ctx *ext.Context) error {
	from := b.allowed(ctx)
	if from == nil || ctx.CallbackQuery == nil {
		return nil
	}
	id, ok := parseNotesCallback(ctx.CallbackQuery.Data)
	if !ok {
		answerToast(bot, ctx, "не та кнопка")
		return nil
	}
	chatID := from.Id
	if ctx.EffectiveChat != nil {
		chatID = ctx.EffectiveChat.Id
	}
	p, err := b.st.PackByID(id)
	if err != nil {
		return err
	}
	if p == nil || p.Status != model.PackDone {
		answerToast(bot, ctx, "PDF ещё не готов")
		return nil
	}
	answerToast(bot, ctx, "отправляю")
	if err := b.sendPackPDF(chatID, p); err != nil {
		return b.send(chatID, "Не смог отправить PDF: "+err.Error(), nil)
	}
	return nil
}

func (b *Bot) sendPackPDF(chatID int64, p *model.LecturePack) error {
	if p == nil {
		return fmt.Errorf("нет конспекта")
	}
	rel := strings.TrimSpace(p.NotesPDF)
	if rel == "" {
		rel = filepath.ToSlash(filepath.Join(p.Dir, "notes.pdf"))
	}
	root := b.cfg.RecordingsDir
	abs, err := archive.UnderRoot(root, rel)
	if err != nil {
		return err
	}
	st, err := os.Stat(abs)
	if err != nil || st.Size() < 100 {
		return fmt.Errorf("файла ещё нет на диске")
	}
	f, err := os.Open(abs)
	if err != nil {
		return err
	}
	defer f.Close()
	name := archive.PDFFileName(p.Discipline, p.Number)
	_, err = b.api.SendDocument(chatID, gotgbot.InputFileByReader(name, f), &gotgbot.SendDocumentOpts{
		Caption: archive.Label(p.Discipline, p.Number),
	})
	return err
}
