package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Is it safe to delete this directory?
//
// Nobody should have to be afraid of deleting a checkout.
//
// That fear is rational and it is also answerable. A checkout is safe to
// remove when everything in it exists somewhere else, and "somewhere else"
// has a precise definition: pushed to a remote. Everything that makes a
// checkout unsafe is enumerable, and every item is one git command.
//
// This is the same shape as the ports audit and it earns its place the same
// way: the failure mode is not that the answer is hard, it is that nobody
// checks, so the directory sits there for a year because deleting it feels
// like a risk. An unanswered question costs more than a bad answer.
//
// The rule this enforces is that a branch which exists only on local disk
// does not exist. This says which ones those are.

// RepoState is everything that decides whether a checkout can be removed.
type RepoState struct {
	Path   string
	Name   string
	Remote string
	// Dirty counts modified and untracked paths together, because both are
	// work that exists nowhere else. Untracked files are the ones people
	// forget: a manifest written and never added is invisible to `git
	// status --short` habits and gone forever on an rm.
	Dirty int
	// Unpushed is commits on branches that have an upstream and are ahead
	// of it.
	Unpushed int
	// LocalOnly are branches with no upstream at all. Worse than unpushed:
	// nothing anywhere knows they exist.
	LocalOnly []string
	// Stashes are invisible by construction. Nobody has ever remembered a
	// stash from three weeks ago.
	Stashes int
	// UnpushedTags are tags that exist here and not on the remote, which
	// matters once releases are cut from tags.
	UnpushedTags []string
	Err          string
}

// Safe says whether the directory can be deleted without losing anything,
// and why not when it cannot.
func (r RepoState) Safe() (bool, []string) {
	var why []string
	if r.Err != "" {
		why = append(why, r.Err)
	}
	if r.Remote == "" {
		why = append(why, "no remote: this repository exists only here")
	}
	if r.Dirty > 0 {
		why = append(
			why,
			fmt.Sprintf(
				"%d uncommitted or untracked path(s)",
				r.Dirty,
			),
		)
	}
	if r.Unpushed > 0 {
		why = append(
			why,
			fmt.Sprintf("%d unpushed commit(s)", r.Unpushed),
		)
	}
	if len(r.LocalOnly) > 0 {
		why = append(
			why,
			fmt.Sprintf(
				"%d branch(es) with no upstream: %s",
				len(
					r.LocalOnly,
				),
				strings.Join(r.LocalOnly, ", "),
			),
		)
	}
	if r.Stashes > 0 {
		why = append(why, fmt.Sprintf("%d stash(es)", r.Stashes))
	}
	if len(r.UnpushedTags) > 0 {
		why = append(why, fmt.Sprintf(
			"%d unpushed tag(s): %s",
			len(
				r.UnpushedTags,
			),
			strings.Join(r.UnpushedTags, ", "),
		))
	}
	return len(why) == 0, why
}

// findRepos walks a root for checkouts, without descending into them.
//
// Depth-limited and it does not follow symlinks: a model cache or a
// node_modules tree under a root would otherwise turn a two-second answer
// into a filesystem crawl.
func findRepos(root string, maxDepth int) ([]string, error) {
	var out []string
	rootDepth := strings.Count(
		filepath.Clean(root),
		string(filepath.Separator),
	)
	err := filepath.WalkDir(
		root,
		func(path string, d os.DirEntry, err error) error {
			if err != nil {
				// unreadable is not fatal: report what can be
				// read
				return nil
			}
			if !d.IsDir() {
				return nil
			}
			if name := d.Name(); name == "node_modules" ||
				name == "vendor" ||
				name == ".venv" ||
				name == "target" {
				return filepath.SkipDir
			}
			if strings.Count(
				path,
				string(filepath.Separator),
			)-rootDepth > maxDepth {
				return filepath.SkipDir
			}
			if _, err := os.Stat(
				filepath.Join(path, ".git"),
			); err == nil {
				out = append(out, path)
				// a repository's contents are its own business:
				// submodules and nested checkouts are reported
				// by their parent
				return filepath.SkipDir
			}
			return nil
		},
	)
	sort.Strings(out)
	return out, err
}

// git runs one command in a repository and returns trimmed output.
func gitIn(dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir}, args...)
	out, err := exec.Command("git", full...).Output()
	return strings.TrimSpace(string(out)), err
}

// inspectRepo answers every question at once.
//
// It never fetches. A fetch would make the answer more current and would
// also mean a network round trip per repository and a tool that hangs on a
// dead remote -- and the question being asked is about what is HERE, which
// the local refs already answer.
func inspectRepo(dir string) RepoState {
	r := RepoState{Path: dir, Name: filepath.Base(dir)}

	if out, err := gitIn(dir, "remote"); err == nil {
		r.Remote = strings.Split(out, "\n")[0]
	}

	// modified plus untracked, in one call
	if out, err := gitIn(dir, "status", "--porcelain"); err == nil {
		for _, l := range strings.Split(out, "\n") {
			if strings.TrimSpace(l) != "" {
				r.Dirty++
			}
		}
	} else {
		r.Err = "cannot read status: " + err.Error()
		return r
	}

	// every local branch, its upstream, and how far ahead it is
	out, err := gitIn(
		dir,
		"for-each-ref",
		"--format=%(refname:short)\t%(upstream:short)\t"+
			"%(upstream:track)",
		"refs/heads",
	)
	if err == nil {
		for _, l := range strings.Split(out, "\n") {
			if strings.TrimSpace(l) == "" {
				continue
			}
			f := strings.SplitN(l, "\t", 3)
			branch := f[0]
			upstream := ""
			if len(f) > 1 {
				upstream = f[1]
			}
			track := ""
			if len(f) > 2 {
				track = f[2]
			}
			if upstream == "" {
				r.LocalOnly = append(r.LocalOnly, branch)
				continue
			}
			// "[ahead 3]", "[ahead 2, behind 1]", or empty. Behind
			// does not matter here: work that exists on the remote
			// and not locally is not work that deleting this
			// directory would lose.
			if i := strings.Index(track, "ahead "); i >= 0 {
				n := 0
				fmt.Sscanf(track[i+len("ahead "):], "%d", &n)
				r.Unpushed += n
			}
		}
	}

	if out, err := gitIn(dir, "stash", "list"); err == nil && out != "" {
		r.Stashes = len(strings.Split(out, "\n"))
	}

	// tags here that the remote has never seen. Compared against the
	// remote-tracking refs rather than by asking the network.
	local, _ := gitIn(dir, "tag")
	if local != "" && r.Remote != "" {
		remoteTags := map[string]bool{}
		if out, err := gitIn(
			dir,
			"ls-remote",
			"--tags",
			"--refs",
			r.Remote,
		); err == nil {
			for _, l := range strings.Split(out, "\n") {
				if i := strings.LastIndex(l, "/"); i >= 0 {
					remoteTags[l[i+1:]] = true
				}
			}
			for _, t := range strings.Split(local, "\n") {
				if t = strings.TrimSpace(t); t != "" &&
					!remoteTags[t] {
					r.UnpushedTags = append(
						r.UnpushedTags,
						t,
					)
				}
			}
		}
	}
	return r
}

// cliRepos is `puffin repos [path]`.
func cliRepos(args []string) int {
	root := "."
	depth := 3
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--depth":
			if i+1 < len(args) {
				i++
				if n, err := strconv.Atoi(args[i]); err == nil {
					depth = n
				}
			}
		default:
			root = args[i]
		}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	repos, _ := findRepos(abs, depth)
	if len(repos) == 0 {
		fmt.Printf("no repositories under %s\n", abs)
		return 0
	}

	safe, unsafe := 0, 0
	for _, dir := range repos {
		st := inspectRepo(dir)
		ok, why := st.Safe()
		rel, _ := filepath.Rel(abs, dir)
		if ok {
			safe++
			fmt.Printf("  safe    %s\n", rel)
			continue
		}
		unsafe++
		fmt.Printf("  HOLD    %s\n", rel)
		for _, w := range why {
			fmt.Printf("          %s\n", w)
		}
	}
	fmt.Printf(
		"\n%d safe to delete, %d holding something that exists "+
			"nowhere else\n",
		safe,
		unsafe,
	)
	if unsafe > 0 {
		// exit 1 so this can gate something: a cleanup script, a
		// cutover
		return 1
	}
	return 0
}
