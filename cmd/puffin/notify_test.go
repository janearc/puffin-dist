package main

import (
	"strings"
	"testing"
)

// tmux does not forward OSC 777: it is not a sequence tmux understands, and
// an unrecognised OSC dies in the pane. `puffin watch` in a tmux pane --
// exactly where it is meant to run -- would notify nobody and report no
// error while doing it.
func TestTmuxPassthrough(t *testing.T) {
	seq := "\x1b]777;notify;a;b\x07"

	t.Setenv("TMUX", "")
	if got := wrapForTmux(seq); got != seq {
		t.Fatalf("wrapped outside tmux: %q", got)
	}

	t.Setenv("TMUX", "/tmp/tmux-501/default,123,0")
	got := wrapForTmux(seq)
	if !strings.HasPrefix(got, "\x1bPtmux;") {
		t.Fatalf("no passthrough wrapper: %q", got)
	}
	if !strings.HasSuffix(got, "\x1b\\") {
		t.Fatalf("no string terminator: %q", got)
	}
	// every ESC in the payload is doubled, which is how tmux finds the end
	inner := strings.TrimSuffix(
		strings.TrimPrefix(got, "\x1bPtmux;"),
		"\x1b\\",
	)
	if strings.Contains(inner, "\x1b") &&
		!strings.Contains(inner, "\x1b\x1b") {
		t.Fatalf("payload escapes not doubled: %q", inner)
	}
	if strings.Count(inner, "\x1b") != 2 {
		t.Fatalf("expected exactly one doubled escape, got %q", inner)
	}
}
