package bbb

import "context"

type Session interface {
	InLobby(ctx context.Context) (bool, error)
	Close() error
}

type Joiner interface {
	Join(ctx context.Context, meetingURL, fio, socks5 string) (Session, error)
}

type drySession struct{}

func (drySession) InLobby(context.Context) (bool, error) { return false, nil }
func (drySession) Close() error                          { return nil }

type DryJoiner struct{}

func (DryJoiner) Join(_ context.Context, _, _, _ string) (Session, error) {
	return drySession{}, nil
}
