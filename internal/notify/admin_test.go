package notify

import (
	"context"
	"testing"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
)

func TestNotesButton(t *testing.T) {
	if NotesButton(0) != nil || NotesButton(-1) != nil {
		t.Fatal("non-positive pack id must yield nil")
	}
	mk := NotesButton(7)
	if mk == nil || len(mk.InlineKeyboard) != 1 || len(mk.InlineKeyboard[0]) != 1 {
		t.Fatalf("markup = %#v", mk)
	}
	if got := mk.InlineKeyboard[0][0].CallbackData; got != "nt:7" {
		t.Fatalf("callback = %q", got)
	}
}

func TestGuardsDoNotPanic(t *testing.T) {
	ctx := context.Background()
	Admin(ctx, nil, "hi")
	User(ctx, nil, 1, "hi")
	UserMarkup(ctx, nil, 1, "hi", nil)

	empty := &config.Config{}
	Admin(ctx, empty, "hi")
	User(ctx, empty, 1, "hi")
	UserMarkup(ctx, empty, 0, "hi", nil)
	UserMarkup(ctx, empty, 1, "", nil)

	if err := Document(ctx, nil, 1, "x", "", ""); err == nil {
		t.Fatal("document with nil cfg must fail")
	}
	if err := Document(ctx, empty, 1, "x", "", ""); err == nil {
		t.Fatal("document without token must fail")
	}
}
