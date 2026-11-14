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
	if ctx == nil {
		ctx = context.Background()
	}
	tag := lectureOf(ctx).tag()
	if cfg == nil {
		logx.Errorf("notes", "Transcribe: %s нет конфига", tag)
		return "", fmt.Errorf("нет конфига")
	}
	type provider struct {
		name string
		key  string
		fn   func(context.Context, *config.Config, string) (string, error)
	}
	// AssemblyAI первым; Fish и Groq — запасные, если у него ошибка или нет ключа.
	providers := []provider{
		{"assemblyai", cfg.AssemblyAPIKey, transcribeAssembly},
		{"fish", cfg.FishStudioAPIKey, transcribeFish},
		{"groq", cfg.GroqAPIKey, transcribeGroq},
	}
	var fails []string
	started := time.Now()
	for _, p := range providers {
		if strings.TrimSpace(p.key) == "" {
			logx.Debugf("notes", "Transcribe: %s provider=%s пропускаю: ключа нет", tag, p.name)
			continue
		}
		logx.Infof("notes", "Transcribe: %s пробую provider=%s audio=%s", tag, p.name, audioPath)
		start := time.Now()
		t, err := p.fn(ctx, cfg, audioPath)
		if err == nil {
			logx.Infof("notes", "Transcribe: %s ok provider=%s chars=%d за %s", tag, p.name, len([]rune(t)), time.Since(start).Round(time.Second))
			return t, nil
		}
		logx.Warnf("notes", "Transcribe: %s provider=%s не сработал за %s: %v", tag, p.name, time.Since(start).Round(time.Second), err)
		fails = append(fails, p.name+": "+err.Error())
	}
	if len(fails) > 0 {
		logx.Errorf("notes", "Transcribe: %s все провайдеры STT не сработали за %s: %s", tag, time.Since(started).Round(time.Second), strings.Join(fails, "; "))
		return "", fmt.Errorf("%s", strings.Join(fails, "; "))
	}
	logx.Errorf("notes", "Transcribe: %s нет ключа STT (AssemblyAI / Fish / Groq)", tag)
	return "", fmt.Errorf("нет ключа STT (AssemblyAI / Fish / Groq)")
}

func transcribeFish(ctx context.Context, cfg *config.Config, audioPath string) (string, error) {
	tag := lectureOf(ctx).tag()
	st, err := os.Stat(audioPath)
	if err != nil {
		logx.Errorf("notes", "transcribeFish: %s stat %s: %v", tag, audioPath, err)
		return "", err
	}
	logx.Debugf("notes", "transcribeFish: %s start size=%d bytes url=%s key=%s", tag, st.Size(), cfg.FishStudioAPIURL, keyState(cfg.FishStudioAPIKey))
	if st.Size() < 2048 {
		logx.Errorf("notes", "transcribeFish: %s аудио слишком короткое (%d байт)", tag, st.Size())
		return "", fmt.Errorf("аудио слишком короткое (%d байт)", st.Size())
	}
	f, err := os.Open(audioPath)
	if err != nil {
		logx.Errorf("notes", "transcribeFish: %s open %s: %v", tag, audioPath, err)
		return "", err
	}
	defer f.Close()

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("audio", "audio.ogg")
	if err != nil {
		logx.Errorf("notes", "transcribeFish: %s create form file: %v", tag, err)
		return "", err
	}
	if _, err := io.Copy(part, f); err != nil {
		logx.Errorf("notes", "transcribeFish: %s copy audio: %v", tag, err)
		return "", err
	}
	if err := w.WriteField("language", "ru"); err != nil {
		logx.Warnf("notes", "transcribeFish: %s write language field: %v", tag, err)
	}
	if err := w.WriteField("ignore_timestamps", "true"); err != nil {
		logx.Warnf("notes", "transcribeFish: %s write ignore_timestamps field: %v", tag, err)
	}
	if err := w.Close(); err != nil {
		logx.Errorf("notes", "transcribeFish: %s close multipart: %v", tag, err)
		return "", err
	}

	base := strings.TrimRight(cfg.FishStudioAPIURL, "/")
	if base == "" {
		base = "https://api.fish.audio"
	}
	endpoint := base + "/v1/asr"
	logx.Debugf("notes", "transcribeFish: %s POST %s body=%d bytes", tag, endpoint, body.Len())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		logx.Errorf("notes", "transcribeFish: %s new request: %v", tag, err)
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.FishStudioAPIKey)
	req.Header.Set("Content-Type", w.FormDataContentType())

	start := time.Now()
	cli := &http.Client{Timeout: 15 * time.Minute}
	res, err := cli.Do(req)
	if err != nil {
		logx.Errorf("notes", "transcribeFish: %s request failed after %s: %v", tag, time.Since(start).Round(time.Second), err)
		return "", err
	}
	defer res.Body.Close()
	logx.Debugf("notes", "transcribeFish: %s HTTP %d in %s", tag, res.StatusCode, time.Since(start).Round(time.Millisecond))
	b, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		logx.Errorf("notes", "transcribeFish: %s read body: %v", tag, err)
		return "", fmt.Errorf("fish asr: ответ оборвался: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		logx.Errorf("notes", "transcribeFish: %s HTTP %d: %s", tag, res.StatusCode, truncate(string(b), 400))
		return "", fmt.Errorf("fish asr HTTP %d: %s", res.StatusCode, truncate(string(b), 400))
	}
	var out asrResp
	if err := json.Unmarshal(b, &out); err != nil {
		logx.Errorf("notes", "transcribeFish: %s json unmarshal (%d bytes): %v", tag, len(b), err)
		return "", fmt.Errorf("fish asr json: %w", err)
	}
	text := strings.TrimSpace(out.Text)
	if text == "" {
		logx.Errorf("notes", "transcribeFish: %s пустой текст", tag)
		return "", fmt.Errorf("fish asr: пустой текст")
	}
	logx.Debugf("notes", "transcribeFish: %s ok chars=%d", tag, len([]rune(text)))
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
