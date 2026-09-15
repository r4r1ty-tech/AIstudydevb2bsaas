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
	return fetchSchedule(ctx, groupID, week)
}

var fetchSchedule = fetchFromSSAU

func fetchFromSSAU(ctx context.Context, groupID int64, week int) ([]byte, int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	client, err := newFetchClient()
	if err != nil {
		return nil, 0, err
	}

	path := raspPath(groupID, week)
	body, status, err := fetchOnOrigin(ctx, client, primaryOrigin, path)
	if err != nil {
		return nil, status, err
	}
	if status != http.StatusForbidden {
		return body, status, nil
	}
	logx.Debugf("rasp", "403 on %s, retry via %s", primaryOrigin, wwwOrigin)
	return fetchOnOrigin(ctx, client, wwwOrigin, path)
}

func newFetchClient() (*http.Client, error) {
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		jar, err = cookiejar.New(nil)
		if err != nil {
			return nil, fmt.Errorf("rasp: cookie jar: %w", err)
		}
	}
	return &http.Client{
		Timeout: fetchTimeout,
		Jar:     jar,
	}, nil
}

func raspPath(groupID int64, week int) string {
	if week > 0 {
		return fmt.Sprintf("/rasp?groupId=%d&selectedWeek=%d", groupID, week)
	}
	return fmt.Sprintf("/rasp?groupId=%d", groupID)
}

func fetchOnOrigin(ctx context.Context, client *http.Client, origin, path string) ([]byte, int, error) {
	if err := warmCookies(ctx, client, origin+"/rasp"); err != nil {
		if ctx.Err() != nil {
			return nil, 0, err
		}
	}
	return doGET(ctx, client, origin+path)
}

func warmCookies(ctx context.Context, client *http.Client, url string) error {
	req, err := newBrowserRequest(ctx, url)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func doGET(ctx context.Context, client *http.Client, url string) ([]byte, int, error) {
	req, err := newBrowserRequest(ctx, url)
	if err != nil {
		return nil, 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

func newBrowserRequest(ctx context.Context, url string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", acceptHdr)
	req.Header.Set("Accept-Language", acceptLang)
	return req, nil
}
