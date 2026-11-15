package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func TestParseRetryDelay(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		hdr  http.Header
		body string
		want time.Duration
	}{
		{
			name: "без заголовков и тела — дефолт",
			want: DefaultRateLimitCooldown,
		},
		{
			name: "Retry-After в секундах",
			hdr:  http.Header{"Retry-After": []string{"42"}},
			want: 42 * time.Second,
		},
		{
			name: "Retry-After HTTP-дата",
			hdr:  http.Header{"Retry-After": []string{now.Add(75 * time.Second).UTC().Format(http.TimeFormat)}},
			want: 75 * time.Second,
		},
		{
			name: "Retry-After прошедшая дата — fallback",
			hdr:  http.Header{"Retry-After": []string{now.Add(-10 * time.Second).UTC().Format(http.TimeFormat)}},
			want: DefaultRateLimitCooldown,
		},
		{
			name: "Gemini error.details retryDelay целые секунды",
			body: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"12s"}]}}`,
			want: 12 * time.Second,
		},
		{
			name: "Gemini дробные секунды в retryDelay",
			body: `{"error":{"details":[{"retryDelay":"12.5s"}]}}`,
			want: 12500 * time.Millisecond,
		},
		{
			name: "Gemini массив ошибок с retryDelay",
			body: `[{"error":{"code":429,"details":[{"retryDelay":"45s"}]}}]`,
			want: 45 * time.Second,
		},
		{
			name: "текстовый retry in без details",
			body: `{"error":{"message":"Quota exceeded. Please retry in 18.5s."}}`,
			want: 18500 * time.Millisecond,
		},
		{
			name: "максимум из Retry-After и retryDelay (побеждает Retry-After)",
			hdr:  http.Header{"Retry-After": []string{"60"}},
			body: `{"error":{"details":[{"retryDelay":"15s"}]}}`,
			want: 60 * time.Second,
		},
		{
			name: "максимум из Retry-After и retryDelay (побеждает retryDelay)",
			hdr:  http.Header{"Retry-After": []string{"10"}},
			body: `{"error":{"details":[{"retryDelay":"50s"}]}}`,
			want: 50 * time.Second,
		},
		{
			name: "ограничение сверху MaxRateLimitCooldown",
			hdr:  http.Header{"Retry-After": []string{"36000"}},
			body: `{"error":{"details":[{"retryDelay":"2h"}]}}`,
			want: MaxRateLimitCooldown,
		},
		{
			name: "protobuf Duration объект",
			body: `{"error":{"details":[{"retry_delay":{"seconds":20,"nanos":500000000}}]}}`,
			want: 20500 * time.Millisecond,
		},
		{
			name: "retryDelay с пробелом перед единицей измерения (ms)",
			body: `{"retryDelay": "500 ms"}`,
			want: 500 * time.Millisecond,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseRetryDelay(tc.hdr, []byte(tc.body), now)
			if got != tc.want {
				t.Fatalf("parseRetryDelay() = %v, ожидали %v", got, tc.want)
			}
		})
	}
}

func TestKeyPoolCooldownLifecycleAndSkip(t *testing.T) {
	pool := NewKeyPool()
	var mu sync.Mutex
	curNow := time.Date(2026, 11, 14, 15, 0, 0, 0, time.UTC)
	pool.SetNowFunc(func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return curNow
	})
	advance := func(d time.Duration) {
		mu.Lock()
		curNow = curNow.Add(d)
		mu.Unlock()
	}

	var k1Calls, k2Calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		switch auth {
		case "Bearer key-1":
			n := k1Calls.Add(1)
			if n == 1 {
				w.Header().Set("Retry-After", "20")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"details":[{"retryDelay":"40s"}]}}`))
				return
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ответ-1"}}]}`))
		case "Bearer key-2":
			k2Calls.Add(1)
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ответ-2"}}]}`))
		default:
			http.Error(w, "unknown key", http.StatusUnauthorized)
		}
	}))
	defer srv.Close()

	chain := []Target{
		{Provider: Gemini, Model: "m1", KeyIdx: 0, KeyLabel: "Первый", url: srv.URL, key: "key-1"},
		{Provider: Gemini, Model: "m2", KeyIdx: 0, KeyLabel: "Первый", url: srv.URL, key: "key-1"},
		{Provider: Gemini, Model: "m1", KeyIdx: 1, KeyLabel: "Второй", url: srv.URL, key: "key-2"},
	}

	// Вызов 1: key-1 (m1) получает 429 c retryDelay=40s, key-1 (m2) сразу пропускается,
	// отвечает key-2.
	res1, err := AskWithPool(context.Background(), pool, chain, "", "привет")
	if err != nil {
		t.Fatalf("первый AskWithPool: %v", err)
	}
	if res1.Text != "ответ-2" || res1.Used == nil || res1.Used.KeyLabel != "Второй" {
		t.Fatalf("ожидали ответ от key-2, получили: %+v", res1)
	}
	if k1Calls.Load() != 1 {
		t.Fatalf("вторая модель на том же ключе в cooldown не должна была вызываться: k1Calls=%d", k1Calls.Load())
	}
	if until := pool.CooldownUntil("key-1"); until.Sub(curNow) != 40*time.Second {
		t.Fatalf("cooldown key-1 = %v, ожидали 40s", until.Sub(curNow))
	}

	// Вызов 2 (через 10s, cooldown 40s ещё активен): сразу бьёт в key-2, не трогая key-1.
	advance(10 * time.Second)
	res2, err := Ask(WithPool(context.Background(), pool), chain, "", "повтор")
	if err != nil {
		t.Fatalf("второй Ask: %v", err)
	}
	if res2.Text != "ответ-2" {
		t.Fatalf("ожидали ответ-2, получили %q", res2.Text)
	}
	if k1Calls.Load() != 1 || k2Calls.Load() != 2 {
		t.Fatalf("ожидали k1Calls=1 и k2Calls=2, получили k1=%d k2=%d", k1Calls.Load(), k2Calls.Load())
	}
	if len(res2.Attempts) != 1 || res2.Attempts[0].Target.KeyLabel != "Второй" {
		t.Fatalf("в Attempts должна быть только попытка key-2: %+v", res2.Attempts)
	}

	// Вызов 3 (через ещё 31s -> итого 41s > 40s): cooldown истёк, key-1 снова работает.
	advance(31 * time.Second)
	if until := pool.CooldownUntil("key-1"); !until.IsZero() {
		t.Fatalf("ожидали истёкший cooldown, получили %v", until)
	}
	res3, err := AskWithPool(context.Background(), pool, chain, "", "проверка выхода")
	if err != nil {
		t.Fatalf("третий AskWithPool: %v", err)
	}
	if res3.Text != "ответ-1" || k1Calls.Load() != 2 || k2Calls.Load() != 2 {
		t.Fatalf("ожидали возврат на key-1: res=%+v k1=%d k2=%d", res3, k1Calls.Load(), k2Calls.Load())
	}
}

func TestKeyPoolAllCooldownReturnsErrorWithoutRequests(t *testing.T) {
	pool := NewKeyPool()
	now := time.Date(2026, 11, 14, 16, 0, 0, 0, time.UTC)
	pool.SetNowFunc(func() time.Time { return now })

	pool.SetCooldown("k1", now.Add(25*time.Second))
	pool.SetCooldown("k2", now.Add(10*time.Second))

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	chain := []Target{
		{Provider: Gemini, Model: "m1", KeyIdx: 0, KeyLabel: "K1", url: srv.URL, key: "k1"},
		{Provider: Gemini, Model: "m1", KeyIdx: 1, KeyLabel: "K2", url: srv.URL, key: "k2"},
	}

	_, err := AskWithPool(context.Background(), pool, chain, "", "тест")
	if err == nil {
		t.Fatal("ожидали ошибку при всех ключах в cooldown")
	}
	if !errors.Is(err, ErrAllCooldown) {
		t.Fatalf("ожидали ErrAllCooldown, получили: %v", err)
	}
	if !strings.Contains(err.Error(), "10s") {
		t.Fatalf("в ошибке должно быть ближайшее время до выхода из cooldown (10s), получили: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("сетевые запросы не должны выполняться, calls=%d", calls.Load())
	}
}

func TestKeyPoolSemaphoreLimitsConcurrencyToTwo(t *testing.T) {
	pool := NewKeyPool()

	var curInFlight atomic.Int32
	var maxInFlight atomic.Int32
	var totalCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := curInFlight.Add(1)
		for {
			prev := maxInFlight.Load()
			if cur <= prev || maxInFlight.CompareAndSwap(prev, cur) {
				break
			}
		}
		totalCalls.Add(1)
		time.Sleep(35 * time.Millisecond)
		curInFlight.Add(-1)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ок"}}]}`))
	}))
	defer srv.Close()

	chain := []Target{
		{Provider: Gemini, Model: "m1", KeyIdx: 0, KeyLabel: "Один", url: srv.URL, key: "same-secret"},
	}

	const parallel = 6
	var wg sync.WaitGroup
	errs := make(chan error, parallel)
	for i := 0; i < parallel; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := AskWithPool(context.Background(), pool, chain, "", "привет")
			if err != nil {
				errs <- err
				return
			}
			if res.Text != "ок" {
				errs <- errors.New("неожиданный текст: " + res.Text)
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("параллельный запрос упал: %v", err)
	}
	if totalCalls.Load() != parallel {
		t.Fatalf("вызовов %d, ожидали %d", totalCalls.Load(), parallel)
	}
	if maxInFlight.Load() > MaxConcurrentPerKey {
		t.Fatalf("превышен лимит одновременных запросов: maxInFlight=%d > %d", maxInFlight.Load(), MaxConcurrentPerKey)
	}
	if maxInFlight.Load() < 2 {
		t.Fatalf("ожидали реальную конкурентность 2, получили maxInFlight=%d", maxInFlight.Load())
	}
	if got := pool.InFlight("same-secret"); got != 0 {
		t.Fatalf("после завершения InFlight=%d, ожидали 0", got)
	}
}

func TestKeyPoolPrefersFreeKeyWhenFirstKeyBusyAndRespectsContext(t *testing.T) {
	pool := NewKeyPool()

	k1Entered := make(chan struct{}, 2)
	k1Release := make(chan struct{})
	var k2Calls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer k1":
			k1Entered <- struct{}{}
			<-k1Release
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"от-k1"}}]}`))
		case "Bearer k2":
			k2Calls.Add(1)
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"от-k2"}}]}`))
		}
	}))
	defer srv.Close()

	// Занимаем оба слота ключа k1.
	var wg sync.WaitGroup
	for i := 0; i < MaxConcurrentPerKey; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = AskWithPool(context.Background(), pool, []Target{
				{Provider: Gemini, Model: "m1", url: srv.URL, key: "k1"},
			}, "", "держим")
		}()
	}
	<-k1Entered
	<-k1Entered

	// Третий запрос с цепочкой [k1, k2] должен сразу уйти в свободный k2, не дожидаясь k1.
	res, err := AskWithPool(context.Background(), pool, []Target{
		{Provider: Gemini, Model: "m1", url: srv.URL, key: "k1"},
		{Provider: Gemini, Model: "m1", url: srv.URL, key: "k2"},
	}, "", "быстро")
	if err != nil {
		t.Fatalf("ожидали успех через свободный k2: %v", err)
	}
	if res.Text != "от-k2" || k2Calls.Load() != 1 {
		t.Fatalf("ожидали от-k2, получили %+v (k2Calls=%d)", res, k2Calls.Load())
	}

	// Запрос только с занятым k1 и коротким таймаутом должен отвалиться по ctx.Done().
	shortCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = AskWithPool(shortCtx, pool, []Target{
		{Provider: Gemini, Model: "m1", url: srv.URL, key: "k1"},
	}, "", "таймаут")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ожидали context.DeadlineExceeded, получили: %v", err)
	}

	close(k1Release)
	wg.Wait()
	if got := pool.InFlight("k1"); got != 0 {
		t.Fatalf("после освобождения InFlight(k1)=%d, ожидали 0", got)
	}
}

func TestShortUTF8Cyrillic(t *testing.T) {
	src := "  Привет, кириллица и длинная ошибка от сервера!  "
	got := short(src, 10)
	if !utf8.ValidString(got) {
		t.Fatalf("невалидный UTF-8 после short: %q", got)
	}
	if got != "Привет, ки…" {
		t.Fatalf("short() = %q, ожидали %q", got, "Привет, ки…")
	}
	if same := short("Коротко", 20); same != "Коротко" {
		t.Fatalf("short() не должен обрезать короткую строку: %q", same)
	}
	if empty := short("Текст", 0); empty != "" {
		t.Fatalf("short(_, 0) = %q, ожидали пустую строку", empty)
	}
}

func TestAskChainScrubsSecretAndPoolSnapshot(t *testing.T) {
	pool := NewKeyPool()
	now := time.Date(2026, 11, 14, 18, 0, 0, 0, time.UTC)
	pool.SetNowFunc(func() time.Time { return now })

	chain := []Target{
		{Provider: Gemini, Model: "m1", KeyIdx: 0, KeyLabel: "Тайный", key: "super-secret-xyz"},
	}

	res, err := askChain(context.Background(), pool, chain, func(ctx context.Context, tgt Target) (string, int, http.Header, []byte, error) {
		return "", http.StatusTooManyRequests, http.Header{"Retry-After": []string{"15"}}, []byte("bad super-secret-xyz"), errors.New("fail super-secret-xyz")
	})
	if err == nil {
		t.Fatal("ожидали ошибку")
	}
	if strings.Contains(err.Error(), "super-secret-xyz") {
		t.Fatalf("секрет утёк в ошибку: %v", err)
	}
	if len(res.Attempts) != 1 || strings.Contains(res.Attempts[0].Err, "super-secret-xyz") {
		t.Fatalf("секрет утёк в Attempt: %+v", res.Attempts)
	}

	snap := pool.Snapshot([]string{"super-secret-xyz", "fresh-key"})
	if snap["super-secret-xyz"].Remaining != 15*time.Second {
		t.Fatalf("снимок супер-ключа: %+v", snap["super-secret-xyz"])
	}
	if snap["fresh-key"].Remaining != 0 || snap["fresh-key"].InFlight != 0 {
		t.Fatalf("снимок свободного ключа: %+v", snap["fresh-key"])
	}
}

func TestWaiterSkipsRequestIfKeyEntersCooldownWhileWaiting(t *testing.T) {
	pool := NewKeyPool()

	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var calls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n <= 2 {
			entered <- struct{}{}
			<-release
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate limit"}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"не должно вызываться"}}]}`))
	}))
	defer srv.Close()

	chain := []Target{
		{Provider: Gemini, Model: "m1", url: srv.URL, key: "wait-key"},
	}

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = AskWithPool(context.Background(), pool, chain, "", "holder")
		}()
	}
	<-entered
	<-entered

	waiterDone := make(chan error, 1)
	go func() {
		_, err := AskWithPool(context.Background(), pool, chain, "", "waiter")
		waiterDone <- err
	}()

	// Даём горутине waiter дойти до ожидания семафора.
	time.Sleep(25 * time.Millisecond)
	close(release)
	wg.Wait()

	err := <-waiterDone
	if !errors.Is(err, ErrAllCooldown) {
		t.Fatalf("ожидали ErrAllCooldown для waiter после 429 держателей, получили: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("waiter не должен был делать 3-й HTTP-запрос, calls=%d", calls.Load())
	}
}

func TestEmptySecretHasNoConcurrencyLimitAndGlobalHelpers(t *testing.T) {
	pool := NewKeyPool()

	var cur, max atomic.Int32
	allIn := make(chan struct{})
	var ready atomic.Int32

	chain := []Target{{Provider: "local", Model: "m1", key: ""}}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := askChain(context.Background(), pool, chain, func(ctx context.Context, tgt Target) (string, int, http.Header, []byte, error) {
				c := cur.Add(1)
				for {
					p := max.Load()
					if c <= p || max.CompareAndSwap(p, c) {
						break
					}
				}
				if ready.Add(1) == 4 {
					close(allIn)
				}
				<-allIn
				cur.Add(-1)
				return "ok", http.StatusOK, nil, nil, nil
			})
			if err != nil {
				t.Errorf("пустой ключ упал: %v", err)
			}
		}()
	}
	wg.Wait()
	if max.Load() != 4 {
		t.Fatalf("пустой секрет не должен ограничиваться семафором, max=%d", max.Load())
	}

	// Проверяем глобальные обёртки над defaultPool с изоляцией уникальным ключом и восстановлением часов.
	t.Cleanup(func() { SetPoolNowFunc(nil) })
	now := time.Date(2026, 11, 14, 19, 0, 0, 0, time.UTC)
	SetPoolNowFunc(func() time.Time { return now })

	uniqueSecret := "global-helper-test-secret"
	DefaultPool().SetCooldown(uniqueSecret, now.Add(20*time.Second))

	until, ok := KeyCooldownUntil(uniqueSecret)
	if !ok || !until.Equal(now.Add(20*time.Second)) {
		t.Fatalf("KeyCooldownUntil = (%v, %v), ожидали +20s", until, ok)
	}
	if inf := KeyInFlight(uniqueSecret); inf != 0 {
		t.Fatalf("KeyInFlight = %d, ожидали 0", inf)
	}
	snap := PoolSnapshot([]string{uniqueSecret})
	if snap[uniqueSecret].Remaining != 20*time.Second {
		t.Fatalf("PoolSnapshot = %+v", snap)
	}
}
