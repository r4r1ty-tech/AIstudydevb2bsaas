package model

import (
	"fmt"
	"strconv"
	"strings"
	"time"
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
	SOCKS5        string     `json:"socks5"`
	ExtraWords    []string   `json:"extra_words"`
	OnboardStage  int        `json:"onboard_stage"`
	Onboarded     bool       `json:"onboarded"`
	CreatedAt     time.Time  `json:"created_at"`
}

func (u User) Active(now time.Time) bool {
	if !u.Enabled {
		return false
	}
	if u.DisabledUntil != nil && now.Before(*u.DisabledUntil) {
		return false
	}
	return u.Onboarded && strings.TrimSpace(u.FIO) != ""
}

func (u User) Surname() string {
	f := strings.TrimSpace(u.FIO)
	if f == "" {
		return ""
	}
	return strings.Fields(f)[0]
}

const (
	StageFIO   = 0
	StageSub   = 1
	StageWords = 2
	StageDone  = 3
)

var CommonWakeWords = []string{"тест", "контрольная", "мудл", "moodle"}

func ParseWakeWords(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
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
	return out
}

func SkipWakeWords(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "", "-", "—", ".", "нет", "не надо", "пропуск", "skip", "/skip", "clear", "очистить":
		return true
	default:
		return false
	}
}

func FormatWakeWords(words []string) string {
	return strings.Join(ParseWakeWords(strings.Join(words, " ")), ", ")
}

func MergeWakeWords(old, add []string) []string {
	return ParseWakeWords(strings.Join(append(append([]string{}, old...), add...), " "))
}

func (u User) WakeList() []string {
	add := append([]string{}, CommonWakeWords...)
	if s := u.Surname(); s != "" {
		add = append(add, s)
	}
	add = append(add, u.ExtraWords...)
	return ParseWakeWords(strings.Join(add, " "))
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
	return l.Subgroup == 0 || l.Subgroup == n
}

func (l Lesson) SlotLabel() string {
	return l.Start + "–" + l.End
}

func (l Lesson) Identity() string {
	on := "0"
	if l.Online {
		on = "1"
	}
	return strings.Join([]string{
		l.Date,
		l.Start,
		l.Discipline,
		l.Teacher,
		l.Place,
		fmt.Sprintf("%d", l.Subgroup),
		on,
	}, "|")
}

type BBBLink struct {
	Key       string    `json:"key"`
	URL       string    `json:"url"`
	UpdatedAt time.Time `json:"updated_at"`
}

func BBBKey(groupID int64, discipline, teacher string) string {
	return fmt.Sprintf("%d|%s|%s", groupID, strings.TrimSpace(discipline), strings.TrimSpace(teacher))
}

func BBBLessonKey(lessonID int64) string {
	if lessonID <= 0 {
		return ""
	}
	return fmt.Sprintf("lesson:%d", lessonID)
}

func ParseBBBLessonID(key string) (int64, bool) {
	key = strings.TrimSpace(key)
	if !strings.HasPrefix(key, "lesson:") {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(key, "lesson:"), 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func ParseBBBKey(key string) (discipline, teacher string) {
	parts := strings.Split(key, "|")
	switch len(parts) {
	case 0, 1:
		return strings.TrimSpace(key), ""
	case 2:
		return strings.TrimSpace(parts[1]), ""
	default:
		return strings.TrimSpace(parts[1]), strings.TrimSpace(strings.Join(parts[2:], "|"))
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
	if strings.TrimSpace(t.Name) != "" {
		return strings.TrimSpace(t.Name)
	}
	return TestGuestName
}

func BBBLabel(key string) string {
	if id, ok := ParseBBBLessonID(key); ok {
		return fmt.Sprintf("пара %d", id)
	}
	d, t := ParseBBBKey(key)
	d = strings.TrimSpace(d)
	t = strings.TrimSpace(t)
	if d == "" {
		return strings.TrimSpace(key)
	}
	if t == "" {
		return d
	}
	return d + " · " + t
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
	EventProxy   = "proxy"
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
	t := strings.ToLower(strings.TrimSpace(typ))
	return strings.Contains(t, "лекц") || strings.Contains(t, "lecture")
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
