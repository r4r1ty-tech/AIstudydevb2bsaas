package bbb

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/proto"
)

func TestFindChrome(t *testing.T) {
	if p := FindChrome(""); p == "" {
		t.Skip("no chromium on this machine")
	}
}

func TestChromeGuestJoinLocalHTML(t *testing.T) {
	bin := FindChrome("")
	if bin == "" {
		t.Skip("no chromium")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><form method="get" action="/room">
<input id="join_name" name="join_name" />
<button type="submit">Join</button>
</form>`)
	})
	mux.HandleFunc("/room", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("join_name")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><div data-test="userListItem">%s</div>
<button data-test="listenOnlyBtn">Listen only</button>
<textarea data-test="messageInput"></textarea>
<button data-test="sendMessageButton" onclick="document.body.dataset.msg=document.querySelector('[data-test=messageInput]').value">Send</button>`, name)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	j := NewChromeJoiner(bin)
	t.Cleanup(func() { _ = j.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	sess, err := j.Join(ctx, JoinReq{URL: srv.URL + "/", FIO: "Иванов Иван"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	cs, ok := sess.(*chromeSession)
	if !ok || cs.page == nil {
		t.Fatal("expected chrome session")
	}
	html, err := cs.page.Timeout(10 * time.Second).HTML()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, "Иванов Иван") && !strings.Contains(html, "join_name") {
		t.Fatalf("page missing name, html=%s", html)
	}
	lobby, err := sess.InLobby(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if lobby {
		t.Fatal("local meeting should not look like lobby")
	}
	if err := sess.Greet(ctx); err != nil {
		t.Fatal(err)
	}
	res, err := cs.page.Eval(`() => document.body.dataset.msg || ''`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Value.Str() != helloText {
		t.Fatalf("hello = %q", res.Value.Str())
	}
	if err := sess.Greet(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestChromeWelcomeJoinRoom(t *testing.T) {
	bin := FindChrome("")
	if bin == "" {
		t.Skip("no chromium")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><h1>You have been invited to join X</h1>
<button data-test="joinButton" onclick="location.href='/form'">Join Room</button>`)
	})
	mux.HandleFunc("/form", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><form method="get" action="/room">
<input id="join_name" name="join_name"><button type="submit">Join</button></form>`)
	})
	mux.HandleFunc("/room", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("join_name")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><div data-test="userListItem">%s</div>
<button data-test="listenOnlyBtn">Listen only</button>`, name)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	j := NewChromeJoiner(bin)
	t.Cleanup(func() { _ = j.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	sess, err := j.Join(ctx, JoinReq{URL: srv.URL + "/", FIO: "Иванов Иван"})
	if err != nil {
		t.Fatalf("join through welcome screen: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	room, err := sess.InRoom(ctx)
	if err != nil || !room {
		t.Fatalf("InRoom = %v, %v", room, err)
	}
}

func TestJSRegexpStripsGoFlags(t *testing.T) {
	t.Parallel()
	if got := jsRegexp(`(?i)listen\s*only|только\s*слушать`); got != `listen\s*only|только\s*слушать` {
		t.Fatalf("jsRegexp = %q", got)
	}
}

func TestClickListenOnlyByRussianLabel(t *testing.T) {
	bin := FindChrome("")
	if bin == "" {
		t.Skip("no chromium")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html>
<button type="button" onclick="document.body.dataset.ok='1'">Только слушать</button>`)
	})
	srv := httptest.NewServer(mux)
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
	t.Cleanup(func() { _ = page.Close() })
	_ = page.Timeout(15 * time.Second).WaitLoad()

	if err := clickListenOnly(page); err != nil {
		t.Fatal(err)
	}
	res, err := page.Eval(`() => document.body.dataset.ok || ''`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Value.Str() != "1" {
		t.Fatalf("listen-only click did not land, got %q", res.Value.Str())
	}
}
