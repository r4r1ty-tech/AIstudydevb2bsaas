package tg

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
)

func TestWebAppURLAndPanelMarkup(t *testing.T) {
	empty := &Bot{cfg: &config.Config{}}
	if empty.webAppURL() != "" || empty.panelMarkup() != nil {
		t.Fatal("empty url should yield nothing")
	}
	b := &Bot{cfg: &config.Config{WebAppURL: "https://x.example/base/"}}
	if got := b.webAppURL(); got != "https://x.example/base/" {
		t.Fatalf("url = %q", got)
	}
	mk := b.panelMarkup()
	if mk == nil || mk.InlineKeyboard[0][0].WebApp == nil {
		t.Fatalf("markup = %#v", mk)
	}
	if got := mk.InlineKeyboard[0][0].WebApp.Url; got != "https://x.example/base/" {
		t.Fatalf("webapp url = %q", got)
	}
}

func TestAwaitRoundTrip(t *testing.T) {
	b := &Bot{awaiting: make(map[int64]awaitKind)}
	if b.peekAwait(1) != awaitNone {
		t.Fatal("unknown id must be awaitNone")
	}
	b.setAwait(1, awaitFIO)
	if b.peekAwait(1) != awaitFIO {
		t.Fatal("expected awaitFIO")
	}
	b.clearAwait(1)
	if b.peekAwait(1) != awaitNone {
		t.Fatal("clearAwait failed")
	}
	b.setAwait(2, awaitNone)
}

func TestRememberT15(t *testing.T) {
	b := &Bot{lastT15: make(map[int64]int64)}
	if b.lastT15Lesson(5) != 0 {
		t.Fatal("unknown t15 must be 0")
	}
	b.rememberT15(5, 42)
	if b.lastT15Lesson(5) != 42 {
		t.Fatal("rememberT15 failed")
	}
}

func TestAllowedOnlyWhitelist(t *testing.T) {
	b := &Bot{cfg: &config.Config{Whitelist: []int64{7}}}
	if b.allowed(nil) != nil {
		t.Fatal("nil ctx")
	}
	outsider := &ext.Context{EffectiveUser: &gotgbot.User{Id: 8}}
	if b.allowed(outsider) != nil {
		t.Fatal("outsider must be rejected")
	}
	inside := &ext.Context{EffectiveUser: &gotgbot.User{Id: 7}}
	if b.allowed(inside) == nil {
		t.Fatal("whitelisted user must pass")
	}
}

func TestLeaveCallbackHelpers(t *testing.T) {
	if got := leaveCallbackData(15); got != "x:15" {
		t.Fatalf("data = %q", got)
	}
	id, ok := parseLeaveCallback("x:15")
	if !ok || id != 15 {
		t.Fatalf("parse = %d %v", id, ok)
	}
	for _, bad := range []string{"", "x:", "x:0", "y:1", "x:1:2"} {
		if _, ok := parseLeaveCallback(bad); ok {
			t.Errorf("parseLeaveCallback(%q) should fail", bad)
		}
	}
}

func TestTestLiveKey(t *testing.T) {
	a := testLiveKey(model.TestJoin{Status: model.TestRoom, URL: "u", Want: model.TestWantDummy, Mode: model.TestWantDummy})
	b := testLiveKey(model.TestJoin{Status: model.TestLobby, URL: "u", Want: model.TestWantDummy, Mode: model.TestWantDummy})
	if a == b {
		t.Fatal("keys must differ by status")
	}
}

func TestNotePacksSplitsReadyAndPending(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	done, err := st.EnsurePack(model.Lesson{ID: 1, Discipline: "Матан", Date: "2026-09-01"}, "u", root)
	if err != nil {
		t.Fatal(err)
	}
	done.Status = model.PackDone
	if err := st.SavePack(done); err != nil {
		t.Fatal(err)
	}
	failed, err := st.EnsurePack(model.Lesson{ID: 2, Discipline: "Физика", Date: "2026-09-02"}, "u", root)
	if err != nil {
		t.Fatal(err)
	}
	failed.Status = model.PackError
	if err := st.SavePack(failed); err != nil {
		t.Fatal(err)
	}

	b := &Bot{st: st}
	ready, pending, err := b.notePacks()
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 1 || ready[0].Status != model.PackDone {
		t.Fatalf("ready = %#v", ready)
	}
	if len(pending) != 1 || pending[0].Status != model.PackError {
		t.Fatalf("pending = %#v", pending)
	}
}

func TestSendPackPDFGuards(t *testing.T) {
	root := t.TempDir()
	b := &Bot{cfg: &config.Config{RecordingsDir: root}}
	if err := b.sendPackPDF(1, nil); err == nil {
		t.Fatal("nil pack must fail")
	}
	esc := &model.LecturePack{Dir: "..", NotesPDF: "../escape.pdf"}
	if err := b.sendPackPDF(1, esc); err == nil {
		t.Fatal("path escape must fail")
	}
	missing := &model.LecturePack{Dir: "2026-09-01_matan_1", NotesPDF: "2026-09-01_matan_1/notes.pdf"}
	if err := b.sendPackPDF(1, missing); err == nil {
		t.Fatal("missing pdf must fail")
	}
}

func TestNotePacksNilStore(t *testing.T) {
	b := &Bot{}
	ready, pending, err := b.notePacks()
	if err != nil || ready != nil || pending != nil {
		t.Fatalf("nil store: %v %v %v", ready, pending, err)
	}
}

func TestNowUsesLocation(t *testing.T) {
	loc := time.FixedZone("Samara", 4*3600)
	b := &Bot{loc: loc}
	if b.now().Location() != loc {
		t.Fatalf("location = %v", b.now().Location())
	}
}
