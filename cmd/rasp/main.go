package main

import (
	"context"
	"sync"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/access"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/app"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/notify"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/rasp"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/uni"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/vault"
)

func main() {
	logx.Infof("rasp", "starting rasp")
	app.Run("rasp", false, func(ctx context.Context, cfg *config.Config, st *store.Store, loc *time.Location) error {
		r := &rasp.Refresher{Store: st, GroupID: cfg.GroupID, Loc: loc}

		// Аккаунты универа: ЛК — основной источник расписания, плюс уведомления ЛК
		// и задания Moodle. Без ключа шифрования живём по-старому (только ssau.ru/rasp).
		var wg sync.WaitGroup
		if v, err := vault.Load(cfg.SecretKey, cfg.SecretKeyFile); err != nil {
			logx.Errorf("rasp", "vault: %v — синк ЛК и Moodle выключен", err)
			notify.Admin(ctx, cfg, "ключ шифрования не загрузился, ЛК и Moodle не синкаются: "+err.Error())
		} else {
			access.InitLLMBridge(st, v)
			defer access.InitLLMBridge(nil, nil)
			sy := &uni.Syncer{
				Store: st, Vault: v, Cfg: cfg, Loc: loc, GroupID: cfg.GroupID, AdminID: cfg.AdminID,
				LKBase: cfg.LKBaseURL, MoodleBase: cfg.MoodleBaseURL,
				Notify: func(ctx context.Context, id int64, text string) { notify.HTML(ctx, cfg, id, text) },
			}
			r.Source = sy.Lessons
			wg.Add(1)
			go func() {
				defer wg.Done()
				sy.Run(ctx)
			}()
			// Дождаться завершения sy.Run перед возвратом и закрытием БД в app.Run.
			defer wg.Wait()
		}

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
		r.StartCron(ctx)
		return nil
	})
}
