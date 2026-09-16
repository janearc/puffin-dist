package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// A selection anchored at one line and extended upward covers the same
// lines as one extended downward. Getting that backwards yanks the wrong
// half of a log.
func TestVisualSpanRunsBothWays(t *testing.T) {
	v := visual{on: true, anchor: 10}
	if lo, hi := v.span(4); lo != 4 || hi != 10 {
		t.Fatalf("upward span is %d..%d, want 4..10", lo, hi)
	}
	if lo, hi := v.span(14); lo != 10 || hi != 14 {
		t.Fatalf("downward span is %d..%d, want 10..14", lo, hi)
	}
	off := visual{}
	if lo, hi := off.span(7); lo != 7 || hi != 7 {
		t.Fatalf(
			"with no selection the span is %d..%d, want the "+
				"cursor alone",
			lo,
			hi,
		)
	}
}

// Both ends of a selection are inside it. An off-by-one here drops the
// line somebody was actually after.
func TestVisualCovers(t *testing.T) {
	v := visual{on: true, anchor: 3}
	for _, i := range []int{3, 4, 5} {
		if !v.covers(i, 5) {
			t.Fatalf(
				"line %d is inside 3..5 and was not covered",
				i,
			)
		}
	}
	if v.covers(6, 5) || v.covers(2, 5) {
		t.Fatal("the selection covers a line outside its span")
	}
}

// v extends with the motion keys, and y ends the selection whatever it did
// with the clipboard -- a headless host has none, and the mode must not be
// left switched on because of it.
func TestLogsVisualKeys(t *testing.T) {
	p := &logsPane{}
	p.v.Lines = make([]LogLine, 20)
	p.cursor = 5

	key := func(s string) {
		var msg tea.KeyMsg
		if s == "esc" {
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		} else {
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
		}
		np, _ := p.Update(msg)
		p = np.(*logsPane)
	}

	key("v")
	if !p.vis.on || p.vis.anchor != 5 {
		t.Fatalf(
			"v did not anchor at the cursor: on=%v anchor=%d",
			p.vis.on,
			p.vis.anchor,
		)
	}
	key("j")
	key("j")
	if lo, hi := p.vis.span(p.cursor); lo != 5 || hi != 7 {
		t.Fatalf("after two j the span is %d..%d, want 5..7", lo, hi)
	}
	key("esc")
	if p.vis.on {
		t.Fatal("esc did not leave visual mode")
	}
	key("v")
	key("y")
	if p.vis.on {
		t.Fatal("y left the pane in visual mode")
	}
	if p.yanked == "" {
		t.Fatal("y said nothing about what it did")
	}
	key("j")
	if p.yanked != "" {
		t.Fatal("the yank receipt outlived the next keystroke")
	}
}

// what lands on the clipboard is the log, not the rendering.
func TestRawLinesCarryNoStyling(t *testing.T) {
	p := &logsPane{}
	p.v.Lines = []LogLine{
		{Pod: "flipr-0", NS: "local", Stream: "stdout", Text: "hello"},
	}
	raw := p.rawLines()
	if len(raw) != 1 {
		t.Fatalf("got %d lines", len(raw))
	}
	if strings.Contains(raw[0], "\x1b[") {
		t.Fatalf("an escape sequence reached the clipboard: %q", raw[0])
	}
	for _, want := range []string{"local", "flipr-0", "hello"} {
		if !strings.Contains(raw[0], want) {
			t.Fatalf("the yanked line lost %q: %q", want, raw[0])
		}
	}
}
