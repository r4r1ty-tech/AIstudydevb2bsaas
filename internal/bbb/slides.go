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

// SlideManifestItem описывает один слайд в манифесте slides.json.
type SlideManifestItem struct {
	Num          int    `json:"num"`                        // порядковый номер (1..N, соответствует 01.png)
	File         string `json:"file"`                       // имя файла картинки, например "01.png"
	SlideID      string `json:"slide_id,omitempty"`         // ID слайда в Meteor DDP
	PresID       string `json:"pres_id,omitempty"`          // ID презентации
	ImageURI     string `json:"image_uri,omitempty"`        // URI картинки слайда
	TxtURI       string `json:"txt_uri,omitempty"`          // URI текстового слоя
	Text         string `json:"text,omitempty"`             // текстовый слой слайда (из Content или TxtURI)
	FirstShownAt string `json:"first_shown_at,omitempty"`   // RFC3339 UTC время первого показа, если было
	ShownOffsetS int    `json:"shown_offset_sec,omitempty"` // секунды от начала сессии, если известно
	Timestamp    string `json:"timestamp,omitempty"`        // "[мм:сс]" от начала сессии, если известно
}

// ReadSlideManifest читает манифест слайдов slides.json из файла или каталога.
//
// Аргументы:
//   - pathOrDir: путь к файлу slides.json или к каталогу, содержащему slides.json
//
// Возвращает срез элементов манифеста или ошибку чтения/десериализации.
func ReadSlideManifest(pathOrDir string) ([]SlideManifestItem, error) {
	target := pathOrDir
	fi, err := os.Stat(target)
	if err != nil {
		return nil, err
	}
	if fi.IsDir() {
		target = filepath.Join(target, "slides.json")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return nil, err
	}
	var items []SlideManifestItem
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("read slide manifest %s: %w", target, err)
	}
	return items, nil
}

func writeSlideManifestAtomic(target string, items []SlideManifestItem) error {
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal slide manifest: %w", err)
	}
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write tmp manifest: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename tmp manifest: %w", err)
	}
	return nil
}

// GrabSlides загружает слайды всех показанных презентаций напрямую по HTTP
// из метаданных Meteor DDP (imageUri / png fallback / локальный рендер SVG->PNG).
// Сохраняет картинки слайдов, текстовый слой и манифест slides.json без повторного скачивания.
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

	// Ждём завершения начальной синхронизации подписки на слайды.
	select {
	case <-s.slidesSub:
	case <-time.After(2 * time.Second):
	case <-ctx.Done():
		return 0, ctx.Err()
	}

	// Сериализуем параллельные вызовы GrabSlides без удержания presMu,
	// чтобы горутина чтения Meteor DDP не блокировалась на HTTP и ФС.
	s.slidesGrabMu.Lock()
	defer s.slidesGrabMu.Unlock()

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
	presChanged := presSummary != s.presLogged
	if presChanged {
		s.presLogged = presSummary
	}
	if s.savedSlides == nil {
		s.savedSlides = make(map[string]string)
	}
	localSaved := make(map[string]string, len(s.savedSlides))
	for k, v := range s.savedSlides {
		localSaved[k] = v
	}
	s.presMu.Unlock()

	if presChanged {
		// Состав презентаций поменялся (новая или переключили) — пишем в основной лог.
		logx.Infof("bbb", "GrabSlides: презентации в комнате: %s; слайдов с картинкой=%d", presSummary, len(slides))
	} else {
		logx.Debugf("bbb", "GrabSlides: презентации без изменений: %s; слайдов=%d", presSummary, len(slides))
	}

	manifestJSONPath := filepath.Join(dir, "slides.json")
	var manifestItems []SlideManifestItem
	byKey := make(map[string]int)
	byFile := make(map[string]int)
	bySlideID := make(map[string]int)
	loadedSaved := make(map[string]string)

	if items, err := ReadSlideManifest(manifestJSONPath); err == nil {
		for _, it := range items {
			if !isSafeSlideFilename(it.File) {
				logx.Warnf("bbb", "GrabSlides: пропущена небезопасная запись в slides.json: file=%q", it.File)
				continue
			}
			idx := len(manifestItems)
			manifestItems = append(manifestItems, it)
			byFile[it.File] = idx
			if it.SlideID != "" {
				bySlideID[it.SlideID] = idx
			}
			k := it.PresID + "/" + it.SlideID
			if it.SlideID == "" {
				k = it.PresID + "/" + it.ImageURI
			}
			byKey[k] = idx
			localSaved[k] = it.File
			loadedSaved[k] = it.File
		}
	}

	metaPath := filepath.Join(dir, ".slides_meta.json")
	if data, err := os.ReadFile(metaPath); err == nil {
		var mf map[string]string
		if err := json.Unmarshal(data, &mf); err == nil {
			for k, v := range mf {
				if !isSafeSlideFilename(v) {
					logx.Warnf("bbb", "GrabSlides: пропущена небезопасная запись в .slides_meta.json: key=%q file=%q", k, v)
					continue
				}
				if _, ok := localSaved[k]; !ok {
					localSaved[k] = v
					loadedSaved[k] = v
				}
			}
		} else {
			logx.Warnf("bbb", "GrabSlides: манифест %s не читается: %v", metaPath, err)
		}
	}

	if len(loadedSaved) > 0 {
		s.presMu.Lock()
		for k, v := range loadedSaved {
			s.savedSlides[k] = v
		}
		s.presMu.Unlock()
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
	manifestUpdated := false

	for _, sl := range slides {
		if saved >= maxSlideItems || ctx.Err() != nil {
			break
		}

		sl = s.refreshSlideSnapshot(sl)

		slideKey := sl.PresentationID + "/" + sl.ID
		if sl.ID == "" {
			slideKey = sl.PresentationID + "/" + sl.ImageURI
		}

		idx := -1
		if i, ok := byKey[slideKey]; ok {
			idx = i
		} else if sl.ID != "" {
			if i, ok := bySlideID[sl.ID]; ok {
				idx = i
			}
		}

		file, alreadySaved := localSaved[slideKey]
		if alreadySaved && !isSafeSlideFilename(file) {
			alreadySaved = false
		}
		var fileExists bool
		if alreadySaved {
			if _, err := os.Stat(filepath.Join(dir, file)); err == nil {
				fileExists = true
				if idx == -1 {
					if i, ok := byFile[file]; ok {
						idx = i
					}
				}
			}
		}

		if alreadySaved && fileExists {
			if idx >= 0 && idx < len(manifestItems) {
				it := &manifestItems[idx]
				if it.SlideID == "" && sl.ID != "" {
					it.SlideID = sl.ID
					manifestUpdated = true
				}
				if it.PresID == "" && sl.PresentationID != "" {
					it.PresID = sl.PresentationID
					manifestUpdated = true
				}
				if it.ImageURI == "" && sl.ImageURI != "" {
					it.ImageURI = sl.ImageURI
					manifestUpdated = true
				}
				if it.TxtURI == "" && sl.TxtURI != "" {
					it.TxtURI = sl.TxtURI
					manifestUpdated = true
				}
				if it.FirstShownAt == "" && !sl.FirstShownAt.IsZero() {
					it.FirstShownAt, it.ShownOffsetS, it.Timestamp = formatSlideShownTime(sl.FirstShownAt, s.startedAt)
					manifestUpdated = true
				}
				if strings.TrimSpace(it.Text) == "" {
					txt := strings.TrimSpace(sl.Content)
					if txt == "" && sl.TxtURI != "" {
						txt = s.fetchSlideText(ctx, sl.TxtURI)
					}
					if txt != "" {
						it.Text = txt
						sl.Content = txt
						s.setSlideContent(sl, txt)
						manifestUpdated = true
					}
				}
			} else {
				var num int
				_, _ = fmt.Sscanf(file, "%02d.png", &num)
				if num == 0 {
					num = len(manifestItems) + 1
				}
				txt := strings.TrimSpace(sl.Content)
				if txt == "" && sl.TxtURI != "" {
					txt = s.fetchSlideText(ctx, sl.TxtURI)
				}
				if txt != "" {
					sl.Content = txt
					s.setSlideContent(sl, txt)
				}
				firstShownStr, offsetSec, tsStr := formatSlideShownTime(sl.FirstShownAt, s.startedAt)
				newItem := SlideManifestItem{
					Num:          num,
					File:         file,
					SlideID:      sl.ID,
					PresID:       sl.PresentationID,
					ImageURI:     sl.ImageURI,
					TxtURI:       sl.TxtURI,
					Text:         txt,
					FirstShownAt: firstShownStr,
					ShownOffsetS: offsetSec,
					Timestamp:    tsStr,
				}
				manifestItems = append(manifestItems, newItem)
				newIdx := len(manifestItems) - 1
				byKey[slideKey] = newIdx
				byFile[file] = newIdx
				if sl.ID != "" {
					bySlideID[sl.ID] = newIdx
				}
				manifestUpdated = true
			}
			logx.Debugf("bbb", "GrabSlides: слайд №%d id=%s уже в %s", sl.Num, sl.ID, file)
			continue
		}

		fetchStart := time.Now()
		pngData, err := s.fetchSlidePNG(ctx, sl)
		if err != nil {
			logx.Warnf("bbb", "GrabSlides: слайд №%d id=%s pres=%s url=%s не скачался за %s: %v",
				sl.Num, sl.ID, s.presName(sl.PresentationID), redactURL(sl.ImageURI), time.Since(fetchStart).Round(time.Millisecond), err)
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
		localSaved[slideKey] = filename
		s.setSavedSlide(slideKey, filename)

		txt := strings.TrimSpace(sl.Content)
		if txt == "" && sl.TxtURI != "" {
			txt = s.fetchSlideText(ctx, sl.TxtURI)
		}
		if txt != "" {
			sl.Content = txt
			s.setSlideContent(sl, txt)
		}

		firstShownStr, offsetSec, tsStr := formatSlideShownTime(sl.FirstShownAt, s.startedAt)

		newItem := SlideManifestItem{
			Num:          saved,
			File:         filename,
			SlideID:      sl.ID,
			PresID:       sl.PresentationID,
			ImageURI:     sl.ImageURI,
			TxtURI:       sl.TxtURI,
			Text:         txt,
			FirstShownAt: firstShownStr,
			ShownOffsetS: offsetSec,
			Timestamp:    tsStr,
		}
		manifestItems = append(manifestItems, newItem)
		newIdx := len(manifestItems) - 1
		byKey[slideKey] = newIdx
		byFile[filename] = newIdx
		if sl.ID != "" {
			bySlideID[sl.ID] = newIdx
		}
		manifestUpdated = true

		logx.Infof("bbb", "GrabSlides: слайд №%d → %s pres=%s id=%s размер=%d байт за %s url=%s",
			sl.Num, filename, s.presName(sl.PresentationID), sl.ID, len(pngData),
			time.Since(fetchStart).Round(time.Millisecond), redactURL(sl.ImageURI))
	}

	if newCount > 0 || manifestUpdated {
		sort.Slice(manifestItems, func(i, j int) bool {
			return manifestItems[i].Num < manifestItems[j].Num
		})
		if err := writeSlideManifestAtomic(manifestJSONPath, manifestItems); err != nil {
			logx.Warnf("bbb", "GrabSlides: манифест %s не записан: %v", manifestJSONPath, err)
		}
		if mfBytes, err := json.Marshal(localSaved); err == nil {
			if err := os.WriteFile(metaPath, mfBytes, 0o644); err != nil {
				logx.Warnf("bbb", "GrabSlides: манифест %s не записан: %v", metaPath, err)
			}
		}
	}

	if newCount > 0 {
		logx.Infof("bbb", "GrabSlides: итог новых слайдов=%d всего=%d за %s dir=%s",
			newCount, saved, time.Since(started).Round(time.Millisecond), dir)
	} else {
		logx.Debugf("bbb", "GrabSlides: новых нет, всего=%d за %s", saved, time.Since(started).Round(time.Millisecond))
	}
	return saved, nil
}

// isSafeSlideFilename проверяет, что имя файла слайда не содержит путей или обхода каталогов.
func isSafeSlideFilename(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if filepath.Base(name) != name || strings.Contains(name, "..") {
		return false
	}
	if strings.ContainsAny(name, `/\`) {
		return false
	}
	return true
}

// formatSlideShownTime форматирует время первого показа слайда и смещение относительно начала сессии.
func formatSlideShownTime(firstShownAt, startedAt time.Time) (string, int, string) {
	if firstShownAt.IsZero() {
		return "", 0, ""
	}
	firstShownStr := firstShownAt.UTC().Format(time.RFC3339)
	if startedAt.IsZero() {
		return firstShownStr, 0, ""
	}
	diff := int(firstShownAt.Sub(startedAt).Seconds())
	if diff < 0 {
		diff = 0
	}
	return firstShownStr, diff, fmt.Sprintf("[%02d:%02d]", diff/60, diff%60)
}

// slideMapKeyLocked ищет ключ слайда в карте DDP по DocID с fallback на ID; presMu должен быть захвачен.
func slideMapKeyLocked(m map[string]slideMeta, sl slideMeta) (string, bool) {
	if sl.DocID != "" {
		if _, ok := m[sl.DocID]; ok {
			return sl.DocID, true
		}
	}
	if sl.ID != "" {
		if _, ok := m[sl.ID]; ok {
			return sl.ID, true
		}
		for k, cur := range m {
			if cur.ID == sl.ID && cur.PresentationID == sl.PresentationID {
				return k, true
			}
		}
	}
	return "", false
}

// refreshSlideSnapshot обновляет изменяемые поля слайда (FirstShownAt, Content, Current) из DDP-состояния.
func (s *directSession) refreshSlideSnapshot(sl slideMeta) slideMeta {
	s.presMu.Lock()
	defer s.presMu.Unlock()
	key, ok := slideMapKeyLocked(s.allSlides, sl)
	if !ok {
		return sl
	}
	cur := s.allSlides[key]
	if !cur.FirstShownAt.IsZero() {
		sl.FirstShownAt = cur.FirstShownAt
	}
	if cur.Content != "" {
		sl.Content = cur.Content
	}
	if cur.Current {
		sl.Current = cur.Current
	}
	return sl
}

// setSlideContent сохраняет загруженный текстовый слой слайда в памяти без перезаписи других полей DDP.
func (s *directSession) setSlideContent(sl slideMeta, txt string) {
	if txt == "" {
		return
	}
	s.presMu.Lock()
	defer s.presMu.Unlock()
	if key, ok := slideMapKeyLocked(s.allSlides, sl); ok {
		cur := s.allSlides[key]
		if strings.TrimSpace(cur.Content) == "" {
			cur.Content = txt
			s.allSlides[key] = cur
		}
	} else {
		key := sl.DocID
		if key == "" {
			key = sl.ID
		}
		if key != "" {
			sl.Content = txt
			s.allSlides[key] = sl
		}
	}
	if key, ok := slideMapKeyLocked(s.slides, sl); ok {
		cur := s.slides[key]
		if strings.TrimSpace(cur.Content) == "" {
			cur.Content = txt
			s.slides[key] = cur
		}
	}
}

// setSavedSlide фиксирует имя сохранённого файла слайда в карте savedSlides под presMu.
func (s *directSession) setSavedSlide(slideKey, filename string) {
	s.presMu.Lock()
	if s.savedSlides == nil {
		s.savedSlides = make(map[string]string)
	}
	s.savedSlides[slideKey] = filename
	s.presMu.Unlock()
}

// presName возвращает имя презентации по id с захватом presMu.
func (s *directSession) presName(pid string) string {
	s.presMu.Lock()
	defer s.presMu.Unlock()
	return s.presNameLocked(pid)
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

const maxSlideTextBytes = 128 * 1024 // 128 KiB на текстовый слой слайда

// isRejectedSlideTextContentType отбрасывает HTML-страницы ошибок и бинарные ответы вместо текста.
func isRejectedSlideTextContentType(ct string) bool {
	lower := strings.ToLower(strings.TrimSpace(ct))
	for _, bad := range []string{
		"text/html",
		"image/",
		"application/octet-stream",
		"application/pdf",
	} {
		if strings.Contains(lower, bad) {
			return true
		}
	}
	return false
}

// fetchSlideText загружает текстовый слой слайда по txtURI и возвращает очищенный текст.
func (s *directSession) fetchSlideText(ctx context.Context, txtURI string) string {
	if txtURI == "" {
		return ""
	}
	full := s.urlWithHost(txtURI)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		logx.Debugf("bbb", "fetchSlideText: new request %s: %v", redactURL(full), err)
		return ""
	}
	resp, raw, err := s.httpDoLimit(ctx, req, maxSlideTextBytes)
	if err != nil {
		logx.Debugf("bbb", "fetchSlideText: get %s: %v", redactURL(full), err)
		return ""
	}
	if resp.StatusCode != http.StatusOK {
		logx.Debugf("bbb", "fetchSlideText: %s http %d", redactURL(full), resp.StatusCode)
		return ""
	}
	if ct := resp.Header.Get("Content-Type"); isRejectedSlideTextContentType(ct) {
		logx.Debugf("bbb", "fetchSlideText: отброшен Content-Type=%q для %s", ct, redactURL(full))
		return ""
	}
	return strings.TrimSpace(string(raw))
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
