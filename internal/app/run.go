package app

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
)

func Run(name string, needToken bool, fn func(context.Context, *config.Config, *store.Store, *time.Location) error) {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("ssau-" + name + " ")

	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	if needToken {
		if err := cfg.RequireBotToken(); err != nil {
			log.Fatal(err)
		}
	}
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		log.Fatalf("timezone %s: %v", cfg.Timezone, err)
	}
	if err := os.MkdirAll(cfg.RecordingsDir, 0755); err != nil {
		log.Fatal(err)
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()
	if err := st.RememberAdmin(config.DefaultAdminID); err != nil {
		log.Printf("admin id: %v", err)
	}
	cfg.AdminID = config.DefaultAdminID

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("up db=%s tz=%s", cfg.DBPath, cfg.Timezone)
	if err := fn(ctx, cfg, st, loc); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}
