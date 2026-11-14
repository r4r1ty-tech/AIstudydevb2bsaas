package main

import (
	"context"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/app"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/bbb"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
)

func main() {
	logx.Infof("bbb", "starting bbb")
	logx.Debugf("bbb", "main: binary=bbb need_token=false")
	app.Run("bbb", false, func(ctx context.Context, cfg *config.Config, st *store.Store, loc *time.Location) error {
		logx.Debugf("bbb", "main: worker enter dry_run=%v mode=direct", cfg.BBBDryRun)
		if cfg.BBBDryRun {
			logx.Warnf("bbb", "DRY RUN — в BBB не подключаемся")
		} else {
			logx.Infof("bbb", "mode=direct (go+ddp+pion, без Chromium)")
		}
		if err := bbb.NewWorker(cfg, st, loc).Run(ctx); err != nil {
			logx.Errorf("bbb", "main: worker.Run: %v", err)
			return err
		}
		logx.Debugf("bbb", "main: worker.Run returned")
		return nil
	})
}
