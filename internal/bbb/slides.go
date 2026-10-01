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
	dups := 0
	for i := 0; i < 80 && n < 60; i++ {
		raw, err := shotPresentation(page)
		if err != nil || len(raw) < 80 {
			// Без области презентации снимок всей страницы — это лобби или заглушка
			// Greenlight, а не слайд: vision-конспект по нему — мусор.
			logx.Debugf("bbb", "GrabSlides: iter=%d no presentation: bytes=%d err=%v", i, len(raw), err)
			break
		}
		sum := sha1.Sum(raw)
		key := hex.EncodeToString(sum[:])
		if _, ok := seen[key]; ok {
			dups++
			// Слайд мог не успеть отрисоваться: даём паузу, а не жмём «дальше» сразу.
			if dups >= 3 {
				logx.Debugf("bbb", "GrabSlides: iter=%d %d duplicates in a row, stop", i, dups)
				break
			}
			time.Sleep(time.Duration(dups) * 800 * time.Millisecond)
			if dups == 2 && !clickNextSlide(page) {
				logx.Debugf("bbb", "GrabSlides: iter=%d no next button, stop", i)
				break
			}
			continue
		}
		dups = 0
		seen[key] = struct{}{}
		n++
		name := filepath.Join(dir, fmt.Sprintf("%02d.png", n))
		if err := os.WriteFile(name, raw, 0644); err != nil {
			logx.Errorf("bbb", "GrabSlides: write %s: %v", name, err)
			return n, fmt.Errorf("GrabSlides: write: %w", err)
		}
		logx.Debugf("bbb", "GrabSlides: saved %s bytes=%d", name, len(raw))
		if !clickNextSlide(page) {
			logx.Debugf("bbb", "GrabSlides: iter=%d no next button after save, stop", i)
			break
		}
		time.Sleep(800 * time.Millisecond)
	}
	logx.Infof("bbb", "GrabSlides: captured n=%d dir=%s", n, dir)
	return n, nil
}

// nextSlideJS жмёт видимую активную «следующий слайд»: на последнем слайде BBB
// оставляет кнопку в DOM с disabled, и rod-клик по ней «успешен» — 60 дублей.
const nextSlideJS = `function (sels) {
	const ok = (n) => { const r = n.getBoundingClientRect(); return r.width > 0 && r.height > 0 && !n.disabled && n.getAttribute('aria-disabled') !== 'true' }
	let any = false
	for (const sel of sels) {
		for (const n of document.querySelectorAll(sel)) {
			any = true
			if (ok(n)) { n.click(); return 'click' }
		}
	}
	return any ? 'disabled' : 'none'
}`

func clickNextSlide(page *rod.Page) bool {
	res, err := page.Context(context.Background()).Timeout(3*time.Second).Eval(nextSlideJS, nextSlideSels)
	if err != nil || res == nil {
		logx.Debugf("bbb", "clickNextSlide: %v", err)
		return false
	}
	switch res.Value.Str() {
	case "click":
		return true
	case "disabled":
		return false
	}
	return clickByText(page.Timeout(2*time.Second), `(?i)next slide|следующ`)
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
