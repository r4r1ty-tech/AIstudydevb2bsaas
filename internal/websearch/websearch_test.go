package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const liteHTML = `<html><body><table>
<tr><td><a rel="nofollow" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.org%2Ftcp&amp;rut=x" class='result-link'>TCP  handshake</a></td></tr>
<tr><td class='result-snippet'> Три <b>пакета</b>: SYN, SYN-ACK, ACK </td></tr>
<tr><td><a rel="nofollow" href="https://duckduckgo.com/y.js?ad=1" class='result-link'>Реклама</a></td></tr>
<tr><td class='result-snippet'>ad</td></tr>
<tr><td><a rel="nofollow" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2F2" class='result-link'>Второй</a></td></tr>
<tr><td class='result-snippet'>второй сниппет</td></tr>
</table></body></html>`

func TestParseDDGLite(t *testing.T) {
	hits, err := parseDDGLite(strings.NewReader(liteHTML), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("want 2 hits (без рекламы), got %d: %+v", len(hits), hits)
	}
	if hits[0].URL != "https://example.org/tcp" || hits[0].Title != "TCP handshake" || hits[0].Snippet != "Три пакета: SYN, SYN-ACK, ACK" {
		t.Fatalf("bad hit: %+v", hits[0])
	}
}

func TestParseDDGLiteCaptcha(t *testing.T) {
	_, err := parseDDGLite(strings.NewReader(`<form id="challenge-form"></form>`), 5)
	if err == nil {
		t.Fatal("want captcha error")
	}
}

func TestTavily(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" || r.URL.Path != "/search" {
			w.WriteHeader(401)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["query"] != "q" {
			w.WriteHeader(400)
			return
		}
		_, _ = w.Write([]byte(`{"results":[{"title":"T","url":"https://u","content":"C"}]}`))
	}))
	defer srv.Close()
	tv := &Tavily{Key: "k", HTTP: srv.Client(), Base: srv.URL}
	hits, err := tv.Search(context.Background(), "q", 3)
	if err != nil || len(hits) != 1 || hits[0].Snippet != "C" {
		t.Fatalf("hits=%+v err=%v", hits, err)
	}
}

// Tavily просит advanced + raw_content, а не только сниппеты, и разбирает
// полный текст страницы в Hit.Content. max_results держится в 5..10.
func TestTavilyAdvancedRawContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query             string `json:"query"`
			MaxResults        int    `json:"max_results"`
			SearchDepth       string `json:"search_depth"`
			IncludeRawContent bool   `json:"include_raw_content"`
			IncludeAnswer     *bool  `json:"include_answer"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("тело не JSON: %v", err)
			w.WriteHeader(400)
			return
		}
		if body.SearchDepth != "advanced" || !body.IncludeRawContent {
			t.Errorf("тело запроса: %+v, ждал search_depth=advanced и include_raw_content=true", body)
		}
		if body.MaxResults < 5 || body.MaxResults > 10 {
			t.Errorf("max_results=%d, ждал 5..10", body.MaxResults)
		}
		if body.IncludeAnswer != nil && *body.IncludeAnswer {
			t.Error("include_answer должен быть false")
		}
		_, _ = w.Write([]byte(`{"results":[{"title":"T","url":"https://u/t","content":"C","raw_content":"Полный текст страницы"}]}`))
	}))
	defer srv.Close()
	tv := &Tavily{Key: "k", HTTP: srv.Client(), Base: srv.URL}
	hits, err := tv.Search(context.Background(), "q", 3)
	if err != nil || len(hits) != 1 {
		t.Fatalf("hits=%+v err=%v", hits, err)
	}
	if hits[0].Snippet != "C" || hits[0].Content != "Полный текст страницы" {
		t.Fatalf("hit=%+v", hits[0])
	}
}

// Exa просит text до 2000 символов и кладёт его в Hit.Content.
func TestExaTextContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "k" {
			w.WriteHeader(401)
			return
		}
		var body struct {
			Contents struct {
				Text struct {
					MaxCharacters int `json:"maxCharacters"`
				} `json:"text"`
			} `json:"contents"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("тело не JSON: %v", err)
			w.WriteHeader(400)
			return
		}
		if body.Contents.Text.MaxCharacters != 2000 {
			t.Errorf("maxCharacters=%d, ждал 2000", body.Contents.Text.MaxCharacters)
		}
		_, _ = w.Write([]byte(`{"results":[{"title":"T","url":"https://u","text":"Текст страницы","highlights":["фрагмент"]}]}`))
	}))
	defer srv.Close()
	ex := &Exa{Key: "k", HTTP: srv.Client(), Base: srv.URL}
	hits, err := ex.Search(context.Background(), "q", 3)
	if err != nil || len(hits) != 1 {
		t.Fatalf("hits=%+v err=%v", hits, err)
	}
	if hits[0].Snippet != "фрагмент" || hits[0].Content != "Текст страницы" {
		t.Fatalf("hit=%+v", hits[0])
	}
}

type fake struct {
	name string
	hits []Hit
	err  error
	n    *int
}

func (f fake) Name() string { return f.name }
func (f fake) Search(context.Context, string, int) ([]Hit, error) {
	*f.n++
	return f.hits, f.err
}

func TestChainFallback(t *testing.T) {
	var a, b, c int
	s := &Searcher{Providers: []Provider{
		fake{name: "a", err: errors.New("down"), n: &a},
		fake{name: "b", n: &b}, // пусто
		fake{name: "c", hits: []Hit{{URL: "1"}, {URL: "2"}, {URL: "3"}}, n: &c},
	}}
	hits, who, err := s.Search(context.Background(), "q", 2)
	if err != nil || who != "c" || len(hits) != 2 || a != 1 || b != 1 || c != 1 {
		t.Fatalf("hits=%v who=%s err=%v calls=%d/%d/%d", hits, who, err, a, b, c)
	}
	_, _, err = (&Searcher{Providers: []Provider{fake{name: "a", err: errors.New("down"), n: &a}}}).Search(context.Background(), "q", 2)
	if err == nil || !strings.Contains(err.Error(), "a: down") {
		t.Fatalf("err=%v", err)
	}
}

func TestNewOrder(t *testing.T) {
	s := New(Options{TavilyKey: "t", SearXNGURL: "http://x/", DuckDuckGo: true})
	var names []string
	for _, p := range s.Providers {
		names = append(names, p.Name())
	}
	if strings.Join(names, ",") != "tavily,searxng,duckduckgo" {
		t.Fatalf("order %v", names)
	}
	if New(Options{}).Enabled() {
		t.Fatal("empty must be disabled")
	}
}
