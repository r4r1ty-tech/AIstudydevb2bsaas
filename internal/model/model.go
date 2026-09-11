package model

import (
	"fmt"
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

type BBBLink struct {
	Key       string    `json:"key"`
	URL       string    `json:"url"`
	UpdatedAt time.Time `json:"updated_at"`
}

func BBBKey(groupID int64, discipline, teacher string) string {
	return fmt.Sprintf("%d|%s|%s", groupID, strings.TrimSpace(discipline), strings.TrimSpace(teacher))
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
)

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
	Name string    `json:"name"`
	Size int64     `json:"size"`
	Mod  time.Time `json:"mod"`
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
