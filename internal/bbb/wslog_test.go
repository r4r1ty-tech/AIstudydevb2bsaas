package bbb

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestHandshakeTapReadsStatusAndHeaders(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	go func() {
		defer func() { _ = server.Close() }()
		_, _ = server.Write([]byte("HTTP/1.1 403 Forbidden\r\nRetry-After: 7\r\nSet-Cookie: secret=1\r\n\r\n"))
	}()
	tap := &handshakeTap{Conn: client}
	buf := make([]byte, 256)
	if _, err := tap.Read(buf); err != nil {
		t.Fatal(err)
	}
	st, line := tap.Status()
	if st != 403 || line != "HTTP/1.1 403 Forbidden" {
		t.Fatalf("status=%d line=%q", st, line)
	}
	if got := tap.Header("Retry-After"); got != "7" {
		t.Fatalf("Retry-After=%q", got)
	}
}

func TestIsThrottleError(t *testing.T) {
	for _, code := range []int{403, 429, 503} {
		if !isThrottleError(&handshakeError{Status: code}) {
			t.Fatalf("HTTP %d должен считаться throttle", code)
		}
	}
	if isThrottleError(&handshakeError{Status: 500}) {
		t.Fatal("HTTP 500 не должен включать host cooldown")
	}
}

func newGuardWorker() *Worker {
	return &Worker{
		joinHostCooldown: map[string]time.Time{},
		joinHostFailN:    map[string]int{},
		joinHostSem:      map[string]chan struct{}{},
		joinHostRecSem:   map[string]chan struct{}{},
		joinHostNext:     map[string]time.Time{},
		retryAt:          map[string]time.Time{},
	}
}

func TestHostCooldownAndDefer(t *testing.T) {
	w := newGuardWorker()
	host := "bbb.ssau.ru"
	w.noteHostFailure(host, &handshakeError{Status: 403})
	wait := w.hostCooldownWait(host)
	if wait <= 0 || wait > joinHostCooldown+time.Second {
		t.Fatalf("cooldown = %s, ожидали около %s", wait, joinHostCooldown)
	}
	w.deferHost("key", host, wait)
	if until := w.retryAt["key"]; !until.After(time.Now()) {
		t.Fatal("deferHost должен отложить повтор")
	}
	w.resetHostFailure(host)
	if wait := w.hostCooldownWait(host); wait != 0 {
		t.Fatalf("после успеха cooldown должен сброситься, получили %s", wait)
	}
}

func TestNoteHostFailureCooldownDoesNotOverflow(t *testing.T) {
	w := newGuardWorker()
	for i := 0; i < 40; i++ {
		w.noteHostFailure("host", &handshakeError{Status: 429})
	}
	wait := w.hostCooldownWait("host")
	if wait <= 0 || wait > joinHostCooldownMax+time.Second {
		t.Fatalf("cooldown после 40 ошибок = %s, ожидали (0, %s]", wait, joinHostCooldownMax)
	}
}

func TestBeginHostJoinStopsWhileCooldown(t *testing.T) {
	w := newGuardWorker()
	w.joinHostCooldown["host"] = time.Now().Add(time.Minute)
	release, wait, ok := w.beginHostJoin(context.Background(), "host", false)
	if ok || release != nil || wait <= 0 {
		t.Fatalf("во время cooldown ожидали отказ с паузой, получили ok=%v wait=%s", ok, wait)
	}
}

func TestBeginHostJoinContextDoesNotLeakSlot(t *testing.T) {
	w := newGuardWorker()
	sem := make(chan struct{}, maxJoinsPerHost)
	sem <- struct{}{}
	sem <- struct{}{}
	w.joinHostSem["host"] = sem
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if release, _, ok := w.beginHostJoin(ctx, "host", false); ok || release != nil {
		t.Fatal("при заполненном семафоре и отменённом ctx слот не должен выдаваться")
	}
	if len(sem) != maxJoinsPerHost {
		t.Fatalf("слот семафора утёк после отмены ctx: занято %d из %d", len(sem), maxJoinsPerHost)
	}
}

func TestRecorderHasOwnJoinSlot(t *testing.T) {
	w := newGuardWorker()
	sem := make(chan struct{}, maxJoinsPerHost)
	sem <- struct{}{}
	sem <- struct{}{}
	w.joinHostSem["host"] = sem
	release, _, ok := w.beginHostJoin(context.Background(), "host", true)
	if !ok {
		t.Fatal("рекордер должен получать отдельный слот, даже когда обычные заняты")
	}
	release()
}
