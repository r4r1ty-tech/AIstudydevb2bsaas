package tg

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

var allowedTag = regexp.MustCompile(`</?(b|i|code|a)( [^>]*)?>`)

func assertSafeHTML(t *testing.T, name, s string) {
	t.Helper()
	rest := allowedTag.ReplaceAllString(s, "")
	if strings.ContainsAny(rest, "<>") {
		t.Fatalf("%s leaked raw angle brackets:\n%s", name, s)
	}
}

func TestEsc(t *testing.T) {
	got := esc(`<b>Иванов & Co</b>`)
	want := `&lt;b&gt;Иванов &amp; Co&lt;/b&gt;`
	if got != want {
		t.Fatalf("esc = %q", got)
	}
	if strings.Contains(got, "<b>") {
		t.Fatal("raw tag leaked")
	}
}

func TestFormatHelpers(t *testing.T) {
	if bold("Матан") != "<b>Матан</b>" {
		t.Fatalf("bold = %q", bold("Матан"))
	}
	if code("11:30") != "<code>11:30</code>" {
		t.Fatalf("code = %q", code("11:30"))
	}
	if italic("x") != "<i>x</i>" {
		t.Fatalf("italic = %q", italic("x"))
	}
	got := hlink("https://bbb.ssau.ru/b/x", "комната")
	if !strings.Contains(got, `href="https://bbb.ssau.ru/b/x"`) || !strings.Contains(got, ">комната</a>") {
		t.Fatalf("hlink = %q", got)
	}
	got = hlink("https://x?a=1&b=2", "x")
	if !strings.Contains(got, "a=1&amp;b=2") {
		t.Fatalf("hlink escape = %q", got)
	}
}

func TestDashOr(t *testing.T) {
	if dashOr("") != "—" || dashOr("  ") != "—" {
		t.Fatal("empty should be dash")
	}
	if dashOr("Матан") != "Матан" {
		t.Fatal("non-empty passthrough")
	}
}

func TestKnownCommand(t *testing.T) {
	for _, ok := range []string{"/start", "/Settings", "/help@ssau_bot", "/cancel", "/today"} {
		if !knownCommand(ok) {
			t.Errorf("%q should be known", ok)
		}
	}
	for _, bad := range []string{"", "привет", "/nope", "/", "/startle"} {
		if knownCommand(bad) {
			t.Errorf("%q should be unknown", bad)
		}
	}
}

func TestCommandName(t *testing.T) {
	cases := map[string]string{
		"/start":      "start",
		"/Help@bot":   "help",
		"/words лаба": "words",
		"  /TODAY  ":  "today",
		"привет":      "",
		"/":           "",
	}
	for in, want := range cases {
		if got := commandName(in); got != want {
			t.Errorf("commandName(%q) = %q want %q", in, got, want)
		}
	}
}

func TestFormatsEscapeHostileInput(t *testing.T) {
	hostile := "<b>x</b> & <script>alert(1)</script>"
	l := model.Lesson{
		Discipline: hostile,
		Teacher:    hostile,
		Start:      "11:30",
		End:        "13:05",
		Begin:      time.Now().Add(time.Minute),
		Finish:     time.Now().Add(time.Hour),
	}
	u := model.User{FIO: hostile, Subgroup: 2, ExtraWords: []string{hostile}}
	now := time.Now()

	assertSafeHTML(t, "lessonHead", formatLessonHead(l, time.UTC))
	assertSafeHTML(t, "t15", formatT15Card(l, now, time.UTC, false))
	assertSafeHTML(t, "joinAck", formatJoinAck(&l, time.UTC, hostile, false))
	assertSafeHTML(t, "skipAck", formatSkipAck(&l, time.UTC))
	assertSafeHTML(t, "today", formatToday(now, time.UTC, hostile, []todayRow{
		{Lesson: l, Presence: model.PresenceError, Detail: hostile},
	}))
	assertSafeHTML(t, "settings", formatSettings(u))
	assertSafeHTML(t, "wakeReply", formatWakeReply(u))
	assertSafeHTML(t, "onboard", formatOnboardDone(u))
	assertSafeHTML(t, "savedLink", formatSavedLink(l))
	assertSafeHTML(t, "live", formatLiveCard(l, "https://bbb.ssau.ru/b/x?a=1&b=2", hostile, model.PresenceRoom, now, time.UTC))
	assertSafeHTML(t, "testCard", formatTestCard(model.TestJoin{
		URL:     "https://bbb.ssau.ru/b/x?a=1&b=<b>",
		Want:    model.TestWantListen,
		Status:  model.TestLobby,
		Name:    hostile,
		Message: hostile,
	}))
	assertSafeHTML(t, "testCardErr", formatTestCard(model.TestJoin{
		URL:     "https://bbb.ssau.ru/b/x",
		Want:    model.TestWantDummy,
		Status:  model.TestError,
		Name:    hostile,
		Message: hostile,
	}))
}

func TestLessonHeadEscapesUserText(t *testing.T) {
	l := model.Lesson{
		Discipline: "<script>alert(1)</script>",
		Teacher:    "Иванов & Сидоров",
		Start:      "11:30",
		End:        "13:05",
	}
	got := formatLessonHead(l, nil)
	if strings.Contains(got, "<script>") {
		t.Fatalf("raw script leaked:\n%s", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") || !strings.Contains(got, "&amp;") {
		t.Fatalf("not escaped:\n%s", got)
	}
}
