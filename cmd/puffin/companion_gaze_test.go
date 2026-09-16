package main

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/janearc/puffin-auklet/auklet"
)

// TestGazeEveryDirection sweeps the whole domain Toward can produce, for
// every mascot, at every row count the corner can be asked for.
func TestGazeEveryDirection(t *testing.T) {
	os.Setenv("PUFFIN_BIRD", "1")
	b := newBird()
	SetOverlay(b)

	for _, name := range mascotNames() {
		sp, ok := spriteFor(name)
		if !ok {
			continue
		}
		b.setSprite(sp)
		for rows := 1; rows <= 24; rows++ {
			cols := sp.ColsFor(rows)
			gazeAll(t, b, sp, name, rows, cols)
		}
	}
}

// gazeAll walks every direction the eyes can look, including the nine that
// include not looking anywhere.
func gazeAll(
	t *testing.T, b *bird, sp auklet.Sprite, name string, rows, cols int,
) {
	t.Helper()
	for dx := -1; dx <= 1; dx++ {
		for dy := -1; dy <= 1; dy++ {
			gazeOnce(t, b, sp, name, rows, cols, dx, dy)
		}
	}
}

// gazeOnce draws one gaze and reports a panic as a failure naming every
// number that produced it. Its own function because five levels of loop
// leave a format string no room, and because the recover has to be the
// deferred call of the thing that might panic.
func gazeOnce(
	t *testing.T, b *bird, sp auklet.Sprite,
	name string, rows, cols, dx, dy int,
) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("PANIC Gaze mascot=%s rows=%d cols=%d "+
				"dx=%d dy=%d: %v", name, rows, cols, dx, dy, r)
		}
	}()
	pose := sp.Gaze(dx, dy)
	if rows < birdRowsEyes {
		pose = withoutGaze(pose)
	}
	_ = sp.CellsAt(
		b.themeFor(currentTheme()), auklet.Quadrant, cols, rows, pose)
}

// TestLivePointerThenCompanion is the sequence that broke: mouse is live,
// the gaze path is therefore active, then C cycles the character.
func TestLivePointerThenCompanion(t *testing.T) {
	os.Setenv("PUFFIN_BIRD", "1")
	SetOverlay(newBird())

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("PANIC live pointer then C: %v", r)
		}
	}()

	var m tea.Model = newModel("test", "")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 162, Height: 44})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})

	for _, at := range [][2]int{
		{0, 0},
		{161, 43},
		{80, 22},
		{161, 0},
		{0, 43},
		{158, 40},
	} {
		seeMouse(
			tea.MouseMsg{
				X:      at[0],
				Y:      at[1],
				Action: tea.MouseActionMotion,
			},
			162,
			44,
		)
		pointer.mu.Lock()
		pointer.at.Seen, pointer.at.Reporting = time.Now(), true
		pointer.mu.Unlock()
		for i := 0; i < 6; i++ {
			m, _ = m.Update(
				tea.KeyMsg{
					Type:  tea.KeyRunes,
					Runes: []rune{'C'},
				},
			)
			if v := m.View(); strings.Count(v, "\n") < 0 {
				t.Fatal("impossible")
			}
		}
	}
}
