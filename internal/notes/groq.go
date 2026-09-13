package notes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
)

func transcribeGroq(ctx context.Context, cfg *config.Config, audioPath string) (string, error) {
	st, err := os.Stat(audioPath)
	if err != nil {
		return "", err
	}
	if st.Size() < 2048 {
		return "", fmt.Errorf("аудио слишком короткое (%d байт)", st.Size())
	}
	f, err := os.Open(audioPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", filepath.Base(audioPath))
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(part, f); err != nil {
		return "", err
	}
	model := cfg.GroqSTTModel
	if model == "" {
		model = "whisper-large-v3"
	}
	_ = w.WriteField("model", model)
	_ = w.WriteField("language", "ru")
	_ = w.WriteField("response_format", "json")
	if err := w.Close(); err != nil {
		return "", err
	}

	base := strings.TrimRight(cfg.GroqAPIURL, "/")
	if base == "" {
		base = "https://api.groq.com/openai/v1"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/audio/transcriptions", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.GroqAPIKey)
	req.Header.Set("Content-Type", w.FormDataContentType())

	cli := &http.Client{Timeout: 15 * time.Minute}
	res, err := cli.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("groq whisper HTTP %d: %s", res.StatusCode, truncate(string(b), 400))
	}
	var out asrResp
	if err := json.Unmarshal(b, &out); err != nil {
		return "", fmt.Errorf("groq whisper json: %w", err)
	}
	text := strings.TrimSpace(out.Text)
	if text == "" {
		return "", fmt.Errorf("groq whisper: пустой текст")
	}
	return text, nil
}
