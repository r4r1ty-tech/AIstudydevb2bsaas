package bbb

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
	"github.com/go-rod/rod/lib/proto"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/capture"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

type ChromeJoiner struct {
	Bin         string
	UserDataDir string

	mu          sync.Mutex
	browser     *rod.Browser
	recBrowser  *rod.Browser
	launcher    *launcher.Launcher
	recLauncher *launcher.Launcher
}

func NewChromeJoiner(bin string) *ChromeJoiner {
	if bin == "" {
		bin = FindChrome("")
	}
	return &ChromeJoiner{Bin: bin, UserDataDir: chromeUserDir()}
}

func chromeUserDir() string {
	if d := strings.TrimSpace(os.Getenv("CHROME_USER_DATA_DIR")); d != "" {
		return d
	}
	return filepath.Join(os.TempDir(), "ssau-bbb-chrome")
}

func (c *ChromeJoiner) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.browser != nil {
		_ = c.browser.Close()
		c.browser = nil
	}
	if c.recBrowser != nil {
		_ = c.recBrowser.Close()
		c.recBrowser = nil
	}
	if c.launcher != nil {
		c.launcher.Kill()
		c.launcher = nil
	}
	if c.recLauncher != nil {
		c.recLauncher.Kill()
		c.recLauncher = nil
	}
	return nil
}

func (c *ChromeJoiner) ensureBrowser() (*rod.Browser, error) {
	return c.ensure(false)
}

func (c *ChromeJoiner) ensure(record bool) (*rod.Browser, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if record && c.recBrowser != nil {
		return c.recBrowser, nil
	}
	if !record && c.browser != nil {
		return c.browser, nil
	}
	bin := c.Bin
	if bin == "" {
		bin = FindChrome("")
	}
	if bin == "" {
		return nil, fmt.Errorf("chromium не найден — apt install chromium или CHROME_BIN")
	}

	dir := c.UserDataDir
	if dir == "" {
		dir = chromeUserDir()
	}
	if record {
		dir = dir + "-rec"
	}
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
		Set(flags.Flag("use-fake-ui-for-media-stream"))

	if record {
		l = l.Env(capture.PulseEnv()...)
	} else {
		l = l.Set(flags.Flag("use-fake-device-for-media-stream"))
	}

	u, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("chrome launch: %w", err)
	}
	b := rod.New().ControlURL(u).NoDefaultDevice()
	if err := b.Connect(); err != nil {
		l.Kill()
		return nil, fmt.Errorf("chrome connect: %w", err)
	}
	if record {
		c.recLauncher = l
		c.recBrowser = b
		logx.Infof("bbb", "chromium-rec %s", bin)
	} else {
		c.launcher = l
		c.browser = b
		logx.Infof("bbb", "chromium %s", bin)
	}
	return b, nil
}

type chromeSession struct {
	page      *rod.Page
	root      *rod.Browser
	contextID proto.BrowserBrowserContextID
	bridge    *socksBridge

	greetMu sync.Mutex
	greeted bool
}

func (s *chromeSession) Greet(ctx context.Context) error {
	if s == nil || s.page == nil {
		return nil
	}
	s.greetMu.Lock()
	done := s.greeted
	s.greetMu.Unlock()
	if done {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	st, err := s.seat(ctx)
	if err != nil {
		return err
	}
	if st != seatRoom {
		return nil
	}
	if err := sendHello(s.page.Context(ctx)); err != nil {
		return err
	}
	s.greetMu.Lock()
	s.greeted = true
	s.greetMu.Unlock()
	return nil
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

func (c *ChromeJoiner) Join(ctx context.Context, req JoinReq) (Session, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	root, err := c.ensure(req.Role == RoleRecord)
	if err != nil {
		return nil, err
	}

	bridge, err := startSOCKSBridge(req.SOCKS5)
	if err != nil {
		return nil, err
	}
	proxyURL, err := chromeProxyURL(req.SOCKS5, bridge)
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

	if err := page.Timeout(30 * time.Second).Navigate(req.URL); err != nil {
		_ = sess.Close()
		return nil, fmt.Errorf("navigate: %w", err)
	}
	_ = page.Timeout(15 * time.Second).WaitLoad()

	if err := sess.waitSeated(ctx, req); err != nil {
		_ = sess.Close()
		return nil, err
	}
	st, _ := sess.seat(ctx)
	logx.Infof("bbb", "seat=%s %s", st, pageHint(page))
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
	btn, err := page.Timeout(8 * time.Second).Race().
		Element("button[type='submit']").
		Element("[data-test='joinButton']").
		Element("[data-test='sessionJoinButton']").
		Do()
	if err != nil {
		if !clickByText(page.Timeout(4*time.Second), joinNameRE) {
			return err
		}
	} else if err := btn.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return err
	}
	_ = page.Timeout(8 * time.Second).WaitLoad()
	return nil
}

var listenOnlySels = []string{
	"[data-test='listenOnlyBtn']",
	"[data-test='helpListenOnlyBtn']",
	"[data-test='listenOnlyJoin']",
	`button[aria-label='Listen only']`,
	`button[aria-label='Только слушать']`,
}

var inMeetingSels = roomSels

const (
	listenOnlyRE = `(?i)listen\s*only|только\s*слушать`
	joinNameRE   = `(?i)join|войти|подключ`
	chatOpenRE   = `(?i)public chat|публичн.*чат|открыть чат`
	helloText    = "Здравствуйте"
)

var chatOpenSels = []string{
	"[data-test='chatButton']",
	"[data-test='publicChatTab']",
	"[data-test='publicChat']",
	`button[aria-label='Public Chat']`,
	`button[aria-label='Публичный чат']`,
}

var chatInputSels = []string{
	"[data-test='messageInput']",
	"[data-test='chatInput']",
	"textarea#message-input",
	"#message-input",
	"textarea[id*='message']",
	"textarea[placeholder]",
	`[contenteditable='true'][data-test*='message']`,
}

var chatSendSels = []string{
	"[data-test='sendMessageButton']",
	"[data-test='sendMessageBtn']",
	`button[aria-label='Send message']`,
	`button[aria-label='Отправить']`,
}

func sendHello(page *rod.Page) error {
	var last error
	for i := 0; i < 8; i++ {
		p := page.Timeout(2 * time.Second)
		_ = clickFirst(p, chatOpenSels)
		_ = clickByText(p, chatOpenRE)
		last = typeHello(p)
		if last == nil {
			return nil
		}
		time.Sleep(400 * time.Millisecond)
	}
	if last == nil {
		last = fmt.Errorf("поле чата не найдено")
	}
	return last
}

func typeHello(page *rod.Page) error {
	el, err := findFirst(page, chatInputSels)
	if err != nil {
		return err
	}
	_ = el.Click(proto.InputMouseButtonLeft, 1)
	_ = el.SelectAllText()
	if err := el.Input(helloText); err != nil {
		return err
	}
	if clickFirst(page, chatSendSels) {
		return nil
	}
	if err := el.Type(input.Enter); err != nil {
		return fmt.Errorf("отправить: %w", err)
	}
	return nil
}

func findFirst(page *rod.Page, sels []string) (*rod.Element, error) {
	for _, sel := range sels {
		ok, el, err := page.Has(sel)
		if err == nil && ok && el != nil {
			return el, nil
		}
	}
	return nil, fmt.Errorf("поле чата не найдено")
}

func clickListenOnly(page *rod.Page) error {
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		p := page.Timeout(3 * time.Second)
		if clickFirst(p, listenOnlySels) || clickByText(p, listenOnlyRE) {
			return nil
		}
		// Lobby / already in the roster: audio modal may never appear.
		if hasAny(p, inMeetingSels) {
			return nil
		}
		time.Sleep(400 * time.Millisecond)
	}
	return fmt.Errorf("кнопка «только слушать» не найдена")
}

var closeAudioSels = []string{
	"[data-test='closeModalButton']",
	"[data-test='closeModal']",
	"[data-test='closeButton']",
	`button[aria-label='Close']`,
	`button[aria-label='Закрыть']`,
}

func dismissAudio(page *rod.Page) error {
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		p := page.Timeout(2 * time.Second)
		if clickFirst(p, closeAudioSels) || clickByText(p, `(?i)close|закрыть|skip|пропуст`) {
			return nil
		}
		if hasAny(p, inMeetingSels) {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return clickListenOnly(page)
}

func hasAny(page *rod.Page, sels []string) bool {
	for _, sel := range sels {
		if ok, _, err := page.Has(sel); err == nil && ok {
			return true
		}
	}
	return false
}

func clickFirst(page *rod.Page, sels []string) bool {
	for _, sel := range sels {
		ok, el, err := page.Has(sel)
		if err != nil || !ok || el == nil {
			continue
		}
		if err := el.Click(proto.InputMouseButtonLeft, 1); err == nil {
			return true
		}
	}
	return false
}

func clickByText(page *rod.Page, goRE string) bool {
	jsRE := jsRegexp(goRE)
	if jsRE == "" {
		return false
	}
	res, err := page.Eval(`(re) => {
		const rx = new RegExp(re, 'i')
		const nodes = document.querySelectorAll('button, [role="button"], [data-test], span, div, a')
		for (const n of nodes) {
			const t = ((n.innerText || '') + ' ' + (n.getAttribute('aria-label') || '')).trim()
			if (t && rx.test(t)) { n.click(); return true }
		}
		return false
	}`, jsRE)
	if err != nil || res == nil {
		return false
	}
	return res.Value.Bool()
}

// jsRegexp strips Go/PCRE inline flags — rod Eval runs in the browser.
func jsRegexp(goRE string) string {
	s := strings.TrimSpace(goRE)
	s = strings.TrimPrefix(s, "(?i)")
	s = strings.TrimPrefix(s, "(?m)")
	s = strings.TrimPrefix(s, "(?s)")
	return s
}
