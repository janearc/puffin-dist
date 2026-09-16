package main

import (
	"strings"

	"github.com/atotto/clipboard"
)

// Selecting text without the mouse.
//
// vim bindings, and then the mouse does not matter.
//
// That is the right answer and not a workaround.
//
// While puffin holds the mouse the terminal never sees a drag, so copying
// breaks -- and the fix everyone reaches for is to give the mouse back, which
// trades away the wheel and the clicks to buy something the keyboard was always
// better at.
//
// vim solved this in 1976 and every pair of hands that will ever use puffin
// already knows the keys.
//
// The model is visual-LINE only. Character-wise selection in a pane whose lines
// are styled, truncated and highlighted would be selecting from the rendering
// rather than from the log, and what lands on the clipboard would carry escape
// sequences and end at whatever column the terminal happened to be.
//
// Lines are what a log is made of, and lines are what you paste into a ticket.
type visual struct {
	on     bool
	anchor int // where v was pressed; the cursor is the other end
}

// span returns the inclusive line range currently selected, low to high.
func (v visual) span(cursor int) (int, int) {
	if !v.on {
		return cursor, cursor
	}
	if v.anchor <= cursor {
		return v.anchor, cursor
	}
	return cursor, v.anchor
}

// covers says whether a line is inside the selection, for the renderer.
func (v visual) covers(i, cursor int) bool {
	if !v.on {
		return false
	}
	lo, hi := v.span(cursor)
	return i >= lo && i <= hi
}

// yank puts lines on the system clipboard and says what it did.
//
// The RAW lines, never the rendered ones: what you paste is what the service
// logged, with no highlighting, no truncation and no escape sequences. A
// paste that carries colour codes into a ticket is worse than no paste.
func yank(lines []string, lo, hi int) string {
	if len(lines) == 0 {
		return "nothing to yank"
	}
	lo = clampInt(lo, 0, len(lines)-1)
	hi = clampInt(hi, 0, len(lines)-1)
	text := strings.Join(lines[lo:hi+1], "\n")
	if err := clipboard.WriteAll(text); err != nil {
		// loud, and specific: on a headless host there is no clipboard
		// at all, and "yanked" would be a lie told quietly
		return "no clipboard: " + err.Error()
	}
	n := hi - lo + 1
	if n == 1 {
		return "yanked 1 line"
	}
	return "yanked " + itoa(n) + " lines"
}
