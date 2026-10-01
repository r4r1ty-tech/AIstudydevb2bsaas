package rasp

import (
	"fmt"
	"sort"
	"strings"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

func Diff(old, new []model.Lesson) string {
	logx.Debugf("rasp", "Diff: old=%d new=%d", len(old), len(new))
	oldSet := make(map[string]model.Lesson, len(old))
	newSet := make(map[string]model.Lesson, len(new))
	for _, l := range old {
		oldSet[l.Identity()] = l
	}
	for _, l := range new {
		newSet[l.Identity()] = l
	}

	var removed, added, changed []string
	for id, l := range oldSet {
		n, ok := newSet[id]
		if !ok {
			removed = append(removed, "- "+formatLessonLine(l))
			continue
		}
		if n.DetailKey() != l.DetailKey() {
			changed = append(changed, "~ "+formatLessonLine(l)+" → "+formatLessonLine(n))
		}
	}
	for id, l := range newSet {
		if _, ok := oldSet[id]; !ok {
			added = append(added, "+ "+formatLessonLine(l))
		}
	}
	sort.Strings(removed)
	sort.Strings(added)
	logx.Debugf("rasp", "Diff: removed=%d added=%d", len(removed), len(added))
	lines := make([]string, 0, len(removed)+len(added))
	lines = append(lines, removed...)
	lines = append(lines, added...)
	result := strings.Join(lines, "\n")
	logx.Debugf("rasp", "Diff: result_bytes=%d", len(result))
	return result
}

func formatLessonLine(l model.Lesson) string {
	logx.Debugf("rasp", "formatLessonLine: date=%s start=%s discipline=%q subgroup=%d online=%v",
		l.Date, l.Start, l.Discipline, l.Subgroup, l.Online)
	place := l.Place
	if place == "" {
		place = "-"
	}
	return fmt.Sprintf("%s %s %s %s (%s, %s, подгруппа %d)",
		l.Date, l.Start, l.Discipline, l.Type, l.Teacher, place, l.Subgroup)
}

func mergeLessons(weeks ...[]model.Lesson) []model.Lesson {
	total := 0
	for _, w := range weeks {
		total += len(w)
	}
	logx.Debugf("rasp", "mergeLessons: weeks=%d input=%d", len(weeks), total)
	seen := make(map[string]struct{})
	var out []model.Lesson
	for _, week := range weeks {
		for _, l := range week {
			id := l.DetailKey()
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, l)
		}
	}
	logx.Debugf("rasp", "mergeLessons: deduped=%d", len(out))
	return out
}
