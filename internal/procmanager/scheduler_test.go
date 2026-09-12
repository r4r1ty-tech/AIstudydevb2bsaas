package procmanager

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestSchedulerConcurrencyLimit(t *testing.T) {
	sched := NewScheduler(1, "")
	var activeJobs int32
	var maxObservedJobs int32

	runJob := func() {
		_ = sched.ExecuteMediaJob(context.Background(), "test-job", func(ctx context.Context) error {
			current := atomic.AddInt32(&activeJobs, 1)
			for {
				oldMax := atomic.LoadInt32(&maxObservedJobs)
				if current <= oldMax || atomic.CompareAndSwapInt32(&maxObservedJobs, oldMax, current) {
					break
				}
			}
			time.Sleep(50 * time.Millisecond)
			atomic.AddInt32(&activeJobs, -1)
			return nil
		})
	}

	go runJob()
	go runJob()
	go runJob()

	time.Sleep(200 * time.Millisecond)

	if maxObservedJobs > 1 {
		t.Errorf("expected max concurrency 1, got %d", maxObservedJobs)
	}
}

func TestSwapFile(t *testing.T) {
	sched := NewScheduler(1, "")
	f, err := sched.CreateSwapFile("test")
	if err != nil {
		t.Fatalf("failed to create swap file: %v", err)
	}
	path := f.Name()
	_, _ = f.WriteString("swap data")
	_ = f.Close()

	if _, err := os.Stat(path); err != nil {
		t.Errorf("swap file stat error: %v", err)
	}

	if err := sched.CleanupSwap(); err != nil {
		t.Errorf("cleanup swap error: %v", err)
	}
}
