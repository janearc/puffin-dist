package main

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// The pane seam.
//
// The first nine screens live in one model struct and one Update, and each new
// one made the next one harder: a region of fields, a case in the type switch,
// a guarded arm in the global key switch. Panes are the way out -- and, more to
// the point, the way IN to a backend.
//
// A pane owns its own state, fetches through ONE function that returns a plain
// struct, and renders from that struct alone.
//
// That fetch function is the seam that matters. Today it shells to kubectl and
// reads this laptop; when the backend container lands it becomes an RPC and the
// pane does not notice, because a pane has never been allowed to hold a kubectl
// type, an http client, or a lipgloss style in its data.
//
// The wire is the boundary, so the data crossing it must already be shaped like
// a message: flat, scalar, no pointers into somebody else's library.
type pane interface {
	// Key is the keystroke that opens the pane from the roster.
	Key() string
	// Title names it in the header.
	Title() string
	// Load fetches. One call, one struct back, addressed by name.
	Load(domain string) tea.Cmd
	// Update folds a message in and answers with what to do next.
	Update(msg tea.Msg) (pane, tea.Cmd)
	// View renders into the terminal it is given. Styles arrive from the
	// theme rather than being reached for, so a pane cannot bake a color.
	View(s Styles, width, height int) string
	// Help is the pane's own footer line: its keys, nobody else's.
	Help() string
}

// clicker is the mouse half of the seam, also optional. A pane that can be
// clicked implements it and gets coordinates in ITS OWN space -- row 0 is
// the pane's first line, not the terminal's -- because a pane should not
// have to know what the frame around it prepends.
type clicker interface {
	Click(x, y int, wheel int) tea.Cmd
}

// paneBodyTop is how many lines paneView writes before the pane's own View
// begins: one from the frame's top padding, then the header line and the blank
// under it.
//
// Kept beside the function that writes them so the two cannot drift apart -- a
// click offset that is wrong by one is a click that sorts the wrong column.
const paneBodyTop = 3

// paneBodyLeft is the frame's left padding.
const paneBodyLeft = 2

// ticker is an optional half of the seam: a pane that wants to refresh
// itself while it is open implements it, and one that does not is unchanged.
// A pane you leave open to watch -- agents, and the cluster screen when it
// grows into one -- should not need a keystroke to stay true.
//
// The interval is the pane's own, because the right cadence is a property of
// what it is watching: transcripts change every few seconds, a colima
// profile does not change all afternoon, and a screen that re-reads the
// world faster than the world moves is just heat.
type ticker interface {
	Tick() time.Duration
}

// capturer is a pane that owns the keyboard while something is being typed
// into it, so a bare q closes a word rather than the pane.
type capturer interface {
	Capturing() bool
}

// escaper is a pane that wants escape for itself -- to leave a composer, or
// to drop a filter -- rather than letting it walk the screen stack back.
type escaper interface {
	HandlesEsc() bool
}

// paneTick asks the open pane to refresh. It carries the pane's title so a
// tick scheduled by a pane you have since closed is discarded rather than
// firing a fetch for a screen nobody is looking at.
type paneTick struct{ pane string }

// tickPane schedules one refresh. Bubbletea's Tick fires once, so a pane
// keeps ticking only for as long as the loop keeps rescheduling it --
// closing the pane stops the clock by simply not scheduling another.
func tickPane(title string, d time.Duration) tea.Cmd {
	return tea.Tick(
		d,
		func(time.Time) tea.Msg { return paneTick{pane: title} },
	)
}

// paneFactories is the registry: a key to a way of making one, not to a
// shared instance. The difference matters the moment two panes are open at
// once -- two logs side by side are two panes with two cursors, two buffers
// and two searches, and a registry of singletons cannot give you that.
func paneFactories() map[string]func() pane {
	return map[string]func() pane{
		"v": func() pane { return &hostPane{} },
		"d": func() pane { return &deployPane{} },
		"a": func() pane { return &agentPane{} },
		"p": func() pane { return &metricsPane{} },
		"l": func() pane { return &logsPane{} },
		"b": func() pane { return &busPane{} },
		"P": func() pane { return &portsPane{} },
	}
}

// panes is one instance of each, for code that only needs to ask what
// exists rather than to open one.
func panes() map[string]pane {
	reg := map[string]pane{}
	for k, mk := range paneFactories() {
		reg[k] = mk()
	}
	return reg
}

// The split.
//
// Two panes of logs. A directory on the left and the file itself on the right.
// Midnight Commander's real insight was never "two lists" -- it was that the
// space between them is where the work happens. You copy from the left to the
// right; you read the tree on one side and the thing itself on the other.
//
// So this is a layout, not a feature: any pane can sit beside any other, and
// the panes do not know it happened. They are handed a width and they render
// into it, which they already did.

// side says which half has the keyboard.
type side int

const (
	left side = iota
	right
)

// paneView is the frame every pane renders inside: puffin's header, the
// pane's body, the pane's own help. The frame is puffin's and the middle is
// the pane's -- which is also where the CRT window is going to live.
func paneView(m model) string {
	if m.openPane == nil {
		return ""
	}
	s := m.styles
	// the width the panes get, which is not the terminal's: the bird needs
	// a strip on the right or it has nowhere to stand. See birdGutter.
	w := m.width - birdGutter(m.width, m.height)
	if m.rightPane == nil {
		head := s.Beak.Render(
			"puffin",
		) + s.Subtitle.Render(
			"  ·  "+m.openPane.Title(),
		) +
			s.AccentAlt.Render(
				"  ·  "+s.Theme,
			) + s.Dim.Render(
			" ("+m.themeSrc+")",
		) + "\n\n"
		body := m.openPane.View(s, w, m.height)
		// the split has to be advertised before you split, not after.
		// The first cut only mentioned it in the split footer, which is
		// the one place you no longer need to be told.
		return drawOverlay(paneFrame(s, clampWidth(head+body+"\n"+
			helpBlock(s, m.openPane.Help())+"\n"+
			s.Dim.Render(
				"|: put another pane beside this one",
			), m.width-4)),
			m.width, m.height)
	}

	// split: each side gets half the width minus the gutter, and each is
	// told its own width so a pane that drops columns when narrow does the
	// right thing without knowing why
	half := (w - 7) / 2
	if half < 30 {
		half = 30
	}
	head := s.Beak.Render("puffin") +
		s.Subtitle.Render(
			"  ·  "+m.openPane.Title()+" | "+m.rightPane.Title(),
		) +
		s.AccentAlt.Render("  ·  "+s.Theme) + "\n\n"

	lb := splitBody(s, m.openPane, half, m.height, m.focus == left)
	rb := splitBody(s, m.rightPane, half, m.height, m.focus == right)
	body := joinColumns(s, lb, rb, half)

	focused := m.openPane
	if m.focus == right {
		focused = m.rightPane
	}
	// clamped to the terminal, not to the panes: lipgloss pads every line
	// of a multi-line render out to the widest one, so a help footer longer
	// than the window widened the whole frame past the edge and the split
	// spilled. The panes were the suspects and they were innocent.
	return drawOverlay(paneFrame(s, clampWidth(head+body+"\n"+
		helpBlock(s, focused.Help())+"\n"+
		s.Dim.Render(
			"ctrl+w: other pane · |: unsplit · a pane key "+
				"replaces the focused side",
		),
		m.width-4)), m.width, m.height)
}

// splitBody renders one side, with a title that says which one has the
// keyboard. Without that mark a split is two lists and a guess.
func splitBody(s Styles, p pane, width, height int, focused bool) string {
	title := s.Dim.Render(padTo(p.Title(), width))
	if focused {
		title = s.RowSel.Render(padTo("\u25b8 "+p.Title(), width))
	}
	return clampWidth(title+"\n"+p.View(s, width, height), width)
}

// clampWidth truncates every line of a rendered block to the width the block
// was given.
//
// It is a backstop and it earned its place: the agents pane draws a row of
// model chips that grows with the number of models, and at half width that one
// line ran past the column.
//
// lipgloss pads a multi-line render to its widest row, so a single over-long
// line dragged the entire right column out to 151 columns inside a 96-column
// half, and the split spilled off the terminal.
//
// Panes should size their own content, and the one that did not is fixed
// separately. This is here so the NEXT one cannot tear the layout: a pane
// that overruns loses its own tail, rather than everything losing its
// alignment.
func clampWidth(block string, width int) string {
	if width <= 0 {
		return block
	}
	lines := strings.Split(block, "\n")
	for i, l := range lines {
		if ansi.StringWidth(l) > width {
			lines[i] = ansi.Truncate(l, width, "\u2026")
		}
	}
	return strings.Join(lines, "\n")
}

// joinColumns puts two rendered blocks side by side, padding the shorter one
// so the divider runs the full height. Lines are padded by their visible
// width, ignoring the escape sequences in them -- padding by len() counts
// colour codes as characters and tears the right column into a staircase.
func joinColumns(s Styles, a, b string, width int) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	n := len(al)
	if len(bl) > n {
		n = len(bl)
	}
	var out strings.Builder
	for i := 0; i < n; i++ {
		l, r := "", ""
		if i < len(al) {
			l = al[i]
		}
		if i < len(bl) {
			r = bl[i]
		}
		if pad := width - visibleWidth(l); pad > 0 {
			l += strings.Repeat(" ", pad)
		}
		out.WriteString(l + s.Dim.Render(" \u2502 ") + r)
		if i < n-1 {
			out.WriteString("\n")
		}
	}
	return out.String()
}

// visibleWidth counts the columns a rendered line occupies, skipping the
// ansi escapes inside it.
func visibleWidth(s string) int {
	// this used to be a hand-rolled escape-state machine, and it was the
	// second implementation of a thing the render path already depends on.
	//
	// The auklet's author lost a render chasing a phantom bug in exactly
	// such a helper: it is the splice that looks broken, never the width
	// function that lied to it.
	//
	// x/ansi is already in the module graph via lipgloss and it counts
	// grapheme width rather than runes, so a wide glyph in a log line stops
	// shifting everything right of it.
	return ansi.StringWidth(s)
}

// paneFrame is the padding every screen in puffin wears. It is a function
// rather than a literal because the CRT window lands here: when the inner
// frame arrives, every pane gets it at once and none of them are edited.
func paneFrame(s Styles, body string) string {
	return lipgloss.NewStyle().Padding(1, 2).Render(body)
}

// framed is the last thing every screen passes through, so an overlay
// reaches all of them from one place and no view has to know it exists.
//
// compile a build tag.
//
//lint:ignore U1000 the live suite calls it, and staticcheck does not
func framed(m model, body string) string {
	return drawOverlay(
		lipgloss.NewStyle().Padding(1, 2).Render(body),
		m.width,
		m.height,
	)
}

// Toasts: a warning that says its piece and goes.
//
// A warning that will not leave the screen is read once and then never
// again, and the screen it is on stops being read with it.
//
// This does not soften the rule about failing loud. The rule is about state --
// never present uncertain state as truth -- and a target that is down, a
// prometheus with no rules, a grafana puffin cannot authenticate to are all
// state, and they stay on the screen in the body where they belong.
//
// What goes here is the transient half: a fetch that hiccuped, a resolve that
// could not reach kubectl this second, things that were true for one refresh
// and are not a condition of the cluster.
//
// A permanent red banner for a passing hiccup is not vigilance. It is noise,
// and noise is what teaches people to stop reading a screen -- which is the
// actual way a warning gets missed.

// toast is a message with an expiry.
type toast struct {
	text  string
	until time.Time
}

// toastFor is how long a toast stays up.
const toastFor = 3 * time.Second

// toastExpired wakes the loop so the pane redraws without it.
type toastExpired struct{}

// show raises a toast and returns the command that clears it. An empty text
// clears immediately: a refresh that succeeds should take the last failure
// down with it rather than leaving it to time out.
func (t *toast) show(text string) tea.Cmd {
	if text == "" {
		t.text, t.until = "", time.Time{}
		return nil
	}
	t.text, t.until = text, time.Now().Add(toastFor)
	return tea.Tick(
		toastFor,
		func(time.Time) tea.Msg { return toastExpired{} },
	)
}

// live says whether the toast is still up.
func (t toast) live() bool { return t.text != "" && time.Now().Before(t.until) }

// view renders it at the bottom, in the caution colour rather than the warn
// colour: this is a thing that happened, not a thing that is wrong.
func (t toast) view(s Styles) string {
	if !t.live() {
		return ""
	}
	return "\n" + s.Caution.Render(t.text) + "\n"
}
