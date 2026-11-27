// Package mcpapp — MCP-сервер для Moodle do.ssau.ru: конфиг из env, менеджер
// сессии (куки + вход по паролю из базы или env) и два инструмента чтения.
package mcpapp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultBaseURL — адрес Moodle по умолчанию.
	DefaultBaseURL = "https://do.ssau.ru/moodle"
	// DefaultTimeout — сколько ждать одну операцию с Moodle.
	DefaultTimeout = 40 * time.Second
	// DefaultStateDirName — путь к состоянию относительно домашнего каталога.
	DefaultStateDirName = ".local/state/ssau-mcp"
	// SessionFileName — файл с куками Moodle внутри StateDir.
	SessionFileName = "session.json"
	// DefaultRecordingsDir — каталог записей и конспектов по умолчанию.
	DefaultRecordingsDir = "recordings"

	// maxToolTimeout защищает от опечатки вроде MOODLE_MCP_TIMEOUT=1000000h.
	maxToolTimeout = 30 * time.Minute
)

// Config — настройки MCP-сервера. Поля приходят из env (см. LoadConfig),
// поверх них можно наложить JSON-файл (см. LoadConfigFile).
type Config struct {
	BaseURL       string        // MOODLE_MCP_BASE_URL
	StateDir      string        // MOODLE_MCP_STATE_DIR
	SessionFile   string        // StateDir/session.json
	DBPath        string        // MOODLE_MCP_DB, необязательно
	SecretKeyFile string        // MOODLE_MCP_SECRET_KEY_FILE, необязательно
	RecordingsDir string        // MOODLE_MCP_RECORDINGS_DIR, по умолчанию recordings
	Login         string        // MOODLE_MCP_LOGIN, необязательно
	Password      string        // MOODLE_MCP_PASSWORD, необязательно
	Timeout       time.Duration // MOODLE_MCP_TIMEOUT
	// AllowWrites включает инструменты, меняющие Moodle (попытки тестов).
	// По умолчанию false: без явного разрешения сервер только читает.
	AllowWrites bool // MOODLE_MCP_ALLOW_WRITES
}

// LoadConfig читает настройки из окружения и подставляет значения по умолчанию.
//
// Пути с ведущим «~/» раскрываются от домашнего каталога. Некорректный
// MOODLE_MCP_TIMEOUT — ошибка, а не молчаливый откат к 40 секундам.
func LoadConfig() (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, fmt.Errorf("mcpapp: домашний каталог: %w", err)
	}
	cfg := Config{
		BaseURL:       strings.TrimRight(envOr("MOODLE_MCP_BASE_URL", DefaultBaseURL), "/"),
		StateDir:      expandHome(envOr("MOODLE_MCP_STATE_DIR", filepath.Join(home, DefaultStateDirName)), home),
		DBPath:        expandHome(strings.TrimSpace(os.Getenv("MOODLE_MCP_DB")), home),
		SecretKeyFile: expandHome(strings.TrimSpace(os.Getenv("MOODLE_MCP_SECRET_KEY_FILE")), home),
		RecordingsDir: expandHome(envOr("MOODLE_MCP_RECORDINGS_DIR", DefaultRecordingsDir), home),
		Login:         strings.TrimSpace(os.Getenv("MOODLE_MCP_LOGIN")),
		// Пароль не обрезаем: пробелы могут быть его частью.
		Password: os.Getenv("MOODLE_MCP_PASSWORD"),
		Timeout:  DefaultTimeout,
	}
	allowWrites, err := envBool("MOODLE_MCP_ALLOW_WRITES")
	if err != nil {
		return Config{}, fmt.Errorf("mcpapp: MOODLE_MCP_ALLOW_WRITES: %w", err)
	}
	cfg.AllowWrites = allowWrites
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if raw := strings.TrimSpace(os.Getenv("MOODLE_MCP_TIMEOUT")); raw != "" {
		d, err := parseTimeout(raw)
		if err != nil {
			return Config{}, fmt.Errorf("mcpapp: MOODLE_MCP_TIMEOUT=%q: %w", raw, err)
		}
		cfg.Timeout = d
	}
	cfg.SessionFile = filepath.Join(cfg.StateDir, SessionFileName)
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// LoadConfigFile накладывает JSON-файл на конфиг из окружения. Заданные в
// файле поля перекрывают переменные окружения; пустые — не трогают.
func LoadConfigFile(path string) (Config, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return Config{}, err
	}
	if strings.TrimSpace(path) == "" {
		return cfg, nil
	}
	if home, herr := os.UserHomeDir(); herr == nil {
		path = expandHome(path, home)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("mcpapp: прочитать конфиг %s: %w", path, err)
	}
	var fc fileConfig
	if err := json.Unmarshal(raw, &fc); err != nil {
		return Config{}, fmt.Errorf("mcpapp: разобрать конфиг %s: %w", path, err)
	}
	if fc.Password != "" {
		info, err := os.Stat(path)
		if err != nil {
			return Config{}, fmt.Errorf("mcpapp: права конфига %s: %w", path, err)
		}
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			return Config{}, fmt.Errorf("mcpapp: конфиг %s с паролем доступен группе/остальным (права %o), нужен 0600", path, perm)
		}
	}
	if fc.BaseURL != "" {
		cfg.BaseURL = strings.TrimRight(strings.TrimSpace(fc.BaseURL), "/")
	}
	if fc.StateDir != "" {
		cfg.SetStateDir(fc.StateDir)
	}
	home, herr := os.UserHomeDir()
	if fc.DBPath != "" {
		if herr == nil {
			cfg.DBPath = expandHome(fc.DBPath, home)
		} else {
			cfg.DBPath = fc.DBPath
		}
	}
	if fc.SecretKeyFile != "" {
		if herr == nil {
			cfg.SecretKeyFile = expandHome(fc.SecretKeyFile, home)
		} else {
			cfg.SecretKeyFile = fc.SecretKeyFile
		}
	}
	if fc.RecordingsDir != "" {
		cfg.SetRecordingsDir(fc.RecordingsDir)
	}
	if fc.Login != "" {
		cfg.Login = fc.Login
	}
	if fc.Password != "" {
		cfg.Password = fc.Password
	}
	if fc.Timeout != "" {
		d, err := parseTimeout(fc.Timeout)
		if err != nil {
			return Config{}, fmt.Errorf("mcpapp: timeout в %s: %w", path, err)
		}
		cfg.Timeout = d
	}
	if fc.AllowWrites != nil {
		cfg.AllowWrites = *fc.AllowWrites
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	// Пару логин/пароль проверяем только после слияния env и JSON: файл может
	// дополнять половину, заданную в окружении.
	if err := cfg.ValidateCreds(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// fileConfig — форма JSON-файла. Секреты в репозиторий не кладём: файл
// предназначен для локального 0600-конфига вне git.
type fileConfig struct {
	BaseURL       string `json:"base_url"`
	StateDir      string `json:"state_dir"`
	DBPath        string `json:"db"`
	SecretKeyFile string `json:"secret_key_file"`
	RecordingsDir string `json:"recordings_dir"`
	Login         string `json:"login"`
	Password      string `json:"password"`
	Timeout       string `json:"timeout"`
	// Указатель, чтобы false в файле мог явно выключить запись после env=true.
	AllowWrites *bool `json:"allow_writes"`
}

// SetStateDir переопределяет каталог состояния (флаг -state-dir) и пересчитывает
// путь к session.json. Пустая строка ничего не меняет.
func (c *Config) SetStateDir(dir string) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return
	}
	if home, err := os.UserHomeDir(); err == nil {
		dir = expandHome(dir, home)
	}
	c.StateDir = dir
	c.SessionFile = filepath.Join(dir, SessionFileName)
}

// SetRecordingsDir переопределяет каталог записей (флаг -recordings-dir).
// Пустая строка ничего не меняет.
func (c *Config) SetRecordingsDir(dir string) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return
	}
	if home, err := os.UserHomeDir(); err == nil {
		dir = expandHome(dir, home)
	}
	c.RecordingsDir = dir
}

// ValidateCreds сообщает, задана ли полная пара логин/пароль.
func (c Config) ValidateCreds() error {
	if c.Login == "" && c.Password == "" {
		return nil
	}
	if c.Login == "" || c.Password == "" {
		return errors.New("mcpapp: MOODLE_MCP_LOGIN и MOODLE_MCP_PASSWORD нужно задавать вместе")
	}
	return nil
}

func (c Config) validate() error {
	u, err := url.Parse(strings.TrimSpace(c.BaseURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("mcpapp: некорректный base url %q", c.BaseURL)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("mcpapp: base url должен быть http(s), получено %q", u.Scheme)
	}
	if strings.TrimSpace(c.StateDir) == "" {
		return errors.New("mcpapp: пустой state dir")
	}
	if c.Timeout < 0 {
		return fmt.Errorf("mcpapp: отрицательный timeout %s", c.Timeout)
	}
	if c.Timeout > maxToolTimeout {
		return fmt.Errorf("mcpapp: timeout %s больше %s", c.Timeout, maxToolTimeout)
	}
	return nil
}

func parseTimeout(raw string) (time.Duration, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return DefaultTimeout, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		if d <= 0 {
			return 0, errors.New("должно быть больше нуля")
		}
		return d, nil
	}
	// Голое число трактуем как секунды: MOODLE_MCP_TIMEOUT=40.
	if n, err := strconv.Atoi(s); err == nil {
		if n <= 0 {
			return 0, errors.New("должно быть больше нуля")
		}
		return time.Duration(n) * time.Second, nil
	}
	return 0, errors.New("ожидается duration (40s) или секунды")
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// envBool читает булеву переменную окружения. Пустое значение — false;
// 1/0, true/false, yes/no, on/off без учёта регистра. Иное — ошибка,
// чтобы опечатка не включала запись в Moodle молча.
func envBool(key string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "", "0", "false", "no", "off":
		return false, nil
	case "1", "true", "yes", "on":
		return true, nil
	default:
		return false, errors.New("ожидается 0/1, true/false, yes/no или on/off")
	}
}

// expandHome раскрывает «~» и «~/…» в домашний каталог.
func expandHome(path, home string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	return path
}
