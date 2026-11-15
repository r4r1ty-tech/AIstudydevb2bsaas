package main

import (
	"context"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/access"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/app"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/notify"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/uni"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/vault"
)

// ssau-quiz: решает тесты Moodle из очереди в базе (quiz_jobs), которую ставит rasp.
// Без ключа шифрования решать нечем: админ получает уведомление, процесс ждёт остановки и не падает.
func main() {
	logx.Infof("quiz", "starting quiz")
	app.Run("quiz", false, func(ctx context.Context, cfg *config.Config, st *store.Store, loc *time.Location) error {
		v, err := vault.Load(cfg.SecretKey, cfg.SecretKeyFile)
		if err != nil {
			logx.Errorf("quiz", "vault: %v — решение тестов не запущено", err)
			notify.Admin(ctx, cfg, "ключ шифрования не загрузился, тесты не решаются: "+err.Error())
			// Как в rasp: процесс живёт до остановки, а не падает в цикл перезапусков (раз в 5 с с уведомлением).
			<-ctx.Done()
			return nil
		}
		access.InitLLMBridge(st, v)
		defer access.InitLLMBridge(nil, nil)

		sy := &uni.Syncer{
			Store: st, Vault: v, Cfg: cfg, Loc: loc,
			MoodleBase: cfg.MoodleBaseURL,
			Notify:     func(ctx context.Context, id int64, text string) { notify.HTML(ctx, cfg, id, text) },
		}
		sy.RunQuizWorker(ctx)
		return nil
	})
}
