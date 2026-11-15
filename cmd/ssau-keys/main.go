// ssau-keys — интерактивная смена ключей прода: ключ LLM (cheaperinference)
// и токен Telegram-бота. Проверяет ключи, пишет их в /opt/ssau-bot/.env на
// сервере, перезапускает сервисы и обновляет секреты GitHub, чтобы CI-деплой
// не затёр ключ LLM. Секреты не печатает и в argv не передаёт.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	llmURL           = "https://api.cheaperinference.com/v1"
	defaultModel     = "deepseek-v4.1-flash"
	ghRepo           = "r4r1ty-tech/AIstudydevb2bsaas"
	remoteEnv        = "/opt/ssau-bot/.env"
	telegramBaseURL  = "https://api.telegram.org"
	maxHTTPBodyBytes = 1 << 20 // 1 MiB
	httpTimeout      = 60 * time.Second
	ghTimeout        = 30 * time.Second
)

var in = bufio.NewReader(os.Stdin)

func main() {
	code := run()
	fmt.Print("\nEnter — закрыть окно. ")
	_, _ = in.ReadString('\n')
	os.Exit(code)
}

func run() int {
	fmt.Println("SSAU-бот: ключи прода")
	fmt.Println("Пустой ввод — оставить как есть.")
	fmt.Println()

	set := map[string]string{}

	// 1. LLM
	fmt.Printf("1) Ключ ИИ, провайдер cheaperinference (%s)\n", llmURL)
	key := readSecret("   ключ: ")
	if key != "" {
		models, err := listModels(key)
		switch {
		case err != nil && strings.HasPrefix(err.Error(), "HTTP 404"):
			fmt.Println("   список моделей провайдер не отдаёт, проверю ключ запросом к модели")
		case err != nil:
			fmt.Printf("   ✗ ключ не принят: %v\n", err)
			return 1
		default:
			fmt.Printf("   ✓ ключ рабочий, моделей: %d\n", len(models))
		}
		model := pickModel(models)
		if err := pingModel(key, model); err != nil {
			fmt.Printf("   ✗ модель %s не ответила: %v\n", model, err)
			return 1
		}
		fmt.Printf("   ✓ %s отвечает\n", model)
		set["LLM_API_KEY"], set["LLM_API_URL"], set["LLM_MODEL"] = key, llmURL, model
	}
	fmt.Println()

	// 2. Telegram
	fmt.Println("2) Токен Telegram-бота (от @BotFather)")
	tok := readSecret("   токен: ")
	if tok != "" {
		name, err := botName(tok)
		if err != nil {
			fmt.Printf("   ✗ токен не принят: %v\n", err)
			return 1
		}
		fmt.Printf("   ✓ бот @%s\n", name)
		set["TELEGRAM_BOT_TOKEN"] = tok
	}
	fmt.Println()

	if len(set) == 0 {
		fmt.Println("Ничего не ввели — ничего не меняю.")
		return 0
	}

	ok := true
	fmt.Println("3) Применяю")
	if out, err := applyRemote(set); err != nil {
		fmt.Printf("   ✗ сервер: %v\n%s", err, indent(out))
		return 1
	} else {
		fmt.Print(indent(out))
	}

	if key != "" {
		for _, k := range []string{"LLM_API_KEY", "LLM_API_URL", "LLM_MODEL"} {
			if err := ghSecret(k, set[k]); err != nil {
				fmt.Printf("   ✗ секрет GitHub %s: %v (следующий CI-деплой вернёт старый ключ)\n", k, err)
				ok = false
			}
		}
		if ok {
			fmt.Println("   ✓ секреты GitHub LLM_API_KEY/URL/MODEL обновлены")
		}
	}

	if tok != "" {
		oldBot := strings.SplitN(localEnv("TELEGRAM_BOT_TOKEN"), ":", 2)[0]
		if err := updateLocalEnv("TELEGRAM_BOT_TOKEN", tok); err != nil {
			fmt.Printf("   ! локальный .env не обновил: %v\n", err)
		} else {
			fmt.Println("   ✓ локальный .env обновлён")
		}
		if oldBot != strings.SplitN(tok, ":", 2)[0] {
			fmt.Println("   Новый бот: людям из вайтлиста надо заново написать ему /start.")
		}
	}

	if ok {
		fmt.Println("\nГотово.")
		return 0
	}
	return 1
}

func readSecret(prompt string) string {
	fmt.Print(prompt)
	stty := func(arg string) {
		c := exec.Command("stty", arg)
		c.Stdin = os.Stdin
		_ = c.Run()
	}
	stty("-echo")
	s, _ := in.ReadString('\n')
	stty("echo")
	s = strings.TrimSpace(s)
	if s != "" {
		fmt.Printf("получено (%d симв.)\n", len(s))
	} else {
		fmt.Println("без изменений")
	}
	return s
}

var client = &http.Client{Timeout: httpTimeout}

func doJSON(req *http.Request, dst any) error {
	return doJSONWithClient(client, req, dst)
}

func doJSONWithClient(c *http.Client, req *http.Request, dst any) error {
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBodyBytes+1))
	if err != nil {
		return fmt.Errorf("чтение ответа HTTP %d: %w", resp.StatusCode, err)
	}
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	if len(body) > maxHTTPBodyBytes {
		return fmt.Errorf("ответ HTTP %d превышает лимит %d байт", resp.StatusCode, maxHTTPBodyBytes)
	}
	return json.Unmarshal(body, dst)
}

func listModels(key string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), httpTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, llmURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	var r struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := doJSON(req, &r); err != nil {
		return nil, err
	}
	var ids []string
	for _, m := range r.Data {
		ids = append(ids, m.ID)
	}
	slices.Sort(ids)
	return ids, nil
}

func pickModel(models []string) string {
	def := defaultModel
	if len(models) > 0 && !slices.Contains(models, def) {
		def = models[0]
	}
	if len(models) > 0 {
		fmt.Println("   модели:", strings.Join(models, ", "))
	}
	for {
		fmt.Printf("   модель для конспектов [%s]: ", def)
		s, err := in.ReadString('\n')
		s = strings.TrimSpace(s)
		if s == "" {
			return def
		}
		if len(models) == 0 || slices.Contains(models, s) {
			return s
		}
		if err != nil {
			return def
		}
		fmt.Println("   нет такой модели в списке")
	}
}

func pingModel(key, model string) error {
	body, err := json.Marshal(map[string]any{
		"model":      model,
		"messages":   []map[string]string{{"role": "user", "content": "Ответь одним словом: ок"}},
		"max_tokens": 16,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), httpTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, llmURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	var r struct {
		Choices []json.RawMessage `json:"choices"`
	}
	if err := doJSON(req, &r); err != nil {
		return err
	}
	if len(r.Choices) == 0 {
		return errors.New("пустой ответ")
	}
	return nil
}

func botName(tok string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), httpTimeout)
	defer cancel()
	return botNameWithBase(ctx, client, telegramBaseURL, tok)
}

func botNameWithBase(ctx context.Context, c *http.Client, baseURL, tok string) (string, error) {
	if tok == "" || strings.ContainsAny(tok, " \t\r\n/?#") {
		return "", errors.New("некорректный формат токена Telegram")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/bot"+tok+"/getMe", nil)
	if err != nil {
		return "", sanitizeSecretErr(err, tok)
	}
	var r struct {
		OK     bool `json:"ok"`
		Result struct {
			Username string `json:"username"`
		} `json:"result"`
	}
	if err := doJSONWithClient(c, req, &r); err != nil {
		// *url.Error включает полный URL с токеном; очищаем ошибку перед возвратом.
		return "", sanitizeSecretErr(err, tok)
	}
	if !r.OK {
		return "", errors.New("Telegram ответил ok=false")
	}
	return r.Result.Username, nil
}

func sanitizeSecretErr(err error, secret string) error {
	if err == nil {
		return nil
	}
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		msg := fmt.Sprintf("%s telegram API: %s", ue.Op, ue.Err.Error())
		return errors.New(maskSecret(msg, secret))
	}
	return errors.New(maskSecret(err.Error(), secret))
}

func maskSecret(msg, secret string) string {
	if secret == "" {
		return msg
	}
	out := strings.ReplaceAll(msg, secret, "[REDACTED]")
	if esc := url.PathEscape(secret); esc != "" && esc != secret {
		out = strings.ReplaceAll(out, esc, "[REDACTED]")
	}
	if esc := url.QueryEscape(secret); esc != "" && esc != secret {
		out = strings.ReplaceAll(out, esc, "[REDACTED]")
	}
	return out
}

// remoteScript правит .env и перезапускает работающие сервисы. Значения
// приходят через stdin строками KEY=VALUE.
const remoteScript = `
import os, subprocess, sys, tempfile, time
from pathlib import Path

p = Path("` + remoteEnv + `")
pairs = [l.split("=", 1) for l in sys.stdin.read().splitlines() if "=" in l]
lines = p.read_text(encoding="utf-8").splitlines() if p.exists() else []
for k, v in pairs:
    for i, l in enumerate(lines):
        if l.startswith(k + "="):
            lines[i] = k + "=" + v
            break
    else:
        lines.append(k + "=" + v)
p.parent.mkdir(parents=True, exist_ok=True)
fd, tmp_name = tempfile.mkstemp(prefix=p.name + ".", suffix=".tmp", dir=str(p.parent))
try:
    os.chmod(tmp_name, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as f:
        f.write("\n".join(lines) + "\n")
    os.replace(tmp_name, p)
except Exception:
    if os.path.exists(tmp_name):
        os.unlink(tmp_name)
    raise
print("✓ .env на сервере: " + ", ".join(k for k, _ in pairs))
units = ["ssau-tg", "ssau-rasp", "ssau-panel", "ssau-bbb"]
active = [u for u in units if subprocess.run(["systemctl", "is-active", "--quiet", u]).returncode == 0]
if not active:
    print("нет активных сервисов для перезапуска")
    sys.exit(0)
subprocess.run(["systemctl", "restart", *active], check=True)
time.sleep(3)
bad = [u for u in active if subprocess.run(["systemctl", "is-active", "--quiet", u]).returncode != 0]
if bad:
    print("✗ не поднялись: " + ", ".join(bad))
    sys.exit(1)
print("✓ перезапущены: " + ", ".join(active))
`

func applyRemote(set map[string]string) (string, error) {
	var stdin strings.Builder
	for k, v := range set {
		if strings.ContainsAny(k, "=\r\n") || strings.ContainsAny(v, "\r\n") {
			return "", fmt.Errorf("недопустимые символы в значении для %s", k)
		}
		fmt.Fprintf(&stdin, "%s=%s\n", k, v)
	}
	b64 := base64.StdEncoding.EncodeToString([]byte(remoteScript))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// Адрес прода и jump-хост не хранятся в публичном репозитории: SSAU_SSH_HOST (user@host)
	// и необязательный SSAU_SSH_JUMP задаются в окружении или в ~/.ssh/config.
	host := strings.TrimSpace(os.Getenv("SSAU_SSH_HOST"))
	if host == "" {
		return "", errors.New("не задан SSAU_SSH_HOST (например root@адрес-сервера)")
	}
	if strings.HasPrefix(host, "-") {
		return "", errors.New("SSAU_SSH_HOST не может начинаться с «-»")
	}
	args := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=20"}
	if jump := strings.TrimSpace(os.Getenv("SSAU_SSH_JUMP")); jump != "" && !strings.HasPrefix(jump, "-") {
		args = append(args, "-J", jump)
	}
	args = append(args, host, fmt.Sprintf(`python3 -c "import base64;exec(base64.b64decode('%s'))"`, b64))
	cmd := exec.CommandContext(ctx, "ssh", args...) //nolint:gosec // адрес задаёт сам админ в своём окружении, опции через «-» отсечены выше
	cmd.Stdin = strings.NewReader(stdin.String())
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func ghSecret(name, val string) error {
	ctx, cancel := context.WithTimeout(context.Background(), ghTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "gh", "secret", "set", name, "--repo", ghRepo)
	cmd.Stdin = strings.NewReader(val)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func localEnvPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "dev", "AIstudydevb2bsaas", ".env"), nil
}

func localEnv(key string) string {
	path, err := localEnvPath()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(l, key+"="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func updateLocalEnv(key, val string) error {
	path, err := localEnvPath()
	if err != nil {
		return err
	}
	return updateEnvFile(path, key, val)
}

func updateEnvFile(path, key, val string) error {
	if strings.ContainsAny(key, "=\r\n") || strings.ContainsAny(val, "\r\n") {
		return fmt.Errorf("недопустимые символы в %s", key)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var lines []string
	if trimmed := strings.TrimRight(string(data), "\n"); trimmed != "" {
		lines = strings.Split(trimmed, "\n")
	}
	found := false
	for i, l := range lines {
		if strings.HasPrefix(l, key+"=") {
			lines[i], found = key+"="+val, true
		}
	}
	if !found {
		lines = append(lines, key+"="+val)
	}
	content := []byte(strings.Join(lines, "\n") + "\n")
	return writeFileAtomic0600(path, content)
}

func writeFileAtomic0600(path string, content []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func indent(s string) string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	return "   " + strings.ReplaceAll(s, "\n", "\n   ") + "\n"
}
