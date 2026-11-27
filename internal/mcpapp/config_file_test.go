package mcpapp

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestConfigFileOverridesMCP: JSON-конфиг перекрывает env, а пустые поля
// файла не затирают значения из окружения.
func TestConfigFileOverridesMCP(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MOODLE_MCP_BASE_URL", "https://env.test/moodle")
	t.Setenv("MOODLE_MCP_STATE_DIR", "~/env-state")
	t.Setenv("MOODLE_MCP_TIMEOUT", "40s")

	path := filepath.Join(home, "mcp.json")
	data := `{"base_url":"https://file.test/moodle/","state_dir":"` + filepath.Join(home, "file-state") + `","timeout":"90s"}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != "https://file.test/moodle" {
		t.Fatalf("base url = %q", cfg.BaseURL)
	}
	if cfg.StateDir != filepath.Join(home, "file-state") {
		t.Fatalf("state dir = %q", cfg.StateDir)
	}
	if cfg.SessionFile != filepath.Join(home, "file-state", SessionFileName) {
		t.Fatalf("session file = %q", cfg.SessionFile)
	}
	if cfg.Timeout != 90*time.Second {
		t.Fatalf("timeout = %s", cfg.Timeout)
	}
}

// TestConfigFileRecordingsDirMCP: recordings_dir из JSON перекрывает env,
// «~» раскрывается, а пустое поле env не затирает.
func TestConfigFileRecordingsDirMCP(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MOODLE_MCP_RECORDINGS_DIR", "~/env-rec")

	path := filepath.Join(home, "mcp.json")
	if err := os.WriteFile(path, []byte(`{"recordings_dir":"  ~/file-rec  "}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RecordingsDir != filepath.Join(home, "file-rec") {
		t.Fatalf("recordings из файла = %q", cfg.RecordingsDir)
	}

	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RecordingsDir != filepath.Join(home, "env-rec") {
		t.Fatalf("пустой файл затёр env: %q", cfg.RecordingsDir)
	}

	if err := os.WriteFile(path, []byte(`{"recordings_dir":"  /var/lib/rec  "}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RecordingsDir != "/var/lib/rec" {
		t.Fatalf("абсолютный recordings = %q", cfg.RecordingsDir)
	}
}

// TestConfigFileBadTimeoutMCP: битый таймаут в файле — ошибка.
func TestConfigFileBadTimeoutMCP(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, "mcp.json")
	if err := os.WriteFile(path, []byte(`{"timeout":"soon"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfigFile(path); err == nil {
		t.Fatal("ожидалась ошибка для timeout=soon")
	}
}
