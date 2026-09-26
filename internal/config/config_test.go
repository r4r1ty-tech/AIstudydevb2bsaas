package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func clearEnv(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
	for _, k := range []string{
		"TELEGRAM_BOT_TOKEN", "ADMIN_TELEGRAM_ID", "WEBAPP_PUBLIC_URL",
		"DATABASE_PATH", "GROUP_ID", "GROUP_CODE", "LISTEN_ADDR", "TZ",
		"RECORDINGS_DIR", "CHROME_BIN", "CHROME_USER_DATA_DIR", "BBB_DRY_RUN",
		"LECTURE_PAUSE", "PANEL_PASSWORD", "WHITELIST_EXTRA",
		"LLM_API_KEY", "LLM_API_URL", "LLM_MODEL",
		"DEEPSEEK_API_KEY", "DEEPSEEK_API_URL", "DEEPSEEK_MODEL",
	} {
		t.Setenv(k, "")
	}
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.DBPath != DefaultDBPath {
		t.Errorf("DBPath = %q", c.DBPath)
	}
	if c.GroupID != DefaultGroupID || c.GroupCode != DefaultGroupCode {
		t.Errorf("group = %d/%q", c.GroupID, c.GroupCode)
	}
	if c.Timezone != DefaultTimezone || c.ListenAddr != DefaultListen {
		t.Errorf("tz/listen = %q/%q", c.Timezone, c.ListenAddr)
	}
	if c.RecordingsDir != DefaultRecordings {
		t.Errorf("recordings = %q", c.RecordingsDir)
	}
	if !c.LecturePause {
		t.Error("LecturePause should default true")
	}
	if c.BBBDryRun {
		t.Error("BBBDryRun should default false")
	}
	if len(c.Whitelist) != len(DefaultWhitelist) {
		t.Errorf("whitelist = %v", c.Whitelist)
	}
}

func TestLoadFromEnv(t *testing.T) {
	clearEnv(t)
	t.Setenv("TELEGRAM_BOT_TOKEN", "  123:ABC ")
	t.Setenv("ADMIN_TELEGRAM_ID", "42")
	t.Setenv("WEBAPP_PUBLIC_URL", "https://x.example/")
	t.Setenv("DATABASE_PATH", "/tmp/db.sqlite")
	t.Setenv("GROUP_ID", "777")
	t.Setenv("BBB_DRY_RUN", "1")
	t.Setenv("LECTURE_PAUSE", "0")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.BotToken != "123:ABC" {
		t.Errorf("token = %q", c.BotToken)
	}
	if c.AdminID != 42 || c.GroupID != 777 {
		t.Errorf("ids = %d/%d", c.AdminID, c.GroupID)
	}
	if c.WebAppURL != "https://x.example" {
		t.Errorf("webapp url = %q", c.WebAppURL)
	}
	if c.DBPath != "/tmp/db.sqlite" {
		t.Errorf("db = %q", c.DBPath)
	}
	if !c.BBBDryRun || c.LecturePause {
		t.Errorf("flags = dry:%v pause:%v", c.BBBDryRun, c.LecturePause)
	}
}

func TestWhitelistExtra(t *testing.T) {
	clearEnv(t)
	t.Setenv("WHITELIST_EXTRA", "111, 222")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Whitelist) != len(DefaultWhitelist)+2 {
		t.Fatalf("whitelist = %v", c.Whitelist)
	}
	if !c.IsAllowed(111) || !c.IsAllowed(222) {
		t.Error("extra ids should be allowed")
	}
}

func TestWhitelistExtraInvalid(t *testing.T) {
	clearEnv(t)
	t.Setenv("WHITELIST_EXTRA", "abc")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for bad id")
	}
}

func TestIsAllowedAndAdmin(t *testing.T) {
	c := &Config{AdminID: 2, Whitelist: []int64{1, 2, 3}}
	if !c.IsAllowed(2) || c.IsAllowed(99) {
		t.Errorf("IsAllowed wrong")
	}
	if !c.IsAdmin(2) || c.IsAdmin(1) {
		t.Errorf("IsAdmin wrong")
	}
}

func TestResolveWebAppURL(t *testing.T) {
	var nilCfg *Config
	if nilCfg.ResolveWebAppURL() != "" {
		t.Fatal("nil config must resolve empty")
	}

	c := &Config{WebAppURL: "https://env.example/"}
	if got := c.ResolveWebAppURL(); got != "https://env.example" {
		t.Fatalf("env fallback = %q", got)
	}

	f := filepath.Join(t.TempDir(), "webapp_url")
	if err := os.WriteFile(f, []byte("  https://tunnel.example/ \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.WebAppURLFile = f
	if got := c.ResolveWebAppURL(); got != "https://tunnel.example" {
		t.Fatalf("file should win = %q", got)
	}

	if err := os.WriteFile(f, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := c.ResolveWebAppURL(); got != "https://env.example" {
		t.Fatalf("empty file should fall back = %q", got)
	}

	c.WebAppURLFile = filepath.Join(t.TempDir(), "missing")
	if got := c.ResolveWebAppURL(); got != "https://env.example" {
		t.Fatalf("missing file should fall back = %q", got)
	}
}

func TestRequireBotToken(t *testing.T) {
	if err := (&Config{}).RequireBotToken(); err == nil {
		t.Fatal("empty token must fail")
	}
	if err := (&Config{BotToken: "x"}).RequireBotToken(); err != nil {
		t.Fatalf("token present: %v", err)
	}
}

func TestEnvHelpers(t *testing.T) {
	if got := strEnv("SSAU_X_STR", "fb"); got != "fb" {
		t.Errorf("str fallback = %q", got)
	}
	t.Setenv("SSAU_X_STR", "  v ")
	if got := strEnv("SSAU_X_STR", "fb"); got != "v" {
		t.Errorf("str = %q", got)
	}

	for in, want := range map[string]bool{"1": true, "on": true, "yes": true, "true": true, "0": false, "off": false, "no": false, "false": false} {
		t.Setenv("SSAU_X_BOOL", in)
		if got := boolEnv("SSAU_X_BOOL", true); got != want {
			t.Errorf("boolEnv(%q) = %v", in, got)
		}
	}
	t.Setenv("SSAU_X_BOOL", "")
	if !boolEnv("SSAU_X_BOOL", true) {
		t.Error("empty bool should fallback")
	}
	t.Setenv("SSAU_X_BOOL", "junk")
	if !boolEnv("SSAU_X_BOOL", true) {
		t.Error("junk bool should fallback")
	}

	t.Setenv("SSAU_X_INT", "7")
	if got := int64Env("SSAU_X_INT", 1); got != 7 {
		t.Errorf("int = %d", got)
	}
	t.Setenv("SSAU_X_INT", "x")
	if got := int64Env("SSAU_X_INT", 1); got != 1 {
		t.Errorf("int fallback = %d", got)
	}
}

func TestLoadDotEnv(t *testing.T) {
	dir := t.TempDir()
	body := strings.Join([]string{
		"# comment",
		"SSAU_TEST_DOTENV_A=file-value",
		`SSAU_TEST_DOTENV_B="quoted"`,
		"SSAU_TEST_DOTENV_EXIST=from-file",
		"no_equals_line",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	for _, k := range []string{"SSAU_TEST_DOTENV_A", "SSAU_TEST_DOTENV_B", "SSAU_TEST_DOTENV_EXIST"} {
		os.Unsetenv(k)
		t.Cleanup(func() { os.Unsetenv(k) })
	}
	t.Setenv("SSAU_TEST_DOTENV_EXIST", "from-env")

	loadDotEnv(".env")
	if got := os.Getenv("SSAU_TEST_DOTENV_A"); got != "file-value" {
		t.Errorf("A = %q", got)
	}
	if got := os.Getenv("SSAU_TEST_DOTENV_B"); got != "quoted" {
		t.Errorf("B = %q", got)
	}
	if got := os.Getenv("SSAU_TEST_DOTENV_EXIST"); got != "from-env" {
		t.Errorf("existing env should win, got %q", got)
	}
}

// Prod 26.09: the VDS .env has DEEPSEEK_* (old names), the code read only
// LLM_*, so Summarize always failed with «нет LLM_API_KEY».
func TestLLMFallsBackToDeepSeekNames(t *testing.T) {
	clearEnv(t)
	t.Setenv("DEEPSEEK_API_KEY", "sk-old")
	t.Setenv("DEEPSEEK_API_URL", "https://api.deepseek.com/")
	t.Setenv("DEEPSEEK_MODEL", "deepseek-chat")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.LLMAPIKey != "sk-old" || c.LLMAPIURL != "https://api.deepseek.com" || c.LLMModel != "deepseek-chat" {
		t.Fatalf("fallback: key=%q url=%q model=%q", c.LLMAPIKey, c.LLMAPIURL, c.LLMModel)
	}

	t.Setenv("LLM_API_KEY", "sk-new")
	t.Setenv("LLM_MODEL", "new-model")
	c, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.LLMAPIKey != "sk-new" || c.LLMModel != "new-model" || c.LLMAPIURL != "https://api.deepseek.com" {
		t.Fatalf("new names must win: key=%q model=%q url=%q", c.LLMAPIKey, c.LLMModel, c.LLMAPIURL)
	}
}
