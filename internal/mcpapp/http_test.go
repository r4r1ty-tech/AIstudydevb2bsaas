package mcpapp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const testBearer = "test-bearer-token-3f9c"

// newHTTPStack поднимает MCP-сервер поверх httptest с имитацией Moodle.
// Возвращает адрес HTTP-сервера и буфер, куда пишет логгер HTTP-слоя.
func newHTTPStack(t *testing.T, opts HTTPOptions) (*httptest.Server, *bytes.Buffer) {
	t.Helper()
	fake := newFakeMoodle()
	moodleSrv := httptest.NewServer(fake.handler())
	t.Cleanup(moodleSrv.Close)

	cfg := testConfig(t, moodleSrv.URL)
	cfg.Login, cfg.Password = testLogin, testPassword
	sess, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}

	logBuf := &bytes.Buffer{}
	opts.Logger = slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if opts.Addr == "" {
		opts.Addr = "127.0.0.1:0"
	}
	handler, err := NewHTTPHandler(NewServer(sess), opts)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, logBuf
}

// doRPC отправляет один JSON-RPC запрос на /mcp.
func doRPC(t *testing.T, base, token string, payload any, headers map[string]string) (int, http.Header, []byte) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, base+DefaultHTTPPath, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, body
}

func initializePayload(protocolVersion string) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "http-test", "version": "0"},
		},
	}
}

type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func decodeRPC(t *testing.T, body []byte) rpcEnvelope {
	t.Helper()
	var env rpcEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("ответ не JSON-RPC: %v (%s)", err, body)
	}
	if env.Error != nil {
		t.Fatalf("JSON-RPC ошибка %d: %s", env.Error.Code, env.Error.Message)
	}
	return env
}

// TestValidateHTTPOptionsTokenRequired: без токена сервер не должен слушать
// ничего, кроме петли.
func TestValidateHTTPOptionsTokenRequired(t *testing.T) {
	t.Setenv(EnvBearerToken, "")

	for _, addr := range []string{"0.0.0.0:8090", ":8090", "192.168.1.10:8090", "example.org:8090", "[::]:8090"} {
		opts := HTTPOptions{Addr: addr}
		if err := ValidateHTTPOptions(&opts); err == nil {
			t.Fatalf("адрес %q без токена должен быть отклонён", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:8090", "localhost:8090", "[::1]:8090"} {
		opts := HTTPOptions{Addr: addr}
		if err := ValidateHTTPOptions(&opts); err != nil {
			t.Fatalf("loopback-адрес %q: %v", addr, err)
		}
		if opts.Path != DefaultHTTPPath {
			t.Fatalf("path = %q, want %q", opts.Path, DefaultHTTPPath)
		}
	}

	// Тот же внешний адрес с токеном допустим.
	opts := HTTPOptions{Addr: "0.0.0.0:8090", BearerToken: testBearer}
	if err := ValidateHTTPOptions(&opts); err != nil {
		t.Fatalf("внешний адрес с токеном: %v", err)
	}

	// Токен читается из окружения.
	opts = HTTPOptions{Addr: "0.0.0.0:8090"}
	t.Setenv(EnvBearerToken, testBearer)
	if err := ValidateHTTPOptions(&opts); err != nil {
		t.Fatalf("токен из env: %v", err)
	}
	if opts.BearerToken != testBearer {
		t.Fatal("токен из env не подхвачен")
	}

	// Пробелы в токене — ошибка, без вывода самого токена.
	opts = HTTPOptions{Addr: "127.0.0.1:8090", BearerToken: "bad token"}
	if err := ValidateHTTPOptions(&opts); err == nil {
		t.Fatal("токен с пробелом должен быть отклонён")
	} else if strings.Contains(err.Error(), "bad token") {
		t.Fatal("значение токена попало в текст ошибки")
	}

	// Неизвестный транспортный путь и зарезервированный healthz.
	opts = HTTPOptions{Addr: "127.0.0.1:8090", Path: HealthPath}
	if err := ValidateHTTPOptions(&opts); err == nil {
		t.Fatal("path /healthz должен быть зарезервирован")
	}
	opts = HTTPOptions{Addr: "127.0.0.1:8090", Path: "/"}
	if err := ValidateHTTPOptions(&opts); err == nil {
		t.Fatal("корневой path должен быть отклонён")
	}
	opts = HTTPOptions{Addr: "127.0.0.1:8090", Path: "mcp/"}
	if err := ValidateHTTPOptions(&opts); err != nil {
		t.Fatalf("нормализация пути: %v", err)
	}
	if opts.Path != DefaultHTTPPath {
		t.Fatalf("path = %q, want %q", opts.Path, DefaultHTTPPath)
	}
}

// TestHTTPHandlerBearerAuth проверяет 401 без токена и с неверным токеном,
// успешный JSON-RPC с верным и healthz без секретов.
func TestHTTPHandlerBearerAuth(t *testing.T) {
	srv, logs := newHTTPStack(t, HTTPOptions{BearerToken: testBearer, JSONResponse: true})
	payload := initializePayload("2025-06-18")
	headers := map[string]string{"Mcp-Protocol-Version": "2025-06-18"}

	code, hdr, body := doRPC(t, srv.URL, "", payload, headers)
	if code != http.StatusUnauthorized {
		t.Fatalf("без токена: код %d, want 401 (%s)", code, body)
	}
	if got := hdr.Get("WWW-Authenticate"); !strings.HasPrefix(got, "Bearer") {
		t.Fatalf("WWW-Authenticate = %q", got)
	}

	code, _, body = doRPC(t, srv.URL, "wrong-token", payload, headers)
	if code != http.StatusUnauthorized {
		t.Fatalf("неверный токен: код %d, want 401 (%s)", code, body)
	}
	if bytes.Contains(body, []byte(testBearer)) {
		t.Fatal("токен попал в тело 401")
	}

	code, hdr, body = doRPC(t, srv.URL, testBearer, payload, headers)
	if code != http.StatusOK {
		t.Fatalf("верный токен: код %d, want 200 (%s)", code, body)
	}
	if ct := hdr.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	env := decodeRPC(t, body)
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(env.Result, &init); err != nil {
		t.Fatal(err)
	}
	if init.ProtocolVersion != "2025-06-18" {
		t.Fatalf("protocolVersion = %q, want 2025-06-18", init.ProtocolVersion)
	}
	if init.ServerInfo.Name != "ssau-mcp" {
		t.Fatalf("serverInfo.name = %q", init.ServerInfo.Name)
	}
	if hdr.Get("Mcp-Session-Id") != "" {
		t.Fatalf("stateless-сервер вернул Mcp-Session-Id: %q", hdr.Get("Mcp-Session-Id"))
	}

	// healthz: без токена, без секретов, без лишних заголовков.
	resp, err := http.Get(srv.URL + HealthPath)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	healthBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz: код %d", resp.StatusCode)
	}
	if string(healthBody) != `{"status":"ok"}` {
		t.Fatalf("healthz тело = %q", healthBody)
	}
	if bytes.Contains(healthBody, []byte(testBearer)) {
		t.Fatal("healthz отдал токен")
	}

	// Неизвестный путь — 404, а не MCP.
	resp, err = http.Get(srv.URL + "/nope")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("неизвестный путь: код %d, want 404", resp.StatusCode)
	}

	// Токен не должен оказаться ни в одном логе.
	if strings.Contains(logs.String(), testBearer) {
		t.Fatalf("токен попал в лог: %s", logs.String())
	}
}

// TestHTTPHandlerOriginAndRebinding: браузерные запросы с чужим Origin и
// подменённый Host отклоняются.
func TestHTTPHandlerOriginAndRebinding(t *testing.T) {
	srv, _ := newHTTPStack(t, HTTPOptions{BearerToken: testBearer, JSONResponse: true})
	payload := initializePayload("2025-06-18")
	legacy := map[string]string{"Mcp-Protocol-Version": "2025-06-18"}

	cases := []struct {
		name    string
		headers map[string]string
		host    string
		want    int
	}{
		{"чужой origin", map[string]string{"Origin": "https://evil.example"}, "", http.StatusForbidden},
		{"origin null", map[string]string{"Origin": "null"}, "", http.StatusForbidden},
		{"cross-site без origin", map[string]string{"Sec-Fetch-Site": "cross-site"}, "", http.StatusForbidden},
		{"localhost origin", map[string]string{"Origin": "http://localhost:5173"}, "", http.StatusOK},
		{"127.0.0.1 origin", map[string]string{"Origin": "http://127.0.0.1:8090"}, "", http.StatusOK},
		{"подмена host", nil, "evil.example", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			headers := map[string]string{}
			for k, v := range legacy {
				headers[k] = v
			}
			for k, v := range tc.headers {
				headers[k] = v
			}
			raw, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			req, err := http.NewRequest(http.MethodPost, srv.URL+DefaultHTTPPath, bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			req.Header.Set("Authorization", "Bearer "+testBearer)
			for k, v := range headers {
				req.Header.Set(k, v)
			}
			if tc.host != "" {
				req.Host = tc.host
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, resp.Body)
			if resp.StatusCode != tc.want {
				t.Fatalf("код %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
}

// TestHTTPHandlerExtraOrigins: дополнительные источники из ExtraOrigins
// разрешены, остальные по-прежнему отклоняются.
func TestHTTPHandlerExtraOrigins(t *testing.T) {
	srv, _ := newHTTPStack(t, HTTPOptions{
		BearerToken:  testBearer,
		JSONResponse: true,
		ExtraOrigins: []string{"https://app.example.org"},
	})
	headers := map[string]string{
		"Mcp-Protocol-Version": "2025-06-18",
		"Origin":               "https://app.example.org",
	}
	code, _, body := doRPC(t, srv.URL, testBearer, initializePayload("2025-06-18"), headers)
	if code != http.StatusOK {
		t.Fatalf("разрешённый origin: код %d (%s)", code, body)
	}

	headers["Origin"] = "https://other.example.org"
	code, _, _ = doRPC(t, srv.URL, testBearer, initializePayload("2025-06-18"), headers)
	if code != http.StatusForbidden {
		t.Fatalf("чужой origin: код %d, want 403", code)
	}
}

// doRaw отправляет запрос с произвольным набором заголовков (включая
// повторяющиеся, например два Authorization).
func doRaw(t *testing.T, method, url string, body []byte, headers [][2]string) (int, http.Header, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, kv := range headers {
		req.Header.Add(kv[0], kv[1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, raw
}

// TestHTTPHandlerMethods: stateless-сервер принимает только POST на /mcp.
func TestHTTPHandlerMethods(t *testing.T) {
	srv, _ := newHTTPStack(t, HTTPOptions{BearerToken: testBearer, JSONResponse: true})
	auth := [2]string{"Authorization", "Bearer " + testBearer}

	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		code, hdr, body := doRaw(t, method, srv.URL+DefaultHTTPPath, nil, [][2]string{auth})
		if code != http.StatusMethodNotAllowed {
			t.Fatalf("%s /mcp: код %d, want 405 (%s)", method, code, body)
		}
		if allow := hdr.Get("Allow"); allow != http.MethodPost {
			t.Fatalf("%s /mcp: Allow = %q, want POST", method, allow)
		}
	}
}

// TestHTTPHandlerBodyLimit: тело больше лимита отклоняется с 413.
func TestHTTPHandlerBodyLimit(t *testing.T) {
	srv, _ := newHTTPStack(t, HTTPOptions{BearerToken: testBearer, JSONResponse: true})
	oversize := bytes.Repeat([]byte("a"), defaultMaxRequestBodyBytes+1)
	code, _, body := doRaw(t, http.MethodPost, srv.URL+DefaultHTTPPath, oversize, [][2]string{
		{"Authorization", "Bearer " + testBearer},
		{"Content-Type", "application/json"},
		{"Accept", "application/json, text/event-stream"},
	})
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("код %d, want 413 (%s)", code, body)
	}
}

// TestHTTPHandlerBearerVariants: схема Bearer не зависит от регистра, а
// дублирующийся Authorization отклоняется даже с верным токеном.
func TestHTTPHandlerBearerVariants(t *testing.T) {
	srv, _ := newHTTPStack(t, HTTPOptions{BearerToken: testBearer, JSONResponse: true})
	raw, err := json.Marshal(initializePayload("2025-06-18"))
	if err != nil {
		t.Fatal(err)
	}
	payloadHeaders := [][2]string{
		{"Content-Type", "application/json"},
		{"Accept", "application/json, text/event-stream"},
		{"Mcp-Protocol-Version", "2025-06-18"},
	}

	for _, scheme := range []string{"bearer", "bEaReR"} {
		headers := append([][2]string{{"Authorization", scheme + " " + testBearer}}, payloadHeaders...)
		code, _, body := doRaw(t, http.MethodPost, srv.URL+DefaultHTTPPath, raw, headers)
		if code != http.StatusOK {
			t.Fatalf("схема %q: код %d, want 200 (%s)", scheme, code, body)
		}
	}

	// Два одинаково верных заголовка — всё равно 401.
	headers := append([][2]string{
		{"Authorization", "Bearer " + testBearer},
		{"Authorization", "Bearer " + testBearer},
	}, payloadHeaders...)
	code, _, body := doRaw(t, http.MethodPost, srv.URL+DefaultHTTPPath, raw, headers)
	if code != http.StatusUnauthorized {
		t.Fatalf("дублирующийся Authorization: код %d, want 401 (%s)", code, body)
	}
}

// authRoundTripper добавляет Bearer-токен и запоминает заголовки ответов.
type authRoundTripper struct {
	base http.RoundTripper
	mu   sync.Mutex
	hdrs []http.Header
}

func (rt *authRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+testBearer)
	resp, err := rt.base.RoundTrip(clone)
	if err == nil {
		rt.mu.Lock()
		rt.hdrs = append(rt.hdrs, resp.Header.Clone())
		rt.mu.Unlock()
	}
	return resp, err
}

func (rt *authRoundTripper) headers() []http.Header {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return append([]http.Header(nil), rt.hdrs...)
}

// TestHTTPHandlerToolsOverHTTP — полный текущий протокол (2026-07-28) через
// Streamable HTTP-клиент: discover, tools/list, tools/call, без Mcp-Session-Id.
func TestHTTPHandlerToolsOverHTTP(t *testing.T) {
	srv, logs := newHTTPStack(t, HTTPOptions{BearerToken: testBearer})
	rt := &authRoundTripper{base: http.DefaultTransport}
	transport := &mcp.StreamableClientTransport{
		Endpoint:   srv.URL + DefaultHTTPPath,
		HTTPClient: &http.Client{Transport: rt},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "http-test", Version: "0"}, nil).
		Connect(ctx, transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	if got := cs.InitializeResult().ProtocolVersion; got != "2026-07-28" {
		t.Fatalf("protocolVersion = %q, want 2026-07-28", got)
	}

	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range list.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"moodle_status", "moodle_courses"} {
		if !names[want] {
			t.Fatalf("в tools/list нет %q", want)
		}
	}

	statusRes, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "moodle_status"})
	if err != nil {
		t.Fatal(err)
	}
	var status StatusOutput
	decodeStructured(t, statusRes, &status)
	if !status.Authenticated || status.Login != testLogin {
		t.Fatalf("status = %+v", status)
	}

	coursesRes, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "moodle_courses",
		Arguments: map[string]any{"limit": 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	var courses CoursesOutput
	decodeStructured(t, coursesRes, &courses)
	if courses.Total != 5 || len(courses.Courses) != 2 {
		t.Fatalf("courses = %+v", courses)
	}

	hdrs := rt.headers()
	if len(hdrs) == 0 {
		t.Fatal("не записано ни одного HTTP-ответа")
	}
	for _, h := range hdrs {
		if id := h.Get("Mcp-Session-Id"); id != "" {
			t.Fatalf("stateless-сервер вернул Mcp-Session-Id: %q", id)
		}
	}
	if strings.Contains(logs.String(), testBearer) {
		t.Fatal("токен попал в лог")
	}
}

// TestHTTPHandlerLegacyProtocol — старый протокол (2025-06-18) по сырому HTTP:
// initialize, tools/list, tools/call без Mcp-Session-Id.
func TestHTTPHandlerLegacyProtocol(t *testing.T) {
	srv, _ := newHTTPStack(t, HTTPOptions{BearerToken: testBearer, JSONResponse: true})
	headers := map[string]string{"Mcp-Protocol-Version": "2025-06-18"}

	code, _, body := doRPC(t, srv.URL, testBearer, initializePayload("2025-06-18"), headers)
	if code != http.StatusOK {
		t.Fatalf("initialize: код %d (%s)", code, body)
	}
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(decodeRPC(t, body).Result, &init); err != nil {
		t.Fatal(err)
	}
	if init.ProtocolVersion != "2025-06-18" {
		t.Fatalf("protocolVersion = %q", init.ProtocolVersion)
	}

	code, hdr, body := doRPC(t, srv.URL, testBearer, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{},
	}, headers)
	if code != http.StatusOK {
		t.Fatalf("tools/list: код %d (%s)", code, body)
	}
	if hdr.Get("Mcp-Session-Id") != "" {
		t.Fatal("legacy stateless вернул Mcp-Session-Id")
	}
	var list struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(decodeRPC(t, body).Result, &list); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range list.Tools {
		if tool.Name == "moodle_courses" {
			found = true
		}
	}
	if !found {
		t.Fatalf("в tools/list нет moodle_courses: %+v", list.Tools)
	}

	code, _, body = doRPC(t, srv.URL, testBearer, map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{"name": "moodle_courses", "arguments": map[string]any{"limit": 1}},
	}, headers)
	if code != http.StatusOK {
		t.Fatalf("tools/call: код %d (%s)", code, body)
	}
	var call struct {
		StructuredContent CoursesOutput `json:"structuredContent"`
		IsError           bool          `json:"isError"`
	}
	if err := json.Unmarshal(decodeRPC(t, body).Result, &call); err != nil {
		t.Fatal(err)
	}
	if call.IsError {
		t.Fatalf("инструмент вернул ошибку: %s", body)
	}
	if call.StructuredContent.Total != 5 || len(call.StructuredContent.Courses) != 1 {
		t.Fatalf("courses = %+v", call.StructuredContent)
	}
}

// TestHTTPHandlerNewProtocolRaw: новый протокол требует заголовок
// Mcp-Protocol-Version и _meta с версией; server/discover отвечает.
func TestHTTPHandlerNewProtocolRaw(t *testing.T) {
	srv, _ := newHTTPStack(t, HTTPOptions{BearerToken: testBearer, JSONResponse: true})
	meta := map[string]any{
		"io.modelcontextprotocol/protocolVersion":    "2026-07-28",
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}
	headers := map[string]string{
		"Mcp-Protocol-Version": "2026-07-28",
		"Mcp-Method":           "server/discover",
	}

	code, _, body := doRPC(t, srv.URL, testBearer, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "server/discover", "params": map[string]any{"_meta": meta},
	}, headers)
	if code != http.StatusOK {
		t.Fatalf("server/discover: код %d (%s)", code, body)
	}
	var discover struct {
		SupportedVersions []string `json:"supportedVersions"`
	}
	if err := json.Unmarshal(decodeRPC(t, body).Result, &discover); err != nil {
		t.Fatal(err)
	}
	if len(discover.SupportedVersions) == 0 || discover.SupportedVersions[0] != "2026-07-28" {
		t.Fatalf("supportedVersions = %+v", discover.SupportedVersions)
	}

	// Запрос с _meta, но без заголовка версии — отказ.
	code, _, _ = doRPC(t, srv.URL, testBearer, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{"_meta": meta},
	}, map[string]string{"Mcp-Method": "tools/list"})
	if code != http.StatusBadRequest {
		t.Fatalf("без заголовка версии: код %d, want 400", code)
	}

	// tools/call в новом протоколе требует Mcp-Name.
	code, _, body = doRPC(t, srv.URL, testBearer, map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{"name": "moodle_status", "arguments": map[string]any{}, "_meta": meta},
	}, map[string]string{
		"Mcp-Protocol-Version": "2026-07-28",
		"Mcp-Method":           "tools/call",
		"Mcp-Name":             "moodle_status",
	})
	if code != http.StatusOK {
		t.Fatalf("tools/call: код %d (%s)", code, body)
	}
	var call struct {
		StructuredContent StatusOutput `json:"structuredContent"`
	}
	if err := json.Unmarshal(decodeRPC(t, body).Result, &call); err != nil {
		t.Fatal(err)
	}
	if !call.StructuredContent.Authenticated {
		t.Fatalf("status = %+v", call.StructuredContent)
	}
}

// TestHTTPHandlerNoTokenLoopback: на loopback без токена сервер работает,
// а healthz не требует аутентификации.
func TestHTTPHandlerNoTokenLoopback(t *testing.T) {
	t.Setenv(EnvBearerToken, "")
	srv, _ := newHTTPStack(t, HTTPOptions{JSONResponse: true})

	code, _, body := doRPC(t, srv.URL, "", initializePayload("2025-06-18"),
		map[string]string{"Mcp-Protocol-Version": "2025-06-18"})
	if code != http.StatusOK {
		t.Fatalf("loopback без токена: код %d (%s)", code, body)
	}
}

// TestNewHTTPServerTimeouts: таймауты заданы явно и точно.
func TestNewHTTPServerTimeouts(t *testing.T) {
	srv := NewHTTPServer(nil, HTTPOptions{Addr: DefaultHTTPAddr})
	if srv.ReadHeaderTimeout != 10*time.Second {
		t.Fatalf("ReadHeaderTimeout = %s, want 10s", srv.ReadHeaderTimeout)
	}
	if srv.ReadTimeout != 30*time.Second {
		t.Fatalf("ReadTimeout = %s, want 30s", srv.ReadTimeout)
	}
	if srv.WriteTimeout != 5*time.Minute {
		t.Fatalf("WriteTimeout = %s, want 5m", srv.WriteTimeout)
	}
	if srv.IdleTimeout != 2*time.Minute {
		t.Fatalf("IdleTimeout = %s, want 2m", srv.IdleTimeout)
	}
	if srv.MaxHeaderBytes != 64<<10 {
		t.Fatalf("MaxHeaderBytes = %d, want %d", srv.MaxHeaderBytes, 64<<10)
	}
	if srv.WriteTimeout < DefaultTimeout {
		t.Fatalf("WriteTimeout %s меньше таймаута инструмента %s", srv.WriteTimeout, DefaultTimeout)
	}
}
