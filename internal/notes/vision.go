package notes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/llm"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

// Gemini через OpenAI-совместимый вход AI Studio.
const (
	GeminiOpenAIURL   = "https://generativelanguage.googleapis.com/v1beta/openai"
	GeminiVisionModel = "gemini-3.6-flash"

	visionBatchSize = 6
	maxVisionSlides = 60

	visionSysPrompt = "Ты помогаешь составлять подробный конспект университетской лекции. " +
		"Распознавай и описывай слайды презентации максимально точно: выписывай весь текст, " +
		"все формулы строго в LaTeX ($...$ в строке, $$...$$ отдельной строкой), " +
		"объясняй смысл схем, графиков и таблиц. Пиши по-русски, без вступлений и лишних комментариев. " +
		"Каждый слайд оформляй строго под заголовком «=== Слайд N ===»."
)

var (
	reStrictSlideHeader = regexp.MustCompile(`(?i)^[#*` + "`" + `_\s]*={2,}\s*(?:слайд|slide)\s*(?:№\s*)?(\d+)(?:\s*\[[^\]]*\])?\s*={2,}[#*` + "`" + `_\s]*:?\s*(.*)$`)
	reSoftSlideHeader   = regexp.MustCompile(`(?i)^[#*` + "`" + `_\s]*(?:слайд|slide)\s*(?:№\s*)?(\d+)(?:\s*\[[^\]]*\])?[#*` + "`" + `_\s]*(?:[:\-—]+\s*(.*)|\s*)$`)
)

// slideManifestItem локально повторяет поля slides.json без импорта internal/bbb
// (во избежание цикла импортов internal/bbb -> internal/notes).
type slideManifestItem struct {
	Num          int    `json:"num"`
	File         string `json:"file"`
	Text         string `json:"text,omitempty"`
	FirstShownAt string `json:"first_shown_at,omitempty"`
	ShownOffsetS int    `json:"shown_offset_sec,omitempty"`
	Timestamp    string `json:"timestamp,omitempty"`
}

type slideEntry struct {
	num       int
	filePath  string
	fileName  string
	text      string
	timestamp string
}

type visionSlideItem struct {
	num       int
	timestamp string
	raw       []byte
	mime      string
}

type visionProvider struct {
	name  string
	key   string
	base  string
	model string
}

func legacyVisionProviders(cfg *config.Config) []visionProvider {
	if cfg == nil {
		return nil
	}
	var out []visionProvider
	if k := strings.TrimSpace(cfg.GroqAPIKey); k != "" {
		base := strings.TrimRight(cfg.GroqAPIURL, "/")
		if base == "" {
			base = "https://api.groq.com/openai/v1"
		}
		model := cfg.GroqVisionModel
		if model == "" {
			model = "qwen/qwen3.6-27b"
		}
		out = append(out, visionProvider{name: "groq", key: k, base: base, model: model})
	}
	if k := strings.TrimSpace(cfg.GrokAPIKey); k != "" {
		base := strings.TrimRight(cfg.GrokAPIURL, "/")
		if base == "" {
			base = "https://api.x.ai/v1"
		}
		model := cfg.GrokModel
		if model == "" {
			model = "grok-2-vision-1212"
		}
		out = append(out, visionProvider{name: "grok", key: k, base: base, model: model})
	}
	return out
}

func visionProviders(cfg *config.Config) []visionProvider {
	logx.Debugf("notes", "visionProviders: cfg_nil=%t", cfg == nil)
	if cfg == nil {
		return nil
	}
	out := legacyVisionProviders(cfg)
	// Ключи Gemini для отображения настроенности vision (основная работа идёт через llm.PoolChain).
	for i, k := range llm.ParseKeys(cfg.GeminiAPIKeys...) {
		if i >= 4 {
			break
		}
		out = append(out, visionProvider{name: "gemini/" + k.Label, key: k.Secret, base: GeminiOpenAIURL, model: GeminiVisionModel})
	}
	logx.Debugf("notes", "visionProviders: count=%d", len(out))
	for i, p := range out {
		logx.Debugf("notes", "visionProviders: [%d] name=%s model=%s base=%s key_len=%d", i, p.name, p.model, p.base, len(p.key))
	}
	return out
}

func hasVisionConfigured(cfg *config.Config) bool {
	return len(llm.MediaTargets(llm.PoolChain(cfg), false, true)) > 0 || len(visionProviders(cfg)) > 0
}

// visionBatchAttempts — сколько раз пробуем один батч: первый вызов и два повтора.
const visionBatchAttempts = 3

// visionRetryDelay — базовая пауза перед повтором батча: перед попыткой n ждём n*visionRetryDelay.
// Переменная, а не константа, чтобы тесты не ждали реальные секунды.
var visionRetryDelay = 10 * time.Second

// SlidesResult — результат распознавания слайдов.
type SlidesResult struct {
	// Text — описание для конспекта: vision-описания и текстовый слой из slides.json.
	Text string
	// Incomplete — часть батчей не распознана даже после повторов. Для их слайдов в Text
	// только текстовый слой из slides.json (или ничего). Такой результат нельзя кэшировать как окончательный.
	Incomplete bool
	// FailedSlides — номера слайдов из батчей, которые не удалось распознать.
	FailedSlides []int
}

// DescribeSlides возвращает только текст описания слайдов; подробности — в DescribeSlidesDetailed.
func DescribeSlides(ctx context.Context, cfg *config.Config, slidesDir string) (string, error) {
	res, err := DescribeSlidesDetailed(ctx, cfg, slidesDir)
	return res.Text, err
}

// DescribeSlidesDetailed распознаёт слайды из каталога slidesDir батчами через пул LLM-ключей
// (только бесплатные vision-ключи без Cheaper/AdminOnly) и при необходимости подставляет
// текстовый слой из slides.json с отметками времени первого показа.
// Неудавшийся батч повторяется до visionBatchAttempts раз. Если и это не помогло, его слайды
// берутся из текстового слоя, а результат помечается как Incomplete.
func DescribeSlidesDetailed(ctx context.Context, cfg *config.Config, slidesDir string) (SlidesResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	logx.Debugf("notes", "DescribeSlides: start dir=%s", slidesDir)

	entries, err := loadSlideEntries(slidesDir)
	if err != nil {
		return SlidesResult{}, err
	}
	if len(entries) == 0 {
		logx.Debugf("notes", "DescribeSlides: no slides or text layer in %s, skip", slidesDir)
		return SlidesResult{}, nil
	}

	visionItems := make([]visionSlideItem, 0, len(entries))
	for _, e := range entries {
		if e.filePath == "" {
			continue
		}
		raw, err := os.ReadFile(e.filePath)
		if err != nil {
			logx.Warnf("notes", "DescribeSlides: read slide %d (%s): %v", e.num, e.filePath, err)
			continue
		}
		if len(raw) < 32 {
			logx.Warnf("notes", "DescribeSlides: slide %d (%s) too small (%d bytes), skip image", e.num, e.filePath, len(raw))
			continue
		}
		if len(raw) > llm.MaxMediaPartBytes {
			logx.Warnf("notes", "DescribeSlides: slide %d (%s) exceeds limit (%d bytes), skip image", e.num, e.filePath, len(raw))
			continue
		}
		visionItems = append(visionItems, visionSlideItem{
			num:       e.num,
			timestamp: e.timestamp,
			raw:       raw,
			mime:      detectImageMIME(e.filePath),
		})
	}

	visionTexts := make(map[int]string, len(visionItems))
	var lastErr error
	var failedSlides []int

	poolChain := llm.MediaTargets(llm.PoolChain(cfg), false, true)
	if err := ValidateChain(poolChain); err != nil {
		return SlidesResult{}, err
	}

	usePool := len(poolChain) > 0
	var legacyProvs []visionProvider
	if !usePool {
		legacyProvs = legacyVisionProviders(cfg)
	}

	if len(visionItems) > 0 && (usePool || len(legacyProvs) > 0) {
		for start := 0; start < len(visionItems); start += visionBatchSize {
			if err := ctx.Err(); err != nil {
				return SlidesResult{}, err
			}
			end := start + visionBatchSize
			if end > len(visionItems) {
				end = len(visionItems)
			}
			batch := visionItems[start:end]
			batchNums := make([]int, len(batch))
			parts := make([]llm.Part, 0, 1+len(batch))
			parts = append(parts, llm.TextPart(buildBatchVisionPrompt(batch)))
			for i, it := range batch {
				batchNums[i] = it.num
				parts = append(parts, llm.ImagePart(it.raw, it.mime))
			}

			reply, batchErr := askVisionBatch(ctx, poolChain, usePool, legacyProvs, parts, batchNums)
			if err := ctx.Err(); err != nil {
				return SlidesResult{}, err
			}
			if batchErr != nil {
				logx.Warnf("notes", "DescribeSlides: batch %d..%d не распознан после %d попыток: %v",
					batchNums[0], batchNums[len(batchNums)-1], visionBatchAttempts, batchErr)
				lastErr = batchErr
				failedSlides = append(failedSlides, batchNums...)
				continue
			}

			parsed := parseBatchSlideDescriptions(reply, batchNums)
			for num, txt := range parsed {
				if strings.TrimSpace(txt) != "" {
					visionTexts[num] = strings.TrimSpace(txt)
				}
			}
		}
	} else if len(visionItems) > 0 {
		logx.Debugf("notes", "DescribeSlides: no vision keys configured, using text layer from slides.json if present")
	}

	var b strings.Builder
	for _, e := range entries {
		desc := strings.TrimSpace(visionTexts[e.num])
		if desc == "" {
			desc = strings.TrimSpace(e.text)
		}
		if desc == "" {
			continue
		}
		if e.timestamp != "" {
			fmt.Fprintf(&b, "Слайд %d %s:\n%s\n\n", e.num, e.timestamp, desc)
		} else {
			fmt.Fprintf(&b, "Слайд %d:\n%s\n\n", e.num, desc)
		}
	}

	res := strings.TrimSpace(b.String())
	incomplete := len(failedSlides) > 0
	if res == "" && lastErr != nil {
		logx.Errorf("notes", "DescribeSlides: all vision batches failed and no text layer: %v", lastErr)
		return SlidesResult{Incomplete: true, FailedSlides: failedSlides}, lastErr
	}

	logx.Infof("notes", "DescribeSlides: done slides=%d vision=%d result_chars=%d incomplete=%t",
		len(entries), len(visionTexts), len(res), incomplete)
	return SlidesResult{Text: res, Incomplete: incomplete, FailedSlides: failedSlides}, nil
}

// askVisionBatch отправляет батч и при неудаче повторяет его с растущей паузой.
// Пустой ответ считается неудачей. Отмена ctx прерывает повторы сразу.
func askVisionBatch(ctx context.Context, poolChain []llm.Target, usePool bool, legacyProvs []visionProvider, parts []llm.Part, nums []int) (string, error) {
	var lastErr error
	for attempt := 1; attempt <= visionBatchAttempts; attempt++ {
		reply, err := askVisionOnce(ctx, poolChain, usePool, legacyProvs, parts)
		if err == nil {
			return reply, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		lastErr = err
		// Все ключи на паузе из-за лимита: повтор сейчас ничего не даст, сдаёмся сразу.
		if attempt == visionBatchAttempts || errors.Is(err, llm.ErrAllCooldown) {
			break
		}
		wait := visionRetryDelay * time.Duration(attempt)
		logx.Warnf("notes", "DescribeSlides: batch %d..%d попытка %d/%d неудачна: %v, повтор через %s",
			nums[0], nums[len(nums)-1], attempt, visionBatchAttempts, err, wait)
		if !visionPause(ctx, wait) {
			return "", ctx.Err()
		}
	}
	return "", lastErr
}

// askVisionOnce — одна попытка отправки батча: через пул ключей либо через запасных провайдеров.
func askVisionOnce(ctx context.Context, poolChain []llm.Target, usePool bool, legacyProvs []visionProvider, parts []llm.Part) (string, error) {
	if !usePool {
		return describePartsLegacy(ctx, legacyProvs, visionSysPrompt, parts)
	}
	res, err := llm.AskParts(ctx, poolChain, visionSysPrompt, parts)
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(res.Text)
	if text == "" {
		return "", errors.New("vision: пустой ответ")
	}
	return text, nil
}

// visionPause ждёт перед повтором батча; в тестах подменяется, чтобы не ждать реальные секунды.
var visionPause = sleepVisionRetry

// sleepVisionRetry ждёт d или отмены ctx. Возвращает false, если ctx отменён раньше.
func sleepVisionRetry(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// DescribeImage возвращает текстовое описание одной картинки (рисунка из вопроса теста).
// Сначала пробует vision-ключи из общего пула, а при их отсутствии или отказе
// использует запасных настроенных провайдеров Groq / Grok.
func DescribeImage(ctx context.Context, cfg *config.Config, raw []byte, mime, prompt string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(raw) < 32 {
		return "", fmt.Errorf("пустой файл")
	}
	if !strings.HasPrefix(mime, "image/") {
		mime = "image/png"
	}

	var errs []string
	chain := llm.MediaTargets(llm.PoolChain(cfg), false, true)
	if len(chain) > 0 {
		res, err := llm.AskImage(ctx, chain, "", prompt, raw, mime)
		if err == nil && strings.TrimSpace(res.Text) != "" {
			return strings.TrimSpace(res.Text), nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		if err != nil {
			errs = append(errs, err.Error())
		} else {
			errs = append(errs, "пусто")
		}
	}

	provs := legacyVisionProviders(cfg)
	if len(chain) == 0 && len(provs) == 0 {
		return "", fmt.Errorf("нет ключа vision (GEMINI_API_KEYS / GROQ_API_KEY / GROK_API_KEY)")
	}
	parts := make([]llm.Part, 0, 2)
	if strings.TrimSpace(prompt) != "" {
		parts = append(parts, llm.TextPart(prompt))
	}
	parts = append(parts, llm.ImagePart(raw, mime))
	for _, p := range provs {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		t, err := describePartsLegacy(ctx, []visionProvider{p}, "", parts)
		if err == nil && strings.TrimSpace(t) != "" {
			return strings.TrimSpace(t), nil
		}
		if err == nil {
			err = fmt.Errorf("пусто")
		}
		errs = append(errs, err.Error())
	}
	return "", fmt.Errorf("vision: %s", strings.Join(errs, "; "))
}

func loadSlideEntries(slidesDir string) ([]slideEntry, error) {
	ents, err := os.ReadDir(slidesDir)
	if err != nil {
		if os.IsNotExist(err) {
			logx.Debugf("notes", "DescribeSlides: dir %s not found, skip", slidesDir)
			return nil, nil
		}
		logx.Errorf("notes", "DescribeSlides: read dir %s: %v", slidesDir, err)
		return nil, fmt.Errorf("DescribeSlides: read dir %s: %w", slidesDir, err)
	}

	var imgFiles []string
	imgSet := make(map[string]bool)
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if isSlideImageName(name) {
			imgFiles = append(imgFiles, name)
			imgSet[name] = true
		}
	}
	sort.Strings(imgFiles)

	manifest := readSlideManifest(slidesDir)

	if len(imgFiles) == 0 {
		var entries []slideEntry
		for i, it := range manifest {
			txt := strings.TrimSpace(it.Text)
			if txt == "" {
				continue
			}
			num := it.Num
			if num <= 0 {
				num = i + 1
			}
			entries = append(entries, slideEntry{
				num:       num,
				text:      txt,
				timestamp: formatManifestTimestamp(it),
			})
		}
		sort.SliceStable(entries, func(i, j int) bool {
			return entries[i].num < entries[j].num
		})
		if len(entries) > maxVisionSlides {
			entries = entries[:maxVisionSlides]
		}
		return entries, nil
	}

	matchedByFile := make(map[string]slideManifestItem, len(manifest))
	var unmatchedManifest []slideManifestItem
	for _, it := range manifest {
		if it.File != "" {
			if imgSet[it.File] {
				if _, exists := matchedByFile[it.File]; !exists {
					matchedByFile[it.File] = it
				}
				continue
			}
			unmatchedManifest = append(unmatchedManifest, it)
			continue
		}
		unmatchedManifest = append(unmatchedManifest, it)
	}

	var extraTextEntries []slideEntry
	for _, it := range unmatchedManifest {
		if it.File == "" && it.Num >= 1 && it.Num <= len(imgFiles) {
			fname := imgFiles[it.Num-1]
			if _, exists := matchedByFile[fname]; !exists {
				matchedByFile[fname] = it
				continue
			}
		}
		if txt := strings.TrimSpace(it.Text); txt != "" && it.Num > 0 {
			extraTextEntries = append(extraTextEntries, slideEntry{
				num:       it.Num,
				text:      txt,
				timestamp: formatManifestTimestamp(it),
			})
		}
	}

	entries := make([]slideEntry, 0, len(imgFiles)+len(extraTextEntries))
	for i, name := range imgFiles {
		entry := slideEntry{
			num:      i + 1,
			filePath: filepath.Join(slidesDir, name),
			fileName: name,
		}
		if it, ok := matchedByFile[name]; ok {
			if it.Num > 0 {
				entry.num = it.Num
			}
			entry.text = strings.TrimSpace(it.Text)
			entry.timestamp = formatManifestTimestamp(it)
		}
		entries = append(entries, entry)
	}
	entries = append(entries, extraTextEntries...)

	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].num != entries[j].num {
			return entries[i].num < entries[j].num
		}
		return entries[i].fileName < entries[j].fileName
	})

	if len(entries) > maxVisionSlides {
		logx.Debugf("notes", "DescribeSlides: truncating slides %d -> %d", len(entries), maxVisionSlides)
		entries = entries[:maxVisionSlides]
	}
	return entries, nil
}

func readSlideManifest(slidesDir string) []slideManifestItem {
	path := filepath.Join(slidesDir, "slides.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			logx.Warnf("notes", "DescribeSlides: read %s: %v", path, err)
		}
		return nil
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	var rawItems []slideManifestItem
	if err := json.Unmarshal(data, &rawItems); err != nil {
		logx.Warnf("notes", "DescribeSlides: parse %s: %v", path, err)
		return nil
	}
	items := make([]slideManifestItem, 0, len(rawItems))
	for _, it := range rawItems {
		if it.File != "" && !isSafeSlideFilename(it.File) {
			logx.Warnf("notes", "DescribeSlides: пропущена небезопасная запись в slides.json: file=%q", it.File)
			continue
		}
		items = append(items, it)
	}
	return items
}

func isSafeSlideFilename(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if filepath.Base(name) != name || strings.Contains(name, "..") {
		return false
	}
	if strings.ContainsAny(name, `/\`) {
		return false
	}
	return true
}

func isSlideImageName(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".png") ||
		strings.HasSuffix(lower, ".jpg") ||
		strings.HasSuffix(lower, ".jpeg") ||
		strings.HasSuffix(lower, ".webp")
}

func detectImageMIME(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	default:
		return "image/png"
	}
}

func formatManifestTimestamp(it slideManifestItem) string {
	ts := strings.TrimSpace(it.Timestamp)
	if ts != "" {
		if strings.HasPrefix(ts, "[") && strings.HasSuffix(ts, "]") {
			return ts
		}
		return "[" + ts + "]"
	}
	if it.ShownOffsetS > 0 || (it.ShownOffsetS >= 0 && strings.TrimSpace(it.FirstShownAt) != "") {
		return fmt.Sprintf("[%02d:%02d]", it.ShownOffsetS/60, it.ShownOffsetS%60)
	}
	return ""
}

func buildBatchVisionPrompt(batch []visionSlideItem) string {
	var sb strings.Builder
	sb.WriteString("Опиши каждый слайд из этого батча отдельно. Для каждого слайда выпиши весь текст, ")
	sb.WriteString("все формулы строго в LaTeX ($...$ в строке, $$...$$ отдельной строкой) и объясни смысл схем и графиков. ")
	sb.WriteString("Пиши по-русски, без вступлений.\n")
	sb.WriteString("Начинай описание каждого слайда строго с отдельной строки-заголовка: === Слайд N ===\n\n")
	sb.WriteString("Слайды в этом запросе (в порядке приложенных изображений):\n")
	for _, it := range batch {
		if it.timestamp != "" {
			fmt.Fprintf(&sb, "- Слайд %d %s\n", it.num, it.timestamp)
		} else {
			fmt.Fprintf(&sb, "- Слайд %d\n", it.num)
		}
	}
	return sb.String()
}

func parseBatchSlideDescriptions(reply string, batchNums []int) map[int]string {
	out := make(map[int]string, len(batchNums))
	trimmedReply := strings.TrimSpace(reply)
	if trimmedReply == "" || len(batchNums) == 0 {
		return out
	}

	batchSet := make(map[int]bool, len(batchNums))
	for _, n := range batchNums {
		batchSet[n] = true
	}

	lines := strings.Split(strings.ReplaceAll(trimmedReply, "\r\n", "\n"), "\n")
	curNum := 0
	var buf []string

	flush := func() {
		if curNum == 0 {
			return
		}
		txt := strings.TrimSpace(strings.Join(buf, "\n"))
		if txt != "" {
			if prev, ok := out[curNum]; ok && prev != "" {
				out[curNum] = prev + "\n" + txt
			} else {
				out[curNum] = txt
			}
		}
		buf = nil
	}

	for _, line := range lines {
		if num, rest, ok := matchSlideHeader(line, batchSet); ok {
			flush()
			curNum = num
			if strings.TrimSpace(rest) != "" {
				buf = append(buf, strings.TrimSpace(rest))
			}
			continue
		}
		if curNum != 0 {
			buf = append(buf, line)
		}
	}
	flush()

	if len(out) == 0 && len(batchNums) == 1 {
		out[batchNums[0]] = trimmedReply
	}
	return out
}

func matchSlideHeader(line string, allowed map[int]bool) (int, string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return 0, "", false
	}
	for _, re := range []*regexp.Regexp{reStrictSlideHeader, reSoftSlideHeader} {
		m := re.FindStringSubmatch(trimmed)
		if len(m) < 2 {
			continue
		}
		num, err := strconv.Atoi(m[1])
		if err != nil || !allowed[num] {
			continue
		}
		var rest string
		if len(m) > 2 {
			rest = m[2]
		}
		return num, rest, true
	}
	return 0, "", false
}

func describePartsLegacy(ctx context.Context, provs []visionProvider, sysPrompt string, parts []llm.Part) (string, error) {
	content, err := llm.BuildContentParts(parts)
	if err != nil {
		return "", err
	}
	type legacyMsg struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	var msgs []legacyMsg
	if strings.TrimSpace(sysPrompt) != "" {
		sysJSON, err := json.Marshal(sysPrompt)
		if err != nil {
			return "", err
		}
		msgs = append(msgs, legacyMsg{Role: "system", Content: sysJSON})
	}
	msgs = append(msgs, legacyMsg{Role: "user", Content: content})

	var lastErr error
	for _, prov := range provs {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		payload, err := json.Marshal(struct {
			Model    string      `json:"model"`
			Messages []legacyMsg `json:"messages"`
		}{
			Model:    prov.model,
			Messages: msgs,
		})
		if err != nil {
			lastErr = err
			continue
		}
		txt, err := postVisionJSON(ctx, prov, payload)
		if err == nil && strings.TrimSpace(txt) != "" {
			return strings.TrimSpace(txt), nil
		}
		if err == nil {
			err = fmt.Errorf("%s: пустой ответ", prov.name)
		}
		lastErr = err
		logx.Warnf("notes", "vision: legacy provider=%s failed: %v", prov.name, err)
	}
	return "", lastErr
}

func postVisionJSON(ctx context.Context, prov visionProvider, payload []byte) (string, error) {
	endpoint := llm.ChatEndpoint(prov.base)
	logx.Debugf("notes", "vision: POST %s payload=%d bytes key_len=%d", endpoint, len(payload), len(prov.key))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		logx.Errorf("notes", "vision: new request %s: %v", endpoint, err)
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+prov.key)
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	cli := &http.Client{Timeout: 2 * time.Minute}
	res, err := cli.Do(req)
	if err != nil {
		logx.Errorf("notes", "vision: request %s failed after %s: %v", prov.name, time.Since(start), err)
		return "", err
	}
	defer res.Body.Close()
	logx.Debugf("notes", "vision: provider=%s HTTP %d in %s", prov.name, res.StatusCode, time.Since(start))
	b, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil {
		logx.Errorf("notes", "vision: read body provider=%s: %v", prov.name, err)
		return "", fmt.Errorf("%s vision: ответ оборвался: %w", prov.name, err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		logx.Errorf("notes", "vision: %s HTTP %d: %s", prov.name, res.StatusCode, truncate(string(b), 300))
		return "", fmt.Errorf("%s HTTP %d: %s", prov.name, res.StatusCode, truncate(string(b), 300))
	}
	var out chatResp
	if err := json.Unmarshal(b, &out); err != nil {
		logx.Errorf("notes", "vision: json unmarshal provider=%s (%d bytes): %v", prov.name, len(b), err)
		return "", err
	}
	if len(out.Choices) == 0 {
		logx.Errorf("notes", "vision: %s: пустой ответ", prov.name)
		return "", fmt.Errorf("%s: пустой ответ", prov.name)
	}
	text := strings.TrimSpace(out.Choices[0].Message.Content)
	logx.Debugf("notes", "vision: ok provider=%s chars=%d", prov.name, len(text))
	return text, nil
}
