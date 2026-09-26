package tg

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/archive"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

func (b *Bot) onNotes(_ *gotgbot.Bot, ctx *ext.Context) error {
	logx.Debugf("tg", "onNotes: enter")
	from := b.allowed(ctx)
	if from == nil || ctx.EffectiveMessage == nil {
		logx.Warnf("tg", "onNotes: rejected")
		return nil
	}
	logx.Debugf("tg", "onNotes: tg=%d chat=%d", from.Id, ctx.EffectiveMessage.Chat.Id)
	return b.sendNotes(ctx.EffectiveMessage.Chat.Id)
}

func (b *Bot) sendNotes(chatID int64) error {
	logx.Debugf("tg", "sendNotes: chat=%d", chatID)
	ready, pending, err := b.notePacks()
	if err != nil {
		logx.Errorf("tg", "sendNotes: packs: %v", err)
		return err
	}
	logx.Debugf("tg", "sendNotes: ready=%d pending=%d", len(ready), len(pending))
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
	logx.Infof("tg", "notes list sent chat=%d ready=%d", chatID, len(ids))
	return b.send(chatID, text, &gotgbot.SendMessageOpts{ReplyMarkup: notesKeyboard(ids, labels)})
}

func (b *Bot) notePacks() (ready, pending []model.LecturePack, err error) {
	logx.Debugf("tg", "notePacks: enter")
	if b.st == nil {
		logx.Debugf("tg", "notePacks: nil store")
		return nil, nil, nil
	}
	all, err := b.st.ListPacks()
	if err != nil {
		logx.Errorf("tg", "notePacks: list packs: %v", err)
		return nil, nil, err
	}
	for _, p := range all {
		if p.Status == model.PackDone {
			// Локальные файлы живут ~сутки; после очистки они только в GitHub.
			if p.CleanedAt != nil {
				continue
			}
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
	logx.Debugf("tg", "notePacks: total=%d ready=%d pending=%d", len(all), len(ready), len(pending))
	return ready, pending, nil
}

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
