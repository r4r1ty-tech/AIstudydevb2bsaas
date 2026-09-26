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
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
	"github.com/go-rod/rod/lib/proto"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/capture"
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
		log.Printf("bbb: chromium-rec %s", bin)
	} else {
		c.launcher = l
		c.browser = b
		log.Printf("bbb: chromium %s", bin)
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

func (s *chromeSession) InMeeting(ctx context.Context) (bool, error) {
	if s == nil || s.page == nil {
		return false, nil
	}
	p := s.page.Context(ctx).Timeout(3 * time.Second)
	if hasAny(p, meetingSels) {
		return true, nil
	}
	return false, nil
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
	lobby, err := s.InLobby(ctx)
	if err != nil {
		return err
	}
	if lobby {
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
	proxyHint := "direct"
	if strings.TrimSpace(req.SOCKS5) != "" {
		proxyHint = "socks5"
	}
	log.Printf("bbb: join start url=%s name=%q role=%d proxy=%s", req.URL, req.FIO, req.Role, proxyHint)

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
		log.Printf("bbb: navigate fail: %v | %s", err, pageSnap(page))
		_ = sess.Close()
		return nil, fmt.Errorf("navigate: %w", err)
	}
	if err := page.Timeout(15 * time.Second).WaitLoad(); err != nil {
		log.Printf("bbb: waitload: %v | %s", err, pageSnap(page))
	} else {
		log.Printf("bbb: navigated | %s", pageSnap(page))
	}

	if err := fillGuestName(page, req.FIO); err != nil {
		log.Printf("bbb: guest form fail: %v | %s | hits=%s", err, pageSnap(page), guestFormHits(page))
		_ = sess.Close()
		return nil, fmt.Errorf("guest form: %w", err)
	}
	log.Printf("bbb: guest form ok name=%q | %s", req.FIO, pageSnap(page))

	if err := waitJoined(page, 45*time.Second); err != nil {
		log.Printf("bbb: waitJoined fail: %v | %s | hits=%s", err, pageSnap(page), joinHits(page))
		_ = sess.Close()
		return nil, err
	}
	log.Printf("bbb: waitJoined ok | %s | hits=%s", pageSnap(page), joinHits(page))

	if lobby, lerr := sess.InLobby(ctx); lerr != nil {
		log.Printf("bbb: InLobby err: %v", lerr)
	} else if lobby {
		log.Printf("bbb: in lobby | %s", pageSnap(page))
		return sess, nil
	}
	if req.Role == RolePresence {
		if err := dismissAudio(page); err != nil {
			log.Printf("bbb: skip audio: %v | %s | hits=%s", err, pageSnap(page), joinHits(page))
		} else {
			log.Printf("bbb: audio dismissed/skipped | %s", pageSnap(page))
		}
	} else if err := clickListenOnly(page); err != nil {
		log.Printf("bbb: listen-only: %v | %s | hits=%s", err, pageSnap(page), joinHits(page))
	} else {
		log.Printf("bbb: listen-only ok | %s", pageSnap(page))
	}
	if ok, merr := sess.InMeeting(ctx); merr != nil {
		log.Printf("bbb: InMeeting err: %v", merr)
	} else if ok {
		log.Printf("bbb: in meeting | %s", pageSnap(page))
		return sess, nil
	}
	if lobby, _ := sess.InLobby(ctx); lobby {
		log.Printf("bbb: lobby after audio | %s", pageSnap(page))
		return sess, nil
	}
	log.Printf("bbb: not in room after join | %s | hits=%s", pageSnap(page), joinHits(page))
	_ = sess.Close()
	return nil, fmt.Errorf("не в комнате после захода")
}

func fillGuestName(page *rod.Page, fio string) error {
	p := page.Timeout(20 * time.Second)
	el, err := p.Race().
		Element("input.join-form[type='text']").
		Element("input[name*='join_name']").
		Element("input[id*='join_name']").
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
	log.Printf("bbb: guest name field found")
	_ = el.SelectAllText()
	if err := el.Input(fio); err != nil {
		return fmt.Errorf("input name: %w", err)
	}
	// Prefer JS submit: rod mouse Click often dies with "context deadline exceeded"
	// on Greenlight (#room-join present but not stably actionable under CDP).
	if err := submitGuestJoin(page); err != nil {
		return err
	}
	if err := page.Timeout(20 * time.Second).WaitLoad(); err != nil {
		log.Printf("bbb: post-join waitload: %v", err)
	}
	return nil
}

func submitGuestJoin(page *rod.Page) error {
	p := page.Timeout(10 * time.Second)
	res, err := p.Eval(`() => {
		const btn = document.querySelector('#room-join')
			|| document.querySelector('button.join-form[type="submit"]')
			|| document.querySelector('form button[type="submit"]');
		if (btn) {
			btn.removeAttribute('disabled');
			btn.click();
			return 'btn:' + (btn.id || btn.className || 'submit');
		}
		const form = document.querySelector('form');
		if (form) {
			if (typeof form.requestSubmit === 'function') form.requestSubmit();
			else form.submit();
			return 'form';
		}
		return '';
	}`)
	if err == nil && res != nil {
		if how := strings.TrimSpace(res.Value.Str()); how != "" {
			log.Printf("bbb: join submitted via js (%s)", how)
			return nil
		}
	}
	if clickByText(p, joinNameRE) {
		log.Printf("bbb: join clicked by text fallback")
		return nil
	}
	if err != nil {
		return fmt.Errorf("join submit: %w", err)
	}
	return fmt.Errorf("join submit: кнопка/форма не найдены")
}

func waitJoined(page *rod.Page, d time.Duration) error {
	if page == nil {
		return fmt.Errorf("нет вкладки")
	}
	deadline := time.Now().Add(d)
	lastLog := time.Time{}
	for time.Now().Before(deadline) {
		p := page.Timeout(3 * time.Second)
		if hasAny(p, lobbySels) {
			log.Printf("bbb: waitJoined hit lobby sel | %s", pageSnap(page))
			return nil
		}
		if sel := firstHit(p, meetingSels); sel != "" {
			log.Printf("bbb: waitJoined hit meeting sel=%s | %s", sel, pageSnap(page))
			return nil
		}
		html, err := p.HTML()
		if err == nil {
			low := strings.ToLower(html)
			for _, m := range []string{
				"waiting for a moderator",
				"waiting for the moderator",
				"you'll join when",
				"guest lobby",
				"ожидайте",
			} {
				if strings.Contains(low, m) {
					log.Printf("bbb: waitJoined hit lobby text=%q | %s", m, pageSnap(page))
					return nil
				}
			}
		}
		if time.Since(lastLog) >= 5*time.Second {
			log.Printf("bbb: waitJoined… left=%s | %s | hits=%s",
				time.Until(deadline).Round(time.Second), pageSnap(page), joinHits(page))
			lastLog = time.Now()
		}
		time.Sleep(400 * time.Millisecond)
	}
	return fmt.Errorf("нет лобби/комнаты после формы гостя")
}

func pageSnap(page *rod.Page) string {
	if page == nil {
		return "page=nil"
	}
	p := page.Timeout(3 * time.Second)
	url := "?"
	title := "-"
	body := "-"
	if t, err := p.Eval(`() => ({
		url: location.href || '',
		title: document.title || '',
		body: ((document.body && (document.body.innerText || document.body.textContent) || '').replace(/\s+/g, ' ').trim()).slice(0, 220)
	})`); err == nil && t != nil {
		m := t.Value.Map()
		if v, ok := m["url"]; ok {
			url = v.Str()
		}
		if v, ok := m["title"]; ok {
			if s := strings.TrimSpace(v.Str()); s != "" {
				title = s
			}
		}
		if v, ok := m["body"]; ok {
			if s := strings.TrimSpace(v.Str()); s != "" {
				body = s
			}
		}
	}
	return fmt.Sprintf("url=%s title=%q body=%q", url, title, body)
}

func guestFormHits(page *rod.Page) string {
	sels := []string{
		"input.join-form[type='text']",
		"input[name*='join_name']",
		"input[id*='join_name']",
		"#join_name",
		"#room-join",
		"button.join-form[type='submit']",
		"button[type='submit']",
		"input[type='text']",
		"form",
	}
	return selHits(page, sels)
}

func joinHits(page *rod.Page) string {
	sels := append(append([]string{}, lobbySels...), meetingSels...)
	sels = append(sels,
		"input.join-form[type='text']",
		"#room-join",
		"[data-test='audioModal']",
		"[data-test='listeningButton']",
		"button[aria-label*='Listen']",
		"button[aria-label*='слушать']",
	)
	return selHits(page, sels)
}

func selHits(page *rod.Page, sels []string) string {
	if page == nil {
		return "-"
	}
	p := page.Timeout(2 * time.Second)
	var hit []string
	for _, sel := range sels {
		if ok, _, err := p.Has(sel); err == nil && ok {
			hit = append(hit, sel)
		}
	}
	if len(hit) == 0 {
		return "none"
	}
	return strings.Join(hit, ",")
}

func firstHit(page *rod.Page, sels []string) string {
	if page == nil {
		return ""
	}
	for _, sel := range sels {
		if ok, _, err := page.Has(sel); err == nil && ok {
			return sel
		}
	}
	return ""
}

var listenOnlySels = []string{
	"[data-test='listenOnlyBtn']",
	"[data-test='helpListenOnlyBtn']",
	"[data-test='listenOnlyJoin']",
	`button[aria-label='Listen only']`,
	`button[aria-label='Только слушать']`,
}

var lobbySels = []string{
	"[data-test='waitingUsers']",
	"[data-test='waitingusers']",
}

var meetingSels = []string{
	"[data-test='userListItem']",
	"[data-test='chatButton']",
	"[data-test='publicChatTab']",
	"[data-test='whiteboard']",
	"[data-test='presentationInner']",
	"[data-test='listenOnlyBtn']",
	"[data-test='closeModalButton']",
}

// inMeetingSels: lobby OR meeting — used while waiting for audio UI.
var inMeetingSels = append(append([]string{}, lobbySels...), meetingSels...)

const (
	listenOnlyRE = `(?i)listen\s*only|только\s*слушать`
	joinNameRE   = `(?i)join|войти|подключ|присоедин`
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
