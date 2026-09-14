package rasp

import (
	"fmt"
	"sort"
	"strings"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

func Diff(old, new []model.Lesson) string {
	oldSet := make(map[string]model.Lesson, len(old))
	newSet := make(map[string]model.Lesson, len(new))
	for _, l := range old {
		oldSet[l.Identity()] = l
	}
	for _, l := range new {
		newSet[l.Identity()] = l
	}

	var removed, added []string
	for id, l := range oldSet {
		if _, ok := newSet[id]; !ok {
			removed = append(removed, "- "+formatLessonLine(l))
		}
	}
	for id, l := range newSet {
		if _, ok := oldSet[id]; !ok {
			added = append(added, "+ "+formatLessonLine(l))
		}
	}
	sort.Strings(removed)
	sort.Strings(added)
	lines := make([]string, 0, len(removed)+len(added))
	lines = append(lines, removed...)
	lines = append(lines, added...)
	return strings.Join(lines, "\n")
}

func formatLessonLine(l model.Lesson) string {
	place := l.Place
	if place == "" {
		place = "-"
	}
	return fmt.Sprintf("%s %s %s (%s, %s, подгруппа %d)",
		l.Date, l.Start, l.Discipline, l.Teacher, place, l.Subgroup)
}

func mergeLessons(weeks ...[]model.Lesson) []model.Lesson {
	seen := make(map[string]struct{})
	var out []model.Lesson
	for _, week := range weeks {
		for _, l := range week {
			id := l.Identity()
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, l)
		}
	}
	return out
}
