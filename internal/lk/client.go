// Package lk — клиент личного кабинета lk.ssau.ru: вход, расписание группы
// (с подгруппами, онлайн-неделями, ссылками BBB и парами ИОТ).
//
// ЛК — Next.js. Вход — server action: обычная multipart-форма со скрытыми
// полями $ACTION_*, которые отдаёт страница входа. Данные — JSON из /api/proxy/*
// с HttpOnly-кукой сессии; без неё 401.
package lk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/websess"
)

const DefaultBase = "https://lk.ssau.ru"

var (
	ErrBadLogin     = errors.New("lk: bad login")
	ErrUnauthorized = errors.New("lk: session expired")
)

type Client struct {
	s *websess.Session
}

func New(base string) (*Client, error) {
	if base == "" {
		base = DefaultBase
	}
	s, err := websess.New(base, 30*time.Second)
	if err != nil {
		return nil, err
	}
	s.Name = "lk"
	return &Client{s: s}, nil
}

// Session — куки для сохранения; Restore — поднять их.
func (c *Client) Session() string    { return c.s.Export() }
func (c *Client) Restore(raw string) { c.s.Import(raw) }

// Login входит заново. Неверный логин/пароль — ErrBadLogin.
func (c *Client) Login(ctx context.Context, login, password string) error {
	login = strings.TrimSpace(login)
	if login == "" || password == "" {
		return ErrBadLogin
	}
	if err := c.s.Clear(); err != nil {
		return err
	}
	resp, page, err := c.s.Get(ctx, "/account/login", "text/html")
	if err != nil {
		return fmt.Errorf("lk: login page: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("lk: login page status %d", resp.StatusCode)
	}
	fields, err := loginFields(page)
	if err != nil {
		return err
	}
	fields.Set("login", login)
	fields.Set("password", password)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, k := range fieldOrder(fields) {
		for _, v := range fields[k] {
			if err := mw.WriteField(k, v); err != nil {
				return err
			}
		}
	}
	if err := mw.Close(); err != nil {
		return err
	}
	req, err := c.s.Request(ctx, http.MethodPost, "/account/login", &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Origin", c.s.Base.Scheme+"://"+c.s.Base.Host)
	req.Header.Set("Referer", c.s.URL("/account/login"))
	resp, body, err := c.s.Do(req)
	if err != nil {
		return fmt.Errorf("lk: login post: %w", err)
	}
	if bytes.Contains(body, []byte("Неверный логин")) {
		logx.Debugf("lk", "login: отказ — сайт вернул «Неверный логин» (status=%d)", resp.StatusCode)
		return ErrBadLogin
	}
	// Успешный вход ставит куку и редиректит; проверяем по API, а не по вёрстке.
	if _, err := c.Groups(ctx); err != nil {
		if errors.Is(err, ErrUnauthorized) {
			logx.Warnf("lk", "login: no session after post status=%d", resp.StatusCode)
			return ErrBadLogin
		}
		return err
	}
	logx.Debugf("lk", "login: ok (сессия подтверждена через /personal/groups)")
	return nil
}

// loginFields — все поля формы входа со значениями (server action их требует).
func loginFields(page []byte) (url.Values, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(page))
	if err != nil {
		return nil, fmt.Errorf("lk: parse login page: %w", err)
	}
	var form *goquery.Selection
	doc.Find("form").EachWithBreak(func(_ int, f *goquery.Selection) bool {
		if f.Find("input[name=password]").Length() > 0 {
			form = f
			return false
		}
		return true
	})
	if form == nil {
		return nil, errors.New("lk: login form not found")
	}
	out := url.Values{}
	form.Find("input[name]").Each(func(_ int, in *goquery.Selection) {
		name, _ := in.Attr("name")
		val, _ := in.Attr("value")
		out.Add(name, val)
	})
	if !strings.Contains(strings.Join(fieldOrder(out), " "), "$ACTION") {
		return nil, errors.New("lk: login form has no server action fields")
	}
	return out, nil
}

// fieldOrder — служебные поля Next первыми, как шлёт браузер.
func fieldOrder(v url.Values) []string {
	var action, rest []string
	for k := range v {
		if strings.HasPrefix(k, "$ACTION") {
			action = append(action, k)
		} else {
			rest = append(rest, k)
		}
	}
	sort.Strings(action)
	sort.Strings(rest)
	return append(action, rest...)
}

// getJSON тянет JSON и возвращает сырой ответ (для снимка, если разбор ничего не нашёл).
func (c *Client) getJSON(ctx context.Context, path string, out any) ([]byte, error) {
	resp, body, err := c.s.Get(ctx, path, "application/json")
	if err != nil {
		return nil, fmt.Errorf("lk: GET %s: %w", path, err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return body, ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return body, fmt.Errorf("lk: GET %s: status %d", path, resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return body, fmt.Errorf("lk: GET %s: decode: %w", path, err)
	}
	return body, nil
}

type Group struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Groups — группы студента (обычно одна).
func (c *Client) Groups(ctx context.Context) ([]Group, error) {
	var out []Group
	if _, err := c.getJSON(ctx, "/api/proxy/personal/groups", &out); err != nil {
		return out, err
	}
	logx.Debugf("lk", "группы: %d", len(out))
	for _, g := range out {
		logx.Debugf("lk", "группа id=%d %q", g.ID, g.Name)
	}
	return out, nil
}

type Year struct {
	ID   int64  `json:"id"`
	Name string `json:"name"` // "2026-2027"
}

// Years — учебные годы ЛК; id нужен расписанию.
func (c *Client) Years(ctx context.Context) ([]Year, error) {
	var out []Year
	if _, err := c.getJSON(ctx, "/api/proxy/statements/student/years", &out); err != nil {
		return out, err
	}
	names := make([]string, 0, len(out))
	for _, y := range out {
		names = append(names, fmt.Sprintf("%d=%s", y.ID, y.Name))
	}
	logx.Debugf("lk", "учебные годы: %d [%s]", len(out), strings.Join(names, ", "))
	return out, nil
}

// CurrentYearID — год, в который попадает now (учебный год с сентября).
func CurrentYearID(years []Year, now time.Time) (int64, bool) {
	start := now.Year()
	if now.Month() < time.August {
		start--
	}
	prefix := strconv.Itoa(start) + "-"
	var best int64
	for _, y := range years {
		if strings.HasPrefix(strings.TrimSpace(y.Name), prefix) {
			return y.ID, true
		}
		if y.ID > best {
			best = y.ID
		}
	}
	return best, best > 0
}

// Timetable — ответ get-timetable за одну неделю.
type Timetable struct {
	Lessons     []TTLesson `json:"lessons"`
	IET         []TTLesson `json:"ietLessons"`
	HasSession  bool       `json:"hasSession"`
	CurrentYear struct {
		ID        int64  `json:"id"`
		StartDate string `json:"startDate"` // понедельник первой недели
		Weeks     int    `json:"weeks"`
	} `json:"currentYear"`

	raw []byte // сырой ответ ЛК: снимок, если разбор не нашёл пар
}

type named struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type TTLesson struct {
	ID      int64 `json:"id"`
	Weekday struct {
		ID int `json:"id"` // 1 = понедельник
	} `json:"weekday"`
	Time struct {
		Begin string `json:"beginTime"`
		End   string `json:"endTime"`
	} `json:"time"`
	Discipline *named  `json:"discipline"`
	Type       *named  `json:"type"`
	Teachers   []named `json:"teachers"`
	Groups     []struct {
		ID       int64 `json:"id"`
		Subgroup *int  `json:"subgroup"`
	} `json:"groups"`
	Flows []struct {
		Subgroup   *int   `json:"subgroup"`
		Discipline *named `json:"discipline"`
		LoadType   *named `json:"loadType"`
	} `json:"flows"`
	Conference *struct {
		URL string `json:"url"`
	} `json:"conference"`
	Comment string   `json:"comment"`
	Weeks   []TTWeek `json:"weeks"`
}

type TTWeek struct {
	Week     int    `json:"week"`
	IsOnline int    `json:"isOnline"`
	Room     *named `json:"room"`
	Building *named `json:"building"`
}

// Timetable — расписание группы на неделю week учебного года yearID.
func (c *Client) Timetable(ctx context.Context, yearID int64, week int, groupID int64) (*Timetable, error) {
	q := url.Values{}
	q.Set("yearId", strconv.FormatInt(yearID, 10))
	q.Set("week", strconv.Itoa(week))
	q.Set("userType", "student")
	q.Set("groupId", strconv.FormatInt(groupID, 10))
	var out Timetable
	raw, err := c.getJSON(ctx, "/api/proxy/timetable/get-timetable?"+q.Encode(), &out)
	if err != nil {
		return nil, err
	}
	out.raw = raw
	logx.Debugf("lk", "расписание год=%d неделя=%d группа=%d: lessons=%d ietLessons=%d, начало года %s, hasSession=%v",
		yearID, week, groupID, len(out.Lessons), len(out.IET), out.CurrentYear.StartDate, out.HasSession)
	return &out, nil
}

// fullNameRe — ФИО в RSC-разметке /account: {"name":…,"fullName":"Фамилия Имя Отчество",…}
// (в HTML кавычки экранированы \").
var fullNameRe = regexp.MustCompile(`\\?"fullName\\?"\s*:\s*\\?"([^"\\]{2,120})`)

// FullName — ФИО владельца сессии со страницы /account.
func (c *Client) FullName(ctx context.Context) (string, error) {
	resp, body, err := c.s.Get(ctx, "/account", "text/html")
	if err != nil {
		return "", fmt.Errorf("lk: GET /account: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("lk: GET /account: status %d", resp.StatusCode)
	}
	return parseFullName(body)
}

func parseFullName(page []byte) (string, error) {
	m := fullNameRe.FindSubmatch(page)
	if m == nil {
		if bytes.Contains(page, []byte(`name="password"`)) {
			return "", ErrUnauthorized
		}
		return "", errors.New("lk: на /account нет fullName")
	}
	name := strings.Join(strings.Fields(string(m[1])), " ")
	if name == "" {
		return "", errors.New("lk: пустое fullName")
	}
	return name, nil
}
