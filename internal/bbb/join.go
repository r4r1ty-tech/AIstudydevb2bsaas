package bbb

import "context"

type Role int

const (
	RolePresence Role = iota
	RoleRecord
	RoleSlides
)

type JoinReq struct {
	URL    string
	FIO    string
	SOCKS5 string
	Role   Role
}

type Session interface {
	InLobby(ctx context.Context) (bool, error)
	Greet(ctx context.Context) error
	Close() error
	GrabSlides(ctx context.Context, dir string) (int, error)
}

type Joiner interface {
	Join(ctx context.Context, req JoinReq) (Session, error)
}

type drySession struct{}

func (drySession) InLobby(context.Context) (bool, error) { return false, nil }
func (drySession) Greet(context.Context) error           { return nil }
func (drySession) Close() error                          { return nil }
func (drySession) GrabSlides(context.Context, string) (int, error) {
	return 0, nil
}

type DryJoiner struct{}

func (DryJoiner) Join(_ context.Context, _ JoinReq) (Session, error) {
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
	if c.fn != nil {
		c.fn()
		c.fn = nil
	}
	if c.Session != nil {
		return c.Session.Close()
	}
	return nil
}
