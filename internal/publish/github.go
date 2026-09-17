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

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
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
	ok := g != nil && strings.TrimSpace(g.Token) != "" &&
		g.Owner != "" && g.Repo != "" && g.Branch != ""
	if g != nil {
		logx.Debugf("publish", "Enabled: token=%t owner=%q repo=%q branch=%q -> %t", strings.TrimSpace(g.Token) != "", g.Owner, g.Repo, g.Branch, ok)
	} else {
		logx.Debugf("publish", "Enabled: github=nil -> false")
	}
	return ok
}

func (g *GitHub) api() string {
	if g.API != "" {
		a := strings.TrimRight(g.API, "/")
		logx.Debugf("publish", "api: custom=%s", a)
		return a
	}
	logx.Debugf("publish", "api: default=%s", defaultAPI)
	return defaultAPI
}

func (g *GitHub) client() *http.Client {
	if g.Client != nil {
		logx.Debugf("publish", "client: custom")
		return g.Client
	}
	logx.Debugf("publish", "client: default timeout=60s")
	return &http.Client{Timeout: 60 * time.Second}
}

func BlobSHA(content []byte) string {
	logx.Debugf("publish", "BlobSHA: bytes=%d", len(content))
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(content))
	h.Write(content)
	sum := hex.EncodeToString(h.Sum(nil))
	logx.Debugf("publish", "BlobSHA: %s", sum)
	return sum
}

func contentPath(repoPath string) string {
	parts := strings.Split(strings.Trim(repoPath, "/"), "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	out := strings.Join(parts, "/")
	logx.Debugf("publish", "contentPath: in=%q out=%q", repoPath, out)
	return out
}

// Upsert creates or updates a file at repoPath. Returns true when the remote
// content changed. A no-op (same blob sha) returns false without a commit.
func (g *GitHub) Upsert(ctx context.Context, repoPath string, content []byte, message string) (bool, error) {
	logx.Debugf("publish", "Upsert: start path=%q bytes=%d message=%q", repoPath, len(content), message)
	if ctx == nil {
		ctx = context.Background()
	}
	if !g.Enabled() {
		logx.Errorf("publish", "Upsert: github не настроен path=%q", repoPath)
		return false, fmt.Errorf("publish: github не настроен")
	}
	if strings.TrimSpace(message) == "" {
		message = "update " + repoPath
		logx.Debugf("publish", "Upsert: default message=%q", message)
	}
	sum := BlobSHA(content)
	endpoint := fmt.Sprintf("%s/repos/%s/%s/contents/%s", g.api(), g.Owner, g.Repo, contentPath(repoPath))
	logx.Debugf("publish", "Upsert: endpoint=%s branch=%s token_len=%d", endpoint, g.Branch, len(g.Token))

	var sha string
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?ref="+url.QueryEscape(g.Branch), nil)
	if err != nil {
		logx.Errorf("publish", "Upsert: new GET request %s: %v", endpoint, err)
		return false, err
	}
	g.auth(req)
	start := time.Now()
	res, err := g.client().Do(req)
	if err != nil {
		logx.Errorf("publish", "Upsert: GET request failed after %s: %v", time.Since(start), err)
		return false, err
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		logx.Errorf("publish", "Upsert: read GET body: %v", err)
	}
	res.Body.Close()
	logx.Debugf("publish", "Upsert: GET HTTP %d in %s body=%d bytes", res.StatusCode, time.Since(start), len(body))
	switch res.StatusCode {
	case http.StatusOK:
		var meta struct {
			SHA string `json:"sha"`
		}
		if err := json.Unmarshal(body, &meta); err != nil {
			logx.Errorf("publish", "Upsert: decode meta path=%q: %v", repoPath, err)
			return false, err
		}
		sha = meta.SHA
		logx.Debugf("publish", "Upsert: remote sha=%s local sha=%s", sha, sum)
		if sha == sum {
			logx.Infof("publish", "Upsert: skip unchanged path=%q", repoPath)
			return false, nil
		}
	case http.StatusNotFound:
		logx.Debugf("publish", "Upsert: remote path not found, will create path=%q", repoPath)
	default:
		logx.Errorf("publish", "Upsert: get %s: HTTP %d: %s", repoPath, res.StatusCode, truncate(string(body), 300))
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
		logx.Errorf("publish", "Upsert: marshal payload path=%q: %v", repoPath, err)
		return false, err
	}
	put, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(buf))
	if err != nil {
		logx.Errorf("publish", "Upsert: new PUT request %s: %v", endpoint, err)
		return false, err
	}
	g.auth(put)
	put.Header.Set("Content-Type", "application/json")
	logx.Debugf("publish", "Upsert: PUT %s payload=%d bytes new_sha=%t", endpoint, len(buf), sha != "")
	start = time.Now()
	res2, err := g.client().Do(put)
	if err != nil {
		logx.Errorf("publish", "Upsert: PUT request failed after %s: %v", time.Since(start), err)
		return false, err
	}
	out, err := io.ReadAll(io.LimitReader(res2.Body, 1<<20))
	if err != nil {
		logx.Errorf("publish", "Upsert: read PUT body: %v", err)
	}
	res2.Body.Close()
	logx.Debugf("publish", "Upsert: PUT HTTP %d in %s body=%d bytes", res2.StatusCode, time.Since(start), len(out))
	if res2.StatusCode != http.StatusOK && res2.StatusCode != http.StatusCreated {
		logx.Errorf("publish", "Upsert: put %s: HTTP %d: %s", repoPath, res2.StatusCode, truncate(string(out), 300))
		return false, fmt.Errorf("publish: put %s: HTTP %d: %s", repoPath, res2.StatusCode, truncate(string(out), 300))
	}
	logx.Infof("publish", "Upsert: push ok path=%q message=%q", repoPath, message)
	return true, nil
}

func (g *GitHub) auth(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+g.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "ssau-lecture-bot")
	logx.Debugf("publish", "auth: headers set token_len=%d", len(g.Token))
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		logx.Debugf("publish", "truncate: len=%d max=%d unchanged", len(s), n)
		return s
	}
	logx.Debugf("publish", "truncate: len=%d max=%d cut", len(s), n)
	return s[:n] + "…"
}
