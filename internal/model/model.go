package model

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

type JoinDecision string

const (
	JoinPending JoinDecision = "pending"
	JoinYes     JoinDecision = "yes"
	JoinNo      JoinDecision = "no"
)

type User struct {
	TelegramID    int64      `json:"telegram_id"`
	Username      string     `json:"username"`
	FirstName     string     `json:"first_name"`
	LastName      string     `json:"last_name"`
	FIO           string     `json:"fio"`
	Subgroup      int        `json:"subgroup"`
	Enabled       bool       `json:"enabled"`
	DisabledUntil *time.Time `json:"disabled_until,omitempty"`
	ExtraWords    []string   `json:"extra_words"`
	OnboardStage  int        `json:"onboard_stage"`
	Onboarded     bool       `json:"onboarded"`
	CreatedAt     time.Time  `json:"created_at"`
}

func (u User) Active(now time.Time) bool {
	logx.Debugf("model", "User.Active: id=%d enabled=%v onboarded=%v fio=%q", u.TelegramID, u.Enabled, u.Onboarded, u.FIO)
	if !u.Enabled {
		logx.Debugf("model", "User.Active: id=%d -> false disabled", u.TelegramID)
		return false
	}
	if u.DisabledUntil != nil && now.Before(*u.DisabledUntil) {
		logx.Debugf("model", "User.Active: id=%d -> false disabled_until=%s", u.TelegramID, u.DisabledUntil.Format(time.RFC3339))
		return false
	}
	res := u.Onboarded && strings.TrimSpace(u.FIO) != ""
	logx.Debugf("model", "User.Active: id=%d -> %v", u.TelegramID, res)
	return res
}

func (u User) Surname() string {
	logx.Debugf("model", "User.Surname: id=%d fio=%q", u.TelegramID, u.FIO)
	f := strings.TrimSpace(u.FIO)
	if f == "" {
		logx.Debugf("model", "User.Surname: id=%d -> empty", u.TelegramID)
		return ""
	}
	out := strings.Fields(f)[0]
	logx.Debugf("model", "User.Surname: id=%d -> %q", u.TelegramID, out)
	return out
}

const (
	StageFIO   = 0
	StageSub   = 1
	StageWords = 2
	StageDone  = 3
)

var CommonWakeWords = []string{"тест", "контрольная", "мудл", "moodle"}

func ParseWakeWords(s string) []string {
	logx.Debugf("model", "ParseWakeWords: raw=%q", s)
	s = strings.TrimSpace(s)
	if s == "" {
		logx.Debugf("model", "ParseWakeWords: empty -> nil")
		return nil
	}
	for _, sep := range []string{",", ";", "\n"} {
		s = strings.ReplaceAll(s, sep, " ")
	}
	seen := map[string]struct{}{}
	out := make([]string, 0)
	for _, p := range strings.Fields(s) {
		p = strings.Trim(p, ".,!?«»\"'")
		p = strings.ToLower(p)
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	logx.Debugf("model", "ParseWakeWords: raw=%q -> %v", s, out)
	return out
}

func SkipWakeWords(s string) bool {
	logx.Debugf("model", "SkipWakeWords: raw=%q", s)
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "", "-", "—", ".", "нет", "не надо", "пропуск", "skip", "/skip", "clear", "очистить":
		logx.Debugf("model", "SkipWakeWords: %q -> true", s)
		return true
	default:
		logx.Debugf("model", "SkipWakeWords: %q -> false", s)
		return false
	}
}

func FormatWakeWords(words []string) string {
	logx.Debugf("model", "FormatWakeWords: n=%d words=%v", len(words), words)
	out := strings.Join(ParseWakeWords(strings.Join(words, " ")), ", ")
	logx.Debugf("model", "FormatWakeWords: -> %q", out)
	return out
}

func MergeWakeWords(old, add []string) []string {
	logx.Debugf("model", "MergeWakeWords: old=%v add=%v", old, add)
	out := ParseWakeWords(strings.Join(append(append([]string{}, old...), add...), " "))
	logx.Debugf("model", "MergeWakeWords: -> %v", out)
	return out
}

func (u User) WakeList() []string {
	logx.Debugf("model", "User.WakeList: id=%d extra=%v", u.TelegramID, u.ExtraWords)
	add := append([]string{}, CommonWakeWords...)
	if s := u.Surname(); s != "" {
		add = append(add, s)
	}
	add = append(add, u.ExtraWords...)
	out := ParseWakeWords(strings.Join(add, " "))
	logx.Debugf("model", "User.WakeList: id=%d -> %v", u.TelegramID, out)
	return out
}

type Lesson struct {
	ID         int64     `json:"id"`
	Date       string    `json:"date"`  // 2006-01-02, Samara calendar day
	Start      string    `json:"start"` // 15:04
	End        string    `json:"end"`   // 15:04
	Begin      time.Time `json:"begin"`
	Finish     time.Time `json:"finish"`
	Discipline string    `json:"discipline"`
	Teacher    string    `json:"teacher"`
	Place      string    `json:"place"`
	Subgroup   int       `json:"subgroup"` // 0 = вся группа
	Type       string    `json:"type"`
	Online     bool      `json:"online"`
}

func (l Lesson) MatchesSubgroup(n int) bool {
	logx.Debugf("model", "Lesson.MatchesSubgroup: lesson=%d subgroup=%d n=%d", l.ID, l.Subgroup, n)
	res := l.Subgroup == 0 || l.Subgroup == n
	logx.Debugf("model", "Lesson.MatchesSubgroup: lesson=%d -> %v", l.ID, res)
	return res
}

func (l Lesson) SlotLabel() string {
	logx.Debugf("model", "Lesson.SlotLabel: lesson=%d start=%s end=%s", l.ID, l.Start, l.End)
	out := l.Start + "–" + l.End
	logx.Debugf("model", "Lesson.SlotLabel: lesson=%d -> %q", l.ID, out)
	return out
}

func (l Lesson) Identity() string {
	logx.Debugf("model", "Lesson.Identity: lesson=%d date=%s start=%s disc=%q teacher=%q place=%q subgroup=%d online=%v",
		l.ID, l.Date, l.Start, l.Discipline, l.Teacher, l.Place, l.Subgroup, l.Online)
	on := "0"
	if l.Online {
		on = "1"
	}
	out := strings.Join([]string{
		l.Date,
		l.Start,
		l.Discipline,
		l.Teacher,
		l.Place,
		fmt.Sprintf("%d", l.Subgroup),
		on,
	}, "|")
	logx.Debugf("model", "Lesson.Identity: lesson=%d -> %q", l.ID, out)
	return out
}

type BBBLink struct {
	Key       string    `json:"key"`
	URL       string    `json:"url"`
	UpdatedAt time.Time `json:"updated_at"`
}

func BBBKey(groupID int64, discipline, teacher string) string {
	logx.Debugf("model", "BBBKey: group=%d discipline=%q teacher=%q", groupID, discipline, teacher)
	out := fmt.Sprintf("%d|%s|%s", groupID, strings.TrimSpace(discipline), strings.TrimSpace(teacher))
	logx.Debugf("model", "BBBKey: -> %q", out)
	return out
}

// BBBRoomKey — комната предмета у препода, переживает смену id пар между неделями.
func BBBRoomKey(discipline, teacher string) string {
	return "room|" + strings.TrimSpace(discipline) + "|" + strings.TrimSpace(teacher)
}

func BBBLessonKey(lessonID int64) string {
	logx.Debugf("model", "BBBLessonKey: lesson=%d", lessonID)
	if lessonID <= 0 {
		logx.Debugf("model", "BBBLessonKey: lesson=%d -> empty", lessonID)
		return ""
	}
	out := fmt.Sprintf("lesson:%d", lessonID)
	logx.Debugf("model", "BBBLessonKey: lesson=%d -> %q", lessonID, out)
	return out
}

func ParseBBBLessonID(key string) (int64, bool) {
	logx.Debugf("model", "ParseBBBLessonID: key=%q", key)
	key = strings.TrimSpace(key)
	if !strings.HasPrefix(key, "lesson:") {
		logx.Debugf("model", "ParseBBBLessonID: key=%q -> 0,false", key)
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(key, "lesson:"), 10, 64)
	if err != nil || n <= 0 {
		if err != nil {
			logx.Debugf("model", "ParseBBBLessonID: parse %q: %v", key, err)
		}
		logx.Debugf("model", "ParseBBBLessonID: key=%q -> 0,false", key)
		return 0, false
	}
	logx.Debugf("model", "ParseBBBLessonID: key=%q -> %d,true", key, n)
	return n, true
}

func ParseBBBKey(key string) (discipline, teacher string) {
	logx.Debugf("model", "ParseBBBKey: key=%q", key)
	parts := strings.Split(key, "|")
	switch len(parts) {
	case 0, 1:
		d, t := strings.TrimSpace(key), ""
		logx.Debugf("model", "ParseBBBKey: key=%q -> %q,%q", key, d, t)
		return d, t
	case 2:
		d, t := strings.TrimSpace(parts[1]), ""
		logx.Debugf("model", "ParseBBBKey: key=%q -> %q,%q", key, d, t)
		return d, t
	default:
		d := strings.TrimSpace(parts[1])
		t := strings.TrimSpace(strings.Join(parts[2:], "|"))
		logx.Debugf("model", "ParseBBBKey: key=%q -> %q,%q", key, d, t)
		return d, t
	}
}

const TestGuestName = "тест"

const (
	TestWantOff    = ""
	TestWantDummy  = "dummy"
	TestWantListen = "listen"
)

const (
	TestIdle    = "idle"
	TestJoining = "joining"
	TestLobby   = "lobby"
	TestRoom    = "room"
	TestError   = "error"
)

type TestJoin struct {
	URL       string    `json:"url"`
	Want      string    `json:"want"`
	Status    string    `json:"status"`
	Mode      string    `json:"mode"`
	Name      string    `json:"name"`
	Message   string    `json:"message"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (t TestJoin) GuestName() string {
	logx.Debugf("model", "TestJoin.GuestName: name=%q", t.Name)
	if strings.TrimSpace(t.Name) != "" {
		out := strings.TrimSpace(t.Name)
		logx.Debugf("model", "TestJoin.GuestName: -> %q", out)
		return out
	}
	logx.Debugf("model", "TestJoin.GuestName: -> default %q", TestGuestName)
	return TestGuestName
}

func BBBLabel(key string) string {
	logx.Debugf("model", "BBBLabel: key=%q", key)
	if id, ok := ParseBBBLessonID(key); ok {
		out := fmt.Sprintf("пара %d", id)
		logx.Debugf("model", "BBBLabel: key=%q -> %q", key, out)
		return out
	}
	d, t := ParseBBBKey(key)
	d = strings.TrimSpace(d)
	t = strings.TrimSpace(t)
	if d == "" {
		out := strings.TrimSpace(key)
		logx.Debugf("model", "BBBLabel: key=%q -> %q", key, out)
		return out
	}
	if t == "" {
		logx.Debugf("model", "BBBLabel: key=%q -> %q", key, d)
		return d
	}
	out := d + " · " + t
	logx.Debugf("model", "BBBLabel: key=%q -> %q", key, out)
	return out
}

type Event struct {
	ID         int64     `json:"id"`
	At         time.Time `json:"at"`
	Type       string    `json:"type"`
	TelegramID int64     `json:"telegram_id"`
	LessonID   int64     `json:"lesson_id"`
	Message    string    `json:"message"`
}

const (
	EventJoin    = "join"
	EventLeave   = "leave"
	EventLobby   = "lobby"
	EventReparse = "reparse"
	EventWake    = "wake"
	EventNoBBB   = "no_bbb"
	EventOnboard = "onboard"
	EventT15     = "t15"
	EventSkip    = "skip"
	EventError   = "error"
	EventRecord  = "record"
	EventSlides  = "slides"
	EventNotes   = "notes"
)

func IsLecture(typ string) bool {
	logx.Debugf("model", "IsLecture: type=%q", typ)
	t := strings.ToLower(strings.TrimSpace(typ))
	res := strings.Contains(t, "лекц") || strings.Contains(t, "lecture")
	logx.Debugf("model", "IsLecture: type=%q -> %v", typ, res)
	return res
}

const (
	PackRecording  = "recording"
	PackRecorded   = "recorded"
	PackSlides     = "slides"
	PackTranscribe = "transcribe"
	PackNotes      = "notes"
	PackDone       = "done"
	PackError      = "error"
)

type LecturePack struct {
	ID            int64      `json:"id"`
	LessonID      int64      `json:"lesson_id"`
	Discipline    string     `json:"discipline"`
	Number        int        `json:"number"`
	Date          string     `json:"date"`
	Dir           string     `json:"dir"`
	BBBURL        string     `json:"bbb_url"`
	Status        string     `json:"status"`
	Audio         string     `json:"audio"`
	Transcript    string     `json:"transcript"`
	NotesPDF      string     `json:"notes_pdf"`
	Err           string     `json:"err,omitempty"`
	PublishStatus string     `json:"publish_status,omitempty"`
	PublishedAt   *time.Time `json:"published_at,omitempty"`
	CleanedAt     *time.Time `json:"cleaned_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type ParseRun struct {
	ID          int64     `json:"id"`
	At          time.Time `json:"at"`
	OK          bool      `json:"ok"`
	Status      string    `json:"status"`
	LessonCount int       `json:"lesson_count"`
	OnlineCount int       `json:"online_count"`
	Diff        string    `json:"diff"`
}

type JoinIntent struct {
	TelegramID int64        `json:"telegram_id"`
	LessonID   int64        `json:"lesson_id"`
	Decision   JoinDecision `json:"decision"`
	AskedAt    time.Time    `json:"asked_at"`
	DecidedAt  *time.Time   `json:"decided_at,omitempty"`
}

type Recording struct {
	Name       string    `json:"name"`
	Rel        string    `json:"rel,omitempty"`
	Size       int64     `json:"size"`
	Mod        time.Time `json:"mod"`
	Status     string    `json:"status,omitempty"`
	Number     int       `json:"number,omitempty"`
	Discipline string    `json:"discipline,omitempty"`
}

type PersonCard struct {
	User        *User `json:"user"`
	TelegramID  int64 `json:"telegram_id"`
	InWhitelist bool  `json:"in_whitelist"`
	HasProfile  bool  `json:"has_profile"`
}

const (
	PresenceNone  = "none"
	PresenceLobby = "lobby"
	PresenceRoom  = "room"
	PresenceError = "error"
)

type Presence struct {
	TelegramID int64     `json:"telegram_id"`
	LessonID   int64     `json:"lesson_id"`
	State      string    `json:"state"`
	Message    string    `json:"message"`
	UpdatedAt  time.Time `json:"updated_at"`
}
