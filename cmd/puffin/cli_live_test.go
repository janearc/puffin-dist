//go:build live

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The published surface, exercised for real.
//
// The estate's rule is integration tests for the entirety of the published
// operations, and the operations are not a guess -- cli.go carries one
// `commands` list precisely so the help and the surface cannot drift apart.
//
// This walks that list rather than a copy of it, so a verb added tomorrow shows
// up here as a failure rather than as a gap nobody notices.
//
// Only the read-only half is invoked. The mutating verbs end somebody's work,
// and this repository's rule is that nothing in a test ever presses a
// confirming key: the wire is checked, the wire is never sent.
//
// Those verbs have their own live tests --
// TestLiveStopAndStartRestoresTheRealCount and
// TestLiveRestartRollsWithoutScaling -- which build their own workload and take
// it away again.

// notUnattended is every verb this file must not simply call, in two
// groups, kept apart because they are excluded for opposite reasons and
// one name for both would be a lie of the kind this repository keeps
// finding.
//
// Destructive ones end somebody's work. They are covered by live tests
// that build their own workload and take it away again --
// TestLiveStopAndStartRestoresTheRealCount, TestLiveRestartRollsWithout-
// Scaling -- and never by pressing a confirming key here.
//
// Blocking ones do not return: watch waits for something to happen and pet
// opens a screen. They need a driver, not an invocation.
var notUnattended = map[string]string{
	"set": "destructive", "exec": "destructive", "sh": "destructive",
	"stop": "destructive", "start": "destructive", "restart": "destructive",
	"watch": "blocking", "pet": "blocking",
}

// verb is the first word of a `commands` entry: "logs <pod> [-n ns]" -> "logs".
func verb(use string) string {
	if i := strings.IndexAny(use, " "); i >= 0 {
		return use[:i]
	}
	return use
}

// TestLiveEveryPublishedVerbIsCovered fails when a read-only verb is added
// to the surface and nothing here runs it. It is the tripwire that keeps
// this file honest as the surface grows.
func TestLiveEveryPublishedVerbIsCovered(t *testing.T) {
	exercised := map[string]bool{
		"status": true, "truth": true, "flags": true, "get": true,
		"logs": true, "agents": true, "notify": true, "selftest": true,
		"version": true, "--age": true, "--context": true, "diff": true,
		"repos": true,
	}
	for _, c := range commands {
		v := verb(c.use)
		if notUnattended[v] != "" || exercised[v] {
			continue
		}
		t.Errorf(
			"published verb %q has no live coverage; exercise it "+
				"or classify it in notUnattended",
			v,
		)
	}
}

// TestLiveReadOnlyVerbsAnswer runs each read-only verb against the real
// enclave. The assertion is deliberately weak -- that it runs and does not
// crash -- because the strong assertions belong to the per-area live tests.
//
// What this catches is the failure they cannot: a verb that stopped being wired
// up at all.
func TestLiveReadOnlyVerbsAnswer(t *testing.T) {
	for _, args := range [][]string{
		{"version"}, {"--age"}, {"--contexts"},
		{"status"}, {"truth"}, {"flags"}, {"agents"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if code := runCLI(args); code != 0 {
				t.Errorf(
					"puffin %s exited %d",
					strings.Join(args, " "),
					code,
				)
			}
		})
	}
}

// TestLiveReposAuditsTheCodeRoot runs the audit against the configured
// code root, which is the directory it was written for.
//
// The assertion that matters is not a count -- checkouts come and go -- but
// that every answer is decided rather than blank.
//
// A repository reported with no remote AND no reasons is the audit having
// silently failed, which is the shape of bug this whole file exists to catch:
// an unsafe directory that reads as a clean result.
func TestLiveReposAuditsTheCodeRoot(t *testing.T) {
	root := codeRoot()
	if _, err := os.Stat(root); err != nil {
		t.Skipf("no %s on this host", root)
	}
	dirs, err := findRepos(root, 3)
	if err != nil {
		t.Fatalf("findRepos: %v", err)
	}
	if len(dirs) == 0 {
		t.Fatalf(
			"no checkouts found under %s; the walk is broken",
			root,
		)
	}

	sawSafe := false
	for _, d := range dirs {
		r := inspectRepo(d)
		safe, why := r.Safe()
		t.Logf(
			"%-24s safe=%-5v %s",
			r.Name,
			safe,
			strings.Join(why, "; "),
		)
		if r.Err != "" {
			t.Errorf("%s: %s", r.Name, r.Err)
		}
		if !safe && len(why) == 0 {
			t.Errorf("%s: unsafe with no reason given", r.Name)
		}
		if safe && r.Remote == "" {
			t.Errorf("%s: reported safe with no remote", r.Name)
		}
		if safe {
			sawSafe = true
		}
	}
	if !sawSafe {
		t.Error(
			"every checkout in the estate is unsafe; that is a " +
				"bug in the audit, not the estate",
		)
	}
}

// TestLiveAgentsProjectNamesTheRoot is the integration half of the
// project-column fix, against the real transcripts on this host.
//
// The unit tests build a fake HOME. This one asserts the property that
// actually matters on the real machine: a session's project must never be
// a bare repository name when two groups hold that name, because the agents
// pane is where sessions are killed from.
func TestLiveAgentsProjectNamesTheRoot(t *testing.T) {
	sessions := fetchAgents(agentWindow).Sessions
	if len(sessions) == 0 {
		t.Skip("no claude sessions on this host to inspect")
	}

	root := codeRoot()
	checked := 0
	for _, s := range sessions {
		if s.Cwd == "" ||
			!strings.HasPrefix(
				s.Cwd,
				root+string(filepath.Separator),
			) {
			continue
		}
		checked++
		if s.Project == "" {
			t.Errorf("session in %s has no project", s.Cwd)
			continue
		}
		// under mesh the name must carry its root, or two different
		// repositories with one name are one row
		if !strings.Contains(s.Project, "/") &&
			s.Project != filepath.Base(mesh) {
			rest := strings.TrimPrefix(
				s.Cwd,
				mesh+string(filepath.Separator),
			)
			if strings.Contains(rest, string(filepath.Separator)) {
				t.Errorf(
					"session in %s rendered as %q, which "+
						"does not say which root",
					s.Cwd,
					s.Project,
				)
			}
		}
		if s.Project == filepath.Base(s.Cwd) &&
			strings.Contains(s.Cwd, "/.claude/worktrees/") {
			t.Errorf(
				"worktree %s rendered as its branch name %q",
				s.Cwd,
				s.Project,
			)
		}
	}
	if checked == 0 {
		t.Skip("no sessions under the code root to check")
	}
	t.Logf("checked %d sessions under %s", checked, root)
}
