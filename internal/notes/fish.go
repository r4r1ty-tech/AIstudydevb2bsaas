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
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

type asrResp struct {
	Text string `json:"text"`
}

func Transcribe(ctx context.Context, cfg *config.Config, audioPath string) (string, error) {
	logx.Debugf("notes", "Transcribe: start audio=%s cfg_nil=%t", audioPath, cfg == nil)
	if cfg == nil {
		logx.Errorf("notes", "Transcribe: нет конфига")
		return "", fmt.Errorf("нет конфига")
	}
	var last error
	fishKey := strings.TrimSpace(cfg.FishStudioAPIKey)
	groqKey := strings.TrimSpace(cfg.GroqAPIKey)
	logx.Debugf("notes", "Transcribe: fish_key=%t groq_key=%t fish_url=%s groq_url=%s", fishKey != "", groqKey != "", cfg.FishStudioAPIURL, cfg.GroqAPIURL)
	if fishKey != "" {
		logx.Debugf("notes", "Transcribe: try fish")
		t, err := transcribeFish(ctx, cfg, audioPath)
		if err == nil {
			logx.Debugf("notes", "Transcribe: fish ok chars=%d", len(t))
			return t, nil
		}
		logx.Warnf("notes", "Transcribe: fish failed: %v", err)
		last = fmt.Errorf("fish: %w", err)
	}
	if groqKey != "" {
		logx.Debugf("notes", "Transcribe: try groq")
		t, err := transcribeGroq(ctx, cfg, audioPath)
		if err == nil {
			logx.Debugf("notes", "Transcribe: groq ok chars=%d", len(t))
			return t, nil
		}
		logx.Errorf("notes", "Transcribe: groq failed: %v", err)
		if last != nil {
			return "", fmt.Errorf("%v; groq: %w", last, err)
		}
		return "", err
	}
	if last != nil {
		logx.Errorf("notes", "Transcribe: все провайдеры STT не сработали: %v", last)
		return "", last
	}
	logx.Errorf("notes", "Transcribe: нет ключа STT (Fish / Groq)")
	return "", fmt.Errorf("нет ключа STT (Fish / Groq)")
}

func transcribeFish(ctx context.Context, cfg *config.Config, audioPath string) (string, error) {
	logx.Debugf("notes", "transcribeFish: start audio=%s url=%s key_len=%d", audioPath, cfg.FishStudioAPIURL, len(cfg.FishStudioAPIKey))
	st, err := os.Stat(audioPath)
	if err != nil {
		logx.Errorf("notes", "transcribeFish: stat %s: %v", audioPath, err)
		return "", err
	}
	logx.Debugf("notes", "transcribeFish: audio size=%d bytes", st.Size())
	if st.Size() < 2048 {
		logx.Errorf("notes", "transcribeFish: аудио слишком короткое (%d байт)", st.Size())
		return "", fmt.Errorf("аудио слишком короткое (%d байт)", st.Size())
	}
	f, err := os.Open(audioPath)
	if err != nil {
		logx.Errorf("notes", "transcribeFish: open %s: %v", audioPath, err)
		return "", err
	}
	defer f.Close()

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("audio", "audio.ogg")
	if err != nil {
		logx.Errorf("notes", "transcribeFish: create form file: %v", err)
		return "", err
	}
	if _, err := io.Copy(part, f); err != nil {
		logx.Errorf("notes", "transcribeFish: copy audio: %v", err)
		return "", err
	}
	if err := w.WriteField("language", "ru"); err != nil {
		logx.Warnf("notes", "transcribeFish: write language field: %v", err)
	}
	if err := w.WriteField("ignore_timestamps", "true"); err != nil {
		logx.Warnf("notes", "transcribeFish: write ignore_timestamps field: %v", err)
	}
	if err := w.Close(); err != nil {
		logx.Errorf("notes", "transcribeFish: close multipart: %v", err)
		return "", err
	}

	base := strings.TrimRight(cfg.FishStudioAPIURL, "/")
	if base == "" {
		base = "https://api.fish.audio"
	}
	endpoint := base + "/v1/asr"
	logx.Debugf("notes", "transcribeFish: POST %s body=%d bytes", endpoint, body.Len())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		logx.Errorf("notes", "transcribeFish: new request: %v", err)
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.FishStudioAPIKey)
	req.Header.Set("Content-Type", w.FormDataContentType())

	start := time.Now()
	cli := &http.Client{Timeout: 15 * time.Minute}
	res, err := cli.Do(req)
	if err != nil {
		logx.Errorf("notes", "transcribeFish: request failed after %s: %v", time.Since(start), err)
		return "", err
	}
	defer res.Body.Close()
	logx.Debugf("notes", "transcribeFish: HTTP %d in %s", res.StatusCode, time.Since(start))
	b, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		logx.Errorf("notes", "transcribeFish: read body: %v", err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		logx.Errorf("notes", "transcribeFish: HTTP %d: %s", res.StatusCode, truncate(string(b), 400))
		return "", fmt.Errorf("fish asr HTTP %d: %s", res.StatusCode, truncate(string(b), 400))
	}
	var out asrResp
	if err := json.Unmarshal(b, &out); err != nil {
		logx.Errorf("notes", "transcribeFish: json unmarshal (%d bytes): %v", len(b), err)
		return "", fmt.Errorf("fish asr json: %w", err)
	}
	text := strings.TrimSpace(out.Text)
	if text == "" {
		logx.Errorf("notes", "transcribeFish: пустой текст")
		return "", fmt.Errorf("fish asr: пустой текст")
	}
	logx.Debugf("notes", "transcribeFish: ok chars=%d", len(text))
	return text, nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		logx.Debugf("notes", "truncate: len=%d max=%d unchanged", len(s), n)
		return s
	}
	logx.Debugf("notes", "truncate: len=%d max=%d cut", len(s), n)
	return s[:n] + "…"
}
