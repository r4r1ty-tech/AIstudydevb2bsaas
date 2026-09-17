package main

import (
	"context"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/app"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/notify"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/rasp"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
)

func main() {
	logx.Infof("rasp", "starting rasp")
	logx.Debugf("rasp", "main: binary=rasp need_token=false")
	app.Run("rasp", false, func(ctx context.Context, cfg *config.Config, st *store.Store, loc *time.Location) error {
		logx.Debugf("rasp", "main: refresh enter group_id=%d", cfg.GroupID)
		r := &rasp.Refresher{Store: st, GroupID: cfg.GroupID, Loc: loc}
		run, err := r.Refresh(ctx)
		if err != nil {
			logx.Errorf("rasp", "main: refresh: %v", err)
			notify.Admin(ctx, cfg, "парсер не смог: "+err.Error())
		} else if !run.OK {
			logx.Warnf("rasp", "main: refresh not ok status=%q", run.Status)
			notify.Admin(ctx, cfg, "парсер: "+run.Status)
		} else {
			logx.Infof("rasp", "lessons=%d online=%d", run.LessonCount, run.OnlineCount)
		}
		logx.Debugf("rasp", "main: StartCron enter")
		r.StartCron(ctx)
		logx.Debugf("rasp", "main: StartCron returned")
		return nil
	})
}
