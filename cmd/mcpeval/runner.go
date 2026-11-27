package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// runConfig — параметры одного прогона.
type runConfig struct {
	Transport   string
	Server      string
	Endpoint    string
	Bearer      string
	TasksPath   string
	OutPath     string
	StepTimeout time.Duration
	Verbose     bool
}

type transportInfo struct {
	Type    string `json:"type"`
	Command string `json:"command,omitempty"`
	URL     string `json:"url,omitempty"`
}

type summary struct {
	TotalTasks    int         `json:"total_tasks"`
	Passed        int         `json:"passed"`
	Failed        int         `json:"failed"`
	Steps         int         `json:"steps"`
	ToolCalls     int         `json:"tool_calls"`
	ResourceReads int         `json:"resource_reads"`
	PromptLists   int         `json:"prompt_lists"`
	ResourceLists int         `json:"resource_lists"`
	LatencyMS     latencyStat `json:"latency_ms"`
	OutputBytes   int64       `json:"output_bytes"`
	EstTokens     int64       `json:"est_tokens"`
	RunDurationMS float64     `json:"run_duration_ms"`
}

type latencyStat struct {
	P50   float64 `json:"p50"`
	P95   float64 `json:"p95"`
	Total float64 `json:"total"`
}

type invariant struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

type checkResult struct {
	Check check  `json:"check"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type stepResult struct {
	Kind        string        `json:"kind"`
	Target      string        `json:"target"`
	OK          bool          `json:"ok"`
	DurationMS  float64       `json:"duration_ms"`
	OutputBytes int64         `json:"output_bytes"`
	EstTokens   int64         `json:"est_tokens"`
	Error       string        `json:"error,omitempty"`
	Checks      []checkResult `json:"checks"`
}

type taskResult struct {
	ID         string       `json:"id"`
	Title      string       `json:"title"`
	OK         bool         `json:"ok"`
	DurationMS float64      `json:"duration_ms"`
	EstTokens  int64        `json:"est_tokens"`
	Error      string       `json:"error,omitempty"`
	Steps      []stepResult `json:"steps"`
}

type report struct {
	GeneratedAt  string        `json:"generated_at"`
	Transport    transportInfo `json:"transport"`
	Summary      summary       `json:"summary"`
	InvariantsOK bool          `json:"invariants_ok"`
	Invariants   []invariant   `json:"invariants"`
	Tasks        []taskResult  `json:"tasks"`
	Limitations  []string      `json:"limitations"`
}

// runner собирает состояние прогона: сессию и накопленные метрики.
type runner struct {
	cfg     runConfig
	session *mcp.ClientSession
	payload []float64
	bytes   int64
	calls   int
	tools   int
	resRead int
	prompt  int
	resList int
}

// execute подключается к серверу, проверяет инварианты и прогоняет задачи.
func execute(ctx context.Context, cfg runConfig, tf *taskFile) (*report, error) {
	start := time.Now()
	r := &runner{cfg: cfg}
	if err := r.connect(ctx); err != nil {
		return nil, err
	}
	defer r.close()

	inv, invOK := r.checkInvariants(ctx, tf)
	rep := &report{
		GeneratedAt:  time.Now().Format(time.RFC3339),
		Transport:    r.transportInfo(),
		InvariantsOK: invOK,
		Invariants:   inv,
		Limitations: []string{
			"HTTP-таймаут одной операции Moodle жёстко равен 40s (internal/moodle.New), MOODLE_MCP_TIMEOUT его не расширяет",
			"Все задачи read-only: тесты не запускаются, startattempt/submit не вызываются",
			"moodle_notes читает только локальные lecture_packs и RECORDINGS_DIR; без базы и каталога записей список пуст",
			"Живой прогон зависит от доступности do.ssau.ru и актуальности сохранённой сессии",
		},
		Tasks: make([]taskResult, 0, len(tf.Tasks)),
	}

	for i := range tf.Tasks {
		t := &tf.Tasks[i]
		fmt.Fprintf(os.Stderr, "[%d/%d] %s... ", i+1, len(tf.Tasks), t.ID)
		res := r.runTask(ctx, t)
		rep.Tasks = append(rep.Tasks, res)
		if res.OK {
			fmt.Fprintf(os.Stderr, "ok %.0fms\n", res.DurationMS)
		} else {
			fmt.Fprintf(os.Stderr, "FAIL: %s\n", res.Error)
		}
	}

	rep.Summary = r.summary(rep.Tasks, time.Since(start))
	return rep, nil
}

// connect поднимает stdio-процесс или HTTP-клиент.
func (r *runner) connect(ctx context.Context) error {
	client := mcp.NewClient(&mcp.Implementation{
		Name:    "ssau-mcpeval",
		Version: "0.1.0",
	}, nil)

	var transport mcp.Transport
	switch r.cfg.Transport {
	case "stdio":
		cmd := exec.Command(r.cfg.Server)
		cmd.Stderr = os.Stderr
		transport = &mcp.CommandTransport{Command: cmd}
	case "http":
		httpClient := &http.Client{Timeout: 0}
		if r.cfg.Bearer != "" {
			httpClient.Transport = &bearerTransport{
				token: r.cfg.Bearer,
				base:  http.DefaultTransport,
			}
		}
		transport = &mcp.StreamableClientTransport{
			Endpoint:             r.cfg.Endpoint,
			HTTPClient:           httpClient,
			DisableStandaloneSSE: true,
		}
	default:
		return fmt.Errorf("неизвестный транспорт %q", r.cfg.Transport)
	}
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return fmt.Errorf("mcpeval: подключиться к MCP: %w", err)
	}
	r.session = session
	return nil
}

func (r *runner) close() {
	if r.session != nil {
		_ = r.session.Close()
	}
}

func (r *runner) transportInfo() transportInfo {
	if r.cfg.Transport == "http" {
		return transportInfo{Type: "http", URL: r.cfg.Endpoint}
	}
	return transportInfo{Type: "stdio", Command: r.cfg.Server}
}

// checkInvariants проверяет, что сервер отдаёт обещанные инструменты,
// промпты и ресурсы. Ошибка инварианта — провал всего прогона.
func (r *runner) checkInvariants(ctx context.Context, tf *taskFile) ([]invariant, bool) {
	callCtx, cancel := context.WithTimeout(ctx, r.cfg.StepTimeout)
	defer cancel()

	var out []invariant
	ok := true
	add := func(name string, missing []string, total int) {
		item := invariant{Name: name, OK: len(missing) == 0}
		if item.OK {
			item.Detail = fmt.Sprintf("все %d на месте", total)
		} else {
			item.Detail = "нет: " + strings.Join(missing, ", ")
			ok = false
		}
		out = append(out, item)
	}

	if len(tf.RequiredTools) > 0 {
		res, err := r.session.ListTools(callCtx, nil)
		if err != nil {
			out = append(out, invariant{Name: "tools", Detail: err.Error()})
			ok = false
		} else {
			add("tools", missing(res.Tools, tf.RequiredTools, func(t *mcp.Tool) string { return t.Name }), len(tf.RequiredTools))
		}
	}
	if len(tf.RequiredPrompts) > 0 {
		res, err := r.session.ListPrompts(callCtx, nil)
		if err != nil {
			out = append(out, invariant{Name: "prompts", Detail: err.Error()})
			ok = false
		} else {
			add("prompts", missing(res.Prompts, tf.RequiredPrompts, func(p *mcp.Prompt) string { return p.Name }), len(tf.RequiredPrompts))
		}
	}
	if len(tf.RequiredRes) > 0 {
		res, err := r.session.ListResources(callCtx, nil)
		if err != nil {
			out = append(out, invariant{Name: "resources", Detail: err.Error()})
			ok = false
		} else {
			add("resources", missingResource(res.Resources, tf.RequiredRes), len(tf.RequiredRes))
		}
	}
	if len(tf.RequiredTpl) > 0 {
		res, err := r.session.ListResourceTemplates(callCtx, nil)
		if err != nil {
			out = append(out, invariant{Name: "resource_templates", Detail: err.Error()})
			ok = false
		} else {
			add("resource_templates", missingTemplate(res.ResourceTemplates, tf.RequiredTpl), len(tf.RequiredTpl))
		}
	}
	return out, ok
}

func missing[T any](items []*T, required []string, name func(*T) string) []string {
	have := make(map[string]bool, len(items))
	for _, it := range items {
		have[name(it)] = true
	}
	var out []string
	for _, want := range required {
		if !have[want] {
			out = append(out, want)
		}
	}
	return out
}

func missingResource(items []*mcp.Resource, required []string) []string {
	have := make(map[string]bool, len(items))
	for _, it := range items {
		have[it.URI] = true
		have[it.Name] = true
	}
	return absent(required, have)
}

func missingTemplate(items []*mcp.ResourceTemplate, required []string) []string {
	have := make(map[string]bool, len(items))
	for _, it := range items {
		have[it.URITemplate] = true
		have[it.Name] = true
	}
	return absent(required, have)
}

func absent(required []string, have map[string]bool) []string {
	var out []string
	for _, want := range required {
		if !have[want] {
			out = append(out, want)
		}
	}
	return out
}

// runTask выполняет шаги задачи. Таймаут задачи (если задан) применяется к
// каждому шагу: длинная Moodle-операция не должна съедать весь прогон.
func (r *runner) runTask(ctx context.Context, t *task) taskResult {
	start := time.Now()
	res := taskResult{ID: t.ID, Title: t.Title, OK: true, Steps: make([]stepResult, 0, len(t.Steps))}
	timeout := r.cfg.StepTimeout
	if t.TimeoutSeconds > 0 {
		timeout = time.Duration(t.TimeoutSeconds) * time.Second
	}
	for i := range t.Steps {
		s := &t.Steps[i]
		sr := r.runStep(ctx, s, timeout)
		res.Steps = append(res.Steps, sr)
		res.EstTokens += sr.EstTokens
		if !sr.OK {
			res.OK = false
			if res.Error == "" {
				res.Error = fmt.Sprintf("шаг %d (%s): %s", i+1, sr.Target, sr.Error)
			}
		}
	}
	res.DurationMS = ms(time.Since(start))
	return res
}

// runStep делает один MCP-вызов и прогоняет проверки.
func (r *runner) runStep(ctx context.Context, s *step, timeout time.Duration) stepResult {
	res := stepResult{Kind: s.Kind, OK: true, Checks: make([]checkResult, 0, len(s.Checks))}
	res.Target = s.Tool
	if res.Target == "" {
		res.Target = s.URI
	}
	if res.Target == "" {
		res.Target = s.Kind
	}

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	payload, size, err := r.call(callCtx, s)
	res.DurationMS = ms(time.Since(start))
	res.OutputBytes = size
	res.EstTokens = estTokens(size)
	r.bytes += size
	r.payload = append(r.payload, res.DurationMS)
	r.calls++

	if err != nil {
		res.OK = false
		res.Error = err.Error()
		return res
	}
	if r.cfg.Verbose {
		fmt.Fprintf(os.Stderr, "  %s %s: %.0fms, %d B\n", s.Kind, res.Target, res.DurationMS, size)
	}
	for i := range s.Checks {
		c := s.Checks[i]
		cr := checkResult{Check: c}
		if cerr := evalCheck(c, payload); cerr != nil {
			cr.Error = cerr.Error()
			res.OK = false
			if res.Error == "" {
				res.Error = cerr.Error()
			}
		} else {
			cr.OK = true
		}
		res.Checks = append(res.Checks, cr)
	}
	if !res.OK && res.Error == "" {
		res.Error = "проверка не пройдена"
	}
	return res
}

// call выполняет шаг и возвращает JSON-совместимый ответ и объём вывода.
func (r *runner) call(ctx context.Context, s *step) (any, int64, error) {
	switch s.Kind {
	case "tool":
		r.tools++
		result, err := r.session.CallTool(ctx, &mcp.CallToolParams{
			Name:      s.Tool,
			Arguments: s.Arguments,
		})
		if err != nil {
			return nil, 0, fmt.Errorf("вызов %s: %w", s.Tool, err)
		}
		size := toolResultBytes(result)
		if result.IsError {
			return nil, size, fmt.Errorf("%s: %s", s.Tool, contentText(result.Content))
		}
		return payloadFromTool(result), size, nil
	case "resource":
		r.resRead++
		result, err := r.session.ReadResource(ctx, &mcp.ReadResourceParams{URI: s.URI})
		if err != nil {
			return nil, 0, fmt.Errorf("ресурс %s: %w", s.URI, err)
		}
		return payloadFromResource(result)
	case "prompts":
		r.prompt++
		result, err := r.session.ListPrompts(ctx, nil)
		if err != nil {
			return nil, 0, fmt.Errorf("prompts/list: %w", err)
		}
		names := make([]any, 0, len(result.Prompts))
		for _, p := range result.Prompts {
			names = append(names, p.Name)
		}
		payload := map[string]any{"prompts": names, "names": names, "count": len(names)}
		return payload, jsonBytes(payload), nil
	case "resources":
		r.resList++
		result, err := r.session.ListResources(ctx, nil)
		if err != nil {
			return nil, 0, fmt.Errorf("resources/list: %w", err)
		}
		names := make([]any, 0, len(result.Resources))
		uris := make([]any, 0, len(result.Resources))
		for _, it := range result.Resources {
			names = append(names, it.Name)
			uris = append(uris, it.URI)
		}
		payload := map[string]any{"resources": names, "names": names, "uris": uris, "count": len(names)}
		return payload, jsonBytes(payload), nil
	case "resource_templates":
		r.resList++
		result, err := r.session.ListResourceTemplates(ctx, nil)
		if err != nil {
			return nil, 0, fmt.Errorf("resources/templates/list: %w", err)
		}
		names := make([]any, 0, len(result.ResourceTemplates))
		uris := make([]any, 0, len(result.ResourceTemplates))
		for _, it := range result.ResourceTemplates {
			names = append(names, it.Name)
			uris = append(uris, it.URITemplate)
		}
		payload := map[string]any{"templates": names, "names": names, "uris": uris, "count": len(names)}
		return payload, jsonBytes(payload), nil
	default:
		return nil, 0, fmt.Errorf("неизвестный kind %q", s.Kind)
	}
}

// payloadFromTool выбирает структурированный ответ; если его нет, пытается
// разобрать текстовый JSON, иначе заворачивает текст в поле text.
func payloadFromTool(result *mcp.CallToolResult) any {
	if result.StructuredContent != nil {
		if normalized, err := normalizeJSON(result.StructuredContent); err == nil {
			return normalized
		}
		return result.StructuredContent
	}
	text := contentText(result.Content)
	var parsed any
	if err := json.Unmarshal([]byte(text), &parsed); err == nil {
		return parsed
	}
	return map[string]any{"text": text}
}

// payloadFromResource отдаёт JSON-ресурсы как объект, остальные — как text.
func payloadFromResource(result *mcp.ReadResourceResult) (any, int64, error) {
	if len(result.Contents) == 0 {
		return nil, 0, errors.New("ресурс вернул пустой contents")
	}
	var size int64
	var texts []string
	var mime string
	for _, c := range result.Contents {
		size += int64(len(c.Text)) + int64(len(c.Blob))
		if c.Text != "" {
			texts = append(texts, c.Text)
		}
		if mime == "" {
			mime = c.MIMEType
		}
	}
	text := strings.Join(texts, "\n")
	if strings.Contains(mime, "json") {
		var parsed any
		if err := json.Unmarshal([]byte(text), &parsed); err == nil {
			return parsed, size, nil
		}
	}
	return map[string]any{"text": text, "mime_type": mime, "size": size, "count": len(result.Contents)}, size, nil
}

func toolResultBytes(result *mcp.CallToolResult) int64 {
	if result == nil {
		return 0
	}
	// StructuredContent и Content обычно дублируют один и тот же JSON:
	// считаем один раз, иначе оценка токенов завышается вдвое.
	if result.StructuredContent != nil {
		if raw, err := json.Marshal(result.StructuredContent); err == nil {
			return int64(len(raw))
		}
	}
	var size int64
	for _, c := range result.Content {
		switch t := c.(type) {
		case *mcp.TextContent:
			size += int64(len(t.Text))
		case *mcp.ImageContent:
			size += int64(len(t.Data))
		case *mcp.AudioContent:
			size += int64(len(t.Data))
		case *mcp.EmbeddedResource:
			if t.Resource != nil {
				size += int64(len(t.Resource.Text)) + int64(len(t.Resource.Blob))
			}
		}
	}
	return size
}

func contentText(content []mcp.Content) string {
	var b strings.Builder
	for _, c := range content {
		if t, ok := c.(*mcp.TextContent); ok {
			b.WriteString(t.Text)
			b.WriteByte('\n')
		}
	}
	return strings.TrimSpace(b.String())
}

func (r *runner) summary(tasks []taskResult, runDuration time.Duration) summary {
	s := summary{
		TotalTasks:    len(tasks),
		Steps:         r.calls,
		ToolCalls:     r.tools,
		ResourceReads: r.resRead,
		PromptLists:   r.prompt,
		ResourceLists: r.resList,
		OutputBytes:   r.bytes,
		EstTokens:     estTokens(r.bytes),
		LatencyMS:     latency(r.payload),
		RunDurationMS: ms(runDuration),
	}
	for _, t := range tasks {
		if t.OK {
			s.Passed++
		} else {
			s.Failed++
		}
	}
	return s
}

// latency считает p50/p95 по nearest-rank и суммарное время шагов.
func latency(values []float64) latencyStat {
	if len(values) == 0 {
		return latencyStat{}
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	var total float64
	for _, v := range sorted {
		total += v
	}
	return latencyStat{
		P50:   percentile(sorted, 50),
		P95:   percentile(sorted, 95),
		Total: round1(total),
	}
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int((p/100)*float64(len(sorted)) + 0.5)
	if idx < 1 {
		idx = 1
	}
	if idx > len(sorted) {
		idx = len(sorted)
	}
	return round1(sorted[idx-1])
}

// estTokens — грубая оценка токенов: 4 байта на токен.
func estTokens(size int64) int64 {
	if size <= 0 {
		return 0
	}
	return (size + 3) / 4
}

func jsonBytes(v any) int64 {
	raw, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return int64(len(raw))
}

func ms(d time.Duration) float64 {
	return round1(float64(d) / float64(time.Millisecond))
}

func round1(v float64) float64 {
	return float64(int64(v*10+0.5)) / 10
}

// bearerTransport добавляет Authorization к каждому HTTP-запросу клиента.
type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(clone)
}

// writeReport пишет JSON в файл (0600) или в stdout.
func writeReport(path string, rep *report) error {
	raw, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return fmt.Errorf("mcpeval: собрать отчёт: %w", err)
	}
	raw = append(raw, '\n')
	if strings.TrimSpace(path) == "" {
		_, err = os.Stdout.Write(raw)
		return err
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("mcpeval: записать отчёт %s: %w", path, err)
	}
	fmt.Fprintf(os.Stderr, "отчёт: %s\n", path)
	return nil
}
