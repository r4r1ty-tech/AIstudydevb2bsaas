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
	"strings"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
)

type asrResp struct {
	Text string `json:"text"`
}

func Transcribe(ctx context.Context, cfg *config.Config, audioPath string) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("нет конфига")
	}
	var last error
	if strings.TrimSpace(cfg.FishStudioAPIKey) != "" {
		t, err := transcribeFish(ctx, cfg, audioPath)
		if err == nil {
			return t, nil
		}
		last = fmt.Errorf("fish: %w", err)
	}
	if strings.TrimSpace(cfg.GroqAPIKey) != "" {
		t, err := transcribeGroq(ctx, cfg, audioPath)
		if err == nil {
			return t, nil
		}
		if last != nil {
			return "", fmt.Errorf("%v; groq: %w", last, err)
		}
		return "", err
	}
	if last != nil {
		return "", last
	}
	return "", fmt.Errorf("нет ключа STT (Fish / Groq)")
}

func transcribeFish(ctx context.Context, cfg *config.Config, audioPath string) (string, error) {
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
	part, err := w.CreateFormFile("audio", "audio.ogg")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(part, f); err != nil {
		return "", err
	}
	_ = w.WriteField("language", "ru")
	_ = w.WriteField("ignore_timestamps", "true")
	if err := w.Close(); err != nil {
		return "", err
	}

	base := strings.TrimRight(cfg.FishStudioAPIURL, "/")
	if base == "" {
		base = "https://api.fish.audio"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/asr", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.FishStudioAPIKey)
	req.Header.Set("Content-Type", w.FormDataContentType())

	cli := &http.Client{Timeout: 15 * time.Minute}
	res, err := cli.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("fish asr HTTP %d: %s", res.StatusCode, truncate(string(b), 400))
	}
	var out asrResp
	if err := json.Unmarshal(b, &out); err != nil {
		return "", fmt.Errorf("fish asr json: %w", err)
	}
	text := strings.TrimSpace(out.Text)
	if text == "" {
		return "", fmt.Errorf("fish asr: пустой текст")
	}
	return text, nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
