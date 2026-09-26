package rasp

import (
	"context"
	"fmt"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
)

func WaitRun(ctx context.Context, st *store.Store, after time.Time, timeout time.Duration) (model.ParseRun, error) {
	logx.Debugf("rasp", "WaitRun: enter after=%s timeout=%s", after.Format(time.RFC3339), timeout)
	if st == nil {
		logx.Errorf("rasp", "WaitRun: nil store")
		return model.ParseRun{}, fmt.Errorf("rasp: nil store")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := time.Now().Add(timeout)
	after = after.Add(-time.Second)
	logx.Debugf("rasp", "WaitRun: deadline=%s after=%s", deadline.Format(time.RFC3339), after.Format(time.RFC3339))
	attempts := 0
	for {
		attempts++
		run, err := st.LastParseRun()
		if err != nil {
			logx.Errorf("rasp", "WaitRun: last parse run attempt=%d: %v", attempts, err)
			return model.ParseRun{}, err
		}
		if run != nil {
			logx.Debugf("rasp", "WaitRun: attempt=%d run_at=%s ok=%v", attempts, run.At.Format(time.RFC3339), run.OK)
		} else {
			logx.Debugf("rasp", "WaitRun: attempt=%d no runs", attempts)
		}
		if run != nil && run.At.After(after) {
			logx.Infof("rasp", "WaitRun: satisfied attempts=%d run_at=%s", attempts, run.At.Format(time.RFC3339))
			return *run, nil
		}
		if time.Now().After(deadline) {
			logx.Warnf("rasp", "WaitRun: timeout after=%s attempts=%d", after.Format(time.RFC3339), attempts)
			return model.ParseRun{}, fmt.Errorf("rasp: не дождался рефреша — жив ли ssau-rasp.service?")
		}
		select {
		case <-ctx.Done():
			logx.Warnf("rasp", "WaitRun: ctx done attempts=%d: %v", attempts, ctx.Err())
			return model.ParseRun{}, ctx.Err()
		case <-time.After(400 * time.Millisecond):
		}
	}
}
