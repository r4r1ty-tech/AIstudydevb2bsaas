// Package moodle — клиент Moodle do.ssau.ru для студента: вход по форме, курсы,
// лента сроков и разбор страниц заданий и тестов (срок, что сделать, статус).
//
// Мобильный веб-сервис на сайте выключен (enablemobilewebservice=0), поэтому
// работаем как браузер: кука MoodleSession, sesskey со страницы и AJAX
// /lib/ajax/service.php. core_course_get_contents там закрыт — состав курса
// берём из HTML course/view.php.
package moodle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/websess"
)

const DefaultBase = "https://do.ssau.ru/moodle"

var (
	ErrBadLogin     = errors.New("moodle: bad login")
	ErrUnauthorized = errors.New("moodle: session expired")
)

var sesskeyRe = regexp.MustCompile(`"sesskey":"([A-Za-z0-9]+)"`)

type Client struct {
	s *websess.Session

	mu      sync.Mutex
	sesskey string
}

func New(base string) (*Client, error) {
	if base == "" {
		base = DefaultBase
	}
	s, err := websess.New(base, 40*time.Second)
	if err != nil {
		return nil, err
	}
	return &Client{s: s}, nil
}

func (c *Client) Session() string { return c.s.Export() }

func (c *Client) Restore(raw string) {
	c.s.Import(raw)
	c.mu.Lock()
	c.sesskey = ""
	c.mu.Unlock()
}

func onLoginPage(resp *http.Response) bool {
	return resp != nil && resp.Request != nil && strings.Contains(resp.Request.URL.Path, "/login/index.php")
}

// Login входит по форме. Неверный логин/пароль — ErrBadLogin.
func (c *Client) Login(ctx context.Context, login, password string) error {
	login = strings.TrimSpace(login)
	if login == "" || password == "" {
		return ErrBadLogin
	}
	if err := c.s.Clear(); err != nil {
		return err
	}
	resp, page, err := c.s.Get(ctx, "/login/index.php", "text/html")
	if err != nil {
		return fmt.Errorf("moodle: login page: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("moodle: login page status %d", resp.StatusCode)
	}
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(page))
	if err != nil {
		return fmt.Errorf("moodle: parse login page: %w", err)
	}
	form := doc.Find("form#login").First()
	if form.Length() == 0 {
		form = doc.Find("form").FilterFunction(func(_ int, f *goquery.Selection) bool {
			return f.Find("input[name=password]").Length() > 0
		}).First()
	}
	if form.Length() == 0 {
		return errors.New("moodle: login form not found")
	}
	vals := url.Values{}
	form.Find("input[name]").Each(func(_ int, in *goquery.Selection) {
		name, _ := in.Attr("name")
		if t, _ := in.Attr("type"); t == "checkbox" || t == "submit" {
			return
		}
		v, _ := in.Attr("value")
		vals.Set(name, v)
	})
	vals.Set("username", login)
	vals.Set("password", password)
	action := form.AttrOr("action", c.s.URL("/login/index.php"))

	req, err := c.s.Request(ctx, http.MethodPost, action, strings.NewReader(vals.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Referer", c.s.URL("/login/index.php"))
	resp, body, err := c.s.Do(req)
	if err != nil {
		return fmt.Errorf("moodle: login post: %w", err)
	}
	if onLoginPage(resp) {
		logx.Infof("moodle", "login rejected status=%d", resp.StatusCode)
		return ErrBadLogin
	}
	if key := findSesskey(body); key != "" {
		c.setSesskey(key)
	} else if _, err := c.refreshSesskey(ctx); err != nil {
		if errors.Is(err, ErrUnauthorized) {
			return ErrBadLogin
		}
		return err
	}
	logx.Infof("moodle", "login ok")
	return nil
}

func findSesskey(page []byte) string {
	if m := sesskeyRe.FindSubmatch(page); len(m) == 2 {
		return string(m[1])
	}
	return ""
}

func (c *Client) setSesskey(k string) {
	c.mu.Lock()
	c.sesskey = k
	c.mu.Unlock()
}

func (c *Client) refreshSesskey(ctx context.Context) (string, error) {
	resp, body, err := c.s.Get(ctx, "/my/", "text/html")
	if err != nil {
		return "", fmt.Errorf("moodle: dashboard: %w", err)
	}
	if onLoginPage(resp) {
		return "", ErrUnauthorized
	}
	key := findSesskey(body)
	if key == "" {
		return "", errors.New("moodle: no sesskey on dashboard")
	}
	c.setSesskey(key)
	return key, nil
}

func (c *Client) key(ctx context.Context) (string, error) {
	c.mu.Lock()
	k := c.sesskey
	c.mu.Unlock()
	if k != "" {
		return k, nil
	}
	return c.refreshSesskey(ctx)
}

// Check — жива ли сохранённая сессия.
func (c *Client) Check(ctx context.Context) error {
	_, err := c.refreshSesskey(ctx)
	return err
}

type ajaxReply struct {
	Error     bool            `json:"error"`
	Data      json.RawMessage `json:"data"`
	Exception *struct {
		Message   string `json:"message"`
		ErrorCode string `json:"errorcode"`
	} `json:"exception"`
}

// Ajax вызывает метод внешнего API Moodle так же, как страница.
func (c *Client) Ajax(ctx context.Context, method string, args any, out any) error {
	key, err := c.key(ctx)
	if err != nil {
		return err
	}
	payload, err := json.Marshal([]map[string]any{{"index": 0, "methodname": method, "args": args}})
	if err != nil {
		return err
	}
	path := "/lib/ajax/service.php?sesskey=" + url.QueryEscape(key) + "&info=" + url.QueryEscape(method)
	req, err := c.s.Request(ctx, http.MethodPost, path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	resp, body, err := c.s.Do(req)
	if err != nil {
		return fmt.Errorf("moodle: %s: %w", method, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("moodle: %s: status %d", method, resp.StatusCode)
	}
	var replies []ajaxReply
	if err := json.Unmarshal(body, &replies); err != nil {
		// Протухшая сессия иногда отдаёт объект ошибки, а не массив.
		var one ajaxReply
		if json.Unmarshal(body, &one) == nil && one.Error {
			replies = []ajaxReply{one}
		} else {
			return fmt.Errorf("moodle: %s: decode: %w", method, err)
		}
	}
	if len(replies) == 0 {
		return fmt.Errorf("moodle: %s: empty reply", method)
	}
	r := replies[0]
	if r.Error {
		code, msg := "", ""
		if r.Exception != nil {
			code, msg = r.Exception.ErrorCode, r.Exception.Message
		}
		switch code {
		case "servicerequireslogin", "invalidsesskey", "requireloginerror", "sitepolicynotagreed":
			c.setSesskey("")
			return ErrUnauthorized
		}
		return fmt.Errorf("moodle: %s: %s %s", method, code, msg)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(r.Data, out); err != nil {
		return fmt.Errorf("moodle: %s: decode data: %w", method, err)
	}
	return nil
}

type Course struct {
	ID       int64  `json:"id"`
	FullName string `json:"fullname"`
	Hidden   bool   `json:"hidden"`
	ViewURL  string `json:"viewurl"`
}

// Courses — все курсы, куда записан студент (кроме скрытых). Только «текущих»
// мало: у прошлогоднего курса может висеть открытый тест (так было с «Теорией информации»).
func (c *Client) Courses(ctx context.Context) ([]Course, error) {
	fetch := func(class string) ([]Course, error) {
		var out struct {
			Courses []Course `json:"courses"`
		}
		err := c.Ajax(ctx, "core_course_get_enrolled_courses_by_timeline_classification", map[string]any{
			"offset": 0, "limit": 0, "classification": class, "sort": "fullname",
		}, &out)
		return out.Courses, err
	}
	list, err := fetch("all")
	if err != nil {
		return nil, err
	}
	out := list[:0]
	for _, cr := range list {
		if !cr.Hidden {
			out = append(out, cr)
		}
	}
	return out, nil
}

type Event struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	ModuleName string `json:"modulename"`
	Instance   int64  `json:"instance"`
	EventType  string `json:"eventtype"`
	TimeSort   int64  `json:"timesort"`
	Overdue    bool   `json:"overdue"`
	URL        string `json:"url"`
	Course     struct {
		ID       int64  `json:"id"`
		FullName string `json:"fullname"`
	} `json:"course"`
	Action *struct {
		Name       string `json:"name"`
		Actionable bool   `json:"actionable"`
	} `json:"action"`
}

// CMID — id модуля курса из ссылки события (view.php?id=…).
func (e Event) CMID() int64 { return cmidFromURL(e.URL) }

// Timeline — события «надо сделать» с from по to. Moodle убирает событие,
// когда действие выполнено, так что лента — это список несделанного.
func (c *Client) Timeline(ctx context.Context, from, to time.Time) ([]Event, error) {
	const page = 50 // больше сервер не принимает
	var all []Event
	after := int64(0)
	for i := 0; i < 20; i++ {
		args := map[string]any{"timesortfrom": from.Unix(), "limitnum": page}
		if !to.IsZero() {
			args["timesortto"] = to.Unix()
		}
		if after > 0 {
			args["aftereventid"] = after
		}
		var out struct {
			Events []Event `json:"events"`
		}
		if err := c.Ajax(ctx, "core_calendar_get_action_events_by_timesort", args, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Events...)
		if len(out.Events) < page {
			break
		}
		after = out.Events[len(out.Events)-1].ID
	}
	return all, nil
}

// page — GET HTML-страницы Moodle с проверкой, что нас не выкинуло на вход.
func (c *Client) page(ctx context.Context, path string) (*goquery.Document, error) {
	resp, body, err := c.s.Get(ctx, path, "text/html")
	if err != nil {
		return nil, fmt.Errorf("moodle: GET %s: %w", path, err)
	}
	if onLoginPage(resp) {
		c.setSesskey("")
		return nil, ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("moodle: GET %s: status %d", path, resp.StatusCode)
	}
	if key := findSesskey(body); key != "" {
		c.setSesskey(key)
	}
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("moodle: parse %s: %w", path, err)
	}
	return doc, nil
}
