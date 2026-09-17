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
	logx.Debugf("notes", "Build: start discipline=%q lecture=%d root=%s dir=%s cfg_nil=%t", p.Discipline, p.Number, root, p.Dir, cfg == nil)
	if ctx == nil {
		ctx = context.Background()
	}
	dir := filepath.Join(root, p.Dir)
	audio := archive.AudioFile(dir)
	trPath := archive.TranscriptFile(dir)
	slides := archive.SlidesDir(dir)
	mdPath := archive.NotesMD(dir)
	pdfPath := archive.NotesPDF(dir)
	logx.Debugf("notes", "Build: paths dir=%s audio=%s transcript=%s slides=%s md=%s pdf=%s", dir, audio, trPath, slides, mdPath, pdfPath)

	var transcript string
	if t, err := Transcribe(ctx, cfg, audio); err != nil {
		logx.Warnf("notes", "stt %s/%d: %v", p.Discipline, p.Number, err)
	} else {
		transcript = t
		if err := os.WriteFile(trPath, []byte(transcript), 0644); err != nil {
			logx.Errorf("notes", "Build: write transcript %s: %v", trPath, err)
		} else {
			logx.Debugf("notes", "Build: transcript written path=%s bytes=%d", trPath, len(transcript))
		}
	}

	slideText, err := DescribeSlides(ctx, cfg, slides)
	if err != nil {
		logx.Errorf("notes", "slides %s/%d: %v", p.Discipline, p.Number, err)
	}
	logx.Debugf("notes", "Build: sources transcript_chars=%d slides_chars=%d", len(transcript), len(slideText))

	if transcript == "" && slideText == "" {
		logx.Errorf("notes", "Build: нет ни расшифровки, ни слайдов discipline=%q lecture=%d", p.Discipline, p.Number)
		return fmt.Errorf("нет ни расшифровки, ни слайдов")
	}

	title := fmt.Sprintf("%s · лекция %d", p.Discipline, p.Number)
	logx.Debugf("notes", "Build: summarizing title=%q", title)
	md, err := Summarize(ctx, cfg, p.Discipline, p.Number, transcript, slideText)
	if err != nil {
		logx.Errorf("notes", "Build: summarize %s/%d: %v", p.Discipline, p.Number, err)
		return err
	}
	if err := os.WriteFile(mdPath, []byte(md), 0644); err != nil {
		logx.Errorf("notes", "Build: write notes %s: %v", mdPath, err)
	} else {
		logx.Debugf("notes", "Build: notes written path=%s bytes=%d", mdPath, len(md))
	}
	if err := WritePDF(cfg, title, md, pdfPath); err != nil {
		logx.Errorf("notes", "Build: pdf %s: %v", pdfPath, err)
		return err
	}
	logx.Infof("notes", "Build: done discipline=%q lecture=%d pdf=%s", p.Discipline, p.Number, pdfPath)
	return nil
}
