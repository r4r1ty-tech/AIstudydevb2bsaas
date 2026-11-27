// Command mcpeval — офлайн-клиент MCP-сервера Moodle: прогоняет read-only
// задачи из JSON, проверяет инварианты и печатает метрики с JSON-отчётом.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// taskFile — корневая структура evals/moodle_tasks.json.
type taskFile struct {
	Version         int      `json:"version"`
	Description     string   `json:"description"`
	RequiredTools   []string `json:"required_tools"`
	RequiredPrompts []string `json:"required_prompts"`
	RequiredRes     []string `json:"required_resources"`
	RequiredTpl     []string `json:"required_templates"`
	Tasks           []task   `json:"tasks"`
}

// task — одна read-only проверка. Может состоять из нескольких шагов: тогда
// задача считается пройденной, только если прошли все шаги.
type task struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	Steps          []step `json:"steps"`
}

// step — один вызов MCP: инструмент, ресурс или список.
type step struct {
	// Kind: tool, resource, prompts, resources или resource_templates.
	Kind      string         `json:"kind"`
	Tool      string         `json:"tool,omitempty"`
	Arguments map[string]any `json:"arguments,omitempty"`
	URI       string         `json:"uri,omitempty"`
	Checks    []check        `json:"checks,omitempty"`
}

// check — проверка значения по пути в структурированном ответе. Пустой путь
// означает весь ответ. Достаточно одного заполненного условия.
type check struct {
	Path        string   `json:"path"`
	Exists      *bool    `json:"exists,omitempty"`
	Equals      any      `json:"equals,omitempty"`
	OneOf       []any    `json:"one_of,omitempty"`
	NonEmpty    bool     `json:"non_empty,omitempty"`
	Contains    string   `json:"contains,omitempty"`
	NotContains string   `json:"not_contains,omitempty"`
	Regex       string   `json:"regex,omitempty"`
	Min         *float64 `json:"min,omitempty"`
	Max         *float64 `json:"max,omitempty"`
	MinCount    *int     `json:"min_count,omitempty"`
}

// loadTasks читает и валидирует файл задач.
func loadTasks(path string) (*taskFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("mcpeval: прочитать задачи %s: %w", path, err)
	}
	var tf taskFile
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&tf); err != nil {
		return nil, fmt.Errorf("mcpeval: разобрать задачи %s: %w", path, err)
	}
	if err := tf.validate(); err != nil {
		return nil, fmt.Errorf("mcpeval: задачи %s: %w", path, err)
	}
	return &tf, nil
}

func (tf *taskFile) validate() error {
	if len(tf.Tasks) == 0 {
		return fmt.Errorf("нет ни одной задачи")
	}
	seen := make(map[string]bool, len(tf.Tasks))
	for i := range tf.Tasks {
		t := &tf.Tasks[i]
		if strings.TrimSpace(t.ID) == "" {
			return fmt.Errorf("задача #%d без id", i+1)
		}
		if seen[t.ID] {
			return fmt.Errorf("повторяющийся id %q", t.ID)
		}
		seen[t.ID] = true
		if len(t.Steps) == 0 {
			return fmt.Errorf("задача %q без шагов", t.ID)
		}
		for j := range t.Steps {
			if err := t.Steps[j].validate(); err != nil {
				return fmt.Errorf("задача %q, шаг %d: %w", t.ID, j+1, err)
			}
		}
	}
	return nil
}

func (s *step) validate() error {
	switch s.Kind {
	case "tool":
		if strings.TrimSpace(s.Tool) == "" {
			return fmt.Errorf("kind=tool без tool")
		}
	case "resource":
		if strings.TrimSpace(s.URI) == "" {
			return fmt.Errorf("kind=resource без uri")
		}
	case "prompts", "resources", "resource_templates":
	default:
		return fmt.Errorf("неизвестный kind %q", s.Kind)
	}
	for i := range s.Checks {
		if err := s.Checks[i].validate(); err != nil {
			return fmt.Errorf("проверка %d: %w", i+1, err)
		}
	}
	return nil
}

func (c *check) validate() error {
	if c.Exists == nil && c.Equals == nil && len(c.OneOf) == 0 && !c.NonEmpty &&
		c.Contains == "" && c.NotContains == "" && c.Regex == "" && c.Min == nil &&
		c.Max == nil && c.MinCount == nil {
		return fmt.Errorf("пустая проверка для пути %q", c.Path)
	}
	if c.Regex != "" {
		if _, err := regexp.Compile(c.Regex); err != nil {
			return fmt.Errorf("regexp %q: %w", c.Regex, err)
		}
	}
	if c.Min != nil && c.Max != nil && *c.Min > *c.Max {
		return fmt.Errorf("min %v больше max %v", *c.Min, *c.Max)
	}
	return nil
}
