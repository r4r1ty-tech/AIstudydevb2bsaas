package bbb

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-rod/rod"
)

type seat int

const (
	seatUnknown seat = iota
	seatForm
	seatLobby
	seatRoom
)

func (s seat) String() string {
	switch s {
	case seatForm:
		return "form"
	case seatLobby:
		return "lobby"
	case seatRoom:
		return "room"
	default:
		return "unknown"
	}
}

type seatSignals struct {
	text     string
	hasForm  bool
	hasRoom  bool
	hasLobby bool
	hasAudio bool
}

var formSels = []string{
	"#join_name",
	"#join-name",
	"input[name='join_name']",
	"input[name='name']",
	"input[autocomplete='name']",
}

var roomSels = []string{
	"[data-test='userListItem']",
	"[data-test='userListContent']",
	"[data-test='chatButton']",
	"[data-test='navBar']",
	"[data-test='actionsBar']",
	"[data-test='messages']",
}

var lobbyElSels = []string{
	"[data-test='guestLobby']",
	"[data-test='waitingRoom']",
	"[data-test='waitingUsersView']",
	"[data-test='waitingUsers']",
	"[data-test='waitingusers']",
}

var lobbyTextMarks = []string{
	"waiting for a moderator",
	"waiting for the moderator",
	"waiting room",
	"you'll join when",
	"you will join when",
	"placed in a waiting",
	"guest lobby",
	"ожидайте",
	"зал ожидания",
	"ждите модератора",
	"ждём модератора",
	"ждем модератора",
	"meeting hasn't started",
	"meeting has not started",
	"session has not",
	"has not been started",
	"комната ещё не",
	"комната еще не",
	"встреча ещё не",
	"встреча еще не",
}

var afterJoinWait = 45 * time.Second

func classifySeat(sig seatSignals) seat {
	if sig.hasRoom {
		return seatRoom
	}
	if sig.hasLobby {
		return seatLobby
	}
	if sig.hasForm {
		return seatForm
	}
	low := strings.ToLower(sig.text)
	for _, m := range lobbyTextMarks {
		if strings.Contains(low, m) {
			return seatLobby
		}
	}
	return seatUnknown
}

func (s *chromeSession) InLobby(ctx context.Context) (bool, error) {
	st, err := s.seat(ctx)
	if err != nil {
		return false, err
	}
	return st == seatLobby, nil
}

func (s *chromeSession) InRoom(ctx context.Context) (bool, error) {
	st, err := s.seat(ctx)
	if err != nil {
		return false, err
	}
	return st == seatRoom, nil
}

func (s *chromeSession) seat(ctx context.Context) (seat, error) {
	sig, err := s.signals(ctx)
	if err != nil {
		return seatUnknown, err
	}
	return classifySeat(sig), nil
}

func (s *chromeSession) signals(ctx context.Context) (seatSignals, error) {
	if s == nil || s.page == nil {
		return seatSignals{}, fmt.Errorf("нет вкладки")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	p := s.page.Context(ctx).Timeout(4 * time.Second)
	sig := seatSignals{
		hasForm:  hasAny(p, formSels),
		hasRoom:  hasAny(p, roomSels),
		hasLobby: hasAny(p, lobbyElSels),
		hasAudio: hasAny(p, listenOnlySels) || hasAny(p, closeAudioSels),
	}
	text, err := visibleText(p)
	if err != nil {
		return sig, err
	}
	sig.text = text
	return sig, nil
}

func visibleText(page *rod.Page) (string, error) {
	res, err := page.Eval(`() => {
		const t = (document.body && (document.body.innerText || document.body.textContent) || '')
		return String(t).slice(0, 4000)
	}`)
	if err != nil {
		return "", err
	}
	if res == nil {
		return "", nil
	}
	return res.Value.Str(), nil
}

func pageHint(page *rod.Page) string {
	if page == nil {
		return ""
	}
	res, err := page.Timeout(3 * time.Second).Eval(`() => {
		const title = document.title || ''
		const href = location.href || ''
		const t = (document.body && (document.body.innerText || '') || '').replace(/\s+/g, ' ').trim().slice(0, 180)
		return title + ' | ' + href + ' | ' + t
	}`)
	if err != nil || res == nil {
		return errString(err)
	}
	return strings.TrimSpace(res.Value.Str())
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (s *chromeSession) waitSeated(ctx context.Context, req JoinReq) error {
	if s == nil || s.page == nil {
		return fmt.Errorf("нет вкладки")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	filled := false
	deadline := time.Now().Add(afterJoinWait)
	var last seat
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		sig, err := s.signals(ctx)
		if err != nil {
			return err
		}
		last = classifySeat(sig)
		if last == seatLobby || last == seatRoom {
			audioOnce(s.page.Context(ctx), req.Role)
			return nil
		}
		if sig.hasAudio {
			audioOnce(s.page.Context(ctx), req.Role)
		}
		if last == seatForm && !filled {
			if err := fillGuestName(s.page.Context(ctx), req.FIO); err != nil {
				return fmt.Errorf("форма гостя: %w", err)
			}
			filled = true
			continue
		}
		time.Sleep(400 * time.Millisecond)
	}
	hint := pageHint(s.page.Context(ctx))
	if hint == "" {
		hint = last.String()
	}
	return fmt.Errorf("не в комнате (%s): %s", last, hint)
}

func audioOnce(page *rod.Page, role Role) {
	if page == nil {
		return
	}
	p := page.Timeout(2 * time.Second)
	if role == RolePresence {
		if clickFirst(p, closeAudioSels) || clickByText(p, `(?i)close|закрыть|skip|пропуст`) {
			return
		}
	}
	_ = clickFirst(p, listenOnlySels) || clickByText(p, listenOnlyRE)
}
