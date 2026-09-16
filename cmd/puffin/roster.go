package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// rosterView is the enclave at a glance.
//
// The screen is three regions, not one column of text: a header that names
// the enclave and what is wrong with it, a body that scrolls, and a help
// footer pinned to the bottom.
//
// It used to be one column, and the frame clamp drops lines from the bottom, so
// a cluster carrying twenty-seven furnishings pushed the whole menu off the
// screen. The keys were still bound; there was simply nothing left on the
// screen to say so.
//
// It only showed up at a large font size -- a bigger font is a shorter terminal
// -- which is to say the front page lost its menu exactly when somebody's eyes
// were tired, and looked fine whenever anyone went looking for the bug.
func rosterView(m model) string {
	s := m.styles
	body := m.rosterBody()
	size := m.rosterWindow()
	off := windowOffset(len(body), m.rosterRow(), size, m.rosterOffset)
	last := minInt(off+size, len(body))

	var b strings.Builder
	b.WriteString(rosterHead(s, m))
	for i := off; i < last; i++ {
		b.WriteString(body[i] + "\n")
	}
	b.WriteString(rosterFoot(s, m, scrollNote(len(body), last-off, off)))
	return drawOverlay(
		lipgloss.NewStyle().Padding(1, 2).Render(b.String()),
		m.width,
		m.height,
	)
}

// rosterHead is the part of the front page that does NOT scroll: who we
// are, which enclave, and everything currently wrong with it.
//
// The warnings stay pinned deliberately. They are the reason to look at
// this screen at all, and a warning that scrolls out of sight under a list
// of postgres containers is a warning nobody reads. Every line it returns
// is newline-terminated, so counting newlines counts lines.
func rosterHead(s Styles, m model) string {
	var b strings.Builder
	b.WriteString(
		s.Beak.Render(
			"puffin",
		) + s.Subtitle.Render(
			"  ·  the enclave  ·  "+m.domain,
		) +
			s.AccentAlt.Render(
				"  ·  "+s.Theme,
			) + s.Dim.Render(
			" ("+m.themeSrc+")",
		),
	)
	// freshness sits at the right edge of the title line, where the eye
	// already goes for it, rather than trailing the last help line
	if !m.enclave.When.IsZero() {
		lead := len("puffin  ·  the enclave  ·  ") + len(m.domain) +
			len("  ·  ") + len(s.Theme) + len(" ("+m.themeSrc+")")
		stamp := "as of " + m.enclave.When.Format(time.Kitchen)
		if gap := m.width - 4 - lead - len(stamp); gap > 1 {
			b.WriteString(
				strings.Repeat(" ", gap) + s.Dim.Render(stamp),
			)
		}
	}
	b.WriteString("\n\n")

	if m.themeNote != "" {
		b.WriteString(s.Caution.Render("! "+m.themeNote) + "\n")
	}
	if m.companionNote != "" {
		b.WriteString(
			s.Dim.Render(
				m.companionNote+" · C for the next one",
			) + "\n",
		)
	}
	if m.loading {
		b.WriteString(
			s.Dim.Render("asking the mesh who exists...") + "\n",
		)
	}
	for _, w := range m.enclave.Warnings {
		b.WriteString(s.Caution.Render("! "+w) + "\n")
	}
	// Running on remembered flags is a degraded state under flipr's
	// charter, and a degraded state that looks like a healthy one is the
	// failure this repository keeps finding.
	//
	// It is drawn in Warn rather than Caution and it names the duration,
	// because "flipr is down" and "flipr has been down for two hours" are
	// different problems.
	if held, since, why := FliprHeld(); held {
		b.WriteString(s.Warn.Render(fmt.Sprintf(
			"!! flipr unreachable for %s — puffin is running on "+
				"remembered flags: %s",
			humanSince(since),
			why,
		)) + "\n")
	}
	if len(m.enclave.Services) == 0 && !m.loading {
		b.WriteString(
			s.Dim.Render("nobody home. r to rediscover.") + "\n",
		)
	}
	return b.String()
}

// rosterBody is everything on the front page that scrolls: the services,
// then the furnishings.
//
// One list, because the window is one window. The two blocks are drawn
// differently and sorted separately, but they compete for the same rows,
// and the moment they were treated as two independent things the second
// one grew until it pushed the footer out of the terminal.
//
// Both column headers live in here rather than in the head: a header
// scrolls with the rows it names, or it ends up standing over somebody
// else's rows claiming to describe them.
func (m model) rosterBody() []string {
	s := m.styles
	var out []string

	if len(m.enclave.Services) > 0 {
		out = append(
			out,
			s.Header.Render(pad("service", 16)+pad("health", 10)+
				pad(
					"lease",
					16,
				)+pad("version", 10)+pad("api", 8)+"flags"),
		)
		for i, svc := range m.enclave.Services {
			out = append(out, rosterRow(s, svc, i == m.cursor))
		}
	}

	// furnishingsBlock speaks in a block because it is also drawn on its
	// own elsewhere; here it is cut back into lines so it can be windowed
	// with everything else.
	if f := furnishingsBlock(s, m); f != "" {
		out = append(
			out,
			strings.Split(strings.TrimSuffix(f, "\n"), "\n")...)
	}
	return out
}

// rosterRow draws one service, with the cursor's ground under it or not.
func rosterRow(s Styles, svc Service, on bool) string {
	// five states, five renderings: only DOWN earns the red. A
	// heartbeating daemon with no route is green -- alive by
	// citizenship, unreachable by design -- and absent is dim,
	// because "not deployed" is not an incident.
	var health string
	switch svc.State {
	case StateHealthy:
		health = s.On(s.Ok, on).Render(pad("healthy", 10))
	case StateDaemon:
		health = s.On(s.Ok, on).Render(pad("daemon", 10))
	case StateUnhealthy:
		health = s.On(s.Caution, on).Render(pad("unhealthy", 10))
	case StateAbsent:
		health = s.On(s.Dim, on).Render(pad("absent", 10))
	default:
		health = s.On(s.Warn, on).Render(pad("down", 10))
	}
	// the lease column: citizenship AND its remaining term, because
	// authorization that expires in nine seconds is a different fact
	// from authorization with a minute in hand
	citizen := s.On(s.Dim, on).Render(pad("-", 16))
	if svc.Lease != nil {
		label := svc.Lease.State
		if ttl := svc.Lease.TTL(time.Now()); ttl > 0 {
			label = fmt.Sprintf(
				"%s %ds",
				svc.Lease.State,
				int(ttl.Seconds()),
			)
		}
		switch svc.Citizen {
		case "authorized":
			citizen = s.On(s.Ok, on).Render(pad(label, 16))
		case "expiring":
			citizen = s.On(s.Caution, on).Render(pad(label, 16))
		case "expired":
			citizen = s.On(s.Warn, on).Render(pad(label, 16))
		}
	}
	// the api column carries the versions the service publishes --
	// an api version is a promise, and promises belong on the
	// first display
	api := s.On(s.Dim, on).Render(pad("-", 8))
	if svc.HasAPI {
		v := strings.Join(svc.APIVersions, " ")
		if v == "" {
			v = "yes"
		}
		api = s.On(s.Ok, on).Render(pad(v, 8))
	}
	flags := s.On(s.Row, on).Render("-")
	if svc.Flags >= 0 {
		flags = s.On(s.Row, on).Render(fmt.Sprintf("%d", svc.Flags))
		if svc.Expensive > 0 {
			flags += s.On(s.Caution, on).
				Render(fmt.Sprintf(" ($%d)", svc.Expensive))
		}
		if svc.StaleNS > 0 {
			// stale deploy namespaces are gc debt, not flags --
			// shown as debt so nobody reads six as sixty-eight
			// again
			flags += s.On(s.Dim, on).
				Render(fmt.Sprintf(" +%d stale "+
					"ns", svc.StaleNS))
		}
	}
	// the trailing pad carries the cursor's ground to the edge of
	// the columns rather than stopping at the last character
	marker := s.On(s.Row, on).Render("  ")
	if on {
		marker = s.On(s.RowSel, on).Render("▸ ")
	}
	return marker + s.On(s.Row, on).
		Render(pad(svc.Name, 16)) +
		health + citizen +
		s.On(s.Row, on).
			Render(pad(orDash(svc.Version), 10)) +
		api + flags +
		s.On(s.Row, on).
			Render(padTo("", 8))
}

// rosterFoot is the help, pinned to the bottom of the screen.
//
// The blank separator line above the menu carries the scroll note when there is
// one, so saying what is off the ends costs no height. A footer that grows a
// line whenever the list overflows takes that line FROM the list, which is the
// wrong direction: the note appears precisely when rows are scarcest.
//
// The note is words rather than a bar down the right-hand edge. This screen
// is the one that has to survive a 70-column terminal, and a track parked
// at column 90 is truncated away by the frame clamp exactly when there is
// something to scroll.
func rosterFoot(s Styles, m model, note string) string {
	var b strings.Builder
	if note != "" {
		b.WriteString(s.Dim.Render("  " + note + "  ·  j/k, pgup/pgdn"))
	}
	b.WriteString("\n")

	// two lines, grouped by what they open: the screens that read a member,
	// then the panes and the verbs.
	//
	// One line of eleven keys runs the width of the terminal and reads as a
	// wall rather than a menu.
	//
	// the two rows are justified to a common right margin: the gaps carry
	// the slack, the way a column of type does, so the block reads as a
	// block rather than two ragged sentences with a timestamp trailing off
	// the end of one of them
	screens := []string{
		"enter: api",
		"f: flags",
		"m: maps",
		"c: cluster",
		"r: reread",
		"q: quit",
	}
	panes := []string{
		"v: host",
		"d: deploys",
		"a: agents",
		"p: metrics",
		"l: logs",
		"b: bus",
	}
	// the split hint used to live here and it was a small lie: the roster
	// is not a pane, so the key it advertised did nothing when pressed on
	// the screen advertising it.
	//
	// It belongs on the row of things that ARE panes, next to the keys that
	// open them.
	//
	// the ports pane sits with the settings rather than with the panes: the
	// panes row is a row of screens you open to look at the estate, and
	// that row had run out of width, which is its own signal the companion
	// keys go on the screen you would go looking on.
	//
	// They shipped documented only in the pane help lines, which is the one
	// place you do not look for a setting -- so the feature existed,
	// worked, persisted, and could not be found.
	//
	// It names who is standing there rather than saying "C: companion",
	// because that answers the first question as well as the second: at
	// eight rows two of the four are the same silhouette, and "which one is
	// that" is asked before "how do I change it".
	extras := []string{
		"t: theme",
		"T: themes",
		mouseHelp(m.mouse),
		"P: ports",
		"|: split",
	}
	// the corner row joins the grid rather than sitting outside it.
	//
	// It used to be exempt because justification would have stretched two
	// items across the full margin as a row of gaps -- a real objection to
	// justifying it, and no objection at all to putting it in columns,
	// where a short row simply stops.
	corner := []string{"C: " + currentMascot(), "e: poke"}
	// 4 for the frame's horizontal padding, which the grid has to fit
	// inside
	for _, line := range helpColumns(
		m.width-4,
		screens,
		panes,
		extras,
		corner,
	) {
		b.WriteString(helpBlock(s, line) + "\n")
	}
	// puffin's own build, on the screen it spent the evening telling other
	// services to put theirs on
	b.WriteString(s.Dim.Render(thisBuild().Line()))
	return b.String()
}

// helpColumns lays the help out as a GRID rather than as justified rows.
//
// justify spread the slack into the gaps, so every row ended on a common
// right margin and nothing inside them lined up: six ragged columns that
// happened to finish together, in a screen with horizontal space going
// spare.
//
// So a column is as wide as its widest item across every row, and a row with
// fewer items stops rather than stretching. The separators then land in
// fixed columns too, which is most of what makes a grid read as one.
//
// A grid wider than the terminal is not a grid. Past that width the rows
// render at their natural spacing and the eye does the work instead -- the
// same bargain justify made, for the same reason: squeezed columns are
// worse than none.
func helpColumns(width int, rows ...[]string) []string {
	var w []int
	for _, r := range rows {
		for i, it := range r {
			for len(w) <= i {
				w = append(w, 0)
			}
			if n := ansi.StringWidth(it); n > w[i] {
				w[i] = n
			}
		}
	}
	total := 0
	for i, n := range w {
		total += n
		if i > 0 {
			total += 3 // " · "
		}
	}
	grid := width <= 0 || total <= width

	out := make([]string, 0, len(rows))
	var b strings.Builder
	for _, r := range rows {
		b.Reset()
		for i, it := range r {
			if i > 0 {
				b.WriteString(" · ")
			}
			b.WriteString(it)
			// the last item in a row is never padded: trailing
			// space buys nothing and shows up in a diff
			if grid && i < len(r)-1 {
				b.WriteString(
					strings.Repeat(
						" ",
						w[i]-ansi.StringWidth(it),
					),
				)
			}
		}
		out = append(out, b.String())
	}
	return out
}

// rosterWindow is how many body rows fit between the header and the help.
//
// It measures both rather than subtracting a constant.
//
// The header grows a line for every warning the enclave is carrying and another
// when flipr is unreachable -- which is to say it is tallest in exactly the
// situation where losing the menu hurts most, and a constant would have been
// wrong there and only there.
//
// A zero height is a test or a terminal that has not reported its size yet:
// show everything rather than nothing.
//
// The floor of three rows only bites below about thirteen, where the head
// and the help alone are taller than the terminal. There is nothing to
// arbitrate at that size: the body keeps its three rows and the frame clamp
// takes the overflow off the bottom, the way it did everywhere before this.
func (m model) rosterWindow() int {
	if m.height <= 0 {
		return 1 << 30
	}
	s := m.styles
	// 2 for the frame's vertical padding. Head lines are newline-terminated
	// so its newlines ARE its line count; the foot's last line is not, so
	// it counts one more than it carries.
	chrome := 2 + strings.Count(
		rosterHead(s, m),
		"\n",
	) + strings.Count(
		rosterFoot(s, m, ""),
		"\n",
	) + 1
	if n := m.height - chrome; n > 3 {
		return n
	}
	return 3
}

// rosterRow is where the cursor sits in the BODY's coordinates, which is
// one row below its service: the column header is body row zero.
func (m model) rosterRow() int {
	if len(m.enclave.Services) == 0 {
		return 0
	}
	return m.cursor + 1
}

// rosterScroll walks the cursor down the services and, past the last one,
// scrolls the page instead.
//
// The furnishings are not selectable -- there is nothing to open on one -- so a
// cursor that stops at the last service would leave everything below it
// unreachable.
//
// A list that will not move when there is visibly more below it is what
// "scrolling doesn't work" looks like from the outside, whatever the keys are
// technically doing.
func (m model) rosterScroll(d int) model {
	if next := m.cursor + d; next >= 0 && next < len(m.enclave.Services) {
		m.cursor = next
		m.rosterOffset = windowOffset(
			len(m.rosterBody()),
			m.rosterRow(),
			m.rosterWindow(),
			m.rosterOffset,
		)
		return m
	}
	return m.rosterPage(d)
}

// rosterPage moves the window itself, leaving the selection where it is.
func (m model) rosterPage(d int) model {
	size := m.rosterWindow()
	m.rosterOffset = clampInt(
		m.rosterOffset+d,
		0,
		maxInt(0, len(m.rosterBody())-size),
	)
	return m
}
