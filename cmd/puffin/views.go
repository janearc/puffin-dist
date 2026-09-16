package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The operator screens' views: flags, maps, cluster. One file so the visual
// grammar stays in one place -- headers, cursors, and colors behave
// identically across all three.

// flagsView is the intuitive flipr: every flag, the one under the cursor
// editable in place with the reason flipr demands.
func flagsView(m model) string {
	s := m.styles
	var b strings.Builder
	b.WriteString(
		s.Beak.Render(
			"puffin",
		) + s.Subtitle.Render(
			"  ·  flipr  ·  every flag in the store",
		) + "\n\n",
	)

	if m.flagsErr != "" {
		b.WriteString(s.Warn.Render(m.flagsErr) + "\n")
		b.WriteString(
			s.Dim.Render(
				"if flipr is down, the network is down -- by "+
					"design.",
			) + "\n",
		)
		return drawOverlay(
			lipgloss.NewStyle().Padding(1, 2).Render(b.String()),
			m.width,
			m.height,
		)
	}
	if len(m.flags) == 0 {
		b.WriteString(
			s.Dim.Render(
				"no flags anywhere. services publish at "+
					"deploy.",
			) + "\n",
		)
	}

	lastNS := ""
	for i, f := range m.flags {
		ns := f.Service + " @ " + f.Version
		if ns != lastNS {
			b.WriteString(
				"\n" + s.Accent.Render(
					f.Service,
				) + s.Dim.Render(
					" @ "+f.Version,
				) + "\n",
			)
			lastNS = ns
		}
		on := i == m.flagCursor
		marker := s.On(s.Row, on).Render("  ")
		if on {
			marker = s.On(s.RowSel, on).Render("▸ ")
		}
		exp := s.On(s.Row, on).Render("  ")
		if f.Expensive {
			exp = s.On(s.Caution, on).Render("$ ")
		}
		valStyle := s.Row
		if f.Kind == "bool" {
			valStyle = s.Ok
			if f.Value != "true" {
				valStyle = s.Dim
			}
		}
		// the value and the description are padded so the cursor's
		// ground runs the whole row: this is the screen that flips
		// flags, and the row you are about to open has to be
		// unmistakable
		b.WriteString(
			marker + exp + s.On(s.Row, on).Render(pad(f.Key, 26)) +
				s.On(s.Row, on).
					Render(" ") +
				s.On(valStyle, on).Render(pad(f.Value, 14)) +
				s.On(s.Help, on).
					Render(padTo(f.Desc, 48)) +
				"\n",
		)

		// the editor opens directly under its flag, where the eye
		// already is
		if i == m.flagCursor && m.editing {
			b.WriteString(
				"      " + s.Dim.Render(
					"new value ",
				) + m.editValue.View() + "\n",
			)
			b.WriteString(
				"      " + s.Dim.Render(
					"reason    ",
				) + m.editReason.View() + "\n",
			)
			if m.flagNote != "" {
				b.WriteString(
					"      " + s.Warn.Render(
						m.flagNote,
					) + "\n",
				)
			}
			b.WriteString(
				"      " + s.Help.Render(
					"tab: switch field · enter: flip · "+
						"esc: cancel",
				) + "\n",
			)
		}
	}
	if !m.editing && m.flagNote != "" {
		b.WriteString("\n" + s.Ok.Render(m.flagNote) + "\n")
	}
	b.WriteString(
		"\n" + s.Help.Render(
			"enter: edit the flag under the cursor · j/k: move · "+
				"q: back",
		),
	)
	return drawOverlay(
		lipgloss.NewStyle().Padding(1, 2).Render(b.String()),
		m.width,
		m.height,
	)
}

// mapsView is the browser: mounts, then directories, then the assessment.
func mapsView(m model) string {
	s := m.styles
	var b strings.Builder
	where := "/" + strings.Join(m.mapsStack, "")
	b.WriteString(
		s.Beak.Render(
			"puffin",
		) + s.Subtitle.Render(
			"  ·  maps  ·  kingfisher"+where,
		) + "\n\n",
	)

	if m.mapsErr != "" {
		b.WriteString(s.Warn.Render(m.mapsErr) + "\n\n")
	}
	if m.mapsListing == nil {
		b.WriteString(s.Dim.Render("reading the mount table...") + "\n")
		return drawOverlay(
			lipgloss.NewStyle().Padding(1, 2).Render(b.String()),
			m.width,
			m.height,
		)
	}

	if m.mapsListing.Manifest != "" {
		b.WriteString(
			s.Dim.Render(
				"manifest: "+m.mapsListing.Manifest,
			) + "\n\n",
		)
	}
	for i, e := range m.mapsListing.Entries {
		on := i == m.mapsCursor
		marker := s.On(s.Row, on).Render("  ")
		if on {
			marker = s.On(s.RowSel, on).Render("▸ ")
		}
		if e.Dir {
			b.WriteString(
				marker + s.On(s.Accent, on).
					Render(pad(e.Name, 34)) +
					s.On(s.Dim, on).
						Render(padTo("dir", 12)) +
					"\n",
			)
		} else {
			name := s.On(s.Row, on).Render(pad(e.Name, 34))
			b.WriteString(marker + name +
				s.On(
					s.Dim,
					on,
				).Render(padTo(humanBytes(e.Bytes), 12)) + "\n")
		}
	}

	// the assessment: what HEAD said about the selected file. The listing
	// promised a size; this is whether the other end actually delivers.
	if m.mapsAttrs != nil {
		a := m.mapsAttrs
		verdict := s.Ok.Render("AVAILABLE")
		if !a.Available() {
			verdict = s.Warn.Render(
				fmt.Sprintf(
					"NOT AVAILABLE (http %d)",
					a.Status,
				),
			)
		}
		lines := []string{
			verdict,
			"size          " + humanBytes(a.Bytes),
			"content-type  " + a.ContentType,
		}
		if a.LastModified != "" {
			lines = append(lines, "last-modified "+a.LastModified)
		}
		if a.CacheControl != "" {
			lines = append(lines, "cache-control "+a.CacheControl)
		}
		if a.Ranges {
			lines = append(lines, "range requests supported")
		}
		b.WriteString(
			"\n" + s.Panel.Render(strings.Join(lines, "\n")) + "\n",
		)
	}

	b.WriteString(
		"\n" + s.Help.Render(
			"enter: open dir / assess file · backspace: up · q: "+
				"back",
		),
	)
	return drawOverlay(
		lipgloss.NewStyle().Padding(1, 2).Render(b.String()),
		m.width,
		m.height,
	)
}

// kubeView is the cluster at a glance, namespaces grouped, colors by condition:
// running-and-ready green, pending caution, crashing red, finished dim -- a
// terminal pod is finished, not failing. kubeTrackCol is the column the
// scrollbar stands in, for every row on this screen.
//
// It is a constant because it was two numbers. Pod rows appended the track
// straight after their own padding, which lands at 77; header rows went
// through trackAt at 74. Three columns apart, so the bar rendered as two
// ragged columns of blocks down the right-hand side rather than one line.
//
// trackAt exists because this already happened once and its comment says so.
// The repair was applied to the header rows and not to the pod rows, and a fix
// that reaches half its call sites is how a bug comes back wearing the same
// clothes. One name, both callers, no arithmetic at the call site.
//
// 2 for the cursor marker, 44 for the name, 11 phase, 6 ready, 4 restarts,
// 10 age, 18 memory. Narrower would truncate a column off every pod on the
// screen to move a bar that was already in the right place.
const kubeTrackCol = 95

// kubeView draws the cluster screen, which is the one screen with its own
// fold state and so does not go through the pane seam.
func kubeView(m model) string {
	s := m.styles
	var b strings.Builder
	b.WriteString(
		s.Beak.Render(
			"puffin",
		) + s.Subtitle.Render(
			"  ·  cluster  ·  ",
		) +
			s.AccentAlt.Render(
				m.kube.Context,
			) + "\n",
	)
	// this screen shows ONE cluster, and never said so. Looking for a
	// service that lives in another one and finding an empty list reads as
	// an outage -- which is exactly how a healthy fleet got reported as
	// down. Naming what is not shown costs a line.
	if others := m.otherClusters(); others != "" {
		b.WriteString(s.Dim.Render("not shown: "+others+
			" · PUFFIN_KUBE_CONTEXT changes which cluster this "+
			"is") + "\n")
	}
	b.WriteString("\n")

	if m.kube.Err != "" {
		b.WriteString(s.Warn.Render(m.kube.Err) + "\n")
		return drawOverlay(
			lipgloss.NewStyle().Padding(1, 2).Render(b.String()),
			m.width,
			m.height,
		)
	}
	if len(m.kube.Pods) == 0 {
		b.WriteString(s.Dim.Render("asking kubectl...") + "\n")
		return drawOverlay(
			lipgloss.NewStyle().Padding(1, 2).Render(b.String()),
			m.width,
			m.height,
		)
	}

	// every pod is counted into exactly one bucket, so the headline adds up
	// to the number of pods. It did not before, and terminal pods were the
	// ones that vanished.
	var all [5]int
	for _, p := range m.kube.Pods {
		all[podHealth(p)]++
	}
	b.WriteString(
		s.Ok.Render(
			fmt.Sprintf("%d running", all[podUp]),
		) + s.Dim.Render(
			" \u00b7 ",
		) +
			s.Caution.Render(
				fmt.Sprintf("%d waiting", all[podWaiting]),
			) + s.Dim.Render(
			" \u00b7 ",
		) +
			s.Warn.Render(
				fmt.Sprintf("%d broken", all[podBroken]),
			) + s.Dim.Render(
			" \u00b7 ",
		) +
			s.Warn.Render(
				fmt.Sprintf("%d failed", all[podFailed]),
			) + s.Dim.Render(
			" \u00b7 ",
		) +
			s.Dim.Render(
				fmt.Sprintf("%d done", all[podDone]),
			) + "\n",
	)

	// folded first, then windowed, and both are needed: an unfolded
	// kube-system scrolls the thing you came to watch off the top on its
	// own, and a fully folded cluster still outgrows a short terminal.
	rows := m.kubeVisible()
	size := m.kubeWindow()
	off := windowOffset(len(rows), m.kubeCursor, size, m.kubeOffset)
	last := off + size
	if last > len(rows) {
		last = len(rows)
	}
	bar := scrollbar(len(rows), last-off, off)
	for i := off; i < last; i++ {
		r := rows[i]
		track := s.Dim.Render(" " + bar[i-off])
		on := i == m.kubeCursor
		marker := s.On(s.Row, on).Render("  ")
		if on {
			marker = s.On(s.RowSel, on).Render("\u25b8 ")
		}
		indent := strings.Repeat("  ", r.Depth)
		if r.Kind == rowPod {
			p := *r.Pod
			phaseStyle := s.Warn
			switch podHealth(p) {
			case -1:
				phaseStyle = s.Dim
			case 0:
				phaseStyle = s.Ok
			case 1:
				phaseStyle = s.Caution
			}
			phase := s.On(phaseStyle, on).Render(pad(p.Phase, 11))
			restarts := pad(fmt.Sprintf("%d", p.Restarts), 4)
			restartStyle := s.Dim
			if p.Restarts > 5 && !p.Terminal {
				restartStyle = s.Warn
			} else if p.Restarts > 0 && !p.Terminal {
				restartStyle = s.Caution
			}
			line := marker + s.On(s.Row, on).
				Render(indent+pad(p.Name, 44-len(indent))) +
				phase +
				s.On(s.Dim, on).
					Render(pad(p.Ready, 6)) +
				s.On(restartStyle, on).Render(restarts) +
				s.On(s.Dim, on).
					Render(padTo(p.Age, 10)) +
				memCell(
					s,
					p,
					on,
				)
			b.WriteString(trackAt(line, kubeTrackCol, track) + "\n")
			continue
		}
		// a header carries what the fold took away -- how many pods are
		// under it and what state they are in -- so folding never hides
		// a broken pod without the fold itself saying a pod is broken
		glyph := "v "
		if r.Folded {
			glyph = "> "
		}
		labelStyle := s.Header
		label := r.Label
		if r.Kind == rowGroup {
			// "kestrel-" is a prefix, not a name, and read as one
			// it looks like a typo. The star says it stands for
			// what is underneath.
			labelStyle, label = s.Accent, r.Label+"*"
		}
		count := fmt.Sprintf("  %d pods", r.Count)
		if r.Count == 1 {
			count = "  1 pod"
		}
		line := marker + s.On(labelStyle, on).
			Render(indent+glyph+label) +
			s.On(s.Dim, on).
				Render(count) +
			healthChip(
				s,
				r.Health,
			)
		// the row the cursor is on says what will open it. Not every
		// row -- that is a screen full of instructions -- just the one
		// you are about to press a key at.
		if on && r.Folded {
			line += s.On(s.Help, on).Render("   space opens")
		} else if on {
			line += s.On(s.Help, on).Render("   space folds")
		}
		b.WriteString(trackAt(line, kubeTrackCol, track) + "\n")
	}
	if note := scrollNote(len(rows), last-off, off); note != "" {
		b.WriteString(s.Dim.Render("  "+note) + "\n")
	}

	b.WriteString("\n")
	switch {
	case m.kubeAsk != nil:
		// the confirmation names the workload, not the pod: stopping a
		// pod is not a thing, and saying so here is the whole point
		a := m.kubeAsk
		b.WriteString(s.Caution.Render(fmt.Sprintf("%s %s in %s?",
			a.verb, a.w.Target(), a.w.Namespace)))
		if a.verb == "stop" {
			b.WriteString(
				s.Dim.Render(
					fmt.Sprintf(
						"  (%d replicas down to 0)",
						a.w.Replicas,
					),
				),
			)
		}
		b.WriteString(
			"\n" + s.Help.Render(
				"y: yes · any other key: no",
			) + "\n",
		)
	case m.kubeBusy:
		b.WriteString(s.Dim.Render("asking kubectl...") + "\n")
	case m.kubeNote != "":
		b.WriteString(s.Ok.Render(m.kubeNote) + "\n")
	}

	b.WriteString(s.Help.Render(
		"j/k: move · space: fold · h: collapse · l: logs · s: start " +
			"· x: stop · R: restart · r: refresh · q: back\n" +
			"context is explicit, always: " + m.kube.Context,
	))
	return drawOverlay(
		lipgloss.NewStyle().Padding(1, 2).Render(b.String()),
		m.width,
		m.height,
	)
}

// healthChip is the rollup a folded header carries: only the states that are
// present, because "0 broken" on every line is noise and a missing "1
// broken" is an outage nobody was shown.
//
// Every pod under the header is in exactly one bucket, so these numbers add
// up to the count beside them. They did not before: terminal pods were
// counted nowhere, which is why a row could read "kestrel- 6" with nothing
// after it -- six pods and not a word about any of them.
func healthChip(s Styles, h [5]int) string {
	out := ""
	for _, c := range []struct {
		n     int
		label string
		style lipgloss.Style
	}{
		{h[podUp], "up", s.Ok},
		{h[podWaiting], "waiting", s.Caution},
		{h[podBroken], "broken", s.Warn},
		{h[podFailed], "failed", s.Warn},
		{h[podDone], "done", s.Dim},
	} {
		if c.n > 0 {
			out += c.style.Render(
				fmt.Sprintf("  %d %s", c.n, c.label),
			)
		}
	}
	return out
}

// memCell says whether a pod has room, which is not what its phase says.
//
// The question being asked of a database is whether it has enough memory.
// Running means it has not been killed yet. It says nothing about how close it
// is, and an operator asking the second question was having to leave for
// grafana to answer it.
//
// The colours are the semantic ones, so a pod near its ceiling reads the
// same under every theme -- the same rule the context column follows, and
// the same reason: a warning that changes colour with the skin is not a
// warning.
func memCell(s Styles, p KubePod, on bool) string {
	const w = 18
	switch {
	case p.Terminal:
		// a finished pod's last reading is not news
		return s.On(s.Dim, on).Render(pad("-", w))
	case p.MemUsed == 0:
		// metrics-server absent, starting, or not reporting this pod.
		// An absent measurement is not zero bytes and must not read as
		// though the pod were idle.
		return s.On(s.Dim, on).Render(pad("-", w))
	case p.MemLimit == 0:
		// running unbounded: there is no percentage to give, and the
		// absence of a ceiling is itself the thing worth seeing
		return s.On(s.Caution, on).
			Render(pad(kubeBytes(p.MemUsed)+" ∞", w))
	}
	pct := float64(p.MemUsed) / float64(p.MemLimit) * 100
	style := s.Dim
	switch {
	case pct >= 90:
		style = s.Warn
	case pct >= 70:
		style = s.Caution
	}
	return s.On(style, on).Render(
		pad(
			fmt.Sprintf(
				"%s/%s %.0f%%",
				kubeBytes(p.MemUsed),
				kubeBytes(p.MemLimit),
				pct,
			),
			w,
		))
}
