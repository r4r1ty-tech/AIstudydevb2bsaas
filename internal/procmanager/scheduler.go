package procmanager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

type Priority int

const (
	PriorityHigh   Priority = 1 // Live BBB session
	PriorityMedium Priority = 2 // Media processing / ffmpeg
	PriorityLow    Priority = 3 // Fish Studio STT & LLM
)

type Task struct {
	ID       string
	Priority Priority
	Work     func(ctx context.Context) error
}

type Scheduler struct {
	MaxConcurrentMediaJobs int
	SwapDir                string
	sem                    chan struct{}
	mu                     sync.Mutex
}

func NewScheduler(maxMediaJobs int, swapDir string) *Scheduler {
	if maxMediaJobs <= 0 {
		maxMediaJobs = 1
	}
	if swapDir == "" {
		swapDir = filepath.Join(os.TempDir(), "ssau-swap-buf")
	}
	_ = os.MkdirAll(swapDir, 0755)

	return &Scheduler{
		MaxConcurrentMediaJobs: maxMediaJobs,
		SwapDir:                swapDir,
		sem:                    make(chan struct{}, maxMediaJobs),
	}
}

// ExecuteMediaJob runs a heavy media job respecting max concurrency limit and performing GC cleanup.
func (s *Scheduler) ExecuteMediaJob(ctx context.Context, taskName string, fn func(ctx context.Context) error) error {
	select {
	case s.sem <- struct{}{}:
		defer func() {
			<-s.sem
			runtime.GC() // Free unused memory immediately
		}()
	case <-ctx.Done():
		return ctx.Err()
	}

	if err := fn(ctx); err != nil {
		return fmt.Errorf("task %s failed: %w", taskName, err)
	}

	return nil
}

// CreateSwapFile creates a temporary file buffer on disk (swap) to avoid RAM inflation.
func (s *Scheduler) CreateSwapFile(prefix string) (*os.File, error) {
	f, err := os.CreateTemp(s.SwapDir, prefix+"-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("create swap file: %w", err)
	}
	return f, nil
}

// CleanupSwap remove old temporary swap files.
func (s *Scheduler) CleanupSwap() error {
	entries, err := os.ReadDir(s.SwapDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			_ = os.Remove(filepath.Join(s.SwapDir, entry.Name()))
		}
	}
	return nil
}
