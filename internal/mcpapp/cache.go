package mcpapp

import (
	"context"
	"sync"
	"time"
)

const (
	// outlineCacheTTL — структура курса меняется редко, но и ждать сутки нельзя.
	outlineCacheTTL = 10 * time.Minute
	// materialCacheTTL — текст материала почти статичен.
	materialCacheTTL = 24 * time.Hour
	// deadlinesCacheTTL — сроки должны быть свежими, поэтому минута.
	deadlinesCacheTTL = time.Minute

	// defaultCacheEntries — сколько ключей помнит один кэш.
	defaultCacheEntries = 128

	// defaultToolConcurrency — сколько запросов к Moodle идёт параллельно.
	defaultToolConcurrency = 3
	minToolConcurrency     = 2
	maxToolConcurrency     = 4
)

// ttlEntry — значение и момент, после которого оно недействительно.
type ttlEntry[T any] struct {
	value   T
	expires time.Time
}

// TTLCache — потокобезопасный кэш с единым TTL и потолком числа записей.
// Просроченные записи удаляются при чтении и при вставке, поэтому память
// ограничена maxEntries и кэш не растёт бесконечно.
type TTLCache[T any] struct {
	ttl   time.Duration
	max   int
	now   func() time.Time
	mu    sync.Mutex
	items map[string]ttlEntry[T]
}

// NewTTLCache создаёт кэш с TTL ttl. maxEntries <= 0 заменяется значением
// по умолчанию; ttl <= 0 — одной минутой, чтобы запись не жила вечно.
func NewTTLCache[T any](ttl time.Duration, maxEntries int) *TTLCache[T] {
	if ttl <= 0 {
		ttl = time.Minute
	}
	if maxEntries <= 0 {
		maxEntries = defaultCacheEntries
	}
	return &TTLCache[T]{
		ttl:   ttl,
		max:   maxEntries,
		now:   time.Now,
		items: make(map[string]ttlEntry[T]),
	}
}

// Get возвращает значение, если ключ есть и TTL не истёк. Просроченная запись
// при этом удаляется.
func (c *TTLCache[T]) Get(key string) (T, bool) {
	var zero T
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[key]
	if !ok {
		return zero, false
	}
	if !c.now().Before(e.expires) {
		delete(c.items, key)
		return zero, false
	}
	return e.value, true
}

// Set кладёт значение и продлевает TTL. Если записей уже максимум, сначала
// выбрасываются просроченные, затем самая старая по сроку истечения.
func (c *TTLCache[T]) Set(key string, v T) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	c.evictExpiredLocked(now)
	if _, exists := c.items[key]; !exists && len(c.items) >= c.max {
		c.evictOldestLocked()
	}
	c.items[key] = ttlEntry[T]{value: v, expires: now.Add(c.ttl)}
}

// Delete убирает ключ, не дожидаясь истечения TTL.
func (c *TTLCache[T]) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, key)
}

// Len — сколько записей сейчас в кэше, включая ещё не выброшенные просроченные.
func (c *TTLCache[T]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// Clear очищает кэш целиком.
func (c *TTLCache[T]) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	clear(c.items)
}

// evictExpiredLocked удаляет записи с истёкшим TTL. Вызывается под c.mu.
func (c *TTLCache[T]) evictExpiredLocked(now time.Time) {
	for k, e := range c.items {
		if !now.Before(e.expires) {
			delete(c.items, k)
		}
	}
}

// evictOldestLocked удаляет запись, которая истечёт раньше всех. Вызывается
// под c.mu и только когда кэш непуст.
func (c *TTLCache[T]) evictOldestLocked() {
	oldestKey := ""
	var oldest time.Time
	first := true
	for k, e := range c.items {
		if first || e.expires.Before(oldest) {
			oldestKey, oldest, first = k, e.expires, false
		}
	}
	if oldestKey != "" {
		delete(c.items, oldestKey)
	}
}

// Limiter — семафор на N одновременных запросов к Moodle. Нужен, чтобы
// параллельное чтение материалов не превращалось в шквал запросов.
type Limiter struct {
	slots chan struct{}
}

// NewLimiter создаёт семафор. Допустимый диапазон — 2..4; 0 и отрицательные
// значения дают значение по умолчанию 3, остальные вне диапазона
// подтягиваются к ближайшей границе.
func NewLimiter(n int) *Limiter {
	switch {
	case n <= 0:
		n = defaultToolConcurrency
	case n < minToolConcurrency:
		n = minToolConcurrency
	case n > maxToolConcurrency:
		n = maxToolConcurrency
	}
	return &Limiter{slots: make(chan struct{}, n)}
}

// Cap — сколько одновременных захватов допускает семафор.
func (l *Limiter) Cap() int {
	if l == nil {
		return 0
	}
	return cap(l.slots)
}

// Acquire занимает слот или ждёт его освобождения. Отмена контекста
// возвращает ошибку контекста, не занимая слот.
func (l *Limiter) Acquire(ctx context.Context) error {
	if l == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case l.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release возвращает слот. Вызов без успешного Acquire игнорируется, чтобы
// ошибка в вызывающем коде не роняла сервер паникой «send on closed channel».
func (l *Limiter) Release() {
	if l == nil {
		return
	}
	select {
	case <-l.slots:
	default:
	}
}

// TruncateText обрезает строку до maxRunes рун и сообщает, было ли усечение.
// Счёт идёт по рунам, а не байтам: кириллица и эмодзи не должны резаться
// посередине символа.
func TruncateText(s string, maxRunes int) (string, bool) {
	if maxRunes < 0 {
		maxRunes = 0
	}
	if maxRunes == 0 {
		return "", s != ""
	}
	i := 0
	for pos := range s {
		if i == maxRunes {
			return s[:pos], true
		}
		i++
	}
	return s, false
}

// toolCaches — кэши инструментов MCP. Живут столько же, сколько сессия
// Moodle, и не переживают её закрытие.
type toolCaches struct {
	outline   *TTLCache[OutlineOutput]
	material  *TTLCache[MaterialOutput]
	search    *TTLCache[SearchOutput]
	deadlines *TTLCache[DeadlinesOutput]
}

// newToolCaches создаёт кэши с TTL и потолком записей по умолчанию.
func newToolCaches() *toolCaches {
	return &toolCaches{
		outline:   NewTTLCache[OutlineOutput](outlineCacheTTL, defaultCacheEntries),
		material:  NewTTLCache[MaterialOutput](materialCacheTTL, defaultCacheEntries),
		search:    NewTTLCache[SearchOutput](outlineCacheTTL, defaultCacheEntries),
		deadlines: NewTTLCache[DeadlinesOutput](deadlinesCacheTTL, defaultCacheEntries),
	}
}
