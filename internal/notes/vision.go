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
	return out
}

func DescribeSlides(ctx context.Context, cfg *config.Config, slidesDir string) (string, error) {
	provs := visionProviders(cfg)
	if len(provs) == 0 {
		return "", nil
	}
	ents, err := os.ReadDir(slidesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
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
		files = files[:40]
	}
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
			last = err
		}
		if last != nil {
			fmt.Fprintf(&b, "Слайд %d: ошибка (%v)\n", i+1, last)
			continue
		}
		fmt.Fprintf(&b, "Слайд %d:\n%s\n\n", i+1, text)
	}
	return strings.TrimSpace(b.String()), nil
}

func describeOne(ctx context.Context, prov visionProvider, path string, n int) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(raw) < 32 {
		return "", fmt.Errorf("пустой файл")
	}
	mime := "image/png"
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		mime = "image/jpeg"
	case ".webp":
		mime = "image/webp"
	}
	dataURL := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw)
	payload, err := json.Marshal(visionReq{
		Model: prov.model,
		Messages: []visionMsg{{
			Role: "user",
			Content: []any{
				map[string]any{"type": "text", "text": fmt.Sprintf("Это слайд %d университетской лекции. Выпиши весь текст, формулы и смысл схемы. По-русски, без вступлений.", n)},
				map[string]any{"type": "image_url", "image_url": map[string]string{"url": dataURL}},
			},
		}},
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, prov.base+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+prov.key)
	req.Header.Set("Content-Type", "application/json")
	cli := &http.Client{Timeout: 2 * time.Minute}
	res, err := cli.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("%s HTTP %d: %s", prov.name, res.StatusCode, truncate(string(b), 300))
	}
	var out chatResp
	if err := json.Unmarshal(b, &out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("%s: пустой ответ", prov.name)
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}
