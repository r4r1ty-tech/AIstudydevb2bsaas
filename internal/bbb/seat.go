package bbb

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-rod/rod"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/capture"
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
	// Не любой input[type='text']: на экране ожидания поле поиска/чата давало
	// ложную «форму», и бот вписывал ФИО в чужое поле вместо ожидания.
	"input[id*='name' i][type='text']",
	"input[placeholder*='name' i]",
	"input[placeholder*='имя' i]",
	"input[placeholder*='фио' i]",
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
	"wait for a moderator",
	"wait for the moderator",
	"moderator to approve",
	"approve you",
	"подождите, пока модератор",
	"пока модератор",
	"модератор одобрит",
	"модератор не впустит",
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

// BBB-клиент через прокси грузится до ~45с; дедлайн с запасом.
var afterJoinWait = 120 * time.Second

// audioJoinWait is how long the recording tab waits for the audio chooser.
var audioJoinWait = 45 * time.Second

// joinAudioSels opens the audio chooser from the navbar (HTML5 client).
var joinAudioSels = []string{
	"[data-test='joinAudio']",
	"[data-test='join-audio']",
	"[data-test='audioButton']",
	"[data-test='audioControl']",
	`button[aria-label='Join audio']`,
	`button[aria-label='Присоединиться к аудио']`,
	`button[aria-label='Подключить звук']`,
}

// audioJoinedSels are present once the tab is already in audio (listen-only
// or mic); then there is nothing to click and navbar toggles must stay alone.
var audioJoinedSels = []string{
	"[data-test='leaveAudio']",
	"[data-test='leaveListenOnly']",
	`button[aria-label='Leave audio']`,
	`button[aria-label='Выйти из аудио']`,
	`button[aria-label='Отключить звук']`,
}

// joinAudioRE is the navbar "join audio" text. No bare "audio": it would also
// match "Leave audio" and drop the tab out of the conference.
const joinAudioRE = `(?i)join audio|подключить звук|присоединиться к аудио|подключиться к аудио`

// audioProbeSels are logged on failure so it is clear which audio controls exist.
var audioProbeSels = []string{
	"[data-test='joinAudio']",
	"[data-test='listenOnlyBtn']",
	"[data-test='microphoneBtn']",
	"[data-test='audioControl']",
	"[data-test='leaveAudio']",
	"[data-test='leaveListenOnly']",
	"[data-test='unmuteButton']",
	"[data-test='muteButton']",
	"[data-test='audioModal']",
}

func audioProbeHint(page *rod.Page) string {
	if page == nil {
		return ""
	}
	res, err := page.Timeout(3*time.Second).Eval(`(sels) => {
		const found = []
		for (const s of sels) { if (document.querySelector(s)) found.push(s) }
		return found.join(',')
	}`, audioProbeSels)
	if err != nil || res == nil {
		logx.Debugf("bbb", "audioProbeHint: eval: %v", err)
		return ""
	}
	return strings.TrimSpace(res.Value.Str())
}

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
	// Типографский апостроф BBB (hasn’t) приводим к прямому, как в метках.
	low := strings.NewReplacer("’", "'", "‘", "'", "ʼ", "'").Replace(strings.ToLower(sig.text))
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
		// Без query: в нём sessionToken живой сессии BBB, а лог читают и хранят.
		const href = (location.origin || '') + (location.pathname || '')
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
	filled := 0
	joinClicks := 0
	badSignals := 0
	deadline := time.Now().Add(afterJoinWait)
	var last seat
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			logx.Errorf("bbb", "waitSeated: ctx: %v", err)
			return fmt.Errorf("waitSeated: ctx: %w", err)
		}
		sig, err := s.signals(ctx)
		if err != nil {
			badSignals++
			logx.Errorf("bbb", "waitSeated: signals (%d): %v", badSignals, err)
			if badSignals >= 10 {
				return fmt.Errorf("waitSeated: signals: %w", err)
			}
			time.Sleep(700 * time.Millisecond)
			continue
		}
		badSignals = 0
		last = classifySeat(sig)
		if last == seatLobby || last == seatRoom {
			logx.Debugf("bbb", "waitSeated: seated=%s", last)
			if last == seatRoom {
				s.audioOK = audioOnce(s.page.Context(ctx), req.Role)
			} else {
				// В лобби аудио-модалки нет: 45 с ожидания тут впустую. Звук
				// подключит EnsureAudio после перехода в комнату.
				audioTry(s.page.Context(ctx), req.Role, 2*time.Second)
			}
			return nil
		}
		if sig.hasAudio {
			// «Close» бывает и у баннеров Greenlight до комнаты — короткая попытка,
			// иначе 45 с ожидания записи съедают дедлайн захода.
			logx.Debugf("bbb", "waitSeated: audio modal, dismiss")
			audioTry(s.page.Context(ctx), req.Role, 3*time.Second)
			continue
		}
		if last == seatForm && filled < 3 {
			logx.Debugf("bbb", "waitSeated: form, fill guest name (n=%d)", filled+1)
			if err := fillGuestName(s.page.Context(ctx), req.FIO); err != nil {
				logx.Warnf("bbb", "waitSeated: fillGuestName (попытка %d): %v", filled+1, err)
				time.Sleep(time.Second)
				continue
			}
			filled++
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

func audioOnce(page *rod.Page, role Role) bool {
	wait := 2 * time.Second
	if role == RoleRecord {
		// Модалка выбора аудио появляется после входа в комнату с задержкой;
		// записывающей вкладке нужно дождаться и нажать «Только слушать».
		wait = audioJoinWait
	}
	return audioTry(page, role, wait)
}

// audioTry подключает аудио за wait. Для записи успех — только когда в навбаре
// появилось «Выйти из аудио»: клик сам по себе ещё не значит, что звук пошёл.
func audioTry(page *rod.Page, role Role, wait time.Duration) bool {
	if page == nil {
		return false
	}
	deadline := time.Now().Add(wait)
	logx.Debugf("bbb", "audioTry: role=%s wait=%s", role, wait.Round(time.Second))
	for time.Now().Before(deadline) {
		p := page.Timeout(2 * time.Second)
		// Уже в аудио — навбар не трогаем (и не кликаем текст «listen only» из чата).
		if role == RoleRecord && hasAny(p, audioJoinedSels) {
			logx.Infof("bbb", "audioTry: role=%s in audio", role)
			logAudioJoined(page, role)
			return true
		}
		if role == RolePresence {
			if clickFirst(p, closeAudioSels) || clickByText(p, `(?i)close|закрыть|skip|пропуст`) {
				logx.Debugf("bbb", "audioTry: closed modal")
				return true
			}
		}
		if clickFirst(p, listenOnlySels) || clickByText(p, listenOnlyRE) {
			logx.Infof("bbb", "audioTry: role=%s listen-only clicked", role)
			if role != RoleRecord || waitAny(page, audioJoinedSels, minDur(10*time.Second, time.Until(deadline)+2*time.Second)) {
				logAudioJoined(page, role)
				return true
			}
			logx.Warnf("bbb", "audioTry: listen-only нажата, но аудио не подключилось — повторяю")
			continue
		}
		// Аудио-модалки может не быть: открываем выбор аудио из навбара.
		if clickFirst(p, joinAudioSels) || clickByText(p, joinAudioRE) {
			logx.Debugf("bbb", "audioTry: opened audio chooser")
			time.Sleep(500 * time.Millisecond)
			continue
		}
		time.Sleep(300 * time.Millisecond)
	}
	if role == RoleRecord {
		logx.Warnf("bbb", "listen-only не нажалась — звук может быть пустым :: audio=[%s] :: %s",
			audioProbeHint(page), clickablesHint(page))
	} else {
		logx.Debugf("bbb", "audioTry: no audio control found")
	}
	return false
}

func waitAny(page *rod.Page, sels []string, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if hasAny(page.Timeout(2*time.Second), sels) {
			return true
		}
		time.Sleep(400 * time.Millisecond)
	}
	return false
}

func minDur(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// logAudioJoined records which audio controls exist and where PulseAudio
// routes streams right after the recording tab joins audio, so a silent
// recording can be told apart from a tab that never played anything.
func logAudioJoined(page *rod.Page, role Role) {
	if role != RoleRecord {
		return
	}
	logx.Infof("bbb", "audio joined :: audio=[%s]", audioProbeHint(page))
	capture.LogRoute("bbb", "audio-joined")
}
