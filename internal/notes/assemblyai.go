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
	tag := lectureOf(ctx).tag()
	st, err := os.Stat(audioPath)
	if err != nil {
		logx.Errorf("notes", "transcribeAssembly: %s stat %s: %v", tag, audioPath, err)
		return "", err
	}
	if st.Size() < 2048 {
		logx.Errorf("notes", "transcribeAssembly: %s аудио слишком короткое (%d байт)", tag, st.Size())
		return "", fmt.Errorf("аудио слишком короткое (%d байт)", st.Size())
	}
	base := strings.TrimRight(cfg.AssemblyAPIURL, "/")
	if base == "" {
		base = "https://api.assemblyai.com"
	}
	logx.Debugf("notes", "transcribeAssembly: %s start audio=%s size=%d url=%s key=%s", tag, audioPath, st.Size(), base, keyState(cfg.AssemblyAPIKey))
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	start := time.Now()

	f, err := os.Open(audioPath)
	if err != nil {
		logx.Errorf("notes", "transcribeAssembly: %s open %s: %v", tag, audioPath, err)
		return "", err
	}
	defer f.Close()
	var up assemblyTranscript
	upStart := time.Now()
	if err := assemblyDo(ctx, cfg, http.MethodPost, base+"/v2/upload", "application/octet-stream", f, &up); err != nil {
		return "", fmt.Errorf("upload: %w", err)
	}
	logx.Infof("notes", "transcribeAssembly: %s аудио загружено (%d байт) за %s", tag, st.Size(), time.Since(upStart).Round(time.Second))
	if up.UploadURL == "" {
		logx.Errorf("notes", "transcribeAssembly: %s upload без upload_url", tag)
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
		logx.Errorf("notes", "transcribeAssembly: %s transcript без id", tag)
		return "", fmt.Errorf("assemblyai: transcript без id")
	}
	logx.Infof("notes", "transcribeAssembly: %s задача id=%s status=%s, жду расшифровку", tag, job.ID, job.Status)

	pollFails := 0
	polls := 0
	for {
		switch job.Status {
		case "completed":
			text := strings.TrimSpace(job.Text)
			if text == "" {
				logx.Errorf("notes", "transcribeAssembly: %s пустой текст (job %s)", tag, job.ID)
				return "", fmt.Errorf("assemblyai: пустой текст")
			}
			logx.Infof("notes", "transcribeAssembly: %s ok job=%s chars=%d polls=%d за %s", tag, job.ID, len([]rune(text)), polls, time.Since(start).Round(time.Second))
			return text, nil
		case "error":
			logx.Errorf("notes", "transcribeAssembly: %s job %s: %s", tag, job.ID, truncate(job.Error, 400))
			return "", fmt.Errorf("assemblyai: %s", truncate(job.Error, 400))
		}
		select {
		case <-ctx.Done():
			logx.Errorf("notes", "transcribeAssembly: %s job %s не дождались за %s: %v", tag, job.ID, time.Since(start).Round(time.Second), ctx.Err())
			return "", ctx.Err()
		case <-time.After(assemblyPoll):
		}
		// Разовый сбой сети при опросе не должен терять уже оплаченную расшифровку.
		next := job
		polls++
		if err := assemblyDo(ctx, cfg, http.MethodGet, base+"/v2/transcript/"+job.ID, "", nil, &next); err != nil {
			pollFails++
			logx.Warnf("notes", "transcribeAssembly: %s опрос job=%s не удался (%d подряд): %v", tag, job.ID, pollFails, err)
			if pollFails >= 5 {
				return "", fmt.Errorf("poll: %w", err)
			}
			continue
		}
		pollFails = 0
		if next.Status != job.Status {
			logx.Infof("notes", "transcribeAssembly: %s job=%s статус %s→%s (опрос %d, за %s)", tag, job.ID, job.Status, next.Status, polls, time.Since(start).Round(time.Second))
		} else {
			logx.Debugf("notes", "transcribeAssembly: %s job=%s статус %s (опрос %d)", tag, job.ID, next.Status, polls)
		}
		job = next
	}
}

func assemblyDo(ctx context.Context, cfg *config.Config, method, url, ctype string, body io.Reader, out *assemblyTranscript) error {
	tag := lectureOf(ctx).tag()
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		logx.Errorf("notes", "assemblyDo: %s new request: %v", tag, err)
		return err
	}
	req.Header.Set("Authorization", cfg.AssemblyAPIKey)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	cli := &http.Client{Timeout: 15 * time.Minute}
	start := time.Now()
	res, err := cli.Do(req)
	if err != nil {
		logx.Errorf("notes", "assemblyDo: %s %s %s: %v", tag, method, url, err)
		return err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		logx.Errorf("notes", "assemblyDo: %s read body: %v", tag, err)
		return fmt.Errorf("assemblyai: ответ оборвался: %w", err)
	}
	logx.Debugf("notes", "assemblyDo: %s %s %s HTTP %d за %s (%d байт)", tag, method, url, res.StatusCode, time.Since(start).Round(time.Millisecond), len(b))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		logx.Errorf("notes", "assemblyDo: %s %s %s HTTP %d: %s", tag, method, url, res.StatusCode, truncate(string(b), 400))
		return fmt.Errorf("assemblyai HTTP %d: %s", res.StatusCode, truncate(string(b), 400))
	}
	*out = assemblyTranscript{}
	if err := json.Unmarshal(b, out); err != nil {
		logx.Errorf("notes", "assemblyDo: %s json unmarshal (%d bytes): %v", tag, len(b), err)
		return fmt.Errorf("assemblyai json: %w", err)
	}
	return nil
}
