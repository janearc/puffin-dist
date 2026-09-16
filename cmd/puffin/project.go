package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// What to call the directory a session is working in.
//
// This was filepath.Base(cwd), and the base name collapses cases that an
// operator needs told apart. Under a code root that holds a staging and a
// production checkout of the same service, both answer "lights". A worktree
// answers with its branch. A subdirectory answers with the subdirectory.
//
// The root itself answers with the root, which is not a project at all, and a
// home directory answers with a username.
//
// The agents pane is where sessions are killed from, so which checkout a
// session is in is the fact the column exists to carry, and two checkouts
// that read the same in that column is the failure to avoid.
//
// So the answer is ROOT plus repository: staging/lights, prod/lights.
//
// The repository is found rather than computed -- the nearest ancestor holding
// a .git -- because computing it needs a hardcoded list of which directories
// are containers and which are repositories that happen to have subdirectories,
// and from the outside those two are the same shape.
//
// Three bugs here were all one bug: a location assumed instead of asked for. A
// list would have been the fourth.

// codeRoot is where the walk stops: the directory checkouts live under.
// Nothing above it is a project, and a root that is itself a repository
// would otherwise label every session on the machine with the root's name.
//
// PUFFIN_CODE_ROOT moves it. The default is ~/src, which is where a
// checkout sits on most machines.
func codeRoot() string {
	if r := os.Getenv("PUFFIN_CODE_ROOT"); r != "" {
		return r
	}
	return filepath.Join(os.Getenv("HOME"), "src")
}

// worktreeMark is the path segment Claude Code puts its worktrees under. A
// worktree carries its own .git file, so the walk would stop inside one and
// report the branch name as the project; cutting here first gives the
// repository the worktree belongs to.
//
// Which branch it is on is the branch column's job, and it already does it.
const worktreeMark = "/.claude/worktrees/"

// projectColWidth is what the agents pane pads this field to (agents.go).
const projectColWidth = 18

// repoCache memoises the ancestor walk. fetchAgents reads every transcript
// on the host and most sessions share a handful of directories, so this is
// one stat per distinct cwd rather than one per session.
var repoCache = struct {
	sync.Mutex
	at map[string]string
}{at: map[string]string{}}

// repoRootOf is the nearest ancestor of dir that holds a .git, stopping
// at the code root. Empty when there is none -- a directory that is not in
// a repository is a real answer, not a failure.
func repoRootOf(dir string) string {
	repoCache.Lock()
	defer repoCache.Unlock()
	if v, ok := repoCache.at[dir]; ok {
		return v
	}
	stop := codeRoot()
	found := ""
	for d := dir; strings.HasPrefix(
		d,
		stop+string(filepath.Separator),
	); d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			found = d
			break
		}
	}
	repoCache.at[dir] = found
	return found
}

// projectName renders the column: which group under the code root a
// session is in, and which repository.
func projectName(cwd string) string {
	if cwd == "" {
		return ""
	}
	cwd = filepath.Clean(cwd)
	if i := strings.Index(cwd, worktreeMark); i >= 0 {
		cwd = cwd[:i]
	}

	home := os.Getenv("HOME")
	base := codeRoot()
	if cwd == home {
		return "~"
	}
	if !strings.HasPrefix(cwd, base+string(filepath.Separator)) {
		// Outside the code root there are no two checkouts of one
		// service to tell apart, so the reason for all of this does not
		// apply.
		//
		// Say where it is relative to home when that fits the column,
		// and fall back to the bare name when it does not
		//
		// -- a project column that overflows pushes every column after
		// it off the screen, which is a worse failure than a slightly
		// vague name for a directory nobody is running a service out
		// of.
		if rel, err := filepath.Rel(home, cwd); err == nil &&
			!strings.HasPrefix(rel, "..") {
			if n := "~/" + rel; len(n) <= projectColWidth {
				return n
			}
		}
		return filepath.Base(cwd)
	}

	rest := strings.TrimPrefix(cwd, base+string(filepath.Separator))
	group := rest
	if i := strings.Index(rest, string(filepath.Separator)); i >= 0 {
		group = rest[:i]
	}
	// a group directory itself is not a project, and saying its own name
	// is the honest answer rather than inventing one
	if group == rest {
		return group
	}
	repo := repoRootOf(cwd)
	if repo == "" {
		// no repository above it: fall back to the whole path under the
		// root, which is longer but never wrong
		return rest
	}
	if repo == filepath.Join(base, group) {
		// the whole group is ONE repository, so it is not a container
		// and "runtime/runtime" says nothing. The subdirectory is the
		// only distinguishing part left.
		return rest
	}
	return group + "/" + filepath.Base(repo)
}
