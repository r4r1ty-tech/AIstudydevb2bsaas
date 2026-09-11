package rasp

import (
	"context"
	"fmt"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
)

func WaitRun(ctx context.Context, st *store.Store, after time.Time, timeout time.Duration) (model.ParseRun, error) {
	if st == nil {
		return model.ParseRun{}, fmt.Errorf("rasp: nil store")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := time.Now().Add(timeout)
	after = after.Add(-time.Second)
	for {
		run, err := st.LastParseRun()
		if err != nil {
			return model.ParseRun{}, err
		}
		if run != nil && run.At.After(after) {
			return *run, nil
		}
		if time.Now().After(deadline) {
			return model.ParseRun{}, fmt.Errorf("rasp: не дождался рефреша — жив ли ssau-rasp.service?")
		}
		select {
		case <-ctx.Done():
			return model.ParseRun{}, ctx.Err()
		case <-time.After(400 * time.Millisecond):
		}
	}
}
