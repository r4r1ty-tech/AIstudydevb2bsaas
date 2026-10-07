package notes

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

type visionMsg struct {
	Role    string `json:"role"`
	Content []any  `json:"content"`
}

type visionReq struct {
	Model    string      `json:"model"`
	Messages []visionMsg `json:"messages"`
}

type visionProvider struct {
	name  string
	key   string
	base  string
	model string
}

func visionProviders(cfg *config.Config) []visionProvider {
	logx.Debugf("notes", "visionProviders: cfg_nil=%t", cfg == nil)
	if cfg == nil {
		return nil
	}
	var out []visionProvider
	if k := strings.TrimSpace(cfg.GroqAPIKey); k != "" {
		base := strings.TrimRight(cfg.GroqAPIURL, "/")
		if base == "" {
			base = "https://api.groq.com/openai/v1"
		}
		model := cfg.GroqVisionModel
		if model == "" {
			model = "qwen/qwen3.6-27b"
		}
		out = append(out, visionProvider{name: "groq", key: k, base: base, model: model})
	}
	if k := strings.TrimSpace(cfg.GrokAPIKey); k != "" {
		base := strings.TrimRight(cfg.GrokAPIURL, "/")
		if base == "" {
			base = "https://api.x.ai/v1"
		}
		model := cfg.GrokModel
		if model == "" {
			model = "grok-2-vision-1212"
		}
		out = append(out, visionProvider{name: "grok", key: k, base: base, model: model})
	}
	logx.Debugf("notes", "visionProviders: count=%d", len(out))
	for i, p := range out {
		logx.Debugf("notes", "visionProviders: [%d] name=%s model=%s base=%s key_len=%d", i, p.name, p.model, p.base, len(p.key))
	}
	return out
}

func DescribeSlides(ctx context.Context, cfg *config.Config, slidesDir string) (string, error) {
	logx.Debugf("notes", "DescribeSlides: start dir=%s", slidesDir)
	provs := visionProviders(cfg)
	if len(provs) == 0 {
		logx.Debugf("notes", "DescribeSlides: no vision providers, skip")
		return "", nil
	}
	ents, err := os.ReadDir(slidesDir)
	if err != nil {
		if os.IsNotExist(err) {
			logx.Debugf("notes", "DescribeSlides: dir %s not found, skip", slidesDir)
			return "", nil
		}
		logx.Errorf("notes", "DescribeSlides: read dir %s: %v", slidesDir, err)
		return "", fmt.Errorf("DescribeSlides: read dir %s: %w", slidesDir, err)
	}
	logx.Debugf("notes", "DescribeSlides: entries=%d providers=%d", len(ents), len(provs))
	var files []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		n := strings.ToLower(e.Name())
		if strings.HasSuffix(n, ".png") || strings.HasSuffix(n, ".jpg") || strings.HasSuffix(n, ".jpeg") || strings.HasSuffix(n, ".webp") {
			files = append(files, filepath.Join(slidesDir, e.Name()))
		}
	}
	sort.Strings(files)
	if len(files) > 40 {
		logx.Debugf("notes", "DescribeSlides: truncating slides %d -> 40", len(files))
		files = files[:40]
	}
	logx.Debugf("notes", "DescribeSlides: image files=%d", len(files))
	var b strings.Builder
	for i, p := range files {
		var text string
		var last error
		for _, prov := range provs {
			t, err := describeOne(ctx, prov, p, i+1)
			if err == nil {
				text = t
				last = nil
				break
			}
			logx.Warnf("notes", "DescribeSlides: slide %d provider=%s failed: %v", i+1, prov.name, err)
			last = err
		}
		if last != nil {
			logx.Errorf("notes", "DescribeSlides: slide %d all providers failed: %v", i+1, last)
			fmt.Fprintf(&b, "Слайд %d: ошибка (%v)\n", i+1, last)
			continue
		}
		logx.Debugf("notes", "DescribeSlides: slide %d ok chars=%d", i+1, len(text))
		fmt.Fprintf(&b, "Слайд %d:\n%s\n\n", i+1, text)
	}
	res := strings.TrimSpace(b.String())
	logx.Infof("notes", "DescribeSlides: done files=%d result_chars=%d", len(files), len(res))
	return res, nil
}

func describeOne(ctx context.Context, prov visionProvider, path string, n int) (string, error) {
	logx.Debugf("notes", "describeOne: start provider=%s model=%s slide=%d path=%s", prov.name, prov.model, n, path)
	raw, err := os.ReadFile(path)
	if err != nil {
		logx.Errorf("notes", "describeOne: read %s: %v", path, err)
		return "", fmt.Errorf("describeOne: read %s: %w", path, err)
	}
	logx.Debugf("notes", "describeOne: image bytes=%d", len(raw))
	if len(raw) < 32 {
		logx.Errorf("notes", "describeOne: пустой файл path=%s bytes=%d", path, len(raw))
		return "", fmt.Errorf("пустой файл")
	}
	mime := "image/png"
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		mime = "image/jpeg"
	case ".webp":
		mime = "image/webp"
	}
	logx.Debugf("notes", "describeOne: mime=%s base64_len=%d", mime, base64.StdEncoding.EncodedLen(len(raw)))
	dataURL := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw)
	payload, err := json.Marshal(visionReq{
		Model: prov.model,
		Messages: []visionMsg{{
			Role: "user",
			Content: []any{
				map[string]any{"type": "text", "text": fmt.Sprintf("Это слайд %d университетской лекции. Выпиши весь текст, формулы и смысл схемы. Формулы — только в LaTeX: в строке $...$, отдельной строкой $$...$$. По-русски, без вступлений.", n)},
				map[string]any{"type": "image_url", "image_url": map[string]string{"url": dataURL}},
			},
		}},
	})
	if err != nil {
		logx.Errorf("notes", "describeOne: marshal payload provider=%s: %v", prov.name, err)
		return "", err
	}
	endpoint := prov.base + "/chat/completions"
	logx.Debugf("notes", "describeOne: POST %s payload=%d bytes key_len=%d", endpoint, len(payload), len(prov.key))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		logx.Errorf("notes", "describeOne: new request %s: %v", endpoint, err)
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+prov.key)
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	cli := &http.Client{Timeout: 2 * time.Minute}
	res, err := cli.Do(req)
	if err != nil {
		logx.Errorf("notes", "describeOne: request %s failed after %s: %v", prov.name, time.Since(start), err)
		return "", err
	}
	defer res.Body.Close()
	logx.Debugf("notes", "describeOne: provider=%s HTTP %d in %s", prov.name, res.StatusCode, time.Since(start))
	b, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil {
		logx.Errorf("notes", "describeOne: read body provider=%s: %v", prov.name, err)
		return "", fmt.Errorf("%s vision: ответ оборвался: %w", prov.name, err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		logx.Errorf("notes", "describeOne: %s HTTP %d: %s", prov.name, res.StatusCode, truncate(string(b), 300))
		return "", fmt.Errorf("%s HTTP %d: %s", prov.name, res.StatusCode, truncate(string(b), 300))
	}
	var out chatResp
	if err := json.Unmarshal(b, &out); err != nil {
		logx.Errorf("notes", "describeOne: json unmarshal provider=%s (%d bytes): %v", prov.name, len(b), err)
		return "", err
	}
	if len(out.Choices) == 0 {
		logx.Errorf("notes", "describeOne: %s: пустой ответ", prov.name)
		return "", fmt.Errorf("%s: пустой ответ", prov.name)
	}
	text := strings.TrimSpace(out.Choices[0].Message.Content)
	logx.Debugf("notes", "describeOne: ok provider=%s slide=%d chars=%d", prov.name, n, len(text))
	return text, nil
}
