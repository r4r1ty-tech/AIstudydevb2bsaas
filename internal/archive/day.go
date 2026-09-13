package archive

import (
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

const HarvestGrace = 3 * time.Minute

// NotesDay is the calendar day whose lecture PDFs start at next midnight.
// At 00:05 on 14.09 this is 13.09; at 23:59 on 13.09 this is still 12.09.
func NotesDay(now time.Time) string {
	if now.IsZero() {
		return ""
	}
	loc := now.Location()
	if loc == nil {
		loc = time.UTC
	}
	now = now.In(loc)
	y, m, d := now.Date()
	midnight := time.Date(y, m, d, 0, 0, 0, 0, loc)
	return midnight.AddDate(0, 0, -1).Format("2006-01-02")
}

func ShouldNotePack(status string) bool {
	switch status {
	case model.PackDone, model.PackRecording, model.PackError:
		return false
	default:
		return true
	}
}

func ShouldHarvest(lessons []model.Lesson, now time.Time, grace time.Duration) bool {
	if grace < 0 {
		grace = HarvestGrace
	}
	today := now.Format("2006-01-02")
	var any bool
	var last time.Time
	for _, l := range lessons {
		if !l.Online || !model.IsLecture(l.Type) || l.Date != today {
			continue
		}
		any = true
		if l.Finish.After(last) {
			last = l.Finish
		}
		if now.Before(l.Finish) {
			return false
		}
	}
	if !any || last.IsZero() {
		return false
	}
	return !now.Before(last.Add(grace))
}
