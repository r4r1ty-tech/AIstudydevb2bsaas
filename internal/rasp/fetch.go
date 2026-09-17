package rasp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"time"

	"golang.org/x/net/publicsuffix"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

const (
	userAgent    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"
	acceptHdr    = "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8"
	acceptLang   = "ru-RU,ru;q=0.9"
	fetchTimeout = 25 * time.Second
)

var (
	primaryOrigin = "https://ssau.ru"
	wwwOrigin     = "https://www.ssau.ru"
)

// Fetch downloads a group week page. week=0 omits selectedWeek (current week on site).
// Returns body and HTTP status even on non-200. err is only for network/context failures.
func Fetch(ctx context.Context, groupID int64, week int) (body []byte, status int, err error) {
	logx.Debugf("rasp", "Fetch: group=%d week=%d", groupID, week)
	body, status, err = fetchSchedule(ctx, groupID, week)
	if err != nil {
		logx.Errorf("rasp", "Fetch: group=%d week=%d: %v", groupID, week, err)
		return body, status, err
	}
	logx.Infof("rasp", "Fetch: group=%d week=%d status=%d bytes=%d", groupID, week, status, len(body))
	return body, status, nil
}

var fetchSchedule = fetchFromSSAU

func fetchFromSSAU(ctx context.Context, groupID int64, week int) ([]byte, int, error) {
	logx.Debugf("rasp", "fetchFromSSAU: group=%d week=%d", groupID, week)
	if ctx == nil {
		ctx = context.Background()
	}
	client, err := newFetchClient()
	if err != nil {
		logx.Errorf("rasp", "fetchFromSSAU: new client: %v", err)
		return nil, 0, err
	}

	path := raspPath(groupID, week)
	logx.Debugf("rasp", "fetchFromSSAU: path=%s origin=%s", path, primaryOrigin)
	body, status, err := fetchOnOrigin(ctx, client, primaryOrigin, path)
	if err != nil {
		logx.Errorf("rasp", "fetchFromSSAU: primary %s%s: %v", primaryOrigin, path, err)
		return nil, status, err
	}
	if status != http.StatusForbidden {
		logx.Debugf("rasp", "fetchFromSSAU: status=%d bytes=%d", status, len(body))
		return body, status, nil
	}
	logx.Warnf("rasp", "403 on %s, retry via %s", primaryOrigin, wwwOrigin)
	body, status, err = fetchOnOrigin(ctx, client, wwwOrigin, path)
	if err != nil {
		logx.Errorf("rasp", "fetchFromSSAU: fallback %s%s: %v", wwwOrigin, path, err)
		return nil, status, err
	}
	logx.Debugf("rasp", "fetchFromSSAU: fallback status=%d bytes=%d", status, len(body))
	return body, status, nil
}

func newFetchClient() (*http.Client, error) {
	logx.Debugf("rasp", "newFetchClient: timeout=%s", fetchTimeout)
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		logx.Warnf("rasp", "newFetchClient: publicsuffix jar: %v", err)
		jar, err = cookiejar.New(nil)
		if err != nil {
			logx.Errorf("rasp", "newFetchClient: cookie jar: %v", err)
			return nil, fmt.Errorf("rasp: cookie jar: %w", err)
		}
	}
	return &http.Client{
		Timeout: fetchTimeout,
		Jar:     jar,
	}, nil
}

func raspPath(groupID int64, week int) string {
	logx.Debugf("rasp", "raspPath: group=%d week=%d", groupID, week)
	if week > 0 {
		path := fmt.Sprintf("/rasp?groupId=%d&selectedWeek=%d", groupID, week)
		logx.Debugf("rasp", "raspPath: path=%s", path)
		return path
	}
	path := fmt.Sprintf("/rasp?groupId=%d", groupID)
	logx.Debugf("rasp", "raspPath: path=%s", path)
	return path
}

func fetchOnOrigin(ctx context.Context, client *http.Client, origin, path string) ([]byte, int, error) {
	logx.Debugf("rasp", "fetchOnOrigin: origin=%s path=%s", origin, path)
	if err := warmCookies(ctx, client, origin+"/rasp"); err != nil {
		if ctx.Err() != nil {
			logx.Errorf("rasp", "fetchOnOrigin: warm cookies %s/rasp: %v", origin, err)
			return nil, 0, err
		}
		logx.Warnf("rasp", "fetchOnOrigin: warm cookies %s/rasp: %v", origin, err)
	}
	body, status, err := doGET(ctx, client, origin+path)
	if err != nil {
		logx.Errorf("rasp", "fetchOnOrigin: GET %s%s: %v", origin, path, err)
		return nil, status, err
	}
	logx.Debugf("rasp", "fetchOnOrigin: GET %s%s status=%d bytes=%d", origin, path, status, len(body))
	return body, status, nil
}

func warmCookies(ctx context.Context, client *http.Client, url string) error {
	logx.Debugf("rasp", "warmCookies: url=%s", url)
	req, err := newBrowserRequest(ctx, url)
	if err != nil {
		logx.Errorf("rasp", "warmCookies: new request %s: %v", url, err)
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		logx.Errorf("rasp", "warmCookies: do %s: %v", url, err)
		return err
	}
	defer resp.Body.Close()
	n, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		logx.Warnf("rasp", "warmCookies: drain %s: %v", url, err)
	}
	logx.Debugf("rasp", "warmCookies: url=%s status=%d drained=%d", url, resp.StatusCode, n)
	return nil
}

func doGET(ctx context.Context, client *http.Client, url string) ([]byte, int, error) {
	logx.Debugf("rasp", "doGET: url=%s", url)
	req, err := newBrowserRequest(ctx, url)
	if err != nil {
		logx.Errorf("rasp", "doGET: new request %s: %v", url, err)
		return nil, 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		logx.Errorf("rasp", "doGET: do %s: %v", url, err)
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		logx.Errorf("rasp", "doGET: read %s status=%d: %v", url, resp.StatusCode, err)
		return nil, resp.StatusCode, err
	}
	logx.Debugf("rasp", "doGET: url=%s status=%d bytes=%d", url, resp.StatusCode, len(body))
	return body, resp.StatusCode, nil
}

func newBrowserRequest(ctx context.Context, url string) (*http.Request, error) {
	logx.Debugf("rasp", "newBrowserRequest: url=%s", url)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		logx.Errorf("rasp", "newBrowserRequest: %s: %v", url, err)
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", acceptHdr)
	req.Header.Set("Accept-Language", acceptLang)
	return req, nil
}
