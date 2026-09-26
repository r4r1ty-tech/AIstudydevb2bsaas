package notify

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/tgtest"
)

func fakeAPI(t *testing.T) (*tgtest.Server, *config.Config) {
	t.Helper()
	api := tgtest.New(t)
	oldOpts := botOpts
	botMu.Lock()
	oldInst, oldTok := botInst, botTok
	botInst, botTok = nil, ""
	botMu.Unlock()
	botOpts = api.Opts()
	t.Cleanup(func() {
		botOpts = oldOpts
		botMu.Lock()
		botInst, botTok = oldInst, oldTok
		botMu.Unlock()
	})
	return api, &config.Config{BotToken: tgtest.Token, AdminID: 1}
}

func TestAdminAndUserSend(t *testing.T) {
	api, cfg := fakeAPI(t)
	Admin(context.Background(), cfg, "запись молчит")
	User(nil, cfg, 42, "привет") //nolint:staticcheck // nil ctx is handled
	UserMarkup(context.Background(), cfg, 42, "конспект", NotesButton(5))
	calls := api.Calls("sendMessage")
	if len(calls) != 3 {
		t.Fatalf("sendMessage calls = %d", len(calls))
	}
	if calls[0].Param("chat_id") != "1" || calls[0].Param("text") != "запись молчит" {
		t.Fatalf("admin call = %+v", calls[0])
	}
	if calls[1].Param("chat_id") != "42" {
		t.Fatalf("user call = %+v", calls[1])
	}
	if !strings.Contains(calls[2].Param("reply_markup"), "nt:5") {
		t.Fatalf("markup = %s", calls[2].Param("reply_markup"))
	}
	if n := len(api.Calls("getMe")); n != 1 {
		t.Fatalf("bot must be created once and reused, getMe=%d", n)
	}
}

func TestUserMarkupSendFailureIsLogged(t *testing.T) {
	api, cfg := fakeAPI(t)
	api.Fail("sendMessage", "Forbidden: bot was blocked by the user")
	User(context.Background(), cfg, 42, "x") // must not panic
	if len(api.Calls("sendMessage")) != 1 {
		t.Fatal("send was not attempted")
	}
}

func TestBotForTokenError(t *testing.T) {
	api, cfg := fakeAPI(t)
	api.Fail("getMe", "Unauthorized")
	if _, err := botFor(cfg.BotToken); err == nil {
		t.Fatal("bad token must fail")
	}
	User(context.Background(), cfg, 42, "x")
	if len(api.Calls("sendMessage")) != 0 {
		t.Fatal("no send without a bot")
	}
	if sent, failed := Broadcast(context.Background(), cfg, []int64{1, 2}, "x"); sent != 0 || failed != 2 {
		t.Fatalf("broadcast without bot = %d/%d", sent, failed)
	}
	if err := Document(context.Background(), cfg, 1, writeFile(t, "a.pdf"), "", ""); err == nil {
		t.Fatal("document without bot must fail")
	}
}

func TestBroadcast(t *testing.T) {
	api, cfg := fakeAPI(t)
	if s, f := Broadcast(context.Background(), nil, []int64{1}, "x"); s != 0 || f != 0 {
		t.Fatal("nil cfg")
	}
	if s, f := Broadcast(context.Background(), cfg, []int64{1}, "  "); s != 0 || f != 0 {
		t.Fatal("empty text")
	}
	sent, failed := Broadcast(nil, cfg, []int64{1, 0, 2, 3}, "всем") //nolint:staticcheck
	if sent != 3 || failed != 0 {
		t.Fatalf("sent=%d failed=%d", sent, failed)
	}
	api.Fail("sendMessage", "chat not found")
	if sent, failed = Broadcast(context.Background(), cfg, []int64{1, 2}, "всем"); sent != 0 || failed != 2 {
		t.Fatalf("failing sends: sent=%d failed=%d", sent, failed)
	}
}

func writeFile(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("%PDF-1.4 test"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDocument(t *testing.T) {
	api, cfg := fakeAPI(t)
	p := writeFile(t, "notes.pdf")
	if err := Document(nil, cfg, 7, p, " подпись ", ""); err != nil { //nolint:staticcheck
		t.Fatal(err)
	}
	docs := api.Calls("sendDocument")
	if len(docs) != 1 || docs[0].File != "notes.pdf" || docs[0].Param("caption") != "подпись" || docs[0].Param("chat_id") != "7" {
		t.Fatalf("doc call = %+v", docs)
	}
	if err := Document(context.Background(), cfg, 7, p, "", "Лекция 1.pdf"); err != nil {
		t.Fatal(err)
	}
	if got := api.Calls("sendDocument")[1].File; got != "Лекция 1.pdf" {
		t.Fatalf("filename = %q", got)
	}
	if err := Document(context.Background(), cfg, 7, filepath.Join(t.TempDir(), "missing.pdf"), "", ""); err == nil {
		t.Fatal("missing file must fail")
	}
	api.Fail("sendDocument", "file too big")
	if err := Document(context.Background(), cfg, 7, p, "", ""); err == nil {
		t.Fatal("api error must surface")
	}
}
