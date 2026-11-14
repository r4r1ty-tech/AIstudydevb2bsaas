package websearch

import (
	"context"
	"os"
	"testing"
)

// WEBSEARCH_LIVE=1 go test -run Live ./internal/websearch -v — проверка живого DuckDuckGo.
func TestLiveDuckDuckGo(t *testing.T) {
	if os.Getenv("WEBSEARCH_LIVE") == "" {
		t.Skip("WEBSEARCH_LIVE не задан")
	}
	hits, who, err := New(Options{DuckDuckGo: true}).Search(context.Background(), "Какой уровень модели OSI отвечает за маршрутизацию пакетов", 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		t.Logf("[%s] %s — %s\n  %s", who, h.Title, h.URL, h.Snippet)
	}
}
