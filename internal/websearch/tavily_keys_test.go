package websearch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Несколько ключей через запятую: каждый — отдельный поставщик.
func TestNewSplitsTavilyKeys(t *testing.T) {
	s := New(Options{TavilyKey: " a, ,b "})
	if len(s.Providers) != 2 {
		t.Fatalf("провайдеров %d, ждали 2", len(s.Providers))
	}
	for i, want := range []string{"a", "b"} {
		tv, ok := s.Providers[i].(*Tavily)
		if !ok || tv.Key != want {
			t.Fatalf("провайдер %d: ключ %v, ждали %q", i, s.Providers[i], want)
		}
	}
}

// Первый ключ упёрся в лимит (ошибка) — поиск уходит на второй ключ.
func TestTavilyKeyRotationOnQuota(t *testing.T) {
	exhausted := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(432)
	}))
	defer exhausted.Close()
	working := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"title":"T","url":"https://ex/1","content":"c"}]}`))
	}))
	defer working.Close()

	hc := &http.Client{}
	s := &Searcher{Providers: []Provider{
		&Tavily{Key: "old", HTTP: hc, Base: exhausted.URL},
		&Tavily{Key: "new", HTTP: hc, Base: working.URL},
	}}
	hits, who, err := s.Search(context.Background(), "вопрос", 5)
	if err != nil || who != "tavily" || len(hits) != 1 {
		t.Fatalf("hits=%d who=%q err=%v", len(hits), who, err)
	}
}
