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
	app.Run("bbb", false, func(ctx context.Context, cfg *config.Config, st *store.Store, loc *time.Location) error {
		if cfg.BBBDryRun {
			logx.Warnf("bbb", "DRY RUN — chromium не трогаем")
		} else {
			logx.Infof("bbb", "chrome=%s", bbb.FindChrome(cfg.ChromeBin))
		}
		return bbb.NewWorker(cfg, st, loc).Run(ctx)
	})
}
