package main

import (
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// Where something can be drawn on top.
//
// puffin-auklet is a sprite with a canvas that composites in cell space, and
// it is right that it does: styled strings carry no geometry, so there is
// nothing to clip a sprite against. Compositing has to happen in cells and
// the string has to be produced once, at the end.
//
// This file does NOT import it. The other side is moving -- art and
// articulation are being worked on right now -- and a seam that pins itself to
// an API mid-change is a seam that breaks twice a day.
//
// So puffin defines what it needs and an adapter satisfies it later, in one
// file, with the auklet on one side and this on the other.
//
// What puffin promises an overlay:
//
//   - the frame it has just rendered, and the size of it
//   - a clock, at a cadence the overlay asks for
//   - every transition, as an Event
//
// What an overlay promises puffin:
//
//   - Draw returns a frame the same shape it was given. An overlay that
//     changes the line count moves the whole interface under the operator.
//   - Draw is fast. It runs on the update loop, once per frame.
//   - Nothing it does is required for the tool to work. Puffin renders
//     correctly with no overlay at all, and must keep doing so.
type Overlay interface {
	// Draw composites onto an already-rendered frame.
	Draw(frame string, width, height int) string
	// Tick is how often it wants to be woken. Zero means never: a parked
	// sprite that blinks twice a minute should not ask for 15fps, and a
	// terminal is not a game engine.
	Tick() time.Duration
	// Handle receives transitions, so a sprite can emote at what the tool
	// is actually doing rather than on a timer.
	Handle(Event)
}

// overlayTick wakes the overlay.
type overlayTick struct{}

// theOverlay is whatever is drawing on top, or nil. One at a time on
// purpose: two things compositing over the same frame is a layout argument
// nobody wins.
var theOverlay Overlay

// SetOverlay installs one, and subscribes it to events.
func SetOverlay(o Overlay) {
	theOverlay = o
	if o != nil {
		Listen(o.Handle)
	}
}

// overlayEnabled says whether puffin will draw one at all. Off is a
// first-class answer: a bird animating in the corner while somebody reads a
// stack trace at three in the morning is a bird they kill the tool over.
func overlayEnabled() bool {
	if v := os.Getenv("PUFFIN_BIRD"); v != "" {
		return v != "0" && v != "off"
	}
	return rememberedBird()
}

// drawOverlay is the one call site. Everything puffin renders goes through
// paneFrame or a view that ends the same way, so an overlay reaches every
// screen from here without a single view knowing it exists.
// drawOverlay is also where the frame is clamped to the terminal.
//
// It is the one funnel every full-screen view passes through, which makes it
// the only place that can promise nothing runs off the edge.
//
// Before this, the pane path clamped and the full-screen path did not, so at 96
// columns every row of the cluster screen was 108 wide and wrapped -- and the
// agents pane emitted 26 lines into a 14-line terminal.
//
// That is not a cosmetic bug. Anybody with tired eyes resizes the FONT, and a
// bigger font is a smaller terminal: the tool you reach for when you are least
// able to read it was the one that came apart.
//
// A view that lays itself out badly at 54 columns is a thing to fix on its own,
// but it must never be able to scribble outside the window while we get there.
//
// Clamping happens before the overlay draws, so the bird composites against
// the frame that will actually be shown rather than one that is wider than
// the screen.
func drawOverlay(frame string, width, height int) string {
	frame = clampFrame(frame, width, height)
	if theOverlay == nil || !overlayEnabled() {
		return frame
	}
	out := theOverlay.Draw(frame, width, height)
	// an overlay that changes the frame's shape is refused rather than
	// trusted: the alternative is the interface jumping by a line and
	// nobody knowing which of the two things caused it
	if countLines(out) != countLines(frame) {
		return frame
	}
	return out
}

// countLines is how tall a rendered block is, which the clamp needs
// before it can decide what will not fit.
func countLines(s string) int {
	n := 1
	for _, r := range s {
		if r == '\n' {
			n++
		}
	}
	return n
}

// tickOverlay schedules the overlay's next wake-up at whatever cadence it asks
// for right now, which is the point of asking every time:
//
// a bird at rest wants a second between frames and a bird mid-emote wants a
// tenth, and the alternative is running the fast clock all day for the four
// seconds a minute it is needed.
//
// A disabled or absent overlay stops the clock rather than spinning on it.
func tickOverlay() tea.Cmd {
	if theOverlay == nil || !overlayEnabled() {
		return nil
	}
	d := theOverlay.Tick()
	if d <= 0 {
		return nil
	}
	return tea.Tick(d, func(time.Time) tea.Msg { return overlayTick{} })
}

// clampFrame trims a rendered frame to the terminal, in both directions.
//
// Width first, because a wrapped line costs a row and would make the height
// trim wrong. Truncation carries an ellipsis so a cut line says it was cut;
// the height trim cannot, so it drops from the bottom, where the help
// footer lives rather than the data.
func clampFrame(frame string, width, height int) string {
	if width <= 0 && height <= 0 {
		return frame
	}
	lines := strings.Split(frame, "\n")
	if width > 0 {
		for i, l := range lines {
			if ansi.StringWidth(l) > width {
				lines[i] = ansi.Truncate(l, width, "\u2026")
			}
		}
	}
	if height > 0 && len(lines) > height {
		lines = lines[:height]
	}
	return strings.Join(lines, "\n")
}
