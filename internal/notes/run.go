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

// lectureInfo — метка лекции, которую Build кладёт в context: по ней каждая
// строка логов и каждый снимок одной лекции грепаются одной командой.
type lectureInfo struct {
	dir        string // p.Dir, например «2026-11-10_матан_2»
	lessonID   int64
	discipline string
}

type lectureCtxKey struct{}

func withLecture(ctx context.Context, p model.LecturePack) context.Context {
	return context.WithValue(ctx, lectureCtxKey{}, lectureInfo{dir: p.Dir, lessonID: p.LessonID, discipline: p.Discipline})
}

// lectureOf достаёт метку из ctx; без неё (прямой вызов в тестах) — пустая.
func lectureOf(ctx context.Context) lectureInfo {
	if ctx != nil {
		if li, ok := ctx.Value(lectureCtxKey{}).(lectureInfo); ok {
			return li
		}
	}
	return lectureInfo{}
}

// tag — префикс для строк лога: «pack=<Dir> lesson=<id> <предмет>».
func (li lectureInfo) tag() string {
	if li.dir == "" && li.lessonID == 0 {
		return "pack=? (без метки)"
	}
	return fmt.Sprintf("pack=%s lesson=%d %s", li.dir, li.lessonID, li.discipline)
}

// dumpName — имя снимка в logx.Dump: тот же pack=<Dir>, без пробелов в имени файла.
func (li lectureInfo) dumpName() string {
	if li.dir == "" {
		return "unknown"
	}
	return li.dir
}

// keyState — вместо значения ключа в логе: «***», либо «нет», если ключа нет.
func keyState(k string) string {
	if strings.TrimSpace(k) == "" {
		return "нет"
	}
	return "***"
}

// audioUsable returns the recording length and, if it cannot hold a lecture,
// the reason. A file ffmpeg cannot read is left to the STT provider to judge.
func audioUsable(ctx context.Context, audio string) (time.Duration, string) {
	tag := lectureOf(ctx).tag()
	st, err := os.Stat(audio)
	if err != nil {
		logx.Warnf("notes", "audioUsable: %s файла записи нет: %v", tag, err)
		return 0, "файла записи нет"
	}
	start := time.Now()
	dur, peak, err := capture.Probe(ctx, audio)
	if err != nil {
		logx.Warnf("notes", "audioUsable: %s %v — проверку уровня пропускаю", tag, err)
		return 0, ""
	}
	logx.Infof("notes", "audioUsable: %s аудио %d байт, длительность=%s, пик=%.1f dBFS (проверка за %s)",
		tag, st.Size(), dur.Round(time.Second), peak, time.Since(start).Round(time.Millisecond))
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

// countSlideImages — сколько картинок лежит в папке слайдов (то, что уйдёт в vision).
func countSlideImages(dir string) int {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		name := strings.ToLower(e.Name())
		if strings.HasSuffix(name, ".png") || strings.HasSuffix(name, ".jpg") || strings.HasSuffix(name, ".jpeg") || strings.HasSuffix(name, ".webp") {
			n++
		}
	}
	return n
}

func fileSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}

func Build(ctx context.Context, cfg *config.Config, root string, p model.LecturePack) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = withLecture(ctx, p)
	li := lectureOf(ctx)
	tag := li.tag()
	started := time.Now()
	logx.Infof("notes", "Build: старт %s status=%s cfg_nil=%t root=%s", tag, p.Status, cfg == nil, root)

	dir := filepath.Join(root, p.Dir)
	audio := archive.AudioFile(dir)
	trPath := archive.TranscriptFile(dir)
	slides := archive.SlidesDir(dir)
	mdPath := archive.NotesMD(dir)
	pdfPath := archive.NotesPDF(dir)
	logx.Debugf("notes", "Build: %s paths dir=%s audio=%s transcript=%s slides=%s md=%s pdf=%s", tag, dir, audio, trPath, slides, mdPath, pdfPath)

	var transcript string
	audioEmpty := false
	dur, why := audioUsable(ctx, audio)
	if why != "" {
		// Тишину нельзя отдавать в STT: Whisper отвечает выдуманной фразой, и по ней
		// пишется и рассылается выдуманный конспект.
		audioEmpty = true
		logx.Warnf("notes", "Build: %s аудио не годится: %s — в STT не отдаю", tag, why)
	} else if t, err := Transcribe(ctx, cfg, audio); err != nil {
		// Звук есть, а расшифровать не вышло: это повод повторить позже, а не
		// собрать конспект по одним слайдам и закрыть пак.
		logx.Warnf("notes", "Build: %s stt: %v", tag, err)
		return fmt.Errorf("stt: %w", err)
	} else if thin := thinTranscript(t, dur); thin != "" {
		audioEmpty = true
		logx.Warnf("notes", "Build: %s расшифровка отброшена: %s", tag, thin)
	} else {
		transcript = t
		logx.Infof("notes", "Build: %s расшифровка принята chars=%d за аудио %s", tag, len([]rune(t)), dur.Round(time.Second))
		if logx.Verbose() {
			logx.Dump("notes", li.dumpName()+"-transcript", []byte(transcript))
		}
		if err := os.WriteFile(trPath, []byte(transcript), 0644); err != nil {
			logx.Errorf("notes", "Build: %s write transcript %s: %v", tag, trPath, err)
		} else {
			logx.Debugf("notes", "Build: %s transcript saved path=%s bytes=%d", tag, trPath, len(transcript))
		}
	}

	// Слайды: vision работает, если есть провайдер; иначе конспект только по аудио.
	slidesIn := countSlideImages(slides)
	switch {
	case len(visionProviders(cfg)) == 0:
		logx.Infof("notes", "Build: %s слайды: vision не настроен — %d файлов не отдаю", tag, slidesIn)
	case slidesIn == 0:
		logx.Infof("notes", "Build: %s слайды: в %s картинок нет — vision пропускаю", tag, slides)
	default:
		logx.Infof("notes", "Build: %s слайды: в vision %d картинок из %s (лимит 60)", tag, slidesIn, slides)
	}
	visionStart := time.Now()
	slideText, err := DescribeSlides(ctx, cfg, slides)
	if err != nil {
		logx.Errorf("notes", "Build: %s слайды: %v", tag, err)
	}
	if slidesIn > 0 && len(visionProviders(cfg)) > 0 {
		logx.Infof("notes", "Build: %s vision: описано chars=%d за %s", tag, len([]rune(slideText)), time.Since(visionStart).Round(time.Millisecond))
	}
	logx.Debugf("notes", "Build: %s sources transcript_chars=%d slides_chars=%d", tag, len(transcript), len(slideText))

	if transcript == "" && slideText == "" && audioEmpty {
		logx.Errorf("notes", "Build: %s пустая запись и нет слайдов", tag)
		return fmt.Errorf("%w: %s", ErrEmptyAudio, firstNonEmpty(why, "в расшифровке нет речи"))
	}
	if transcript == "" && slideText == "" {
		logx.Errorf("notes", "Build: %s нет ни расшифровки, ни слайдов", tag)
		return fmt.Errorf("нет ни расшифровки, ни слайдов")
	}

	title := fmt.Sprintf("%s · лекция %d", p.Discipline, p.Number)
	logx.Debugf("notes", "Build: %s summarizing title=%q", tag, title)
	md, err := Summarize(ctx, cfg, p.Discipline, p.Number, transcript, slideText)
	if err != nil {
		logx.Errorf("notes", "Build: %s summarize: %v", tag, err)
		return err
	}
	if err := os.WriteFile(mdPath, []byte(md), 0644); err != nil {
		logx.Errorf("notes", "Build: %s write notes %s: %v", tag, mdPath, err)
	} else {
		logx.Debugf("notes", "Build: %s notes written path=%s bytes=%d", tag, mdPath, len(md))
	}
	pdfStart := time.Now()
	if err := WritePDF(cfg, title, md, pdfPath); err != nil {
		logx.Errorf("notes", "Build: %s pdf %s: %v", tag, pdfPath, err)
		return err
	}
	logx.Infof("notes", "Build: %s pdf готов bytes=%d за %s путь=%s", tag, fileSize(pdfPath), time.Since(pdfStart).Round(time.Millisecond), pdfPath)
	logx.Infof("notes", "Build: %s готово за %s transcript_chars=%d slides_chars=%d notes_chars=%d",
		tag, time.Since(started).Round(time.Second), len([]rune(transcript)), len([]rune(slideText)), len([]rune(md)))
	return nil
}
