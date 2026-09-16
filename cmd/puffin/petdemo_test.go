package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// The demo runs the SAME code the roster runs -- seeMouse on the way in, the
// real bird.Draw on the way out. A demo that renders its own version of the
// thing can pass while the product is broken, which makes it worse than no
// demo at all.

// demoModel is the demo wired the way the binary wires it, so the test
// runs the real path rather than a rehearsal of it.
func demoModel(t *testing.T) petDemo {
	t.Helper()
	resetPointer(t)
	t.Setenv("PUFFIN_BIRD", "1")
	b := newBird()
	SetOverlay(b)
	t.Cleanup(func() { SetOverlay(nil) })
	m := petDemo{b: b, w: 120, h: 40}
	return m
}

// The demo has to drive the same pointer and event path the console does,
// or it demonstrates something puffin does not do.
func TestPetDemoRecordsThroughTheRealPath(t *testing.T) {
	m := demoModel(t)
	next, _ := m.Update(
		tea.MouseMsg{X: 9, Y: 4, Action: tea.MouseActionMotion},
	)
	m = next.(petDemo)
	if p := MousePointer(); p.X != 9 || p.Y != 4 {
		t.Fatalf("the demo did not record the pointer: %d,%d", p.X, p.Y)
	}
	out := m.View()
	if !strings.Contains(out, "9, 4") {
		t.Error("the position is not on screen")
	}
	if !strings.Contains(out, "the pet is watching your cursor") {
		t.Error("the demo does not say the pet can see the cursor")
	}
}

// The three states of reporting are each stated in words, because that is
// the distinction the whole struct exists to make.
func TestPetDemoSaysWhichKindOfNothing(t *testing.T) {
	m := demoModel(t)
	if !strings.Contains(m.View(), "nothing can arrive") {
		t.Error("with the mouse given back the demo does not say so")
	}
	mouseReporting(true)
	if !strings.Contains(m.View(), "nothing has arrived yet") {
		t.Error("holding the mouse with no events is not distinguished")
	}
	next, _ := m.Update(
		tea.MouseMsg{X: 3, Y: 3, Action: tea.MouseActionMotion},
	)
	if !strings.Contains(next.(petDemo).View(), "last seen") {
		t.Error("a live pointer does not report its age")
	}
}

// M is the trade, in both directions, and the demo starts on the off side --
// the first thing it has to show is the difference.
func TestPetDemoTogglesTheMouse(t *testing.T) {
	m := demoModel(t)
	next, cmd := m.Update(key('M'))
	m = next.(petDemo)
	if !MousePointer().Reporting || cmd == nil {
		t.Fatal("M did not take the mouse")
	}
	if _, cmd = m.Update(key('M')); MousePointer().Reporting || cmd == nil {
		t.Fatal("M did not give it back")
	}
}

// A click is something happening, and something happening is what the corner
// reacts to -- through the same Handle the event stream uses.
func TestPetDemoClickStartlesThePet(t *testing.T) {
	m := demoModel(t)
	m.Update(
		tea.MouseMsg{
			X:      5,
			Y:      5,
			Button: tea.MouseButtonLeft,
			Action: tea.MouseActionPress,
		},
	)
	if !m.b.performing() {
		t.Error("a click did not reach the pet")
	}
}

// A short window says WHY there are no eyes, rather than looking broken: at
// eight rows a moving pupil does not survive and the gaze is stripped on
// purpose.
func TestPetDemoExplainsAShortWindow(t *testing.T) {
	m := demoModel(t)
	m.h = 18
	if !strings.Contains(m.View(), "too short for eyes") {
		t.Error("a short window does not explain the missing gaze")
	}
	m.h = 44
	if strings.Contains(m.View(), "too short for eyes") {
		t.Error("a tall window claims to be short")
	}
}

// A demo that cannot be closed is a demo that gets killed from another
// terminal.
func TestPetDemoQuits(t *testing.T) {
	m := demoModel(t)
	for _, k := range []rune{'q'} {
		next, cmd := m.Update(key(k))
		if cmd == nil {
			t.Fatalf("%c did not quit", k)
		}
		if !next.(petDemo).quit {
			t.Fatalf("%c did not mark the demo done", k)
		}
	}
}
