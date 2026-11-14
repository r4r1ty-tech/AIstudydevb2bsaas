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
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	llmURL       = "https://api.cheaperinference.com/v1"
	defaultModel = "deepseek-v4.1-flash"
	serverHost   = "root@203.0.113.20"
	jumpHost     = "jumphost"
	ghRepo       = "r4r1ty-tech/AIstudydevb2bsaas"
	remoteEnv    = "/opt/ssau-bot/.env"
)

var in = bufio.NewReader(os.Stdin)

func main() {
	code := run()
	fmt.Print("\nEnter — закрыть окно. ")
	_, _ = in.ReadString('\n')
	os.Exit(code)
}

func run() int {
	fmt.Println("SSAU-бот: ключи прода (сервер прод-VDS, panel.example.com)")
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

var client = &http.Client{Timeout: 60 * time.Second}

func doJSON(req *http.Request, dst any) error {
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(buf.String())
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	return json.Unmarshal(buf.Bytes(), dst)
}

func listModels(key string) ([]string, error) {
	req, _ := http.NewRequest(http.MethodGet, llmURL+"/models", nil)
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
		s, _ := in.ReadString('\n')
		s = strings.TrimSpace(s)
		if s == "" {
			return def
		}
		if len(models) == 0 || slices.Contains(models, s) {
			return s
		}
		fmt.Println("   нет такой модели в списке")
	}
}

func pingModel(key, model string) error {
	body, _ := json.Marshal(map[string]any{
		"model":      model,
		"messages":   []map[string]string{{"role": "user", "content": "Ответь одним словом: ок"}},
		"max_tokens": 16,
	})
	req, _ := http.NewRequest(http.MethodPost, llmURL+"/chat/completions", bytes.NewReader(body))
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
	req, _ := http.NewRequest(http.MethodGet, "https://api.telegram.org/bot"+tok+"/getMe", nil)
	var r struct {
		OK     bool `json:"ok"`
		Result struct {
			Username string `json:"username"`
		} `json:"result"`
	}
	if err := doJSON(req, &r); err != nil {
		// в тексте ошибки токена нет: Telegram его не возвращает
		return "", err
	}
	if !r.OK {
		return "", errors.New("Telegram ответил ok=false")
	}
	return r.Result.Username, nil
}

// remoteScript правит .env и перезапускает работающие сервисы. Значения
// приходят через stdin строками KEY=VALUE.
const remoteScript = `
import subprocess, sys
from pathlib import Path
p = Path("` + remoteEnv + `")
pairs = [l.split("=", 1) for l in sys.stdin.read().splitlines() if "=" in l]
lines = p.read_text().splitlines()
for k, v in pairs:
    for i, l in enumerate(lines):
        if l.startswith(k + "="):
            lines[i] = k + "=" + v
            break
    else:
        lines.append(k + "=" + v)
p.write_text("\n".join(lines) + "\n")
p.chmod(0o600)
print("✓ .env на сервере: " + ", ".join(k for k, _ in pairs))
units = ["ssau-tg", "ssau-rasp", "ssau-panel", "ssau-bbb"]
active = [u for u in units if subprocess.run(["systemctl", "is-active", "--quiet", u]).returncode == 0]
subprocess.run(["systemctl", "restart", *active], check=True)
import time; time.sleep(3)
bad = [u for u in active if subprocess.run(["systemctl", "is-active", "--quiet", u]).returncode != 0]
if bad:
    print("✗ не поднялись: " + ", ".join(bad)); sys.exit(1)
print("✓ перезапущены: " + ", ".join(active))
`

func applyRemote(set map[string]string) (string, error) {
	var stdin strings.Builder
	for k, v := range set {
		fmt.Fprintf(&stdin, "%s=%s\n", k, v)
	}
	b64 := base64.StdEncoding.EncodeToString([]byte(remoteScript))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=20",
		"-J", jumpHost, serverHost,
		fmt.Sprintf(`python3 -c "import base64;exec(base64.b64decode('%s'))"`, b64))
	cmd.Stdin = strings.NewReader(stdin.String())
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func ghSecret(name, val string) error {
	cmd := exec.Command("gh", "secret", "set", name, "--repo", ghRepo)
	cmd.Stdin = strings.NewReader(val)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func localEnvPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "dev", "AIstudydevb2bsaas", ".env")
}

func localEnv(key string) string {
	data, _ := os.ReadFile(localEnvPath())
	for _, l := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(l, key+"="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func updateLocalEnv(key, val string) error {
	path := localEnvPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	found := false
	for i, l := range lines {
		if strings.HasPrefix(l, key+"=") {
			lines[i], found = key+"="+val, true
		}
	}
	if !found {
		lines = append(lines, key+"="+val)
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

func indent(s string) string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	return "   " + strings.ReplaceAll(s, "\n", "\n   ") + "\n"
}
