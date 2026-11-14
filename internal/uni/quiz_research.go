package uni

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodle"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/quizlog"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/websearch"
)

// Веб-поиск при решении теста. Два прохода:
//  1. по каждому вопросу ищем его текст как есть — вопросы Moodle часто
//     лежат в сети дословно (банки ответов, методички, конспекты);
//  2. если модель в ответе не уверена (confidence < quizUnsure) и предложила
//     свой запрос "search" — ищем его и переспрашиваем страницу ещё раз.
const (
	quizSearchHits      = 5
	quizSearchWorkers   = 4
	quizSearchTimeout   = 15 * time.Second
	quizQueryRunes      = 220
	quizSnippetRunes    = 400
	quizUnsure          = 0.6
	quizLogSnippetRunes = 200 // сниппет в подробном логе
)

// webSearcher — поиск из Syncer.Web или из конфига; nil — поиск выключен.
func (s *Syncer) webSearcher() *websearch.Searcher {
	if s.Web != nil {
		if s.Web.Enabled() {
			return s.Web
		}
		return nil
	}
	cfg := s.getCfg()
	if cfg == nil || !cfg.WebSearch {
		return nil
	}
	ws := websearch.New(websearch.Options{
		TavilyKey:  cfg.TavilyAPIKey,
		ExaKey:     cfg.ExaAPIKey,
		SearXNGURL: cfg.SearXNGURL,
		DuckDuckGo: cfg.WebSearchDDG,
	})
	if !ws.Enabled() {
		return nil
	}
	return ws
}

// webFinding — что нашли по одному вопросу.
type webFinding struct {
	Query string
	Hits  []websearch.Hit
}

var quizNoise = regexp.MustCompile(`(?i)(выберите (один|одн?у|несколько)[^:.]*[:.]?|ответ\s*:?\s*$|вопрос\s+\d+|пока нет ответа|балл\s*:\s*[\d,.]+|отметить вопрос)`)

// quizQuery — текст вопроса, годный в поисковик: без служебных фраз Moodle,
// пробелы схлопнуты, обрезан до quizQueryRunes по границе слова.
func quizQuery(q moodle.QuizQuestion) string {
	t := quizNoise.ReplaceAllString(q.Text, " ")
	t = strings.Join(strings.Fields(t), " ")
	r := []rune(t)
	if len(r) > quizQueryRunes {
		cut := string(r[:quizQueryRunes])
		if i := strings.LastIndex(cut, " "); i > quizQueryRunes/2 {
			cut = cut[:i]
		}
		t = cut
	}
	if len([]rune(t)) < 8 {
		return "" // «Ответ:», картинка без текста — искать нечего
	}
	return t
}

// research ищет параллельно по запросам slot→query. Ошибки поиска не валят
// решение: вопрос просто останется без подсказок из сети.
func (s *Syncer) research(ctx context.Context, tag quizTag, ws *websearch.Searcher, queries map[int]string) (map[int]webFinding, []quizlog.Search) {
	out := map[int]webFinding{}
	if ws == nil || len(queries) == 0 {
		return out, nil
	}
	logx.Debugf("uni", "quiz %s: поиск, запросов %d", tag, len(queries))
	var (
		mu   sync.Mutex
		logs []quizlog.Search
		wg   sync.WaitGroup
		sem  = make(chan struct{}, quizSearchWorkers)
	)
	for slot, q := range queries {
		if q == "" {
			continue
		}
		wg.Add(1)
		go func(slot int, q string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			cctx, cancel := context.WithTimeout(ctx, quizSearchTimeout)
			defer cancel()
			hits, who, err := ws.Search(cctx, q, quizSearchHits)
			l := quizlog.Search{Slot: slot, Query: q, Provider: who, Hits: len(hits)}
			if err != nil {
				l.Error = err.Error()
				logx.Warnf("uni", "quiz %s: поиск slot=%d: %v", tag, slot, err)
			}
			logSearch(tag, slot, q, who, hits, err)
			mu.Lock()
			defer mu.Unlock()
			logs = append(logs, l)
			if len(hits) > 0 {
				out[slot] = webFinding{Query: q, Hits: hits}
			}
		}(slot, q)
	}
	wg.Wait()
	return out, logs
}

// logSearch — подробный лог одного поискового запроса: поставщик, число
// результатов и каждый результат (заголовок, адрес, начало сниппета).
func logSearch(tag quizTag, slot int, q, who string, hits []websearch.Hit, err error) {
	errText := ""
	if err != nil {
		errText = " ошибка=" + err.Error()
	}
	logx.Debugf("uni", "quiz %s: поиск slot=%d запрос=%q поставщик=%s результатов=%d%s",
		tag, slot, q, who, len(hits), errText)
	for i, h := range hits {
		logx.Debugf("uni", "quiz %s: поиск slot=%d результат #%d title=%q url=%s snippet=%q",
			tag, slot, i+1, h.Title, h.URL, truncRunes(h.Snippet, quizLogSnippetRunes))
	}
}

// truncRunes — первые n рун строки (для логов).
func truncRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// mergeFindings добавляет новые находки к старым без повторов по URL.
func mergeFindings(dst, add map[int]webFinding) {
	for slot, f := range add {
		old, ok := dst[slot]
		if !ok {
			dst[slot] = f
			continue
		}
		seen := map[string]bool{}
		for _, h := range old.Hits {
			seen[h.URL] = true
		}
		for _, h := range f.Hits {
			if !seen[h.URL] {
				old.Hits = append(old.Hits, h)
			}
		}
		old.Query += " | " + f.Query
		dst[slot] = old
	}
}

// writeFindings — блок «что нашлось в интернете» для промпта.
func writeFindings(b *strings.Builder, f webFinding) {
	if len(f.Hits) == 0 {
		return
	}
	b.WriteString("Найдено в интернете (может не совпадать с вариантом теста, проверяй по смыслу):\n")
	for i, h := range f.Hits {
		sn := []rune(h.Snippet)
		if len(sn) > quizSnippetRunes {
			sn = append(sn[:quizSnippetRunes], '…')
		}
		fmt.Fprintf(b, "  %d) %s — %s\n     %s\n", i+1, h.Title, h.URL, string(sn))
	}
}
