package tg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

func TestStartNewUserAsksFIO(t *testing.T) {
	h := newHarness(t)
	h.text(studentID, "/start")
	u := h.user(studentID)
	if u == nil || u.Onboarded || !u.Enabled || u.Subgroup != 1 {
		t.Fatalf("new user = %+v", u)
	}
	h.wantText(studentID, "Как тебя записать в журнал")
	if h.b.peekAwait(studentID) != awaitFIO {
		t.Fatal("should await FIO")
	}
}

func TestStrangerIsIgnored(t *testing.T) {
	h := newHarness(t)
	h.text(strangerID, "/start")
	h.text(strangerID, "привет")
	h.press(strangerID, "j:y:1")
	if u := h.user(strangerID); u != nil {
		t.Fatalf("stranger stored: %+v", u)
	}
	if n := len(h.api.Calls("sendMessage")); n != 0 {
		t.Fatalf("stranger got %d messages", n)
	}
	if got := h.toasts(); len(got) != 1 || got[0] != "" {
		t.Fatalf("stranger callback must get an empty answer, got %q", got)
	}
}

func TestOnboardingFullFlow(t *testing.T) {
	h := newHarness(t)
	h.text(studentID, "/start")
	h.text(studentID, "Иванов Иван Иванович")
	if u := h.user(studentID); u.FIO != "Иванов Иван Иванович" || u.OnboardStage != model.StageSub {
		t.Fatalf("after fio: %+v", u)
	}
	h.wantText(studentID, askSub)

	h.press(studentID, "ob:sub:2")
	if u := h.user(studentID); u.Subgroup != 2 || u.OnboardStage != model.StageWords {
		t.Fatalf("after sub: %+v", u)
	}
	h.wantText(studentID, askWords)

	h.text(studentID, "лаба, зачёт")
	u := h.user(studentID)
	if !u.Onboarded || u.OnboardStage != model.StageDone {
		t.Fatalf("not onboarded: %+v", u)
	}
	if strings.Join(u.ExtraWords, ",") != "лаба,зачёт" {
		t.Fatalf("words = %v", u.ExtraWords)
	}
	ev, _ := h.st.ListEvents(10)
	if len(ev) == 0 || ev[0].Type != model.EventOnboard {
		t.Fatalf("onboard event missing: %+v", ev)
	}
}

func TestOnboardingSubgroupByTextAndSkipWords(t *testing.T) {
	h := newHarness(t)
	h.text(studentID, "/start")
	h.text(studentID, "Петров Пётр Петрович")
	h.text(studentID, "абракадабра") // not a subgroup
	h.wantText(studentID, askSub)
	h.text(studentID, "1")
	h.press(studentID, "ob:skipw")
	u := h.user(studentID)
	if !u.Onboarded || u.Subgroup != 1 || len(u.ExtraWords) != 0 {
		t.Fatalf("user = %+v", u)
	}
	if got := h.toasts(); got[len(got)-1] != "Пропуск" {
		t.Fatalf("toasts = %q", got)
	}
}

func TestStartResumesOnboardingStage(t *testing.T) {
	h := newHarness(t)
	u := &model.User{TelegramID: studentID, Enabled: true, Subgroup: 1}
	if err := h.st.UpsertUser(u); err != nil {
		t.Fatal(err)
	}
	h.text(studentID, "/start")
	h.wantText(studentID, askFIO)

	if err := h.st.SetFIO(studentID, "А Б В"); err != nil {
		t.Fatal(err)
	}
	h.text(studentID, "/start")
	h.wantText(studentID, askSub)

	if err := h.st.SetOnboardStage(studentID, model.StageWords); err != nil {
		t.Fatal(err)
	}
	h.text(studentID, "/start")
	h.wantText(studentID, askWords)
}

func TestStartOnboardedShowsToday(t *testing.T) {
	h := newHarness(t)
	h.onboarded(studentID, "Иванов И.И.", 1)
	h.lesson("Теория информации", "Лекция", 30*time.Minute, true, 0)
	h.lesson("Чужая подгруппа", "Лабораторная", 40*time.Minute, true, 2)
	h.text(studentID, "/start")
	got := h.lastTo(studentID)
	if !strings.Contains(got, "Теория информации") || strings.Contains(got, "Чужая подгруппа") {
		t.Fatalf("today = %q", got)
	}
}

func TestCommandsHelpCancelToday(t *testing.T) {
	h := newHarness(t)
	h.text(studentID, "/today") // unknown user
	h.wantText(studentID, askFIO)

	h.onboarded(studentID, "Иванов И.И.", 1)
	h.b.setAwait(studentID, awaitWords)
	h.text(studentID, "/cancel")
	h.wantText(studentID, cancelText)
	if h.b.peekAwait(studentID) != awaitNone {
		t.Fatal("/cancel must clear await")
	}
	h.text(studentID, "/help")
	h.wantText(studentID, "Что умею")
	h.text(studentID, "/today")
	h.wantText(studentID, "Иванов")
}

func TestUnknownCommandAndText(t *testing.T) {
	h := newHarness(t)
	h.onboarded(studentID, "Иванов И.И.", 1)
	h.text(studentID, "/nope")
	h.wantText(studentID, fallbackText)
	h.text(studentID, "что-то непонятное")
	h.wantText(studentID, fallbackText)
}

func TestMenuButtons(t *testing.T) {
	h := newHarness(t)
	h.onboarded(studentID, "Иванов И.И.", 1)
	cases := map[string]string{
		btnToday:       "Иванов",
		btnTodayOld:    "Иванов",
		btnNotes:       "Готовых конспектов пока нет",
		btnSettings:    "Иванов",
		btnSettingsOld: "Иванов",
		btnWords:       "через запятую",
		btnHelp:        "Что умею",
		btnLinks:       "",
	}
	for btn, want := range cases {
		h.b.clearAwait(studentID)
		h.api.Reset()
		h.text(studentID, btn)
		got := h.lastTo(studentID)
		if !strings.Contains(got, want) {
			t.Errorf("button %q -> %q, want %q", btn, got, want)
		}
	}
}

func TestSettingsCallbacksAndAwaitFIO(t *testing.T) {
	h := newHarness(t)
	h.onboarded(studentID, "Старое Имя", 1)

	h.press(studentID, "st:fio")
	h.wantText(studentID, askFIO)
	h.text(studentID, "Новое Имя Отчество")
	if u := h.user(studentID); u.FIO != "Новое Имя Отчество" {
		t.Fatalf("fio = %q", u.FIO)
	}
	if h.b.peekAwait(studentID) != awaitNone {
		t.Fatal("await not cleared after fio")
	}

	h.press(studentID, "st:words")
	h.text(studentID, "лаба, коллоквиум")
	if u := h.user(studentID); strings.Join(u.ExtraWords, ",") != "лаба,коллоквиум" {
		t.Fatalf("words = %v", u.ExtraWords)
	}
	h.press(studentID, "st:words")
	h.text(studentID, "-")
	if u := h.user(studentID); len(u.ExtraWords) != 0 {
		t.Fatalf("words not cleared: %v", u.ExtraWords)
	}

	h.press(studentID, "st:sub:2")
	if u := h.user(studentID); u.Subgroup != 2 {
		t.Fatalf("subgroup = %d", u.Subgroup)
	}
	if c := h.api.Calls("editMessageText"); len(c) == 0 {
		t.Fatal("subgroup change should edit the card")
	}

	h.b.setAwait(studentID, awaitFIO)
	h.press(studentID, "st:cancel")
	if h.b.peekAwait(studentID) != awaitNone {
		t.Fatal("Отмена must clear the pending input")
	}
	h.press(studentID, "st:help")
	h.wantText(studentID, "Что умею")
	h.press(studentID, "st:rooms")
	h.press(studentID, "st:sub:7") // invalid
	h.press(studentID, "st:zzz")   // unknown
}

func TestAwaitFIOEmptyAndMenuLabelBypass(t *testing.T) {
	h := newHarness(t)
	h.onboarded(studentID, "Имя", 1)
	h.b.setAwait(studentID, awaitFIO)
	h.text(studentID, btnHelp) // menu label is not taken as FIO
	h.wantText(studentID, "Что умею")
	if u := h.user(studentID); u.FIO != "Имя" {
		t.Fatalf("menu label saved as fio: %q", u.FIO)
	}
}

func TestSettingsCallbackForNotOnboarded(t *testing.T) {
	h := newHarness(t)
	h.press(studentID, "st:fio") // no user at all
	if err := h.st.UpsertUser(&model.User{TelegramID: studentID, Enabled: true, Subgroup: 1}); err != nil {
		t.Fatal(err)
	}
	h.press(studentID, "st:fio")
	h.wantText(studentID, askFIO)
	h.text(studentID, "/settings")
	h.wantText(studentID, askFIO)
}

func TestWordsCommand(t *testing.T) {
	h := newHarness(t)
	h.text(studentID, "/words")
	h.wantText(studentID, askFIO)

	h.onboarded(studentID, "Имя", 1)
	h.text(studentID, "/words")
	h.wantText(studentID, "через запятую")
	h.text(studentID, "/words лаба")
	h.text(studentID, "/words зачёт")
	if u := h.user(studentID); strings.Join(u.ExtraWords, ",") != "лаба,зачёт" {
		t.Fatalf("words merge = %v", u.ExtraWords)
	}
	h.text(studentID, "/words очистить")
	if u := h.user(studentID); len(u.ExtraWords) != 0 {
		t.Fatalf("words clear = %v", u.ExtraWords)
	}
}

func TestWordsCommandFinishesOnboarding(t *testing.T) {
	h := newHarness(t)
	u := &model.User{TelegramID: studentID, FIO: "А Б В", Enabled: true, Subgroup: 1, OnboardStage: model.StageWords}
	if err := h.st.UpsertUser(u); err != nil {
		t.Fatal(err)
	}
	h.text(studentID, "/words лаба")
	if got := h.user(studentID); !got.Onboarded {
		t.Fatalf("not onboarded: %+v", got)
	}
}

func TestBBBLinkAttachesToUpcomingLesson(t *testing.T) {
	h := newHarness(t)
	h.onboarded(studentID, "Имя", 1)
	l := h.lesson("Компьютерные сети", "Лекция", 20*time.Minute, true, 0)
	h.text(studentID, "вот ссылка https://bbb.ssau.ru/b/abc-def-ghi пожалуйста")
	if got := h.st.GetLessonBBB(l.ID); got != "https://bbb.ssau.ru/b/abc-def-ghi" {
		t.Fatalf("link = %q", got)
	}
	h.wantText(studentID, "Компьютерные сети")
	h.text(studentID, "/link")
	h.wantText(studentID, "Компьютерные сети")
}

func TestBBBLinkInBreakGoesToNearestEvenWithInheritedRoom(t *testing.T) {
	h := newHarness(t)
	h.onboarded(studentID, "Имя", 1)
	near := h.lesson("Компьютерные сети", "Лекция", 20*time.Minute, true, 0)
	later := h.lesson("Теория информации", "Лекция", 3*time.Hour, true, 0)
	// Комната «Сетей» запомнена с прошлой недели — пара всё равно ближайшая цель.
	if err := h.st.SetBBB(model.BBBRoomKey(near.Discipline, near.Teacher, near.Type), "https://bbb.ssau.ru/b/old-room"); err != nil {
		t.Fatal(err)
	}
	h.text(studentID, "https://bbb.ssau.ru/b/new-room")
	if url, own, _ := h.st.LessonBBB(near.ID); url != "https://bbb.ssau.ru/b/new-room" || !own {
		t.Fatalf("near = %q own=%v", url, own)
	}
	if url, own, _ := h.st.LessonBBB(later.ID); url != "" || own {
		t.Fatalf("later must stay without link: %q own=%v", url, own)
	}
	h.wantText(studentID, "Компьютерные сети")
}

func TestBBBLinkWithoutLesson(t *testing.T) {
	h := newHarness(t)
	h.onboarded(studentID, "Имя", 1)
	h.text(studentID, "https://bbb.ssau.ru/b/abc-def-ghi")
	h.wantText(studentID, noBBBTarget)
}

func TestJoinCallbackYesAndNo(t *testing.T) {
	h := newHarness(t)
	h.onboarded(studentID, "Иванов И.И.", 1)
	l := h.lesson("Теория информации", "Лекция", 10*time.Minute, true, 0)

	h.press(studentID, joinCallbackData(true, l.ID))
	in, err := h.st.GetIntent(studentID, l.ID)
	if err != nil || in == nil || in.Decision != model.JoinYes {
		t.Fatalf("intent = %+v %v", in, err)
	}
	if got := h.toasts(); got[len(got)-1] != "Захожу" {
		t.Fatalf("toast = %q", got)
	}
	if len(h.api.Calls("editMessageText")) != 1 {
		t.Fatal("card should be edited")
	}

	h.press(studentID, joinCallbackData(false, l.ID))
	in, _ = h.st.GetIntent(studentID, l.ID)
	if in.Decision != model.JoinNo {
		t.Fatalf("decision = %s", in.Decision)
	}
	ev, _ := h.st.ListEvents(5)
	if ev[0].Type != model.EventSkip {
		t.Fatalf("skip event missing: %+v", ev[0])
	}

	h.press(studentID, "j:bad")
	if got := h.toasts(); got[len(got)-1] != "" {
		t.Fatalf("bad data toast = %q", got[len(got)-1])
	}
}

func TestLeaveCallback(t *testing.T) {
	h := newHarness(t)
	h.onboarded(studentID, "Иванов И.И.", 1)
	l := h.lesson("Теория информации", "Лекция", -10*time.Minute, true, 0)
	h.b.live[studentID] = liveSnap{}
	h.press(studentID, leaveCallbackData(l.ID))
	in, _ := h.st.GetIntent(studentID, l.ID)
	if in == nil || in.Decision != model.JoinNo {
		t.Fatalf("intent = %+v", in)
	}
	if _, ok := h.b.live[studentID]; ok {
		t.Fatal("live card should be forgotten")
	}
	h.wantText(studentID, "Выхожу из комнаты")
	h.press(studentID, "x:zzz")
}

func TestPanelCommand(t *testing.T) {
	h := newHarness(t)
	h.text(studentID, "/panel") // not admin
	if len(h.api.Calls("sendMessage")) != 0 {
		t.Fatal("non-admin must get nothing")
	}
	h.text(adminID, "/panel")
	h.wantText(adminID, "WEBAPP_PUBLIC_URL")

	h.b.cfg.WebAppURL = "https://panel.example"
	h.text(adminID, "/panel")
	msg := h.api.Calls("sendMessage")
	last := msg[len(msg)-1]
	if !strings.Contains(last.Param("reply_markup"), "https://panel.example/") {
		t.Fatalf("markup = %s", last.Param("reply_markup"))
	}
	if len(h.api.Calls("setChatMenuButton")) == 0 {
		t.Fatal("admin menu button should be set")
	}
}

func TestTestCommandFlow(t *testing.T) {
	h := newHarness(t)
	h.text(studentID, "/test")
	if len(h.api.Calls("sendMessage")) != 0 {
		t.Fatal("/test is admin-only")
	}
	h.text(adminID, "/test")
	h.press(adminID, "tx:listen") // no url yet
	h.wantText(adminID, testNeedURL)

	h.press(adminID, "tx:url")
	h.text(adminID, "https://bbb.ssau.ru/b/tst-aaa-bbb")
	j, _ := h.st.GetTestJoin()
	if j.URL != "https://bbb.ssau.ru/b/tst-aaa-bbb" {
		t.Fatalf("test url = %q", j.URL)
	}
	h.press(adminID, "tx:name")
	h.text(adminID, "Проверка")
	if j, _ = h.st.GetTestJoin(); j.Name != "Проверка" {
		t.Fatalf("name = %q", j.Name)
	}
	h.press(adminID, "tx:listen")
	if j, _ = h.st.GetTestJoin(); j.Want != model.TestWantListen || j.Status != model.TestJoining {
		t.Fatalf("listen = %+v", j)
	}
	h.press(adminID, "tx:dummy")
	if j, _ = h.st.GetTestJoin(); j.Want != model.TestWantDummy {
		t.Fatalf("dummy = %+v", j)
	}
	h.press(adminID, "tx:leave")
	if j, _ = h.st.GetTestJoin(); j.Want != model.TestWantOff || j.Status != model.TestIdle {
		t.Fatalf("leave = %+v", j)
	}
	h.press(adminID, "tx:???")
	h.press(studentID, "tx:leave") // non-admin ignored

	h.text(adminID, "/test https://bbb.ssau.ru/b/new-url-xyz")
	if j, _ = h.st.GetTestJoin(); j.URL != "https://bbb.ssau.ru/b/new-url-xyz" {
		t.Fatalf("url via command = %q", j.URL)
	}
	h.text(adminID, "/test имя Робот")
	if j, _ = h.st.GetTestJoin(); j.Name != "Робот" {
		t.Fatalf("name via command = %q", j.Name)
	}
	h.text(adminID, "/test -")
	if j, _ = h.st.GetTestJoin(); j.Name != model.TestGuestName {
		t.Fatalf("reset name = %q", j.Name)
	}
}

func TestAwaitTestURLRejectsText(t *testing.T) {
	h := newHarness(t)
	h.onboarded(adminID, "Админ", 1)
	h.b.setAwait(adminID, awaitTestURL)
	h.text(adminID, "не ссылка")
	h.wantText(adminID, askTestURL)
}

func TestTestNamePayloadAndNormalize(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("я", 80)
	for in, want := range map[string]string{"": model.TestGuestName, " - ": model.TestGuestName, "—": model.TestGuestName, " Вася ": "Вася", long: strings.Repeat("я", 64)} {
		if got := normalizeTestName(in); got != want {
			t.Errorf("normalizeTestName(%q) = %q", in, got)
		}
	}
	for in, want := range map[string]string{"имя Петя": "Петя", "name Bob": "Bob", "как X": "X", "Вася": "Вася"} {
		if got, ok := parseTestNamePayload(in); !ok || got != want {
			t.Errorf("parseTestNamePayload(%q) = %q %v", in, got, ok)
		}
	}
	if _, ok := parseTestNamePayload("https://bbb.ssau.ru/b/aaa-bbb-ccc"); ok {
		t.Error("url is not a name")
	}
}

func TestNotesListAndPDF(t *testing.T) {
	h := newHarness(t)
	h.onboarded(studentID, "Имя", 1)
	l := h.lesson("Теория информации", "Лекция", -3*time.Hour, true, 0)
	p, err := h.st.EnsurePack(l, "https://bbb.ssau.ru/b/x", h.b.cfg.RecordingsDir)
	if err != nil {
		t.Fatal(err)
	}
	h.text(studentID, "/notes")
	h.wantText(studentID, "Теория информации") // pending

	pdf := filepath.Join(h.b.cfg.RecordingsDir, p.Dir, "notes.pdf")
	if err := os.MkdirAll(filepath.Dir(pdf), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pdf, []byte("%PDF-1.4 "+strings.Repeat("x", 200)), 0o600); err != nil {
		t.Fatal(err)
	}
	p.Status = model.PackDone
	if err := h.st.SavePack(p); err != nil {
		t.Fatal(err)
	}
	h.text(studentID, btnNotes)
	msgs := h.api.Calls("sendMessage")
	if !strings.Contains(msgs[len(msgs)-1].Param("reply_markup"), "nt:") {
		t.Fatalf("notes keyboard missing: %s", msgs[len(msgs)-1].Param("reply_markup"))
	}
	h.press(studentID, "nt:"+itoa(p.ID))
	docs := h.api.Calls("sendDocument")
	if len(docs) != 1 || !strings.HasSuffix(docs[0].File, ".pdf") {
		t.Fatalf("pdf not sent: %+v", docs)
	}
	h.press(studentID, "nt:999999")
	if got := h.toasts(); got[len(got)-1] != "PDF ещё не готов" {
		t.Fatalf("toast = %q", got)
	}
	h.press(studentID, "nt:abc")
}

func TestNotesPDFMissingOnDisk(t *testing.T) {
	h := newHarness(t)
	h.onboarded(studentID, "Имя", 1)
	l := h.lesson("Сети", "Лекция", -3*time.Hour, true, 0)
	p, err := h.st.EnsurePack(l, "u", h.b.cfg.RecordingsDir)
	if err != nil {
		t.Fatal(err)
	}
	p.Status = model.PackDone
	if err := h.st.SavePack(p); err != nil {
		t.Fatal(err)
	}
	h.press(studentID, "nt:"+itoa(p.ID))
	h.wantText(studentID, "Не смог отправить PDF")
}

func TestWordsCommandBeforeOnboardingShowsHint(t *testing.T) {
	h := newHarness(t)
	if err := h.st.UpsertUser(&model.User{TelegramID: studentID, FIO: "А Б В", Enabled: true, Subgroup: 1, OnboardStage: model.StageSub}); err != nil {
		t.Fatal(err)
	}
	h.text(studentID, "/words")
	h.wantText(studentID, askWordsNext)
}

func TestTodayShowsOfflineAndNextDay(t *testing.T) {
	h := newHarness(t)
	if time.Now().In(h.b.loc).Hour() < 4 {
		t.Skip("пара «3 часа назад» попала бы во вчера")
	}
	h.onboarded(studentID, "Имя", 1)
	h.lesson("Военная кафедра", "Практика", -3*time.Hour, false, 0) // прошла сегодня
	h.lesson("Компьютерные сети", "Лекция", 24*time.Hour, true, 0)  // завтра
	h.lesson("Чужая лаба", "Лабораторная", 25*time.Hour, true, 2)   // не моя подгруппа
	h.text(studentID, "/today")
	got := h.lastTo(studentID)
	if !strings.Contains(got, "Военная кафедра") || !strings.Contains(got, "Прошли") {
		t.Fatalf("today: %s", got)
	}
	if !strings.Contains(got, "Компьютерные сети") {
		t.Fatalf("next day block missing: %s", got)
	}
	if strings.Contains(got, "Чужая лаба") {
		t.Fatalf("other subgroup leaked: %s", got)
	}
	h.text(studentID, "/week")
	if got := h.lastTo(studentID); !strings.Contains(got, "Компьютерные сети") {
		t.Fatalf("week: %s", got)
	}
}
