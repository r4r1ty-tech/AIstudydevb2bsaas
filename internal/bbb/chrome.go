package bbb

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
	"github.com/go-rod/rod/lib/proto"
)

type ChromeJoiner struct {
	Bin string

	mu       sync.Mutex
	browser  *rod.Browser
	launcher *launcher.Launcher
}

func NewChromeJoiner(bin string) *ChromeJoiner {
	if bin == "" {
		bin = FindChrome("")
	}
	return &ChromeJoiner{Bin: bin}
}

func (c *ChromeJoiner) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.browser != nil {
		_ = c.browser.Close()
		c.browser = nil
	}
	if c.launcher != nil {
		c.launcher.Kill()
		c.launcher = nil
	}
	return nil
}

func (c *ChromeJoiner) ensureBrowser() (*rod.Browser, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.browser != nil {
		return c.browser, nil
	}
	bin := c.Bin
	if bin == "" {
		bin = FindChrome("")
	}
	if bin == "" {
		return nil, fmt.Errorf("chromium не найден — apt install chromium или CHROME_BIN")
	}

	dir := filepath.Join(os.TempDir(), "ssau-bbb-chrome")
	_ = os.MkdirAll(dir, 0o755)

	l := launcher.New().
		Bin(bin).
		HeadlessNew(true).
		NoSandbox(true).
		Leakless(false).
		UserDataDir(dir).
		Set(flags.Flag("disable-gpu")).
		Set(flags.Flag("disable-dev-shm-usage")).
		Set(flags.Flag("disable-crash-reporter")).
		Set(flags.Flag("no-first-run")).
		Set(flags.Flag("no-default-browser-check")).
		Set(flags.Flag("autoplay-policy"), "no-user-gesture-required").
		Set(flags.Flag("use-fake-ui-for-media-stream")).
		Set(flags.Flag("use-fake-device-for-media-stream"))

	u, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("chrome launch: %w", err)
	}
	b := rod.New().ControlURL(u).NoDefaultDevice()
	if err := b.Connect(); err != nil {
		l.Kill()
		return nil, fmt.Errorf("chrome connect: %w", err)
	}
	c.launcher = l
	c.browser = b
	log.Printf("bbb: chromium %s", bin)
	return b, nil
}

type chromeSession struct {
	page      *rod.Page
	root      *rod.Browser
	contextID proto.BrowserBrowserContextID
	bridge    *socksBridge
}

func (s *chromeSession) InLobby(ctx context.Context) (bool, error) {
	if s == nil || s.page == nil {
		return false, nil
	}
	p := s.page.Context(ctx)
	html, err := p.Timeout(5 * time.Second).HTML()
	if err != nil {
		return false, err
	}
	low := strings.ToLower(html)
	for _, m := range []string{
		"please wait",
		"ожидайте",
		"waiting for a moderator",
		"waiting for the moderator",
		"you'll join when",
		"guest lobby",
		`data-test="waitingusers"`,
		"waitingusers",
	} {
		if strings.Contains(low, m) {
			return true, nil
		}
	}
	ok, _, err := p.Timeout(2 * time.Second).Has("[data-test='waitingUsers']")
	if err != nil {
		return false, nil
	}
	return ok, nil
}

func (s *chromeSession) Close() error {
	if s == nil {
		return nil
	}
	if s.page != nil {
		_ = s.page.Close()
	}
	if s.root != nil && s.contextID != "" {
		_ = proto.TargetDisposeBrowserContext{BrowserContextID: s.contextID}.Call(s.root)
	}
	if s.bridge != nil {
		_ = s.bridge.Close()
	}
	return nil
}

func (c *ChromeJoiner) Join(ctx context.Context, meetingURL, fio, socks5 string) (Session, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	root, err := c.ensureBrowser()
	if err != nil {
		return nil, err
	}

	bridge, err := startSOCKSBridge(socks5)
	if err != nil {
		return nil, err
	}
	proxyURL, err := chromeProxyURL(socks5, bridge)
	if err != nil {
		if bridge != nil {
			_ = bridge.Close()
		}
		return nil, err
	}

	create := proto.TargetCreateBrowserContext{}
	if proxyURL != "" {
		create.ProxyServer = proxyURL
	}
	res, err := create.Call(root)
	if err != nil {
		if bridge != nil {
			_ = bridge.Close()
		}
		return nil, fmt.Errorf("browser context: %w", err)
	}

	incog := *root
	incog.BrowserContextID = res.BrowserContextID

	page, err := incog.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		_ = proto.TargetDisposeBrowserContext{BrowserContextID: res.BrowserContextID}.Call(root)
		if bridge != nil {
			_ = bridge.Close()
		}
		return nil, fmt.Errorf("tab: %w", err)
	}
	page = page.Context(ctx)

	sess := &chromeSession{
		page:      page,
		root:      root,
		contextID: res.BrowserContextID,
		bridge:    bridge,
	}

	if err := page.Timeout(30 * time.Second).Navigate(meetingURL); err != nil {
		_ = sess.Close()
		return nil, fmt.Errorf("navigate: %w", err)
	}
	_ = page.Timeout(15 * time.Second).WaitLoad()

	if err := fillGuestName(page, fio); err != nil {
		log.Printf("bbb: guest form: %v", err)
	}
	if err := clickListenOnly(page); err != nil {
		log.Printf("bbb: listen-only: %v", err)
	}
	return sess, nil
}

func fillGuestName(page *rod.Page, fio string) error {
	p := page.Timeout(15 * time.Second)
	el, err := p.Race().
		Element("#join_name").
		Element("#join-name").
		Element("input[name='join_name']").
		Element("input[name='name']").
		Element("input[autocomplete='name']").
		Element("input[id*='name'][type='text']").
		Do()
	if err != nil {
		return err
	}
	_ = el.SelectAllText()
	if err := el.Input(fio); err != nil {
		return err
	}
	if ok, box, _ := page.Has("input[type='checkbox']"); ok && box != nil {
		_ = box.Click(proto.InputMouseButtonLeft, 1)
	}
	btn, err := page.Timeout(8*time.Second).Race().
		Element("button[type='submit']").
		ElementR("button", `(?i)join|войти|подключ`).
		Do()
	if err != nil {
		return err
	}
	if err := btn.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return err
	}
	_ = page.Timeout(20 * time.Second).WaitLoad()
	return nil
}

func clickListenOnly(page *rod.Page) error {
	p := page.Timeout(40 * time.Second)
	_, err := p.Race().
		Element("[data-test='listenOnlyBtn']").Handle(clickLeft).
		Element("[data-test='helpListenOnlyBtn']").Handle(clickLeft).
		ElementR("button", `(?i)listen only|только слушать`).Handle(clickLeft).
		Element("[data-test='waitingUsers']").Handle(noopEl).
		Element("[data-test='userListItem']").Handle(noopEl).
		Do()
	return err
}

func clickLeft(el *rod.Element) error {
	return el.Click(proto.InputMouseButtonLeft, 1)
}

func noopEl(*rod.Element) error { return nil }
