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
	quizContentRunes    = 1200 // выдержка из полного текста страницы в промпте
	quizExactRunes      = 160  // длина дословной фразы вопроса
	quizExactMinRunes   = 40   // короче — фраза ни о чём, искать её бессмысленно
	quizMaxSearchesPage = 8    // поисков на страницу: дословные + ключевые слова
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

// quizCleanText — текст вопроса без служебных фраз Moodle и без раздела
// «Формат ответа»/«Пример»: он описывает форму ответа, а не суть задачи.
func quizCleanText(q moodle.QuizQuestion) string {
	t := quizNoise.ReplaceAllString(q.Text, " ")
	t = strings.Join(strings.Fields(t), " ")
	// Сам раздел модель получает целиком в промпте (formatSection).
	if loc := formatMarkerRe.FindStringIndex(t); loc != nil {
		t = strings.TrimSpace(t[:loc[0]])
	}
	return t
}

// quizQuery — текст вопроса, годный в поисковик: без служебных фраз Moodle,
// пробелы схлопнуты, обрезан до quizQueryRunes по границе слова.
func quizQuery(q moodle.QuizQuestion) string {
	t := quizCleanText(q)
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

// quizExactQuery — дословная фраза вопроса в кавычках: вопросы Moodle часто
// лежат в сети слово в слово (studfile, банки вопросов, методички), и точное
// совпадение фразы находит нужную страницу там, где ключевые слова тонут.
// Слишком короткая фраза бесполезна: вернётся что угодно.
func quizExactQuery(q moodle.QuizQuestion) string {
	t := strings.ReplaceAll(quizCleanText(q), `"`, " ")
	t = strings.Join(strings.Fields(t), " ")
	r := []rune(t)
	if len(r) > quizExactRunes {
		cut := string(r[:quizExactRunes])
		if i := strings.LastIndex(cut, " "); i > quizExactRunes/2 {
			cut = cut[:i]
		}
		t = strings.TrimSpace(cut)
	}
	if len([]rune(t)) < quizExactMinRunes {
		return ""
	}
	return `"` + t + `"`
}

// searchReq — один поисковый запрос по вопросу. Exact — дословная фраза.
type searchReq struct {
	Slot  int
	Query string
	Exact bool
}

// questionSearchReqs — до двух запросов на вопрос: сначала дословная фраза,
// потом ключевые слова (старый путь на случай, если точного текста в сети нет).
func questionSearchReqs(q moodle.QuizQuestion) []searchReq {
	var out []searchReq
	if x := quizExactQuery(q); x != "" {
		out = append(out, searchReq{Slot: q.Slot, Query: x, Exact: true})
	}
	if k := quizQuery(q); k != "" {
		out = append(out, searchReq{Slot: q.Slot, Query: k})
	}
	return out
}

// pageSearchReqs — запросы по странице: сначала дословные фразы всех вопросов,
// потом ключевые слова. При нехватке бюджета дословная фраза важнее: именно она
// находит страницу с тем же текстом вопроса. Один и тот же запрос не повторяем.
func pageSearchReqs(pc *pageCtx, qs []moodle.QuizQuestion) []searchReq {
	var exactReqs, keyReqs []searchReq
	for _, q := range qs {
		for _, req := range questionSearchReqs(q) {
			if pc.queried[req.Query] {
				continue
			}
			pc.queried[req.Query] = true
			if req.Exact {
				exactReqs = append(exactReqs, req)
			} else {
				keyReqs = append(keyReqs, req)
			}
		}
	}
	return append(exactReqs, keyReqs...)
}

// research ищет параллельно по запросам reqs, но не больше limit за вызов.
// Ошибки поиска не валят решение: вопрос просто останется без подсказок из сети.
// Находки по одному слоту склеиваются без повторов по URL.
func (s *Syncer) research(ctx context.Context, tag quizTag, ws *websearch.Searcher, reqs []searchReq, limit int) (map[int]webFinding, []quizlog.Search) {
	out := map[int]webFinding{}
	if ws == nil || len(reqs) == 0 {
		return out, nil
	}
	if limit <= 0 {
		logx.Debugf("uni", "quiz %s: лимит поисков на страницу (%d) исчерпан, пропуск %d запросов",
			tag, quizMaxSearchesPage, len(reqs))
		return out, nil
	}
	if len(reqs) > limit {
		logx.Debugf("uni", "quiz %s: поиск: запросов %d, лимит %d — лишние не ищу", tag, len(reqs), limit)
		reqs = reqs[:limit]
	}
	logx.Debugf("uni", "quiz %s: поиск, запросов %d", tag, len(reqs))
	var (
		mu   sync.Mutex
		logs []quizlog.Search
		wg   sync.WaitGroup
		sem  = make(chan struct{}, quizSearchWorkers)
	)
	for _, req := range reqs {
		wg.Add(1)
		go func(req searchReq) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			cctx, cancel := context.WithTimeout(ctx, quizSearchTimeout)
			defer cancel()
			hits, who, err := ws.Search(cctx, req.Query, quizSearchHits)
			l := quizlog.Search{Slot: req.Slot, Query: req.Query, Provider: who, Hits: len(hits), Exact: req.Exact}
			if err != nil {
				l.Error = err.Error()
				logx.Warnf("uni", "quiz %s: поиск slot=%d: %v", tag, req.Slot, err)
			}
			logSearch(tag, req, who, hits, err)
			mu.Lock()
			defer mu.Unlock()
			logs = append(logs, l)
			if len(hits) == 0 {
				return
			}
			prev, ok := out[req.Slot]
			if !ok {
				out[req.Slot] = webFinding{Query: req.Query, Hits: hits}
				return
			}
			prev.Hits = appendUniqueHits(prev.Hits, hits)
			prev.Query += " | " + req.Query
			out[req.Slot] = prev
		}(req)
	}
	wg.Wait()
	return out, logs
}

// appendUniqueHits добавляет находки, пропуская уже известные адреса.
func appendUniqueHits(dst, add []websearch.Hit) []websearch.Hit {
	seen := make(map[string]bool, len(dst)+len(add))
	for _, h := range dst {
		seen[h.URL] = true
	}
	for _, h := range add {
		if h.URL != "" && seen[h.URL] {
			continue
		}
		seen[h.URL] = true
		dst = append(dst, h)
	}
	return dst
}

// logSearch — подробный лог одного поискового запроса: поставщик, дословность,
// число результатов и каждый результат (заголовок, адрес, длина текста, сниппет).
func logSearch(tag quizTag, req searchReq, who string, hits []websearch.Hit, err error) {
	errText := ""
	if err != nil {
		errText = " ошибка=" + err.Error()
	}
	exact := 0
	if req.Exact {
		exact = 1
	}
	logx.Debugf("uni", "quiz %s: поиск slot=%d exact=%d запрос=%q поставщик=%s результатов=%d%s",
		tag, req.Slot, exact, req.Query, who, len(hits), errText)
	for i, h := range hits {
		logx.Debugf("uni", "quiz %s: поиск slot=%d результат #%d title=%q url=%s content=%d snippet=%q",
			tag, req.Slot, i+1, h.Title, h.URL, len([]rune(h.Content)), truncRunes(h.Snippet, quizLogSnippetRunes))
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
		old.Hits = appendUniqueHits(old.Hits, f.Hits)
		old.Query += " | " + f.Query
		dst[slot] = old
	}
}

// writeFindings — блок «что нашлось в интернете» для промпта. Если поставщик
// отдал текст страницы, показываем выдержку из него: нужный фрагмент вопроса
// обычно в тексте, а не в обрезанном сниппете.
func writeFindings(b *strings.Builder, f webFinding) {
	if len(f.Hits) == 0 {
		return
	}
	b.WriteString("Найдено в интернете (может не совпадать с вариантом теста, проверяй по смыслу):\n")
	for i, h := range f.Hits {
		fmt.Fprintf(b, "  %d) %s — %s\n", i+1, h.Title, h.URL)
		if c := strings.TrimSpace(h.Content); c != "" {
			fmt.Fprintf(b, "     [текст страницы, выдержка]\n     %s\n", truncRunes(c, quizContentRunes))
			continue
		}
		fmt.Fprintf(b, "     %s\n", truncRunes(h.Snippet, quizSnippetRunes))
	}
}
