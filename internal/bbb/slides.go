package bbb

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

var nextSlideSels = []string{
	"[data-test='nextSlidePagination']",
	"[data-test='nextSlideButton']",
	"[data-test='nextSlide']",
	`button[aria-label='Next slide']`,
	`button[aria-label='Следующий слайд']`,
}

var presSels = []string{
	"[data-test='presentationInner']",
	"[data-test='presentationFullscreen']",
	"[data-test='whiteboard']",
	"div.presentation",
	"div[class*='presentation']",
	"canvas",
}

func (s *chromeSession) GrabSlides(ctx context.Context, dir string) (int, error) {
	if s == nil || s.page == nil {
		logx.Debugf("bbb", "GrabSlides: no page, skip")
		return 0, nil
	}
	logx.Debugf("bbb", "GrabSlides: dir=%s", dir)
	if ctx == nil {
		ctx = context.Background()
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		logx.Errorf("bbb", "GrabSlides: mkdir %s: %v", dir, err)
		return 0, fmt.Errorf("GrabSlides: mkdir: %w", err)
	}
	page := s.page.Context(ctx)
	if err := page.Timeout(20 * time.Second).WaitLoad(); err != nil {
		logx.Debugf("bbb", "GrabSlides: waitload: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)

	seen := map[string]struct{}{}
	n := 0
	for i := 0; i < 60; i++ {
		raw, err := shotPresentation(page)
		if err != nil || len(raw) < 80 {
			logx.Debugf("bbb", "GrabSlides: iter=%d shot: bytes=%d err=%v", i, len(raw), err)
			if i == 0 {
				raw, err = page.Timeout(8*time.Second).Screenshot(false, &proto.PageCaptureScreenshot{Format: proto.PageCaptureScreenshotFormatPng})
			}
			if err != nil || len(raw) < 80 {
				logx.Debugf("bbb", "GrabSlides: iter=%d no image, stop", i)
				break
			}
		}
		sum := sha1.Sum(raw)
		key := hex.EncodeToString(sum[:])
		if _, ok := seen[key]; ok {
			logx.Debugf("bbb", "GrabSlides: iter=%d duplicate slide, next", i)
			if !clickFirst(page.Timeout(2*time.Second), nextSlideSels) && !clickByText(page.Timeout(2*time.Second), `(?i)next slide|следующ`) {
				logx.Debugf("bbb", "GrabSlides: iter=%d no next button, stop", i)
				break
			}
			time.Sleep(700 * time.Millisecond)
			continue
		}
		seen[key] = struct{}{}
		n++
		name := filepath.Join(dir, fmt.Sprintf("%02d.png", n))
		if err := os.WriteFile(name, raw, 0644); err != nil {
			logx.Errorf("bbb", "GrabSlides: write %s: %v", name, err)
			return n, fmt.Errorf("GrabSlides: write: %w", err)
		}
		logx.Debugf("bbb", "GrabSlides: saved %s bytes=%d", name, len(raw))
		if !clickFirst(page.Timeout(2*time.Second), nextSlideSels) && !clickByText(page.Timeout(2*time.Second), `(?i)next slide|следующ`) {
			logx.Debugf("bbb", "GrabSlides: iter=%d no next button after save, stop", i)
			break
		}
		time.Sleep(800 * time.Millisecond)
	}
	logx.Infof("bbb", "GrabSlides: captured n=%d dir=%s", n, dir)
	return n, nil
}

func shotPresentation(page *rod.Page) ([]byte, error) {
	for _, sel := range presSels {
		ok, el, err := page.Has(sel)
		if err != nil || !ok || el == nil {
			continue
		}
		bin, err := el.Screenshot(proto.PageCaptureScreenshotFormatPng, 90)
		if err == nil && len(bin) > 80 {
			logx.Debugf("bbb", "shotPresentation: %s bytes=%d", sel, len(bin))
			return bin, nil
		}
		if err != nil {
			logx.Debugf("bbb", "shotPresentation: %s: %v", sel, err)
		}
	}
	err := fmt.Errorf("нет области презентации")
	logx.Errorf("bbb", "shotPresentation: %v", err)
	return nil, err
}
