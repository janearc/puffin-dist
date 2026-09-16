package main

import (
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Where the pointer is, available from anywhere.
//
// The mouse reached exactly two places before this: the open pane's Click, and
// the wheel on the older screens.
//
// Everything else in puffin -- the corner, the splash, any pane that wants to
// know where you are looking rather than what you clicked -- had no way to ask,
// because the event was consumed by whichever screen happened to be up.
//
// So the event is recorded first and dispatched second. Recording is not a
// screen's business: a fact about the pointer is true whatever is drawn over
// it, and a consumer that has to be wired into the update loop to learn it
// is a consumer that will not be written.

// Pointer is where the mouse was last seen, and what it was doing.
type Pointer struct {
	// X and Y are terminal cells, from the top left of the window, which is
	// the only frame of reference every consumer shares.
	//
	// A pane's own coordinates are that pane's business -- see PaneXY --
	// and a consumer that stored pane coordinates would be holding a number
	// that means something different the moment the pane is closed.
	X, Y int
	// Button and Action are the last thing it did, verbatim from the
	// terminal.
	//
	// Kept rather than reduced to a bool, because "moved", "pressed" and
	// "released" are three different facts and a consumer deciding between
	// them should not have to guess which one a bool meant.
	Button tea.MouseButton
	Action tea.MouseAction
	// Seen is when. A pointer's position is a claim with an age on it: the
	// mouse can leave the window, the terminal can stop reporting, and a
	// consumer that draws a gaze at a two-minute-old position is drawing a
	// stare at nothing. See Fresh.
	Seen time.Time
	// Reporting says whether puffin is holding the mouse at all. When it is
	// false nothing arrives -- not motion, not clicks -- so a zero position
	// means "not watching", which is a different fact from "top left".
	Reporting bool
	// Width and Height are the terminal as it was when the pointer was
	// seen, so a consumer can work in fractions without asking the model
	// for a size that may since have changed.
	Width, Height int
	// Moves is how many events have been recorded. A consumer can tell a
	// live pointer from one that has been sitting still, without keeping
	// its own history.
	Moves int
}

// pointer is the live one. A mutex rather than a bare variable because the
// update loop writes it and anything drawing may read it, and the two are
// only the same goroutine by convention -- a convention no compiler checks
// and no future overlay is obliged to keep.
var pointer = struct {
	mu sync.Mutex
	at Pointer
}{}

// MousePointer is the pointer as it stands. A copy, deliberately: a consumer
// holding a reference to live state reads a different answer halfway through
// drawing a frame.
func MousePointer() Pointer {
	pointer.mu.Lock()
	defer pointer.mu.Unlock()
	return pointer.at
}

// seeMouse records an event. Called before the event is dispatched, so a
// screen that ignores the mouse still contributes what it was told.
func seeMouse(msg tea.MouseMsg, width, height int) {
	pointer.mu.Lock()
	defer pointer.mu.Unlock()
	pointer.at.X, pointer.at.Y = msg.X, msg.Y
	pointer.at.Button, pointer.at.Action = msg.Button, msg.Action
	pointer.at.Seen = time.Now()
	pointer.at.Width, pointer.at.Height = width, height
	pointer.at.Reporting = true
	pointer.at.Moves++
}

// mouseReporting records the trade being taken or given back.
//
// Turning reporting off does not move the pointer -- the last place it was seen
// is still the last place it was seen -- but it does mean nothing further will
// arrive, and a consumer is entitled to know that rather than watching a
// position quietly go stale.
func mouseReporting(on bool) {
	pointer.mu.Lock()
	defer pointer.mu.Unlock()
	pointer.at.Reporting = on
}

// pointerStale is how long a position stays worth acting on. Thirty seconds
// because the mouse is not the subject of this tool: you point at something,
// you read for a minute, and where the pointer was is no longer where you
// are looking.
const pointerStale = 30 * time.Second

// Fresh says whether the pointer is worth acting on: puffin is holding the
// mouse, something has actually arrived, and it was recent.
//
// The three are separate on purpose. Not reporting means nothing can arrive.
// Never seen means nothing has. Stale means it did, once, and the answer has
// aged out. A consumer that treats all three as "no" is right to; one that
// wants to tell them apart can.
func (p Pointer) Fresh() bool {
	return p.Reporting && !p.Seen.IsZero() &&
		time.Since(p.Seen) < pointerStale
}

// PaneXY converts to the open pane's own coordinates, which is what a pane
// means by a position: the frame draws a border and a title above the
// pane's body, and a pane that used raw terminal cells would be off by that
// much on every row.
//
// Negative means the pointer is outside the body, above it or to the left,
// and that is returned rather than clamped: a click in the frame is not a
// click on the first row, and clamping it into one is how a border becomes a
// button.
func (p Pointer) PaneXY() (x, y int) {
	return p.X - paneBodyLeft, p.Y - paneBodyTop
}

// Toward is the direction from a point to the pointer, as a unit-ish step in
// each axis: -1, 0 or 1. It is the shape a gaze wants -- "look left and
// down" -- rather than an angle, because the thing doing the looking is
// eight rows tall and has three positions per axis to say it with.
//
// A pointer that is not Fresh looks at nothing, which is how a pet stops
// staring at a corner the mouse left two minutes ago.
func (p Pointer) Toward(x, y int) (dx, dy int) {
	if !p.Fresh() {
		return 0, 0
	}
	return sign(p.X - x), sign(p.Y - y)
}

// sign reduces a wheel delta to a direction, since how far a wheel
// reports turning varies by terminal and only the direction is meant.
func sign(n int) int {
	switch {
	case n > 0:
		return 1
	case n < 0:
		return -1
	}
	return 0
}
