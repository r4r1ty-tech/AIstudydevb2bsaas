package main

import (
	"context"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/app"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/tg"
)

func main() {
	logx.Infof("tg", "starting tg")
	logx.Debugf("tg", "main: binary=tg need_token=true")
	app.Run("tg", true, func(ctx context.Context, cfg *config.Config, st *store.Store, loc *time.Location) error {
		logx.Debugf("tg", "main: tg.New cfg_nil=%v store_nil=%v", cfg == nil, st == nil)
		bot, err := tg.New(cfg, st, loc)
		if err != nil {
			logx.Errorf("tg", "main: tg.New: %v", err)
			return err
		}
		logx.Debugf("tg", "main: bot.Start enter")
		if err := bot.Start(ctx); err != nil {
			logx.Errorf("tg", "main: bot.Start: %v", err)
			return err
		}
		logx.Debugf("tg", "main: bot.Start returned")
		return nil
	})
}
