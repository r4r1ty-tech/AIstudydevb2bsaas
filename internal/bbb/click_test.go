package bbb

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// Prod 17.09: the text fallback clicked the outer #app div (its innerText has
// every label on the page), reported success, and the button stayed unpressed.
func TestClickByTextHitsInnerButton(t *testing.T) {
	bin := FindChrome("")
	if bin == "" {
		t.Skip("no chromium")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><div id="app"><div class="modal"><p>Как подключиться к аудио?</p>
<button type="button" onclick="document.body.dataset.ok='1'">Только слушать</button></div></div>`)
	}))
	defer srv.Close()
	j := NewChromeJoiner(bin)
	t.Cleanup(func() { _ = j.Close() })
	root, err := j.ensureBrowser()
	if err != nil {
		t.Fatal(err)
	}
	page, err := root.Page(proto.TargetCreateTarget{URL: srv.URL + "/"})
	if err != nil {
		t.Fatal(err)
	}
	_ = page.Timeout(15 * time.Second).WaitLoad()
	if !clickByText(page, listenOnlyRE) {
		t.Fatal("clickByText found nothing")
	}
	res, err := page.Eval(`() => document.body.dataset.ok || ''`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Value.Str() != "1" {
		t.Fatal("reported a click, but the button was not pressed")
	}
	if clickByText(page, `(?i)нет такого текста`) {
		t.Fatal("no match must report false")
	}
}

func openLocal(t *testing.T, html string) *rod.Page {
	t.Helper()
	bin := FindChrome("")
	if bin == "" {
		t.Skip("no chromium")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, html)
	}))
	t.Cleanup(srv.Close)
	j := NewChromeJoiner(bin)
	t.Cleanup(func() { _ = j.Close() })
	root, err := j.ensureBrowser()
	if err != nil {
		t.Fatal(err)
	}
	page, err := root.Page(proto.TargetCreateTarget{URL: srv.URL + "/"})
	if err != nil {
		t.Fatal(err)
	}
	_ = page.Timeout(15 * time.Second).WaitLoad()
	return page
}

func dataset(t *testing.T, page *rod.Page, key string) string {
	t.Helper()
	res, err := page.Eval(`(k) => document.body.dataset[k] || ''`, key)
	if err != nil {
		t.Fatal(err)
	}
	return res.Value.Str()
}

// bbb_auto_join_audio already put the tab in listen-only: the navbar shows
// "Leave audio". audioOnce must see that and not click anything.
func TestAudioOnceAlreadyJoinedLeavesNavbarAlone(t *testing.T) {
	page := openLocal(t, `<!doctype html><div id="app"><nav data-test="navBar">
<button data-test="leaveAudio" aria-label="Leave audio" onclick="document.body.dataset.left='1'">Leave audio</button>
</nav></div>`)
	old := audioJoinWait
	audioJoinWait = 5 * time.Second
	t.Cleanup(func() { audioJoinWait = old })

	start := time.Now()
	audioOnce(page, RoleRecord)
	if dataset(t, page, "left") == "1" {
		t.Fatal("audioOnce clicked Leave audio")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("audioOnce should return at once when already in audio, took %s", time.Since(start))
	}
}

// No modal yet: audioOnce opens the chooser from the navbar, then presses
// listen-only in the modal that appears.
func TestAudioOnceOpensChooserThenListenOnly(t *testing.T) {
	page := openLocal(t, `<!doctype html><div id="app">
<button data-test="joinAudio" onclick="document.getElementById('m').style.display='block'">Join audio</button>
<div id="m" style="display:none"><button onclick="document.body.dataset.listen='1'">Только слушать</button></div></div>`)
	old := audioJoinWait
	audioJoinWait = 10 * time.Second
	t.Cleanup(func() { audioJoinWait = old })

	audioOnce(page, RoleRecord)
	if dataset(t, page, "listen") != "1" {
		t.Fatal("listen-only not pressed after opening the chooser")
	}
}

func TestChromeAudioLine(t *testing.T) {
	t.Parallel()
	for line, want := range map[string]bool{
		"[ERROR:pulse_util.cc] pa_context_connect() failed": true,
		"ALSA lib pcm.c: Unknown PCM default":               true,
		"[WARNING:webrtc_voice_engine.cc] no audio device":  true,
		"Failed to connect to the bus: service unknown":     false,
		"[ERROR:gpu_init.cc] Passthrough is not supported":  false,
	} {
		if got := chromeAudioLine(line); got != want {
			t.Errorf("chromeAudioLine(%q) = %v, want %v", line, got, want)
		}
	}
}
