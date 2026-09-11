package bbb

import (
	"strconv"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

const maxLeaveEarly = 5 * time.Minute

func WantsJoin(intent *model.JoinIntent) bool {
	if intent != nil && intent.Decision == model.JoinNo {
		return false
	}
	return true
}

func LeaveAt(finish time.Time, early time.Duration) time.Time {
	if early < 0 {
		early = 0
	}
	if early > maxLeaveEarly {
		early = maxLeaveEarly
	}
	return finish.Add(-early)
}

func ShouldBeInRoom(now, begin, leaveAt time.Time) bool {
	if now.Before(begin) {
		return false
	}
	return now.Before(leaveAt)
}

func sessionKey(telegramID, lessonID int64) string {
	return strconv.FormatInt(telegramID, 10) + ":" + strconv.FormatInt(lessonID, 10)
}
