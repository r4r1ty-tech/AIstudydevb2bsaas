package notes

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/archive"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

func ffmpegAudio(t *testing.T, path, source string) {
	t.Helper()
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	out, err := exec.Command(bin, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", source,
		"-t", "70", "-ac", "1", "-c:a", "libopus", "-b:a", "16k", "-y", path).CombinedOutput()
	if err != nil {
		t.Skipf("ffmpeg не собрал тестовое аудио: %v: %s", err, out)
	}
}

// Тишина не должна уходить в STT и LLM: 14.09 Whisper ответил на неё
// «Продолжение следует...», и по этой фразе собирался конспект.
func TestBuildSilentAudioIsEmpty(t *testing.T) {
	hit := false
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		_, _ = w.Write([]byte(`{"text":"Продолжение следует..."}`))
	}))
	t.Cleanup(api.Close)

	root := t.TempDir()
	p := model.LecturePack{Discipline: "Сети", Number: 1, Dir: "pack"}
	dir := filepath.Join(root, p.Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ffmpegAudio(t, archive.AudioFile(dir), "anullsrc=r=48000:cl=mono")
	cfg := &config.Config{
		FishStudioAPIKey: "k", FishStudioAPIURL: api.URL,
		LLMAPIKey: "k", LLMAPIURL: api.URL + "/v1", LLMModel: "m",
	}
	err := Build(context.Background(), cfg, root, p)
	if !errors.Is(err, ErrEmptyAudio) {
		t.Fatalf("want ErrEmptyAudio, got %v", err)
	}
	if hit {
		t.Fatal("тишина ушла во внешний сервис")
	}
}

// Звук есть, а расшифровка — одна фраза на 70 секунд: это не лекция.
func TestBuildThinTranscriptIsEmpty(t *testing.T) {
	llm := false
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/asr" {
			_, _ = w.Write([]byte(`{"text":"Продолжение следует..."}`))
			return
		}
		llm = true
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"выдумка"}}]}`))
	}))
	t.Cleanup(api.Close)

	root := t.TempDir()
	p := model.LecturePack{Discipline: "Сети", Number: 1, Dir: "pack"}
	dir := filepath.Join(root, p.Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ffmpegAudio(t, archive.AudioFile(dir), "sine=frequency=440:sample_rate=48000")
	cfg := &config.Config{
		FishStudioAPIKey: "k", FishStudioAPIURL: api.URL,
		LLMAPIKey: "k", LLMAPIURL: api.URL + "/v1", LLMModel: "m",
	}
	err := Build(context.Background(), cfg, root, p)
	if !errors.Is(err, ErrEmptyAudio) {
		t.Fatalf("want ErrEmptyAudio, got %v", err)
	}
	if llm {
		t.Fatal("по пустой расшифровке позвали LLM")
	}
}

func TestThinTranscript(t *testing.T) {
	if got := thinTranscript("Продолжение следует...", 90*time.Minute); got == "" {
		t.Fatal("фраза на 90 минут должна отбрасываться")
	}
	long := make([]rune, 30000)
	for i := range long {
		long[i] = 'а'
	}
	if got := thinTranscript(string(long), 25*time.Minute); got != "" {
		t.Fatalf("обычная расшифровка отброшена: %s", got)
	}
	if got := thinTranscript("коротко", 0); got != "" {
		t.Fatalf("без длительности не судим: %s", got)
	}
}
