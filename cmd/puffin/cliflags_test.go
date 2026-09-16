package main

import (
	"os"
	"testing"
)

// --context works in front of a subcommand and in front of none, and a
// context puffin cannot see is refused before anything is read.
//
// The refusal is the part worth testing. A typo'd context otherwise fails
// later, once per read, as a different error on every pane -- and
// "connection refused" on six screens does not spell "you misspelled the
// cluster".
func TestTakeGlobalsHandlesContext(t *testing.T) {
	known := kubeContexts()
	if len(known) == 0 {
		t.Skip("no kubectl on this machine")
	}
	t.Setenv("PUFFIN_KUBE_CONTEXT", "")

	rest, tui, code := takeGlobals(
		[]string{"--context", known[0], "status"},
	)
	if code != -1 {
		t.Fatalf("a good context exited with %d", code)
	}
	if tui {
		t.Fatal("a subcommand was treated as a TUI invocation")
	}
	if len(rest) != 1 || rest[0] != "status" {
		t.Fatalf("the subcommand did not survive: %v", rest)
	}
	if os.Getenv("PUFFIN_KUBE_CONTEXT") != known[0] {
		t.Fatalf(
			"the context was not applied: %q",
			os.Getenv("PUFFIN_KUBE_CONTEXT"),
		)
	}

	// with nothing left over, it is a TUI invocation with an opinion
	if _, tui, _ := takeGlobals([]string{"--context=" + known[0]}); !tui {
		t.Fatal("--context alone should open the interface")
	}

	// a name kubectl has never heard of stops here, loudly
	if _, _, code := takeGlobals([]string{
		"--context",
		"k3d-definitely-not",
	}); code == 0 ||
		code == -1 {
		t.Fatalf("an unknown context was accepted (code %d)", code)
	}

	// and a flag with nothing after it is a usage error, not a panic
	if _, _, code := takeGlobals([]string{"--context"}); code != 2 {
		t.Fatalf("a bare --context returned %d", code)
	}
}

// Everything that is not a global flag is passed through untouched.
func TestTakeGlobalsPassesThrough(t *testing.T) {
	rest, tui, code := takeGlobals(
		[]string{"logs", "flipr-0", "--tail", "50"},
	)
	if code != -1 || tui {
		t.Fatalf(
			"a plain subcommand was intercepted: code=%d tui=%v",
			code,
			tui,
		)
	}
	if len(rest) != 4 {
		t.Fatalf("arguments were eaten: %v", rest)
	}
}
