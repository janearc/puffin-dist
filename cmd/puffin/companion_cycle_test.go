package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// C must reach every character, not just the second one.
//
// With PUFFIN_MASCOT set, only one C worked and after that it was stuck.
// It cycled from currentMascot(), which reads PUFFIN_MASCOT first and
// therefore never changes, so every press asked the same question and got
// the same answer.
//
// The environment sets who starts in the corner. It must not pin who can
// stand there afterwards.
func TestCReachesEveryCharacterWithTheEnvSet(t *testing.T) {
	t.Setenv("PUFFIN_BIRD", "1")
	t.Setenv(
		"PUFFIN_NO_STATE",
		"1",
	) // do not write the real remembered mascot
	t.Setenv("PUFFIN_MASCOT", "lamp")

	all := mascotNames()
	if len(all) < 3 {
		t.Skipf(
			"only %d characters; the bug needs at least three to "+
				"show",
			len(all),
		)
	}

	SetOverlay(newBird())
	var m tea.Model = newModel("test", "")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 162, Height: 44})

	seen := map[string]bool{standingMascot(): true}
	for i := 0; i < len(all)*2; i++ {
		m, _ = m.Update(
			tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'C'}},
		)
		seen[standingMascot()] = true
	}

	var missing []string
	for _, n := range all {
		if !seen[n] {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		t.Errorf(
			"pressing C %d times never reached %s (visited %d of "+
				"%d)",
			len(
				all,
			)*2,
			strings.Join(missing, ", "),
			len(seen),
			len(all),
		)
	}
}

// The pet screen has its own C handler, and it had its own copy of the same
// bug. Two call sites, one rule -- which is the shape of every bug found in
// this repository today.
func TestPetScreenCCyclesToo(t *testing.T) {
	t.Setenv("PUFFIN_BIRD", "1")
	t.Setenv("PUFFIN_NO_STATE", "1")
	t.Setenv("PUFFIN_MASCOT", "lamp")

	all := mascotNames()
	SetOverlay(newBird())
	var p tea.Model = petDemo{w: 162, h: 44}

	seen := map[string]bool{standingMascot(): true}
	for i := 0; i < len(all)*2; i++ {
		p, _ = p.Update(
			tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'C'}},
		)
		seen[standingMascot()] = true
	}
	if len(seen) < len(all) {
		t.Errorf(
			"the pet screen visited %d of %d characters",
			len(seen),
			len(all),
		)
	}
}
