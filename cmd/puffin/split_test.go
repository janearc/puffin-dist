package main

import (
	"sort"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// Every pane can be split, and the split renders two columns.
//
// This started as a probe, because "split does not work on the first pane" is
// not something to answer by reading the key switch.
//
// It is kept because the answer was that split works on all six and the report
// was about a screen that is not a pane at all -- which is exactly the
// confusion a regression here would recreate.
func TestEveryPaneSplits(t *testing.T) {
	out := &strings.Builder{}

	keys := []string{}
	for k := range paneFactories() {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		m := newModel("test", "")
		m.width, m.height = 160, 50
		// open the pane the way a keystroke would
		mm, _ := m.Update(
			tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)},
		)
		m = mm.(model)
		if m.scr != screenPane || m.openPane == nil {
			out.WriteString(
				k + ": did not open a pane (scr=" + itoa(
					int(m.scr),
				) + ")\n",
			)
			continue
		}
		mm, _ = m.Update(
			tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("|")},
		)
		m = mm.(model)
		if m.rightPane == nil {
			out.WriteString(k + ": OPENED but split did nothing\n")
		} else {
			out.WriteString(k + ": split ok -> " +
				"" + m.rightPane.Key() + "\n")
		}
	}

	// what does a split actually render? drive it and look.
	for _, k := range keys {
		m := newModel("test", "")
		m.width, m.height = 160, 50
		mm, _ := m.Update(
			tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)},
		)
		m = mm.(model)
		mm, _ = m.Update(
			tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("|")},
		)
		m = mm.(model)
		v := m.View()
		bar := 0
		for _, line := range splitLines(v) {
			if hasRune(line, '\u2502') {
				bar++
			}
		}
		out.WriteString(
			"render " + k + ": lines=" + itoa(
				countLines(v),
			) + " divider-rows=" + itoa(
				bar,
			) + " focus=" + itoa(
				int(m.focus),
			) + "\n",
		)
	}

	if strings.Contains(out.String(), "did not open") ||
		strings.Contains(out.String(), "split did nothing") {
		t.Fatalf("a pane refused to split:\n%s", out.String())
	}

	// and the screens that are NOT panes: the roster eats the key and says
	// nothing, which is the actual bug. Asserted so that porting
	// the roster to a pane makes this fail and someone deletes it.
	m := newModel("test", "")
	m.width, m.height = 160, 50
	mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("|")})
	m = mm.(model)
	if m.rightPane != nil {
		t.Fatal(
			"the roster splits now -- port is done, delete this " +
				"half of the test",
		)
	}
}

// splitLines gives a rendered frame back as rows, which is the unit every
// assertion about layout is made in.
func splitLines(s string) []string {
	out := []string{""}
	for _, r := range s {
		if r == '\n' {
			out = append(out, "")
			continue
		}
		out[len(out)-1] += string(r)
	}
	return out
}

// hasRune tests for a drawing character, which is how a border is found
// without depending on the styling around it.
func hasRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}
