package archive

import (
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

const HarvestGrace = 3 * time.Minute

// NotesDay is the calendar day whose lecture PDFs start at next midnight.
// At 00:05 on 14.09 this is 13.09; at 23:59 on 13.09 this is still 12.09.
func NotesDay(now time.Time) string {
	logx.Debugf("archive", "NotesDay: enter now=%s", now)
	if now.IsZero() {
		logx.Debugf("archive", "NotesDay: zero time")
		return ""
	}
	loc := now.Location()
	if loc == nil {
		logx.Debugf("archive", "NotesDay: nil location -> UTC")
		loc = time.UTC
	}
	now = now.In(loc)
	y, m, d := now.Date()
	midnight := time.Date(y, m, d, 0, 0, 0, 0, loc)
	out := midnight.AddDate(0, 0, -1).Format("2006-01-02")
	logx.Debugf("archive", "NotesDay: loc=%s midnight=%s -> %s", loc, midnight, out)
	return out
}

func ShouldNotePack(status string) bool {
	switch status {
	case model.PackDone, model.PackRecording, model.PackError, model.PackEmpty:
		logx.Debugf("archive", "ShouldNotePack: status=%s -> false", status)
		return false
	default:
		logx.Debugf("archive", "ShouldNotePack: status=%s -> true", status)
		return true
	}
}

func ShouldHarvest(lessons []model.Lesson, now time.Time, grace time.Duration) bool {
	logx.Debugf("archive", "ShouldHarvest: enter lessons=%d now=%s grace=%s", len(lessons), now, grace)
	if grace < 0 {
		logx.Debugf("archive", "ShouldHarvest: negative grace -> %s", HarvestGrace)
		grace = HarvestGrace
	}
	today := now.Format("2006-01-02")
	for _, l := range lessons {
		if !l.Online || !model.IsLecture(l.Type) || l.Date != today {
			continue
		}
		if !l.Finish.IsZero() && !now.Before(l.Finish.Add(grace)) {
			logx.Debugf("archive", "ShouldHarvest: lecture %s finished at %s + grace %s <= now %s -> true", l.Type, l.Finish, grace, now)
			return true
		}
	}
	logx.Debugf("archive", "ShouldHarvest: no finished lectures with grace today=%s -> false", today)
	return false
}

// AllLecturesFinished reports whether all online lectures scheduled for today
// have finished before now. If there are no online lectures today, it returns true.
func AllLecturesFinished(lessons []model.Lesson, now time.Time) bool {
	today := now.Format("2006-01-02")
	for _, l := range lessons {
		if !l.Online || !model.IsLecture(l.Type) || l.Date != today {
			continue
		}
		if now.Before(l.Finish) {
			return false
		}
	}
	return true
}
