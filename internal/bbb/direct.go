package bbb

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PuerkitoBio/goquery"
	xproxy "golang.org/x/net/proxy"
	"golang.org/x/net/websocket"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/proxyrelay"
)

const (
	helloText       = "Здравствуйте"
	maxJoinAttempts = 3
	httpTimeout     = 20 * time.Second
	ddpAuthTimeout  = 15 * time.Second
	maxSlideItems   = 60
)

var lobbyTextMarks = []string{
	"waiting for a moderator",
	"wait for a moderator",
	"wait for the moderator",
	"moderator to approve",
	"approve you",
	"подождите, пока модератор",
	"пока модератор",
	"модератор одобрит",
	"модератор не впустит",
	"waiting for the moderator",
	"waiting room",
	"you'll join when",
	"you will join when",
	"placed in a waiting",
	"guest lobby",
	"ожидайте",
	"зал ожидания",
	"ждите модератора",
	"ждём модератора",
	"ждем модератора",
	"meeting hasn't started",
	"meeting has not started",
	"session has not",
	"has not been started",
	"комната ещё не",
	"комната еще не",
	"встреча ещё не",
	"встреча еще не",
}

// DirectJoiner joins BBB rooms purely over HTTP + Meteor DDP + Pion WebRTC.
type DirectJoiner struct {
	Proxies *proxyrelay.Pool
}

// NewDirectJoiner constructs a browserless BBB joiner.
func NewDirectJoiner() *DirectJoiner {
	return &DirectJoiner{}
}

// Close shuts down any local SOCKS5 proxy relays held by the joiner.
func (d *DirectJoiner) Close() error {
	if d == nil || d.Proxies == nil {
		return nil
	}
	err := d.Proxies.Close()
	d.Proxies = nil
	return err
}

type enterInfo struct {
	ReturnCode     string `json:"returncode"`
	MessageKey     string `json:"messageKey"`
	Message        string `json:"message"`
	FullName       string `json:"fullname"`
	MeetingID      string `json:"meetingID"`
	ExternUserID   string `json:"externUserID"`
	InternalUserID string `json:"internalUserID"`
	AuthToken      string `json:"authToken"`
	VoiceBridge    string `json:"voicebridge"`
	GuestStatus    string `json:"guestStatus"`
}

type enterEnvelope struct {
	Response enterInfo `json:"response"`
}

type stunsEnvelope struct {
	StunServers []struct {
		URL string `json:"url"`
	} `json:"stunServers"`
	TurnServers []struct {
		URL      string `json:"url"`
		Username string `json:"username"`
		Password string `json:"password"`
	} `json:"turnServers"`
}

type slideMeta struct {
	DocID          string // ID документа в коллекции Meteor DDP slides
	ID             string
	PresentationID string
	Num            int
	ImageURI       string
	ThumbURI       string
	TxtURI         string
	Content        string
	Current        bool
	FirstShownAt   time.Time
}

type presMeta struct {
	ID      string
	Name    string
	Current bool
}

// Join enters a BBB meeting room (or guest/waiting lobby) without a browser.
func (d *DirectJoiner) Join(ctx context.Context, req JoinReq) (Session, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	started := time.Now()
	nProxies := 0
	if d != nil {
		nProxies = d.Proxies.Len()
	}
	logx.Infof("bbb", "join: старт роль=%s гость=%q url=%s прокси=%d", req.Role, req.FIO, redactURL(req.URL), nProxies)

	attempts := 1
	if nProxies > 0 {
		attempts = maxJoinAttempts
		if nProxies < attempts {
			attempts = nProxies
		}
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			logx.Infof("bbb", "join: отменён на попытке %d/%d после %s: %v", attempt, attempts, time.Since(started).Round(time.Millisecond), err)
			return nil, err
		}
		var slot *proxyrelay.Slot
		if nProxies > 0 {
			s, perr := d.Proxies.Next()
			if perr != nil {
				logx.Errorf("bbb", "join: нет живого прокси (попытка %d/%d): %v", attempt, attempts, perr)
				if lastErr != nil {
					return nil, fmt.Errorf("%w (последняя ошибка: %v)", perr, lastErr)
				}
				return nil, perr
			}
			slot = s
		}
		proxy := slotRedacted(slot)
		attemptStart := time.Now()
		logx.Debugf("bbb", "join: попытка %d/%d через %s", attempt, attempts, proxy)

		sess, err := d.joinOnce(ctx, req, slot)
		if err == nil {
			slot.OK()
			logx.Infof("bbb", "join: вошёл за %s через %s (попытка %d/%d) гость=%q url=%s",
				time.Since(started).Round(time.Millisecond), proxy, attempt, attempts, req.FIO, redactURL(req.URL))
			return sess, nil
		}
		lastErr = err
		slot.Fail()
		logx.Warnf("bbb", "join: попытка %d/%d через %s не удалась за %s: %v",
			attempt, attempts, proxy, time.Since(attemptStart).Round(time.Millisecond), err)
	}
	logx.Errorf("bbb", "join: не вошёл за %s, попыток %d, гость=%q url=%s: %v",
		time.Since(started).Round(time.Millisecond), attempts, req.FIO, redactURL(req.URL), lastErr)
	return nil, lastErr
}

func slotRedacted(slot *proxyrelay.Slot) string {
	if slot == nil {
		return "direct"
	}
	return slot.Redacted()
}

type dialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

func buildClient(slot *proxyrelay.Slot) (*http.Client, dialFunc, error) {
	jar, _ := cookiejar.New(nil)
	var df dialFunc = (&net.Dialer{Timeout: 15 * time.Second}).DialContext
	if slot != nil {
		pd, err := xproxy.SOCKS5("tcp", slot.LocalAddr(), nil, &net.Dialer{Timeout: 15 * time.Second})
		if err != nil {
			return nil, nil, fmt.Errorf("socks5 dialer: %w", err)
		}
		df = func(_ context.Context, network, addr string) (net.Conn, error) {
			return pd.Dial(network, addr)
		}
	}
	tr := &http.Transport{
		DialContext:         df,
		TLSHandshakeTimeout: 15 * time.Second,
	}
	return &http.Client{
		Jar:       jar,
		Transport: tr,
		Timeout:   httpTimeout,
	}, df, nil
}

func (d *DirectJoiner) joinOnce(ctx context.Context, req JoinReq, slot *proxyrelay.Slot) (*directSession, error) {
	client, dialFn, err := buildClient(slot)
	if err != nil {
		return nil, err
	}
	sessCtx, cancel := context.WithCancel(context.Background())
	s := &directSession{
		req:         req,
		client:      client,
		dialFn:      dialFn,
		ctx:         sessCtx,
		cancel:      cancel,
		chatID:      "MAIN-PUBLIC-GROUP-CHAT",
		pending:     make(map[string]chan string),
		msgAdded:    make(map[string]chan struct{}),
		pres:        make(map[string]presMeta),
		slides:      make(map[string]slideMeta),
		allPres:     make(map[string]presMeta),
		allSlides:   make(map[string]slideMeta),
		savedSlides: make(map[string]string),
		slidesSub:   make(chan struct{}),
		authedCh:    make(chan struct{}),
		ddpDone:     make(chan struct{}),
		voiceReady:  make(chan struct{}),
		startedAt:   time.Now(),
	}

	stepStart := time.Now()
	if err := s.stepEnter(ctx); err != nil {
		logx.Warnf("bbb", "join: шаг enter не прошёл за %s гость=%q: %v", time.Since(stepStart).Round(time.Millisecond), req.FIO, err)
		_ = s.Close()
		return nil, err
	}
	s.mu.Lock()
	seat := seatName(s.inLobby, s.inRoom, s.waitGreen)
	s.mu.Unlock()
	logx.Infof("bbb", "join: шаг enter за %s → %s (роль=%s)", time.Since(stepStart).Round(time.Millisecond), seat, req.Role)
	return s, nil
}

type directSession struct {
	req    JoinReq
	client *http.Client
	dialFn dialFunc

	ctx    context.Context
	cancel context.CancelFunc

	mu           sync.Mutex
	closed       bool
	inLobby      bool
	inRoom       bool
	waitGreen    bool // waiting on Greenlight before BBB sessionToken exists
	sessionToken string
	baseURL      string
	wsHost       string
	finalURL     *url.URL
	enter        enterInfo
	greeted      bool
	lastErr      error
	startedAt    time.Time // когда начался заход (для лога «сколько были в комнате»)
	ddpDialAt    time.Time // когда открыли DDP WebSocket (для лога validateAuthToken)

	// DDP WebSocket state
	ddpWS      *websocket.Conn
	sendMu     sync.Mutex
	nextID     atomic.Int64
	authedCh   chan struct{}
	authedOnce sync.Once
	ddpDone    chan struct{}
	ddpEndOnce sync.Once

	chatID     string
	pendingMu  sync.Mutex
	pending    map[string]chan string
	msgAddedMu sync.Mutex
	msgAdded   map[string]chan struct{}

	slidesGrabMu sync.Mutex // сериализация параллельных вызовов GrabSlides (без удержания presMu)
	presMu       sync.Mutex
	pres         map[string]presMeta
	slides       map[string]slideMeta
	allPres      map[string]presMeta
	allSlides    map[string]slideMeta
	presOrder    []string
	savedSlides  map[string]string
	presLogged   string // последний состав презентаций, записанный в лог (под presMu)
	slidesSub    chan struct{}
	slidesOnce   sync.Once

	voiceJoined atomic.Bool
	voiceReady  chan struct{}
	voiceOnce   sync.Once

	// WebRTC / SFU listen-only state (RoleRecord)
	audioMu    sync.Mutex
	audioSess  *sfuAudioSession
	recMu      sync.Mutex
	curRec     *activeSegmentRec
	iceServers []stunsICE
}

func (s *directSession) httpDo(ctx context.Context, req *http.Request) (*http.Response, []byte, error) {
	return s.httpDoLimit(ctx, req, 4<<20)
}

func (s *directSession) httpDoLimit(ctx context.Context, req *http.Request, maxBytes int64) (*http.Response, []byte, error) {
	if ctx != nil {
		req = req.WithContext(ctx)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")
	}
	resp, err := s.client.Do(req)
	if err != nil {
		// Ошибка клиента содержит URL целиком, а там sessionToken — режем query.
		return nil, nil, scrubErr(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		return resp, nil, err
	}
	return resp, raw, nil
}

func (s *directSession) stepEnter(ctx context.Context) error {
	req, err := http.NewRequest(http.MethodGet, strings.TrimSpace(s.req.URL), nil)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	getStart := time.Now()
	resp, body, err := s.httpDo(ctx, req)
	if err != nil {
		logx.Warnf("bbb", "stepEnter: GET комнаты за %s не удался: %v", time.Since(getStart).Round(time.Millisecond), err)
		return fmt.Errorf("get room: %w", err)
	}

	curURL := resp.Request.URL
	logx.Debugf("bbb", "stepEnter: GET комнаты HTTP %d за %s url=%s", resp.StatusCode, time.Since(getStart).Round(time.Millisecond), redactURL(curURL.String()))
	if tok := curURL.Query().Get("sessionToken"); tok != "" {
		logx.Infof("bbb", "stepEnter: комната отдала сессию сразу (без формы Greenlight) за %s", time.Since(getStart).Round(time.Millisecond))
		return s.enterWithToken(ctx, curURL, tok)
	}

	doc, _ := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	if doc != nil {
		if formAction, formVals, found := extractGreenlightForm(doc, curURL, s.req.FIO); found {
			postReq, err := http.NewRequest(http.MethodPost, formAction, strings.NewReader(formVals.Encode()))
			if err != nil {
				return fmt.Errorf("build form post: %w", err)
			}
			postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			postReq.Header.Set("Origin", curURL.Scheme+"://"+curURL.Host)
			postReq.Header.Set("Referer", curURL.String())

			logx.Infof("bbb", "stepEnter: форма Greenlight найдена, отправляю имя гостя=%q → %s", s.req.FIO, redactURL(formAction))
			postStart := time.Now()
			resp2, body2, err := s.httpDo(ctx, postReq)
			if err != nil {
				logx.Warnf("bbb", "stepEnter: POST формы за %s не удался: %v", time.Since(postStart).Round(time.Millisecond), err)
				return fmt.Errorf("post join form: %w", err)
			}
			curURL = resp2.Request.URL
			body = body2
			logx.Infof("bbb", "stepEnter: форма отправлена за %s HTTP %d url=%s", time.Since(postStart).Round(time.Millisecond), resp2.StatusCode, redactURL(curURL.String()))
			if tok := curURL.Query().Get("sessionToken"); tok != "" {
				return s.enterWithToken(ctx, curURL, tok)
			}
		} else {
			logx.Debugf("bbb", "stepEnter: формы входа на странице нет")
		}
	}

	// No sessionToken yet: check whether this is a Greenlight waiting room.
	lower := strings.ToLower(string(body))
	if containsAny(lower, lobbyTextMarks) || strings.Contains(curURL.Path, "guestWait") {
		s.mu.Lock()
		s.inLobby = true
		s.inRoom = false
		s.waitGreen = true
		s.finalURL = curURL
		s.mu.Unlock()
		reason := "текст лобби на странице"
		if strings.Contains(curURL.Path, "guestWait") {
			reason = "путь guestWait"
		}
		logx.Infof("bbb", "stepEnter: Greenlight — лобби, жду модератора (%s) url=%s", reason, redactURL(curURL.String()))
		return nil
	}

	if resp.StatusCode >= 400 {
		logx.Warnf("bbb", "stepEnter: комната ответила HTTP %d url=%s — ни токена, ни лобби", resp.StatusCode, redactURL(curURL.String()))
		return fmt.Errorf("комната вернула HTTP %d", resp.StatusCode)
	}
	logx.Warnf("bbb", "stepEnter: нет sessionToken и не похоже на лобби url=%s (HTTP %d, %d байт)", redactURL(curURL.String()), resp.StatusCode, len(body))
	return fmt.Errorf("не удалось получить sessionToken со страницы %s", redactURL(curURL.String()))
}

func extractGreenlightForm(doc *goquery.Document, pageURL *url.URL, fio string) (string, url.Values, bool) {
	if doc == nil {
		return "", nil, false
	}
	var form *goquery.Selection
	doc.Find("form").EachWithBreak(func(_ int, f *goquery.Selection) bool {
		if f.Find("input[name*='join_name'], input[name='name'], #join_name, #join-name").Length() > 0 {
			form = f
			return false
		}
		return true
	})
	if form == nil {
		return "", nil, false
	}

	vals := url.Values{}
	form.Find("input").Each(func(_ int, inp *goquery.Selection) {
		name, ok := inp.Attr("name")
		if !ok || name == "" {
			return
		}
		typ, _ := inp.Attr("type")
		val, _ := inp.Attr("value")
		switch strings.ToLower(typ) {
		case "hidden", "text", "":
			if strings.Contains(strings.ToLower(name), "join_name") || name == "name" {
				vals.Set(name, fio)
			} else if val != "" {
				vals.Set(name, val)
			}
		}
	})
	if !vals.Has("authenticity_token") {
		if csrf, ok := doc.Find("meta[name='csrf-token']").Attr("content"); ok && csrf != "" {
			vals.Set("authenticity_token", csrf)
		}
	}
	if !vals.Has("utf8") {
		vals.Set("utf8", "✓")
	}

	action, _ := form.Attr("action")
	targetURL := pageURL.String()
	if action != "" {
		if ref, err := url.Parse(action); err == nil {
			targetURL = pageURL.ResolveReference(ref).String()
		}
	}
	return targetURL, vals, true
}

func containsAny(haystack string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}

func (s *directSession) enterWithToken(ctx context.Context, finalURL *url.URL, token string) error {
	base := fmt.Sprintf("%s://%s", finalURL.Scheme, finalURL.Host)
	enterStart := time.Now()
	er, err := s.fetchEnter(ctx, base, token)
	if err != nil {
		logx.Warnf("bbb", "enter: запрос за %s не удался: %v", time.Since(enterStart).Round(time.Millisecond), err)
		return err
	}
	logx.Infof("bbb", "enter: ответ за %s returncode=%s guestStatus=%s messageKey=%s userId=%s",
		time.Since(enterStart).Round(time.Millisecond), er.ReturnCode, er.GuestStatus, er.MessageKey, er.InternalUserID)
	if er.ReturnCode != "SUCCESS" {
		msg := er.Message
		if msg == "" {
			msg = er.MessageKey
		}
		if msg == "" {
			msg = "returncode=" + er.ReturnCode
		}
		logx.Warnf("bbb", "enter: BBB не пустил: %s", msg)
		return fmt.Errorf("bbb enter: %s", msg)
	}
	switch strings.ToUpper(er.GuestStatus) {
	case "DENY":
		logx.Warnf("bbb", "enter: модератор отклонил вход гостя=%q", s.req.FIO)
		return fmt.Errorf("модератор отклонил вход")
	case "WAIT":
		s.mu.Lock()
		s.inLobby = true
		s.inRoom = false
		s.waitGreen = false
		s.sessionToken = token
		s.baseURL = base
		s.wsHost = finalURL.Host
		s.finalURL = finalURL
		s.enter = er
		s.mu.Unlock()
		logx.Infof("bbb", "enter: guestWait — лобби, модератор ещё не пустил гостя=%q userId=%s", s.req.FIO, er.InternalUserID)
		return nil
	}

	s.mu.Lock()
	s.sessionToken = token
	s.baseURL = base
	s.wsHost = finalURL.Host
	s.finalURL = finalURL
	s.enter = er
	s.waitGreen = false
	s.mu.Unlock()

	if err := s.connectDDP(ctx); err != nil {
		return err
	}
	if s.req.Role == RoleRecord {
		// Kick off listen-only audio right away; failures are retried by EnsureAudio.
		audioStart := time.Now()
		if !s.EnsureAudio(ctx) {
			logx.Warnf("bbb", "webrtc: listen-only за %s не поднялся — отложен, повторю на тиках", time.Since(audioStart).Round(time.Millisecond))
		} else {
			logx.Infof("bbb", "webrtc: listen-only поднят за %s (роль=record)", time.Since(audioStart).Round(time.Millisecond))
		}
	}
	return nil
}

// RTPPackets — сколько RTP-пакетов пришло по listen-only треку текущей WebRTC-сессии
// (для лога записи). Если звук сейчас переподнимается — 0: не ждём up to 15 с.
func (s *directSession) RTPPackets() uint64 {
	if s == nil {
		return 0
	}
	if !s.audioMu.TryLock() {
		return 0
	}
	defer s.audioMu.Unlock()
	if s.audioSess == nil {
		return 0
	}
	return s.audioSess.rtpPackets.Load()
}

func (s *directSession) fetchEnter(ctx context.Context, base, token string) (enterInfo, error) {
	u := base + "/bigbluebutton/api/enter?sessionToken=" + url.QueryEscape(token)
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return enterInfo{}, err
	}
	resp, raw, err := s.httpDo(ctx, req)
	if err != nil {
		return enterInfo{}, fmt.Errorf("enter: %w", err)
	}
	if resp.StatusCode >= 400 {
		return enterInfo{}, fmt.Errorf("enter http %d", resp.StatusCode)
	}
	var env enterEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return enterInfo{}, fmt.Errorf("enter decode: %w", err)
	}
	return env.Response, nil
}

func (s *directSession) fetchICE(ctx context.Context) []stunsICE {
	s.mu.Lock()
	base := s.baseURL
	tok := s.sessionToken
	cached := s.iceServers
	s.mu.Unlock()
	if len(cached) > 0 {
		return cached
	}
	u := base + "/bigbluebutton/api/stuns?sessionToken=" + url.QueryEscape(tok)
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil
	}
	_, raw, err := s.httpDo(ctx, req)
	if err != nil {
		logx.Debugf("bbb", "fetchICE: %v", err)
		return nil
	}
	var env stunsEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil
	}
	var out []stunsICE
	for _, st := range env.StunServers {
		if st.URL != "" {
			out = append(out, stunsICE{URLs: []string{st.URL}})
		}
	}
	for _, tu := range env.TurnServers {
		if tu.URL != "" {
			out = append(out, stunsICE{
				URLs:       []string{tu.URL},
				Username:   tu.Username,
				Credential: tu.Password,
			})
		}
	}
	s.mu.Lock()
	s.iceServers = out
	s.mu.Unlock()
	return out
}

func (s *directSession) cookieHeaderFor(u *url.URL) string {
	if s.client == nil || s.client.Jar == nil || u == nil {
		return ""
	}
	var parts []string
	for _, c := range s.client.Jar.Cookies(u) {
		parts = append(parts, c.Name+"="+c.Value)
	}
	return strings.Join(parts, "; ")
}

// handshakeTap запоминает HTTP-ответ рукопожатия WebSocket, чтобы в лог попал
// реальный статус (403/429/...), а не только «bad status» от x/net/websocket.
type handshakeTap struct {
	net.Conn

	mu     sync.Mutex
	head   []byte
	status int
	line   string
	done   bool
}

func (t *handshakeTap) Read(p []byte) (int, error) {
	n, err := t.Conn.Read(p)
	if n > 0 {
		t.mu.Lock()
		if !t.done {
			const maxHead = 8192
			if room := maxHead - len(t.head); room > 0 {
				if n < room {
					t.head = append(t.head, p[:n]...)
				} else {
					t.head = append(t.head, p[:room]...)
				}
			}
			if i := bytes.Index(t.head, []byte("\r\n\r\n")); i >= 0 {
				t.finishLocked(t.head[:i+4])
			} else if len(t.head) >= maxHead {
				t.done = true
			}
		}
		t.mu.Unlock()
	}
	return n, err
}

func (t *handshakeTap) finishLocked(head []byte) {
	t.done = true
	lines := strings.Split(string(head), "\r\n")
	if len(lines) == 0 {
		return
	}
	t.line = strings.TrimSpace(lines[0])
	fields := strings.Fields(t.line)
	if len(fields) >= 2 {
		t.status, _ = strconv.Atoi(fields[1])
	}
}

// Status возвращает код ответа и первую строку ответа.
func (t *handshakeTap) Status() (int, string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.status, t.line
}

// Header возвращает значение заголовка из ответа рукопожатия (без Set-Cookie).
func (t *handshakeTap) Header(name string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return headerValue(t.head, name)
}

func headerValue(head []byte, name string) string {
	lines := strings.Split(string(head), "\r\n")
	for _, line := range lines[1:] {
		k, v, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(k), name) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// handshakeError — рукопожатие WebSocket не дало 101. Код нужен выше, чтобы
// включить защиту от шквала 403/429 с одного IP.
type handshakeError struct {
	URL        string
	Status     int
	StatusLine string
	RetryAfter string
	Err        error
}

func (e *handshakeError) Error() string {
	if e == nil {
		return "ddp handshake"
	}
	if e.Status > 0 {
		msg := fmt.Sprintf("bad status HTTP %d", e.Status)
		if e.RetryAfter != "" {
			msg += " (Retry-After " + e.RetryAfter + ")"
		}
		return msg
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return "bad status"
}

func (e *handshakeError) Unwrap() error { return e.Err }

func handshakeLogFields(tap *handshakeTap) string {
	var parts []string
	if st, line := tap.Status(); st > 0 {
		parts = append(parts, fmt.Sprintf("status=%d", st))
		if line != "" {
			parts = append(parts, "line="+strconv.Quote(line))
		}
	}
	for _, h := range []string{"Retry-After", "Server", "Content-Type", "X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset"} {
		if v := tap.Header(h); v != "" {
			parts = append(parts, h+"="+strconv.Quote(v))
		}
	}
	if len(parts) == 0 {
		return "status=unknown"
	}
	return strings.Join(parts, " ")
}

func (s *directSession) dialWebSocket(ctx context.Context, wsURL, origin, cookie string) (*websocket.Conn, error) {
	cfg, err := websocket.NewConfig(wsURL, origin)
	if err != nil {
		return nil, err
	}
	if cookie != "" {
		cfg.Header.Set("Cookie", cookie)
	}
	cfg.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")

	u, err := url.Parse(wsURL)
	if err != nil {
		return nil, err
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		if u.Scheme == "wss" {
			port = "443"
		} else {
			port = "80"
		}
	}
	addr := net.JoinHostPort(host, port)
	logx.Debugf("bbb", "ddp handshake: ws=%s origin=%s host=%s cookie_len=%d", wsURL, origin, host, len(cookie))
	rawConn, err := s.dialFn(ctx, "tcp", addr)
	if err != nil {
		logx.Warnf("bbb", "ddp handshake: TCP %s не открылся: %v", addr, err)
		return nil, err
	}
	if u.Scheme == "wss" {
		tlsConn := tls.Client(rawConn, &tls.Config{ServerName: host})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = rawConn.Close()
			logx.Warnf("bbb", "ddp handshake: TLS %s не прошёл: %v", host, err)
			return nil, err
		}
		rawConn = tlsConn
	}
	tap := &handshakeTap{Conn: rawConn}
	started := time.Now()
	wsConn, err := websocket.NewClient(cfg, tap)
	if err != nil {
		_ = rawConn.Close()
		status, line := tap.Status()
		retry := tap.Header("Retry-After")
		logx.Warnf("bbb", "ddp handshake: ws=%s НЕ открылся за %s: %s err=%v",
			wsURL, time.Since(started).Round(time.Millisecond), handshakeLogFields(tap), err)
		return nil, &handshakeError{URL: wsURL, Status: status, StatusLine: line, RetryAfter: retry, Err: err}
	}
	logx.Debugf("bbb", "ddp handshake: ws=%s открыт за %s: %s", wsURL, time.Since(started).Round(time.Millisecond), handshakeLogFields(tap))
	return wsConn, nil
}

func (s *directSession) connectDDP(ctx context.Context) error {
	s.mu.Lock()
	scheme := "wss"
	if s.finalURL != nil && s.finalURL.Scheme == "http" {
		scheme = "ws"
	}
	host := s.wsHost
	base := s.baseURL
	finalURL := s.finalURL
	enter := s.enter
	s.mu.Unlock()

	wsURL := fmt.Sprintf("%s://%s/html5client/sockjs/123/abcdefgh/websocket", scheme, host)
	cookie := s.cookieHeaderFor(finalURL)
	logx.Debugf("bbb", "connectDDP: гость=%q роль=%s host=%s base=%s cookie_len=%d", s.req.FIO, s.req.Role, host, base, len(cookie))
	dialStart := time.Now()
	ws, err := s.dialWebSocket(ctx, wsURL, base, cookie)
	if err != nil {
		logx.Warnf("bbb", "connectDDP: гость=%q роль=%s WebSocket %s не открылся за %s: %v", s.req.FIO, s.req.Role, scheme+"://"+host, time.Since(dialStart).Round(time.Millisecond), err)
		return fmt.Errorf("ddp dial: %w", err)
	}
	s.mu.Lock()
	s.ddpDialAt = time.Now()
	s.mu.Unlock()
	logx.Debugf("bbb", "ddp: WebSocket открыт за %s host=%s", time.Since(dialStart).Round(time.Millisecond), host)

	s.mu.Lock()
	s.ddpWS = ws
	s.inLobby = false
	s.inRoom = true
	s.mu.Unlock()

	go s.ddpReadLoop(enter)

	waitCtx, cancel := context.WithTimeout(ctx, ddpAuthTimeout)
	defer cancel()
	select {
	case <-s.authedCh:
		logx.Infof("bbb", "ddp: авторизован, в комнате (с момента WebSocket %s)", time.Since(dialStart).Round(time.Millisecond))
		return nil
	case <-s.ddpDone:
		s.mu.Lock()
		lerr := s.lastErr
		s.mu.Unlock()
		if lerr != nil {
			return fmt.Errorf("ddp auth: %w", lerr)
		}
		return fmt.Errorf("ddp closed before auth")
	case <-waitCtx.Done():
		return fmt.Errorf("ddp auth timeout: %w", waitCtx.Err())
	}
}

func (s *directSession) sendDDP(obj any) error {
	b, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	arr, err := json.Marshal([]string{string(b)})
	if err != nil {
		return err
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.ddpWS == nil {
		return io.ErrClosedPipe
	}
	return websocket.Message.Send(s.ddpWS, string(arr))
}

func (s *directSession) callDDP(ctx context.Context, method string, params []any) (string, error) {
	id := fmt.Sprintf("%d", s.nextID.Add(1))
	ch := make(chan string, 1)
	s.pendingMu.Lock()
	s.pending[id] = ch
	s.pendingMu.Unlock()

	if err := s.sendDDP(map[string]any{
		"msg":    "method",
		"method": method,
		"params": params,
		"id":     id,
	}); err != nil {
		s.pendingMu.Lock()
		delete(s.pending, id)
		s.pendingMu.Unlock()
		return "", err
	}

	select {
	case raw := <-ch:
		return raw, nil
	case <-s.ddpDone:
		return "", fmt.Errorf("ddp connection closed")
	case <-ctx.Done():
		s.pendingMu.Lock()
		delete(s.pending, id)
		s.pendingMu.Unlock()
		return "", ctx.Err()
	}
}

func (s *directSession) ddpReadLoop(enter enterInfo) {
	defer s.ddpEndOnce.Do(func() { close(s.ddpDone) })
	subs := []string{"users", "meetings", "group-chat", "group-chat-msg", "voiceUsers", "presentations", "slides"}
	subStarted := false
	voiceDocID := ""

	for {
		if s.ctx.Err() != nil {
			return
		}
		_ = s.ddpWS.SetReadDeadline(time.Now().Add(45 * time.Second))
		var frame string
		if err := websocket.Message.Receive(s.ddpWS, &frame); err != nil {
			if strings.Contains(err.Error(), "timeout") {
				continue
			}
			s.mu.Lock()
			if !s.closed && s.lastErr == nil {
				s.lastErr = err
			}
			wasClosed := s.closed
			s.inRoom = false
			s.mu.Unlock()
			if !wasClosed {
				logx.Warnf("bbb", "ddp: WebSocket оборвался гость=%q: %v", s.req.FIO, err)
			} else {
				logx.Debugf("bbb", "ddp: читалка остановлена после закрытия: %v", err)
			}
			return
		}
		switch {
		case frame == "o":
			_ = s.sendDDP(map[string]any{
				"msg":     "connect",
				"version": "1",
				"support": []string{"1", "pre2", "pre1"},
			})
		case frame == "h":
			continue
		case strings.HasPrefix(frame, "c["):
			s.mu.Lock()
			if !s.closed {
				s.lastErr = fmt.Errorf("sockjs closed: %s", frame)
			}
			wasClosed := s.closed
			s.inRoom = false
			s.mu.Unlock()
			if !wasClosed {
				logx.Warnf("bbb", "ddp: сервер закрыл сессию гость=%q кадр=%s", s.req.FIO, truncateFrame(frame))
			}
			return
		case strings.HasPrefix(frame, "a["):
			var msgs []string
			if err := json.Unmarshal([]byte(frame[1:]), &msgs); err != nil {
				continue
			}
			for _, rawMsg := range msgs {
				var m map[string]any
				if err := json.Unmarshal([]byte(rawMsg), &m); err != nil {
					continue
				}
				msgType, _ := m["msg"].(string)
				switch msgType {
				case "connected":
					go func() {
						cctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
						defer cancel()
						validateStart := time.Now()
						_, err := s.callDDP(cctx, "validateAuthToken", []any{
							enter.MeetingID, enter.InternalUserID, enter.AuthToken, enter.ExternUserID,
						})
						if err != nil {
							logx.Warnf("bbb", "ddp: validateAuthToken за %s не прошёл: %v", time.Since(validateStart).Round(time.Millisecond), err)
							s.mu.Lock()
							s.lastErr = err
							s.mu.Unlock()
							return
						}
						s.mu.Lock()
						dialAt := s.ddpDialAt
						s.mu.Unlock()
						logx.Infof("bbb", "ddp: validateAuthToken ok за %s (от открытия сокета %s)",
							time.Since(validateStart).Round(time.Millisecond), time.Since(dialAt).Round(time.Millisecond))
						s.authedOnce.Do(func() { close(s.authedCh) })
						if !subStarted {
							subStarted = true
							logx.Debugf("bbb", "ddp: подписки на коллекции: %s", strings.Join(subs, ","))
							for idx, name := range subs {
								_ = s.sendDDP(map[string]any{
									"msg":    "sub",
									"id":     fmt.Sprintf("sub-%d", idx+1),
									"name":   name,
									"params": []any{},
								})
							}
						}
					}()
				case "ping":
					if id, _ := m["id"].(string); id != "" {
						_ = s.sendDDP(map[string]any{"msg": "pong", "id": id})
					} else {
						_ = s.sendDDP(map[string]any{"msg": "pong"})
					}
				case "result":
					if id, ok := m["id"].(string); ok {
						s.pendingMu.Lock()
						ch := s.pending[id]
						delete(s.pending, id)
						s.pendingMu.Unlock()
						if ch != nil {
							ch <- rawMsg
						}
					}
				case "ready":
					if arr, ok := m["subs"].([]any); ok {
						for _, item := range arr {
							if item == "sub-7" {
								s.slidesOnce.Do(func() { close(s.slidesSub) })
							}
						}
					}
				case "added", "changed", "removed":
					s.handleDDPCollection(msgType, m, enter.InternalUserID, &voiceDocID)
				}
			}
		}
	}
}

func (s *directSession) handleDDPCollection(msgType string, m map[string]any, selfUserID string, voiceDocID *string) {
	coll, _ := m["collection"].(string)
	docID, _ := m["id"].(string)
	fields, _ := m["fields"].(map[string]any)

	switch coll {
	case "group-chat":
		if fields != nil {
			if cid, ok := fields["chatId"].(string); ok {
				if access, _ := fields["access"].(string); access == "PUBLIC_ACCESS" {
					s.mu.Lock()
					s.chatID = cid
					s.mu.Unlock()
				}
			}
		}
	case "group-chat-msg":
		if fields != nil {
			if corr, _ := fields["correlationId"].(string); corr != "" {
				s.msgAddedMu.Lock()
				ch := s.msgAdded[corr]
				delete(s.msgAdded, corr)
				s.msgAddedMu.Unlock()
				if ch != nil {
					close(ch)
				}
			}
		}
	case "voiceUsers":
		if fields != nil {
			if uid, _ := fields["intId"].(string); uid == selfUserID {
				*voiceDocID = docID
			}
			if docID != "" && docID == *voiceDocID {
				if joined, ok := fields["joined"].(bool); ok {
					s.voiceJoined.Store(joined)
					if joined {
						s.voiceOnce.Do(func() {
							close(s.voiceReady)
							logx.Infof("bbb", "ddp: голос в комнате подключён (voiceUsers joined) гость=%q", s.req.FIO)
						})
					}
				}
			}
		}
	case "users":
		if fields != nil {
			if uid, _ := fields["userId"].(string); uid == selfUserID {
				ejected, _ := fields["ejected"].(bool)
				loggedOut, _ := fields["loggedOut"].(bool)
				if ejected || loggedOut {
					s.mu.Lock()
					s.inRoom = false
					if ejected {
						s.lastErr = fmt.Errorf("исключён из комнаты")
					}
					s.mu.Unlock()
					logx.Warnf("bbb", "ddp: гость=%q выбит из комнаты: ejected=%v loggedOut=%v", s.req.FIO, ejected, loggedOut)
				}
			}
		}
	case "meetings":
		if fields != nil {
			if ended, ok := fields["meetingEnded"].(bool); ok && ended {
				s.mu.Lock()
				s.inRoom = false
				s.lastErr = fmt.Errorf("встреча завершена")
				s.mu.Unlock()
				logx.Infof("bbb", "ddp: встреча завершена (meetingEnded) гость=%q", s.req.FIO)
			}
		}
	case "presentations":
		s.presMu.Lock()
		if msgType == "removed" {
			pm := s.pres[docID]
			delete(s.pres, docID)
			if pm.ID != "" {
				delete(s.pres, pm.ID)
			}
		} else if fields != nil {
			pm := s.allPres[docID]
			if pm.ID == "" {
				pm = s.pres[docID]
			}
			if pm.ID == "" {
				pm.ID = docID
			}
			if v, ok := fields["id"].(string); ok && v != "" {
				pm.ID = v
			}
			if v, ok := fields["name"].(string); ok {
				pm.Name = v
			}
			if v, ok := fields["current"].(bool); ok {
				pm.Current = v
			}
			s.pres[docID] = pm
			s.allPres[docID] = pm
			if pm.ID != "" {
				s.pres[pm.ID] = pm
				s.allPres[pm.ID] = pm
			}
			found := false
			for _, pid := range s.presOrder {
				if pid == docID || pid == pm.ID {
					found = true
					break
				}
			}
			if !found {
				s.presOrder = append(s.presOrder, docID)
			}
			if pm.Current {
				now := time.Now().UTC()
				for sid, sl := range s.allSlides {
					if sl.PresentationID == docID || sl.PresentationID == pm.ID {
						if sl.Current && sl.FirstShownAt.IsZero() {
							sl.FirstShownAt = now
							s.allSlides[sid] = sl
							if _, inPres := s.slides[sid]; inPres {
								s.slides[sid] = sl
							}
						}
					}
				}
			}
		}
		s.presMu.Unlock()
	case "slides":
		s.presMu.Lock()
		if msgType == "removed" {
			delete(s.slides, docID)
		} else if fields != nil {
			sm := s.allSlides[docID]
			if sm.ID == "" {
				sm = s.slides[docID]
			}
			if sm.DocID == "" {
				sm.DocID = docID
			}
			if sm.ID == "" {
				sm.ID = docID
			}
			if v, ok := fields["id"].(string); ok && v != "" {
				sm.ID = v
			}
			if v, ok := fields["presentationId"].(string); ok && v != "" {
				sm.PresentationID = v
			}
			if v, ok := fields["num"].(float64); ok {
				sm.Num = int(v)
			}
			if v, ok := fields["imageUri"].(string); ok && v != "" {
				sm.ImageURI = v
			}
			if v, ok := fields["thumbUri"].(string); ok && v != "" {
				sm.ThumbURI = v
			}
			if v, ok := fields["txtUri"].(string); ok && v != "" {
				sm.TxtURI = v
			}
			if v, ok := fields["content"].(string); ok && v != "" {
				sm.Content = v
			}
			if v, ok := fields["current"].(bool); ok {
				sm.Current = v
			}
			if sm.Current && sm.FirstShownAt.IsZero() {
				if s.isPresentationActiveLocked(sm.PresentationID) {
					sm.FirstShownAt = time.Now().UTC()
				}
			}
			s.slides[docID] = sm
			s.allSlides[docID] = sm
		}
		s.presMu.Unlock()
	}
}

func (s *directSession) isPresentationActiveLocked(presID string) bool {
	if presID == "" {
		return true
	}
	pm, ok := s.allPres[presID]
	if !ok {
		pm, ok = s.pres[presID]
	}
	if !ok {
		for _, p := range s.allPres {
			if p.ID == presID {
				pm = p
				ok = true
				break
			}
		}
		if !ok {
			for _, p := range s.pres {
				if p.ID == presID {
					pm = p
					ok = true
					break
				}
			}
		}
	}
	if !ok {
		return true
	}
	if pm.Current {
		return true
	}
	hasAnyCurrent := false
	for _, p := range s.allPres {
		if p.Current {
			hasAnyCurrent = true
			break
		}
	}
	if !hasAnyCurrent {
		for _, p := range s.pres {
			if p.Current {
				hasAnyCurrent = true
				break
			}
		}
	}
	return !hasAnyCurrent
}

func (s *directSession) advanceLobby(ctx context.Context) error {
	s.mu.Lock()
	lobby := s.inLobby
	waitGreen := s.waitGreen
	base := s.baseURL
	tok := s.sessionToken
	finalURL := s.finalURL
	s.mu.Unlock()
	if !lobby {
		return nil
	}
	if waitGreen {
		// Re-check Greenlight; once the moderator starts the room, POST yields sessionToken.
		logx.Debugf("bbb", "lobby: Greenlight — перепроверяю страницу гость=%q", s.req.FIO)
		return s.stepEnter(ctx)
	}
	if tok == "" || base == "" {
		return nil
	}
	er, err := s.fetchEnter(ctx, base, tok)
	if err != nil {
		logx.Warnf("bbb", "lobby: enter-опрос гость=%q не удался: %v", s.req.FIO, err)
		return err
	}
	switch strings.ToUpper(er.GuestStatus) {
	case "WAIT":
		logx.Debugf("bbb", "lobby: guestWait, модератор не пустил (returncode=%s)", er.ReturnCode)
		return nil
	case "DENY":
		logx.Warnf("bbb", "lobby: модератор отклонил вход гостя=%q", s.req.FIO)
		return fmt.Errorf("модератор отклонил вход")
	default:
		if er.ReturnCode != "SUCCESS" {
			logx.Warnf("bbb", "lobby: enter вернул returncode=%s guestStatus=%s гость=%q", er.ReturnCode, er.GuestStatus, s.req.FIO)
			return fmt.Errorf("enter: %s", er.ReturnCode)
		}
		logx.Infof("bbb", "lobby: модератор пустил гостя=%q (guestStatus=%s) — захожу в комнату", s.req.FIO, er.GuestStatus)
		s.mu.Lock()
		s.enter = er
		s.mu.Unlock()
		return s.enterWithToken(ctx, finalURL, tok)
	}
}

func (s *directSession) InLobby(ctx context.Context) (bool, error) {
	if s == nil {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	closed := s.closed
	lobby := s.inLobby
	s.mu.Unlock()
	if closed {
		return false, fmt.Errorf("session closed")
	}
	if lobby {
		if err := s.advanceLobby(ctx); err != nil {
			return false, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inLobby {
		return true, nil
	}
	select {
	case <-s.ddpDone:
		if s.lastErr != nil {
			return false, s.lastErr
		}
		return false, fmt.Errorf("ddp disconnected")
	default:
	}
	return false, s.lastErr
}

func (s *directSession) InRoom(ctx context.Context) (bool, error) {
	if s == nil {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	closed := s.closed
	lobby := s.inLobby
	s.mu.Unlock()
	if closed {
		return false, fmt.Errorf("session closed")
	}
	if lobby {
		if err := s.advanceLobby(ctx); err != nil {
			return false, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inLobby {
		return false, nil
	}
	select {
	case <-s.ddpDone:
		if s.lastErr != nil {
			return false, s.lastErr
		}
		return false, fmt.Errorf("ddp disconnected")
	default:
	}
	return s.inRoom, s.lastErr
}

func (s *directSession) Greet(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if s.greeted || s.closed {
		s.mu.Unlock()
		return nil
	}
	room := s.inRoom && !s.inLobby
	chatID := s.chatID
	enter := s.enter
	s.mu.Unlock()
	if !room {
		return nil
	}

	corrID := fmt.Sprintf("%s-%d", enter.InternalUserID, time.Now().UnixMilli())
	addedCh := make(chan struct{})
	s.msgAddedMu.Lock()
	s.msgAdded[corrID] = addedCh
	s.msgAddedMu.Unlock()
	defer func() {
		s.msgAddedMu.Lock()
		delete(s.msgAdded, corrID)
		s.msgAddedMu.Unlock()
	}()

	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	_, err := s.callDDP(cctx, "sendGroupChatMsg", []any{
		chatID,
		map[string]any{
			"correlationId": corrID,
			"sender": map[string]any{
				"id":   enter.InternalUserID,
				"name": "",
				"role": "",
			},
			"chatEmphasizedText": false,
			"message":            helloText,
		},
	})
	if err != nil {
		return fmt.Errorf("directSession.Greet: %w", err)
	}
	select {
	case <-addedCh:
	case <-time.After(3 * time.Second):
	case <-cctx.Done():
	}

	s.mu.Lock()
	s.greeted = true
	s.mu.Unlock()
	logx.Infof("bbb", "greeted direct user=%s", enter.InternalUserID)
	return nil
}

func (s *directSession) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.inRoom = false
	s.inLobby = false
	ws := s.ddpWS
	startedAt := s.startedAt
	greeted := s.greeted
	s.mu.Unlock()

	rtp := s.RTPPackets()
	s.stopAudio()
	s.cancel()
	if !startedAt.IsZero() {
		logx.Infof("bbb", "direct: сессия закрыта гость=%q роль=%s, жила %s, RTP=%d, здоровалась=%v",
			s.req.FIO, s.req.Role, time.Since(startedAt).Round(time.Second), rtp, greeted)
	}

	if ws != nil {
		lctx, lcancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = s.callDDP(lctx, "userLeftMeeting", []any{})
		lcancel()
		_ = ws.Close()
	}
	if s.client != nil {
		s.client.CloseIdleConnections()
	}
	return nil
}

// truncateFrame — кадр DDP/SockJS для лога, без простыни.
func truncateFrame(s string) string {
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}

func (s *directSession) urlWithHost(pathStr string) string {
	s.mu.Lock()
	base := s.baseURL
	s.mu.Unlock()
	if strings.HasPrefix(pathStr, "http://") || strings.HasPrefix(pathStr, "https://") {
		return pathStr
	}
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path.Clean("/"+pathStr), "/")
}
