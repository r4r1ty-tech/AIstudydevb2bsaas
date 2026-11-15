package webapp

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/lk"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodle"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/uni"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/vault"
)

// storeMoodle — настоящий Moodle панели: вход проверяется на сайте, пароль
// хранится зашифрованным, задания собирает процесс rasp (internal/uni) в sqlite.
type storeMoodle struct {
	st         *store.Store
	v          *vault.Vault
	owner      int64 // «я» панели — ADMIN_TELEGRAM_ID
	lkBase     string
	moodleBase string
}

func (m *storeMoodle) Status() MoodleStatus {
	acc, err := m.st.GetAccount(m.owner, model.AccountMoodle)
	if err != nil {
		logx.Errorf("webapp", "moodle status: %v", err)
		return MoodleStatus{Available: true, Message: "Не удалось прочитать аккаунт."}
	}
	if acc == nil {
		return MoodleStatus{Available: true}
	}
	st := MoodleStatus{Connected: true, Available: true, Login: acc.Login, SyncedAt: acc.SyncedAt}
	switch acc.Status {
	case model.AccountBadLogin:
		st.Message = "Moodle больше не принимает пароль — подключите заново."
	case model.AccountError:
		st.Message = "Последнее обновление не удалось, повторю сам."
	case model.AccountPending:
		st.Message = "Собираю задания — это пара минут."
	}
	return st
}

// Окна показа: старая просрочка — это закрытые элементы прошлых курсов,
// старое сданное только раздувает список.
const (
	overdueWindow = 14 * 24 * time.Hour
	doneWindow    = 30 * 24 * time.Hour
)

func (m *storeMoodle) Deadlines(_ context.Context, now time.Time) ([]Deadline, error) {
	items, err := m.st.ListMoodleItems(m.owner)
	if err != nil {
		return nil, err
	}
	out := make([]Deadline, 0, len(items))
	for _, it := range items {
		if it.Due == nil {
			// Без срока — всё несделанное, в том числе ещё не открытое: его показываем
			// отдельной группой, а не прячем до открытия.
			if !it.Finished() {
				out = append(out, deadlineFromItem(it, now))
			}
			continue
		}
		if age := now.Sub(*it.Due); (it.Finished() && age > doneWindow) || (!it.Finished() && age > overdueWindow) {
			continue
		}
		out = append(out, deadlineFromItem(it, now))
	}
	return out, nil
}

// notOpenItem — элемент ещё не открыт для сдачи: только если Opens в будущем.
// Текст «не открыт для попыток» без Opens сюда не относим: это часто исчерпанные
// попытки, такой тест остаётся закрытым (model.MoodleItem.Closed). Срок уже прошёл —
// не «скоро», даже если Opens в будущем.
func notOpenItem(it model.MoodleItem, now time.Time) bool {
	if it.Finished() || (it.Due != nil && it.Due.Before(now)) {
		return false
	}
	return it.Opens != nil && it.Opens.After(now)
}

func deadlineFromItem(it model.MoodleItem, now time.Time) Deadline {
	kind := DeadlineAssign
	if it.Kind == model.MoodleQuiz {
		kind = DeadlineQuiz
	}
	status := DeadlineTodo
	switch it.Status {
	case model.MoodleDraft:
		status = DeadlineDraft
	case model.MoodleSubmitted, model.MoodleDone:
		status = DeadlineSubmitted
	}
	d := Deadline{
		ID: strconv.FormatInt(it.CMID, 10), Course: it.Course, Title: it.Title, Kind: kind, Status: status,
		URL: it.URL, Section: it.Section, Todo: it.Todo,
		Closed: it.Closed(now), NotOpen: notOpenItem(it, now),
	}
	if it.Due != nil {
		d.Due = *it.Due
	}
	if it.Opens != nil {
		d.Opens = *it.Opens
	}
	return d
}

func (m *storeMoodle) Login(ctx context.Context, login, password string) (MoodleStatus, error) {
	c, err := moodle.New(m.moodleBase)
	if err != nil {
		return m.Status(), err
	}
	if err := c.Login(ctx, login, password); err != nil {
		if errors.Is(err, moodle.ErrBadLogin) {
			return m.Status(), ErrMoodleBadLogin
		}
		return m.Status(), err
	}
	sealed, err := m.v.Seal(password)
	if err != nil {
		return m.Status(), err
	}
	sess, _ := m.v.Seal(c.Session())
	if err := m.st.PutAccount(model.Account{TelegramID: m.owner, Kind: model.AccountMoodle, Login: login, Secret: sealed, Session: sess}); err != nil {
		return m.Status(), err
	}
	if err := uni.Kick(m.st, m.owner); err != nil {
		logx.Warnf("webapp", "moodle kick: %v", err)
	}
	m.maybeLK(ctx, login, password, sealed)
	return m.Status(), nil
}

// maybeLK — если ЛК ещё не подключён, пробуем тот же логин: обычно он общий.
func (m *storeMoodle) maybeLK(ctx context.Context, login, password, sealed string) {
	if acc, err := m.st.GetAccount(m.owner, model.AccountLK); err != nil || acc != nil {
		return
	}
	c, err := lk.New(m.lkBase)
	if err != nil {
		return
	}
	if err := c.Login(ctx, login, password); err != nil {
		logx.Infof("webapp", "lk with moodle login: %v", err)
		return
	}
	sess, _ := m.v.Seal(c.Session())
	if err := m.st.PutAccount(model.Account{TelegramID: m.owner, Kind: model.AccountLK, Login: login, Secret: sealed, Session: sess}); err != nil {
		logx.Warnf("webapp", "save lk: %v", err)
		return
	}
	if err := m.st.RequestRaspRefresh(); err != nil {
		logx.Warnf("webapp", "rasp kick: %v", err)
	}
}

func (m *storeMoodle) Logout() error {
	return m.st.DeleteAccount(m.owner, model.AccountMoodle)
}
