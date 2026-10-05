package tg

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

const bbbHostPath = "bbb.ssau.ru/b/"

func extractBBBURL(text string) string {
	logx.Debugf("tg", "extractBBBURL: len=%d", len(text))
	lower := strings.ToLower(text)
	idx := strings.Index(lower, bbbHostPath)
	if idx < 0 {
		logx.Debugf("tg", "extractBBBURL: host not found")
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
		logx.Debugf("tg", "extractBBBURL: empty after trim")
		return ""
	}
	low := strings.ToLower(rest)
	if !strings.HasPrefix(low, "http://") && !strings.HasPrefix(low, "https://") {
		rest = "https://" + rest
	}
	logx.Debugf("tg", "extractBBBURL: %s", rest)
	return rest
}

func parseJoinCallback(data string) (yes bool, lessonID int64, ok bool) {
	logx.Debugf("tg", "parseJoinCallback: data=%q", data)
	parts := strings.Split(data, ":")
	if len(parts) != 3 || parts[0] != "j" {
		logx.Debugf("tg", "parseJoinCallback: malformed")
		return false, 0, false
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || id == 0 {
		logx.Debugf("tg", "parseJoinCallback: bad id err=%v", err)
		return false, 0, false
	}
	switch parts[1] {
	case "y":
		return true, id, true
	case "n":
		return false, id, true
	default:
		logx.Debugf("tg", "parseJoinCallback: bad verb %q", parts[1])
		return false, 0, false
	}
}

func joinCallbackData(yes bool, lessonID int64) string {
	logx.Debugf("tg", "joinCallbackData: yes=%v lesson=%d", yes, lessonID)
	if yes {
		return "j:y:" + strconv.FormatInt(lessonID, 10)
	}
	return "j:n:" + strconv.FormatInt(lessonID, 10)
}

func parseSubgroup(text string) (n int, ok bool) {
	logx.Debugf("tg", "parseSubgroup: text=%q", strings.TrimSpace(text))
	s := strings.TrimSpace(text)
	// Пустой текст — не ответ: кнопка меню или /settings на шаге подгруппы раньше
	// молча ставили подгруппу 1, и студент 2-й получал чужие пары и заходы.
	for _, tok := range strings.Fields(s) {
		tok = strings.Trim(tok, ".,")
		if tok == "1" {
			return 1, true
		}
		if tok == "2" {
			return 2, true
		}
	}
	logx.Debugf("tg", "parseSubgroup: no group in %q", s)
	return 0, false
}

// normalizeFIO: ФИО — имя в комнате BBB, его видят преподаватель и группа.
// Схлопываем пробелы и переводы строк; нужно 2–4 слова и не длиннее 80 символов.
func normalizeFIO(text string) (string, bool) {
	words := strings.Fields(text)
	if len(words) < 2 || len(words) > 4 {
		return "", false
	}
	fio := strings.Join(words, " ")
	if utf8.RuneCountInString(fio) > 80 {
		return "", false
	}
	return fio, true
}

func isCommandText(text string) bool {
	text = strings.TrimSpace(text)
	ok := strings.HasPrefix(text, "/")
	logx.Debugf("tg", "isCommandText: %q -> %v", text, ok)
	return ok
}

func pickLessonForBBB(now time.Time, subgroup int, lessons []model.Lesson, hasLink func(lessonID int64) bool, intents map[int64]time.Time) *model.Lesson {
	logx.Debugf("tg", "pickLessonForBBB: now=%s sub=%d lessons=%d intents=%d", now.Format(time.RFC3339), subgroup, len(lessons), len(intents))
	var live *model.Lesson
	for i := range lessons {
		l := lessons[i]
		if !l.Online || !l.MatchesSubgroup(subgroup) {
			continue
		}
		if l.Begin.IsZero() || l.Finish.IsZero() {
			continue
		}
		if now.Before(l.Begin) || !now.Before(l.Finish) {
			continue
		}
		if live == nil || l.Begin.After(live.Begin) {
			cp := l
			live = &cp
		}
	}
	if live != nil {
		logx.Debugf("tg", "pickLessonForBBB: live lesson=%d", live.ID)
		return live
	}

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
		logx.Debugf("tg", "pickLessonForBBB: nearest lesson=%d", nearest.ID)
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
	if best != nil {
		logx.Debugf("tg", "pickLessonForBBB: intent lesson=%d at=%s", best.ID, bestAt.Format(time.RFC3339))
	} else {
		logx.Debugf("tg", "pickLessonForBBB: no target")
	}
	return best
}
