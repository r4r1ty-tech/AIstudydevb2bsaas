package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
)

// Run wires config, logging, timezone, recordings dir and the store, then
// hands them to the binary. log.Fatal paths are not reachable from a test.
func TestRunPassesWiredDependencies(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("TELEGRAM_BOT_TOKEN", "123:fake")
	t.Setenv("ADMIN_TELEGRAM_ID", "")
	t.Setenv("DATABASE_PATH", filepath.Join(dir, "data", "bot.db"))
	t.Setenv("RECORDINGS_DIR", filepath.Join(dir, "rec"))
	t.Setenv("TZ", "Europe/Samara")
	t.Setenv("LOG_FILE", "")
	t.Setenv("LOG_LEVEL", "warn")

	called := false
	Run("test", true, func(ctx context.Context, cfg *config.Config, st *store.Store, loc *time.Location) error {
		called = true
		if ctx == nil || cfg == nil || st == nil || loc == nil {
			t.Fatal("nil dependency")
		}
		if loc.String() != "Europe/Samara" {
			t.Errorf("loc = %s", loc)
		}
		if cfg.AdminID != config.DefaultAdminID {
			t.Errorf("admin default = %d", cfg.AdminID)
		}
		if _, err := st.ListUsers(); err != nil {
			t.Errorf("store not usable: %v", err)
		}
		return nil
	})
	if !called {
		t.Fatal("fn not called")
	}
	if st, err := os.Stat(filepath.Join(dir, "rec")); err != nil || !st.IsDir() {
		t.Fatalf("recordings dir not created: %v", err)
	}
}

// An error after the context is cancelled (normal shutdown) is not fatal.
func TestRunIgnoresErrorAfterShutdown(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("DATABASE_PATH", filepath.Join(dir, "bot.db"))
	t.Setenv("RECORDINGS_DIR", filepath.Join(dir, "rec"))
	t.Setenv("TZ", "UTC")
	t.Setenv("LOG_FILE", "")
	Run("test", false, func(ctx context.Context, _ *config.Config, _ *store.Store, _ *time.Location) error {
		p, _ := os.FindProcess(os.Getpid())
		_ = p.Signal(os.Interrupt)
		<-ctx.Done()
		return errors.New("stopped by signal")
	})
}
