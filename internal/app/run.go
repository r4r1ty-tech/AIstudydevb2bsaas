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
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
)

func Run(name string, needToken bool, fn func(context.Context, *config.Config, *store.Store, *time.Location) error) {
	logx.Debugf("app", "Run: enter name=%s need_token=%v", name, needToken)
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("ssau-" + name + " ")

	logx.Infof("app", "Run: starting %s", name)
	cfg, err := config.Load()
	if err != nil {
		logx.Errorf("app", "Run: config.Load: %v", err)
		log.Fatal(err)
	}
	logx.Setup()
	if needToken {
		if err := cfg.RequireBotToken(); err != nil {
			logx.Errorf("app", "Run: require bot token: %v", err)
			log.Fatal(err)
		}
	}
	logx.Debugf("app", "Run: config loaded db=%s tz=%s recordings=%s group_id=%d", cfg.DBPath, cfg.Timezone, cfg.RecordingsDir, cfg.GroupID)
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		logx.Errorf("app", "Run: timezone %s: %v", cfg.Timezone, err)
		log.Fatalf("timezone %s: %v", cfg.Timezone, err)
	}
	if err := os.MkdirAll(cfg.RecordingsDir, 0755); err != nil {
		logx.Errorf("app", "Run: mkdir recordings %s: %v", cfg.RecordingsDir, err)
		log.Fatal(err)
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		logx.Errorf("app", "Run: store.Open %s: %v", cfg.DBPath, err)
		log.Fatal(err)
	}
	defer st.Close()

	// Админ задаётся в env (ADMIN_TELEGRAM_ID), дефолт config.DefaultAdminID.
	if cfg.AdminID == 0 {
		cfg.AdminID = config.DefaultAdminID
	}
	logx.Infof("app", "admin=%d whitelist=%d", cfg.AdminID, len(cfg.Whitelist))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logx.Infof("app", "up db=%s tz=%s level=%s", cfg.DBPath, cfg.Timezone, logx.LevelName(logx.CurrentLevel()))
	if err := fn(ctx, cfg, st, loc); err != nil && ctx.Err() == nil {
		logx.Errorf("app", "exit: %v", err)
		log.Fatal(err)
	}
	logx.Infof("app", "stopped")
}
