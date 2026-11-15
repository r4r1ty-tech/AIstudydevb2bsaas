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
