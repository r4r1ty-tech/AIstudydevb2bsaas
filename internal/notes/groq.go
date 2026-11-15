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
	tag := lectureOf(ctx).tag()
	st, err := os.Stat(audioPath)
	if err != nil {
		logx.Errorf("notes", "transcribeGroq: %s stat %s: %v", tag, audioPath, err)
		return "", err
	}
	logx.Debugf("notes", "transcribeGroq: %s start size=%d bytes url=%s key=%s", tag, st.Size(), cfg.GroqAPIURL, keyState(cfg.GroqAPIKey))
	if st.Size() < 2048 {
		logx.Errorf("notes", "transcribeGroq: %s аудио слишком короткое (%d байт)", tag, st.Size())
		return "", fmt.Errorf("аудио слишком короткое (%d байт)", st.Size())
	}
	f, err := os.Open(audioPath)
	if err != nil {
		logx.Errorf("notes", "transcribeGroq: %s open %s: %v", tag, audioPath, err)
		return "", err
	}
	defer f.Close()

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", filepath.Base(audioPath))
	if err != nil {
		logx.Errorf("notes", "transcribeGroq: %s create form file: %v", tag, err)
		return "", err
	}
	if _, err := io.Copy(part, f); err != nil {
		logx.Errorf("notes", "transcribeGroq: %s copy audio: %v", tag, err)
		return "", err
	}
	model := cfg.GroqSTTModel
	if model == "" {
		model = "whisper-large-v3"
	}
	if err := w.WriteField("model", model); err != nil {
		logx.Warnf("notes", "transcribeGroq: %s write model field: %v", tag, err)
	}
	if err := w.WriteField("language", "ru"); err != nil {
		logx.Warnf("notes", "transcribeGroq: %s write language field: %v", tag, err)
	}
	if err := w.WriteField("response_format", "json"); err != nil {
		logx.Warnf("notes", "transcribeGroq: %s write response_format field: %v", tag, err)
	}
	if err := w.Close(); err != nil {
		logx.Errorf("notes", "transcribeGroq: %s close multipart: %v", tag, err)
		return "", err
	}
	logx.Debugf("notes", "transcribeGroq: %s model=%s", tag, model)

	base := strings.TrimRight(cfg.GroqAPIURL, "/")
	if base == "" {
		base = "https://api.groq.com/openai/v1"
	}
	endpoint := base + "/audio/transcriptions"
	logx.Debugf("notes", "transcribeGroq: %s POST %s body=%d bytes", tag, endpoint, body.Len())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		logx.Errorf("notes", "transcribeGroq: %s new request: %v", tag, err)
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.GroqAPIKey)
	req.Header.Set("Content-Type", w.FormDataContentType())

	start := time.Now()
	cli := &http.Client{Timeout: 15 * time.Minute}
	res, err := cli.Do(req)
	if err != nil {
		logx.Errorf("notes", "transcribeGroq: %s request failed after %s: %v", tag, time.Since(start).Round(time.Second), err)
		return "", err
	}
	defer res.Body.Close()
	logx.Debugf("notes", "transcribeGroq: %s HTTP %d in %s", tag, res.StatusCode, time.Since(start).Round(time.Millisecond))
	b, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		logx.Errorf("notes", "transcribeGroq: %s read body: %v", tag, err)
		return "", fmt.Errorf("groq stt: ответ оборвался: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		logx.Errorf("notes", "transcribeGroq: %s HTTP %d: %s", tag, res.StatusCode, truncate(string(b), 400))
		return "", fmt.Errorf("groq whisper HTTP %d: %s", res.StatusCode, truncate(string(b), 400))
	}
	var out asrResp
	if err := json.Unmarshal(b, &out); err != nil {
		logx.Errorf("notes", "transcribeGroq: %s json unmarshal (%d bytes): %v", tag, len(b), err)
		return "", fmt.Errorf("groq whisper json: %w", err)
	}
	text := strings.TrimSpace(out.Text)
	if text == "" {
		logx.Errorf("notes", "transcribeGroq: %s пустой текст", tag)
		return "", fmt.Errorf("groq whisper: пустой текст")
	}
	logx.Debugf("notes", "transcribeGroq: %s ok chars=%d", tag, len([]rune(text)))
	return text, nil
}
