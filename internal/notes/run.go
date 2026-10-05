package notes

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/archive"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/capture"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

// ErrEmptyAudio: в записи нет лекции (тишина, обрывок) и слайдов тоже нет —
// повторять сборку бессмысленно.
var ErrEmptyAudio = errors.New("записи нет")

const (
	minAudioLen  = time.Minute
	minAudioPeak = -50.0 // dBFS; тишина в комнате BBB даёт около -76
	// Обычная речь — около 1000 знаков в минуту; галлюцинация на тишине — пара фраз.
	minCharsPerMinute = 60
)

// audioUsable returns the recording length and, if it cannot hold a lecture,
// the reason. A file ffmpeg cannot read is left to the STT provider to judge.
func audioUsable(ctx context.Context, audio string) (time.Duration, string) {
	if _, err := os.Stat(audio); err != nil {
		return 0, "файла записи нет"
	}
	dur, peak, err := capture.Probe(ctx, audio)
	if err != nil {
		logx.Warnf("notes", "audioUsable: %v — проверку уровня пропускаю", err)
		return 0, ""
	}
	if dur > 0 && dur < minAudioLen {
		return dur, fmt.Sprintf("запись %s — короче минуты", dur.Round(time.Second))
	}
	if peak < minAudioPeak {
		return dur, fmt.Sprintf("тишина: пик %.0f dBFS за %s", peak, dur.Round(time.Second))
	}
	return dur, ""
}

// thinTranscript rejects a transcript far too short for the audio length.
func thinTranscript(text string, dur time.Duration) string {
	if dur < minAudioLen {
		return ""
	}
	chars := len([]rune(strings.TrimSpace(text)))
	if float64(chars) < dur.Minutes()*minCharsPerMinute {
		return fmt.Sprintf("%d знаков на %s записи", chars, dur.Round(time.Second))
	}
	return ""
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

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
	audioEmpty := false
	dur, why := audioUsable(ctx, audio)
	if why != "" {
		// Тишину нельзя отдавать в STT: Whisper отвечает выдуманной фразой, и по ней
		// пишется и рассылается выдуманный конспект.
		audioEmpty = true
		logx.Warnf("notes", "Build: %s/%d аудио не годится: %s", p.Discipline, p.Number, why)
	} else if t, err := Transcribe(ctx, cfg, audio); err != nil {
		// Звук есть, а расшифровать не вышло: это повод повторить позже, а не
		// собрать конспект по одним слайдам и закрыть пак.
		logx.Warnf("notes", "stt %s/%d: %v", p.Discipline, p.Number, err)
		return fmt.Errorf("stt: %w", err)
	} else if thin := thinTranscript(t, dur); thin != "" {
		audioEmpty = true
		logx.Warnf("notes", "Build: %s/%d расшифровка отброшена: %s", p.Discipline, p.Number, thin)
	} else {
		transcript = t
		if err := os.WriteFile(trPath, []byte(transcript), 0644); err != nil {
			logx.Errorf("notes", "Build: write transcript %s: %v", trPath, err)
		} else {
			logx.Debugf("notes", "Build: transcript saved path=%s bytes=%d", trPath, len(transcript))
		}
	}

	slideText, err := DescribeSlides(ctx, cfg, slides)
	if err != nil {
		logx.Errorf("notes", "slides %s/%d: %v", p.Discipline, p.Number, err)
	}
	logx.Debugf("notes", "Build: sources transcript_chars=%d slides_chars=%d", len(transcript), len(slideText))

	if transcript == "" && slideText == "" && audioEmpty {
		logx.Errorf("notes", "Build: пустая запись и нет слайдов discipline=%q lecture=%d", p.Discipline, p.Number)
		return fmt.Errorf("%w: %s", ErrEmptyAudio, firstNonEmpty(why, "в расшифровке нет речи"))
	}
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
