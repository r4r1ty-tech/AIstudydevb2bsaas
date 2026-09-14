package tg

import (
	"strconv"
	"strings"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

const bbbHostPath = "bbb.ssau.ru/b/"

func extractBBBURL(text string) string {
	lower := strings.ToLower(text)
	idx := strings.Index(lower, bbbHostPath)
	if idx < 0 {
		return ""
	}
	start := idx
	if idx >= 8 && lower[idx-8:idx] == "https://" {
		start = idx - 8
	} else if idx >= 7 && lower[idx-7:idx] == "http://" {
		start = idx - 7
	}
	rest := strings.TrimSpace(text[start:])
	if i := strings.IndexAny(rest, " \t\n\r<>\"'"); i >= 0 {
		rest = rest[:i]
	}
	rest = strings.TrimRight(rest, ".,;)]»")
	if rest == "" {
		return ""
	}
	low := strings.ToLower(rest)
	if !strings.HasPrefix(low, "http://") && !strings.HasPrefix(low, "https://") {
		rest = "https://" + rest
	}
	return rest
}

func parseJoinCallback(data string) (yes bool, lessonID int64, ok bool) {
	parts := strings.Split(data, ":")
	if len(parts) != 3 || parts[0] != "j" {
		return false, 0, false
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || id == 0 {
		return false, 0, false
	}
	switch parts[1] {
	case "y":
		return true, id, true
	case "n":
		return false, id, true
	default:
		return false, 0, false
	}
}

func joinCallbackData(yes bool, lessonID int64) string {
	if yes {
		return "j:y:" + strconv.FormatInt(lessonID, 10)
	}
	return "j:n:" + strconv.FormatInt(lessonID, 10)
}

func parseSubgroup(text string) (n int, ok bool) {
	s := strings.TrimSpace(text)
	if s == "" || s == "-" || s == "—" || s == "." {
		return 1, true
	}
	for _, tok := range strings.Fields(s) {
		tok = strings.Trim(tok, ".,")
		if tok == "1" {
			return 1, true
		}
		if tok == "2" {
			return 2, true
		}
	}
	return 0, false
}

func isCommandText(text string) bool {
	text = strings.TrimSpace(text)
	return strings.HasPrefix(text, "/")
}

func pickLessonForBBB(now time.Time, subgroup int, lessons []model.Lesson, hasLink func(lessonID int64) bool, intents map[int64]time.Time) *model.Lesson {
	var nearest *model.Lesson
	for i := range lessons {
		l := lessons[i]
		if !l.Online || !l.Begin.After(now) || !l.MatchesSubgroup(subgroup) {
			continue
		}
		if hasLink != nil && hasLink(l.ID) {
			continue
		}
		if nearest == nil || l.Begin.Before(nearest.Begin) {
			cp := l
			nearest = &cp
		}
	}
	if nearest != nil {
		return nearest
	}
	var best *model.Lesson
	var bestAt time.Time
	for i := range lessons {
		at, ok := intents[lessons[i].ID]
		if !ok {
			continue
		}
		if best == nil || at.After(bestAt) {
			cp := lessons[i]
			best = &cp
			bestAt = at
		}
	}
	return best
}
