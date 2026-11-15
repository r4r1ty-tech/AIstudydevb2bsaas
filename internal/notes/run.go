package notes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
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

// ErrSlidesIncomplete: часть слайдов не распознана vision даже после повторов. Конспект и PDF уже
// записаны, но сборку стоит повторить: вызывающий решает, сколько раз и когда.
var ErrSlidesIncomplete = errors.New("слайды распознаны не полностью")

const (
	minAudioLen  = time.Minute
	minAudioPeak = -50.0 // dBFS; тишина в комнате BBB даёт около -76
	// Обычная речь — около 1000 знаков в минуту; галлюцинация на тишине — пара фраз.
	minCharsPerMinute = 60
)

// buildCache хранит метаданные этапов сборки лекции в .build_cache.json.
type buildCache struct {
	AudioDurationSec   float64 `json:"audio_duration_sec,omitempty"`
	AudioSize          int64   `json:"audio_size,omitempty"`
	SlidesHash         string  `json:"slides_hash,omitempty"`
	SlidesDesc         string  `json:"slides_desc,omitempty"`
	SummarizeInputHash string  `json:"summarize_input_hash,omitempty"`
	NotesMDHash        string  `json:"notes_md_hash,omitempty"`
	PDFNotesHash       string  `json:"pdf_notes_hash,omitempty"`
}

func buildCachePath(dir string) string {
	return filepath.Join(dir, ".build_cache.json")
}

func slidesDescPath(dir string) string {
	return filepath.Join(dir, ".slides_desc.txt")
}

func loadBuildCache(dir string) *buildCache {
	data, err := os.ReadFile(buildCachePath(dir))
	if err != nil {
		return &buildCache{}
	}
	var bc buildCache
	if err := json.Unmarshal(data, &bc); err != nil {
		return &buildCache{}
	}
	return &bc
}

// writeFileAtomic атомарно записывает data в path через временный файл в той же директории.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func saveBuildCache(dir string, bc *buildCache) error {
	if bc == nil {
		return nil
	}
	data, err := json.MarshalIndent(bc, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(buildCachePath(dir), data, 0644)
}

// transcriptGrowthThreshold возвращает порог изменения длительности аудио: max(5 мин, 10% от закэшированной длительности).
func transcriptGrowthThreshold(cachedDur time.Duration) time.Duration {
	threshold := 5 * time.Minute
	pct := time.Duration(float64(cachedDur) * 0.10)
	if pct > threshold {
		threshold = pct
	}
	return threshold
}

// isTranscriptCacheValid проверяет, что отклонение длительности аудио от закэшированной
// в любую сторону меньше допустимого порога max(5 мин, 10%).
func isTranscriptCacheValid(dur, cachedDur time.Duration) bool {
	diff := dur - cachedDur
	if diff < 0 {
		diff = -diff
	}
	threshold := transcriptGrowthThreshold(cachedDur)
	return diff < threshold
}

// isAudioSizeCompatible проверяет, что размер аудиофайла не указывает на подмену или обрезку файла.
func isAudioSizeCompatible(audioSz, cachedSz int64, dur, cachedDur time.Duration) bool {
	if cachedSz <= 0 || audioSz <= 0 {
		return true
	}
	// Уменьшение размера более чем на 20% указывает на подмену или обрезку файла.
	if audioSz < cachedSz && cachedSz-audioSz > cachedSz/5 {
		return false
	}
	// Существенный рост размера (> 20%) без роста длительности также указывает на подмену файла.
	if dur <= cachedDur && audioSz > cachedSz && audioSz-cachedSz > cachedSz/5 {
		return false
	}
	return true
}

// hashFile вычисляет SHA-256 файла и записывает строку с именем и хэшем в общий hasher.
func hashFile(hasher io.Writer, name, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fileH := sha256.New()
	if _, err := io.Copy(fileH, f); err != nil {
		return err
	}
	fmt.Fprintf(hasher, "%s:%x\n", name, fileH.Sum(nil))
	return nil
}

// computeSlidesHash вычисляет общий SHA-256 хэш набора картинок слайдов (и slides.json, если есть).
// Порядок картинок детерминирован (сортировка по имени, до 60 штук, как в DescribeSlides).
func computeSlidesHash(slidesDir string) (string, int, error) {
	ents, err := os.ReadDir(slidesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", 0, nil
		}
		return "", 0, err
	}
	var imgNames []string
	var hasManifest bool
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		lower := strings.ToLower(name)
		if lower == "slides.json" {
			hasManifest = true
		}
		if strings.HasSuffix(lower, ".png") || strings.HasSuffix(lower, ".jpg") ||
			strings.HasSuffix(lower, ".jpeg") || strings.HasSuffix(lower, ".webp") {
			imgNames = append(imgNames, name)
		}
	}
	sort.Strings(imgNames)
	if len(imgNames) > 60 {
		imgNames = imgNames[:60]
	}
	if len(imgNames) == 0 && !hasManifest {
		return "", 0, nil
	}

	h := sha256.New()
	for _, name := range imgNames {
		filePath := filepath.Join(slidesDir, name)
		if err := hashFile(h, name, filePath); err != nil {
			return "", 0, fmt.Errorf("хэширование слайда %s: %w", filePath, err)
		}
	}
	if hasManifest {
		manifestPath := filepath.Join(slidesDir, "slides.json")
		if err := hashFile(h, "slides.json", manifestPath); err != nil {
			return "", 0, fmt.Errorf("хэширование манифеста %s: %w", manifestPath, err)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), len(imgNames), nil
}

// computeSummarizeInputHash вычисляет SHA-256 от входных данных суммаризации.
func computeSummarizeInputHash(discipline string, number int, transcript, slidesText string) string {
	h := sha256.New()
	fmt.Fprintf(h, "discipline:%s\nnumber:%d\ntranscript_len:%d\nslides_len:%d\n", discipline, number, len(transcript), len(slidesText))
	h.Write([]byte(transcript))
	h.Write([]byte{'\n'})
	h.Write([]byte(slidesText))
	return hex.EncodeToString(h.Sum(nil))
}

// computeStringSHA256 вычисляет SHA-256 от строки.
func computeStringSHA256(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

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

// stepTranscript выполняет расшифровку аудиозаписи либо берёт готовый transcript.txt из кэша.
func stepTranscript(ctx context.Context, cfg *config.Config, dir, audioPath, trPath string, bc *buildCache) (string, bool, string, error) {
	tag := lectureOf(ctx).tag()
	dur, why := audioUsable(ctx, audioPath)
	if why != "" {
		// При отсутствии, обрыве или тишине в аудио нельзя возвращать старый кэш transcript.txt,
		// иначе удалённая или пустая запись будет подменена старой расшифровкой.
		// Тишину также нельзя отдавать в STT: Whisper отвечает выдуманной фразой, и по ней
		// пишется и рассылается выдуманный конспект.
		logx.Warnf("notes", "Build: %s аудио не годится: %s — в STT и кэш не отдаю", tag, why)
		return "", true, why, nil
	}

	audioSz := archive.FileSize(audioPath)

	// Проверяем наличие уже существующего непустого transcript.txt (только при годном аудио)
	rawTr, err := os.ReadFile(trPath)
	existingTr := strings.TrimSpace(string(rawTr))

	if err == nil && len(existingTr) > 0 {
		prevDur := bc.AudioDurationSec
		prevSize := bc.AudioSize
		cachedDur := time.Duration(bc.AudioDurationSec * float64(time.Second))
		// Если transcript.txt остался от старой версии без .build_cache.json,
		// считаем текущую длительность базовой и используем готовый transcript.txt.
		if cachedDur <= 0 && bc.AudioSize <= 0 {
			cachedDur = dur
		}

		durValid := isTranscriptCacheValid(dur, cachedDur)
		sizeValid := isAudioSizeCompatible(audioSz, bc.AudioSize, dur, cachedDur)
		if durValid && sizeValid {
			diff := dur - cachedDur
			if diff < 0 {
				diff = -diff
			}
			threshold := transcriptGrowthThreshold(cachedDur)
			logx.Infof("notes", "Build: %s расшифровка взята из кэша chars=%d (аудио %s, кэш %s, отклонение %s < порога %s)",
				tag, len([]rune(existingTr)), dur.Round(time.Second), cachedDur.Round(time.Second),
				diff.Round(time.Second), threshold.Round(time.Second))
			bc.AudioDurationSec = cachedDur.Seconds()
			if bc.AudioSize <= 0 {
				bc.AudioSize = audioSz
			}
			if bc.AudioDurationSec != prevDur || bc.AudioSize != prevSize {
				_ = saveBuildCache(dir, bc)
			}
			return existingTr, false, "", nil
		}

		threshold := transcriptGrowthThreshold(cachedDur)
		switch {
		case !durValid && dur >= cachedDur:
			logx.Infof("notes", "Build: %s аудио выросло на %s >= порога %s (было %s, стало %s) — повторная расшифровка",
				tag, (dur - cachedDur).Round(time.Second), threshold.Round(time.Second), cachedDur.Round(time.Second), dur.Round(time.Second))
		case !durValid:
			logx.Infof("notes", "Build: %s аудио уменьшилось на %s >= порога %s (было %s, стало %s) — повторная расшифровка",
				tag, (cachedDur - dur).Round(time.Second), threshold.Round(time.Second), cachedDur.Round(time.Second), dur.Round(time.Second))
		default:
			logx.Infof("notes", "Build: %s размер аудио существенно изменился (было %d байт, стало %d байт) — повторная расшифровка",
				tag, bc.AudioSize, audioSz)
		}
	}

	t, err := Transcribe(ctx, cfg, audioPath)
	if err != nil {
		// Звук есть, а расшифровать не вышло: это повод повторить позже, а не
		// собрать конспект по одним слайдам и закрыть пак.
		logx.Warnf("notes", "Build: %s stt: %v", tag, err)
		return "", false, "", fmt.Errorf("stt: %w", err)
	}

	if thin := thinTranscript(t, dur); thin != "" {
		logx.Warnf("notes", "Build: %s расшифровка отброшена: %s", tag, thin)
		return "", true, thin, nil
	}

	transcript := t
	logx.Infof("notes", "Build: %s расшифровка принята chars=%d за аудио %s",
		tag, len([]rune(t)), dur.Round(time.Second))
	if logx.Verbose() {
		logx.Dump("notes", lectureOf(ctx).dumpName()+"-transcript", []byte(transcript))
	}

	if err := writeFileAtomic(trPath, []byte(transcript), 0644); err != nil {
		logx.Errorf("notes", "Build: %s write transcript %s: %v", tag, trPath, err)
	} else {
		logx.Debugf("notes", "Build: %s transcript saved path=%s bytes=%d", tag, trPath, len(transcript))
	}

	bc.AudioDurationSec = dur.Seconds()
	bc.AudioSize = audioSz
	_ = saveBuildCache(dir, bc)

	return transcript, false, "", nil
}

// stepSlides описывает слайды либо берёт готовое описание из кэша по SHA-256 картинок.
// Неполное описание (часть батчей не распознана) в кэш не пишется: при следующей сборке слайды пересчитаются.
func stepSlides(ctx context.Context, cfg *config.Config, dir, slidesDir string, bc *buildCache) (SlidesResult, error) {
	tag := lectureOf(ctx).tag()
	slidesHash, _, err := computeSlidesHash(slidesDir)
	if err != nil {
		logx.Errorf("notes", "Build: %s вычисление хэша слайдов: %v", tag, err)
	}

	descFile := slidesDescPath(dir)

	// Если хэш слайдов совпадает с закэшированным и описание не пустое — используем кэш
	if slidesHash != "" && bc.SlidesHash == slidesHash {
		desc := bc.SlidesDesc
		if desc == "" {
			if data, err := os.ReadFile(descFile); err == nil {
				desc = strings.TrimSpace(string(data))
			}
		}
		if desc != "" {
			logx.Infof("notes", "Build: %s слайды: взяты из кэша chars=%d (sha256=%s)",
				tag, len([]rune(desc)), slidesHash[:min(12, len(slidesHash))])
			return SlidesResult{Text: desc}, nil
		}
	}

	slidesIn := countSlideImages(slidesDir)
	visionReady := hasVisionConfigured(cfg)
	switch {
	case !visionReady:
		logx.Infof("notes", "Build: %s слайды: vision не настроен — %d файлов не отдаю", tag, slidesIn)
	case slidesIn == 0:
		logx.Infof("notes", "Build: %s слайды: в %s картинок нет — vision пропускаю", tag, slidesDir)
	default:
		logx.Infof("notes", "Build: %s слайды: в vision %d картинок из %s (лимит 60)", tag, slidesIn, slidesDir)
	}

	visionStart := time.Now()
	det, err := DescribeSlidesDetailed(ctx, cfg, slidesDir)
	slideText := det.Text
	if err != nil {
		logx.Errorf("notes", "Build: %s слайды: %v", tag, err)
	}
	if slidesIn > 0 && visionReady {
		logx.Infof("notes", "Build: %s vision: описано chars=%d за %s",
			tag, len([]rune(slideText)), time.Since(visionStart).Round(time.Millisecond))
	}

	switch {
	case det.Incomplete:
		// Часть слайдов осталась без vision-описания: отдаём текст как есть, но кэш не трогаем как окончательный.
		logx.Warnf("notes", "Build: %s слайды: неполно — vision не распознал слайды %v, в кэш не пишу (пересчитаю при следующей сборке)",
			tag, det.FailedSlides)
		bc.SlidesHash = ""
		bc.SlidesDesc = ""
		_ = saveBuildCache(dir, bc)
	case slideText != "" && slidesHash != "":
		bc.SlidesHash = slidesHash
		bc.SlidesDesc = slideText
		if err := writeFileAtomic(descFile, []byte(slideText), 0644); err != nil {
			logx.Warnf("notes", "Build: %s запись %s: %v", tag, descFile, err)
		}
		_ = saveBuildCache(dir, bc)
	}

	return det, err
}

// stepNotes формирует конспект notes.md через LLM либо берёт готовый файл с диска.
func stepNotes(ctx context.Context, cfg *config.Config, dir, mdPath string, discipline string, number int, transcript, slidesText string, bc *buildCache) (string, error) {
	tag := lectureOf(ctx).tag()
	inputHash := computeSummarizeInputHash(discipline, number, transcript, slidesText)

	if rawMD, err := os.ReadFile(mdPath); err == nil {
		existingMD := strings.TrimSpace(string(rawMD))
		if len(existingMD) > 0 {
			// Если хэш входа совпадает или конспект уже есть после падения на WritePDF
			if bc.SummarizeInputHash == inputHash || bc.SummarizeInputHash == "" {
				logx.Infof("notes", "Build: %s конспект взят из кэша chars=%d (input_hash=%s)",
					tag, len([]rune(existingMD)), inputHash[:min(12, len(inputHash))])
				bc.SummarizeInputHash = inputHash
				bc.NotesMDHash = computeStringSHA256(existingMD)
				_ = saveBuildCache(dir, bc)
				return existingMD, nil
			}
			logx.Infof("notes", "Build: %s входные данные конспекта изменились — повторная суммаризация", tag)
		}
	}

	title := fmt.Sprintf("%s · лекция %d", discipline, number)
	logx.Debugf("notes", "Build: %s summarizing title=%q", tag, title)
	md, err := Summarize(ctx, cfg, discipline, number, transcript, slidesText)
	if err != nil {
		logx.Errorf("notes", "Build: %s summarize: %v", tag, err)
		return "", err
	}

	if err := writeFileAtomic(mdPath, []byte(md), 0644); err != nil {
		logx.Errorf("notes", "Build: %s write notes %s: %v", tag, mdPath, err)
	} else {
		logx.Debugf("notes", "Build: %s notes written path=%s bytes=%d", tag, mdPath, len(md))
	}

	bc.SummarizeInputHash = inputHash
	bc.NotesMDHash = computeStringSHA256(md)
	_ = saveBuildCache(dir, bc)

	return md, nil
}

// stepPDF рендерит PDF из Markdown либо пропускает шаг, если валидный PDF уже существует.
func stepPDF(ctx context.Context, dir, pdfPath, title, md string, bc *buildCache) error {
	mdHash := computeStringSHA256(md)

	st, err := os.Stat(pdfPath)
	if err == nil && st.Size() > 0 {
		// Если notes.md не изменился с момента создания PDF
		if bc.PDFNotesHash == mdHash || (bc.PDFNotesHash == "" && bc.NotesMDHash == mdHash) {
			logx.Infof("notes", "Build: pdf взят из кэша bytes=%d путь=%s", st.Size(), pdfPath)
			bc.PDFNotesHash = mdHash
			_ = saveBuildCache(dir, bc)
			return nil
		}
	}

	pdfStart := time.Now()
	if err := WritePDF(ctx, title, md, pdfPath); err != nil {
		logx.Errorf("notes", "Build: pdf %s: %v", pdfPath, err)
		return err
	}
	logx.Infof("notes", "Build: pdf готов bytes=%d за %s путь=%s",
		archive.FileSize(pdfPath), time.Since(pdfStart).Round(time.Millisecond), pdfPath)

	bc.PDFNotesHash = mdHash
	_ = saveBuildCache(dir, bc)
	return nil
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

	bc := loadBuildCache(dir)

	// Этап 1: Расшифровка аудиозаписи (с проверкой порога роста и кэшированием в transcript.txt)
	transcript, audioEmpty, why, err := stepTranscript(ctx, cfg, dir, audio, trPath, bc)
	if err != nil {
		return err
	}

	// Этап 2: Описание слайдов (с кэшированием по SHA-256 картинок)
	slidesRes, err := stepSlides(ctx, cfg, dir, slides, bc)
	slideText := slidesRes.Text
	if err != nil {
		logx.Errorf("notes", "Build: %s слайды: %v", tag, err)
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

	// Этап 3: Генерация конспекта notes.md через LLM (с кэшированием по хэшу входа)
	md, err := stepNotes(ctx, cfg, dir, mdPath, p.Discipline, p.Number, transcript, slideText, bc)
	if err != nil {
		return err
	}

	// Этап 4: Рендер PDF (с пропуском, если PDF уже актуален)
	title := fmt.Sprintf("%s · лекция %d", p.Discipline, p.Number)
	if err := stepPDF(ctx, dir, pdfPath, title, md, bc); err != nil {
		return err
	}

	if slidesRes.Incomplete {
		logx.Warnf("notes", "Build: %s конспект записан, но слайды не распознаны полностью: %v", tag, slidesRes.FailedSlides)
		return fmt.Errorf("%w: слайды %v", ErrSlidesIncomplete, slidesRes.FailedSlides)
	}
	logx.Infof("notes", "Build: %s готово за %s transcript_chars=%d slides_chars=%d notes_chars=%d",
		tag, time.Since(started).Round(time.Second), len([]rune(transcript)), len([]rune(slideText)), len([]rune(md)))
	return nil
}
