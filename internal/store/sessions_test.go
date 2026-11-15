package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestWebSessions(t *testing.T) {
	st := openTemp(t)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	month := 30 * 24 * time.Hour

	if s, err := st.GetWebSession("нет", now); err != nil || s != nil {
		t.Fatalf("несуществующая: (%v, %v)", s, err)
	}
	if err := st.CreateWebSession("h1", 42, now, now.Add(month)); err != nil {
		t.Fatal(err)
	}
	s, err := st.GetWebSession("h1", now.Add(time.Hour))
	if err != nil || s == nil || s.TelegramID != 42 || !s.CreatedAt.Equal(now) || !s.ExpiresAt.Equal(now.Add(month)) {
		t.Fatalf("после создания: (%+v, %v)", s, err)
	}

	// Срок вышел — сессии нет.
	if s, _ := st.GetWebSession("h1", now.Add(month)); s != nil {
		t.Fatalf("просроченная отдалась: %+v", s)
	}

	// Продление двигает срок вперёд и никогда не назад.
	if err := st.ExtendWebSession("h1", now.Add(50*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := st.ExtendWebSession("h1", now.Add(40*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	s, _ = st.GetWebSession("h1", now)
	if s == nil || !s.ExpiresAt.Equal(now.Add(50*24*time.Hour)) {
		t.Fatalf("после продления: %+v", s)
	}

	// Выход удаляет; чужие сессии не трогает.
	if err := st.CreateWebSession("h2", 42, now, now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateWebSession("other", 7, now, now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteWebSession("h1"); err != nil {
		t.Fatal(err)
	}
	if s, _ := st.GetWebSession("h1", now); s != nil {
		t.Fatal("h1 не удалилась")
	}
	if err := st.DeleteUserWebSessions(42); err != nil {
		t.Fatal(err)
	}
	if s, _ := st.GetWebSession("h2", now); s != nil {
		t.Fatal("h2 не удалилась")
	}
	if s, _ := st.GetWebSession("other", now); s == nil {
		t.Fatal("сессия другого человека пропала")
	}
}

func TestWebSessionsPurgeAndTrim(t *testing.T) {
	st := openTemp(t)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	if err := st.CreateWebSession("old", 1, now.Add(-48*time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n, err := st.PurgeWebSessions(now); err != nil || n != 1 {
		t.Fatalf("purge: n=%d err=%v", n, err)
	}

	// Создание новой сессии заодно вычищает просроченные.
	if err := st.CreateWebSession("old2", 1, now.Add(-48*time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateWebSession("fresh", 2, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.PurgeWebSessions(now); n != 0 {
		t.Fatalf("просроченная осталась после Create: %d", n)
	}

	// Лимит на человека: остаются самые новые.
	total := WebSessionsPerUser + 5
	name := func(i int) string { return "u9-" + string(rune('a'+i)) }
	for i := 0; i < total; i++ {
		at := now.Add(time.Duration(i) * time.Minute)
		if err := st.CreateWebSession(name(i), 9, at, at.Add(24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	alive := 0
	for i := 0; i < total; i++ {
		s, _ := st.GetWebSession(name(i), now.Add(time.Hour))
		if (s != nil) != (i >= 5) {
			t.Fatalf("сессия %d: живая=%v", i, s != nil)
		}
		if s != nil {
			alive++
		}
	}
	if alive != WebSessionsPerUser {
		t.Fatalf("живых %d, ждали %d", alive, WebSessionsPerUser)
	}
	if s, _ := st.GetWebSession("fresh", now); s == nil {
		t.Fatal("сессия другого человека вытеснена")
	}
}

// Миграция: база без таблицы web_sessions (старый деплой) открывается и
// получает её; повторное открытие ничего не ломает и не стирает.
func TestWebSessionsMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bot.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`DROP TABLE web_sessions`); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	st, err = Open(path)
	if err != nil {
		t.Fatalf("Open старой базы: %v", err)
	}
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	if err := st.CreateWebSession("keep", 5, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	st, err = Open(path)
	if err != nil {
		t.Fatalf("повторный Open: %v", err)
	}
	defer st.Close()
	if s, _ := st.GetWebSession("keep", now); s == nil || s.TelegramID != 5 {
		t.Fatalf("сессия не пережила переоткрытие: %+v", s)
	}
}
