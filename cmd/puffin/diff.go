package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Diffing what is on disk against what is running.
//
// There is a diff for this, and it is kubectl's own: `kubectl diff -f` does a
// server-side dry run and reports what applying would change.
//
// That matters more than a text diff would, because the answer accounts for
// defaulting, admission and mutating webhooks -- the cluster fills in fields
// your manifest never mentioned, and a plain text comparison reports every one
// of them as drift. What comes back here is what would actually change.
//
// This is the read half of the deploy story. Puffin could already say
// which build was running and which was declared; it could not say what was
// different about them.

// DiffResult is one comparison.
type DiffResult struct {
	Path    string
	Context string
	// Changed is true when the cluster and the disk disagree. kubectl says
	// so with exit status 1, which is not an error and must not be treated
	// as one -- that conflation is the classic way to make a diff tool
	// report failure every time it finds its answer.
	Changed bool
	Text    string
	Err     string
}

// isKustomizeRoot reports whether a directory is built by kustomize rather
// than being a pile of manifests.
func isKustomizeRoot(dir string) bool {
	for _, n := range []string{
		"kustomization.yaml",
		"kustomization.yml",
		"Kustomization",
	} {
		if st, err := os.Stat(filepath.Join(dir, n)); err == nil &&
			!st.IsDir() {
			return true
		}
	}
	return false
}

// diffManifests runs kubectl diff for a path.
func diffManifests(kubeCtx, path string) DiffResult {
	r := DiffResult{Path: path, Context: kubeCtx}
	if _, err := os.Stat(path); err != nil {
		r.Err = err.Error()
		return r
	}
	// -k for a kustomize root, -f for anything else. This is not a nicety.
	//
	// Feeding an overlay's files to -f individually compares patch
	// fragments against live objects, which reports drift that does not
	// exist and misses the drift that does
	//
	// -- the overlay's own kustomization.yaml also fails outright, because
	// "Kustomization" is not a kind the API server has ever heard of.
	//
	// Both were happening here before this: five drifted units, two of
	// which were fragments.
	args := []string{"--context", kubeCtx, "diff"}
	if st, err := os.Stat(path); err == nil && st.IsDir() &&
		isKustomizeRoot(path) {
		args = append(args, "-k", path)
	} else {
		args = append(args, "-f", path, "-R")
	}
	cmd := exec.Command("kubectl", args...)
	out, err := cmd.CombinedOutput()
	r.Text = strings.TrimRight(string(out), "\n")
	switch {
	case err == nil:
		// exit 0: no differences. An empty diff is a real answer and
		// the screen says so in words rather than showing nothing.
		return r
	case cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == 1:
		r.Changed = true
		return r
	default:
		r.Err = err.Error()
		if r.Text != "" {
			r.Err += ": " + firstLine(r.Text)
		}
		return r
	}
}

// firstLine is the useful part of a multi-line error.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// diffSummary counts the lines that actually changed, so a screen can say
// "14 lines across 3 objects" before showing any of them.
func diffSummary(text string) (added, removed, objects int) {
	for _, l := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(l, "diff -u -N"):
			objects++
		case strings.HasPrefix(l, "+++") || strings.HasPrefix(l, "---"):
			// file headers, not content
		case strings.HasPrefix(l, "+"):
			added++
		case strings.HasPrefix(l, "-"):
			removed++
		}
	}
	return added, removed, objects
}

// DiffUnit is one independently comparable piece of a tree.
//
// The unit exists because kubectl does not have one. `kubectl diff -f <tree>`
// is all-or-nothing: one object it cannot dry-run aborts the run and emits no
// hunks at all, so a single immutable field takes the answer for the whole
// estate with it.
//
// Splitting the tree up is the only way to get a partial answer out of a tool
// that does not believe in them.
type DiffUnit struct {
	Path    string
	Changed bool
	Text    string
	// Err is the third outcome, and the one that did not exist before: not
	// clean, not drifted, could not be compared. A resource kubectl refuses
	// to dry-run is a real answer about that resource and belongs in the
	// report; it was previously the absence of a report.
	Err string
}

// TreeDiff is a whole manifest tree, compared piece by piece.
type TreeDiff struct {
	Root    string
	Context string
	Units   []DiffUnit
}

// Counts returns clean, drifted and uncomparable units.
func (t TreeDiff) Counts() (clean, drifted, failed int) {
	for _, u := range t.Units {
		switch {
		case u.Err != "":
			failed++
		case u.Changed:
			drifted++
		default:
			clean++
		}
	}
	return
}

// kustomizeBases finds the directories that other kustomizations build ON.
//
// A base is a building block, not a deployable. A bus/base that targets a
// namespace which does not exist in this cluster is doing its job -- correctly,
// because only the overlay retargets it -- so diffing the base against the
// cluster reports "namespaces fleet not found" on every single run, forever.
//
// A report with a permanent entry in its failure section is a report whose
// failure section stops being read, which for a freeze audit is worse than not
// having one.
//
// So a directory named as a resource by another kustomization is skipped as
// a standalone unit. It is still compared -- through the overlay that
// actually deploys it, which is the only form anybody applies.
//
// This reads the resources list textually rather than parsing YAML: the
// entries that matter are relative paths on their own line, and puffin does
// not carry a YAML parser for one field.
func kustomizeBases(root string) map[string]bool {
	bases := map[string]bool{}
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if n := d.Name(); n != "kustomization.yaml" &&
			n != "kustomization.yml" &&
			n != "Kustomization" {
			return nil
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		dir := filepath.Dir(p)
		in := false
		for _, line := range strings.Split(string(body), "\n") {
			t := strings.TrimSpace(line)
			switch {
			case strings.HasPrefix(t, "resources:"):
				in = true
				continue
			case in && strings.HasPrefix(t, "- "):
				ref := strings.TrimSpace(
					strings.TrimPrefix(t, "- "),
				)
				if ref == "" {
					continue
				}
				// what makes it a base is that it resolves to a
				// directory.
				//
				// An earlier version also required a slash, to
				// keep bare filenames out -- which silently
				// missed "- sub", a perfectly ordinary sibling
				// reference.
				//
				// The stat below was always the real test and
				// the extra one only had false negatives to
				// offer.
				abs := filepath.Clean(filepath.Join(dir, ref))
				if st, err := os.Stat(abs); err == nil &&
					st.IsDir() {
					bases[abs] = true
				}
			case in && t != "" && !strings.HasPrefix(
				t,
				"#",
			) && !strings.HasPrefix(t, "-"):
				in = false // a new top-level key ends the list
			}
		}
		return nil
	})
	return bases
}

// diffTree compares a manifest tree against the cluster, one piece at a time.
//
// It tries each top-level directory on its own, and only when one of those
// fails does it descend and try that directory's files individually. That
// ordering keeps the common case to one kubectl invocation per directory
// while still isolating a failure down to the file that caused it.
//
// A path with no subdirectories is compared whole, which is the single-file
// and flat-directory case.
func diffTree(kubeCtx, root string) TreeDiff {
	return diffTreeSkipping(kubeCtx, root, kustomizeBases(root))
}

// diffTreeSkipping is diffTree with the base set already computed, so the
// walk that finds them happens once for a tree rather than once per level.
func diffTreeSkipping(kubeCtx, root string, bases map[string]bool) TreeDiff {
	t := TreeDiff{Root: root, Context: kubeCtx}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Units = append(
			t.Units,
			DiffUnit{Path: root, Err: err.Error()},
		)
		return t
	}
	// a kustomize root is ONE unit whatever is inside it: kustomize decides
	// what the tree means, and walking past it to the files underneath
	// throws that decision away.
	if isKustomizeRoot(root) {
		r := diffManifests(kubeCtx, root)
		t.Units = append(
			t.Units,
			DiffUnit{
				Path:    root,
				Changed: r.Changed,
				Text:    r.Text,
				Err:     r.Err,
			},
		)
		return t
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(root, e.Name()))
		}
	}
	if len(dirs) == 0 {
		r := diffManifests(kubeCtx, root)
		t.Units = append(
			t.Units,
			DiffUnit{
				Path:    root,
				Changed: r.Changed,
				Text:    r.Text,
				Err:     r.Err,
			},
		)
		return t
	}
	for _, d := range dirs {
		if bases[d] {
			continue // compared through the overlay that deploys it
		}
		if isKustomizeRoot(d) {
			r := diffManifests(kubeCtx, d)
			t.Units = append(
				t.Units,
				DiffUnit{
					Path:    d,
					Changed: r.Changed,
					Text:    r.Text,
					Err:     r.Err,
				},
			)
			continue
		}
		r := diffManifests(kubeCtx, d)
		if r.Err == "" {
			t.Units = append(
				t.Units,
				DiffUnit{
					Path:    d,
					Changed: r.Changed,
					Text:    r.Text,
				},
			)
			continue
		}
		// the directory failed as a whole: find out whether it is one
		// file or all of them, because "bus cannot be compared" and
		// "one PersistentVolume in bus cannot be compared" are
		// different reports
		t.Units = append(
			t.Units,
			diffFiles(kubeCtx, d, r.Err, bases)...)
	}
	return t
}

// diffFiles compares a directory's manifests one file at a time.
//
// fallbackErr is what the whole directory said, used when the directory has
// no files to descend into -- an empty descent must not turn a failure into
// silence.
func diffFiles(
	kubeCtx, dir, fallbackErr string,
	bases map[string]bool,
) []DiffUnit {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []DiffUnit{{Path: dir, Err: err.Error()}}
	}
	var out []DiffUnit
	for _, e := range entries {
		if e.IsDir() {
			sub := filepath.Join(dir, e.Name())
			if bases[sub] {
				continue
			}
			out = append(
				out,
				diffTreeSkipping(kubeCtx, sub, bases).Units...)
			continue
		}
		// kustomize control files are directives, not manifests. -f on
		// one fails with "no matches for kind Kustomization", which is
		// true and useless: the directory it governs is diffed with -k
		// instead.
		if n := e.Name(); n == "kustomization.yaml" ||
			n == "kustomization.yml" ||
			n == "Kustomization" {
			continue
		}
		if ext := strings.ToLower(
			filepath.Ext(e.Name()),
		); ext != ".yaml" &&
			ext != ".yml" &&
			ext != ".json" {
			continue
		}
		f := filepath.Join(dir, e.Name())
		r := diffManifests(kubeCtx, f)
		out = append(
			out,
			DiffUnit{
				Path:    f,
				Changed: r.Changed,
				Text:    r.Text,
				Err:     r.Err,
			},
		)
	}
	if len(out) == 0 {
		return []DiffUnit{{Path: dir, Err: fallbackErr}}
	}
	return out
}

// rel shortens a path for the report, so a column of them reads.
func rel(root, p string) string {
	if r, err := filepath.Rel(root, p); err == nil &&
		!strings.HasPrefix(r, "..") {
		return r
	}
	return p
}

// cliDiff is `puffin diff <path>`.
//
// Exit status is the count of units that are not clean, capped at 125. Zero
// means every piece of the tree was compared and every piece matched, which is
// freeze condition 4 answered with a number.
//
// That is cassowary's freeze-audit convention rather than diff's, deliberately:
// this is an audit that happens to print a diff, and "exits with the count"
// layers into a suite unchanged.
func cliDiff(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(
			os.Stderr,
			"usage: puffin diff <path to manifests>",
		)
		fmt.Fprintln(
			os.Stderr,
			"compares what is on disk against what the cluster "+
				"is running",
		)
		fmt.Fprintln(
			os.Stderr,
			"exit status is the number of pieces that drifted or "+
				"could not be compared",
		)
		return 2
	}
	t := diffTree(kubeContext(), args[0])
	clean, drifted, failed := t.Counts()

	fmt.Printf("# %s vs %s\n", t.Root, t.Context)
	fmt.Printf(
		"# %d compared · %d clean · %d drifted · %d could not be "+
			"compared\n\n",
		clean+drifted,
		clean,
		drifted,
		failed,
	)

	if drifted > 0 {
		fmt.Println("drifted:")
		for _, u := range t.Units {
			if u.Err != "" || !u.Changed {
				continue
			}
			added, removed, objects := diffSummary(u.Text)
			fmt.Printf(
				"  %-28s +%d -%d across %d object(s)\n",
				rel(t.Root, u.Path),
				added,
				removed,
				objects,
			)
		}
		fmt.Println()
	}
	// the failures are named and the reason is given, because a resource
	// that cannot be diffed is a finding about that resource
	if failed > 0 {
		fmt.Println("could not be compared:")
		for _, u := range t.Units {
			if u.Err == "" {
				continue
			}
			fmt.Printf(
				"  %-28s %s\n",
				rel(t.Root, u.Path),
				firstLine(u.Err),
			)
		}
		fmt.Println()
	}
	for _, u := range t.Units {
		if u.Err == "" && u.Changed && strings.TrimSpace(u.Text) != "" {
			fmt.Printf("# %s\n%s\n\n", rel(t.Root, u.Path), u.Text)
		}
	}
	return minInt(drifted+failed, 125)
}
