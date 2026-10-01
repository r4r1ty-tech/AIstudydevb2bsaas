package bbb

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
	"github.com/go-rod/rod/lib/proto"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/capture"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/proxyrelay"
)

type ChromeJoiner struct {
	Bin         string
	UserDataDir string

	// Proxies, if set, gives every tab its own upstream via a browser context.
	Proxies *proxyrelay.Pool

	mu          sync.Mutex
	browser     *rod.Browser
	recBrowser  *rod.Browser
	launcher    *launcher.Launcher
	recLauncher *launcher.Launcher
}

func NewChromeJoiner(bin string) *ChromeJoiner {
	logx.Debugf("bbb", "NewChromeJoiner: bin=%q", bin)
	if bin == "" {
		bin = FindChrome("")
	}
	dir := chromeUserDir()
	logx.Debugf("bbb", "NewChromeJoiner: bin=%q userDataDir=%q", bin, dir)
	return &ChromeJoiner{Bin: bin, UserDataDir: dir}
}

func chromeUserDir() string {
	if d := strings.TrimSpace(os.Getenv("CHROME_USER_DATA_DIR")); d != "" {
		logx.Debugf("bbb", "chromeUserDir: env=%q", d)
		return d
	}
	d := filepath.Join(os.TempDir(), "ssau-bbb-chrome")
	logx.Debugf("bbb", "chromeUserDir: default=%q", d)
	return d
}

func (c *ChromeJoiner) Close() error {
	logx.Debugf("bbb", "ChromeJoiner.Close: browser=%v recBrowser=%v", c.browser != nil, c.recBrowser != nil)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.browser != nil {
		if err := c.browser.Close(); err != nil {
			logx.Debugf("bbb", "ChromeJoiner.Close: browser close: %v", err)
		}
		c.browser = nil
	}
	if c.recBrowser != nil {
		if err := c.recBrowser.Close(); err != nil {
			logx.Debugf("bbb", "ChromeJoiner.Close: recBrowser close: %v", err)
		}
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
	if c.Proxies != nil {
		if err := c.Proxies.Close(); err != nil {
			logx.Debugf("bbb", "ChromeJoiner.Close: proxies close: %v", err)
		}
		c.Proxies = nil
	}
	logx.Debugf("bbb", "ChromeJoiner.Close: done")
	return nil
}

func (c *ChromeJoiner) ensureBrowser() (*rod.Browser, error) {
	logx.Debugf("bbb", "ensureBrowser: quality=false")
	return c.ensure(false)
}

// quality=false — минимальный браузер для зрителей (крошечное окно, без звука).
// quality=true — окно побольше: запись и снятие слайдов.
func (c *ChromeJoiner) ensure(quality bool) (*rod.Browser, error) {
	logx.Debugf("bbb", "ensure: quality=%v", quality)
	c.mu.Lock()
	defer c.mu.Unlock()
	if quality && c.recBrowser != nil {
		if browserAlive(c.recBrowser) {
			logx.Debugf("bbb", "ensure: reuse recBrowser")
			return c.recBrowser, nil
		}
		logx.Warnf("bbb", "ensure: chromium-rec не отвечает — перезапускаю")
		c.dropLocked(true)
	}
	if !quality && c.browser != nil {
		if browserAlive(c.browser) {
			logx.Debugf("bbb", "ensure: reuse browser")
			return c.browser, nil
		}
		logx.Warnf("bbb", "ensure: chromium не отвечает — перезапускаю")
		c.dropLocked(false)
	}
	bin := c.Bin
	if bin == "" {
		bin = FindChrome("")
	}
	if bin == "" {
		err := fmt.Errorf("chromium не найден — apt install chromium или CHROME_BIN")
		logx.Errorf("bbb", "ensure: %v", err)
		return nil, err
	}

	dir := c.UserDataDir
	if dir == "" {
		dir = chromeUserDir()
	}
	if quality {
		dir = dir + "-rec"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logx.Debugf("bbb", "ensure: mkdir %s: %v", dir, err)
	}
	clearStaleLock(dir)

	l := launcher.New().
		Bin(bin).
		HeadlessNew(true).
		NoSandbox(true).
		Leakless(false).
		UserDataDir(dir).
		Set(flags.Flag("disable-gpu")).
		Set(flags.Flag("disable-dev-shm-usage")).
		Set(flags.Flag("disable-crash-reporter")).
		Set(flags.Flag("disable-breakpad")).
		Set(flags.Flag("disable-background-networking")).
		Set(flags.Flag("disable-sync")).
		Set(flags.Flag("disable-default-apps")).
		Set(flags.Flag("disable-component-extensions-with-background-pages")).
		Set(flags.Flag("no-first-run")).
		Set(flags.Flag("no-default-browser-check")).
		Set(flags.Flag("autoplay-policy"), "no-user-gesture-required").
		Set(flags.Flag("use-fake-ui-for-media-stream")).
		Set(flags.Flag("process-per-site")).
		Set(flags.Flag("renderer-process-limit"), "2").
		Set(flags.Flag("js-flags"), "--max-old-space-size=128").
		Set(flags.Flag("disk-cache-size"), "1").
		Set(flags.Flag("media-cache-size"), "1").
		Set(flags.Flag("disable-features"), "AudioServiceOutOfProcess")

	if quality {
		// Sink должен существовать до старта браузера, иначе Chromium создаст
		// поток на дефолтном устройстве, а не на ssau_rec.monitor.
		if err := capture.EnsureSink(); err != nil {
			logx.Warnf("bbb", "ensure: ensure sink: %v", err)
		}
		if err := capture.EnsureDefaultSink(); err != nil {
			logx.Warnf("bbb", "ensure: default sink: %v", err)
		}
		// Одна вкладка, чей звук идёт в null-sink на запись и вейкворды.
		l = l.Env(capture.PulseEnv()...).
			Set(flags.Flag("window-size"), "1280,800")
	} else {
		// Зрители: крошечное окно, фейковый микрофон, без вывода звука.
		// Не дублируют запись и жрут минимум CPU/RAM.
		l = l.Set(flags.Flag("use-fake-device-for-media-stream")).
			Set(flags.Flag("mute-audio")).
			Set(flags.Flag("window-size"), "320,240")
	}

	// Stderr Chromium шумный; оставляем только то, что про звук и WebRTC —
	// иначе «вкладка в аудио, а sink-input нет» не разобрать.
	l = l.Logger(logx.LineWriter(logx.LevelWarn, "bbb", "chromium: ", chromeAudioLine))
	u, err := l.Launch()
	if err != nil {
		logx.Errorf("bbb", "ensure: launch bin=%s quality=%v: %v", bin, quality, err)
		return nil, fmt.Errorf("chrome launch: %w", err)
	}
	b := rod.New().ControlURL(u).NoDefaultDevice()
	if err := b.Connect(); err != nil {
		l.Kill()
		logx.Errorf("bbb", "ensure: connect %s quality=%v: %v", u, quality, err)
		return nil, fmt.Errorf("chrome connect: %w", err)
	}
	if quality {
		c.recLauncher = l
		c.recBrowser = b
		logx.Infof("bbb", "chromium-rec %s", bin)
		capture.LogRoute("bbb", "chrome-rec-launched")
	} else {
		c.launcher = l
		c.browser = b
		logx.Infof("bbb", "chromium %s", bin)
	}
	logx.Debugf("bbb", "ensure: ready quality=%v dir=%s", quality, dir)
	return b, nil
}

// browserAlive: процесс Chromium жив и отвечает по CDP (после OOM-kill кэш
// иначе отдаёт мёртвый браузер, и все заходы падают до рестарта сервиса).
func browserAlive(b *rod.Browser) bool {
	if b == nil {
		return false
	}
	_, err := proto.BrowserGetVersion{}.Call(b.Context(context.Background()).Timeout(3 * time.Second))
	return err == nil
}

// reset забывает браузер нужного качества: следующий ensure запустит новый.
func (c *ChromeJoiner) reset(quality bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if quality && c.recBrowser != nil && !browserAlive(c.recBrowser) {
		c.dropLocked(true)
	}
	if !quality && c.browser != nil && !browserAlive(c.browser) {
		c.dropLocked(false)
	}
}

func (c *ChromeJoiner) dropLocked(quality bool) {
	if quality {
		if c.recLauncher != nil {
			c.recLauncher.Kill()
		}
		c.recBrowser, c.recLauncher = nil, nil
		return
	}
	if c.launcher != nil {
		c.launcher.Kill()
	}
	c.browser, c.launcher = nil, nil
}

// clearStaleLock убирает SingletonLock профиля, если процесс-владелец мёртв
// (Chromium пережил SIGKILL bbb или упал): иначе новый запуск не стартует.
func clearStaleLock(dir string) {
	lock := filepath.Join(dir, "SingletonLock")
	target, err := os.Readlink(lock)
	if err != nil {
		return
	}
	i := strings.LastIndex(target, "-")
	if i < 0 {
		return
	}
	pid, err := strconv.Atoi(target[i+1:])
	if err != nil || pid <= 0 {
		return
	}
	if syscall.Kill(pid, 0) == nil {
		return // жив — не трогаем
	}
	for _, f := range []string{"SingletonLock", "SingletonSocket", "SingletonCookie"} {
		_ = os.Remove(filepath.Join(dir, f))
	}
	logx.Warnf("bbb", "clearStaleLock: снят протухший lock профиля %s (pid %d)", dir, pid)
}

type chromeSession struct {
	page *rod.Page

	ctxBrowser *rod.Browser
	role       Role

	audioMu sync.Mutex
	audioOK bool

	greetMu sync.Mutex
	greeted bool
}

func (s *chromeSession) Greet(ctx context.Context) error {
	if s == nil || s.page == nil {
		logx.Debugf("bbb", "chromeSession.Greet: no page, skip")
		return nil
	}
	logx.Debugf("bbb", "chromeSession.Greet: enter")
	s.greetMu.Lock()
	done := s.greeted
	s.greetMu.Unlock()
	if done {
		logx.Debugf("bbb", "chromeSession.Greet: already greeted")
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	st, err := s.seat(ctx)
	if err != nil {
		logx.Errorf("bbb", "chromeSession.Greet: seat: %v", err)
		return fmt.Errorf("chromeSession.Greet: seat: %w", err)
	}
	if st != seatRoom {
		logx.Debugf("bbb", "chromeSession.Greet: seat=%s, skip", st)
		return nil
	}
	if err := sendHello(s.page.Context(ctx)); err != nil {
		logx.Errorf("bbb", "chromeSession.Greet: hello: %v", err)
		return fmt.Errorf("chromeSession.Greet: hello: %w", err)
	}
	s.greetMu.Lock()
	s.greeted = true
	s.greetMu.Unlock()
	logx.Infof("bbb", "greeted page=%s", pageHint(s.page))
	return nil
}

// EnsureAudio для записывающей вкладки: проверяет, что она в аудио, и если
// нет — короткая попытка нажать «Только слушать» (после лобби, после сбоя WebRTC).
func (s *chromeSession) EnsureAudio(ctx context.Context) bool {
	if s == nil || s.page == nil || s.role != RoleRecord {
		return true
	}
	if !s.audioMu.TryLock() {
		return true // проверка уже идёт с прошлого тика
	}
	defer s.audioMu.Unlock()
	p := s.page.Context(ctx)
	if hasAny(p.Timeout(2*time.Second), audioJoinedSels) {
		s.audioOK = true
		return true
	}
	s.audioOK = audioTry(p, s.role, 8*time.Second)
	return s.audioOK
}

func (s *chromeSession) Close() error {
	if s == nil {
		return nil
	}
	logx.Debugf("bbb", "chromeSession.Close: page=%v ctx=%v", s.page != nil, s.ctxBrowser != nil)
	// Свежий контекст с таймаутом: контекст захода мог быть отменён (shutdown),
	// а зависший Chromium без таймаута подвесит тик воркера.
	if s.page != nil {
		if err := s.page.Context(context.Background()).Timeout(5 * time.Second).Close(); err != nil {
			logx.Debugf("bbb", "chromeSession.Close: page close: %v", err)
		}
	}
	if s.ctxBrowser != nil {
		if err := s.ctxBrowser.Context(context.Background()).Timeout(5 * time.Second).Close(); err != nil {
			logx.Debugf("bbb", "chromeSession.Close: context dispose: %v", err)
		} else {
			logx.Debugf("bbb", "chromeSession.Close: context disposed")
		}
	}
	return nil
}

// maxJoinAttempts is how many different proxies one join may try before giving up.
const maxJoinAttempts = 3

// withListenOnlyAudio asks the BBB client to auto-join audio in listen-only
// mode, so the recording tab does not depend on clicking the audio modal.
func withListenOnlyAudio(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" {
		logx.Debugf("bbb", "withListenOnlyAudio: keep raw url err=%v", err)
		return raw
	}
	q := u.Query()
	q.Set("userdata-bbb_auto_join_audio", "true")
	q.Set("userdata-bbb_force_listen_only", "true")
	q.Set("userdata-bbb_listen_only_mode", "true")
	q.Set("userdata-bbb_skip_check_audio", "true")
	u.RawQuery = q.Encode()
	logx.Infof("bbb", "withListenOnlyAudio: url=%s", redactURL(u.String()))
	return u.String()
}

func (c *ChromeJoiner) Join(ctx context.Context, req JoinReq) (Session, error) {
	logx.Debugf("bbb", "ChromeJoiner.Join: role=%s fio=%q url=%s", req.Role, req.FIO, redactURL(req.URL))
	if ctx == nil {
		ctx = context.Background()
	}
	root, err := c.ensure(req.Role != RolePresence)
	if err != nil {
		logx.Errorf("bbb", "ChromeJoiner.Join: ensure: %v", err)
		return nil, fmt.Errorf("ChromeJoiner.Join: ensure: %w", err)
	}

	attempts := 1
	if c.Proxies.Len() > 0 {
		attempts = maxJoinAttempts
		if c.Proxies.Len() < attempts {
			attempts = c.Proxies.Len()
		}
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var slot *proxyrelay.Slot
		if c.Proxies.Len() > 0 {
			s, perr := c.Proxies.Next()
			if perr != nil {
				// Прокси настроены, но все мертвы: напрямую с IP сервера не идём молча.
				logx.Errorf("bbb", "ChromeJoiner.Join: %v", perr)
				if lastErr != nil {
					return nil, fmt.Errorf("%w (последняя ошибка: %v)", perr, lastErr)
				}
				return nil, perr
			}
			slot = s
		}
		sess, err := c.attemptJoin(ctx, root, req, slot)
		if err == nil {
			slot.OK()
			return sess, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			// Остановка/пауза — не вина прокси.
			return nil, err
		}
		if isBrowserGone(err) {
			c.reset(req.Role != RolePresence)
		}
		if slot == nil {
			logx.Errorf("bbb", "ChromeJoiner.Join: attempt=%d direct failed: %v", attempt, err)
			return nil, err
		}
		if !isProxyErr(err) {
			// Форма, лобби, «встреча не началась» — смена прокси не поможет,
			// а штраф убил бы рабочий прокси. Повтор — бэкофф воркера.
			logx.Warnf("bbb", "ChromeJoiner.Join: attempt=%d proxy=%s: %v — не сетевая ошибка, прокси не виноват", attempt, slot.Redacted(), err)
			return nil, err
		}
		slot.Fail()
		logx.Warnf("bbb", "ChromeJoiner.Join: attempt=%d proxy=%s failed: %v — меняю прокси", attempt, slot.Redacted(), err)
	}
	logx.Errorf("bbb", "ChromeJoiner.Join: все %d попытки провалились: %v", attempts, lastErr)
	return nil, lastErr
}

// isProxyErr: сбой сети/прокси (вкладка не открылась, страница не загрузилась),
// а не поведение BBB-страницы.
func isProxyErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, m := range []string{"proxy context", "navigate:", "ERR_PROXY", "ERR_TUNNEL", "ERR_SOCKS", "ERR_CONNECTION", "ERR_TIMED_OUT", "ERR_EMPTY_RESPONSE", "ERR_NAME_NOT_RESOLVED", "ERR_ADDRESS_UNREACHABLE"} {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}

// isBrowserGone: процесс Chromium умер или CDP-сокет закрыт — кэш браузера пуст.
func isBrowserGone(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "tab:") || strings.Contains(msg, "websocket") || strings.Contains(msg, "use of closed network connection") || strings.Contains(msg, "connection refused")
}

// attemptJoin performs one join through an optional proxy slot, in its own
// browser context so the proxy applies to this tab only.
func (c *ChromeJoiner) attemptJoin(ctx context.Context, root *rod.Browser, req JoinReq, slot *proxyrelay.Slot) (Session, error) {
	browser := root
	var ctxBrowser *rod.Browser
	proxyLabel := "direct"
	if slot != nil {
		res, cerr := proto.TargetCreateBrowserContext{ProxyServer: slot.LocalURL()}.Call(root)
		if cerr != nil {
			// Раньше тут шли напрямую и засчитывали прокси успех — заход с IP сервера
			// и скрытый сломанный прокси.
			logx.Warnf("bbb", "attemptJoin: proxy context %s: %v", slot.Redacted(), cerr)
			return nil, fmt.Errorf("proxy context: %w", cerr)
		}
		sub := *root
		sub.BrowserContextID = res.BrowserContextID
		browser = &sub
		ctxBrowser = &sub
		proxyLabel = slot.Redacted()
	}
	logx.Infof("bbb", "attemptJoin: proxy=%s url=%s role=%s", proxyLabel, redactURL(req.URL), req.Role)

	page, err := browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		logx.Errorf("bbb", "attemptJoin: tab: %v", err)
		if ctxBrowser != nil {
			if derr := ctxBrowser.Close(); derr != nil {
				logx.Debugf("bbb", "attemptJoin: dispose after tab fail: %v", derr)
			}
		}
		return nil, fmt.Errorf("tab: %w", err)
	}
	page = page.Context(ctx)

	sess := &chromeSession{page: page, ctxBrowser: ctxBrowser, role: req.Role}

	target := req.URL
	if req.Role == RoleRecord {
		target = withListenOnlyAudio(req.URL)
	}
	if err := page.Timeout(30 * time.Second).Navigate(target); err != nil {
		if cerr := sess.Close(); cerr != nil {
			logx.Debugf("bbb", "attemptJoin: cleanup close: %v", cerr)
		}
		logx.Errorf("bbb", "attemptJoin: navigate %s: %v", redactURL(target), err)
		return nil, fmt.Errorf("navigate: %w", err)
	}
	if err := page.Timeout(15 * time.Second).WaitLoad(); err != nil {
		logx.Debugf("bbb", "attemptJoin: waitload: %v", err)
	}

	if err := sess.waitSeated(ctx, req); err != nil {
		if cerr := sess.Close(); cerr != nil {
			logx.Debugf("bbb", "attemptJoin: cleanup close: %v", cerr)
		}
		logx.Errorf("bbb", "attemptJoin: waitSeated: %v", err)
		return nil, fmt.Errorf("ChromeJoiner.Join: waitSeated: %w", err)
	}
	st, err := sess.seat(ctx)
	if err != nil {
		logx.Debugf("bbb", "attemptJoin: seat probe: %v", err)
	}
	logx.Infof("bbb", "seat=%s proxy=%s %s", st, proxyLabel, pageHint(page))
	return sess, nil
}

func fillGuestName(page *rod.Page, fio string) error {
	logx.Debugf("bbb", "fillGuestName: fio=%q", fio)
	p := page.Timeout(15 * time.Second)
	el, err := p.Race().
		Element("#join_name").
		Element("#join-name").
		Element("input[name='join_name']").
		Element("input[name='name']").
		Element("input[autocomplete='name']").
		Element("input[id*='name' i][type='text']").
		Element("input[placeholder*='name' i]").
		Element("input[placeholder*='имя' i]").
		Element("input[placeholder*='фио' i]").
		Do()
	if err != nil {
		logx.Errorf("bbb", "fillGuestName: name field: %v", err)
		return fmt.Errorf("fillGuestName: name field: %w", err)
	}
	if err := el.SelectAllText(); err != nil {
		logx.Debugf("bbb", "fillGuestName: select all: %v", err)
	}
	if err := el.Input(fio); err != nil {
		logx.Errorf("bbb", "fillGuestName: input: %v", err)
		return fmt.Errorf("fillGuestName: input: %w", err)
	}
	// Чекбокс согласия — только видимый и ещё не отмеченный: ретраи waitSeated
	// иначе переключают его вкл → выкл → вкл.
	if res, err := el.Eval(guestCheckboxJS); err != nil {
		logx.Debugf("bbb", "fillGuestName: checkbox: %v", err)
	} else if res != nil && res.Value.Bool() {
		logx.Debugf("bbb", "fillGuestName: checkbox ticked")
	}
	if err := submitGuestForm(page, el); err != nil {
		logx.Errorf("bbb", "fillGuestName: %v", err)
		return fmt.Errorf("fillGuestName: %w", err)
	}
	if err := page.Timeout(8 * time.Second).WaitLoad(); err != nil {
		logx.Debugf("bbb", "fillGuestName: waitload: %v", err)
	}
	logx.Debugf("bbb", "fillGuestName: done")
	return nil
}

var joinButtonSels = []string{
	"[data-test='joinButton']",
	"[data-test='sessionJoinButton']",
	"button[type='submit']",
	"input[type='submit']",
}

// guestCheckboxJS отмечает видимый неотмеченный чекбокс формы поля имени.
const guestCheckboxJS = `function () {
	const root = this.form || document
	const vis = (n) => { const r = n.getBoundingClientRect(); const s = getComputedStyle(n); return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none' }
	const box = [...root.querySelectorAll("input[type='checkbox']")].find((b) => vis(b) && !b.disabled && !b.checked)
	if (!box) return false
	box.click()
	return true
}`

// guestSubmitJS: видимая активная кнопка join в форме поля имени (потом по
// странице) → click(); нет кнопки → form.requestSubmit(). Скрытые submit чужих
// форм (логин Greenlight) не трогаем — на них rod ждал WaitInteractable до дедлайна.
const guestSubmitJS = `function (sels) {
	const vis = (n) => { const r = n.getBoundingClientRect(); const s = getComputedStyle(n); return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none' && !n.disabled && n.getAttribute('aria-disabled') !== 'true' }
	const pick = (root) => { for (const sel of sels) { const b = [...root.querySelectorAll(sel)].find(vis); if (b) return b } return null }
	const f = this.form
	const b = (f && pick(f)) || pick(document)
	if (b) { b.click(); return 'click ' + (b.getAttribute('data-test') || b.tagName.toLowerCase()) }
	if (f) { if (f.requestSubmit) f.requestSubmit(); else f.submit(); return 'submit' }
	return ''
}`

// submitGuestForm жмёт join для поля имени el: rod-клик по видимой кнопке
// своей формы, затем DOM-клик/requestSubmit, в крайнем случае Enter в поле.
func submitGuestForm(page *rod.Page, el *rod.Element) error {
	for _, sel := range joinButtonSels {
		btns, err := page.Elements(sel)
		if err != nil {
			continue
		}
		for _, b := range btns {
			if !sameForm(el, b) {
				continue
			}
			if vis, err := b.Visible(); err != nil || !vis {
				continue
			}
			if dis, err := b.Property("disabled"); err == nil && dis.Bool() {
				continue
			}
			if err := b.Timeout(clickWait).Click(proto.InputMouseButtonLeft, 1); err == nil {
				logx.Debugf("bbb", "submitGuestForm: clicked %s", sel)
				return nil
			} else {
				logx.Debugf("bbb", "submitGuestForm: click %s: %v", sel, err)
			}
		}
	}
	res, err := el.Context(context.Background()).Timeout(3*time.Second).Eval(guestSubmitJS, joinButtonSels)
	if err == nil && res != nil && res.Value.Str() != "" {
		logx.Debugf("bbb", "submitGuestForm: js %s", res.Value.Str())
		return nil
	}
	logx.Debugf("bbb", "submitGuestForm: js: %v", err)
	if clickByText(page.Timeout(4*time.Second), joinNameRE) {
		return nil
	}
	if err := el.Context(context.Background()).Timeout(3 * time.Second).Type(input.Enter); err != nil {
		return fmt.Errorf("join button: нет кнопки, Enter: %w", err)
	}
	logx.Debugf("bbb", "submitGuestForm: enter")
	return nil
}

// sameForm: кнопка в той же форме, что и поле имени (или у поля нет формы).
func sameForm(field, btn *rod.Element) bool {
	res, err := field.Eval(`function (b) { return !this.form || this.form === b.form || this.form.contains(b) }`, btn.Object)
	if err != nil || res == nil {
		return true
	}
	return res.Value.Bool()
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
	logx.Debugf("bbb", "sendHello: enter")
	var last error
	for i := 0; i < 8; i++ {
		p := page.Timeout(2 * time.Second)
		if !clickFirst(p, chatOpenSels) {
			logx.Debugf("bbb", "sendHello: attempt=%d no chat open button", i)
		}
		if !clickByText(p, chatOpenRE) {
			logx.Debugf("bbb", "sendHello: attempt=%d no chat open text", i)
		}
		last = typeHello(p)
		if last == nil {
			logx.Debugf("bbb", "sendHello: sent on attempt=%d", i)
			return nil
		}
		logx.Debugf("bbb", "sendHello: attempt=%d type: %v", i, last)
		time.Sleep(400 * time.Millisecond)
	}
	if last == nil {
		last = fmt.Errorf("поле чата не найдено")
	}
	logx.Errorf("bbb", "sendHello: %v", last)
	return fmt.Errorf("sendHello: %w", last)
}

func typeHello(page *rod.Page) error {
	logx.Debugf("bbb", "typeHello: enter")
	el, err := findFirst(page, chatInputSels)
	if err != nil {
		logx.Errorf("bbb", "typeHello: input: %v", err)
		return fmt.Errorf("typeHello: input: %w", err)
	}
	if err := el.Click(proto.InputMouseButtonLeft, 1); err != nil {
		logx.Debugf("bbb", "typeHello: click: %v", err)
	}
	if err := el.SelectAllText(); err != nil {
		logx.Debugf("bbb", "typeHello: select: %v", err)
	}
	if err := el.Input(helloText); err != nil {
		logx.Errorf("bbb", "typeHello: input text: %v", err)
		return fmt.Errorf("typeHello: input text: %w", err)
	}
	if clickFirst(page, chatSendSels) {
		logx.Debugf("bbb", "typeHello: sent via button")
		return nil
	}
	if err := el.Type(input.Enter); err != nil {
		logx.Errorf("bbb", "typeHello: send: %v", err)
		return fmt.Errorf("отправить: %w", err)
	}
	logx.Debugf("bbb", "typeHello: sent via enter")
	return nil
}

func findFirst(page *rod.Page, sels []string) (*rod.Element, error) {
	for _, sel := range sels {
		ok, el, err := page.Has(sel)
		if err == nil && ok && el != nil {
			logx.Debugf("bbb", "findFirst: matched %s", sel)
			return el, nil
		}
	}
	logx.Warnf("bbb", "findFirst: no chat field among %d selectors", len(sels))
	return nil, fmt.Errorf("поле чата не найдено")
}

func clickListenOnly(page *rod.Page) error {
	logx.Debugf("bbb", "clickListenOnly: enter")
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		p := page.Timeout(3 * time.Second)
		if clickFirst(p, listenOnlySels) || clickByText(p, listenOnlyRE) {
			logx.Debugf("bbb", "clickListenOnly: clicked")
			return nil
		}
		// Lobby / already in the roster: audio modal may never appear.
		if hasAny(p, inMeetingSels) {
			logx.Debugf("bbb", "clickListenOnly: in meeting already")
			return nil
		}
		time.Sleep(400 * time.Millisecond)
	}
	err := fmt.Errorf("кнопка «только слушать» не найдена")
	logx.Warnf("bbb", "clickListenOnly: %v", err)
	return err
}

var closeAudioSels = []string{
	"[data-test='closeModalButton']",
	"[data-test='closeModal']",
	"[data-test='closeButton']",
	`button[aria-label='Close']`,
	`button[aria-label='Закрыть']`,
}

func dismissAudio(page *rod.Page) error {
	logx.Debugf("bbb", "dismissAudio: enter")
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		p := page.Timeout(2 * time.Second)
		if clickFirst(p, closeAudioSels) || clickByText(p, `(?i)close|закрыть|skip|пропуст`) {
			logx.Debugf("bbb", "dismissAudio: closed")
			return nil
		}
		if hasAny(p, inMeetingSels) {
			logx.Debugf("bbb", "dismissAudio: in meeting already")
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	logx.Warnf("bbb", "dismissAudio: timeout, fallback listen-only")
	err := clickListenOnly(page)
	if err != nil {
		logx.Errorf("bbb", "dismissAudio: %v", err)
		return fmt.Errorf("dismissAudio: %w", err)
	}
	return nil
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
		// Своя короткая пауза на rod-клик: WaitInteractable на перекрытой
		// кнопке иначе съедает весь таймаут страницы, и DOM-фолбэк не успевает.
		if err := el.Timeout(clickWait).Click(proto.InputMouseButtonLeft, 1); err == nil {
			logx.Debugf("bbb", "clickFirst: clicked %s", sel)
			return true
		} else {
			logx.Debugf("bbb", "clickFirst: click %s: %v", sel, err)
			if clickJS(page, sel) {
				logx.Debugf("bbb", "clickFirst: js-clicked %s", sel)
				return true
			}
		}
	}
	logx.Debugf("bbb", "clickFirst: none of %d selectors clicked", len(sels))
	return false
}

// clickWait bounds rod's wait for a button to become clickable.
const clickWait = 800 * time.Millisecond

// clickJS clicks through the DOM directly, for elements rod deems unclickable
// (e.g. a modal button that is present but not yet interactable). It runs on
// a fresh context: the caller's Timeout() page may already be expired by the
// failed rod click, and then Eval would fail at once.
func clickJS(page *rod.Page, sel string) bool {
	if page == nil || sel == "" {
		return false
	}
	res, err := page.Context(context.Background()).Timeout(3*time.Second).Eval(`(sel) => {
		const n = document.querySelector(sel)
		if (!n) return false
		n.click()
		return true
	}`, sel)
	if err != nil || res == nil {
		logx.Debugf("bbb", "clickJS: %s: %v", sel, err)
		return false
	}
	ok := res.Value.Bool()
	logx.Debugf("bbb", "clickJS: %s -> %v", sel, ok)
	return ok
}

func clickByText(page *rod.Page, goRE string) bool {
	jsRE := jsRegexp(goRE)
	if jsRE == "" {
		logx.Debugf("bbb", "clickByText: empty regex")
		return false
	}
	// Обёртки (#app, модалка) тоже содержат искомый текст в innerText; клик по
	// ним ничего не делает. Берём самый вложенный видимый элемент с совпадением
	// и жмём его ближайшего интерактивного предка.
	res, err := page.Eval(clickByTextJS, jsRE)
	if err != nil || res == nil {
		logx.Debugf("bbb", "clickByText: eval re=%q: %v", jsRE, err)
		return false
	}
	hit := strings.TrimSpace(res.Value.Str())
	if hit == "" {
		logx.Debugf("bbb", "clickByText: re=%q clicked=false", jsRE)
		return false
	}
	logx.Debugf("bbb", "clickByText: re=%q clicked %s", jsRE, hit)
	return true
}

const clickByTextJS = `(re) => {
	const rx = new RegExp(re, 'i')
	const interactive = 'button, a, [role="button"], [role="menuitem"], [data-test]'
	const own = (n) => ((n.innerText || '') + ' ' + (n.getAttribute('aria-label') || '')).trim()
	const visible = (n) => {
		const r = n.getBoundingClientRect()
		if (r.width === 0 && r.height === 0) return false
		const s = getComputedStyle(n)
		return s.visibility !== 'hidden' && s.display !== 'none'
	}
	let best = null, bestLen = Infinity
	for (const n of document.querySelectorAll('button, a, [role="button"], [role="menuitem"], [data-test], span, div, label, li')) {
		const t = own(n)
		if (!t || !rx.test(t) || !visible(n)) continue
		// Предпочитаем интерактивный элемент; среди равных — самый короткий текст.
		const len = t.length - (n.matches(interactive) ? 100000 : 0)
		if (len < bestLen) { best = n; bestLen = len }
	}
	if (!best) return ''
	const target = best.closest(interactive) || best
	target.click()
	const dt = target.getAttribute('data-test') || ''
	return target.tagName.toLowerCase() + (dt ? '[' + dt + ']' : '') + ' "' + own(target).replace(/\s+/g, ' ').slice(0, 40) + '"'
}`

// jsRegexp strips Go/PCRE inline flags — rod Eval runs in the browser.
func jsRegexp(goRE string) string {
	s := strings.TrimSpace(goRE)
	s = strings.TrimPrefix(s, "(?i)")
	s = strings.TrimPrefix(s, "(?m)")
	s = strings.TrimPrefix(s, "(?s)")
	logx.Debugf("bbb", "jsRegexp: in=%q out=%q", goRE, s)
	return s
}

// chromeAudioLine keeps Chromium stderr lines about audio output and WebRTC.
func chromeAudioLine(line string) bool {
	low := strings.ToLower(line)
	for _, k := range []string{"pulse", "alsa", "audio", "webrtc", "p2p", "stun", "media_stream", "getusermedia"} {
		if strings.Contains(low, k) {
			return true
		}
	}
	return false
}
