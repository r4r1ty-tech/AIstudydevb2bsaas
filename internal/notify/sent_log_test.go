package notify

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

// Доставленное сообщение попадает в журнал INFO: кому, сколько символов и начало без переносов строк.
func TestHTMLLogsDeliveredMessage(t *testing.T) {
	api, cfg := fakeAPI(t)
	var buf bytes.Buffer
	logx.SetDebugSink(&buf, nil)
	t.Cleanup(func() { logx.SetDebugSink(nil, nil) })

	text := "🗓 <b>Скоро тест</b>\nТест 2 — очень длинное название, которое точно длиннее шестидесяти символов"
	HTML(context.Background(), cfg, 42, text)
	if n := len(api.Calls("sendMessage")); n != 1 {
		t.Fatalf("sendMessage calls = %d", n)
	}

	runes := []rune(text)
	head := strings.ReplaceAll(string(runes[:60]), "\n", " ")
	want := "[INFO] notify: sent tg=42 chars=" + strconv.Itoa(len(runes)) + " \"" + head + "\""
	var sentLines []string
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.Contains(l, "notify: sent") {
			sentLines = append(sentLines, l)
		}
	}
	if len(sentLines) != 1 || !strings.Contains(sentLines[0], want) {
		t.Fatalf("журнал: строки отправки %q, want %q", sentLines, want)
	}
}

// Сбой отправки в журнал INFO не пишем, остаётся ошибка.
func TestHTMLFailureNotLoggedAsSent(t *testing.T) {
	api, cfg := fakeAPI(t)
	api.Fail("sendMessage", "Forbidden: bot was blocked by the user")
	var buf bytes.Buffer
	logx.SetDebugSink(&buf, nil)
	t.Cleanup(func() { logx.SetDebugSink(nil, nil) })

	HTML(context.Background(), cfg, 42, "привет")
	if strings.Contains(buf.String(), "notify: sent") {
		t.Fatalf("сбой записан как отправка: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "[ERROR]") {
		t.Fatalf("ошибка не записана: %s", buf.String())
	}
}
