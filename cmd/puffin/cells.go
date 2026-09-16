package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/janearc/puffin-auklet/canvas"
)

// Compositing the bird ONTO the text, rather than into a hole cut for it.
//
// The bird composites on top of other text, which is the whole trick.
//
// The first arrangement reserved a strip on the right and let the panes
// render narrower. That works and it is a lie: the bird is not on top of
// anything, it is standing in a space nobody was allowed to use. It also
// costs every pane twenty columns forever.
//
// The honest version needs the frame as cells, and puffin renders to a styled
// string, which is why this did not exist. So: parse the frame back into cells,
// but only the rectangle the bird occupies.
//
// Everything left of the splice column is untouched bytes, so the rest of the
// screen is byte-identical to what it was -- what is reparsed is the twenty
// columns the bird was going to draw over anyway.
//
// The transparency is the same trick as before and needs no alpha: a quadrant
// glyph carries a foreground and a background, so a cell that is half bird
// takes the bird as its foreground and whatever the TEXT had as its background.
//
// Where the bird has no glyph at all, the text's own cell is written back
// unchanged, escape sequences and all.

// styledCell is one terminal cell as it appeared in a rendered frame: the
// rune, and the SGR sequence in force when it was written.
type styledCell struct {
	r   rune
	sgr string // "" means default
}

// cellsOf parses one rendered line into cells.
//
// It tracks SGR state rather than parsing colours, which is the point: an
// unrecognised attribute -- bold, italic, underline, a 24-bit colour, an
// extension nobody here has heard of -- survives untouched, because it is
// carried as the bytes it was written as and written back the same way.
//
// Parsing colours into lipgloss types would flatten every attribute this code
// does not know about, and puffin's log highlighting is full of them.
func cellsOf(line string) []styledCell {
	var out []styledCell
	sgr := ""
	for i := 0; i < len(line); {
		if line[i] == 0x1b {
			// consume the whole escape sequence
			j := i + 1
			for j < len(line) && line[j] != 'm' && line[j] != 0x1b {
				j++
			}
			if j < len(line) && line[j] == 'm' {
				seq := line[i : j+1]
				if seq == "\x1b[0m" || seq == "\x1b[m" {
					sgr = ""
				} else {
					sgr += seq
				}
				i = j + 1
				continue
			}
			i++
			continue
		}
		r, size := decodeRune(line[i:])
		out = append(out, styledCell{r: r, sgr: sgr})
		i += size
	}
	return out
}

// renderCells writes cells back out, emitting a style only when it changes.
func renderCells(cells []styledCell) string {
	var b strings.Builder
	cur := ""
	for _, c := range cells {
		if c.sgr != cur {
			if cur != "" {
				b.WriteString("\x1b[0m")
			}
			b.WriteString(c.sgr)
			cur = c.sgr
		}
		b.WriteRune(c.r)
	}
	if cur != "" {
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

// decodeRune is utf8.DecodeRuneInString without the import ceremony.
func decodeRune(s string) (rune, int) {
	for i, r := range s {
		if i > 0 {
			return []rune(s[:i])[0], i
		}
		_ = r
	}
	rs := []rune(s)
	if len(rs) == 0 {
		return ' ', 1
	}
	return rs[0], len(string(rs[0]))
}

// styleFor renders one bird cell with its own colours, over whatever
// background the text underneath was using.
//
// bg is the SGR the text cell carried. The bird's foreground is set; its
// background is set only when the sprite asked for one. That is what makes
// the half-covered cells read as transparent: the glyph is the bird and the
// ground stays the page's.
func styleFor(c canvas.Cell, under string) string {
	st := lipgloss.NewStyle()
	if c.FG != nil {
		st = st.Foreground(c.FG)
	}
	if c.BG != nil {
		st = st.Background(c.BG)
	}
	out := st.Render(string(c.R))
	if c.BG == nil && under != "" {
		// keep the text's ground under a glyph that did not bring one
		return under + out
	}
	return out
}

// compositeCorner draws bird cells over the bottom-right of a rendered
// frame, preserving every cell the bird does not cover.
//
// Unlike the splice it replaces, a row whose text reaches into the bird's
// columns is NOT skipped -- that skip is why panes had no bird at all. The text
// under the bird is overwritten where the bird has a glyph and kept everywhere
// else. cornerAt is where the corner sprite stands, in the frame's own cells.
//
// It exists so the drawing and anything that needs to KNOW where it is -- the
// gaze, which has to point from the sprite towards the pointer -- agree.
//
// Two copies of this arithmetic would drift the first time the margin changed,
// and the symptom would be a bird looking slightly past your cursor, which is
// the kind of wrong nobody can quite name.
func cornerAt(frameLines, w, h, width int) (x, y int, ok bool) {
	x = width - w - 2
	y = frameLines - h - 1
	return x, y, frameLines >= h+1 && x >= 1 && y >= 0
}

// compositeCorner splices the sprite into an already-rendered frame,
// touching only the rectangle it occupies. Everything left of the splice
// column is passed through as bytes, escape sequences and all.
func compositeCorner(
	frame string,
	cells [][]canvas.Cell,
	w, h, width int,
) string {
	lines := strings.Split(frame, "\n")
	if len(cells) == 0 {
		return frame
	}
	start, top, ok := cornerAt(len(lines), w, h, width)
	if !ok {
		return frame
	}
	for y := 0; y < h && y < len(cells); y++ {
		row := lines[top+y]
		tc := cellsOf(row)
		// pad the row out to the bird's columns: a short row is most of
		// them, and without this the bird would only appear on rows
		// that happened to be long enough
		for len(tc) < start+w {
			tc = append(tc, styledCell{r: ' '})
		}
		var b strings.Builder
		b.WriteString(renderCells(tc[:start]))
		for x := 0; x < w && x < len(cells[y]); x++ {
			c := cells[y][x]
			under := tc[start+x]
			if c.R == 0 || c.R == ' ' {
				b.WriteString(renderCells([]styledCell{under}))
				continue
			}
			b.WriteString(styleFor(c, under.sgr))
		}
		b.WriteString(renderCells(tc[start+w:]))
		lines[top+y] = b.String()
	}
	return strings.Join(lines, "\n")
}

// visibleWidthOf is ansi.StringWidth, named for what the callers want.
func visibleWidthOf(s string) int { return ansi.StringWidth(s) }
