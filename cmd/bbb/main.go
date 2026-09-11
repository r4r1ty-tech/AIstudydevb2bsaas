package main

import (
	"context"
	"log"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/app"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/bbb"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
)

func main() {
	app.Run("bbb", false, func(ctx context.Context, cfg *config.Config, st *store.Store, loc *time.Location) error {
		if cfg.BBBDryRun {
			log.Printf("DRY RUN — chromium не трогаем")
		} else {
			log.Printf("chrome=%s", bbb.FindChrome(cfg.ChromeBin))
		}
		return bbb.NewWorker(cfg, st, loc).Run(ctx)
	})
}
