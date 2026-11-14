package notes

import (
	"context"
	"fmt"
	"strings"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/llm"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

// chatResp — ответ OpenAI-совместимого chat/completions (нужен vision.go).
type chatResp struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func Summarize(ctx context.Context, cfg *config.Config, discipline string, n int, transcript, slides string) (string, error) {
	logx.Debugf("notes", "Summarize: start discipline=%q lecture=%d transcript_chars=%d slides_chars=%d cfg_nil=%t", discipline, n, len(transcript), len(slides), cfg == nil)
	if len(llm.DefaultChain(cfg)) == 0 {
		logx.Errorf("notes", "Summarize: нет ключей ИИ (GEMINI_API_KEYS / OPENCODE_API_KEY / LLM_*)")
		return "", fmt.Errorf("нет ключей ИИ")
	}
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

// Ask — общая бесплатная цепочка провайдеров (internal/llm): gemini, потом
// opencode; LLM_* (cheaperinference) — только если больше ничего не настроено.
func Ask(ctx context.Context, cfg *config.Config, sysPrompt, userPrompt string) (string, error) {
	res, err := llm.Ask(ctx, llm.DefaultChain(cfg), sysPrompt, userPrompt)
	if err != nil {
		return "", err
	}
	return res.Text, nil
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
