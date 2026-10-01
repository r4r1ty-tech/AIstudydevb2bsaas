package bbb

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/proxyrelay"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/proxyrelay/proxytest"
)

const roomHTML = `<!doctype html><div data-test="userListItem">room</div>`

// Chromium never proxies loopback, so the tab asks for bbb.test and the
// fake upstream sends it to the local server — proof the proxy was used.
func TestJoinThroughProxySkipsDeadOne(t *testing.T) {
	bin := FindChrome("")
	if bin == "" {
		t.Skip("no chromium")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, roomHTML)
	}))
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	up := proxytest.Start(t, "u", "good", strings.TrimPrefix(srv.URL, "http://"))

	list, err := proxyrelay.ParseLines(strings.NewReader(up.Entry("bad") + "\n" + up.Entry("good") + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	j := NewChromeJoiner(bin)
	j.Proxies = proxyrelay.NewPool(list)
	t.Cleanup(func() { _ = j.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	sess, err := j.Join(ctx, JoinReq{URL: "http://bbb.test:" + port + "/", FIO: "Тест"})
	if err != nil {
		t.Fatalf("join through the second proxy: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	if up.Rejected.Load() == 0 {
		t.Fatal("first (bad) proxy was never tried")
	}
	hosts := up.Hosts()
	via := false
	for _, h := range hosts {
		via = via || h == "bbb.test:"+port
	}
	if !via {
		t.Fatalf("tab did not go through the proxy: %v", hosts)
	}
	if room, err := sess.InRoom(ctx); err != nil || !room {
		t.Fatalf("InRoom = %v %v", room, err)
	}
}

func TestJoinAllProxiesDeadFails(t *testing.T) {
	bin := FindChrome("")
	if bin == "" {
		t.Skip("no chromium")
	}
	up := proxytest.Start(t, "u", "good", "127.0.0.1:1")
	list, err := proxyrelay.ParseLines(strings.NewReader(up.Entry("bad1") + "\n" + up.Entry("bad2") + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	j := NewChromeJoiner(bin)
	j.Proxies = proxyrelay.NewPool(list)
	t.Cleanup(func() { _ = j.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if _, err := j.Join(ctx, JoinReq{URL: "http://bbb.test:9/", FIO: "Тест"}); err == nil {
		t.Fatal("join must fail when every proxy is rejected")
	}
	if got := up.Rejected.Load(); got < 2 {
		t.Fatalf("both proxies should be tried, rejected=%d", got)
	}
}

func TestWithListenOnlyAudio(t *testing.T) {
	t.Parallel()
	got := withListenOnlyAudio(" https://bbb.ssau.ru/b/aaa-bbb-ccc?x=1 ")
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	for _, k := range []string{"userdata-bbb_auto_join_audio", "userdata-bbb_force_listen_only", "userdata-bbb_listen_only_mode", "userdata-bbb_skip_check_audio"} {
		if q.Get(k) != "true" {
			t.Errorf("%s = %q", k, q.Get(k))
		}
	}
	if q.Get("x") != "1" {
		t.Error("existing query lost")
	}
	if got := withListenOnlyAudio("not a url"); got != "not a url" {
		t.Errorf("bad url changed: %q", got)
	}
}

func TestDismissAudioAndClickJS(t *testing.T) {
	page := openLocal(t, `<!doctype html><div data-test="audioModal">
<button data-test="closeModalButton" onclick="document.body.dataset.closed='1'">×</button>
<button id="covered" onclick="document.body.dataset.js='1'" style="position:relative">js</button>
<div style="position:fixed;inset:0;background:transparent"></div></div>`)
	if err := dismissAudio(page); err != nil {
		t.Fatal(err)
	}
	if dataset(t, page, "closed") != "1" {
		t.Fatal("covered close button was not pressed")
	}
	if !clickJS(page, "#covered") || dataset(t, page, "js") != "1" {
		t.Fatal("clickJS must click through overlays")
	}
	if clickJS(page, "#missing") || clickJS(nil, "#x") || clickJS(page, "") {
		t.Fatal("clickJS on nothing must be false")
	}
}

func TestDismissAudioFallsBackToListenOnly(t *testing.T) {
	page := openLocal(t, `<!doctype html><div data-test="navBar"></div>`)
	start := time.Now()
	// in meeting already: returns without clicking anything
	if err := dismissAudio(page); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("dismissAudio should see the meeting at once")
	}
}

func TestDiagnosticHints(t *testing.T) {
	page := openLocal(t, `<!doctype html><title>BBB</title>
<button data-test="joinAudio" aria-label="Join audio">Join audio</button>
<button data-test="leaveAudio">Leave</button>
<a href="#">link text</a><input value="guest">`)
	hint := clickablesHint(page)
	for _, want := range []string{"dt=joinAudio", "aria=Join audio", `"link text"`, `"guest"`} {
		if !strings.Contains(hint, want) {
			t.Errorf("clickablesHint missing %q: %s", want, hint)
		}
	}
	probe := audioProbeHint(page)
	if !strings.Contains(probe, "joinAudio") || !strings.Contains(probe, "leaveAudio") || strings.Contains(probe, "listenOnlyBtn") {
		t.Fatalf("audioProbeHint = %q", probe)
	}
	if h := pageHint(page); !strings.HasPrefix(h, "BBB | ") {
		t.Fatalf("pageHint = %q", h)
	}
	if clickablesHint(nil) != "" || audioProbeHint(nil) != "" || pageHint(nil) != "" {
		t.Fatal("nil page hints must be empty")
	}
}

func TestGrabSlidesWalksPresentation(t *testing.T) {
	page := openLocal(t, `<!doctype html><body style="margin:0">
<div data-test="presentationInner" id="p" style="width:320px;height:200px;background:#c00">1</div>
<button data-test="nextSlide" id="n" onclick="
  const p = document.getElementById('p'); const i = +p.textContent + 1; p.textContent = i;
  p.style.background = ['#c00','#0c0','#00c'][i-1];
  if (i >= 3) this.remove()">next</button></body>`)
	dir := filepath.Join(t.TempDir(), "slides")
	sess := &chromeSession{page: page}
	n, err := sess.GrabSlides(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("slides = %d, want 3", n)
	}
	for i := 1; i <= 3; i++ {
		if st, err := os.Stat(filepath.Join(dir, fmt.Sprintf("%02d.png", i))); err != nil || st.Size() < 80 {
			t.Fatalf("slide %d: %v", i, err)
		}
	}
	if n, err := (&chromeSession{}).GrabSlides(context.Background(), dir); n != 0 || err != nil {
		t.Fatal("no page: nothing to grab")
	}
}

func TestShotPresentationFallsBackToPage(t *testing.T) {
	page := openLocal(t, `<!doctype html><p>no presentation here</p>`)
	if _, err := shotPresentation(page); err == nil {
		t.Fatal("no presentation area must fail")
	}
	dir := filepath.Join(t.TempDir(), "s")
	n, err := (&chromeSession{page: page}).GrabSlides(context.Background(), dir)
	if err != nil || n != 0 {
		t.Fatalf("no presentation must give no slides (not a page screenshot): n=%d err=%v", n, err)
	}
}

// Последний слайд: кнопка «дальше» остаётся в DOM, но disabled — останавливаемся,
// а не снимаем десятки дублей.
func TestGrabSlidesStopsOnDisabledNext(t *testing.T) {
	page := openLocal(t, `<!doctype html><body style="margin:0">
<div data-test="presentationInner" id="p" style="width:320px;height:200px;background:#c00">1</div>
<button data-test="nextSlide" id="n" onclick="
  const p = document.getElementById('p'); const i = +p.textContent + 1; p.textContent = i;
  p.style.background = ['#c00','#0c0'][i-1]; if (i >= 2) this.disabled = true">next</button></body>`)
	dir := filepath.Join(t.TempDir(), "slides")
	start := time.Now()
	n, err := (&chromeSession{page: page}).GrabSlides(context.Background(), dir)
	if err != nil || n != 2 {
		t.Fatalf("slides = %d %v, want 2", n, err)
	}
	if time.Since(start) > 15*time.Second {
		t.Fatalf("took %s — kept clicking a disabled button", time.Since(start))
	}
}

func TestGreetOnlyInRoomAndOnce(t *testing.T) {
	page := openLocal(t, `<!doctype html><div data-test="guestLobby">wait</div>`)
	s := &chromeSession{page: page}
	if err := s.Greet(context.Background()); err != nil || s.greeted {
		t.Fatalf("lobby must not greet: %v %v", err, s.greeted)
	}
	if err := (&chromeSession{}).Greet(context.Background()); err != nil {
		t.Fatal(err)
	}
	var nilSess *chromeSession
	if err := nilSess.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStringers(t *testing.T) {
	t.Parallel()
	for s, want := range map[seat]string{seatForm: "form", seatLobby: "lobby", seatRoom: "room", seatUnknown: "unknown"} {
		if s.String() != want {
			t.Errorf("seat %d = %q", s, s.String())
		}
	}
	for r, want := range map[Role]string{RolePresence: "presence", RoleRecord: "record", RoleSlides: "slides", Role(9): "unknown"} {
		if r.String() != want {
			t.Errorf("role %d = %q", r, r.String())
		}
	}
	if errString(nil) != "" || errString(errBoom) != "boom" {
		t.Error("errString")
	}
}

func procState(t *testing.T, pid int) byte {
	t.Helper()
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	return s[i+2]
}

// procHogs must SIGSTOP matching processes on Hold and SIGCONT them on the
// last Release — checked on a real child listed in a fake /proc.
func TestProcHogsFreezeAndThaw(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no /proc")
	}
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	pid := child.Process.Pid

	fake := t.TempDir()
	writeCmd(t, fake, fmt.Sprint(pid), "node\x00/root/.cursor-server/bin/cursor-agent\x00")
	g := &procHogs{procDir: fake}

	g.Hold()
	g.Hold()
	time.Sleep(50 * time.Millisecond)
	if st := procState(t, pid); st != 'T' {
		t.Fatalf("after Hold state=%c, want T (stopped)", st)
	}
	g.Release()
	if st := procState(t, pid); st != 'T' {
		t.Fatal("still held once: must stay stopped")
	}
	g.Release()
	time.Sleep(50 * time.Millisecond)
	if st := procState(t, pid); st == 'T' {
		t.Fatal("last Release must thaw")
	}
	g.Release() // extra release: no-op

	g.Hold()
	g.Reset()
	time.Sleep(50 * time.Millisecond)
	if st := procState(t, pid); st == 'T' {
		t.Fatal("Reset must thaw")
	}
	g.Reset() // already clean

	var nilG *procHogs
	nilG.Hold()
	nilG.Release()
	nilG.Reset()
	if newProcHogs().procDir != "/proc" {
		t.Fatal("default proc dir")
	}
	if thawHogs([]int{0x7ffffff0}) != 0 {
		t.Fatal("thawing a missing pid must not count")
	}
}
