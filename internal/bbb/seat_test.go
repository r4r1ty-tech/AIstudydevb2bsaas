package bbb

import (
	"testing"
)

func TestClassifySeat(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		sig  seatSignals
		want seat
	}{
		{"room wins", seatSignals{hasRoom: true, hasForm: true}, seatRoom},
		{"lobby el", seatSignals{hasLobby: true}, seatLobby},
		{"lobby text", seatSignals{text: "Waiting for a moderator to join"}, seatLobby},
		{"form", seatSignals{hasForm: true}, seatForm},
		{"unknown", seatSignals{text: "Greenlight"}, seatUnknown},
		{"ru lobby", seatSignals{text: "Ожидайте модератора"}, seatLobby},
		{"form beats fuzzy lobby text", seatSignals{hasForm: true, text: "Waiting for a moderator to join"}, seatForm},
	}
	for _, c := range cases {
		if got := classifySeat(c.sig); got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}
