// Package websearch — веб-поиск для решателя тестов: цепочка поставщиков,
// первый ответивший побеждает. Tavily / Exa / SearXNG включаются ключом или
// адресом, DuckDuckGo lite работает без ключа и стоит последним запасным.
package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// Hit — один результат поиска.
type Hit struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// Provider — один поисковик.
type Provider interface {
	Name() string
	Search(ctx context.Context, q string, n int) ([]Hit, error)
}

// Options — что включено. Пустые ключи/адреса — поставщик выключен.
type Options struct {
	TavilyKey  string
	ExaKey     string
	SearXNGURL string
	DuckDuckGo bool
	HTTP       *http.Client
}

// Searcher — цепочка поставщиков.
type Searcher struct {
	Providers []Provider
}

// New собирает цепочку: Tavily → Exa → SearXNG → DuckDuckGo.
func New(o Options) *Searcher {
	hc := o.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	s := &Searcher{}
	// TAVILY_API_KEY может содержать несколько ключей через запятую: каждый
	// становится отдельным поставщиком в цепочке. Когда лимит одного ключа
	// исчерпан, Search перейдёт к следующему ключу сам.
	for _, k := range strings.Split(o.TavilyKey, ",") {
		if k = strings.TrimSpace(k); k != "" {
			s.Providers = append(s.Providers, &Tavily{Key: k, HTTP: hc})
		}
	}
	if o.ExaKey != "" {
		s.Providers = append(s.Providers, &Exa{Key: o.ExaKey, HTTP: hc})
	}
	if o.SearXNGURL != "" {
		s.Providers = append(s.Providers, &SearXNG{Base: strings.TrimRight(o.SearXNGURL, "/"), HTTP: hc})
	}
	if o.DuckDuckGo {
		s.Providers = append(s.Providers, &DuckDuckGo{HTTP: hc})
	}
	return s
}

// Enabled — есть хоть один поставщик.
func (s *Searcher) Enabled() bool { return s != nil && len(s.Providers) > 0 }

// Search идёт по цепочке до первого непустого ответа. Возвращает имя
// ответившего поставщика.
func (s *Searcher) Search(ctx context.Context, q string, n int) ([]Hit, string, error) {
	if !s.Enabled() {
		return nil, "", errors.New("websearch: нет поставщиков")
	}
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, "", errors.New("websearch: пустой запрос")
	}
	var errs []string
	for _, p := range s.Providers {
		hits, err := p.Search(ctx, q, n)
		if err == nil && len(hits) > 0 {
			if len(hits) > n {
				hits = hits[:n]
			}
			return hits, p.Name(), nil
		}
		if err == nil {
			err = errors.New("пусто")
		}
		errs = append(errs, p.Name()+": "+err.Error())
		if ctx.Err() != nil {
			break
		}
	}
	return nil, "", fmt.Errorf("websearch: %s", strings.Join(errs, "; "))
}

func doJSON(ctx context.Context, hc *http.Client, method, u string, hdr map[string]string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return json.Unmarshal(data, out)
}

// Tavily — https://docs.tavily.com, бесплатно 1000 запросов в месяц.
type Tavily struct {
	Key  string
	HTTP *http.Client
	Base string // для тестов
}

func (t *Tavily) Name() string { return "tavily" }

func (t *Tavily) Search(ctx context.Context, q string, n int) ([]Hit, error) {
	base := t.Base
	if base == "" {
		base = "https://api.tavily.com"
	}
	var out struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	body := map[string]any{"query": q, "max_results": n, "search_depth": "basic"}
	if err := doJSON(ctx, t.HTTP, http.MethodPost, base+"/search",
		map[string]string{"Authorization": "Bearer " + t.Key}, body, &out); err != nil {
		return nil, err
	}
	hits := make([]Hit, 0, len(out.Results))
	for _, r := range out.Results {
		hits = append(hits, Hit{Title: r.Title, URL: r.URL, Snippet: r.Content})
	}
	return hits, nil
}

// Exa — https://docs.exa.ai.
type Exa struct {
	Key  string
	HTTP *http.Client
	Base string
}

func (e *Exa) Name() string { return "exa" }

func (e *Exa) Search(ctx context.Context, q string, n int) ([]Hit, error) {
	base := e.Base
	if base == "" {
		base = "https://api.exa.ai"
	}
	var out struct {
		Results []struct {
			Title      string   `json:"title"`
			URL        string   `json:"url"`
			Text       string   `json:"text"`
			Highlights []string `json:"highlights"`
		} `json:"results"`
	}
	body := map[string]any{
		"query": q, "numResults": n, "type": "auto",
		"contents": map[string]any{"highlights": map[string]any{"numSentences": 3}},
	}
	if err := doJSON(ctx, e.HTTP, http.MethodPost, base+"/search",
		map[string]string{"x-api-key": e.Key}, body, &out); err != nil {
		return nil, err
	}
	hits := make([]Hit, 0, len(out.Results))
	for _, r := range out.Results {
		sn := strings.Join(r.Highlights, " … ")
		if sn == "" {
			sn = r.Text
		}
		hits = append(hits, Hit{Title: r.Title, URL: r.URL, Snippet: sn})
	}
	return hits, nil
}

// SearXNG — свой экземпляр с включённым format=json.
type SearXNG struct {
	Base string
	HTTP *http.Client
}

func (s *SearXNG) Name() string { return "searxng" }

func (s *SearXNG) Search(ctx context.Context, q string, n int) ([]Hit, error) {
	var out struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	u := s.Base + "/search?" + url.Values{"q": {q}, "format": {"json"}, "language": {"ru"}}.Encode()
	if err := doJSON(ctx, s.HTTP, http.MethodGet, u, nil, nil, &out); err != nil {
		return nil, err
	}
	hits := make([]Hit, 0, len(out.Results))
	for _, r := range out.Results {
		hits = append(hits, Hit{Title: r.Title, URL: r.URL, Snippet: r.Content})
	}
	return hits, nil
}

// DuckDuckGo — lite-версия без ключа. Полная html.duckduckgo.com с серверов
// быстро отвечает капчей, lite живёт дольше, но тоже может отрезать.
type DuckDuckGo struct {
	HTTP *http.Client
	Base string
}

func (d *DuckDuckGo) Name() string { return "duckduckgo" }

func (d *DuckDuckGo) Search(ctx context.Context, q string, n int) ([]Hit, error) {
	// DDG с серверов быстро отдаёт капчу. Пробуем три точки входа и разные
	// заголовки: lite → html → основной html. Первая непустая выдача побеждает.
	type endpoint struct {
		base string
		path string
		html bool
	}
	var endpoints []endpoint
	if d.Base != "" {
		base := strings.TrimRight(d.Base, "/")
		if strings.Contains(base, "html.duckduckgo.com") {
			endpoints = []endpoint{{base: base, path: "/html/", html: true}}
		} else {
			endpoints = []endpoint{{base: base, path: "/lite/"}}
		}
	} else {
		endpoints = []endpoint{
			{base: "https://lite.duckduckgo.com", path: "/lite/"},
			{base: "https://html.duckduckgo.com", path: "/html/", html: true},
			{base: "https://duckduckgo.com", path: "/html/", html: true},
		}
	}
	var errs []string
	for _, ep := range endpoints {
		u := ep.base + ep.path + "?" + url.Values{"q": {q}, "kl": {"ru-ru"}}.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36")
		req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en;q=0.6")
		req.Header.Set("Accept", "text/html,application/xhtml+xml")
		req.Header.Set("Referer", "https://duckduckgo.com/")
		resp, err := d.HTTP.Do(req)
		if err != nil {
			errs = append(errs, ep.base+": "+err.Error())
			continue
		}
		if resp.StatusCode/100 != 2 {
			_ = resp.Body.Close()
			errs = append(errs, fmt.Sprintf("%s: status %d", ep.base, resp.StatusCode))
			continue
		}
		var hits []Hit
		if ep.html {
			hits, err = parseDDGHTML(io.LimitReader(resp.Body, 2<<20), n)
		} else {
			hits, err = parseDDGLite(io.LimitReader(resp.Body, 2<<20), n)
		}
		_ = resp.Body.Close()
		if err == nil && len(hits) > 0 {
			return hits, nil
		}
		if err == nil {
			err = errors.New("пусто")
		}
		errs = append(errs, ep.base+": "+err.Error())
		if ctx.Err() != nil {
			break
		}
	}
	return nil, fmt.Errorf("duckduckgo: %s", strings.Join(errs, "; "))
}

func parseDDGLite(r io.Reader, n int) ([]Hit, error) {
	doc, err := goquery.NewDocumentFromReader(r)
	if err != nil {
		return nil, err
	}
	if doc.Find("form#challenge-form, .anomaly-modal").Length() > 0 {
		return nil, errors.New("капча")
	}
	links := doc.Find("a.result-link")
	snips := doc.Find("td.result-snippet")
	var hits []Hit
	links.Each(func(i int, a *goquery.Selection) {
		if len(hits) >= n {
			return
		}
		h := Hit{Title: clean(a.Text()), URL: ddgTarget(a.AttrOr("href", ""))}
		if i < snips.Length() {
			h.Snippet = clean(snips.Eq(i).Text())
		}
		if h.URL != "" && !strings.Contains(h.URL, "duckduckgo.com/y.js") {
			hits = append(hits, h)
		}
	})
	return hits, nil
}

func parseDDGHTML(r io.Reader, n int) ([]Hit, error) {
	doc, err := goquery.NewDocumentFromReader(r)
	if err != nil {
		return nil, err
	}
	if doc.Find("form#challenge-form, .anomaly-modal, #challenge-form").Length() > 0 {
		return nil, errors.New("капча")
	}
	links := doc.Find("a.result__a")
	snips := doc.Find(".result__snippet")
	var hits []Hit
	links.Each(func(i int, a *goquery.Selection) {
		if len(hits) >= n {
			return
		}
		h := Hit{Title: clean(a.Text()), URL: ddgTarget(a.AttrOr("href", ""))}
		if i < snips.Length() {
			h.Snippet = clean(snips.Eq(i).Text())
		}
		if h.URL != "" && !strings.Contains(h.URL, "duckduckgo.com/y.js") {
			hits = append(hits, h)
		}
	})
	return hits, nil
}

// ddgTarget достаёт настоящий адрес из //duckduckgo.com/l/?uddg=...
func ddgTarget(href string) string {
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil {
		return href
	}
	if t := u.Query().Get("uddg"); t != "" {
		return t
	}
	return href
}

func clean(s string) string { return strings.Join(strings.Fields(s), " ") }
