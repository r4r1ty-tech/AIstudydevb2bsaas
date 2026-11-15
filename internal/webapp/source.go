package webapp

import (
	"context"
	"errors"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
)

// Source — всё, что панель читает и пишет. Обработчики не знают, откуда данные:
// *store.Store подходит как есть (общая sqlite), demoSource держит правдоподобные
// данные в памяти для PANEL_DEMO=1.
type Source interface {
	ListUsers() ([]model.User, error)
	GetUser(id int64) (*model.User, error)
	UpsertUser(u *model.User) error
	SetEnabled(id int64, enabled bool) error
	SetDisabledUntil(id int64, until *time.Time) error
	SetFIO(id int64, fio string) error
	SetSubgroup(id int64, n int) error
	SetExtraWords(id int64, words []string) error

	ListLessons() ([]model.Lesson, error)
	LessonByID(id int64) (*model.Lesson, error)
	CurrentLesson(now time.Time) (*model.Lesson, error)
	NextLesson(now time.Time) (*model.Lesson, error)
	LastParseRun() (*model.ParseRun, error)

	GetLessonBBB(lessonID int64) string
	SetLessonBBB(lessonID int64, url string) error
	GetBBB(key string) (*model.BBBLink, error)
	SetBBB(key, url string) error
	ListBBB() ([]model.BBBLink, error)

	GetIntent(telegramID, lessonID int64) (*model.JoinIntent, error)
	PutIntent(i model.JoinIntent) error

	ListPresence() ([]model.Presence, error)
	ListEvents(limit int) ([]model.Event, error)
	GetTestJoin() (model.TestJoin, error)
	PutTestJoin(j model.TestJoin) error
	ListPacks() ([]model.LecturePack, error)
	AnyRecording() (bool, error)
}

var _ Source = (*store.Store)(nil)

// packDetails — необязательные сведения о паке, которых нет в таблице.
// Демо отдаёт их из памяти; для настоящей базы панель смотрит файлы на диске.
type packDetails interface {
	PackSize(p model.LecturePack) int64
	PackTopic(p model.LecturePack) string
}

// MoodleStatus — состояние подключения аккаунта универа.
type MoodleStatus struct {
	Connected bool       `json:"connected"`
	Available bool       `json:"available"` // вход из панели уже работает
	Login     string     `json:"login,omitempty"`
	SyncedAt  *time.Time `json:"synced_at,omitempty"`
	Message   string     `json:"message,omitempty"`
}

// Deadline — один срок из Moodle.
type Deadline struct {
	ID      string    `json:"id"`
	Course  string    `json:"course"`
	Title   string    `json:"title"`
	Kind    string    `json:"kind"` // quiz | assign
	Due     time.Time `json:"due"`
	Status  string    `json:"status"` // todo | draft | submitted
	URL     string    `json:"url"`
	Section string    `json:"section,omitempty"`
	Todo    string    `json:"todo,omitempty"` // что сделать: условия, попытки, описание
	// Closed — тест закрыт для попыток и не пройден (model.MoodleItem.Closed).
	Closed bool `json:"closed"`
	// Opens — когда откроется (нулевое время, если Moodle не сказал).
	Opens time.Time `json:"opens"`
	// NotOpen — ещё не открыт: Opens в будущем, срок не прошёл. Такие не идут в «Пройти все».
	NotOpen bool `json:"not_open"`
}

const (
	DeadlineQuiz   = "quiz"
	DeadlineAssign = "assign"

	DeadlineTodo      = "todo"
	DeadlineDraft     = "draft"
	DeadlineSubmitted = "submitted"
)

// Moodle — источник дедлайнов. Настоящий опрос Moodle — следующий этап,
// пока в обычном режиме работает moodleStub.
type Moodle interface {
	Status() MoodleStatus
	Deadlines(ctx context.Context, now time.Time) ([]Deadline, error)
	Login(ctx context.Context, login, password string) (MoodleStatus, error)
	Logout() error
}

// ErrMoodleNotReady — вход в Moodle из панели ещё не сделан.
var ErrMoodleNotReady = errors.New("moodle: not implemented")

// ErrMoodleBadLogin — Moodle не принял логин или пароль.
var ErrMoodleBadLogin = errors.New("moodle: bad login")

type moodleStub struct{}

func (moodleStub) Status() MoodleStatus {
	return MoodleStatus{Message: "Подключение к Moodle появится в следующей версии панели."}
}

func (moodleStub) Deadlines(context.Context, time.Time) ([]Deadline, error) {
	return []Deadline{}, nil
}

func (moodleStub) Login(context.Context, string, string) (MoodleStatus, error) {
	return moodleStub{}.Status(), ErrMoodleNotReady
}

func (moodleStub) Logout() error { return nil }
