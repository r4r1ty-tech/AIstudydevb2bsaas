package main

import (
	"context"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/app"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/rasp"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/webapp"
)

func main() {
	logx.Infof("panel", "starting panel")
	logx.Debugf("panel", "main: binary=panel need_token=true")
	app.Run("panel", true, func(ctx context.Context, cfg *config.Config, st *store.Store, loc *time.Location) error {
		_ = loc
		refresh := func(ctx context.Context) (model.ParseRun, error) {
			logx.Debugf("panel", "main: refresh requested")
			t := time.Now()
			if err := st.RequestRaspRefresh(); err != nil {
				logx.Errorf("panel", "main: RequestRaspRefresh: %v", err)
				return model.ParseRun{}, err
			}
			run, err := rasp.WaitRun(ctx, st, t, 45*time.Second)
			if err != nil {
				logx.Errorf("panel", "main: WaitRun: %v", err)
				return run, err
			}
			logx.Debugf("panel", "main: refresh done lessons=%d", run.LessonCount)
			return run, nil
		}
		logx.Infof("panel", "main: webapp starting listen=%s", cfg.ListenAddr)
		srv := webapp.New(cfg, st, refresh, cfg.RecordingsDir)
		if err := srv.Listen(ctx); err != nil {
			logx.Errorf("panel", "main: webapp.Listen: %v", err)
			return err
		}
		logx.Debugf("panel", "main: webapp.Listen returned")
		return nil
	})
}
