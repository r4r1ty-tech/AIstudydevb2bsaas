package bbb

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-rod/rod"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
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
	"#input-name",
	"input[name='join_name']",
	"input[name='name']",
	"input[autocomplete='name']",
	"[data-test='nameInput']",
	"input[type='text']",
}

// Приглашёнческая страница BBB: сначала кнопка «Join Room», только потом форма/звук.
var welcomeJoinSels = []string{
	"[data-test='joinButton']",
	"[data-test='join-room']",
	"[data-test='sessionJoinButton']",
	`button[aria-label='Join Room']`,
	`button[aria-label='Войти в комнату']`,
}

var welcomeJoinRE = `(?i)join\s*room|войти в комнату|войти в конференцию|присоединиться к|подключиться к`

func clickWelcomeJoin(page *rod.Page) bool {
	if page == nil {
		logx.Debugf("bbb", "clickWelcomeJoin: nil page")
		return false
	}
	p := page.Timeout(2 * time.Second)
	if clickFirst(p, welcomeJoinSels) {
		logx.Debugf("bbb", "clickWelcomeJoin: clicked by selector")
		return true
	}
	ok := clickByText(p, welcomeJoinRE)
	logx.Debugf("bbb", "clickWelcomeJoin: by text=%v", ok)
	return ok
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
		logx.Debugf("bbb", "classifySeat: room selector -> room")
		return seatRoom
	}
	if sig.hasLobby {
		logx.Debugf("bbb", "classifySeat: lobby selector -> lobby")
		return seatLobby
	}
	if sig.hasForm {
		logx.Debugf("bbb", "classifySeat: form -> form")
		return seatForm
	}
	low := strings.ToLower(sig.text)
	for _, m := range lobbyTextMarks {
		if strings.Contains(low, m) {
			logx.Debugf("bbb", "classifySeat: text mark %q -> lobby", m)
			return seatLobby
		}
	}
	logx.Debugf("bbb", "classifySeat: unknown form=%v room=%v lobby=%v audio=%v", sig.hasForm, sig.hasRoom, sig.hasLobby, sig.hasAudio)
	return seatUnknown
}

func (s *chromeSession) InLobby(ctx context.Context) (bool, error) {
	st, err := s.seat(ctx)
	if err != nil {
		logx.Errorf("bbb", "InLobby: seat: %v", err)
		return false, fmt.Errorf("InLobby: seat: %w", err)
	}
	in := st == seatLobby
	logx.Debugf("bbb", "InLobby: seat=%s -> %v", st, in)
	return in, nil
}

func (s *chromeSession) InRoom(ctx context.Context) (bool, error) {
	st, err := s.seat(ctx)
	if err != nil {
		logx.Errorf("bbb", "InRoom: seat: %v", err)
		return false, fmt.Errorf("InRoom: seat: %w", err)
	}
	in := st == seatRoom
	logx.Debugf("bbb", "InRoom: seat=%s -> %v", st, in)
	return in, nil
}

func (s *chromeSession) seat(ctx context.Context) (seat, error) {
	sig, err := s.signals(ctx)
	if err != nil {
		logx.Errorf("bbb", "seat: signals: %v", err)
		return seatUnknown, fmt.Errorf("seat: signals: %w", err)
	}
	st := classifySeat(sig)
	logx.Debugf("bbb", "seat: %s", st)
	return st, nil
}

func (s *chromeSession) signals(ctx context.Context) (seatSignals, error) {
	if s == nil || s.page == nil {
		err := fmt.Errorf("нет вкладки")
		logx.Errorf("bbb", "signals: %v", err)
		return seatSignals{}, err
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
		logx.Errorf("bbb", "signals: visibleText: %v", err)
		return sig, fmt.Errorf("signals: visibleText: %w", err)
	}
	sig.text = text
	logx.Debugf("bbb", "signals: form=%v room=%v lobby=%v audio=%v textlen=%d", sig.hasForm, sig.hasRoom, sig.hasLobby, sig.hasAudio, len(text))
	return sig, nil
}

func visibleText(page *rod.Page) (string, error) {
	res, err := page.Eval(`() => {
		const t = (document.body && (document.body.innerText || document.body.textContent) || '')
		return String(t).slice(0, 4000)
	}`)
	if err != nil {
		logx.Debugf("bbb", "visibleText: eval: %v", err)
		return "", fmt.Errorf("visibleText: %w", err)
	}
	if res == nil {
		logx.Debugf("bbb", "visibleText: nil result")
		return "", nil
	}
	t := res.Value.Str()
	logx.Debugf("bbb", "visibleText: len=%d", len(t))
	return t, nil
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
		logx.Debugf("bbb", "pageHint: eval: %v", err)
		return errString(err)
	}
	hint := strings.TrimSpace(res.Value.Str())
	logx.Debugf("bbb", "pageHint: %s", hint)
	return hint
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (s *chromeSession) waitSeated(ctx context.Context, req JoinReq) error {
	if s == nil || s.page == nil {
		err := fmt.Errorf("нет вкладки")
		logx.Errorf("bbb", "waitSeated: %v", err)
		return err
	}
	logx.Debugf("bbb", "waitSeated: role=%s fio=%q url=%s", req.Role, req.FIO, redactURL(req.URL))
	if ctx == nil {
		ctx = context.Background()
	}
	filled := false
	joinClicks := 0
	deadline := time.Now().Add(afterJoinWait)
	var last seat
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			logx.Errorf("bbb", "waitSeated: ctx: %v", err)
			return fmt.Errorf("waitSeated: ctx: %w", err)
		}
		sig, err := s.signals(ctx)
		if err != nil {
			logx.Errorf("bbb", "waitSeated: signals: %v", err)
			return fmt.Errorf("waitSeated: signals: %w", err)
		}
		last = classifySeat(sig)
		if last == seatLobby || last == seatRoom {
			logx.Debugf("bbb", "waitSeated: seated=%s", last)
			audioOnce(s.page.Context(ctx), req.Role)
			return nil
		}
		if sig.hasAudio {
			logx.Debugf("bbb", "waitSeated: audio modal, dismiss")
			audioOnce(s.page.Context(ctx), req.Role)
		}
		if last == seatForm && !filled {
			logx.Debugf("bbb", "waitSeated: form, fill guest name")
			if err := fillGuestName(s.page.Context(ctx), req.FIO); err != nil {
				logx.Errorf("bbb", "waitSeated: fillGuestName: %v", err)
				return fmt.Errorf("форма гостя: %w", err)
			}
			filled = true
			continue
		}
		// Приглашённая страница: пока не нажмём «Join Room», формы/лобби не будет.
		// Пробуем до аудио-модалки: на приглашённой странице может быть и её скрытый DOM.
		if last == seatUnknown && joinClicks < 3 {
			if clickWelcomeJoin(s.page.Context(ctx)) {
				joinClicks++
				logx.Debugf("bbb", "waitSeated: welcome join click=%d", joinClicks)
				time.Sleep(800 * time.Millisecond)
				continue
			}
		}
		time.Sleep(400 * time.Millisecond)
	}
	hint := pageHint(s.page.Context(ctx))
	if hint == "" {
		hint = last.String()
	}
	if clicks := clickablesHint(s.page.Context(ctx)); clicks != "" {
		hint += " :: " + clicks
	}
	err := fmt.Errorf("не в комнате (%s): %s", last, hint)
	logx.Warnf("bbb", "waitSeated: %v", err)
	return err
}

// clickablesHint перечисляет кнопки/ссылки/поля на странице — чтобы понять,
// что за экран BBB, когда заход не распознан.
func clickablesHint(page *rod.Page) string {
	if page == nil {
		return ""
	}
	res, err := page.Timeout(3 * time.Second).Eval(`() => {
		const out = []
		const nodes = document.querySelectorAll('button, a, [role="button"], [data-test], input')
		for (const n of nodes) {
			const dt = n.getAttribute('data-test') || ''
			const al = n.getAttribute('aria-label') || ''
			const tx = (n.innerText || n.value || '').replace(/\s+/g, ' ').trim().slice(0, 40)
			if (!dt && !al && !tx) continue
			out.push(n.tagName.toLowerCase() + (dt ? ' dt=' + dt : '') + (al ? ' aria=' + al : '') + (tx ? ' "' + tx + '"' : ''))
			if (out.length >= 25) break
		}
		return out.join(' | ')
	}`)
	if err != nil || res == nil {
		logx.Debugf("bbb", "clickablesHint: eval: %v", err)
		return ""
	}
	h := strings.TrimSpace(res.Value.Str())
	logx.Debugf("bbb", "clickablesHint: %s", h)
	return h
}

func audioOnce(page *rod.Page, role Role) {
	if page == nil {
		return
	}
	logx.Debugf("bbb", "audioOnce: role=%s", role)
	p := page.Timeout(2 * time.Second)
	if role == RolePresence {
		if clickFirst(p, closeAudioSels) || clickByText(p, `(?i)close|закрыть|skip|пропуст`) {
			logx.Debugf("bbb", "audioOnce: closed modal")
			return
		}
	}
	if clickFirst(p, listenOnlySels) || clickByText(p, listenOnlyRE) {
		logx.Debugf("bbb", "audioOnce: listen-only clicked")
		return
	}
	if role == RoleRecord {
		logx.Warnf("bbb", "listen-only не нажалась — звук может быть пустым")
	} else {
		logx.Debugf("bbb", "audioOnce: no audio control found")
	}
}
