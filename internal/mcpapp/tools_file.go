package mcpapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodle"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodlematerial"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/websess"
)

const (
	// defaultFileName — имя файла, если из адреса его не достать.
	defaultFileName = "file"
	// maxFileNameRunes — потолок имени, чтобы не упереться в лимит файловой системы.
	maxFileNameRunes = 120
	// moodleFileHost — хост Moodle, разрешённый помимо BaseHost.
	moodleFileHost = "do.ssau.ru"
)

var (
	// errFileForbiddenHost — чужой хост или не-http(s) схема: скачивать нельзя.
	errFileForbiddenHost = errors.New("mcpapp: хост файла не разрешён")
	// errFileNotFound — файла нет или модуль не найден.
	errFileNotFound = errors.New("mcpapp: файл не найден")
)

// FileInput — аргументы moodle_file_get.
type FileInput struct {
	URL         string `json:"url,omitempty" jsonschema:"прямая ссылка на файл Moodle (pluginfile.php); можно вместо cmid"`
	CMID        int64  `json:"cmid,omitempty" jsonschema:"id модуля курса; ссылка на файл будет найдена через структуру курса"`
	CourseID    int64  `json:"course_id,omitempty" jsonschema:"курс для поиска cmid; ускоряет разрешение модуля"`
	ExtractText *bool  `json:"extract_text,omitempty" jsonschema:"извлечь текст PDF/HTML/text; по умолчанию true"`
	MaxChars    int    `json:"max_chars,omitempty" jsonschema:"сколько рун текста вернуть: по умолчанию и максимум 100000"`
	Save        bool   `json:"save,omitempty" jsonschema:"сохранить файл в StateDir/files и вернуть path"`
}

// FileOutput — ответ moodle_file_get. Содержимое файла в base64 не отдаётся:
// только текст и, при save=true, путь к сохранённому файлу.
type FileOutput struct {
	URL         string `json:"url"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type,omitempty"`
	Size        int64  `json:"size"`
	Extracted   bool   `json:"extracted"`
	Text        string `json:"text,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
	Saved       bool   `json:"saved,omitempty"`
	Path        string `json:"path,omitempty"`
}

// registerFileTools добавляет moodle_file_get: скачивание файла Moodle с
// allowlist хостов, потолком 25 МБ и извлечением текста.
func registerFileTools(s *mcp.Server, sess *Session) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "moodle_file_get",
		Title:       "Файл Moodle",
		Description: "Скачивает файл Moodle по прямой ссылке или cmid (ссылка ищется через структуру курса), извлекает текст PDF/HTML/text и при save=true сохраняет файл в каталог состояния. Разрешены только do.ssau.ru и хост Moodle, только http(s), не больше 25 МБ; чужие хосты и редиректы отклоняются. Только чтение Moodle.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in FileInput) (*mcp.CallToolResult, FileOutput, error) {
		out, err := readFile(ctx, sess, in)
		if err != nil {
			return nil, FileOutput{}, err
		}
		return nil, out, nil
	})
}

// readFile скачивает файл Moodle и готовит MCP-ответ.
func readFile(ctx context.Context, sess *Session, in FileInput) (FileOutput, error) {
	target := strings.TrimSpace(in.URL)
	if target == "" && in.CMID <= 0 {
		return FileOutput{}, toolFailure("bad_input", "укажите url файла или cmid модуля", nil)
	}
	extract := in.ExtractText == nil || *in.ExtractText
	maxChars := in.MaxChars
	if maxChars <= 0 || maxChars > maxToolOutputRunes {
		maxChars = maxToolOutputRunes
	}

	var out FileOutput
	err := sess.WithClient(ctx, func(ctx context.Context, c *moodle.Client) error {
		allowed := allowedFileHosts(c)
		if target == "" {
			mod, rerr := moodlematerial.ResolveModule(ctx, c, in.CMID, in.CourseID)
			if rerr != nil {
				return rerr
			}
			target = moduleFileURL(mod)
			if target == "" {
				return fmt.Errorf("%w: у модуля cmid=%d нет файла", errFileNotFound, in.CMID)
			}
		}
		if cerr := checkFileURL(target, allowed); cerr != nil {
			return cerr
		}
		res, ferr := c.FetchFileLimited(ctx, target, moodle.FileLimitDefault, allowed)
		if ferr != nil {
			return classifyFetchError(ferr)
		}
		out = FileOutput{
			URL:         res.FinalURL,
			Filename:    res.Filename,
			ContentType: res.ContentType,
			Size:        res.Size,
		}
		if out.URL == "" {
			out.URL = absoluteMoodleURL(sess.cfg.BaseURL, target)
		}
		if out.Filename == "" {
			out.Filename = moodlematerial.FileNameFromURL(out.URL)
		}
		if extract {
			text := moodlematerial.CleanText(moodlematerial.ExtractFileText(res.Data, res.ContentType))
			out.Text, out.Truncated = TruncateText(text, maxChars)
			out.Extracted = out.Text != ""
		}
		if in.Save {
			path, serr := saveDownload(sess.cfg, out.Filename, res.Data)
			if serr != nil {
				return serr
			}
			out.Saved, out.Path = true, path
		}
		return nil
	})
	if err != nil {
		return FileOutput{}, fileToolError(err)
	}
	return out, nil
}

// allowedFileHosts — хосты, с которых разрешено скачивать: do.ssau.ru и хост
// настроенного Moodle (в тестах и на стендах он отличается).
func allowedFileHosts(c *moodle.Client) []string {
	hosts := []string{moodleFileHost}
	if h := c.BaseHost(); h != "" {
		hosts = append(hosts, h)
	}
	return hosts
}

// checkFileURL проверяет адрес до запроса: только http(s), только allowlist,
// без protocol-relative «//host». Относительный путь — это путь Moodle.
func checkFileURL(raw string, allowed []string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("%w: пустой url", errFileNotFound)
	}
	if strings.HasPrefix(raw, "//") {
		return fmt.Errorf("%w: protocol-relative url", errFileForbiddenHost)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: %v", errFileNotFound, err)
	}
	if u.Scheme == "" {
		if u.Host != "" {
			return fmt.Errorf("%w: %s", errFileForbiddenHost, u.Host)
		}
		return nil
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: схема %q", errFileForbiddenHost, u.Scheme)
	}
	host := normalizeHost(u.Hostname())
	if host == "" {
		return fmt.Errorf("%w: пустой хост", errFileForbiddenHost)
	}
	for _, h := range allowed {
		if normalizeHost(h) == host {
			return nil
		}
	}
	return fmt.Errorf("%w: %s", errFileForbiddenHost, host)
}

// normalizeHost — хост для сравнения: нижний регистр, без точки на конце.
func normalizeHost(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	return strings.TrimSuffix(raw, ".")
}

// moduleFileURL выбирает файл модуля: вложение pluginfile.php, затем адрес
// модуля, затем обычный /mod/<kind>/view.php.
func moduleFileURL(mod moodle.CourseModule) string {
	for _, f := range mod.Files {
		if strings.Contains(f, "pluginfile.php") {
			return f
		}
	}
	if mod.URL != "" {
		return mod.URL
	}
	if mod.Kind == "" {
		return ""
	}
	return fmt.Sprintf("/mod/%s/view.php?id=%d", mod.Kind, mod.CMID)
}

// classifyFetchError переводит ошибки moodle.FetchFileLimited в понятные коды.
// unauthorized и too_large пробрасываются как есть: Session.do ретраит первое,
// а второе MCP-слой показывает кодом too_large.
func classifyFetchError(err error) error {
	if errors.Is(err, moodle.ErrUnauthorized) ||
		errors.Is(err, websess.ErrBodyTooLarge) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, moodle.ErrForbiddenHost) {
		return fmt.Errorf("%w: %v", errFileForbiddenHost, err)
	}
	var statusErr *moodle.HTTPStatusError
	if errors.As(err, &statusErr) {
		switch statusErr.Status {
		case http.StatusUnauthorized:
			return fmt.Errorf("%w: %v", moodle.ErrUnauthorized, err)
		case http.StatusForbidden:
			return fmt.Errorf("%w: %v", errFileForbiddenHost, err)
		case http.StatusNotFound:
			return fmt.Errorf("%w: %v", errFileNotFound, err)
		}
	}
	return err
}

// saveDownload пишет файл в StateDir/files с правами 0600 и возвращает путь.
func saveDownload(cfg Config, name string, data []byte) (string, error) {
	dir := filepath.Join(cfg.StateDir, "files")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("mcpapp: каталог files: %w", err)
	}
	path := filepath.Join(dir, safeFileName(name))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("mcpapp: сохранить %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", fmt.Errorf("mcpapp: права %s: %w", path, err)
	}
	return path, nil
}

// safeFileName оставляет в имени буквы, цифры, точку, дефис и подчёркивание:
// «../etc/passwd» не должен выйти за пределы каталога files.
func safeFileName(name string) string {
	name = strings.TrimSpace(filepath.Base(strings.ReplaceAll(name, "\\", "/")))
	var b strings.Builder
	for _, r := range name {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if strings.Trim(out, ".") == "" {
		return defaultFileName
	}
	if strings.HasPrefix(out, ".") {
		out = "_" + out[1:]
	}
	if r := []rune(out); len(r) > maxFileNameRunes {
		out = string(r[:maxFileNameRunes])
	}
	return out
}

// fileToolError добавляет к ошибке код и действие для LLM.
func fileToolError(err error) error {
	code := "moodle_error"
	switch {
	case errors.Is(err, ErrNoCredentials), errors.Is(err, moodle.ErrUnauthorized):
		code = "unauthorized"
	case errors.Is(err, errFileForbiddenHost):
		code = "forbidden_host"
	case errors.Is(err, websess.ErrBodyTooLarge):
		code = "too_large"
	case errors.Is(err, errFileNotFound), errors.Is(err, moodlematerial.ErrModuleNotFound):
		code = "not_found"
	case errors.Is(err, context.DeadlineExceeded):
		code = "timeout"
	}
	return toolFailure(code, "проверьте url или cmid и сессию (moodle_status)", err)
}
