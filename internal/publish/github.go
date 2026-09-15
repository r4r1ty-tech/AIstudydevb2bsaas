package publish

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultAPI = "https://api.github.com"

type GitHub struct {
	Token  string
	Owner  string
	Repo   string
	Branch string
	API    string
	Client *http.Client
}

func (g *GitHub) Enabled() bool {
	return g != nil && strings.TrimSpace(g.Token) != "" &&
		g.Owner != "" && g.Repo != "" && g.Branch != ""
}

func (g *GitHub) api() string {
	if g.API != "" {
		return strings.TrimRight(g.API, "/")
	}
	return defaultAPI
}

func (g *GitHub) client() *http.Client {
	if g.Client != nil {
		return g.Client
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func BlobSHA(content []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(content))
	h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}

func contentPath(repoPath string) string {
	parts := strings.Split(strings.Trim(repoPath, "/"), "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// Upsert creates or updates a file at repoPath. Returns true when the remote
// content changed. A no-op (same blob sha) returns false without a commit.
func (g *GitHub) Upsert(ctx context.Context, repoPath string, content []byte, message string) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !g.Enabled() {
		return false, fmt.Errorf("publish: github не настроен")
	}
	if strings.TrimSpace(message) == "" {
		message = "update " + repoPath
	}
	sum := BlobSHA(content)
	endpoint := fmt.Sprintf("%s/repos/%s/%s/contents/%s", g.api(), g.Owner, g.Repo, contentPath(repoPath))

	var sha string
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?ref="+url.QueryEscape(g.Branch), nil)
	if err != nil {
		return false, err
	}
	g.auth(req)
	res, err := g.client().Do(req)
	if err != nil {
		return false, err
	}
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
		var meta struct {
			SHA string `json:"sha"`
		}
		if err := json.Unmarshal(body, &meta); err != nil {
			return false, err
		}
		sha = meta.SHA
		if sha == sum {
			return false, nil
		}
	case http.StatusNotFound:
	default:
		return false, fmt.Errorf("publish: get %s: HTTP %d: %s", repoPath, res.StatusCode, truncate(string(body), 300))
	}

	payload := map[string]string{
		"message": message,
		"content": base64.StdEncoding.EncodeToString(content),
		"branch":  g.Branch,
	}
	if sha != "" {
		payload["sha"] = sha
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return false, err
	}
	put, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(buf))
	if err != nil {
		return false, err
	}
	g.auth(put)
	put.Header.Set("Content-Type", "application/json")
	res2, err := g.client().Do(put)
	if err != nil {
		return false, err
	}
	out, _ := io.ReadAll(io.LimitReader(res2.Body, 1<<20))
	res2.Body.Close()
	if res2.StatusCode != http.StatusOK && res2.StatusCode != http.StatusCreated {
		return false, fmt.Errorf("publish: put %s: HTTP %d: %s", repoPath, res2.StatusCode, truncate(string(out), 300))
	}
	return true, nil
}

func (g *GitHub) auth(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+g.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "ssau-lecture-bot")
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
