package main

import (
	"os"
	"path/filepath"
	"testing"
)

// mkrepo builds a directory tree under a fake HOME and marks some of it as
// repositories, so the walk is tested against real stat calls rather than
// against a mock of the thing being tested.
func mkrepo(t *testing.T, home string, dirs []string, repos []string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(
			filepath.Join(home, d),
			0o755,
		); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range repos {
		if err := os.MkdirAll(
			filepath.Join(home, r, ".git"),
			0o755,
		); err != nil {
			t.Fatal(err)
		}
	}
}

// TestProjectNameSeparatesTwoCheckouts is the one that matters. A service
// checked out twice under one code root reads the same in the column
// otherwise, and the agents pane is where sessions are killed from.
func TestProjectNameSeparatesTwoCheckouts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoCache.Lock()
	repoCache.at = map[string]string{}
	repoCache.Unlock()

	mkrepo(
		t,
		home,
		[]string{
			"src/staging/atlas/lib",
			"src/staging/tools",
			"src/runtime/bridge",
		},
		[]string{
			"src/staging/lights", "src/prod/lights",
			"src/staging/atlas", "src/staging/tools/ruler",
			"src/prod/lantern", "src/staging/hall-monitor",
			"src/runtime",
		},
	)

	// the worktree directories carry their own .git, which is exactly why
	// the walk has to cut before it reaches them
	mkrepo(t, home, nil, []string{
		"src/prod/lantern/.claude/worktrees/fix-not-run",
		"src/staging/hall-monitor/.claude/worktrees/lease-standing",
	})

	for _, c := range []struct{ in, want string }{
		{"src/staging/lights", "staging/lights"},
		{"src/prod/lights", "prod/lights"},
		{"src/staging/tools/ruler", "staging/ruler"},
		{"src/staging/atlas/lib", "staging/atlas"},
		{
			"src/prod/lantern/.claude/worktrees/fix-not-run",
			"prod/lantern",
		},
		{
			"src/staging/hall-monitor/.claude/worktrees/" +
				"lease-standing",
			"staging/hall-monitor",
		},
		{"src/runtime/bridge", "runtime/bridge"},
		{"src/staging", "staging"},
	} {
		if got := projectName(
			filepath.Join(home, c.in),
		); got != c.want {
			t.Errorf(
				"projectName(%s)\n got %q\nwant %q",
				c.in,
				got,
				c.want,
			)
		}
	}

	if got := projectName(home); got != "~" {
		t.Errorf(
			"home rendered as %q, want ~ -- a username is not a "+
				"project",
			got,
		)
	}
	if got := projectName(""); got != "" {
		t.Errorf("empty cwd rendered as %q", got)
	}
}

// TestProjectNameFitsTheColumn: the pane pads this field to 18.
func TestProjectNameFitsTheColumn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoCache.Lock()
	repoCache.at = map[string]string{}
	repoCache.Unlock()

	mkrepo(
		t,
		home,
		nil,
		[]string{"src/prod/hall-monitor", "src/staging/tools/ruler"},
	)
	for _, p := range []string{
		"src/prod/hall-monitor",
		"src/staging/tools/ruler",
	} {
		if n := len(projectName(filepath.Join(home, p))); n > 18 {
			t.Errorf("%s renders %d wide, column is 18", p, n)
		}
	}
}

// TestProjectNameStopsAtTheRoot: a code root that is itself a repository
// is not unusual. A walk that did not stop below it would find that .git
// and label every session on the machine with the root's name.
func TestProjectNameStopsAtTheRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoCache.Lock()
	repoCache.at = map[string]string{}
	repoCache.Unlock()

	mkrepo(t, home, []string{"src/staging/nothing"}, []string{"src"})
	if got := projectName(
		filepath.Join(home, "src/staging/nothing"),
	); got == "src" ||
		got == "staging/src" {
		t.Errorf(
			"the walk reached the root's own .git and returned %q",
			got,
		)
	}
}
