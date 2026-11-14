package model

import (
	"strings"
	"time"
)

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

// QuizNotOpenNote — фраза, которой страница теста описывает закрытый для попыток
// тест (internal/moodle/pages.go кладёт её в Todo).
const QuizNotOpenNote = "не открыт для попыток"

// Closed — тест закрыт для попыток и не пройден: не сдан, и либо срок уже прошёл,
// либо Moodle пишет, что попыток сейчас нет. Тест, который ещё не открылся
// (Opens в будущем), закрытым не считается: он откроется сам.
func (m MoodleItem) Closed(now time.Time) bool {
	if m.Kind != MoodleQuiz || m.Finished() {
		return false
	}
	if m.Due != nil && m.Due.Before(now) {
		return true
	}
	if m.Opens != nil && m.Opens.After(now) {
		return false
	}
	return strings.Contains(strings.ToLower(m.Todo), QuizNotOpenNote)
}
