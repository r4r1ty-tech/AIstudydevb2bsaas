package notes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

// assemblyPoll is the pause between status checks; tests shorten it.
var assemblyPoll = 3 * time.Second

type assemblyTranscript struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Text      string `json:"text"`
	Error     string `json:"error"`
	UploadURL string `json:"upload_url"`
}

// transcribeAssembly uploads the audio, starts a transcript job and polls it
// until AssemblyAI reports completed or error.
func transcribeAssembly(ctx context.Context, cfg *config.Config, audioPath string) (string, error) {
	st, err := os.Stat(audioPath)
	if err != nil {
		logx.Errorf("notes", "transcribeAssembly: stat %s: %v", audioPath, err)
		return "", err
	}
	if st.Size() < 2048 {
		logx.Errorf("notes", "transcribeAssembly: аудио слишком короткое (%d байт)", st.Size())
		return "", fmt.Errorf("аудио слишком короткое (%d байт)", st.Size())
	}
	base := strings.TrimRight(cfg.AssemblyAPIURL, "/")
	if base == "" {
		base = "https://api.assemblyai.com"
	}
	logx.Debugf("notes", "transcribeAssembly: start audio=%s size=%d url=%s key_len=%d", audioPath, st.Size(), base, len(cfg.AssemblyAPIKey))
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	start := time.Now()

	f, err := os.Open(audioPath)
	if err != nil {
		logx.Errorf("notes", "transcribeAssembly: open %s: %v", audioPath, err)
		return "", err
	}
	defer f.Close()
	var up assemblyTranscript
	if err := assemblyDo(ctx, cfg, http.MethodPost, base+"/v2/upload", "application/octet-stream", f, &up); err != nil {
		return "", fmt.Errorf("upload: %w", err)
	}
	if up.UploadURL == "" {
		logx.Errorf("notes", "transcribeAssembly: upload без upload_url")
		return "", fmt.Errorf("assemblyai: upload без upload_url")
	}

	body, err := json.Marshal(map[string]string{"audio_url": up.UploadURL, "language_code": "ru"})
	if err != nil {
		return "", err
	}
	var job assemblyTranscript
	if err := assemblyDo(ctx, cfg, http.MethodPost, base+"/v2/transcript", "application/json", bytes.NewReader(body), &job); err != nil {
		return "", fmt.Errorf("transcript: %w", err)
	}
	if job.ID == "" {
		logx.Errorf("notes", "transcribeAssembly: transcript без id")
		return "", fmt.Errorf("assemblyai: transcript без id")
	}
	logx.Debugf("notes", "transcribeAssembly: job id=%s status=%s", job.ID, job.Status)

	pollFails := 0
	for {
		switch job.Status {
		case "completed":
			text := strings.TrimSpace(job.Text)
			if text == "" {
				logx.Errorf("notes", "transcribeAssembly: пустой текст")
				return "", fmt.Errorf("assemblyai: пустой текст")
			}
			logx.Infof("notes", "transcribeAssembly: ok chars=%d за %s", len(text), time.Since(start).Round(time.Second))
			return text, nil
		case "error":
			logx.Errorf("notes", "transcribeAssembly: job %s: %s", job.ID, truncate(job.Error, 400))
			return "", fmt.Errorf("assemblyai: %s", truncate(job.Error, 400))
		}
		select {
		case <-ctx.Done():
			logx.Errorf("notes", "transcribeAssembly: job %s не дождались: %v", job.ID, ctx.Err())
			return "", ctx.Err()
		case <-time.After(assemblyPoll):
		}
		// Разовый сбой сети при опросе не должен терять уже оплаченную расшифровку.
		next := job
		if err := assemblyDo(ctx, cfg, http.MethodGet, base+"/v2/transcript/"+job.ID, "", nil, &next); err != nil {
			pollFails++
			if pollFails >= 5 {
				return "", fmt.Errorf("poll: %w", err)
			}
			continue
		}
		pollFails = 0
		job = next
	}
}

func assemblyDo(ctx context.Context, cfg *config.Config, method, url, ctype string, body io.Reader, out *assemblyTranscript) error {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		logx.Errorf("notes", "assemblyDo: new request: %v", err)
		return err
	}
	req.Header.Set("Authorization", cfg.AssemblyAPIKey)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	cli := &http.Client{Timeout: 15 * time.Minute}
	res, err := cli.Do(req)
	if err != nil {
		logx.Errorf("notes", "assemblyDo: %s %s: %v", method, url, err)
		return err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		logx.Errorf("notes", "assemblyDo: read body: %v", err)
		return fmt.Errorf("assemblyai: ответ оборвался: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		logx.Errorf("notes", "assemblyDo: %s %s HTTP %d: %s", method, url, res.StatusCode, truncate(string(b), 400))
		return fmt.Errorf("assemblyai HTTP %d: %s", res.StatusCode, truncate(string(b), 400))
	}
	*out = assemblyTranscript{}
	if err := json.Unmarshal(b, out); err != nil {
		logx.Errorf("notes", "assemblyDo: json unmarshal (%d bytes): %v", len(b), err)
		return fmt.Errorf("assemblyai json: %w", err)
	}
	return nil
}
