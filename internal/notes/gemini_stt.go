package notes

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/capture"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/llm"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

const (
	// Длительность одного фрагмента для Gemini (10 мин, внутри диапазона 8–15 мин).
	geminiChunkDuration = 10 * time.Minute
	// Перекрытие соседних фрагментов для бесшовной склейки (8 с, внутри 5–10 с).
	geminiChunkOverlap = 8 * time.Second
	// Порог минимального остатка, чтобы последний фрагмент не был короче 15 секунд.
	geminiMinTailDuration = 15 * time.Second
	// Запас, при котором аудио до 12 минут отправляется одним фрагментом без нарезки.
	geminiSingleChunkSlack = 2 * time.Minute
	// Таймаут конвертации одного фрагмента через ffmpeg.
	geminiFFmpegTimeout = 5 * time.Minute

	// Минимальное число совпавших слов на стыке кусков (при прямом совпадении и при обрезке первого слова).
	geminiMinOverlapWords        = 3
	geminiMinShiftedOverlapWords = 4
	geminiMaxOverlapWords        = 80

	geminiSTTSysPrompt = "Ты — точный расшифровщик аудиозаписей университетских лекций. " +
		"Сделай точную дословную расшифровку речи на русском языке, сохраняя все научные термины, формулы словами и имена собственные. " +
		"Разбивай текст на абзацы по смыслу. " +
		"Регулярно расставляй метки времени строго в формате [мм:сс] (например, [00:05], [02:34]) относительно начала этого аудиофрагмента. " +
		"Без вступлений, пояснений, заголовков и выдумок — выводи только саму расшифровку."

	geminiSTTUserPrompt = "Дословно расшифруй этот аудиофрагмент лекции на русском языке с метками времени [мм:сс] от начала фрагмента и абзацами по смыслу."
)

var (
	// Переменные-хуки позволяют подменять ffmpeg/probe в модульных тестах.
	probeAudioFn = capture.Probe
	sliceAudioFn = sliceAudioFFmpeg

	reTimestamp       = regexp.MustCompile(`\[(?:(\d+):)?(\d{1,3}):(\d{2})\]`)
	reTimestampPrefix = regexp.MustCompile(`^\[(?:(\d+):)?(\d{1,3}):(\d{2})\]`)
)

type audioChunk struct {
	Start  time.Duration
	Length time.Duration
}

// geminiKeyMarker возвращает непустой маркер, если в пуле есть хотя бы один ключ Gemini с поддержкой аудио.
func geminiKeyMarker(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	if len(llm.MediaTargets(llm.PoolChain(cfg), true, false)) > 0 {
		return "pool"
	}
	return ""
}

// transcribeGemini расшифровывает аудиозапись лекции через бесплатный пул ключей Gemini,
// нарезая длинные записи на фрагменты с перекрытием и обрабатывая их параллельно.
// Фрагмент, который Gemini не расшифровал после перебора ключей, отдаётся cheaper (если ключ задан).
func transcribeGemini(ctx context.Context, cfg *config.Config, audioPath string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	tag := lectureOf(ctx).tag()

	chain := llm.MediaTargets(llm.PoolChain(cfg), true, false)
	if err := ValidateChain(chain); err != nil {
		return "", err
	}
	if len(chain) == 0 {
		return "", fmt.Errorf("нет ключей Gemini в пуле")
	}

	st, err := os.Stat(audioPath)
	if err != nil {
		logx.Errorf("notes", "transcribeGemini: %s stat %s: %v", tag, audioPath, err)
		return "", err
	}
	if st.Size() < 2048 {
		logx.Errorf("notes", "transcribeGemini: %s аудио слишком короткое (%d байт)", tag, st.Size())
		return "", fmt.Errorf("аудио слишком короткое (%d байт)", st.Size())
	}

	dur, _, probeErr := probeAudioFn(ctx, audioPath)
	var chunks []audioChunk
	if probeErr == nil && dur > 0 {
		if dur <= geminiChunkDuration+geminiSingleChunkSlack {
			chunks = []audioChunk{{Start: 0, Length: dur}}
		} else {
			chunks = planAudioChunks(dur, geminiChunkDuration, geminiChunkOverlap)
		}
	} else {
		if probeErr != nil {
			logx.Warnf("notes", "transcribeGemini: %s не удалось определить длительность (%v), отправляю целиком", tag, probeErr)
		}
		chunks = []audioChunk{{Start: 0, Length: 0}}
	}
	if len(chunks) == 0 {
		chunks = []audioChunk{{Start: 0, Length: 0}}
	}

	tmpDir, err := os.MkdirTemp("", "gemini-stt-*")
	if err != nil {
		return "", fmt.Errorf("gemini stt: временная директория: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	chunkPaths := make([]string, len(chunks))
	for i, ch := range chunks {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		outMP3 := filepath.Join(tmpDir, fmt.Sprintf("chunk_%03d.mp3", i))
		if err := sliceAudioFn(ctx, audioPath, ch.Start, ch.Length, outMP3); err != nil {
			return "", fmt.Errorf("gemini stt: нарезка фрагмента %d: %w", i+1, err)
		}
		info, err := os.Stat(outMP3)
		if err != nil {
			return "", fmt.Errorf("gemini stt: файл фрагмента %d: %w", i+1, err)
		}
		if info.Size() == 0 {
			return "", fmt.Errorf("gemini stt: пустой фрагмент %d", i+1)
		}
		if info.Size() > llm.MaxMediaPartBytes {
			return "", fmt.Errorf("gemini stt: фрагмент %d (%d байт) превышает лимит %d байт", i+1, info.Size(), llm.MaxMediaPartBytes)
		}
		chunkPaths[i] = outMP3
	}

	numKeys := countUniqueGeminiKeys(chain)
	workers := min(len(chunks), max(2, numKeys*llm.MaxConcurrentPerKey))
	logx.Infof("notes", "transcribeGemini: %s chunks=%d keys=%d workers=%d dur=%s", tag, len(chunks), numKeys, workers, dur.Round(time.Second))

	results := make([]string, len(chunks))
	err = runChunks(ctx, len(chunks), workers, func(wctx context.Context, idx int) error {
		data, err := os.ReadFile(chunkPaths[idx])
		if err != nil {
			return fmt.Errorf("gemini stt: чтение фрагмента %d: %w", idx+1, err)
		}
		fresh := llm.MediaTargets(llm.PoolChain(cfg), true, false)
		if err := ValidateChain(fresh); err != nil {
			return err
		}
		chunkChain := rotateTargetsByChunk(fresh, idx)
		if len(chunkChain) == 0 {
			return errors.New("нет ключей Gemini в пуле")
		}
		res, err := llm.AskAudio(wctx, chunkChain, geminiSTTSysPrompt, geminiSTTUserPrompt, data, "mp3")
		if err == nil {
			if text := strings.TrimSpace(res.Text); text != "" {
				results[idx] = shiftTimestamps(text, chunks[idx].Start)
				return nil
			}
			err = errors.New("вернул пустой текст")
		}
		// Gemini не справился с фрагментом (после перебора всех ключей): только этот кусок
		// уходит в cheaper, остальные фрагменты Gemini не пересчитываются. Если cheaper тоже
		// не смог, ошибка уходит наверх и вся расшифровка переходит к следующему провайдеру.
		if wctx.Err() != nil || cheaperKeyMarker(cfg) == "" {
			return fmt.Errorf("gemini stt: фрагмент %d: %v", idx+1, err)
		}
		logx.Warnf("notes", "transcribeGemini: %s фрагмент %d не расшифрован Gemini (%v), пробую cheaper", tag, idx+1, err)
		text, cErr := cheaperRecognize(wctx, cfg, chunkPaths[idx], chunks[idx].Start)
		if cErr != nil {
			return fmt.Errorf("gemini stt: фрагмент %d: %v; cheaper: %v", idx+1, err, cErr)
		}
		results[idx] = text
		return nil
	})
	if err != nil {
		return "", err
	}
	stitched := stitchTranscripts(results)
	if strings.TrimSpace(stitched) == "" {
		return "", errors.New("gemini stt: пустой результат расшифровки")
	}
	return stitched, nil
}

// runChunks выполняет do(ctx, idx) для кусков 0..n-1 в workers параллельных горутинах.
// Первая ошибка отменяет контекст оставшихся кусков и возвращается. Если отменён
// родительский контекст, возвращается его ошибка. Общая часть для Gemini и cheaper.
func runChunks(ctx context.Context, n, workers int, do func(ctx context.Context, idx int) error) error {
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobs := make(chan int, n)
	for i := range n {
		jobs <- i
	}
	close(jobs)

	var (
		wg       sync.WaitGroup
		errMu    sync.Mutex
		firstErr error
	)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				if wctx.Err() != nil {
					return
				}
				if err := do(wctx, idx); err != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = err
						cancel()
					}
					errMu.Unlock()
					return
				}
			}
		}()
	}
	wg.Wait()

	errMu.Lock()
	defer errMu.Unlock()
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}

// planAudioChunks строит план нарезки аудио длительностью dur на фрагменты длиной chunkDur
// с перекрытием overlap и шагом chunkDur - overlap. Короткий последний хвост (< 15 с)
// присоединяется к предыдущему фрагменту.
func planAudioChunks(dur, chunkDur, overlap time.Duration) []audioChunk {
	if dur <= 0 {
		return nil
	}
	if chunkDur <= 0 {
		chunkDur = geminiChunkDuration
	}
	if overlap < 0 {
		overlap = 0
	}
	if overlap >= chunkDur {
		overlap = chunkDur / 4
	}
	if dur <= chunkDur {
		return []audioChunk{{Start: 0, Length: dur}}
	}

	step := chunkDur - overlap
	var chunks []audioChunk
	for start := time.Duration(0); start < dur; start += step {
		end := start + chunkDur
		if end >= dur {
			chunks = append(chunks, audioChunk{Start: start, Length: dur - start})
			break
		}
		nextStart := start + step
		if dur-nextStart < geminiMinTailDuration {
			chunks = append(chunks, audioChunk{Start: start, Length: dur - start})
			break
		}
		chunks = append(chunks, audioChunk{Start: start, Length: chunkDur})
	}
	return chunks
}

// sliceAudioFFmpeg режет фрагмент для Gemini: mp3 mono 16 кГц, 64 кбит/с.
func sliceAudioFFmpeg(ctx context.Context, audioPath string, start, length time.Duration, outMP3 string) error {
	return sliceAudioFFmpegBitrate(ctx, audioPath, start, length, "64k", outMP3)
}

// sliceAudioFFmpegBitrate вырезает фрагмент [start, start+length) в mp3 mono 16 кГц с заданным
// битрейтом. Общая нарезка для Gemini и cheaper.
func sliceAudioFFmpegBitrate(ctx context.Context, audioPath string, start, length time.Duration, bitrate, outMP3 string) error {
	fctx, cancel := context.WithTimeout(ctx, geminiFFmpegTimeout)
	defer cancel()

	args := []string{"-hide_banner", "-loglevel", "error", "-y"}
	if start > 0 {
		args = append(args, "-ss", formatFFmpegSeconds(start))
	}
	if length > 0 {
		args = append(args, "-t", formatFFmpegSeconds(length))
	}
	args = append(args, "-i", audioPath, "-vn", "-ac", "1", "-ar", "16000", "-b:a", bitrate, "-f", "mp3", outMP3)

	cmd := exec.CommandContext(fctx, "ffmpeg", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := truncate(string(out), 300)
		if msg != "" {
			return fmt.Errorf("ffmpeg: %w (%s)", err, msg)
		}
		return fmt.Errorf("ffmpeg: %w", err)
	}
	return nil
}

func formatFFmpegSeconds(d time.Duration) string {
	return fmt.Sprintf("%.3f", d.Seconds())
}

func countUniqueGeminiKeys(chain []llm.Target) int {
	seen := make(map[string]struct{}, len(chain))
	for _, t := range chain {
		seen[fmt.Sprintf("%s#%d", t.Provider, t.KeyIdx)] = struct{}{}
	}
	return len(seen)
}

// rotateTargetsByChunk упорядочивает ключи внутри каждой модели по KeyIdx и циклически
// сдвигает их на chunkIdx, чтобы параллельные фрагменты начинали работу с разных ключей.
func rotateTargetsByChunk(chain []llm.Target, chunkIdx int) []llm.Target {
	if len(chain) <= 1 {
		out := make([]llm.Target, len(chain))
		copy(out, chain)
		return out
	}

	type modelGroupKey struct {
		provider string
		model    string
	}
	groups := make(map[modelGroupKey][]llm.Target)
	var order []modelGroupKey
	for _, t := range chain {
		k := modelGroupKey{provider: t.Provider, model: t.Model}
		if _, exists := groups[k]; !exists {
			order = append(order, k)
		}
		groups[k] = append(groups[k], t)
	}

	out := make([]llm.Target, 0, len(chain))
	for _, k := range order {
		g := groups[k]
		sort.Slice(g, func(i, j int) bool {
			return g[i].KeyIdx < g[j].KeyIdx
		})
		shift := 0
		if len(g) > 0 {
			shift = ((chunkIdx % len(g)) + len(g)) % len(g)
		}
		for i := range g {
			out = append(out, g[(i+shift)%len(g)])
		}
	}
	return out
}

// shiftTimestamps находит метки времени [мм:сс] (или [ч:мм:сс]), прибавляет offset
// и форматирует их обратно в [мм:сс] (включая значения >= 60 минут, например [75:12]).
func shiftTimestamps(text string, offset time.Duration) string {
	offsetSec := int(math.Round(offset.Seconds()))
	return reTimestamp.ReplaceAllStringFunc(text, func(match string) string {
		sub := reTimestamp.FindStringSubmatch(match)
		if len(sub) != 4 {
			return match
		}
		sec, err := strconv.Atoi(sub[3])
		if err != nil || sec < 0 || sec >= 60 {
			return match
		}
		mins, err := strconv.Atoi(sub[2])
		if err != nil || mins < 0 {
			return match
		}
		hours := 0
		if sub[1] != "" {
			if mins >= 60 {
				return match
			}
			hours, err = strconv.Atoi(sub[1])
			if err != nil || hours < 0 {
				return match
			}
		}
		totalSec := hours*3600 + mins*60 + sec + offsetSec
		if totalSec < 0 {
			totalSec = 0
		}
		return fmt.Sprintf("[%02d:%02d]", totalSec/60, totalSec%60)
	})
}

type stitchWord struct {
	norm  string
	start int
	end   int
}

type stitchTimestamp struct {
	text  string
	start int
	end   int
}

// tokenizeForStitch извлекает нормализованные слова (без учёта меток времени, регистра
// и пунктуации) и позиции меток времени в исходной строке.
func tokenizeForStitch(s string) ([]stitchWord, []stitchTimestamp) {
	tsMatches := reTimestamp.FindAllStringIndex(s, -1)
	timestamps := make([]stitchTimestamp, 0, len(tsMatches))
	for _, m := range tsMatches {
		timestamps = append(timestamps, stitchTimestamp{
			text:  s[m[0]:m[1]],
			start: m[0],
			end:   m[1],
		})
	}

	var words []stitchWord
	scanSegment := func(segStart, segEnd int) {
		i := segStart
		for i < segEnd {
			for i < segEnd {
				r, sz := utf8.DecodeRuneInString(s[i:segEnd])
				if !unicode.IsSpace(r) {
					break
				}
				i += sz
			}
			if i >= segEnd {
				break
			}
			tokStart := i
			var normBuf strings.Builder
			for i < segEnd {
				r, sz := utf8.DecodeRuneInString(s[i:segEnd])
				if unicode.IsSpace(r) {
					break
				}
				if unicode.IsLetter(r) || unicode.IsDigit(r) {
					normBuf.WriteRune(unicode.ToLower(r))
				}
				i += sz
			}
			if normBuf.Len() > 0 {
				words = append(words, stitchWord{
					norm:  normBuf.String(),
					start: tokStart,
					end:   i,
				})
			}
		}
	}

	pos := 0
	for _, ts := range timestamps {
		if ts.start > pos {
			scanSegment(pos, ts.start)
		}
		pos = ts.end
	}
	if pos < len(s) {
		scanSegment(pos, len(s))
	}
	return words, timestamps
}

func findOverlap(prevWords, nextWords []stitchWord) (skipNext, overlapLen int) {
	maxK := min(min(len(prevWords), len(nextWords)), geminiMaxOverlapWords)
	if maxK < geminiMinOverlapWords {
		return 0, 0
	}

	matchFrom := func(skip, minK int) int {
		avail := min(min(len(prevWords), len(nextWords)-skip), geminiMaxOverlapWords)
		for k := avail; k >= minK; k-- {
			ok := true
			base := len(prevWords) - k
			for i := 0; i < k; i++ {
				if prevWords[base+i].norm != nextWords[skip+i].norm {
					ok = false
					break
				}
			}
			if ok {
				return k
			}
		}
		return 0
	}

	if k := matchFrom(0, geminiMinOverlapWords); k > 0 {
		return 0, k
	}
	// Допускаем обрыв первого слова (1 токен в начале next является суффиксом слова в prev) при >= 3 полных словах.
	if len(nextWords) >= 1+geminiMinOverlapWords {
		if k := matchFrom(1, geminiMinOverlapWords); k > 0 {
			prevWordIdx := len(prevWords) - k - 1
			if k >= geminiMinShiftedOverlapWords ||
				(prevWordIdx >= 0 && strings.HasSuffix(prevWords[prevWordIdx].norm, nextWords[0].norm)) {
				return 1, k
			}
		}
	}
	for skip := 2; skip <= 2; skip++ {
		if len(nextWords)-skip < geminiMinShiftedOverlapWords {
			break
		}
		if k := matchFrom(skip, geminiMinShiftedOverlapWords); k > 0 {
			return skip, k
		}
	}
	return 0, 0
}

// advancePastTrailingPunct пропускает пробелы и одиночную пунктуацию без слов
// после конца вырезанного дубликата.
func advancePastTrailingPunct(s string, pos int) int {
	for pos < len(s) {
		r, sz := utf8.DecodeRuneInString(s[pos:])
		if unicode.IsSpace(r) {
			pos += sz
			continue
		}
		if strings.HasPrefix(s[pos:], "[") && reTimestampPrefix.MatchString(s[pos:]) {
			break
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			break
		}
		// Знак препинания без букв/цифр на границе вырезанного куска тоже убираем.
		pos += sz
	}
	return pos
}

// stitchTranscripts склеивает последовательные расшифровки фрагментов, удаляя
// продублированные на перекрытии слова из начала следующего куска и сохраняя метки времени.
func stitchTranscripts(chunks []string) string {
	var acc string
	for _, raw := range chunks {
		next := strings.TrimSpace(raw)
		if next == "" {
			continue
		}
		if acc == "" {
			acc = next
			continue
		}

		prevWords, _ := tokenizeForStitch(acc)
		nextWords, nextTS := tokenizeForStitch(next)
		skipNext, overlapLen := findOverlap(prevWords, nextWords)
		if overlapLen == 0 {
			acc += "\n\n" + next
			continue
		}

		lastMatched := nextWords[skipNext+overlapLen-1]
		cutPos := advancePastTrailingPunct(next, lastMatched.end)
		rest := strings.TrimSpace(next[cutPos:])
		if rest == "" {
			continue
		}

		// Если в вырезанной части были метки времени, а оставшийся текст не начинается
		// с метки времени, переносим последнюю вырезанную метку на стык.
		if !reTimestampPrefix.MatchString(rest) {
			var lastRemovedTS string
			for _, ts := range nextTS {
				if ts.end <= cutPos {
					lastRemovedTS = ts.text
				}
			}
			if lastRemovedTS != "" {
				rest = lastRemovedTS + " " + rest
			}
		}

		if endsWithSentencePunct(acc) {
			acc += "\n" + rest
		} else {
			acc += " " + rest
		}
	}
	return acc
}

func endsWithSentencePunct(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(s)
	return r == '.' || r == '!' || r == '?' || r == '…'
}
