package notes

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/archive"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

func Build(ctx context.Context, cfg *config.Config, root string, p model.LecturePack) error {
	if ctx == nil {
		ctx = context.Background()
	}
	dir := filepath.Join(root, p.Dir)
	audio := archive.AudioFile(dir)
	trPath := archive.TranscriptFile(dir)
	slides := archive.SlidesDir(dir)
	mdPath := archive.NotesMD(dir)
	pdfPath := archive.NotesPDF(dir)

	var transcript string
	if t, err := Transcribe(ctx, cfg, audio); err != nil {
		logx.Warnf("notes", "stt %s/%d: %v", p.Discipline, p.Number, err)
	} else {
		transcript = t
		_ = os.WriteFile(trPath, []byte(transcript), 0644)
	}

	slideText, err := DescribeSlides(ctx, cfg, slides)
	if err != nil {
		logx.Errorf("notes", "slides %s/%d: %v", p.Discipline, p.Number, err)
	}

	if transcript == "" && slideText == "" {
		return fmt.Errorf("нет ни расшифровки, ни слайдов")
	}

	title := fmt.Sprintf("%s · лекция %d", p.Discipline, p.Number)
	md, err := Summarize(ctx, cfg, p.Discipline, p.Number, transcript, slideText)
	if err != nil {
		return err
	}
	_ = os.WriteFile(mdPath, []byte(md), 0644)
	if err := WritePDF(cfg, title, md, pdfPath); err != nil {
		return err
	}
	return nil
}
