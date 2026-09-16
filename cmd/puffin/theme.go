package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The palette. Baked from dodo's own token set -- the same CSS custom
// properties the admin pane paints with -- so the terminal and the web read
// as one estate.
//
// When the theme endpoint lands (ThemeService, served by dodo), this file
// becomes the fallback and the endpoint becomes the source. Themes are the ONE
// place last-known-good caching is legal in this estate: a stale color misleads
// nobody, a stale flag does. Aesthetics degrade gracefully; truth never does.
type Theme struct {
	// Name is how the theme is asked for: PUFFIN_THEME, and the label the
	// header shows when you cycle with t.
	Name string
	// Light says the ground is pale. It is not decoration: the bird's white
	// face vanishes on a light ground, and dodo's own semantic colors wash
	// out there, which is why stardew overrides them.
	Light  bool
	Bg     lipgloss.Color
	Panel  lipgloss.Color
	Raised lipgloss.Color
	Line   lipgloss.Color
	Ink    lipgloss.Color
	Dim    lipgloss.Color
	Accent lipgloss.Color
	// AccentToken is dodo's second accent -- the cyan against corvid's
	// amber, vaporwave's cyan under the pink sun. It marks a thing that is
	// notable without being a warning.
	AccentToken lipgloss.Color
	// Ok, Warn and Caution are semantic and sit outside the theme blocks in
	// dodo's own css, inherited by every theme. A warning that changes
	// colour with the theme is not a warning: "this is a problem" has to
	// read the same in every skin.
	//
	// A dark theme leaves these alone and semantics() fills them in. Only a
	// light theme overrides them, and only for contrast.
	Ok      lipgloss.Color
	Warn    lipgloss.Color
	Caution lipgloss.Color
	// Plumage and Snow are the bird's own truth: puffins are black with
	// white faces, and the first render painted the whole bird in ink so
	// the dense glyphs read bright -- an inverted puffin. Charcoal keeps
	// the black visible on a dark terminal; snow is the face and belly.
	Plumage lipgloss.Color
	Snow    lipgloss.Color
	// Cursor is the ground the selected row sits on. It is computed rather
	// than borrowed from a surface token, because the surface tokens are
	// not built for this: corvid's --raised-2 is #151b26 against a #0b0e14
	// page, a difference you can measure and not one you can see.
	//
	// A cursor you cannot find is a hazard on a screen where a keystroke
	// interrupts somebody's work, so this is a guaranteed step off the page
	// -- enough to find at a glance, not enough to glare, and toward the
	// theme's own ink so it stays in family.
	Cursor lipgloss.Color
	// Match is the ground a line sits on when it contains what you searched
	// for. Deliberately below the cursor's lift: the cursor still has to
	// win, or a search matching forty lines leaves you unable to find which
	// one you are standing on.
	Match lipgloss.Color
	// Screen and Phosphor are the little CRT: the window inside the window.
	// It is a surface, not a panel -- it has to read as a separate screen
	// sitting in the page, which a one-step-up background does not do.
	//
	// On the puffin palette the panel token is #181818 against a #111111
	// ground and the box may as well not be there.
	//
	// So the ground is pulled toward a muted navy (think ansi 017) rather
	// than lifted a shade, and it is blended with the theme's own ground so
	// vaporwave's screen is violet-blue and bladerunner's is cold against
	// its warm page. In family, still legible.
	Screen   lipgloss.Color
	Phosphor lipgloss.Color
	// The gopher's two blues. Constant across themes, like the beak: a
	// puffin without an orange beak is a guillemot, and a gopher that is
	// not Go blue is some other rodent.
	//
	// GopherFur is the body and the strokes; GopherLight is the eyes, nose
	// and teeth -- the features that have to come forward, or the whole
	// thing reads as a tangle of punctuation, which is what it was when it
	// rendered flat.
	GopherFur   lipgloss.Color
	GopherLight lipgloss.Color
	// Tokens is dodo's palette as it arrived, whole. Puffin paints with a
	// dozen of these; a sprite inking a stencil needs more of them, and
	// both should be wearing the SAME theme rather than each fetching the
	// stylesheet separately and hoping they agree.
	Tokens map[string]string
	// Beak is puffin's own: the one color dodo does not carry. A puffin
	// without an orange beak is a guillemot, and nobody built a TUI called
	// guillemot.
	Beak lipgloss.Color
}

// semantics fills in the colors that do not belong to a theme. Applied to
// every theme that does not set them itself.
func semantics(t Theme) Theme {
	if t.Ok == "" {
		t.Ok = lipgloss.Color("#3fb950")
	}
	if t.Warn == "" {
		t.Warn = lipgloss.Color("#f85149")
	}
	if t.Caution == "" {
		t.Caution = lipgloss.Color("#d29922")
	}
	// The bird is not a theme token either. A puffin is black with a white
	// face in every light, and the beak is orange in all of them -- a
	// puffin without an orange beak is a guillemot, whatever the terminal
	// is wearing.
	//
	// The one adjustment a light ground forces is the face: snow on pale
	// sand is an invisible bird, so it darkens to keep its shape.
	if t.Beak == "" {
		t.Beak = lipgloss.Color("#ff8c42")
	}
	if t.Plumage == "" {
		t.Plumage = lipgloss.Color("#3a3f45")
	}
	if t.Snow == "" {
		if t.Light {
			t.Snow = lipgloss.Color("#6b5b47")
		} else {
			t.Snow = lipgloss.Color("#f2f0eb")
		}
	}
	if t.Cursor == "" {
		t.Cursor = mix(string(t.Bg), string(t.Ink), cursorLift)
	}
	if t.GopherFur == "" {
		t.GopherFur = lipgloss.Color("#00add8") // go's own blue
	}
	if t.GopherLight == "" {
		t.GopherLight = lipgloss.Color("#8ed6e6")
	}
	if t.Match == "" {
		t.Match = mix(string(t.Bg), string(t.Ink), matchLift)
	}
	if t.Screen == "" {
		if t.Light {
			// a light page gets a light screen: a navy box on sand
			// is a hole in the page, not a window
			t.Screen = mix(string(t.Panel), "#dfe7f0", 0.55)
		} else {
			t.Screen = mix(string(t.Bg), crtNavy, 0.62)
		}
	}
	if t.Phosphor == "" {
		if t.Light {
			t.Phosphor = t.Ink
		} else {
			// lifted off the theme's ink so the screen reads
			// brighter than the page around it, which is what a lit
			// screen does
			t.Phosphor = mix(string(t.Ink), "#ffffff", 0.35)
		}
	}
	return t
}

// cursorLift is how far the selected row's ground moves off the page toward the
// theme's ink.
//
// Tuned by eye against vaporwave, which is the hardest case -- a violet page
// hides a violet highlight better than a grey page hides a grey one -- and then
// checked against every theme by test: findable above 0.03 luminance, glaring
// above 0.30. At this value the five themes land between 0.15 and 0.19.
//
// One number for all of them on purpose. Per-theme tuning is a knob that
// drifts: the next theme dodo ships would arrive without one and be wrong
// in a way nobody notices until they are squinting at it.
const cursorLift = 0.22

// matchLift is the same idea for a line that contains what you searched for,
// deliberately lower so the cursor still reads as the cursor.
const matchLift = 0.13

// crtNavy is the muted blue the little screen is pulled toward: ansi 017.
const crtNavy = "#00005f"

// mix blends two hex colours, w being how much of b. Colours that cannot be
// parsed return a, so a bad token degrades to the theme rather than to black.
func mix(a, b string, w float64) lipgloss.Color {
	ar, ag, ab, ok1 := rgb(a)
	br, bg, bb, ok2 := rgb(b)
	if !ok1 || !ok2 {
		return lipgloss.Color(a)
	}
	f := func(x, y int) int { return int(float64(x)*(1-w) + float64(y)*w) }
	return lipgloss.Color(fmt.Sprintf("#%02x%02x%02x",
		f(ar, br), f(ag, bg), f(ab, bb)))
}

// rgb parses #rgb and #rrggbb.
func rgb(h string) (int, int, int, bool) {
	h = strings.TrimSpace(h)
	if !strings.HasPrefix(h, "#") {
		return 0, 0, 0, false
	}
	h = h[1:]
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	if len(h) != 6 {
		return 0, 0, 0, false
	}
	n, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return int((n >> 16) & 0xff), int((n >> 8) & 0xff), int(n & 0xff), true
}

// The themes, transcribed from dodo's own lib/themes/themes.css so the
// terminal and the web read as one estate. corvid is dodo's default and so
// it is puffin's; stardew is the light one and a real test, because
// everything else assumes a dark ground.
//
// `puffin` is the palette this file carried before: an older transcription
// of dodo's shell css that dodo itself has since moved off. It is kept
// because it is what puffin has looked like, and named `puffin` rather than
// `dodo` because calling it dodo's palette stopped being true.

// Corvid is the house default: bench-ui midnight in theme form.
func Corvid() Theme {
	return semantics(Theme{
		Name:        "corvid",
		Bg:          lipgloss.Color("#0b0e14"),
		Panel:       lipgloss.Color("#0e121a"),
		Raised:      lipgloss.Color("#1b2433"),
		Line:        lipgloss.Color("#2b3648"),
		Ink:         lipgloss.Color("#e6edf7"),
		Dim:         lipgloss.Color("#7d8899"),
		Accent:      lipgloss.Color("#e8a33d"),
		AccentToken: lipgloss.Color("#5ad7cd"),
	})
}

// Vaporwave: sun low on the horizon, scanlines over it, cyan haze below.
func Vaporwave() Theme {
	return semantics(Theme{
		Name:        "vaporwave",
		Bg:          lipgloss.Color("#160d2b"),
		Panel:       lipgloss.Color("#1c0f36"),
		Raised:      lipgloss.Color("#2a1a4d"),
		Line:        lipgloss.Color("#432c6b"),
		Ink:         lipgloss.Color("#f2e6ff"),
		Dim:         lipgloss.Color("#9d84c4"),
		Accent:      lipgloss.Color("#ff6ec7"),
		AccentToken: lipgloss.Color("#00e5ff"),
	})
}

// Bladerunner: smog lit from below by something orange, cold light above.
func Bladerunner() Theme {
	return semantics(Theme{
		Name:        "bladerunner",
		Bg:          lipgloss.Color("#0a0a0d"),
		Panel:       lipgloss.Color("#100e0c"),
		Raised:      lipgloss.Color("#1d1813"),
		Line:        lipgloss.Color("#31291f"),
		Ink:         lipgloss.Color("#f0e2c8"),
		Dim:         lipgloss.Color("#8a7c66"),
		Accent:      lipgloss.Color("#ff9d3d"),
		AccentToken: lipgloss.Color("#4dd0e1"),
	})
}

// Stardew: pale sky over a green field. The light one, and the only theme
// that overrides the semantics -- dark-ground green and red wash out on
// sand, so they darken rather than change meaning.
func Stardew() Theme {
	return semantics(Theme{
		Name:        "stardew",
		Light:       true,
		Bg:          lipgloss.Color("#f3e3c3"),
		Panel:       lipgloss.Color("#fcf5e5"),
		Raised:      lipgloss.Color("#e8d7b4"),
		Line:        lipgloss.Color("#cbb08a"),
		Ink:         lipgloss.Color("#4a3524"),
		Dim:         lipgloss.Color("#8a7355"),
		Accent:      lipgloss.Color("#3f8f4a"),
		AccentToken: lipgloss.Color("#56a89c"),
		Ok:          lipgloss.Color("#1a7f37"),
		Warn:        lipgloss.Color("#cf222e"),
		Caution:     lipgloss.Color("#9a6700"),
	})
}

// DodoDark is the palette puffin carried before the themes landed. Kept
// under its own name: dodo has moved on, and it is no longer dodo's.
func DodoDark() Theme {
	return semantics(Theme{
		Name:        "puffin",
		Bg:          lipgloss.Color("#111111"),
		Panel:       lipgloss.Color("#181818"),
		Raised:      lipgloss.Color("#222222"),
		Line:        lipgloss.Color("#333333"),
		Ink:         lipgloss.Color("#cccccc"),
		Dim:         lipgloss.Color("#888888"),
		Accent:      lipgloss.Color("#e8624d"),
		AccentToken: lipgloss.Color("#5ad7cd"),
	})
}

// Themes is the cycle order t walks, and the names PUFFIN_THEME accepts.
func Themes() []Theme {
	return []Theme{
		Corvid(),
		Vaporwave(),
		Bladerunner(),
		Stardew(),
		DodoDark(),
	}
}

// ThemeIndex resolves a name to its place in the cycle. An unknown name is
// not a crash and not a silent default: it answers false and the caller says
// so, because a theme that quietly ignores what you asked for is a bug you
// spend an evening on.
func ThemeIndex(name string) (int, bool) {
	for i, t := range Themes() {
		if strings.EqualFold(t.Name, name) {
			return i, true
		}
	}
	return 0, false
}

// Styles are the theme compiled into lipgloss, built once per theme change.
type Styles struct {
	Title    lipgloss.Style
	Subtitle lipgloss.Style
	Header   lipgloss.Style
	Row      lipgloss.Style
	RowSel   lipgloss.Style
	Ok       lipgloss.Style
	Warn     lipgloss.Style
	Caution  lipgloss.Style
	Dim      lipgloss.Style
	Accent   lipgloss.Style
	Beak     lipgloss.Style
	Plumage  lipgloss.Style
	Snow     lipgloss.Style
	Gopher   lipgloss.Style
	GopherHi lipgloss.Style
	Panel    lipgloss.Style
	Help     lipgloss.Style
	HelpKey  lipgloss.Style
	// AccentAlt is the second accent: notable, but not a warning.
	AccentAlt lipgloss.Style
	// The little CRT: its ground, its text, and the quieter text on it.
	CRT      lipgloss.Style
	CRTDim   lipgloss.Style
	CRTFrame lipgloss.Style
	Screen   lipgloss.Color
	// Cursor is the selected row's ground.
	Cursor lipgloss.Color
	// Match is a searched-for line's ground; MatchTerm is the term in it.
	MatchBg   lipgloss.Color
	Match     lipgloss.Style
	MatchTerm lipgloss.Style
	// Theme is the compiled theme's name, for the header.
	Theme string
	// Tokens is that theme's raw palette, passed through for anything
	// inking more than a terminal does.
	Tokens map[string]string
	// Raised is the theme's raised surface. The cursor uses Cursor, not
	// this: see the note on Theme.Cursor for why a surface token is the
	// wrong thing to select a row with.
	Raised lipgloss.Color
}

// On paints a cell on the cursor's ground when it is the cursor's row. The
// background has to go on each CELL rather than the assembled line: a
// composed row already carries its own color resets, and a background
// wrapped around the outside dies at the first one.
func (s Styles) On(st lipgloss.Style, selected bool) lipgloss.Style {
	if !selected {
		return st
	}
	return st.Background(s.Cursor)
}

// Compile turns tokens into styles.
func Compile(t Theme) Styles {
	return Styles{
		Theme:   t.Name,
		Tokens:  t.Tokens,
		Raised:  t.Raised,
		Cursor:  t.Cursor,
		MatchBg: t.Match,
		Match:   lipgloss.NewStyle().Background(t.Match),
		// the TERM itself, inside the line: the accent on the match
		// ground, bold. A line highlight says "it is in here somewhere"
		// and leaves you reading; the term highlight is the answer.
		MatchTerm: lipgloss.NewStyle().
			Foreground(t.Accent).
			Background(t.Match).
			Bold(true),
		Screen: t.Screen,
		CRT: lipgloss.NewStyle().
			Foreground(t.Phosphor).
			Background(t.Screen),
		CRTDim: lipgloss.NewStyle().
			Foreground(t.Dim).
			Background(t.Screen),
		CRTFrame: lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			BorderForeground(t.Accent).
			Background(t.Screen).Padding(0, 1),
		AccentAlt: lipgloss.NewStyle().Foreground(t.AccentToken),
		Title:     lipgloss.NewStyle().Foreground(t.Ink).Bold(true),
		Subtitle:  lipgloss.NewStyle().Foreground(t.Dim),
		Header: lipgloss.NewStyle().
			Foreground(t.Dim).
			Underline(true),
		Row: lipgloss.NewStyle().Foreground(t.Ink),
		RowSel: lipgloss.NewStyle().
			Foreground(t.Bg).
			Background(t.Accent).
			Bold(true),
		Ok:      lipgloss.NewStyle().Foreground(t.Ok),
		Warn:    lipgloss.NewStyle().Foreground(t.Warn).Bold(true),
		Caution: lipgloss.NewStyle().Foreground(t.Caution),
		Dim:     lipgloss.NewStyle().Foreground(t.Dim),
		Accent:  lipgloss.NewStyle().Foreground(t.Accent),
		Beak:    lipgloss.NewStyle().Foreground(t.Beak).Bold(true),
		Gopher:  lipgloss.NewStyle().Foreground(t.GopherFur),
		GopherHi: lipgloss.NewStyle().
			Foreground(t.GopherLight).
			Bold(true),
		Plumage: lipgloss.NewStyle().Foreground(t.Plumage),
		Snow:    lipgloss.NewStyle().Foreground(t.Snow).Bold(true),
		Panel: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(t.Line).
			Padding(0, 1),
		Help: lipgloss.NewStyle().Foreground(t.Dim).Italic(true),
		// the KEY inside a help line, brighter than the words
		// describing it.
		//
		// It is the only part you act on, and in a line of eight of
		// them it is what the eye is scanning for -- rendering it at
		// the same weight as its own description makes you read the
		// sentence to find the letter.
		HelpKey: lipgloss.NewStyle().Foreground(t.Ink).Italic(true),
	}
}

// liveTheme is the theme puffin is currently wearing, for anything that needs
// the palette rather than the compiled styles -- the bird inks eleven roles
// from it.
//
// Held here rather than passed down because the model is a value type that gets
// copied on every update, and an overlay installed once cannot hold a pointer
// to it.
var liveTheme = DodoDark()

// setLiveTheme is how a cycled theme reaches the styles without threading
// it through every view that draws.
func setLiveTheme(t Theme) { liveTheme = t }

// currentTheme is what to ink with.
func currentTheme() Theme { return liveTheme }

// helpLine renders a help line with its keys picked out.
//
// A help line here is a list of "key: what it does", separated by the middle
// dot. This brightens the key and leaves the description dim, which is what
// makes a row of eight of them scannable rather than readable -- you are
// looking for a letter, not a sentence.
//
// Prose is left alone. A line like "forwards are host processes: nothing in the
// cluster knows they exist" has a colon in it and is not a key binding, so
// anything whose key half is long or full of spaces is rendered as it was.
//
// Getting that wrong would brighten half a sentence and look like a rendering
// bug.
func helpLine(s Styles, line string) string {
	const sep = " · "
	parts := strings.Split(line, sep)
	for i, part := range parts {
		key, rest, found := strings.Cut(part, ":")
		trimmed := strings.TrimSpace(key)
		if !found || len(trimmed) > 12 ||
			strings.Count(trimmed, " ") > 1 {
			parts[i] = s.Help.Render(part)
			continue
		}
		// keep the padding that justification put there: the roster
		// justifies three rows to one margin and the spaces are the
		// alignment
		lead := key[:len(key)-len(strings.TrimLeft(key, " "))]
		parts[i] = s.Help.Render(lead) + s.HelpKey.Render(trimmed) +
			s.Help.Render(":"+rest)
	}
	return strings.Join(parts, s.Help.Render(sep))
}

// helpBlock runs helpLine over every line of a multi-line help string.
func helpBlock(s Styles, text string) string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = helpLine(s, l)
	}
	return strings.Join(lines, "\n")
}
