package bbb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

// GrabSlides fetches slides of all shown presentations directly over HTTP
// from Meteor DDP metadata (imageUri / png fallback / local SVG->PNG render).
// It saves newly appeared slides without overwriting already downloaded ones.
func (s *directSession) GrabSlides(ctx context.Context, dir string) (int, error) {
	if s == nil {
		return 0, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	room := s.inRoom && !s.inLobby && !s.closed
	s.mu.Unlock()
	if !room {
		logx.Debugf("bbb", "GrabSlides: not in room, skip")
		return 0, nil
	}

	// Give the slides subscription a short moment to complete initial sync.
	select {
	case <-s.slidesSub:
	case <-time.After(2 * time.Second):
	case <-ctx.Done():
		return 0, ctx.Err()
	}

	started := time.Now()
	slides := s.collectAllSlides()
	if len(slides) == 0 {
		ents, _ := os.ReadDir(dir)
		count := 0
		for _, e := range ents {
			if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".png") {
				count++
			}
		}
		if count > 0 {
			logx.Debugf("bbb", "GrabSlides: в комнате слайдов нет, в папке уже png=%d dir=%s", count, dir)
			return count, nil
		}
		logx.Debugf("bbb", "GrabSlides: no slides in meeting")
		return 0, nil
	}
	presSummary := s.describePresentations(slides)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logx.Errorf("bbb", "GrabSlides: mkdir %s: %v", dir, err)
		return 0, fmt.Errorf("GrabSlides: mkdir: %w", err)
	}

	s.presMu.Lock()
	defer s.presMu.Unlock()
	if presSummary != s.presLogged {
		// Состав презентаций поменялся (новая или переключили) — пишем в основной лог.
		s.presLogged = presSummary
		logx.Infof("bbb", "GrabSlides: презентации в комнате: %s; слайдов с картинкой=%d", presSummary, len(slides))
	} else {
		logx.Debugf("bbb", "GrabSlides: презентации без изменений: %s; слайдов=%d", presSummary, len(slides))
	}
	if s.savedSlides == nil {
		s.savedSlides = make(map[string]string)
	}

	manifestPath := filepath.Join(dir, ".slides_meta.json")
	if data, err := os.ReadFile(manifestPath); err == nil {
		var mf map[string]string
		if err := json.Unmarshal(data, &mf); err == nil {
			for k, v := range mf {
				s.savedSlides[k] = v
			}
		} else {
			logx.Warnf("bbb", "GrabSlides: манифест %s не читается: %v", manifestPath, err)
		}
	}

	ents, _ := os.ReadDir(dir)
	existingCount := 0
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".png") {
			existingCount++
		}
	}

	saved := existingCount
	newCount := 0
	for _, sl := range slides {
		if saved >= maxSlideItems || ctx.Err() != nil {
			break
		}
		slideKey := sl.PresentationID + "/" + sl.ID
		if sl.ID == "" {
			slideKey = sl.PresentationID + "/" + sl.ImageURI
		}
		if file, ok := s.savedSlides[slideKey]; ok {
			if _, err := os.Stat(filepath.Join(dir, file)); err == nil {
				logx.Debugf("bbb", "GrabSlides: слайд №%d id=%s уже в %s", sl.Num, sl.ID, file)
				continue
			}
		}

		fetchStart := time.Now()
		pngData, err := s.fetchSlidePNG(ctx, sl)
		if err != nil {
			logx.Warnf("bbb", "GrabSlides: слайд №%d id=%s pres=%s url=%s не скачался за %s: %v",
				sl.Num, sl.ID, s.presNameLocked(sl.PresentationID), redactURL(sl.ImageURI), time.Since(fetchStart).Round(time.Millisecond), err)
			continue
		}
		saved++
		newCount++
		filename := fmt.Sprintf("%02d.png", saved)
		out := filepath.Join(dir, filename)
		if err := os.WriteFile(out, pngData, 0o644); err != nil {
			logx.Errorf("bbb", "GrabSlides: записать %s: %v", out, err)
			return saved - 1, fmt.Errorf("GrabSlides: write %s: %w", out, err)
		}
		s.savedSlides[slideKey] = filename
		logx.Infof("bbb", "GrabSlides: слайд №%d → %s pres=%s id=%s размер=%d байт за %s url=%s",
			sl.Num, filename, s.presNameLocked(sl.PresentationID), sl.ID, len(pngData),
			time.Since(fetchStart).Round(time.Millisecond), redactURL(sl.ImageURI))
	}

	if newCount > 0 {
		if mfBytes, err := json.Marshal(s.savedSlides); err == nil {
			if err := os.WriteFile(manifestPath, mfBytes, 0o644); err != nil {
				logx.Warnf("bbb", "GrabSlides: манифест %s не записан: %v", manifestPath, err)
			}
		}
		logx.Infof("bbb", "GrabSlides: итог новых слайдов=%d всего=%d за %s dir=%s",
			newCount, saved, time.Since(started).Round(time.Millisecond), dir)
	} else {
		logx.Debugf("bbb", "GrabSlides: новых нет, всего=%d за %s", saved, time.Since(started).Round(time.Millisecond))
	}
	return saved, nil
}

// presNameLocked — имя презентации по id для лога; presMu должен быть захвачен вызывающим.
func (s *directSession) presNameLocked(pid string) string {
	pm := s.allPres[pid]
	if pm.ID == "" {
		pm = s.pres[pid]
	}
	if pm.Name == "" {
		return pid
	}
	return fmt.Sprintf("%q(%s)", pm.Name, pid)
}

// describePresentations — сводка по презентациям для лога: «имя: N слайдов».
func (s *directSession) describePresentations(slides []slideMeta) string {
	counts := map[string]int{}
	var order []string
	for _, sl := range slides {
		if _, ok := counts[sl.PresentationID]; !ok {
			order = append(order, sl.PresentationID)
		}
		counts[sl.PresentationID]++
	}
	s.presMu.Lock()
	defer s.presMu.Unlock()
	parts := make([]string, 0, len(order))
	for _, pid := range order {
		parts = append(parts, fmt.Sprintf("%s: %d", s.presNameLocked(pid), counts[pid]))
	}
	return strings.Join(parts, "; ")
}

func (s *directSession) collectCurrentSlides() []slideMeta {
	return s.collectAllSlides()
}

func (s *directSession) collectAllSlides() []slideMeta {
	s.presMu.Lock()
	defer s.presMu.Unlock()

	presWithSlides := make(map[string]bool)
	for _, sl := range s.allSlides {
		if sl.ImageURI != "" || sl.ThumbURI != "" {
			presWithSlides[sl.PresentationID] = true
		}
	}
	for _, sl := range s.slides {
		if sl.ImageURI != "" || sl.ThumbURI != "" {
			presWithSlides[sl.PresentationID] = true
		}
	}

	hasNonDefault := false
	for pid := range presWithSlides {
		pm := s.allPres[pid]
		if pm.ID == "" {
			pm = s.pres[pid]
		}
		if !isDefaultPresentation(pm.Name, pm.ID) {
			hasNonDefault = true
			break
		}
	}

	seenPres := make(map[string]bool)
	var orderedPres []string
	for _, pid := range s.presOrder {
		if !seenPres[pid] {
			seenPres[pid] = true
			orderedPres = append(orderedPres, pid)
		}
	}
	for pid := range s.allPres {
		if !seenPres[pid] {
			seenPres[pid] = true
			orderedPres = append(orderedPres, pid)
		}
	}
	for pid := range s.pres {
		if !seenPres[pid] {
			seenPres[pid] = true
			orderedPres = append(orderedPres, pid)
		}
	}
	for pid := range presWithSlides {
		if !seenPres[pid] {
			seenPres[pid] = true
			orderedPres = append(orderedPres, pid)
		}
	}

	var allSlidesList []slideMeta
	seenSlide := make(map[string]bool)

	for _, pid := range orderedPres {
		pm := s.allPres[pid]
		if pm.ID == "" {
			pm = s.pres[pid]
		}
		if hasNonDefault && isDefaultPresentation(pm.Name, pm.ID) {
			continue
		}
		var pSlides []slideMeta
		for _, sl := range s.allSlides {
			if sl.PresentationID != pid || (sl.ImageURI == "" && sl.ThumbURI == "") {
				continue
			}
			key := sl.PresentationID + "/" + sl.ID
			if sl.ID == "" {
				key = sl.PresentationID + "/" + sl.ImageURI
			}
			if !seenSlide[key] {
				seenSlide[key] = true
				pSlides = append(pSlides, sl)
			}
		}
		for _, sl := range s.slides {
			if sl.PresentationID != pid || (sl.ImageURI == "" && sl.ThumbURI == "") {
				continue
			}
			key := sl.PresentationID + "/" + sl.ID
			if sl.ID == "" {
				key = sl.PresentationID + "/" + sl.ImageURI
			}
			if !seenSlide[key] {
				seenSlide[key] = true
				pSlides = append(pSlides, sl)
			}
		}
		sort.Slice(pSlides, func(i, j int) bool {
			if pSlides[i].Num != pSlides[j].Num {
				return pSlides[i].Num < pSlides[j].Num
			}
			return pSlides[i].ID < pSlides[j].ID
		})
		allSlidesList = append(allSlidesList, pSlides...)
	}

	if len(allSlidesList) > maxSlideItems {
		allSlidesList = allSlidesList[:maxSlideItems]
	}
	return allSlidesList
}

func isDefaultPresentation(name, id string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	i := strings.ToLower(strings.TrimSpace(id))
	return n == "default.pdf" || strings.HasPrefix(n, "default") || i == "default"
}

func (s *directSession) fetchSlidePNG(ctx context.Context, sl slideMeta) ([]byte, error) {
	uri := sl.ImageURI
	if uri == "" {
		uri = sl.ThumbURI
	}
	full := s.urlWithHost(uri)

	// Many BBB deployments also serve pre-rendered PNG alongside /svg/N.
	if strings.Contains(full, "/svg/") {
		pngCandidate := strings.Replace(full, "/svg/", "/png/", 1)
		if ct, raw, err := s.downloadSlideBytes(ctx, pngCandidate); err == nil && isPNGData(ct, raw) {
			return raw, nil
		}
	}

	ct, raw, err := s.downloadSlideBytes(ctx, full)
	if err != nil {
		return nil, err
	}
	if isPNGData(ct, raw) {
		return raw, nil
	}
	if strings.Contains(strings.ToLower(ct), "svg") || bytes.Contains(raw[:minInt(len(raw), 256)], []byte("<svg")) {
		return convertSVGToPNG(ctx, raw)
	}
	return convertAnyToPNG(ctx, raw)
}

func (s *directSession) downloadSlideBytes(ctx context.Context, target string) (string, []byte, error) {
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return "", nil, err
	}
	resp, raw, err := s.httpDo(ctx, req)
	if err != nil {
		return "", nil, err
	}
	if resp.StatusCode != http.StatusOK || len(raw) < 32 {
		return "", nil, fmt.Errorf("http %d len=%d", resp.StatusCode, len(raw))
	}
	return resp.Header.Get("Content-Type"), raw, nil
}

func isPNGData(ct string, raw []byte) bool {
	if len(raw) >= 8 && string(raw[:4]) == "\x89PNG" {
		return true
	}
	return strings.Contains(strings.ToLower(ct), "image/png")
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func convertSVGToPNG(ctx context.Context, svgBytes []byte) ([]byte, error) {
	dir, err := os.MkdirTemp("", "bbb-slide-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	inPath := filepath.Join(dir, "slide.svg")
	outPath := filepath.Join(dir, "slide.png")
	if err := os.WriteFile(inPath, svgBytes, 0o600); err != nil {
		return nil, err
	}
	return rasterizeToPNG(ctx, inPath, outPath)
}

func convertAnyToPNG(ctx context.Context, raw []byte) ([]byte, error) {
	dir, err := os.MkdirTemp("", "bbb-slide-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	inPath := filepath.Join(dir, "slide.bin")
	outPath := filepath.Join(dir, "slide.png")
	if err := os.WriteFile(inPath, raw, 0o600); err != nil {
		return nil, err
	}
	return rasterizeToPNG(ctx, inPath, outPath)
}

func rasterizeToPNG(ctx context.Context, inPath, outPath string) ([]byte, error) {
	candidates := [][]string{
		{"ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-i", inPath, "-frames:v", "1", outPath},
		{"rsvg-convert", "-o", outPath, inPath},
		{"magick", inPath, outPath},
		{"convert", inPath, outPath},
	}
	var lastErr error
	for _, argv := range candidates {
		bin, err := exec.LookPath(argv[0])
		if err != nil {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		out, rerr := exec.CommandContext(cctx, bin, argv[1:]...).CombinedOutput()
		cancel()
		if rerr == nil {
			if data, derr := os.ReadFile(outPath); derr == nil && len(data) > 32 {
				return data, nil
			}
		} else {
			lastErr = fmt.Errorf("%s: %v (%s)", argv[0], rerr, strings.TrimSpace(string(out)))
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("нет инструмента для конвертации слайда в PNG (ffmpeg/rsvg-convert/magick)")
}
