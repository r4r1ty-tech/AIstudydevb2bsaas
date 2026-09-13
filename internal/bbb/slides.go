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
		return 0, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return 0, err
	}
	page := s.page.Context(ctx)
	_ = page.Timeout(20 * time.Second).WaitLoad()
	time.Sleep(1500 * time.Millisecond)

	seen := map[string]struct{}{}
	n := 0
	for i := 0; i < 60; i++ {
		raw, err := shotPresentation(page)
		if err != nil || len(raw) < 80 {
			if i == 0 {
				raw, err = page.Timeout(8*time.Second).Screenshot(false, &proto.PageCaptureScreenshot{Format: proto.PageCaptureScreenshotFormatPng})
			}
			if err != nil || len(raw) < 80 {
				break
			}
		}
		sum := sha1.Sum(raw)
		key := hex.EncodeToString(sum[:])
		if _, ok := seen[key]; ok {
			if !clickFirst(page.Timeout(2*time.Second), nextSlideSels) && !clickByText(page.Timeout(2*time.Second), `(?i)next slide|следующ`) {
				break
			}
			time.Sleep(700 * time.Millisecond)
			continue
		}
		seen[key] = struct{}{}
		n++
		name := filepath.Join(dir, fmt.Sprintf("%02d.png", n))
		if err := os.WriteFile(name, raw, 0644); err != nil {
			return n, err
		}
		if !clickFirst(page.Timeout(2*time.Second), nextSlideSels) && !clickByText(page.Timeout(2*time.Second), `(?i)next slide|следующ`) {
			break
		}
		time.Sleep(800 * time.Millisecond)
	}
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
			return bin, nil
		}
	}
	return nil, fmt.Errorf("нет области презентации")
}
