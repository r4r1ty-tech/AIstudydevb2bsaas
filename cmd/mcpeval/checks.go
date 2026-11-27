package main

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

// lookupPath ищет значение по точечному пути в JSON-совместимой структуре.
// Поддерживаются ключи объектов и числовые индексы массивов: courses.0.name.
func lookupPath(root any, path string) (any, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return root, true
	}
	cur := root
	for _, token := range strings.Split(path, ".") {
		if token == "" {
			continue
		}
		switch v := cur.(type) {
		case map[string]any:
			next, ok := v[token]
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			idx, err := strconv.Atoi(token)
			if err != nil || idx < 0 || idx >= len(v) {
				return nil, false
			}
			cur = v[idx]
		default:
			return nil, false
		}
	}
	return cur, true
}

// evalCheck применяет одно условие и возвращает nil, если оно выполнено.
func evalCheck(c check, payload any) error {
	value, found := lookupPath(payload, c.Path)
	if c.Exists != nil {
		if !*c.Exists {
			if found {
				return fmt.Errorf("путь %q не должен существовать", c.Path)
			}
			return nil
		}
		if !found {
			return fmt.Errorf("путь %q не найден", c.Path)
		}
	}
	if !found {
		return fmt.Errorf("путь %q не найден", c.Path)
	}
	if c.Equals != nil && !jsonEqual(value, c.Equals) {
		return fmt.Errorf("%s: получено %s, ожидалось %s", displayPath(c.Path), compactJSON(value), compactJSON(c.Equals))
	}
	if len(c.OneOf) > 0 {
		ok := false
		for _, want := range c.OneOf {
			if jsonEqual(value, want) {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("%s: %s не входит в one_of", displayPath(c.Path), compactJSON(value))
		}
	}
	if c.NonEmpty && isEmpty(value) {
		return fmt.Errorf("%s: пусто", displayPath(c.Path))
	}
	if c.Contains != "" {
		if !valueContains(value, c.Contains) {
			return fmt.Errorf("%s: нет подстроки %q", displayPath(c.Path), c.Contains)
		}
	}
	if c.NotContains != "" {
		if valueContains(value, c.NotContains) {
			return fmt.Errorf("%s: содержит запрещённую подстроку %q", displayPath(c.Path), c.NotContains)
		}
	}
	if c.Regex != "" {
		s, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s: не строка, regex неприменим", displayPath(c.Path))
		}
		re, err := regexp.Compile(c.Regex)
		if err != nil {
			return fmt.Errorf("%s: regexp: %w", displayPath(c.Path), err)
		}
		if !re.MatchString(s) {
			return fmt.Errorf("%s: не совпало с regexp %q", displayPath(c.Path), c.Regex)
		}
	}
	if c.Min != nil || c.Max != nil {
		n, ok := number(value)
		if !ok {
			return fmt.Errorf("%s: не число", displayPath(c.Path))
		}
		if c.Min != nil && n < *c.Min {
			return fmt.Errorf("%s: %v меньше min %v", displayPath(c.Path), n, *c.Min)
		}
		if c.Max != nil && n > *c.Max {
			return fmt.Errorf("%s: %v больше max %v", displayPath(c.Path), n, *c.Max)
		}
	}
	if c.MinCount != nil {
		n := count(value)
		if n < *c.MinCount {
			return fmt.Errorf("%s: элементов %d, нужно минимум %d", displayPath(c.Path), n, *c.MinCount)
		}
	}
	return nil
}

// valueContains ищет подстроку в строке, массиве строк или значениях объекта.
func valueContains(v any, needle string) bool {
	needle = strings.ToLower(needle)
	switch t := v.(type) {
	case string:
		return strings.Contains(strings.ToLower(t), needle)
	case []any:
		for _, item := range t {
			if valueContains(item, needle) {
				return true
			}
		}
		return false
	case map[string]any:
		for _, item := range t {
			if valueContains(item, needle) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// isEmpty — пустая строка (после TrimSpace), пустой массив/объект или null.
func isEmpty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(t) == ""
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	default:
		return false
	}
}

func count(v any) int {
	switch t := v.(type) {
	case []any:
		return len(t)
	case map[string]any:
		return len(t)
	case string:
		return len([]rune(t))
	default:
		return 0
	}
}

// number приводит число из JSON к float64. json.Decoder по умолчанию даёт
// float64, но тесты и ручные значения могут прийти как int.
func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

// jsonEqual сравнивает значения как JSON: числа — по значению, остальное —
// через reflect.DeepEqual после нормализации.
func jsonEqual(a, b any) bool {
	if na, ok := number(a); ok {
		if nb, ok := number(b); ok {
			return math.Abs(na-nb) < 1e-9
		}
	}
	na, errA := normalizeJSON(a)
	nb, errB := normalizeJSON(b)
	if errA != nil || errB != nil {
		return false
	}
	return reflect.DeepEqual(na, nb)
}

// normalizeJSON прогоняет значение через JSON, чтобы map[string]any и
// типизированные структуры сравнивались одинаково.
func normalizeJSON(v any) (any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func compactJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	const limit = 120
	s := string(raw)
	runes := []rune(s)
	if len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return s
}

func displayPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return "(ответ)"
	}
	return path
}
