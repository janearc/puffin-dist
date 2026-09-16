package main

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// crowdedRoster is the shape that broke: fourteen services and twenty-seven
// furnishings, in a terminal 44 rows tall. That is a real enclave at a
// readable font size.
func crowdedRoster() model {
	m := newModel("test", "")
	m.width, m.height, m.loading = 162, 44, false
	for i := 0; i < 14; i++ {
		m.enclave.Services = append(m.enclave.Services, Service{
			Name: fmt.Sprintf(
				"svc%02d",
				i,
			), State: StateHealthy, Flags: -1,
		})
	}
	for i := 0; i < 27; i++ {
		name := fmt.Sprintf("furn%02d", i)
		m.furnishings = append(m.furnishings, Furnishing{
			Name:      name,
			Namespace: "local",
			Ready:     1,
			Want:      1,
			Kind:      "Deployment",
			Containers: []Container{
				{
					Name:  name,
					Image: "docker.io/" + name + ":v1",
				},
			},
		})
	}
	return m
}

// helpLines picks the menu out of a rendered frame. Keyed on the text of
// the keys themselves rather than a row number, because the row the help
// lands on is the thing under test.
func helpLines(frame string) []string {
	var out []string
	for _, line := range strings.Split(ansi.Strip(frame), "\n") {
		if strings.Contains(line, "enter: api") ||
			strings.Contains(line, "v: host") ||
			strings.Contains(line, "t: theme") ||
			strings.Contains(line, "e: poke") {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return out
}

// Issue 21. The front page ran off the bottom of the terminal, and the
// frame clamp answers an overlong frame by dropping lines from the bottom
// -- which is where the menu is. Nothing was broken except that every key
// the screen advertises had become undiscoverable.
//
// The assertion is the menu, not the line count: a screen that fits by
// losing its help has not been fixed.
func TestRosterKeepsItsMenuWhenTheClusterIsFull(t *testing.T) {
	m := crowdedRoster()
	frame := rosterView(m)

	if n := len(strings.Split(frame, "\n")); n > m.height {
		t.Errorf(
			"the frame is %d lines in a %d-line terminal",
			n,
			m.height,
		)
	}
	if help := helpLines(frame); len(help) != 4 {
		t.Errorf(
			"the menu is %d lines, want 4:\n%s",
			len(help),
			ansi.Strip(frame),
		)
	}
	if !strings.Contains(ansi.Strip(frame), thisBuild().Line()) {
		t.Error("the build line went off the bottom with the menu")
	}
}

// A body that does not fit says so, in numbers. "More below" does not tell
// you whether it is two more or two hundred, and the operator deciding
// whether to scroll is deciding on exactly that.
func TestRosterSaysWhatIsOffTheEnds(t *testing.T) {
	m := crowdedRoster()
	out := ansi.Strip(rosterView(m))
	if !strings.Contains(out, "below") {
		t.Errorf(
			"the roster does not say anything is below the "+
				"fold:\n%s",
			out,
		)
	}

	// and says nothing when there is nothing to say: a permanent note that
	// zero rows are hidden is furniture
	tall := crowdedRoster()
	tall.height = 200
	if o := ansi.Strip(rosterView(tall)); strings.Contains(o, "below") {
		t.Error(
			"a roster that fits still claims there is something " +
				"below it",
		)
	}
}

// The furnishings are not selectable, so a cursor that stopped at the last
// service would leave everything under it unreachable. Down keeps going.
func TestRosterScrollsPastTheLastService(t *testing.T) {
	m := crowdedRoster()
	// walk the cursor to the last service: the view has not moved yet,
	// because the services fit
	for i := 0; i < len(m.enclave.Services)-1; i++ {
		m = m.rosterScroll(1)
	}
	if m.cursor != len(m.enclave.Services)-1 {
		t.Fatalf("cursor is on service %d, want the last one", m.cursor)
	}
	if m.rosterOffset != 0 {
		t.Fatalf(
			"the view moved while the cursor was still in the "+
				"services: offset %d",
			m.rosterOffset,
		)
	}

	// past the end, the page moves instead
	before := m.rosterOffset
	m = m.rosterScroll(1)
	if m.rosterOffset <= before {
		t.Fatalf(
			"down at the last service did nothing: offset %d",
			m.rosterOffset,
		)
	}

	// and it can be walked all the way to the last furnishing
	for i := 0; i < 200; i++ {
		m = m.rosterScroll(1)
	}
	if out := ansi.Strip(rosterView(m)); !strings.Contains(out, "furn26") {
		t.Errorf("the last furnishing cannot be reached:\n%s", out)
	}
	// the menu is still there at the bottom of the list, which is where
	// you are most likely to have forgotten which key gets you out
	if help := helpLines(rosterView(m)); len(help) != 4 {
		t.Errorf(
			"the menu is %d lines once scrolled, want 4",
			len(help),
		)
	}
}

// pgdn moves the window and leaves the selection alone. Scrolling to look
// at something is not the same act as choosing it, and a page key that
// dragged the cursor along would lose the service you were about to open.
func TestRosterPageLeavesTheCursorAlone(t *testing.T) {
	m := crowdedRoster()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	m = next.(model)
	if m.cursor != 0 {
		t.Errorf("pgdown moved the cursor to %d", m.cursor)
	}
	if m.rosterOffset == 0 {
		t.Error("pgdown did not move the window")
	}
	back, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	if back.(model).rosterOffset != 0 {
		t.Errorf(
			"pgup did not come back to the top: offset %d",
			back.(model).rosterOffset,
		)
	}
}

// The header measures itself. It grows a line for every warning the enclave
// is carrying, which is to say it is tallest exactly when losing the menu
// hurts most -- a constant subtracted from the height would have been wrong
// there and nowhere else.
func TestRosterWindowShrinksAsWarningsArrive(t *testing.T) {
	m := crowdedRoster()
	quiet := m.rosterWindow()
	m.enclave.Warnings = []string{"one", "two", "three"}
	loud := m.rosterWindow()
	if loud != quiet-3 {
		t.Errorf("three warnings cost %d rows, want 3", quiet-loud)
	}
	if help := helpLines(rosterView(m)); len(help) != 4 {
		t.Errorf(
			"the menu is %d lines with warnings up, want 4",
			len(help),
		)
	}
}
