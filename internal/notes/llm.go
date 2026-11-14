package notes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

type chatReq struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResp struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func Summarize(ctx context.Context, cfg *config.Config, discipline string, n int, transcript, slides string) (string, error) {
	logx.Debugf("notes", "Summarize: start discipline=%q lecture=%d transcript_chars=%d slides_chars=%d cfg_nil=%t", discipline, n, len(transcript), len(slides), cfg == nil)
	if cfg == nil || strings.TrimSpace(cfg.LLMAPIKey) == "" {
		logx.Errorf("notes", "Summarize: нет LLM_API_KEY")
		return "", fmt.Errorf("нет LLM_API_KEY")
	}
	if strings.TrimSpace(cfg.LLMAPIURL) == "" {
		logx.Errorf("notes", "Summarize: нет LLM_API_URL")
		return "", fmt.Errorf("нет LLM_API_URL")
	}
	model := strings.TrimSpace(cfg.LLMModel)
	if model == "" {
		logx.Errorf("notes", "Summarize: нет LLM_MODEL")
		return "", fmt.Errorf("нет LLM_MODEL")
	}
	logx.Debugf("notes", "Summarize: model=%s url=%s key_len=%d", model, cfg.LLMAPIURL, len(cfg.LLMAPIKey))
	sys := "Ты составляешь подробный конспект университетской лекции на русском, Markdown. " +
		"В САМОМ НАЧАЛЕ — отдельный блок «## Организационное»: что и к какому сроку сдавать, дедлайны, " +
		"требования по лабам/курсовым/контрольным, тесты, Moodle, форма отчётности, переносы пар — " +
		"всё, что преподаватель говорил про организацию. Перечисляй конкретные даты/сроки, предмет и форму сдачи. " +
		"Если про организацию в лекции не говорили — напиши «Организационное: в этой лекции не упоминалось» и не выдумывай. " +
		"Дальше — структура: тема, план, основные определения, разбор материала по ходу лекции, формулы и примеры, выводы, вопросы к зачёту. " +
		"Все формулы и математические выражения пиши только в LaTeX: внутри строки — $...$, отдельной строкой — $$...$$. " +
		"Не записывай формулы обычным текстом, юникод-символами (×, ², √, ∑) или в блоках кода; " +
		"то, что преподаватель продиктовал словами («икс в квадрате»), тоже переводи в LaTeX. " +
		"Опирайся на расшифровку аудио и на распознанные слайды. Если слайдов нет — только аудио. Не выдумывай факты, которых нет в материале."
	user := fmt.Sprintf("Предмет: %s\nЛекция №%d\n\n--- Слайды ---\n%s\n\n--- Расшифровка ---\n%s\n",
		discipline, n, emptyDash(slides), emptyDash(transcript))

	content, err := Ask(ctx, cfg, sys, user)
	if err != nil {
		logx.Errorf("notes", "Summarize: %v", err)
		return "", err
	}
	logx.Infof("notes", "Summarize: notes generated discipline=%q lecture=%d chars=%d", discipline, n, len(content))
	return content, nil
}

// Ask отправляет системный и пользовательский промпт к OpenAI-совместимому API модели.
func Ask(ctx context.Context, cfg *config.Config, sysPrompt, userPrompt string) (string, error) {
	if cfg == nil || strings.TrimSpace(cfg.LLMAPIKey) == "" {
		return "", fmt.Errorf("нет LLM_API_KEY")
	}
	if strings.TrimSpace(cfg.LLMAPIURL) == "" {
		return "", fmt.Errorf("нет LLM_API_URL")
	}
	model := strings.TrimSpace(cfg.LLMModel)
	if model == "" {
		return "", fmt.Errorf("нет LLM_MODEL")
	}

	payload, err := json.Marshal(chatReq{
		Model: model,
		Messages: []chatMessage{
			{Role: "system", Content: sysPrompt},
			{Role: "user", Content: userPrompt},
		},
	})
	if err != nil {
		return "", fmt.Errorf("marshal payload: %w", err)
	}
	endpoint := chatEndpoint(cfg.LLMAPIURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.LLMAPIKey)
	req.Header.Set("Content-Type", "application/json")

	cli := &http.Client{Timeout: 15 * time.Minute}
	res, err := cli.Do(req)
	if err != nil {
		return "", fmt.Errorf("llm request failed: %w", err)
	}
	defer res.Body.Close()

	b, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return "", fmt.Errorf("llm: ответ оборвался: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("llm HTTP %d: %s", res.StatusCode, truncate(string(b), 400))
	}
	var out chatResp
	if err := json.Unmarshal(b, &out); err != nil {
		return "", fmt.Errorf("llm unmarshal (%d bytes): %w", len(b), err)
	}
	if len(out.Choices) == 0 || strings.TrimSpace(out.Choices[0].Message.Content) == "" {
		return "", fmt.Errorf("llm: пустой ответ")
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}

// chatEndpoint принимает любой OpenAI-совместимый base URL: и с /v1, и без.
func chatEndpoint(base string) string {
	logx.Debugf("notes", "chatEndpoint: base=%q", base)
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		logx.Debugf("notes", "chatEndpoint: empty base")
		return ""
	}
	if strings.HasSuffix(base, "/v1") {
		logx.Debugf("notes", "chatEndpoint: v1 suffix -> %s/chat/completions", base)
		return base + "/chat/completions"
	}
	logx.Debugf("notes", "chatEndpoint: default -> %s/v1/chat/completions", base)
	return base + "/v1/chat/completions"
}

func emptyDash(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		logx.Debugf("notes", "emptyDash: empty -> (нет)")
		return "(нет)"
	}
	logx.Debugf("notes", "emptyDash: chars=%d", len(s))
	return s
}
