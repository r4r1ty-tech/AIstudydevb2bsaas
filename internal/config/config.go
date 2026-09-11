package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
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
	BotToken      string
	AdminID       int64
	WebAppURL     string
	DBPath        string
	GroupID       int64
	GroupCode     string
	ListenAddr    string
	Timezone      string
	RecordingsDir string
	ChromeBin     string
	BBBDryRun     bool
	PanelPassword string
	Whitelist     []int64
}

func Load() (*Config, error) {
	loadDotEnv(".env")

	c := &Config{
		BotToken:      strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")),
		AdminID:       int64Env("ADMIN_TELEGRAM_ID", DefaultAdminID),
		WebAppURL:     strings.TrimRight(strings.TrimSpace(os.Getenv("WEBAPP_PUBLIC_URL")), "/"),
		DBPath:        strEnv("DATABASE_PATH", DefaultDBPath),
		GroupID:       int64Env("GROUP_ID", DefaultGroupID),
		GroupCode:     strEnv("GROUP_CODE", DefaultGroupCode),
		ListenAddr:    strEnv("LISTEN_ADDR", DefaultListen),
		Timezone:      strEnv("TZ", DefaultTimezone),
		RecordingsDir: strEnv("RECORDINGS_DIR", DefaultRecordings),
		ChromeBin:     strEnv("CHROME_BIN", ""),
		BBBDryRun:     os.Getenv("BBB_DRY_RUN") == "1",
		PanelPassword: strEnv("PANEL_PASSWORD", ""),
		Whitelist:     append([]int64(nil), DefaultWhitelist...),
	}
	if extra := strings.TrimSpace(os.Getenv("WHITELIST_EXTRA")); extra != "" {
		for _, p := range strings.Split(extra, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			id, err := strconv.ParseInt(p, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("WHITELIST_EXTRA: %q: %w", p, err)
			}
			c.Whitelist = append(c.Whitelist, id)
		}
	}
	return c, nil
}

func (c *Config) RequireBotToken() error {
	if c == nil || strings.TrimSpace(c.BotToken) == "" {
		return fmt.Errorf("TELEGRAM_BOT_TOKEN пустой")
	}
	return nil
}

func (c *Config) IsAllowed(id int64) bool {
	for _, w := range c.Whitelist {
		if w == id {
			return true
		}
	}
	return false
}

func (c *Config) IsAdmin(id int64) bool {
	return id == c.AdminID
}

func strEnv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func int64Env(key string, fallback int64) int64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fallback
	}
	return n
}

// loadDotEnv fills os.Getenv gaps from a file. Existing env wins.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
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
			continue
		}
		_ = os.Setenv(k, v)
	}
}
