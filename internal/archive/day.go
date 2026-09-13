package archive

import (
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

const HarvestGrace = 3 * time.Minute

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
