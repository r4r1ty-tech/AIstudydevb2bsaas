package mcpapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

// TestTTLCacheExpiry: запись живёт ровно TTL, затем Get её не отдаёт и удаляет.
func TestTTLCacheExpiry(t *testing.T) {
	c := NewTTLCache[string](time.Minute, 8)
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }

	if _, ok := c.Get("нет"); ok {
		t.Fatal("пустой кэш отдал значение")
	}
	c.Set("a", "1")
	if v, ok := c.Get("a"); !ok || v != "1" {
		t.Fatalf("Get(a) = %q, %v", v, ok)
	}

	now = now.Add(59 * time.Second)
	if _, ok := c.Get("a"); !ok {
		t.Fatal("запись исчезла раньше TTL")
	}

	now = now.Add(time.Second) // ровно истечение
	if _, ok := c.Get("a"); ok {
		t.Fatal("истёкшая запись отдана")
	}
	if c.Len() != 0 {
		t.Fatalf("просроченная запись не удалена: Len=%d", c.Len())
	}
}

// TestTTLCacheMaxEntries: при переполнении вытесняется самая старая запись.
func TestTTLCacheMaxEntries(t *testing.T) {
	c := NewTTLCache[int](time.Minute, 2)
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }

	c.Set("a", 1)
	now = now.Add(time.Second)
	c.Set("b", 2)
	now = now.Add(time.Second)
	c.Set("c", 3)

	if c.Len() != 2 {
		t.Fatalf("Len = %d, want 2", c.Len())
	}
	if _, ok := c.Get("a"); ok {
		t.Fatal("самая старая запись не вытеснена")
	}
	if _, ok := c.Get("b"); !ok {
		t.Fatal("b потеряна")
	}
	if v, ok := c.Get("c"); !ok || v != 3 {
		t.Fatalf("c = %d, %v", v, ok)
	}

	// Обновление существующего ключа не вытесняет соседей.
	c.Set("b", 20)
	if _, ok := c.Get("b"); !ok {
		t.Fatal("b исчезла после обновления")
	}
	if c.Len() != 2 {
		t.Fatalf("Len = %d, want 2", c.Len())
	}
}

// TestTTLCacheDeleteAndClear проверяет Delete и Clear.
func TestTTLCacheDeleteAndClear(t *testing.T) {
	c := NewTTLCache[int](time.Minute, 4)
	c.Set("a", 1)
	c.Set("b", 2)
	c.Delete("a")
	if _, ok := c.Get("a"); ok {
		t.Fatal("Delete не убрал ключ")
	}
	c.Clear()
	if c.Len() != 0 {
		t.Fatalf("Clear оставил %d записей", c.Len())
	}
}

// TestTTLCacheConcurrent: параллельные Set/Get/Delete не падают и не выходят
// за потолок записей (проверяется и под -race).
func TestTTLCacheConcurrent(t *testing.T) {
	c := NewTTLCache[int](time.Minute, 16)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				key := fmt.Sprintf("k%d", (i*31+j)%40)
				c.Set(key, j)
				_, _ = c.Get(key)
				if j%17 == 0 {
					c.Delete(key)
				}
			}
		}(i)
	}
	wg.Wait()
	if n := c.Len(); n > 16 {
		t.Fatalf("кэш вырос до %d записей при потолке 16", n)
	}
}

// TestLimiterClamps: допустимы 2..4, 0 — значение по умолчанию 3.
func TestLimiterClamps(t *testing.T) {
	cases := map[int]int{-5: 3, 0: 3, 1: 2, 2: 2, 3: 3, 4: 4, 5: 4, 100: 4}
	for in, want := range cases {
		if got := NewLimiter(in).Cap(); got != want {
			t.Errorf("NewLimiter(%d).Cap() = %d, want %d", in, got, want)
		}
	}
}

// TestLimiterAcquireRelease: одновременно в работе не больше Cap задач.
func TestLimiterAcquireRelease(t *testing.T) {
	l := NewLimiter(2)
	ctx := context.Background()
	var current, peak atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := l.Acquire(ctx); err != nil {
				t.Errorf("Acquire: %v", err)
				return
			}
			n := current.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			current.Add(-1)
			l.Release()
		}()
	}
	wg.Wait()
	if got := peak.Load(); got > 2 {
		t.Fatalf("одновременных захватов %d, потолок 2", got)
	}
	if got := peak.Load(); got != 2 {
		t.Fatalf("семафор не использовал оба слота: пик %d", got)
	}
}

// TestLimiterCanceledContext: отменённый контекст не занимает слот.
func TestLimiterCanceledContext(t *testing.T) {
	l := NewLimiter(3)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.Acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire = %v, want context.Canceled", err)
	}
	if l.Cap() != 3 {
		t.Fatalf("Cap = %d", l.Cap())
	}
	l.Release() // без Acquire не должен паниковать
}

// TestTruncateText100k: лимит считается в рунах, многобайтные символы не рвутся.
func TestTruncateText100k(t *testing.T) {
	long := strings.Repeat("Я", maxToolOutputRunes+1)
	got, truncated := TruncateText(long, maxToolOutputRunes)
	if !truncated {
		t.Fatal("усечение не отмечено")
	}
	if n := utf8.RuneCountInString(got); n != maxToolOutputRunes {
		t.Fatalf("рун после усечения = %d, want %d", n, maxToolOutputRunes)
	}
	if !utf8.ValidString(got) {
		t.Fatal("строка после усечения — битый UTF-8")
	}

	exact := strings.Repeat("я", maxToolOutputRunes)
	if got, truncated := TruncateText(exact, maxToolOutputRunes); truncated || got != exact {
		t.Fatalf("строка ровно в лимит усечена: truncated=%v", truncated)
	}

	if got, truncated := TruncateText("привет", 10); truncated || got != "привет" {
		t.Fatalf("короткая строка изменена: %q, %v", got, truncated)
	}
	if got, truncated := TruncateText("", 10); truncated || got != "" {
		t.Fatalf("пустая строка: %q, %v", got, truncated)
	}
	if got, truncated := TruncateText("abc", 0); !truncated || got != "" {
		t.Fatalf("нулевой лимит: %q, %v", got, truncated)
	}
	if got, truncated := TruncateText("a😀b", 2); !truncated || got != "a😀" {
		t.Fatalf("эмодзи порезан: %q, %v", got, truncated)
	}
	if got, truncated := TruncateText("a😀b", 3); truncated || got != "a😀b" {
		t.Fatalf("эмодзи в лимите: %q, %v", got, truncated)
	}
}

// TestToolCachesTTL проверяет, что набор кэшей инструментов создаётся с
// ожидаемыми TTL и потолком записей.
func TestToolCachesTTL(t *testing.T) {
	c := newToolCaches()
	cases := []struct {
		name string
		ttl  time.Duration
		max  int
	}{
		{"outline", c.outline.ttl, c.outline.max},
		{"material", c.material.ttl, c.material.max},
		{"search", c.search.ttl, c.search.max},
		{"deadlines", c.deadlines.ttl, c.deadlines.max},
	}
	want := map[string]time.Duration{
		"outline":   outlineCacheTTL,
		"material":  materialCacheTTL,
		"search":    outlineCacheTTL,
		"deadlines": deadlinesCacheTTL,
	}
	for _, tc := range cases {
		if tc.ttl != want[tc.name] {
			t.Errorf("%s TTL = %s, want %s", tc.name, tc.ttl, want[tc.name])
		}
		if tc.max != defaultCacheEntries {
			t.Errorf("%s max = %d, want %d", tc.name, tc.max, defaultCacheEntries)
		}
	}
	if outlineCacheTTL != 10*time.Minute || materialCacheTTL != 24*time.Hour || deadlinesCacheTTL != time.Minute {
		t.Fatal("TTL кэшей не совпадают с заданием")
	}
}
