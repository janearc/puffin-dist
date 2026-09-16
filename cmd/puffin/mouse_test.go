package main

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The pointer, available anywhere.
//
// The mouse reached exactly two places before this -- the open pane's Click,
// and the wheel on the older screens -- because every branch of the event
// consumed it. Anything that wanted to KNOW where the pointer was, rather
// than what it clicked, had no way to ask.

// resetPointer clears the shared pointer state between tests, since it is
// a package global and a stale position reads as a mouse holding still.
func resetPointer(t *testing.T) {
	t.Helper()
	pointer.mu.Lock()
	pointer.at = Pointer{}
	pointer.mu.Unlock()
	t.Cleanup(func() {
		pointer.mu.Lock()
		pointer.at = Pointer{}
		pointer.mu.Unlock()
	})
}

// It is recorded before dispatch, so a screen that ignores the mouse still
// contributes what it was told. The splash swallows every event; the roster
// takes only the wheel; a pane with no clicker drops it. All three must
// still move the pointer.
func TestEveryScreenContributesToThePointer(t *testing.T) {
	for _, tc := range []struct {
		name string
		scr  screen
	}{
		{"splash", screenSplash},
		{"roster", screenRoster},
		{"detail", screenDetail},
	} {
		resetPointer(t)
		m := baseModel()
		m.scr = tc.scr
		m.Update(
			tea.MouseMsg{
				X:      42,
				Y:      7,
				Action: tea.MouseActionMotion,
			},
		)
		p := MousePointer()
		if p.X != 42 || p.Y != 7 {
			t.Errorf(
				"%s: the pointer is at %d,%d",
				tc.name,
				p.X,
				p.Y,
			)
		}
		if p.Moves != 1 {
			t.Errorf("%s: %d events recorded", tc.name, p.Moves)
		}
	}
}

// What it was doing is kept verbatim, because "moved", "pressed" and
// "released" are three different facts.
func TestThePointerKeepsWhatItWasDoing(t *testing.T) {
	resetPointer(t)
	m := baseModel()
	m.Update(
		tea.MouseMsg{
			X:      1,
			Y:      2,
			Button: tea.MouseButtonLeft,
			Action: tea.MouseActionPress,
		},
	)
	p := MousePointer()
	if p.Button != tea.MouseButtonLeft || p.Action != tea.MouseActionPress {
		t.Fatalf("button %v action %v", p.Button, p.Action)
	}
	if p.Width != m.width || p.Height != m.height {
		t.Errorf(
			"the terminal size was not recorded: %dx%d",
			p.Width,
			p.Height,
		)
	}
	if p.Seen.IsZero() {
		t.Error(
			"the pointer has no timestamp, so nothing can tell " +
				"how old it is",
		)
	}
}

// Three kinds of "no", and they are not the same: not reporting means
// nothing CAN arrive, never seen means nothing HAS, stale means it did once
// and the answer has aged out.
func TestFreshTellsTheThreeKindsOfNoApart(t *testing.T) {
	resetPointer(t)
	if MousePointer().Fresh() {
		t.Error("a pointer nothing has ever been reported to is fresh")
	}
	// seen, and reporting
	m := baseModel()
	m.Update(tea.MouseMsg{X: 5, Y: 5, Action: tea.MouseActionMotion})
	if !MousePointer().Fresh() {
		t.Fatal("a pointer that just moved is not fresh")
	}
	// the trade given back: the position stands, but nothing further will
	// arrive and a consumer is entitled to know
	mouseReporting(false)
	p := MousePointer()
	if p.Fresh() {
		t.Error("fresh with mouse reporting off")
	}
	if p.X != 5 || p.Y != 5 {
		t.Error("giving the mouse back moved the pointer")
	}
	// and age
	mouseReporting(true)
	pointer.mu.Lock()
	pointer.at.Seen = time.Now().Add(-pointerStale - time.Second)
	pointer.mu.Unlock()
	if MousePointer().Fresh() {
		t.Error("a pointer older than the staleness window is fresh")
	}
}

// M records the trade in both directions, so nothing has to infer it.
func TestTogglingTheMouseIsRecorded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	resetPointer(t)
	m := baseModel()
	m.mouse = false
	next, _ := m.Update(key('M'))
	if !MousePointer().Reporting {
		t.Fatal("taking the mouse was not recorded")
	}
	next.(model).Update(key('M'))
	if MousePointer().Reporting {
		t.Fatal("giving the mouse back was not recorded")
	}
}

// Toward is the shape a gaze wants: three positions per axis, because the
// thing doing the looking is eight rows tall.
func TestTowardPointsAtThePointer(t *testing.T) {
	resetPointer(t)
	m := baseModel()
	m.Update(tea.MouseMsg{X: 100, Y: 10, Action: tea.MouseActionMotion})
	p := MousePointer()

	for _, tc := range []struct {
		name           string
		x, y           int
		wantDX, wantDY int
	}{
		{"pointer is right and below", 50, 5, 1, 1},
		{"pointer is left and above", 150, 20, -1, -1},
		{"same column", 100, 20, 0, -1},
		{"same cell", 100, 10, 0, 0},
	} {
		dx, dy := p.Toward(tc.x, tc.y)
		if dx != tc.wantDX || dy != tc.wantDY {
			t.Errorf(
				"%s: got %d,%d want %d,%d",
				tc.name,
				dx,
				dy,
				tc.wantDX,
				tc.wantDY,
			)
		}
	}

	// a stale pointer is looked at by nobody
	mouseReporting(false)
	if dx, dy := MousePointer().Toward(0, 0); dx != 0 || dy != 0 {
		t.Errorf(
			"a pointer that cannot arrive was still followed: "+
				"%d,%d",
			dx,
			dy,
		)
	}
}

// A pane means its own coordinates by a position, and the frame draws a
// border and a title above the body. Outside the body is negative rather
// than clamped: a click in the frame is not a click on the first row.
func TestPaneCoordinatesAreOffsetNotClamped(t *testing.T) {
	resetPointer(t)
	m := baseModel()
	m.Update(
		tea.MouseMsg{
			X:      paneBodyLeft + 3,
			Y:      paneBodyTop + 4,
			Action: tea.MouseActionMotion,
		},
	)
	x, y := MousePointer().PaneXY()
	if x != 3 || y != 4 {
		t.Fatalf("pane coordinates %d,%d", x, y)
	}
	m.Update(tea.MouseMsg{X: 0, Y: 0, Action: tea.MouseActionMotion})
	if x, y := MousePointer().PaneXY(); x >= 0 || y >= 0 {
		t.Errorf(
			"a position in the frame clamped into the body: %d,%d",
			x,
			y,
		)
	}
}

// The pet follows the pointer, and only when its face is free.
func TestThePetLooksAtThePointer(t *testing.T) {
	resetPointer(t)
	t.Setenv("PUFFIN_MASCOT", "")
	t.Setenv("PUFFIN_BIRD", "1")
	b := newBird()
	SetOverlay(b)
	t.Cleanup(func() { SetOverlay(nil) })

	frame := ""
	for i := 0; i < 40; i++ {
		frame += strings.Repeat(" ", 120) + "\n"
	}
	// the pointer far to the left of the corner: the pet should look that
	// way
	m := baseModel()
	m.Update(tea.MouseMsg{X: 2, Y: 2, Action: tea.MouseActionMotion})
	looking := b.Draw(frame, 120, 40)

	// and with the mouse given back, it looks at nothing
	mouseReporting(false)
	resting := b.Draw(frame, 120, 40)
	if looking == resting {
		t.Error(
			"the pet drew the same thing whether or not it could " +
				"see the pointer",
		)
	}

	// while performing, the emote owns the face and the gaze does not fight
	// it: two things driving the same cells is what the library's Part
	// mechanism exists to prevent, and the cheapest way to honour it is not
	// to start the fight
	mouseReporting(true)
	m.Update(tea.MouseMsg{X: 2, Y: 2, Action: tea.MouseActionMotion})
	b.play(b.sprite, "surprised")
	mid := b.Draw(frame, 120, 40)
	if mid == looking {
		t.Error("an emote did not take the face from the gaze")
	}
}
