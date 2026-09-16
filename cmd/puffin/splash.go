package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The splash: a full screen of ascii puffin on startup, and a gopher.
// Requirements are requirements.

// puffinRows is the bird, drawn from the reference photo: side profile facing
// left, dark cap and back carried by dense shading, the white face disc and
// breast by light shading, and the great striped wedge of beak as its own layer
// so it wears the orange.
//
// Each row is (beak segment, plumage segment); the renderer colors them
// separately.
var puffinRows = []struct{ beak, body string }{
	{"", "              ▄▄▄▄▓▓▓▓▓▓▓▓▓▄▄▄"},
	{"", "          ▄▄▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▄"},
	{"", "        ▄▓▓▓▓▓▓░░░░░░░░░░░▓▓▓▓▓▓▓▄"},
	{"", "       ▓▓▓▓▓░░░░░░░░░░░░░░░░▓▓▓▓▓▓▓"},
	{"", "      ▓▓▓▓░░░░░░░ ◕ ░░░░░░░░░▓▓▓▓▓▓▓"},
	{"    ▄▄▖", "▓▓▓░░░░░░░░░░░░░░░░░░░▓▓▓▓▓▓▓"},
	{" ▄▟████▙", "▓▓░░░░░░░░░░░░░░░░░░░▓▓▓▓▓▓▓"},
	{"▟██▛▟███▙", "▓░░░░░░░░░░░░░░░░░░░▓▓▓▓▓▓▓"},
	{"▜█▟██▛▜██▌", "░░░░░░░░░░░░░░░░░░▓▓▓▓▓▓▓▓"},
	{" ▜██▛ ▟█▛", "▝░░░░░░░░░░░░░░▄▄▓▓▓▓▓▓▓▓▓"},
	{"  ▜▛  ▀▘", " ░▀▄▄░░░░░▄▄▄▓▓▓▓▓▓▓▓▓▓▓▓"},
	{"", "         ░░░▀▀▀▀▀░░▓▓▓▓▓▓▓▓▓▓▓▓"},
	{"", "          ░░░░░░░░░▓▓▓▓▓▓▓▓▓▓▓"},
	{"", "           ░░░░░░░░▓▓▓▓▓▓▓▓▓▓"},
	{"", "            ░░░░░░░▓▓▓▓▓▓▓▓▓"},
	{"", "             ░░░░░░▓▓▓▓▓▓▓▓"},
}

// renderPuffin composes the bird in its actual colors: black plumage, white
// face and belly, orange beak. Painted per-rune because the truth of a puffin
// is per-feather -- dense glyphs are the black, light shading is the white, and
// painting them one colour makes an inverted bird.
//
// The splash bird is painted in the puffin's OWN colours, not the theme's.
//
// It used to take s.Beak, s.Snow and s.Plumage, which meant vaporwave drew a
// pink-capped bird and stardew a brown one. Puffins are black and their faces
// are white; a theme getting that wrong is the same bug as inverting it,
// arriving by a different route.
//
// A portrait is not a widget: this one is a full screen of bird and owes the
// page nothing.
func renderPuffin(s Styles) string {
	s = splashInk(s)
	var b strings.Builder
	for i, r := range puffinRows {
		if r.beak != "" {
			b.WriteString(s.Beak.Render(r.beak))
		}
		b.WriteString(paintPlumage(s, r.body))
		if i < len(puffinRows)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// paintPlumage walks one row and colors runs by what they depict: light
// shading and ticks are the white face and belly, the eye is dark on white,
// and everything dense is black feather.
func paintPlumage(s Styles, row string) string {
	var b strings.Builder
	var run []rune
	var runSnow bool
	flush := func() {
		if len(run) == 0 {
			return
		}
		if runSnow {
			b.WriteString(s.Snow.Render(string(run)))
		} else {
			b.WriteString(s.Plumage.Render(string(run)))
		}
		run = run[:0]
	}
	for _, r := range row {
		snow := r == '░' || r == '▝' || r == '◕' || r == ' '
		if snow != runSnow {
			flush()
			runSnow = snow
		}
		run = append(run, r)
	}
	flush()
	return b.String()
}

// gopherArt is the maybe-gopher. Small, because it is a guest in a seabird's
// terminal.
const gopherArt = `
      ,_---~~~~~----._
   _,,_,*^____      _____'*g*\"*,
  / __/ /'     ^.  /      \ ^@q   f
 [  @f | @))    |  | @))   l  0 _/
  \'/   \~____ / __ \_____/    \
   |           _l__l_           I
   }          [______]           I
   ]            | | |            |
   ]             ~ ~             |
   |                            |
    |                           |`

// splashView composes the full-screen splash: the bird, the name arriving
// one letter at a time, a prompt that blinks the way prompts blinked in the
// 90s, and the gopher paying its respects in the corner. Tastefully
// animated means exactly two effects and no more.
func splashView(s Styles, width, height, frame int) string {
	// the auklet's front view when there is room and the theme survives
	// validation; the hand-drawn bird otherwise. Both are the same shape to
	// the layout below -- a block of lines to be centred -- so nothing else
	// on this screen knows which one it got.
	bird := renderPuffin(s)
	if b, ok := splashBird(currentTheme(), width, height, frame); ok {
		bird = b
	}

	// the typewriter, which now waits for the bird. The name starts after
	// the pan and the first rawr have landed: a wordmark typing itself
	// while the bird is still turning is two things asking for your eye at
	// once, and the bird wins that fight anyway.
	const nameStarts = 16
	full := "P U F F I N"
	n := (frame - nameStarts) * 2
	if n < 0 {
		n = 0
	}
	if n > len(full) {
		n = len(full)
	}
	title := s.Title.Render(
		full[:n],
	) + s.Dim.Render(
		strings.Repeat("░", (len(full)-n)/2),
	)

	tag := s.Subtitle.Render(
		"a tui for the enclave — every service that publishes an " +
			"api. which is to say, all of them.",
	)

	// the blink: solid until the name lands, then the classic slow flash
	hint := s.Help.Render("press any key to fly")
	if n >= len(full) && frame%8 < 3 {
		hint = strings.Repeat(
			" ",
			lipgloss.Width("press any key to fly"),
		)
	}
	// the guest, animated when there is room and the theme survives
	// validation, hand-drawn otherwise. Same bargain the bird makes, and
	// the same reason: both are a block of lines to the layout below, so
	// nothing else on this screen knows which one it got.
	gopher := paintGopher(s, gopherArt)
	if g, ok := splashGuest(currentTheme(), width, height, frame); ok {
		gopher = g
	}

	main := lipgloss.JoinVertical(
		lipgloss.Center,
		bird,
		"",
		title,
		tag,
		"",
		hint,
	)

	// the gopher gets a reserved strip at the bottom right when the
	// terminal has room for guests.
	//
	// Overlaying it onto the centered layout was the first two attempts,
	// and both left the guest partially eaten by the bird's own rows -- a
	// reserved seat is the only arrangement where the gopher keeps its
	// head.
	gl := strings.Split(gopher, "\n")
	if width > 110 && height > len(gl)+30 {
		upper := lipgloss.Place(
			width,
			height-len(gl),
			lipgloss.Center,
			lipgloss.Center,
			main,
		)
		seat := lipgloss.PlaceHorizontal(
			width-2,
			lipgloss.Right,
			gopher,
		)
		return upper + "\n" + seat
	}
	page := lipgloss.Place(
		width,
		height,
		lipgloss.Center,
		lipgloss.Center,
		main,
	)
	return page
}

// paintGopher shades the guest in two blues, the same way the bird is
// painted by what each glyph depicts rather than by which line it is on.
//
// The split is features against strokes. Line art in one colour reads as a
// tangle of punctuation -- which is what the gopher was, rendered flat in
// the dim token -- and the thing that makes it resolve into a face is the
// eyes and teeth coming forward off the body.
//
// Both blues are constant across every theme, like the beak. A puffin
// without an orange beak is a guillemot; a gopher that is not Go blue is
// some other rodent.
func paintGopher(s Styles, art string) string {
	var b strings.Builder
	var run []rune
	var runLight bool
	flush := func() {
		if len(run) == 0 {
			return
		}
		if runLight {
			b.WriteString(s.GopherHi.Render(string(run)))
		} else {
			b.WriteString(s.Gopher.Render(string(run)))
		}
		run = run[:0]
	}
	for _, r := range art {
		// newlines are written raw: lipgloss pads a multi-line render
		// out to its widest line, which would give the gopher a
		// rectangle of trailing spaces and shove it off its own seat
		if r == '\n' {
			flush()
			b.WriteRune(r)
			continue
		}
		light := isGopherFeature(r)
		if light != runLight {
			flush()
			runLight = light
		}
		run = append(run, r)
	}
	flush()
	return b.String()
}

// isGopherFeature says whether a glyph is a face rather than a stroke: the
// eyes and their pupils, the nose, and the teeth.
func isGopherFeature(r rune) bool {
	switch r {
	case '@', ')', '0', 'q', 'g', 'l', '[', ']', 'I', 'f':
		return true
	}
	return false
}

// splashInk swaps the three styles the bird is painted with for the
// reference bird's own ink, and leaves every other style alone -- the title,
// the version line and the help underneath still belong to the theme,
// because they are interface and the bird is not.
func splashInk(s Styles) Styles {
	t, ok := referenceTheme()
	if !ok {
		return s
	}
	s.Plumage = s.Plumage.Foreground(t.Dark)
	s.Snow = s.Snow.Foreground(t.Light)
	s.Beak = s.Beak.Foreground(t.BeakTip)
	return s
}
