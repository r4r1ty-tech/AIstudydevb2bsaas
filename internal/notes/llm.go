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
	sys := "Ты составляешь подробный конспект университетской лекции на русском, Markdown. " +
		"В САМОМ НАЧАЛЕ — отдельный блок «## Организационное»: что и к какому сроку сдавать, дедлайны, " +
		"требования по лабам/курсовым/контрольным, тесты, Moodle, форма отчётности, переносы пар — " +
		"всё, что преподаватель говорил про организацию. Перечисляй конкретные даты/сроки, предмет и форму сдачи. " +
		"Если про организацию в лекции не говорили — напиши «Организационное: в этой лекции не упоминалось» и не выдумывай. " +
		"Дальше — структура: тема, план, основные определения, разбор материала по ходу лекции, формулы и примеры, выводы, вопросы к зачёту. " +
		"Опирайся на расшифровку аудио и на распознанные слайды. Если слайдов нет — только аудио. Не выдумывай факты, которых нет в материале."
	user := fmt.Sprintf("Предмет: %s\nЛекция №%d\n\n--- Слайды ---\n%s\n\n--- Расшифровка ---\n%s\n",
		discipline, n, emptyDash(slides), emptyDash(transcript))

	payload, err := json.Marshal(chatReq{
		Model: model,
		Messages: []chatMessage{
			{Role: "system", Content: sys},
			{Role: "user", Content: user},
		},
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatEndpoint(cfg.LLMAPIURL), bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.LLMAPIKey)
	req.Header.Set("Content-Type", "application/json")

	cli := &http.Client{Timeout: 4 * time.Minute}
	res, err := cli.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("llm HTTP %d: %s", res.StatusCode, truncate(string(b), 400))
	}
	var out chatResp
	if err := json.Unmarshal(b, &out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 || strings.TrimSpace(out.Choices[0].Message.Content) == "" {
		return "", fmt.Errorf("llm: пустой ответ")
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}

// chatEndpoint принимает любой OpenAI-совместимый base URL: и с /v1, и без.
func chatEndpoint(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return ""
	}
	if strings.HasSuffix(base, "/v1") {
		return base + "/chat/completions"
	}
	return base + "/v1/chat/completions"
}

func emptyDash(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "(нет)"
	}
	return s
}
