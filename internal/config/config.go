package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

// Seed whitelist from PLAN.md. Live on/off lives in sqlite, not here.
var DefaultWhitelist = []int64{
	1074442235, // admin, Mini App
	2047776316,
	808176356,
	1523691310,
	1098241114,
}

const (
	DefaultAdminID    int64 = 1074442235
	DefaultGroupID    int64 = 531023229
	DefaultGroupCode        = "6301-090301D"
	DefaultTimezone         = "Europe/Samara"
	DefaultListen           = ":8080"
	DefaultDBPath           = "data/bot.db"
	DefaultRecordings       = "recordings"
)

type Config struct {
	BotToken         string
	AdminID          int64
	WebAppURL        string
	WebAppURLFile    string
	DBPath           string
	GroupID          int64
	GroupCode        string
	ListenAddr       string
	Timezone         string
	RecordingsDir    string
	ChromeBin        string
	ChromeUserDir    string
	ProxyFile        string
	BBBDryRun        bool
	LecturePause     bool
	PanelPassword    string
	LLMAPIKey        string
	LLMAPIURL        string
	LLMModel         string
	FishStudioAPIKey string
	FishStudioAPIURL string
	GrokAPIKey       string
	GrokAPIURL       string
	GrokModel        string
	GroqAPIKey       string
	GroqAPIURL       string
	GroqVisionModel  string
	GroqSTTModel     string
	VoskModel        string
	VoskScript       string
	GitHubToken      string
	GitHubOwner      string
	GitHubRepo       string
	GitHubBranch     string
	Whitelist        []int64
}

func Load() (*Config, error) {
	logx.Debugf("config", "Load: enter env_file=.env")
	loadDotEnv(".env")

	c := &Config{
		BotToken:         strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")),
		AdminID:          int64Env("ADMIN_TELEGRAM_ID", DefaultAdminID),
		WebAppURL:        strings.TrimRight(strings.TrimSpace(os.Getenv("WEBAPP_PUBLIC_URL")), "/"),
		WebAppURLFile:    strings.TrimSpace(os.Getenv("WEBAPP_URL_FILE")),
		DBPath:           strEnv("DATABASE_PATH", DefaultDBPath),
		GroupID:          int64Env("GROUP_ID", DefaultGroupID),
		GroupCode:        strEnv("GROUP_CODE", DefaultGroupCode),
		ListenAddr:       strEnv("LISTEN_ADDR", DefaultListen),
		Timezone:         strEnv("TZ", DefaultTimezone),
		RecordingsDir:    strEnv("RECORDINGS_DIR", DefaultRecordings),
		ChromeBin:        strEnv("CHROME_BIN", ""),
		ChromeUserDir:    strEnv("CHROME_USER_DATA_DIR", ""),
		ProxyFile:        strEnv("PROXY_FILE", "/opt/ssau-bot/proxies.txt"),
		BBBDryRun:        os.Getenv("BBB_DRY_RUN") == "1",
		LecturePause:     boolEnv("LECTURE_PAUSE", true),
		PanelPassword:    strEnv("PANEL_PASSWORD", ""),
		LLMAPIKey:        strEnv("LLM_API_KEY", ""),
		LLMAPIURL:        strings.TrimRight(strEnv("LLM_API_URL", ""), "/"),
		LLMModel:         strEnv("LLM_MODEL", ""),
		FishStudioAPIKey: strEnv("FISH_STUDIO_API_KEY", ""),
		FishStudioAPIURL: strEnv("FISH_STUDIO_API_URL", "https://api.fish.audio"),
		GrokAPIKey:       strEnv("GROK_API_KEY", ""),
		GrokAPIURL:       strEnv("GROK_API_URL", "https://api.x.ai/v1"),
		GrokModel:        strEnv("GROK_MODEL", "grok-2-vision-1212"),
		GroqAPIKey:       strEnv("GROQ_API_KEY", ""),
		GroqAPIURL:       strEnv("GROQ_API_URL", "https://api.groq.com/openai/v1"),
		GroqVisionModel:  strEnv("GROQ_VISION_MODEL", "qwen/qwen3.6-27b"),
		GroqSTTModel:     strEnv("GROQ_STT_MODEL", "whisper-large-v3"),
		VoskModel:        strEnv("VOSK_MODEL", "/opt/ssau-bot/vosk-model"),
		VoskScript:       strEnv("VOSK_SCRIPT", "/opt/ssau-bot/wake.py"),
		GitHubToken:      strings.TrimSpace(os.Getenv("GITHUB_TOKEN")),
		GitHubOwner:      strEnv("GITHUB_OWNER", "r4r1ty-tech"),
		GitHubRepo:       strEnv("GITHUB_REPO", "LectionsSSAU"),
		GitHubBranch:     strEnv("GITHUB_BRANCH", "main"),
		Whitelist:        append([]int64(nil), DefaultWhitelist...),
	}
	if extra := strings.TrimSpace(os.Getenv("WHITELIST_EXTRA")); extra != "" {
		for _, p := range strings.Split(extra, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			id, err := strconv.ParseInt(p, 10, 64)
			if err != nil {
				logx.Errorf("config", "Load: WHITELIST_EXTRA %q: %v", p, err)
				return nil, fmt.Errorf("WHITELIST_EXTRA: %q: %w", p, err)
			}
			c.Whitelist = append(c.Whitelist, id)
		}
	}
	logx.Debugf("config", "Load: whitelist=%d bot_token_present=%v admin=%d group=%d db=%s webapp_url=%q webapp_url_file=%q listen=%s tz=%s recordings=%s",
		len(c.Whitelist), strings.TrimSpace(c.BotToken) != "", c.AdminID, c.GroupID,
		c.DBPath, c.WebAppURL, c.WebAppURLFile, c.ListenAddr, c.Timezone, c.RecordingsDir)
	logx.Debugf("config", "Load: key_lengths llm=%d grok=%d groq=%d fish=%d github=%d panel=%d",
		len(c.LLMAPIKey), len(c.GrokAPIKey), len(c.GroqAPIKey), len(c.FishStudioAPIKey), len(c.GitHubToken), len(c.PanelPassword))
	logx.Infof("config", "Load: ok whitelist=%d group=%d db=%s", len(c.Whitelist), c.GroupID, c.DBPath)
	return c, nil
}

func (c *Config) RequireBotToken() error {
	logx.Debugf("config", "RequireBotToken: enter")
	if c == nil || strings.TrimSpace(c.BotToken) == "" {
		logx.Errorf("config", "RequireBotToken: TELEGRAM_BOT_TOKEN пустой")
		return fmt.Errorf("TELEGRAM_BOT_TOKEN пустой")
	}
	logx.Debugf("config", "RequireBotToken: token_present=true len=%d", len(strings.TrimSpace(c.BotToken)))
	return nil
}

func (c *Config) IsAllowed(id int64) bool {
	logx.Debugf("config", "IsAllowed: enter id=%d", id)
	for _, w := range c.Whitelist {
		if w == id {
			logx.Debugf("config", "IsAllowed: id=%d allowed=true", id)
			return true
		}
	}
	logx.Debugf("config", "IsAllowed: id=%d allowed=false", id)
	return false
}

func (c *Config) IsAdmin(id int64) bool {
	logx.Debugf("config", "IsAdmin: enter id=%d admin_id=%d", id, c.AdminID)
	ok := id == c.AdminID
	logx.Debugf("config", "IsAdmin: id=%d admin=%v", id, ok)
	return ok
}

func (c *Config) ResolveWebAppURL() string {
	logx.Debugf("config", "ResolveWebAppURL: enter")
	if c == nil {
		logx.Debugf("config", "ResolveWebAppURL: nil config")
		return ""
	}
	if f := strings.TrimSpace(c.WebAppURLFile); f != "" {
		logx.Debugf("config", "ResolveWebAppURL: trying file=%s", f)
		if b, err := os.ReadFile(f); err == nil {
			if u := strings.TrimRight(strings.TrimSpace(string(b)), "/"); u != "" {
				logx.Debugf("config", "ResolveWebAppURL: resolved from file=%s url=%q", f, u)
				return u
			}
			logx.Debugf("config", "ResolveWebAppURL: file=%s empty", f)
		} else {
			logx.Warnf("config", "ResolveWebAppURL: read %s: %v", f, err)
		}
	}
	u := strings.TrimRight(strings.TrimSpace(c.WebAppURL), "/")
	logx.Debugf("config", "ResolveWebAppURL: resolved from env url=%q", u)
	return u
}

func strEnv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		logx.Debugf("config", "strEnv: key=%s set=true", key)
		return v
	}
	logx.Debugf("config", "strEnv: key=%s set=false fallback_used=true", key)
	return fallback
}

func boolEnv(key string, fallback bool) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	switch v {
	case "":
		logx.Debugf("config", "boolEnv: key=%s empty fallback=%v", key, fallback)
		return fallback
	case "0", "false", "no", "off":
		logx.Debugf("config", "boolEnv: key=%s value=%q result=false", key, v)
		return false
	case "1", "true", "yes", "on":
		logx.Debugf("config", "boolEnv: key=%s value=%q result=true", key, v)
		return true
	default:
		logx.Warnf("config", "boolEnv: key=%s invalid=%q fallback=%v", key, v, fallback)
		return fallback
	}
}

func int64Env(key string, fallback int64) int64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		logx.Debugf("config", "int64Env: key=%s empty fallback=%d", key, fallback)
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		logx.Warnf("config", "int64Env: key=%s parse %q: %v", key, v, err)
		return fallback
	}
	logx.Debugf("config", "int64Env: key=%s value=%d", key, n)
	return n
}

// loadDotEnv fills os.Getenv gaps from a file. Existing env wins.
func loadDotEnv(path string) {
	logx.Debugf("config", "loadDotEnv: open %s", path)
	f, err := os.Open(path)
	if err != nil {
		logx.Debugf("config", "loadDotEnv: %s not loaded: %v", path, err)
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	loaded, skipped := 0, 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		v = strings.Trim(v, `"'`)
		if k == "" {
			continue
		}
		if _, exists := os.LookupEnv(k); exists {
			skipped++
			continue
		}
		if err := os.Setenv(k, v); err != nil {
			logx.Warnf("config", "loadDotEnv: set %s: %v", k, err)
			continue
		}
		loaded++
	}
	if err := sc.Err(); err != nil {
		logx.Warnf("config", "loadDotEnv: scan %s: %v", path, err)
	}
	logx.Debugf("config", "loadDotEnv: %s loaded=%d skipped_existing=%d", path, loaded, skipped)
}
