package notes

import (
	"context"
	"fmt"
	"strings"
	"time"

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
	if ctx == nil {
		ctx = context.Background()
	}
	li := lectureOf(ctx)
	tag := li.tag()
	started := time.Now()
	logx.Infof("notes", "Summarize: старт %s discipline=%q lecture=%d transcript_chars=%d slides_chars=%d", tag, discipline, n, len([]rune(transcript)), len([]rune(slides)))
	chain := llm.DefaultChain(cfg)
	if len(chain) == 0 {
		logx.Errorf("notes", "Summarize: %s нет ключей ИИ (GEMINI_API_KEYS / OPENCODE_API_KEY / LLM_*)", tag)
		return "", fmt.Errorf("нет ключей ИИ")
	}
	chainNames := make([]string, 0, len(chain))
	for _, t := range chain {
		chainNames = append(chainNames, t.Provider+"/"+t.Model)
	}
	logx.Debugf("notes", "Summarize: %s цепочка ИИ: %s", tag, strings.Join(chainNames, " -> "))
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

	if logx.Verbose() {
		logx.Dump("notes-llm", li.dumpName()+"-prompt", []byte("=== system ===\n"+sys+"\n\n=== user ===\n"+user))
	}
	res, err := llm.Ask(ctx, chain, sys, user)
	for i, a := range res.Attempts {
		if a.Err != "" {
			logx.Warnf("notes", "Summarize: %s попытка %d/%d %s/%s за %s: %s", tag, i+1, len(res.Attempts), a.Target.Provider, a.Target.Model, a.Took.Round(time.Millisecond), a.Err)
		} else {
			logx.Debugf("notes", "Summarize: %s попытка %d/%d %s/%s за %s ok", tag, i+1, len(res.Attempts), a.Target.Provider, a.Target.Model, a.Took.Round(time.Millisecond))
		}
	}
	if err != nil {
		logx.Errorf("notes", "Summarize: %s за %s: %v", tag, time.Since(started).Round(time.Second), err)
		return "", err
	}
	content := res.Text
	used := "?"
	if res.Used != nil {
		used = res.Used.Provider + "/" + res.Used.Model
	}
	if logx.Verbose() {
		logx.Dump("notes-llm", li.dumpName()+"-reply", []byte(content))
	}
	logx.Infof("notes", "Summarize: %s конспект готов модель=%s символов=%d за %s", tag, used, len([]rune(content)), time.Since(started).Round(time.Second))
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
