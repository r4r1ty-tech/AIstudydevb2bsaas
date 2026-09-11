package main

import (
	"context"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/app"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/rasp"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/webapp"
)

func main() {
	app.Run("panel", true, func(ctx context.Context, cfg *config.Config, st *store.Store, loc *time.Location) error {
		_ = loc
		refresh := func(ctx context.Context) (model.ParseRun, error) {
			t := time.Now()
			if err := st.RequestRaspRefresh(); err != nil {
				return model.ParseRun{}, err
			}
			return rasp.WaitRun(ctx, st, t, 45*time.Second)
		}
		srv := webapp.New(cfg, st, refresh, cfg.RecordingsDir)
		return srv.Listen(ctx)
	})
}
