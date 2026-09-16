package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// freshState isolates a test from the package globals.
//
// uiState and touched are process-wide, and loadUIState leaves whatever was
// already in memory when the file is missing -- correct for a real start, where
// nothing was there, and cross-contamination between tests in one process.
//
// touched matters more than it looks: once a key has been written this session,
// mergeFromDisk deliberately stops reading it back, so a leftover
// touched["guest"] makes a later test see its own past.
func freshState(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	uiMu.Lock()
	prevState := uiState
	prevOn := uiOn
	prevTouched := map[string]bool{}
	for k, v := range touched {
		prevTouched[k] = v
	}
	uiState = UIState{}
	for k := range touched {
		delete(touched, k)
	}
	uiMu.Unlock()
	// PUT IT ALL BACK, and uiOn is the one that matters most.
	//
	// uiOn is false until loadUIState runs, so a test that never calls it
	// does not touch remembered state at all -- which is most of them, and
	// they rely on it without saying so.
	//
	// This helper calls loadUIState, which turns uiOn ON for the remainder
	// of the process. t.Setenv then restores HOME, so every later test
	// starts reading and writing the REAL ~/.local/state/puffin/ui.json.
	// The kube fold tests picked up the operator's own folds and failed.
	//
	// t.Setenv restores itself. Package globals do not, and this helper
	// touches three.
	t.Cleanup(func() {
		uiMu.Lock()
		uiState, uiOn = prevState, prevOn
		for k := range touched {
			delete(touched, k)
		}
		for k, v := range prevTouched {
			touched[k] = v
		}
		uiMu.Unlock()
	})
	loadUIState()
}

// The splash guest and the corner companion are two settings.
//
// They were one setting, so choosing a guest for the corner put the same
// guest on the splash.
func TestGuestIsSeparateFromTheCompanion(t *testing.T) {
	freshState(t)
	t.Setenv("PUFFIN_MASCOT", "")
	t.Setenv("PUFFIN_GUEST", "")

	rememberMascot("lamp")
	rememberGuest("gopher")

	if got := currentMascot(); got != "lamp" {
		t.Errorf("corner = %q, want lamp", got)
	}
	if got := guestName(); got != "gopher" {
		t.Errorf(
			"splash guest = %q, want gopher -- it followed the "+
				"corner",
			got,
		)
	}
}

// Unset, it behaves exactly as it did before: the guest follows the
// companion, except that an auklet companion gets a gopher guest, because
// two puffins on one splash is a reflection rather than a visitor.
func TestGuestDefaultsToTheOldBehaviour(t *testing.T) {
	freshState(t)
	t.Setenv("PUFFIN_GUEST", "")

	for _, c := range []struct{ mascot, want string }{
		{"auklet", "gopher"},
		{"lamp", "lamp"},
		{"lamp", "lamp"},
	} {
		t.Setenv("PUFFIN_MASCOT", c.mascot)
		if got := guestName(); got != c.want {
			t.Errorf(
				"companion %s -> guest %q, want %q",
				c.mascot,
				got,
				c.want,
			)
		}
	}
}

// The environment wins over the remembered value, like every other setting.
func TestGuestEnvOverridesRemembered(t *testing.T) {
	freshState(t)
	rememberGuest("gopher")
	t.Setenv("PUFFIN_GUEST", "lamp")
	if got := guestName(); got != "lamp" {
		t.Errorf("guest = %q, want lamp from the environment", got)
	}
}

// A hand-edited key this session has not touched must survive a save. That
// is what makes the file editable at all: puffin merges from disk before
// writing, gated on what it has changed itself.
func TestHandEditedGuestSurvivesASave(t *testing.T) {
	freshState(t)
	home := os.Getenv("HOME")
	t.Setenv("PUFFIN_GUEST", "")
	t.Setenv("PUFFIN_MASCOT", "")

	// puffin changes something else entirely and writes
	rememberTheme("vaporwave")

	// meanwhile a person edits the file and sets a guest
	path := uiStatePath()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no state written: %v", err)
	}
	var st map[string]any
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	st["guest"] = "lamp"
	nb, _ := json.Marshal(st)
	if err := os.WriteFile(
		filepath.Join(home, ".local", "state", "puffin", "ui.json"),
		nb,
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	// puffin writes again for an unrelated reason
	rememberTheme("corvid")

	b, _ = os.ReadFile(path)
	st = map[string]any{}
	json.Unmarshal(b, &st)
	if st["guest"] != "lamp" {
		t.Errorf("the hand-edited guest was clobbered: %v", st["guest"])
	}
	if st["theme"] != "corvid" {
		t.Errorf("theme = %v, want corvid", st["theme"])
	}
}
