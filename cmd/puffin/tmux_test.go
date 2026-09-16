package main

import (
	"strings"
	"testing"
	"time"
)

// The separator bug, pinned. The obvious choice for a field separator is the
// ASCII unit separator -- it is what 0x1f is FOR -- and tmux escapes it on the
// way out, so the byte arrives as the four characters \037 and every line
// parses as one field.
//
// It failed silently: no error, no complaint from tmux, just an empty pane list
// that reads exactly like a machine running no tmux at all. This is that real
// output.
func TestTmuxEscapesTheObviousSeparator(t *testing.T) {
	escaped := `0:1.1\0370\037/src\037claude.exe\0371\037rough day`
	if n := len(strings.SplitN(escaped, "\x1f", 6)); n != 1 {
		t.Fatalf(
			"tmux's escaped output split into %d fields -- if "+
				"this passes, tmux stopped escaping",
			n,
		)
	}
	// tab survives, and neither a path nor a command can contain one
	if !strings.Contains(tmuxFormat, "\t") {
		t.Fatal("the format is not tab-separated")
	}
	if strings.Contains(tmuxFormat, "\x1f") {
		t.Fatal("the format uses a separator tmux escapes")
	}
}

// The pane title is the one field a human can put anything in, so it goes
// LAST: a tab in a title runs off the end rather than shifting every column.
func TestATabInAPaneTitleDoesNotShiftTheColumns(t *testing.T) {
	line := "0:1.1\tmain\t/src\tclaude.exe\t1\trough\tday\there"
	f := strings.SplitN(line, tmuxSep, 6)
	if len(f) != 6 {
		t.Fatalf("fields: %d", len(f))
	}
	if f[2] != "/src" || f[3] != "claude.exe" || f[4] != "1" {
		t.Fatalf("a title tab shifted the columns: %q", f)
	}
	if f[5] != "rough\tday\there" {
		t.Fatalf("the title lost its tail: %q", f[5])
	}
	// and the title is genuinely last in the format
	if !strings.HasSuffix(tmuxFormat, "#{pane_title}") {
		t.Fatal("the arbitrary field is not last")
	}
}

// A pane belongs to at most ONE session: the newest writer in that directory.
//
// Four sessions have run in one checkout today and exactly one is typing in the
// pane -- handing the address to all four means "say something to this agent"
// can deliver a sentence into a live pane while you believe you are addressing
// a session that ended yesterday.
func TestOnePaneBelongsToOneSession(t *testing.T) {
	now := time.Now()
	v := AgentView{Sessions: []AgentSession{
		{ID: "live", Cwd: "/src", Project: "dev", LastWrite: now},
		{
			ID:        "old",
			Cwd:       "/src",
			Project:   "dev",
			LastWrite: now.Add(-time.Hour),
		},
		{
			ID:        "older",
			Cwd:       "/src",
			Project:   "dev",
			LastWrite: now.Add(-3 * time.Hour),
		},
	}}
	pane := TmuxPane{Target: "0:1.1", Path: "/src", Command: "claude.exe"}
	byPath, n := tmuxByPath([]TmuxPane{pane})
	taken := map[string]bool{}
	for i := range v.Sessions {
		cwd := v.Sessions[i].Cwd
		p, ok := byPath[cwd]
		if !ok {
			continue
		}
		if taken[cwd] {
			v.Sessions[i].TmuxShared = p.Target
			continue
		}
		taken[cwd] = true
		v.Sessions[i].Tmux, v.Sessions[i].TmuxCount = p, n[cwd]
	}
	if v.Sessions[0].Tmux.Target != "0:1.1" {
		t.Fatal("the newest writer did not get the pane")
	}
	for _, s := range v.Sessions[1:] {
		if s.Tmux.Target != "" {
			t.Fatalf("%s is addressable and should not be", s.ID)
		}
		if s.TmuxShared != "0:1.1" {
			t.Fatalf("%s does not say who holds the pane", s.ID)
		}
	}
	// and the pane says WHY rather than doing nothing
	why := tmuxWhyNot(v.Sessions[1])
	if !strings.Contains(why, "0:1.1") ||
		!strings.Contains(why, "not what is typing") {
		t.Fatalf("unhelpful refusal: %q", why)
	}
}

// The active pane wins when two panes really do share a directory.
func TestActivePaneWinsAShareddirectory(t *testing.T) {
	best, n := tmuxByPath([]TmuxPane{
		{Target: "0:1.1", Path: "/x"},
		{Target: "0:2.0", Path: "/x", Active: true},
	})
	if best["/x"].Target != "0:2.0" {
		t.Fatalf("picked %q", best["/x"].Target)
	}
	if n["/x"] != 2 {
		t.Fatalf(
			"count: %d -- the screen must be able to say the "+
				"target was a pick",
			n["/x"],
		)
	}
}
