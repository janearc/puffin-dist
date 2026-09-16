package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// `puffin pet` -- the pointer telemetry, on screen, doing its job.
//
// A demo rather than a toy. Mouse behaviour is the kind of thing that reads as
// correct in a test and wrong in a terminal: whether the eyes track, how stale
// a position has to be before they stop, what happens the moment you hand the
// mouse back.
//
// All of that is a sentence in a commit message and an obvious fact on a
// screen.
//
// It runs the SAME code the roster runs -- seeMouse on the way in, the real
// bird.Draw on the way out -- so what you see here is what the corner does.
// A demo that renders its own version of the thing is a demo that can pass
// while the product is broken.

// petDemo is the whole model: a size, a bird, and a clock.
type petDemo struct {
	w, h  int
	b     *bird
	tick  int
	quit  bool
	holds bool // whether we asked the terminal for the mouse
}

// petTick drives the idle animation, which is otherwise driven by whatever
// else is redrawing the screen.
type petTick struct{}

// petTicker drives the demo's own clock, so the companion animates
// without anything else on the screen having to change.
func petTicker() tea.Cmd {
	return tea.Tick(
		120*time.Millisecond,
		func(time.Time) tea.Msg { return petTick{} },
	)
}

// Init starts the clock as the program opens, or the first frame waits
// for a keystroke that may never come.
func (m petDemo) Init() tea.Cmd { return petTicker() }

// Update records the pointer exactly as the real loop does, and otherwise
// only counts frames.
func (m petDemo) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case petTick:
		m.tick++
		return m, petTicker()
	case tea.MouseMsg:
		// the one line the whole feature rests on
		seeMouse(msg, m.w, m.h)
		if msg.Action == tea.MouseActionPress {
			// a click is something happening, and something
			// happening is what the corner reacts to. This is the
			// same call the event stream makes; nothing here is
			// special-cased for the demo.
			m.b.Handle(Event{Kind: WatchFired})
		}
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			m.quit = true
			return m, tea.Quit
		case "M":
			m.holds = !m.holds
			mouseReporting(m.holds)
			if m.holds {
				return m, tea.EnableMouseCellMotion
			}
			return m, tea.DisableMouse
		case "C":
			chooseMascot(nextMascot(standingMascot()))
		case "e":
			pokeCompanion()
		}
	}
	return m, nil
}

// View draws a blank page, hands it to the real corner renderer, and prints
// the pointer's own account of itself above it.
func (m petDemo) View() string {
	if m.w == 0 || m.quit {
		return ""
	}
	s := Compile(currentTheme())
	p := MousePointer()

	var b strings.Builder
	b.WriteString(
		s.Beak.Render(
			"puffin",
		) + s.Subtitle.Render(
			"  ·  the pet, and what it can see",
		) + "\n\n",
	)

	// the struct, field by field, because the point of the demo is that
	// these are the facts anything in puffin can now ask for
	row := func(k, v string) {
		b.WriteString(
			"  " + s.Dim.Render(
				pad(k, 14),
			) + s.Row.Render(
				v,
			) + "\n",
		)
	}
	row("position", fmt.Sprintf("%d, %d", p.X, p.Y))
	row("last action", fmt.Sprintf("%v %v", p.Button, p.Action))
	row("events", fmt.Sprintf("%d", p.Moves))
	row("terminal", fmt.Sprintf("%dx%d", p.Width, p.Height))

	switch {
	case !p.Reporting:
		row(
			"reporting",
			"off -- nothing can arrive · M takes the mouse",
		)
	case p.Seen.IsZero():
		row(
			"reporting",
			"on -- nothing has arrived yet · move the mouse",
		)
	default:
		row(
			"reporting",
			fmt.Sprintf(
				"on · last seen %s ago",
				humanSpanDemo(time.Since(p.Seen)),
			),
		)
	}
	if p.Fresh() {
		row("fresh", "yes -- the pet is watching your cursor")
	} else {
		row("fresh", "no -- the pet looks at nothing")
	}

	// where the pet is standing, and therefore which way it should be
	// looking. Shown because "is it tracking" is much easier to answer when
	// you can see what it is tracking FROM.
	rows := rowsFor(m.h, m.h)
	cols := m.b.sprite.ColsFor(rows)
	if x, y, ok := cornerAt(m.h, cols, rows, m.w); ok {
		dx, dy := p.Toward(x+cols/2, y+rows/2)
		row(
			"pet at",
			fmt.Sprintf(
				"%d, %d (%d rows)",
				x+cols/2,
				y+rows/2,
				rows,
			),
		)
		row(
			"looking",
			fmt.Sprintf(
				"%s %s",
				lookWord(dx, "left", "right"),
				lookWord(dy, "up", "down"),
			),
		)
	}
	if rows < birdRowsEyes {
		b.WriteString("\n  " + s.Caution.Render(
			"this window is too short for eyes: at 8 rows a "+
				"moving pupil does not survive, "+
				"so the gaze is stripped on purpose. make "+
				"the terminal taller.",
		) + "\n")
	}

	b.WriteString("\n  " + s.Help.Render(
		"M: take/give back the mouse · C: next pet · e: poke it · "+
			"click: startle it · q: quit",
	) + "\n")

	// pad out to the full height so the corner renderer has a page to
	// composite onto, exactly as the roster gives it one
	page := b.String()
	for strings.Count(page, "\n") < m.h-1 {
		page += "\n"
	}
	return drawOverlay(page, m.w, m.h)
}

// lookWord turns a -1/0/1 into a word, because "0" is not an answer to
// "which way is it looking".
func lookWord(d int, neg, pos string) string {
	switch {
	case d < 0:
		return neg
	case d > 0:
		return pos
	}
	return "level"
}

// humanSpanDemo is a short age, to tenths, so the staleness window is
// visible as it runs out rather than only when it has.
func humanSpanDemo(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

// cliPet runs the demo.
func cliPet(args []string) int {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			fmt.Println("usage: puffin pet")
			fmt.Println(
				"  a page with the pet on it and the " +
					"pointer's own account of itself.",
			)
			fmt.Println(
				"  M takes the mouse, and then it follows " +
					"your cursor.",
			)
			return 0
		}
	}
	loadUIState()
	b := newBird()
	SetOverlay(b)
	// the demo starts NOT holding the mouse, whatever the preference says:
	// the first thing it has to demonstrate is the difference, and starting
	// on the interesting side hides half of it
	mouseReporting(false)
	m := petDemo{b: b}
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "pet:", err)
		return 1
	}
	return 0
}
