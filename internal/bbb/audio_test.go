package bbb

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

func TestSFUAudioSessionStopSendsStopBeforeClose(t *testing.T) {
	t.Parallel()

	gotCh := make(chan map[string]any, 4)
	srv := httptest.NewServer(websocket.Handler(func(ws *websocket.Conn) {
		defer ws.Close()
		var raw string
		for {
			if err := websocket.Message.Receive(ws, &raw); err != nil {
				return
			}
			var msg map[string]any
			if json.Unmarshal([]byte(raw), &msg) == nil {
				gotCh <- msg
			}
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	ws, err := websocket.Dial(wsURL, "", srv.URL)
	if err != nil {
		t.Fatalf("websocket.Dial: %v", err)
	}

	_, cancel := context.WithCancel(context.Background())
	readDone := make(chan struct{})
	close(readDone)

	as := &sfuAudioSession{
		sfuWS:    ws,
		readDone: readDone,
		cancel:   cancel,
	}

	as.stop()
	// Повторный вызов stop() безопасен и не шлёт дубликат.
	as.stop()

	select {
	case msg := <-gotCh:
		if msg["id"] != "stop" || msg["type"] != "audio" || msg["role"] != "recv" {
			t.Fatalf("неожиданное сообщение stop: %+v", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("сервер не получил сообщение stop до закрытия соединения")
	}

	if !as.closed.Load() {
		t.Fatal("после stop() флаг closed должен быть true")
	}
}
