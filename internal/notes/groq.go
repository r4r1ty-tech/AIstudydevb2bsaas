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
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

func transcribeGroq(ctx context.Context, cfg *config.Config, audioPath string) (string, error) {
	logx.Debugf("notes", "transcribeGroq: start audio=%s url=%s key_len=%d", audioPath, cfg.GroqAPIURL, len(cfg.GroqAPIKey))
	st, err := os.Stat(audioPath)
	if err != nil {
		logx.Errorf("notes", "transcribeGroq: stat %s: %v", audioPath, err)
		return "", err
	}
	logx.Debugf("notes", "transcribeGroq: audio size=%d bytes", st.Size())
	if st.Size() < 2048 {
		logx.Errorf("notes", "transcribeGroq: аудио слишком короткое (%d байт)", st.Size())
		return "", fmt.Errorf("аудио слишком короткое (%d байт)", st.Size())
	}
	f, err := os.Open(audioPath)
	if err != nil {
		logx.Errorf("notes", "transcribeGroq: open %s: %v", audioPath, err)
		return "", err
	}
	defer f.Close()

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", filepath.Base(audioPath))
	if err != nil {
		logx.Errorf("notes", "transcribeGroq: create form file: %v", err)
		return "", err
	}
	if _, err := io.Copy(part, f); err != nil {
		logx.Errorf("notes", "transcribeGroq: copy audio: %v", err)
		return "", err
	}
	model := cfg.GroqSTTModel
	if model == "" {
		model = "whisper-large-v3"
	}
	if err := w.WriteField("model", model); err != nil {
		logx.Warnf("notes", "transcribeGroq: write model field: %v", err)
	}
	if err := w.WriteField("language", "ru"); err != nil {
		logx.Warnf("notes", "transcribeGroq: write language field: %v", err)
	}
	if err := w.WriteField("response_format", "json"); err != nil {
		logx.Warnf("notes", "transcribeGroq: write response_format field: %v", err)
	}
	if err := w.Close(); err != nil {
		logx.Errorf("notes", "transcribeGroq: close multipart: %v", err)
		return "", err
	}
	logx.Debugf("notes", "transcribeGroq: model=%s", model)

	base := strings.TrimRight(cfg.GroqAPIURL, "/")
	if base == "" {
		base = "https://api.groq.com/openai/v1"
	}
	endpoint := base + "/audio/transcriptions"
	logx.Debugf("notes", "transcribeGroq: POST %s body=%d bytes", endpoint, body.Len())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		logx.Errorf("notes", "transcribeGroq: new request: %v", err)
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.GroqAPIKey)
	req.Header.Set("Content-Type", w.FormDataContentType())

	start := time.Now()
	cli := &http.Client{Timeout: 15 * time.Minute}
	res, err := cli.Do(req)
	if err != nil {
		logx.Errorf("notes", "transcribeGroq: request failed after %s: %v", time.Since(start), err)
		return "", err
	}
	defer res.Body.Close()
	logx.Debugf("notes", "transcribeGroq: HTTP %d in %s", res.StatusCode, time.Since(start))
	b, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		logx.Errorf("notes", "transcribeGroq: read body: %v", err)
		return "", fmt.Errorf("groq stt: ответ оборвался: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		logx.Errorf("notes", "transcribeGroq: HTTP %d: %s", res.StatusCode, truncate(string(b), 400))
		return "", fmt.Errorf("groq whisper HTTP %d: %s", res.StatusCode, truncate(string(b), 400))
	}
	var out asrResp
	if err := json.Unmarshal(b, &out); err != nil {
		logx.Errorf("notes", "transcribeGroq: json unmarshal (%d bytes): %v", len(b), err)
		return "", fmt.Errorf("groq whisper json: %w", err)
	}
	text := strings.TrimSpace(out.Text)
	if text == "" {
		logx.Errorf("notes", "transcribeGroq: пустой текст")
		return "", fmt.Errorf("groq whisper: пустой текст")
	}
	logx.Debugf("notes", "transcribeGroq: ok chars=%d", len(text))
	return text, nil
}
