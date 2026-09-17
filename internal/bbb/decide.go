package bbb

import (
	"strconv"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

const (
	maxLeaveEarly    = 5 * time.Minute
	joinEarlySilence = 5 * time.Minute
	JoinEarlyYes     = 15 * time.Minute
)

func WantsJoin(intent *model.JoinIntent) bool {
	if intent != nil && intent.Decision == model.JoinNo {
		logx.Debugf("bbb", "WantsJoin: decision=%q -> false", intent.Decision)
		return false
	}
	decision := ""
	if intent != nil {
		decision = string(intent.Decision)
	}
	logx.Debugf("bbb", "WantsJoin: decision=%q -> true", decision)
	return true
}

func EnterAt(begin time.Time, intent *model.JoinIntent) time.Time {
	early := joinEarlySilence
	if intent != nil && intent.Decision == model.JoinYes {
		early = JoinEarlyYes
	}
	at := begin.Add(-early)
	logx.Debugf("bbb", "EnterAt: begin=%s early=%s at=%s", begin.Format(time.RFC3339), early, at.Format(time.RFC3339))
	return at
}

func LeaveAt(finish time.Time, early time.Duration) time.Time {
	if early < 0 {
		early = 0
	}
	if early > maxLeaveEarly {
		early = maxLeaveEarly
	}
	at := finish.Add(-early)
	logx.Debugf("bbb", "LeaveAt: finish=%s early=%s at=%s", finish.Format(time.RFC3339), early, at.Format(time.RFC3339))
	return at
}

func ShouldBeInRoom(now, begin, leaveAt time.Time) bool {
	if now.Before(begin) {
		logx.Debugf("bbb", "ShouldBeInRoom: now=%s begin=%s -> false (early)", now.Format(time.RFC3339), begin.Format(time.RFC3339))
		return false
	}
	in := now.Before(leaveAt)
	logx.Debugf("bbb", "ShouldBeInRoom: now=%s begin=%s leaveAt=%s -> %v", now.Format(time.RFC3339), begin.Format(time.RFC3339), leaveAt.Format(time.RFC3339), in)
	return in
}

func sessionKey(telegramID, lessonID int64) string {
	key := strconv.FormatInt(telegramID, 10) + ":" + strconv.FormatInt(lessonID, 10)
	logx.Debugf("bbb", "sessionKey: tg=%d lesson=%d key=%s", telegramID, lessonID, key)
	return key
}
