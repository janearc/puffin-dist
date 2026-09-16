package main

import (
	"testing"

	"github.com/janearc/puffin-auklet/auklet"
)

// how many cells does a pose actually change, at a given height?
func delta(t *testing.T, rows int, pose auklet.Pose) int {
	th, _ := aukletTheme(currentTheme())
	cols := auklet.SideView.ColsFor(rows)
	base := auklet.SideView.CellsAt(th, auklet.Quadrant, cols, rows, nil)
	got := auklet.SideView.CellsAt(th, auklet.Quadrant, cols, rows, pose)
	n := 0
	for y := range base {
		for x := range base[y] {
			if base[y][x] != got[y][x] {
				n++
			}
		}
	}
	return n
}

// the derived pose, not a drawn one: WideEyes finds the eye the way Gaze does
// and redraws it larger around its own centre, so a sprite nobody in this repo
// drew gets surprise as well. puffin's hand-drawn proof is gone -- it was six
// lines of role letters that made the argument, and the argument is won.
func wideEyesPose() auklet.Pose { return auklet.SideView.WideEyes(1.6) }

// A scaled eye out-reads a pupil shift at every size, and by the most where
// it matters most.
//
// The measurement, cells changed against the resting bird:
//
//	rows   gaze  blink  wide
//	   8      1      3     6
//	  11      3      3    11
//	  14      4      6    14
//	  22      5      8    24
//
// The corner bird is eight rows on a short terminal, where a look changes two
// cells and flickers, and a scaled eye changes four.
//
// Surprise is legible at a size nothing else eye-related survives -- and it
// needs no transform, no scaling machinery and no new mechanism, because a
// bigger eye is just a bigger patch in a pose.
//
// The reason it is allowed at all is the rule the whole sprite obeys: a part
// may move as far as there is art behind it, and the eye is surrounded by drawn
// cheek. The beak is the counter-example and has none.
//
// One correction came back from rendering it, and it is the part worth keeping:
// scaling the socket pixel by pixel thickens the RING too, and it reads as
// goggles. What reads as alarm is a THIN ring at a larger radius with the pupil
// filling most of it.
//
// The pupil growing with the ring is what earns the cells; the ring is the part
// that must not be magnified.
//
// The test guards the finding, not the drawing.
func TestScaledEyeOutReadsGazeAtEightRows(t *testing.T) {
	const rows = 8
	gaze := delta(t, rows, auklet.SideView.Gaze(-1, 0))
	wide := delta(t, rows, wideEyesPose())
	if wide <= gaze {
		t.Fatalf(
			"at %d rows a scaled eye changes %d cells and a gaze "+
				"%d; "+
				"the scaled eye is meant to be the legible one",
			rows,
			wide,
			gaze,
		)
	}
}
