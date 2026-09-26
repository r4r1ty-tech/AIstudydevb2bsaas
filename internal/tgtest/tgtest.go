// Package tgtest is a fake Telegram Bot API for tests: it answers every
// method with a valid result and records what the bot sent.
package tgtest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

const (
	Token   = "123:fake"
	BotID   = 123
	BotName = "fake_bot"
)

// Call is one request the bot made.
type Call struct {
	Method string
	Params map[string]string
	// File is the uploaded document name, if any.
	File string
}

// Param returns a request parameter ("" when absent).
func (c Call) Param(k string) string { return c.Params[k] }

// Server is the fake API. Fail makes a method return a Telegram error.
type Server struct {
	*httptest.Server

	mu    sync.Mutex
	calls []Call
	fail  map[string]string
	msgID atomic.Int64
}

// New starts the fake API and closes it with the test.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{fail: map[string]string{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// Opts points a gotgbot bot at this server.
func (s *Server) Opts() *gotgbot.BotOpts {
	return &gotgbot.BotOpts{
		BotClient: &gotgbot.BaseBotClient{
			Client:             http.Client{},
			DefaultRequestOpts: &gotgbot.RequestOpts{APIURL: s.URL},
		},
	}
}

// Bot returns a gotgbot bot that talks to this server.
func (s *Server) Bot(t testing.TB) *gotgbot.Bot {
	t.Helper()
	b, err := gotgbot.NewBot(Token, s.Opts())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Fail makes method answer with a Telegram error until cleared with "".
func (s *Server) Fail(method, description string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if description == "" {
		delete(s.fail, method)
		return
	}
	s.fail[method] = description
}

// Calls returns every recorded call, optionally only those of the methods.
func (s *Server) Calls(methods ...string) []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Call
	for _, c := range s.calls {
		if len(methods) == 0 || contains(methods, c.Method) {
			out = append(out, c)
		}
	}
	return out
}

// Texts returns the text of every sendMessage/editMessageText call.
func (s *Server) Texts() []string {
	var out []string
	for _, c := range s.Calls("sendMessage", "editMessageText") {
		out = append(out, c.Param("text"))
	}
	return out
}

// LastText is the text of the latest sendMessage/editMessageText, or "".
func (s *Server) LastText() string {
	t := s.Texts()
	if len(t) == 0 {
		return ""
	}
	return t[len(t)-1]
}

// Reset forgets recorded calls.
func (s *Server) Reset() {
	s.mu.Lock()
	s.calls = nil
	s.mu.Unlock()
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	method := parts[len(parts)-1]
	call := Call{Method: method, Params: map[string]string{}}
	if err := r.ParseMultipartForm(32 << 20); err == nil && r.MultipartForm != nil {
		for k, v := range r.MultipartForm.Value {
			if len(v) > 0 {
				call.Params[k] = v[0]
			}
		}
		for _, fh := range r.MultipartForm.File {
			if len(fh) > 0 {
				call.File = fh[0].Filename
			}
		}
	} else if body, _ := io.ReadAll(r.Body); len(body) > 0 {
		var m map[string]any
		if json.Unmarshal(body, &m) == nil {
			for k, v := range m {
				call.Params[k] = fmt.Sprint(v)
			}
		}
	}
	s.mu.Lock()
	s.calls = append(s.calls, call)
	desc, failing := s.fail[method]
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if failing {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 400, "description": desc})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": s.result(call)})
}

func (s *Server) result(c Call) any {
	switch c.Method {
	case "getMe":
		return map[string]any{"id": BotID, "is_bot": true, "first_name": "Fake", "username": BotName}
	case "getUpdates":
		return []any{}
	case "sendMessage", "sendDocument", "editMessageText", "editMessageReplyMarkup":
		var chat int64
		fmt.Sscan(c.Param("chat_id"), &chat)
		return map[string]any{
			"message_id": s.msgID.Add(1),
			"date":       0,
			"chat":       map[string]any{"id": chat, "type": "private"},
			"text":       c.Param("text"),
		}
	default:
		return true
	}
}

// Message builds a private-chat text update from user id.
func Message(updateID, userID int64, text string) *gotgbot.Update {
	msg := &gotgbot.Message{
		MessageId: updateID,
		Date:      1,
		Chat:      gotgbot.Chat{Id: userID, Type: "private"},
		From:      &gotgbot.User{Id: userID, FirstName: "U", Username: fmt.Sprintf("u%d", userID)},
		Text:      text,
	}
	if strings.HasPrefix(text, "/") {
		cmd := strings.Fields(text)[0]
		msg.Entities = []gotgbot.MessageEntity{{Type: "bot_command", Offset: 0, Length: int64(len(cmd))}}
	}
	return &gotgbot.Update{UpdateId: updateID, Message: msg}
}

// Callback builds an inline-button press by userID on a bot message.
func Callback(updateID, userID int64, data string) *gotgbot.Update {
	return &gotgbot.Update{
		UpdateId: updateID,
		CallbackQuery: &gotgbot.CallbackQuery{
			Id:   fmt.Sprintf("cb%d", updateID),
			From: gotgbot.User{Id: userID, FirstName: "U"},
			Data: data,
			Message: &gotgbot.Message{
				MessageId: 777,
				Date:      1,
				Chat:      gotgbot.Chat{Id: userID, Type: "private"},
				Text:      "card",
			},
			ChatInstance: "ci",
		},
	}
}
