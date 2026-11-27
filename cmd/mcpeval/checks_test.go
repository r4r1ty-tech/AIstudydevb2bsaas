package main

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLookupPath(t *testing.T) {
	root := map[string]any{
		"courses": []any{
			map[string]any{"id": float64(1515), "name": "Теория информации"},
		},
		"total": float64(1),
	}

	tests := []struct {
		path string
		want any
		ok   bool
	}{
		{"total", float64(1), true},
		{"courses.0.id", float64(1515), true},
		{"courses.0.name", "Теория информации", true},
		{"courses.1.id", nil, false},
		{"nope", nil, false},
		{"courses.0.id.extra", nil, false},
		{"", root, true},
	}
	for _, tt := range tests {
		got, ok := lookupPath(root, tt.path)
		if ok != tt.ok {
			t.Fatalf("lookupPath(%q) ok=%v, want %v", tt.path, ok, tt.ok)
		}
		if ok && !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("lookupPath(%q)=%v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestEvalCheck(t *testing.T) {
	payload := map[string]any{
		"authenticated": true,
		"total":         float64(5),
		"text":          "Специальный курс по Python",
		"names":         []any{"moodle_status", "moodle_courses"},
	}

	ok := []check{
		{Path: "authenticated", Equals: true},
		{Path: "total", Min: ptrFloat(3)},
		{Path: "text", Contains: "python"},
		{Path: "names", Contains: "moodle_courses"},
		{Path: "text", NotContains: "Java"},
		{Path: "text", Regex: `Python$`},
		{Path: "names", MinCount: ptrInt(2)},
	}
	for i := range ok {
		if err := evalCheck(ok[i], payload); err != nil {
			t.Fatalf("проверка %d должна пройти: %v", i+1, err)
		}
	}

	bad := []check{
		{Path: "authenticated", Equals: false},
		{Path: "total", Min: ptrFloat(6)},
		{Path: "text", Contains: "Java"},
		{Path: "missing", NonEmpty: true},
		{Path: "total", Max: ptrFloat(4)},
		{Path: "names", MinCount: ptrInt(3)},
	}
	for i := range bad {
		if err := evalCheck(bad[i], payload); err == nil {
			t.Fatalf("проверка %d должна провалиться", i+1)
		}
	}
}

func TestEvalCheckExists(t *testing.T) {
	payload := map[string]any{"items": []any{}}
	yes, no := true, false
	if err := evalCheck(check{Path: "items", Exists: &yes}, payload); err != nil {
		t.Fatalf("items должен существовать: %v", err)
	}
	if err := evalCheck(check{Path: "missing", Exists: &no}, payload); err != nil {
		t.Fatalf("missing не должен существовать: %v", err)
	}
}

func TestLatencyAndTokens(t *testing.T) {
	got := latency([]float64{10, 20, 30, 40, 50})
	if got.P50 != 30 || got.P95 != 50 || got.Total != 150 {
		t.Fatalf("latency=%+v", got)
	}
	if estTokens(0) != 0 || estTokens(1) != 1 || estTokens(8) != 2 {
		t.Fatalf("estTokens неверен")
	}
}

func TestCompactJSONKeepsUTF8(t *testing.T) {
	in := strings.Repeat("энтропия ", 40)
	got := compactJSON(in)
	if !utf8.ValidString(got) {
		t.Fatalf("compactJSON разрезал UTF-8: %q", got)
	}
	if n := len([]rune(got)); n != 121 {
		t.Fatalf("ожидалось 120 рун и многоточие, получено %d", n)
	}
}

func TestTaskFileValidate(t *testing.T) {
	tf := &taskFile{Tasks: []task{{ID: "a", Steps: []step{{Kind: "tool", Tool: "moodle_status"}}}}}
	if err := tf.validate(); err != nil {
		t.Fatalf("валидный файл: %v", err)
	}
	dup := &taskFile{Tasks: []task{
		{ID: "a", Steps: []step{{Kind: "tool", Tool: "moodle_status"}}},
		{ID: "a", Steps: []step{{Kind: "tool", Tool: "moodle_status"}}},
	}}
	if err := dup.validate(); err == nil {
		t.Fatal("ожидалась ошибка повторяющегося id")
	}
	bad := &taskFile{Tasks: []task{{ID: "a", Steps: []step{{Kind: "tool"}}}}}
	if err := bad.validate(); err == nil {
		t.Fatal("ожидалась ошибка пустого tool")
	}
}

func ptrFloat(v float64) *float64 { return &v }
func ptrInt(v int) *int           { return &v }
