package main

import (
	"os"
	"path/filepath"
	"testing"
)

// enableUIState turns persistence on against a temp directory, the way main
// turns it on against the real one.
func enableUIState(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	t.Setenv("PUFFIN_NO_STATE", "")
	uiMu.Lock()
	uiState = UIState{
		Version: uiStateVersion,
		Folds:   map[string]map[string]bool{},
	}
	uiMu.Unlock()
	loadUIState()
	t.Cleanup(func() {
		uiMu.Lock()
		uiOn = false
		uiState = UIState{Version: uiStateVersion}
		uiMu.Unlock()
	})
	return dir
}

// Preferences survive a restart. This is a cookie, not a cache of truth:
// it records which namespaces this operator does not want to look at, which
// cannot be wrong about the estate because it claims nothing about it.
func TestFoldsSurviveARestart(t *testing.T) {
	dir := enableUIState(t)
	rememberFold("k3d-local", "kube-system", true)
	rememberFold("k3d-local", "canary", true)
	if _, err := os.Stat(
		filepath.Join(dir, "puffin", "ui.json"),
	); err != nil {
		t.Fatalf("nothing was written: %v", err)
	}
	// a fresh process reads it back
	uiMu.Lock()
	uiState = UIState{Version: uiStateVersion}
	uiMu.Unlock()
	loadUIState()
	folds := applyFolds("k3d-local", map[string]bool{})
	if !folds["kube-system"] || !folds["canary"] {
		t.Fatalf("folds did not survive: %+v", folds)
	}
}

// Remembered decisions are layered OVER the rule's defaults, never instead
// of them: a namespace that appears tomorrow gets the rule rather than
// inheriting somebody's answer about a different one.
func TestRememberedFoldsLayerOverDefaults(t *testing.T) {
	enableUIState(t)
	rememberFold("k3d-local", "local", true) // deliberately closed
	defaults := map[string]bool{
		// the rule says namespaces open
		"local":                  false,
		"local/kestrel":          true, // and big prefix groups closed
		"a-namespace-from-today": false,
	}
	got := applyFolds("k3d-local", defaults)
	if !got["local"] {
		t.Error("the remembered decision was ignored")
	}
	if !got["local/kestrel"] {
		t.Error("the rule's default was lost")
	}
	if got["a-namespace-from-today"] {
		t.Error(
			"a new namespace inherited an opinion nobody " +
				"expressed about it",
		)
	}
}

// Folds are per cluster: folding kube-system in local says nothing about
// another cluster, and carrying it across would move an answer over the
// dev/prod boundary.
func TestFoldsDoNotCrossClusters(t *testing.T) {
	enableUIState(t)
	rememberFold("k3d-local", "kube-system", true)
	if got := applyFolds(
		"k3d-fleet",
		map[string]bool{"kube-system": false},
	); got["kube-system"] {
		t.Fatal("a fold crossed from one cluster to another")
	}
}

// PUFFIN_THEME is a statement about THIS run and outranks last time.
func TestEnvironmentOutranksTheRememberedTheme(t *testing.T) {
	enableUIState(t)
	rememberTheme("vaporwave")
	if m := newModel("test", ""); m.styles.Theme != "vaporwave" {
		t.Fatalf("the remembered theme was ignored: %q", m.styles.Theme)
	}
	if m := newModel(
		"test",
		"bladerunner",
	); m.styles.Theme != "bladerunner" {
		t.Fatalf(
			"the environment lost to a preference: %q",
			m.styles.Theme,
		)
	}
}

// Nothing is written until main turns persistence on -- a test that drives
// the update loop must not touch the operator's real preferences.
func TestPersistenceIsOffUntilItIsTurnedOn(t *testing.T) {
	uiMu.Lock()
	uiOn = false
	uiState = UIState{Version: uiStateVersion}
	uiMu.Unlock()
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	rememberFold("k3d-local", "kube-system", true)
	rememberTheme("vaporwave")
	rememberAgentSort(2, true)
	if _, err := os.Stat(
		filepath.Join(dir, "puffin", "ui.json"),
	); err == nil {
		t.Fatal(
			"preferences were written by a process that never " +
				"opted in",
		)
	}
	if got := applyFolds(
		"k3d-local",
		map[string]bool{"kube-system": false},
	); got["kube-system"] {
		t.Fatal(
			"a fold leaked through the package while persistence " +
				"was off",
		)
	}
}

// A file from a future puffin is discarded, not guessed at.
func TestAnUnknownStateShapeIsDiscarded(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	t.Setenv("PUFFIN_NO_STATE", "")
	if err := os.MkdirAll(filepath.Join(dir, "puffin"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "puffin", "ui.json")
	if err := os.WriteFile(
		path,
		[]byte(`{"version":99,"theme":"nyancat"}`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	uiMu.Lock()
	uiState = UIState{Version: uiStateVersion}
	uiMu.Unlock()
	loadUIState()
	t.Cleanup(func() { uiMu.Lock(); uiOn = false; uiMu.Unlock() })
	if rememberedTheme() == "nyancat" {
		t.Fatal(
			"a state file from a shape puffin does not know was " +
				"believed",
		)
	}
	// and a corrupt file is not an error either
	if err := os.WriteFile(path, []byte(`{not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	loadUIState()
}

// Two puffins, running at once, which is the normal case for a tool people
// like. Neither may erase the other's preference, and neither needs to know
// the other exists -- they share a little state rather than coordinating.
func TestTwoInstancesDoNotClobberEachOther(t *testing.T) {
	dir := enableUIState(t)
	path := filepath.Join(dir, "puffin", "ui.json")

	// instance A folds a namespace
	rememberFold("k3d-local", "kube-system", true)

	// instance B started earlier and knows nothing about that fold. It
	// cycles the theme, which before the merge wrote its stale map over
	// A's work and lost the fold with nothing to notice.
	uiMu.Lock()
	uiState = UIState{
		Version: uiStateVersion,
		Folds:   map[string]map[string]bool{},
	}
	touched = map[string]bool{}
	uiMu.Unlock()
	rememberTheme("vaporwave")

	// a third process reads what is on disk
	uiMu.Lock()
	uiState = UIState{Version: uiStateVersion}
	touched = map[string]bool{}
	uiMu.Unlock()
	loadUIState()
	if got := applyFolds(
		"k3d-local",
		map[string]bool{},
	); !got["kube-system"] {
		t.Fatal("one instance's theme change erased another's fold")
	}
	if rememberedTheme() != "vaporwave" {
		t.Fatal("the theme did not survive")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("nothing on disk: %v", err)
	}
}

// A notification is claimed once across processes: a daemon watching /OOM/
// and a tui in tmux both match, and two notifications for one event is how
// a notifier gets muted.
func TestNotificationsAreClaimedOnce(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	if !claimNotification("logs:/OOM/") {
		t.Fatal("the first claim was refused")
	}
	// a second process, same subject, inside the window
	if claimNotification("logs:/OOM/") {
		t.Fatal("the same subject was claimed twice")
	}
	// a different subject is not blocked by it
	if !claimNotification("logs:/panic/") {
		t.Fatal("an unrelated subject was blocked")
	}
	// and an unreadable store answers YES rather than falling silent: a
	// missed notification is worse than a duplicated one
	t.Setenv("XDG_CACHE_HOME", "/dev/null/nope")
	if !claimNotification("logs:/OOM/") {
		t.Fatal("an unusable store silenced a notification")
	}
}

// PUFFIN_MASCOT is a statement about THIS run and outranks last time -- the
// same rule TestEnvironmentOutranksTheRememberedTheme checks for the theme.
// This checks the other half: that the choice actually survives a restart,
// the way TestFoldsSurviveARestart checks it for folds.
func TestMascotPersistsAcrossRestart(t *testing.T) {
	dir := enableUIState(t)
	rememberMascot("gopher")
	if _, err := os.Stat(
		filepath.Join(dir, "puffin", "ui.json"),
	); err != nil {
		t.Fatalf("nothing was written: %v", err)
	}
	// a fresh process reads it back
	uiMu.Lock()
	uiState = UIState{Version: uiStateVersion}
	uiMu.Unlock()
	loadUIState()
	if got := rememberedMascot(); got != "gopher" {
		t.Fatalf("the mascot did not survive: got %q", got)
	}
}
