package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Reporter struct {
	APIKey     string
	BaseURL    string
	Model      string
	HTTPClient *http.Client
}

type Report struct {
	Discipline          string
	Summary             string
	KeyTerms            []string
	HasTestAlert        bool
	TasksAndExams       []string
	QuestionsAndAnswers []string
	FormattedText       string
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func NewReporter(apiKey, baseURL, model string) *Reporter {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	if model == "" {
		model = "gpt-4o-mini"
	}
	return &Reporter{
		APIKey:  apiKey,
		BaseURL: strings.TrimRight(baseURL, "/"),
		Model:   model,
		HTTPClient: &http.Client{
			Timeout: 3 * time.Minute,
		},
	}
}

// GenerateReport creates a structured Markdown report from lecture transcript.
func (r *Reporter) GenerateReport(ctx context.Context, discipline, transcript string) (*Report, error) {
	if transcript == "" {
		return nil, fmt.Errorf("транскрипт пустой")
	}

	// Simple heuristic for test presence
	low := strings.ToLower(transcript)
	hasTest := strings.Contains(low, "тест") || strings.Contains(low, "контрольн") || strings.Contains(low, "мудл") || strings.Contains(low, "moodle")

	// If no API key, generate structured fallback report from transcript text
	if r.APIKey == "" {
		return r.generateFallbackReport(discipline, transcript, hasTest), nil
	}

	prompt := fmt.Sprintf(`Ты помощник студента. Сделай структурированный конспект лекции по предмету "%s" на основе следующей расшифровки:

Транскрипт:
%s

Сформируй ответ в красивом Markdown со следующими разделами:
1. 📌 **Тема и краткое содержание (TL;DR)**
2. 🔑 **Ключевые термины и определения**
3. ⚠️ **Тесты, контрольные и домашнее задание** (если говорилось про тест/moodle/дз)
4. ❓ **Вопросы студентов и ответы преподавателя**
`, discipline, ChunkText(transcript, 12000))

	reqBody := chatCompletionRequest{
		Model: r.Model,
		Messages: []chatMessage{
			{Role: "system", Content: "Ты отличник-студент, который пишет понятные и структурированные конспекты лекций."},
			{Role: "user", Content: prompt},
		},
		Temperature: 0.3,
	}

	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := r.BaseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+r.APIKey)
	req.Header.Set("Content-Type", "application/json")

	hc := r.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}

	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llm request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read llm response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("llm status %d: %s", resp.StatusCode, string(respBytes))
	}

	var chatResp chatCompletionResponse
	if err := json.Unmarshal(respBytes, &chatResp); err != nil {
		return nil, fmt.Errorf("unmarshal llm response: %w", err)
	}

	if chatResp.Error != nil {
		return nil, fmt.Errorf("llm api error: %s", chatResp.Error.Message)
	}

	if len(chatResp.Choices) == 0 {
		return nil, fmt.Errorf("empty choices from llm")
	}

	summaryText := strings.TrimSpace(chatResp.Choices[0].Message.Content)

	report := &Report{
		Discipline:    discipline,
		Summary:       summaryText,
		HasTestAlert:  hasTest,
		FormattedText: summaryText,
	}

	return report, nil
}

func (r *Reporter) generateFallbackReport(discipline, transcript string, hasTest bool) *Report {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📖 **Конспект лекции: %s**\n\n", discipline))
	if hasTest {
		sb.WriteString("⚠️ **ВНИМАНИЕ: На лекции упомянут тест / Moodle / контрольная!**\n\n")
	}
	sb.WriteString("📌 **Краткое содержание:**\n")
	lines := strings.Split(transcript, "\n")
	for i, l := range lines {
		if i >= 5 {
			break
		}
		if strings.TrimSpace(l) != "" {
			sb.WriteString("• " + strings.TrimSpace(l) + "\n")
		}
	}
	sb.WriteString("\n📄 *Примечание: Задайте LLM_API_KEY для детальной нейросетевой обработки.*")

	return &Report{
		Discipline:    discipline,
		Summary:       sb.String(),
		HasTestAlert:  hasTest,
		FormattedText: sb.String(),
	}
}

// ChunkText limits text length to maxChars.
func ChunkText(text string, maxChars int) string {
	if len(text) <= maxChars {
		return text
	}
	return text[:maxChars] + "\n...[текст сокращен]"
}
