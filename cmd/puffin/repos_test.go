package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runGit drives a real repository. inspectRepo shells out to git, so the
// only honest fixture is git: a mock here would test the mock's idea of
// porcelain output, and porcelain output is exactly the thing that drifts.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir,
		"-c", "user.email=test",
		"-c", "user.name=test",
		"-c", "commit.gpgsign=false",
		"-c", "init.defaultBranch=main",
	}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// clonedRepo returns a working checkout whose remote is a bare repository
// on disk, so ls-remote answers without a network.
func clonedRepo(t *testing.T) (work string) {
	t.Helper()
	base := t.TempDir()
	bare := filepath.Join(base, "origin.git")
	if out, err := exec.Command(
		"git",
		"init",
		"--bare",
		"-b",
		"main",
		bare,
	).CombinedOutput(); err != nil {
		t.Fatalf("bare init: %v\n%s", err, out)
	}
	work = filepath.Join(base, "work")
	if out, err := exec.Command(
		"git",
		"clone",
		"-q",
		bare,
		work,
	).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	if err := os.WriteFile(
		filepath.Join(work, "README"),
		[]byte("hello\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", "README")
	runGit(t, work, "commit", "-qm", "first")
	runGit(t, work, "push", "-q", "origin", "main")
	runGit(t, work, "branch", "--set-upstream-to=origin/main", "main")
	return work
}

// TestInspectRepoCleanIsSafe is the baseline: everything pushed, nothing
// held back, so the directory can go.
func TestInspectRepoCleanIsSafe(t *testing.T) {
	r := inspectRepo(clonedRepo(t))
	safe, why := r.Safe()
	if !safe {
		t.Fatalf(
			"a fully pushed checkout reported unsafe: %v (%+v)",
			why,
			r,
		)
	}
	if r.Remote != "origin" {
		t.Errorf("remote = %q, want origin", r.Remote)
	}
}

// TestInspectRepoCountsWhatWouldBeLost walks every reason a checkout is
// not safe. Each one is work that exists nowhere else, which is the whole
// question this file was written to answer.
func TestInspectRepoCountsWhatWouldBeLost(t *testing.T) {
	t.Run("modified and untracked both count", func(t *testing.T) {
		dir := clonedRepo(t)
		os.WriteFile(
			filepath.Join(dir, "README"),
			[]byte("changed\n"),
			0o644,
		)
		os.WriteFile(
			filepath.Join(dir, "never-added.yaml"),
			[]byte("x\n"),
			0o644,
		)
		r := inspectRepo(dir)
		if r.Dirty != 2 {
			t.Errorf(
				"Dirty = %d, want 2 (one modified, one "+
					"untracked)",
				r.Dirty,
			)
		}
		if safe, _ := r.Safe(); safe {
			t.Error("a dirty checkout reported safe")
		}
	})

	t.Run("unpushed commits", func(t *testing.T) {
		dir := clonedRepo(t)
		os.WriteFile(
			filepath.Join(dir, "README"),
			[]byte("two\n"),
			0o644,
		)
		runGit(t, dir, "commit", "-qam", "second")
		os.WriteFile(
			filepath.Join(dir, "README"),
			[]byte("three\n"),
			0o644,
		)
		runGit(t, dir, "commit", "-qam", "third")
		if r := inspectRepo(dir); r.Unpushed != 2 {
			t.Errorf("Unpushed = %d, want 2", r.Unpushed)
		}
	})

	t.Run(
		"a branch with no upstream is worse than unpushed",
		func(t *testing.T) {
			dir := clonedRepo(t)
			runGit(t, dir, "checkout", "-qb", "only-here")
			r := inspectRepo(dir)
			if len(r.LocalOnly) != 1 ||
				r.LocalOnly[0] != "only-here" {
				t.Errorf(
					"LocalOnly = %v, want [only-here]",
					r.LocalOnly,
				)
			}
			_, why := r.Safe()
			if !strings.Contains(
				strings.Join(why, " "),
				"no upstream",
			) {
				t.Errorf(
					"Safe() did not name the branch: %v",
					why,
				)
			}
		},
	)

	t.Run("stashes are invisible by construction", func(t *testing.T) {
		dir := clonedRepo(t)
		os.WriteFile(
			filepath.Join(dir, "README"),
			[]byte("stashed\n"),
			0o644,
		)
		runGit(t, dir, "stash", "push", "-q", "-m", "wip")
		if r := inspectRepo(dir); r.Stashes != 1 {
			t.Errorf("Stashes = %d, want 1", r.Stashes)
		}
	})

	t.Run("a tag the remote has never seen", func(t *testing.T) {
		dir := clonedRepo(t)
		runGit(t, dir, "tag", "-a", "v9.9.9", "-m", "local only")
		r := inspectRepo(dir)
		if len(r.UnpushedTags) != 1 || r.UnpushedTags[0] != "v9.9.9" {
			t.Errorf(
				"UnpushedTags = %v, want [v9.9.9]",
				r.UnpushedTags,
			)
		}
	})

	t.Run("a pushed tag does not count", func(t *testing.T) {
		dir := clonedRepo(t)
		runGit(t, dir, "tag", "-a", "v1.0.0", "-m", "released")
		runGit(t, dir, "push", "-q", "origin", "v1.0.0")
		if r := inspectRepo(dir); len(r.UnpushedTags) != 0 {
			t.Errorf("UnpushedTags = %v, want none", r.UnpushedTags)
		}
	})

	t.Run("no remote means it exists only here", func(t *testing.T) {
		dir := t.TempDir()
		runGit(t, dir, "init", "-q", "-b", "main")
		os.WriteFile(filepath.Join(dir, "f"), []byte("x\n"), 0o644)
		runGit(t, dir, "add", "f")
		runGit(t, dir, "commit", "-qm", "only copy")
		r := inspectRepo(dir)
		if r.Remote != "" {
			t.Errorf("Remote = %q, want empty", r.Remote)
		}
		safe, why := r.Safe()
		if safe ||
			!strings.Contains(strings.Join(why, " "), "only here") {
			t.Errorf("safe=%v why=%v", safe, why)
		}
	})
}

// TestSafeNamesEveryReason: Safe() is what the operator reads before an rm,
// so it has to list all of them at once rather than the first one found.
func TestSafeNamesEveryReason(t *testing.T) {
	r := RepoState{
		Dirty: 2, Unpushed: 3, Stashes: 1,
		LocalOnly: []string{"a", "b"}, UnpushedTags: []string{"v1"},
		Remote: "origin", Err: "kaboom",
	}
	safe, why := r.Safe()
	if safe {
		t.Fatal("reported safe with six reasons not to be")
	}
	joined := strings.Join(why, "\n")
	for _, want := range []string{"kaboom", "2 uncommitted", "3 unpushed " +
		"commit", "a, b", "1 stash", "v1"} {
		if !strings.Contains(joined, want) {
			t.Errorf("Safe() never mentioned %q:\n%s", want, joined)
		}
	}
	if ok, _ := (RepoState{Remote: "origin"}).Safe(); !ok {
		t.Error("an empty state with a remote should be safe")
	}
}

// TestFindReposStopsAtTheRepository: a checkout's contents are its own
// business, and a crawl into a model cache turns a two-second answer into
// a filesystem walk.
func TestFindReposStopsAtTheRepository(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{
		"one/.git", "two/.git", "two/nested/.git",
		"deep/a/b/c/.git", "skip/node_modules/pkg/.git", "plain/subdir",
	} {
		if err := os.MkdirAll(
			filepath.Join(root, d),
			0o755,
		); err != nil {
			t.Fatal(err)
		}
	}

	got, err := findRepos(root, 3)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, g := range got {
		rel, _ := filepath.Rel(root, g)
		names = append(names, rel)
	}
	joined := strings.Join(names, " ")

	if !strings.Contains(joined, "one") ||
		!strings.Contains(joined, "two") {
		t.Errorf("missed a top-level checkout: %v", names)
	}
	if strings.Contains(joined, "nested") {
		t.Errorf("descended into a checkout: %v", names)
	}
	if strings.Contains(joined, "node_modules") {
		t.Errorf("walked into node_modules: %v", names)
	}
	if strings.Contains(joined, "plain") {
		t.Errorf("reported a directory with no .git: %v", names)
	}
}

// TestFindReposHonoursDepth: the limit is what keeps this bounded on a root
// somebody points at their whole home directory.
func TestFindReposHonoursDepth(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "a/b/c/d/e/.git"), 0o755)
	if got, _ := findRepos(root, 2); len(got) != 0 {
		t.Errorf("depth 2 reached a checkout five levels down: %v", got)
	}
	if got, _ := findRepos(root, 6); len(got) != 1 {
		t.Errorf("depth 6 did not find it: %v", got)
	}
}
