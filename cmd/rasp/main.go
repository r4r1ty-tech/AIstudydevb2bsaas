package main

import (
	"context"
	"log"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/app"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/notify"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/rasp"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
)

func main() {
	app.Run("rasp", false, func(ctx context.Context, cfg *config.Config, st *store.Store, loc *time.Location) error {
		r := &rasp.Refresher{Store: st, GroupID: cfg.GroupID, Loc: loc}
		run, err := r.Refresh(ctx)
		if err != nil {
			notify.Admin(ctx, cfg, "парсер не смог: "+err.Error())
		} else if !run.OK {
			notify.Admin(ctx, cfg, "парсер: "+run.Status)
		} else {
			log.Printf("lessons=%d online=%d", run.LessonCount, run.OnlineCount)
		}
		r.StartCron(ctx)
		return nil
	})
}
