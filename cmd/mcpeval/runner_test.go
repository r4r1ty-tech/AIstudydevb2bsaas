package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type valueIn struct {
	Value string `json:"value"`
}

type valueOut struct {
	Value string `json:"value"`
}

// mockServer собирает сервер с успешным инструментом, инструментом с
// IsError=true, медленным инструментом и падающим ресурсом.
func mockServer() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "mock-mcp", Version: "test"}, nil)

	mcp.AddTool(srv, &mcp.Tool{Name: "ok"}, func(_ context.Context, _ *mcp.CallToolRequest, in valueIn) (*mcp.CallToolResult, valueOut, error) {
		return nil, valueOut{Value: in.Value}, nil
	})
	mcp.AddTool(srv, &mcp.Tool{Name: "is_error"}, func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, valueOut, error) {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: "boom"}},
		}, valueOut{}, nil
	})
	mcp.AddTool(srv, &mcp.Tool{Name: "slow"}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, valueOut, error) {
		select {
		case <-ctx.Done():
			return nil, valueOut{}, ctx.Err()
		case <-time.After(2 * time.Second):
			return nil, valueOut{Value: "late"}, nil
		}
	})
	srv.AddResource(&mcp.Resource{URI: "mock://fail", Name: "fail", MIMEType: "text/plain"},
		func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return nil, errors.New("resource boom")
		})
	return srv
}

// newInMemoryRunner подключает клиент к mock-серверу без сети.
func newInMemoryRunner(t *testing.T) *runner {
	t.Helper()
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := mockServer().Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return &runner{cfg: runConfig{StepTimeout: time.Second}, session: cs}
}

func TestRunStepSuccess(t *testing.T) {
	r := newInMemoryRunner(t)
	res := r.runStep(context.Background(), &step{
		Kind:      "tool",
		Tool:      "ok",
		Arguments: map[string]any{"value": "hi"},
		Checks:    []check{{Path: "value", Equals: "hi"}},
	}, time.Second)

	if !res.OK {
		t.Fatalf("шаг должен пройти: %s", res.Error)
	}
	if len(res.Checks) != 1 || !res.Checks[0].OK {
		t.Fatalf("проверка не прошла: %+v", res.Checks)
	}
	if res.EstTokens == 0 {
		t.Fatal("ожидалась ненулевая оценка токенов")
	}
}

func TestRunStepIsError(t *testing.T) {
	r := newInMemoryRunner(t)
	res := r.runStep(context.Background(), &step{Kind: "tool", Tool: "is_error"}, time.Second)

	if res.OK {
		t.Fatal("IsError=true должен проваливать шаг")
	}
	if !strings.Contains(res.Error, "boom") {
		t.Fatalf("ошибка не содержит текст инструмента: %q", res.Error)
	}
}

func TestRunStepTimeout(t *testing.T) {
	r := newInMemoryRunner(t)
	start := time.Now()
	res := r.runStep(context.Background(), &step{Kind: "tool", Tool: "slow"}, 50*time.Millisecond)

	if res.OK {
		t.Fatal("таймаут должен проваливать шаг")
	}
	if res.Error == "" {
		t.Fatal("ожидалась ошибка таймаута")
	}
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Fatalf("таймаут не сработал: %s", elapsed)
	}
}

func TestRunStepResourceError(t *testing.T) {
	r := newInMemoryRunner(t)
	res := r.runStep(context.Background(), &step{Kind: "resource", URI: "mock://fail"}, time.Second)

	if res.OK {
		t.Fatal("ошибка ресурса должна проваливать шаг")
	}
	if !strings.Contains(res.Error, "resource boom") {
		t.Fatalf("неожиданная ошибка: %q", res.Error)
	}
}

func TestRunStepUnknownTool(t *testing.T) {
	r := newInMemoryRunner(t)
	res := r.runStep(context.Background(), &step{Kind: "tool", Tool: "nope"}, time.Second)

	if res.OK {
		t.Fatal("неизвестный инструмент должен проваливать шаг")
	}
	if res.Error == "" {
		t.Fatal("ожидалась ошибка вызова")
	}
}

func TestRunStepCheckFailure(t *testing.T) {
	r := newInMemoryRunner(t)
	res := r.runStep(context.Background(), &step{
		Kind:      "tool",
		Tool:      "ok",
		Arguments: map[string]any{"value": "hi"},
		Checks:    []check{{Path: "value", Equals: "no"}},
	}, time.Second)

	if res.OK {
		t.Fatal("проваленная проверка должна проваливать шаг")
	}
	if len(res.Checks) != 1 || res.Checks[0].OK || res.Checks[0].Error == "" {
		t.Fatalf("нет ошибки проверки: %+v", res.Checks)
	}
}

// newHTTPServer поднимает Streamable HTTP поверх mock-сервера.
func newHTTPServer(t *testing.T, srv *mcp.Server) *httptest.Server {
	t.Helper()
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts
}

func writeTasks(t *testing.T, tf *taskFile) string {
	t.Helper()
	raw, err := json.Marshal(tf)
	if err != nil {
		t.Fatalf("marshal tasks: %v", err)
	}
	path := filepath.Join(t.TempDir(), "tasks.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write tasks: %v", err)
	}
	return path
}

func readReport(t *testing.T, path string) *report {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	var rep report
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	return &rep
}

func TestRunWithArgsSuccess(t *testing.T) {
	ts := newHTTPServer(t, mockServer())
	tasks := writeTasks(t, &taskFile{
		RequiredTools: []string{"ok"},
		Tasks: []task{{ID: "ok", Steps: []step{{
			Kind: "tool", Tool: "ok", Arguments: map[string]any{"value": "hi"},
			Checks: []check{{Path: "value", Equals: "hi"}},
		}}}},
	})
	out := filepath.Join(t.TempDir(), "report.json")

	err := runWithArgs(context.Background(), []string{
		"-transport", "http", "-url", ts.URL, "-tasks", tasks, "-out", out, "-timeout", "2s",
	})
	if err != nil {
		t.Fatalf("успешный прогон вернул ошибку: %v", err)
	}
	rep := readReport(t, out)
	if rep.Summary.Passed != 1 || rep.Summary.Failed != 0 || !rep.InvariantsOK {
		t.Fatalf("неверная сводка: %+v", rep.Summary)
	}
}

func TestRunWithArgsFailedTask(t *testing.T) {
	ts := newHTTPServer(t, mockServer())
	tasks := writeTasks(t, &taskFile{
		RequiredTools: []string{"is_error"},
		Tasks: []task{{ID: "fail", Steps: []step{{
			Kind: "tool", Tool: "is_error",
		}}}},
	})
	out := filepath.Join(t.TempDir(), "report.json")

	err := runWithArgs(context.Background(), []string{
		"-transport", "http", "-url", ts.URL, "-tasks", tasks, "-out", out, "-timeout", "2s",
	})
	if err == nil {
		t.Fatal("проваленный прогон должен вернуть ошибку (ненулевой код возврата)")
	}
	rep := readReport(t, out)
	if rep.Summary.Failed != 1 || rep.Summary.Passed != 0 {
		t.Fatalf("неверная сводка: %+v", rep.Summary)
	}
}

func TestRunWithArgsInvariantFailure(t *testing.T) {
	ts := newHTTPServer(t, mockServer())
	tasks := writeTasks(t, &taskFile{
		RequiredTools: []string{"missing_tool"},
		Tasks: []task{{ID: "ok", Steps: []step{{
			Kind: "tool", Tool: "ok", Arguments: map[string]any{"value": "hi"},
		}}}},
	})
	out := filepath.Join(t.TempDir(), "report.json")

	err := runWithArgs(context.Background(), []string{
		"-transport", "http", "-url", ts.URL, "-tasks", tasks, "-out", out, "-timeout", "2s",
	})
	if err == nil {
		t.Fatal("проваленный инвариант должен возвращать ошибку")
	}
	rep := readReport(t, out)
	if rep.InvariantsOK {
		t.Fatal("invariants_ok должен быть false")
	}
	if len(rep.Invariants) != 1 || rep.Invariants[0].OK {
		t.Fatalf("нет проваленного инварианта: %+v", rep.Invariants)
	}
}
