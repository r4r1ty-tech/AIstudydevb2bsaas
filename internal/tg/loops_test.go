package tg

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/tgtest"
)

func TestTickT15AsksOnceForOnlineLessons(t *testing.T) {
	h := newHarness(t)
	h.onboarded(studentID, "Иванов И.И.", 1)
	soon := h.lesson("Теория информации", "Лекция", 10*time.Minute, true, 0)
	h.lesson("Офлайн пара", "Лекция", 12*time.Minute, false, 0)
	h.lesson("Чужая подгруппа", "Лабораторная", 11*time.Minute, true, 2)
	h.lesson("Далёкая пара", "Лекция", 3*time.Hour, true, 0)

	h.b.tickT15()
	msgs := h.api.Calls("sendMessage")
	if len(msgs) != 1 {
		t.Fatalf("want one T-15 card, got %d: %v", len(msgs), h.api.Texts())
	}
	card := msgs[0]
	if !strings.Contains(card.Param("text"), "Теория информации") || !strings.Contains(card.Param("text"), "Ссылка нужна") {
		t.Fatalf("card = %q", card.Param("text"))
	}
	if !strings.Contains(card.Param("reply_markup"), joinCallbackData(true, soon.ID)) {
		t.Fatalf("keyboard = %s", card.Param("reply_markup"))
	}
	in, _ := h.st.GetIntent(studentID, soon.ID)
	if in == nil || in.Decision != model.JoinPending {
		t.Fatalf("intent = %+v", in)
	}
	if h.b.lastT15Lesson(studentID) != soon.ID {
		t.Fatal("T-15 lesson not remembered")
	}

	h.b.tickT15()
	if n := len(h.api.Calls("sendMessage")); n != 1 {
		t.Fatalf("second tick re-sent the card (%d)", n)
	}
}

func TestTickT15WithLinkAndSendFailure(t *testing.T) {
	h := newHarness(t)
	h.onboarded(studentID, "Иванов И.И.", 1)
	l := h.lesson("Сети", "Лекция", 5*time.Minute, true, 0)
	if err := h.st.SetLessonBBB(l.ID, "https://bbb.ssau.ru/b/aaa-bbb-ccc"); err != nil {
		t.Fatal(err)
	}
	h.api.Fail("sendMessage", "Forbidden: bot was blocked by the user")
	h.b.tickT15()
	if in, _ := h.st.GetIntent(studentID, l.ID); in != nil {
		t.Fatal("failed card must not store an intent — next tick retries")
	}
	h.api.Fail("sendMessage", "")
	h.b.tickT15()
	h.wantText(studentID, "Ссылка этой пары уже есть")
}

func TestTickLiveSendsEditsAndClearsCard(t *testing.T) {
	h := newHarness(t)
	h.onboarded(studentID, "Иванов И.И.", 1)
	l := h.lesson("Теория информации", "Лекция", -5*time.Minute, true, 0)
	set := func(state string) {
		if err := h.st.SetPresence(model.Presence{TelegramID: studentID, LessonID: l.ID, State: state, UpdatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}

	set(model.PresenceLobby)
	h.b.tickLive()
	h.wantText(studentID, "лобби")
	if n := len(h.api.Calls("sendMessage")); n != 1 {
		t.Fatalf("sent %d", n)
	}
	h.b.tickLive() // nothing changed
	if n := len(h.api.Calls("sendMessage", "editMessageText")); n != 1 {
		t.Fatalf("unchanged card re-sent (%d calls)", n)
	}

	set(model.PresenceRoom)
	h.b.tickLive()
	if n := len(h.api.Calls("editMessageText")); n != 1 {
		t.Fatalf("state change should edit, got %d edits", n)
	}
	h.wantText(studentID, "в комнате")

	h.api.Fail("editMessageText", "message to edit not found")
	if err := h.st.SetLessonBBB(l.ID, "https://bbb.ssau.ru/b/x"); err != nil {
		t.Fatal(err)
	}
	h.b.tickLive() // edit fails → a fresh card is sent
	if n := len(h.api.Calls("sendMessage")); n != 2 {
		t.Fatalf("failed edit should fall back to send, sends=%d", n)
	}
	h.api.Fail("editMessageText", "")

	if err := h.st.ClearPresence(studentID); err != nil {
		t.Fatal(err)
	}
	h.b.tickLive()
	h.wantText(studentID, "Вышел из комнаты")
	if len(h.b.live) != 0 {
		t.Fatal("card state not cleared")
	}
}

func TestSyncLiveCardMissingLessonOrUser(t *testing.T) {
	h := newHarness(t)
	h.b.syncLiveCard(model.Presence{TelegramID: studentID, LessonID: 12345, State: model.PresenceRoom}, time.Now())
	l := h.lesson("Сети", "Лекция", 0, true, 0)
	h.b.syncLiveCard(model.Presence{TelegramID: studentID, LessonID: l.ID, State: model.PresenceRoom}, time.Now())
	if n := len(h.api.Calls()); n != 0 {
		t.Fatalf("no card without lesson/user, got %d calls", n)
	}
	h.b.clearLiveCard(studentID, "x") // nothing remembered: no-op
}

func TestSyncTestLive(t *testing.T) {
	h := newHarness(t)
	h.b.syncTestLive() // idle: nothing
	if len(h.api.Calls()) != 0 {
		t.Fatal("idle test must not send")
	}
	j, _ := h.st.GetTestJoin()
	j.URL, j.Want, j.Status = "https://bbb.ssau.ru/b/t", model.TestWantDummy, model.TestJoining
	if err := h.st.PutTestJoin(j); err != nil {
		t.Fatal(err)
	}
	h.b.syncTestLive()
	if n := len(h.api.Calls("sendMessage")); n != 1 {
		t.Fatalf("sends = %d", n)
	}
	h.b.syncTestLive()
	if n := len(h.api.Calls("sendMessage", "editMessageText")); n != 1 {
		t.Fatal("unchanged test card re-sent")
	}
	j.Status = model.TestRoom
	_ = h.st.PutTestJoin(j)
	h.b.syncTestLive()
	if n := len(h.api.Calls("editMessageText")); n != 1 {
		t.Fatalf("status change should edit (%d)", n)
	}
	j.Want, j.Status = model.TestWantOff, model.TestIdle
	_ = h.st.PutTestJoin(j)
	h.b.syncTestLive()
	h.wantText(adminID, "Тест: вышел из комнаты")
	if h.b.testLive != nil {
		t.Fatal("test card state not cleared")
	}

	h.b.cfg.AdminID = 0
	h.b.syncTestLive()
	var nilBot *Bot
	nilBot.syncTestLive()
}

func TestPublishProfileAndMenuButton(t *testing.T) {
	h := newHarness(t)
	h.b.publishProfile()
	if n := len(h.api.Calls("setMyCommands")); n != 2 {
		t.Fatalf("setMyCommands calls = %d (users + admin scope)", n)
	}
	if len(h.api.Calls("setMyDescription")) != 1 || len(h.api.Calls("setMyShortDescription")) != 1 {
		t.Fatal("descriptions not published")
	}
	h.api.Reset()

	h.b.cfg.WebAppURL = "https://one.example"
	h.b.syncWebAppURL()
	h.b.syncWebAppURL() // unchanged: no second call
	calls := h.api.Calls("setChatMenuButton")
	if len(calls) != 1 || !strings.Contains(calls[0].Param("menu_button"), "https://one.example/") {
		t.Fatalf("menu button calls = %+v", calls)
	}

	h.api.Fail("setMyCommands", "boom")
	h.api.Fail("setChatMenuButton", "boom")
	h.b.publishProfile() // failures are logged, not fatal
}

func TestNotifyAdminAndSendErrors(t *testing.T) {
	h := newHarness(t)
	if err := h.b.NotifyAdmin(context.Background(), "привет"); err != nil {
		t.Fatal(err)
	}
	h.wantText(adminID, "привет")
	h.api.Fail("sendMessage", "chat not found")
	if err := h.b.NotifyAdmin(nil, "x"); err == nil { //nolint:staticcheck // nil ctx is handled
		t.Fatal("expected error")
	}
	if err := h.b.sendMain(adminID, "x"); err == nil {
		t.Fatal("sendMain must surface send errors")
	}
	if err := h.b.sendInline(adminID, "x", t15Keyboard(1)); err == nil {
		t.Fatal("sendInline must surface send errors")
	}
}

func TestStartPollsUntilCancelled(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.b.Start(ctx) }()
	deadline := time.Now().Add(3 * time.Second)
	for len(h.api.Calls("getUpdates")) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if len(h.api.Calls("getUpdates")) == 0 {
		t.Fatal("polling did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Start did not stop on cancel")
	}
	if len(h.api.Calls("setMyCommands")) == 0 {
		t.Fatal("profile not published on start")
	}

	cctx, ccancel := context.WithCancel(context.Background())
	ccancel()
	if err := h.b.Start(cctx); err == nil {
		t.Fatal("cancelled ctx must fail fast")
	}
}

func TestNewValidation(t *testing.T) {
	h := newHarness(t)
	if _, err := New(nil, h.st, nil); err == nil {
		t.Fatal("nil cfg")
	}
	if _, err := New(&config.Config{BotToken: tgtest.Token}, nil, nil); err == nil {
		t.Fatal("nil store")
	}
	b, err := New(&config.Config{BotToken: tgtest.Token}, h.st, nil)
	if err != nil || b.loc == nil {
		t.Fatalf("nil loc should default: %v", err)
	}
	h.api.Fail("getMe", "Unauthorized")
	if _, err := New(&config.Config{BotToken: tgtest.Token}, h.st, nil); err == nil {
		t.Fatal("bad token must fail")
	}
}

func TestWebAppLoopStops(t *testing.T) {
	h := newHarness(t)
	f := filepath.Join(t.TempDir(), "url")
	if err := os.WriteFile(f, []byte("https://loop.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.b.cfg.WebAppURLFile = f
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.b.webappLoop(ctx); close(done) }()
	go func() { h.b.t15Loop(ctx) }()
	go func() { h.b.liveLoop(ctx) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("webappLoop ignored cancel")
	}
	if len(h.api.Calls("setChatMenuButton")) == 0 {
		t.Fatal("webappLoop should push the url on start")
	}
}
