package bbb

import (
	"context"
	"net/url"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u == nil {
		return raw
	}
	u.RawQuery = ""
	u.Fragment = ""
	u.User = nil
	return u.String()
}

type Role int

const (
	RolePresence Role = iota
	RoleRecord
	RoleSlides
)

func (r Role) String() string {
	switch r {
	case RolePresence:
		return "presence"
	case RoleRecord:
		return "record"
	case RoleSlides:
		return "slides"
	default:
		return "unknown"
	}
}

type JoinReq struct {
	URL  string
	FIO  string
	Role Role
}

type Session interface {
	InLobby(ctx context.Context) (bool, error)
	InRoom(ctx context.Context) (bool, error)
	Greet(ctx context.Context) error
	Close() error
	GrabSlides(ctx context.Context, dir string) (int, error)
}

type Joiner interface {
	Join(ctx context.Context, req JoinReq) (Session, error)
}

type drySession struct{}

func (drySession) InLobby(context.Context) (bool, error) {
	logx.Debugf("bbb", "drySession.InLobby: -> false")
	return false, nil
}
func (drySession) InRoom(context.Context) (bool, error) {
	logx.Debugf("bbb", "drySession.InRoom: -> true")
	return true, nil
}
func (drySession) Greet(context.Context) error {
	logx.Debugf("bbb", "drySession.Greet: noop")
	return nil
}
func (drySession) Close() error {
	logx.Debugf("bbb", "drySession.Close: noop")
	return nil
}
func (drySession) GrabSlides(context.Context, string) (int, error) {
	logx.Debugf("bbb", "drySession.GrabSlides: -> 0")
	return 0, nil
}

type DryJoiner struct{}

func (DryJoiner) Join(_ context.Context, req JoinReq) (Session, error) {
	logx.Debugf("bbb", "DryJoiner.Join: role=%s fio=%q url=%s", req.Role, req.FIO, redactURL(req.URL))
	return drySession{}, nil
}

type closeHook struct {
	Session
	fn func()
}

func (c *closeHook) Close() error {
	if c == nil {
		return nil
	}
	logx.Debugf("bbb", "closeHook.Close: fn=%v session=%v", c.fn != nil, c.Session != nil)
	if c.fn != nil {
		c.fn()
		c.fn = nil
	}
	if c.Session != nil {
		if err := c.Session.Close(); err != nil {
			logx.Errorf("bbb", "closeHook.Close: session close: %v", err)
			return err
		}
	}
	return nil
}
