package main

import (
	"context"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/app"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/tg"
)

func main() {
	app.Run("tg", true, func(ctx context.Context, cfg *config.Config, st *store.Store, loc *time.Location) error {
		bot, err := tg.New(cfg, st, loc)
		if err != nil {
			return err
		}
		return bot.Start(ctx)
	})
}
