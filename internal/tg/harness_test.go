package tg

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/tgtest"
)

const (
	adminID    int64 = 1
	studentID  int64 = 2
	strangerID int64 = 99
)

// harness is a Bot wired to a fake Telegram API and a temp sqlite store.
type harness struct {
	t   *testing.T
	b   *Bot
	api *tgtest.Server
	st  *store.Store
	upd int64
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	api := tgtest.New(t)
	old := botOpts
	botOpts = api.Opts()
	t.Cleanup(func() { botOpts = old })

	st, err := store.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	loc, err := time.LoadLocation("Europe/Samara")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		BotToken:      tgtest.Token,
		AdminID:       adminID,
		Whitelist:     []int64{studentID},
		RecordingsDir: t.TempDir(),
	}
	b, err := New(cfg, st, loc)
	if err != nil {
		t.Fatal(err)
	}
	api.Reset() // drop getMe
	return &harness{t: t, b: b, api: api, st: st}
}

// do runs an update through the real dispatcher, as polling would.
func (h *harness) do(u *gotgbot.Update) {
	h.t.Helper()
	if err := h.b.disp.ProcessUpdate(h.b.api, u, nil); err != nil {
		h.t.Fatalf("ProcessUpdate: %v", err)
	}
}

func (h *harness) text(from int64, text string) {
	h.t.Helper()
	h.upd++
	h.do(tgtest.Message(h.upd, from, text))
}

func (h *harness) press(from int64, data string) {
	h.t.Helper()
	h.upd++
	h.do(tgtest.Callback(h.upd, from, data))
}

// onboarded stores a finished user.
func (h *harness) onboarded(id int64, fio string, sub int) *model.User {
	h.t.Helper()
	u := &model.User{
		TelegramID: id, FIO: fio, Subgroup: sub, Enabled: true,
		Onboarded: true, OnboardStage: model.StageDone, CreatedAt: time.Now(),
	}
	if err := h.st.UpsertUser(u); err != nil {
		h.t.Fatal(err)
	}
	return u
}

// lesson stores one lesson starting `in` from now and returns it with its id.
func (h *harness) lesson(discipline, typ string, in time.Duration, online bool, sub int) model.Lesson {
	h.t.Helper()
	begin := time.Now().In(h.b.loc).Add(in).Truncate(time.Minute)
	l := model.Lesson{
		Date:       begin.Format("2006-01-02"),
		Start:      begin.Format("15:04"),
		End:        begin.Add(90 * time.Minute).Format("15:04"),
		Begin:      begin,
		Finish:     begin.Add(90 * time.Minute),
		Discipline: discipline,
		Teacher:    "Петров П.П.",
		Type:       typ,
		Subgroup:   sub,
		Online:     online,
	}
	all, err := h.st.ListLessons()
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.st.ReplaceLessons(append(all, l)); err != nil {
		h.t.Fatal(err)
	}
	all, err = h.st.ListLessons()
	if err != nil {
		h.t.Fatal(err)
	}
	for _, got := range all {
		if got.Discipline == discipline && got.Begin.Equal(begin) {
			return got
		}
	}
	h.t.Fatalf("lesson %q not stored", discipline)
	return model.Lesson{}
}

func (h *harness) user(id int64) *model.User {
	h.t.Helper()
	u, err := h.st.GetUser(id)
	if err != nil {
		h.t.Fatal(err)
	}
	return u
}

// lastTo returns the last text sent to chat, failing when there is none.
func (h *harness) lastTo(chat int64) string {
	h.t.Helper()
	var last string
	for _, c := range h.api.Calls("sendMessage", "editMessageText") {
		if c.Param("chat_id") == itoa(chat) {
			last = c.Param("text")
		}
	}
	if last == "" {
		h.t.Fatalf("nothing sent to %d; calls=%+v", chat, h.api.Calls())
	}
	return last
}

func (h *harness) wantText(chat int64, sub string) {
	h.t.Helper()
	if got := h.lastTo(chat); !strings.Contains(got, sub) {
		h.t.Fatalf("last message to %d = %q, want it to contain %q", chat, got, sub)
	}
}

func (h *harness) toasts() []string {
	var out []string
	for _, c := range h.api.Calls("answerCallbackQuery") {
		out = append(out, c.Param("text"))
	}
	return out
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
