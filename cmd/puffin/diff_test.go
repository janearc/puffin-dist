package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A diff that finds differences is a diff that worked. kubectl says so with
// exit status 1, and treating that as an error is the classic way to build a
// diff tool that reports failure every time it succeeds.
func TestDiffTreatsExitOneAsAnAnswer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nothing.yaml")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := diffManifests("k3d-definitely-not-a-cluster", path)
	// no such cluster: that IS an error, and must be reported as one rather
	// than as "no differences"
	if r.Err == "" && !r.Changed {
		t.Fatal("a diff against a missing cluster reported agreement")
	}
}

// A path that is not there fails before kubectl is involved, with the
// filesystem's own message.
func TestDiffRefusesAMissingPath(t *testing.T) {
	r := diffManifests("k3d-local", "/no/such/manifests")
	if r.Err == "" {
		t.Fatal("a missing path was accepted")
	}
	if r.Changed {
		t.Fatal("a missing path reported changes")
	}
}

// The summary counts content, not the diff's own headers: +++ and --- are
// file markers and counting them inflates every result by two per object.
func TestDiffSummaryIgnoresHeaders(t *testing.T) {
	text := strings.Join([]string{
		"diff -u -N /tmp/LIVE/a /tmp/MERGED/a",
		"--- /tmp/LIVE/a",
		"+++ /tmp/MERGED/a",
		"@@ -1,4 +1,4 @@",
		"-  replicas: 1",
		"+  replicas: 3",
		"   image: flipr:abc",
	}, "\n")
	added, removed, objects := diffSummary(text)
	if added != 1 || removed != 1 || objects != 1 {
		t.Fatalf(
			"summary is +%d -%d across %d, want +1 -1 across 1",
			added,
			removed,
			objects,
		)
	}
}

// Issue 41. One object kubectl cannot dry-run used to void the audit for
// the whole estate: `kubectl diff -f <tree>` aborts on the first failure
// and emits no hunks at all, so a single immutable PersistentVolume field
// meant puffin reported nothing about any of the other ten directories.
//
// The tree is walked piece by piece instead, so a failure is isolated to
// the piece that caused it. This runs against a context that does not
// exist, so every unit fails -- which is exactly the shape under test: the
// count of units, not their verdicts.
func TestOneUnComparablePieceDoesNotVoidTheTree(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"alpha", "beta", "gamma"} {
		dir := filepath.Join(root, d)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(dir, "x.yaml"),
			[]byte("kind: ConfigMap\n"),
			0o644,
		); err != nil {
			t.Fatal(err)
		}
	}
	tree := diffTree("k3d-definitely-not-a-cluster", root)
	if len(tree.Units) < 3 {
		t.Fatalf(
			"the tree collapsed to %d unit(s); each directory "+
				"should answer for itself: %+v",
			len(tree.Units),
			tree.Units,
		)
	}
	_, _, failed := tree.Counts()
	if failed == 0 {
		t.Error("a context that does not exist produced no failures")
	}
}

// A kustomize overlay is one unit, and its base is not a separate one.
//
// local' bus/base targets a namespace that exists only in the old network,
// so comparing it standalone reports an error on every run forever. A
// failure section with a permanent entry is a failure section nobody reads.
func TestKustomizeBasesAreComparedThroughTheirOverlay(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "bus", "base")
	overlay := filepath.Join(root, "bus", "overlays", "local")
	for _, d := range []string{base, overlay} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, body string) {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(
		filepath.Join(base, "kustomization.yaml"),
		"resources:\n  - kafka.yaml\n",
	)
	write(filepath.Join(base, "kafka.yaml"), "kind: StatefulSet\n")
	write(
		filepath.Join(overlay, "kustomization.yaml"),
		"resources:\n  - namespace.yaml\n  - ../../base\nnamespace: "+
			"local\n",
	)
	write(filepath.Join(overlay, "namespace.yaml"), "kind: Namespace\n")

	bases := kustomizeBases(root)
	if !bases[base] {
		t.Errorf("the base was not recognised as one: %v", bases)
	}
	if bases[overlay] {
		t.Error("the overlay was mistaken for a base")
	}

	for _, u := range diffTree("k3d-definitely-not-a-cluster", root).Units {
		if u.Path == base {
			t.Errorf("the base was compared standalone: %s", u.Path)
		}
	}
}

// The resources list ends at the next top-level key, or a namespace or an
// images block below it would be read as a path.
func TestKustomizeResourcesListEndsAtTheNextKey(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "kustomization.yaml"),
		[]byte("resources:\n  - sub\nimages:\n  - name: "+
			"a/b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bases := kustomizeBases(root)
	if !bases[sub] {
		t.Errorf("the resource path was missed: %v", bases)
	}
	if len(bases) != 1 {
		t.Errorf(
			"something below the resources list was read as a "+
				"path: %v",
			bases,
		)
	}
}
