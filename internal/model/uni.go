package model

import "time"

// Аккаунты универа: личный кабинет (lk.ssau.ru) и Moodle (do.ssau.ru).
const (
	AccountLK     = "lk"
	AccountMoodle = "moodle"
)

// Состояние аккаунта после последней попытки входа или синка.
const (
	AccountPending  = "pending"   // сохранён, синк ещё не прошёл
	AccountOK       = "ok"        // последний синк удался
	AccountBadLogin = "bad_login" // сайт не принял логин/пароль — нужен новый
	AccountError    = "error"     // сеть/вёрстка; повторим сами
)

type Account struct {
	TelegramID int64      `json:"telegram_id"`
	Kind       string     `json:"kind"`
	Login      string     `json:"login"`
	Secret     string     `json:"-"` // пароль, зашифрован vault
	Session    string     `json:"-"` // куки сессии, зашифрованы vault
	Status     string     `json:"status"`
	Message    string     `json:"message,omitempty"`
	SyncedAt   *time.Time `json:"synced_at,omitempty"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// Типы элементов Moodle, у которых бывают сроки.
const (
	MoodleQuiz     = "quiz"
	MoodleAssign   = "assign"
	MoodleLesson   = "lesson"
	MoodleWorkshop = "workshop"
)

// Статус элемента Moodle для студента.
const (
	MoodleTodo      = "todo"      // ничего не отправлено
	MoodleDraft     = "draft"     // черновик / незавершённая попытка
	MoodleSubmitted = "submitted" // отправлено / попытка завершена
	MoodleDone      = "done"      // условия выполнения засчитаны
)

// MoodleItem — одно задание или тест студента с его сроком и тем, что нужно сделать.
type MoodleItem struct {
	TelegramID int64      `json:"telegram_id"`
	CMID       int64      `json:"cmid"`
	CourseID   int64      `json:"course_id"`
	Course     string     `json:"course"`
	Section    string     `json:"section,omitempty"`
	Kind       string     `json:"kind"`
	Title      string     `json:"title"`
	URL        string     `json:"url"`
	Opens      *time.Time `json:"opens,omitempty"`
	Due        *time.Time `json:"due,omitempty"`
	Status     string     `json:"status"`
	Todo       string     `json:"todo,omitempty"` // условия выполнения, попытки, лимит времени, кратко из описания
	UpdatedAt  time.Time  `json:"updated_at"`
}

// Finished — больше ничего делать не нужно.
func (m MoodleItem) Finished() bool {
	return m.Status == MoodleSubmitted || m.Status == MoodleDone
}
