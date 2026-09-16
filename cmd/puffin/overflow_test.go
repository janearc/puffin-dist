package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// Nothing may render outside the terminal, at any size.
//
// This is an accessibility test, not a cosmetic one. Tired eyes resize the
// FONT, and a bigger font is a smaller terminal in rows and columns.
//
// Measured before the fix: at 96 columns every row of the cluster screen
// rendered 108 wide and wrapped, and the agents pane emitted 26 lines into a
// 14-line terminal. The tool you reach for when you are least able to read it
// was the one that came apart.
//
// The sizes below are what a 162x44 terminal becomes as the font grows.
func TestNothingRendersOutsideTheTerminal(t *testing.T) {
	sizes := [][2]int{
		{162, 44},
		{130, 36},
		{108, 30},
		{96, 26},
		{80, 22},
		{68, 18},
		{54, 14},
	}

	var pods []KubePod
	for i := 0; i < 40; i++ {
		pods = append(pods, KubePod{
			Namespace: "local",
			Name: "a-fairly-long-pod-name-here-" + string(
				rune('a'+i%26),
			),
			Phase:    "Running",
			Ready:    "1/1",
			Age:      "1d",
			Restarts: i,
		})
	}
	sessions := []AgentSession{{
		ID:        "a",
		Project:   "dev/puffin",
		Name:      "puffin-dev",
		Branch:    "puffin-dev",
		Model:     "claude-opus-5",
		Turns:     3684,
		Context:   589000,
		CostKnown: true,
		CostUSD:   384.28, LinesAdded: 3241, LinesRemoved: 423,
	}, {
		ID:      "b",
		Project: "prod/cuckoo",
		Name:    "cuckoo-worker",
		Branch:  "fix/flattening",
		Model:   "claude-opus-5", Turns: 1769,
	}}

	screens := []struct {
		name string
		set  func(m *model)
	}{
		{"cluster", func(m *model) {
			m.scr = screenKube
			m.kube = KubeView{Context: homeContext(), Pods: pods}
		}},
		{"roster", func(m *model) { m.scr = screenRoster }},
		{"agents", func(m *model) {
			p := &agentPane{v: AgentView{Sessions: sessions}}
			m.scr, m.openPane = screenPane, p
		}},
	}

	for _, scr := range screens {
		for _, sz := range sizes {
			w, h := sz[0], sz[1]
			m := newModel("test", "")
			m.width, m.height = w, h
			scr.set(&m)

			lines := strings.Split(m.View(), "\n")
			for i, l := range lines {
				if n := ansi.StringWidth(ansi.Strip(l)); n > w {
					t.Errorf(
						"%s at %dx%d: line %d is %d "+
							"columns wide and "+
							"the terminal is %d",
						scr.name,
						w,
						h,
						i,
						n,
						w,
					)
					break
				}
			}
			if len(lines) > h {
				t.Errorf(
					"%s at %dx%d: rendered %d lines into "+
						"%d rows",
					scr.name,
					w,
					h,
					len(lines),
					h,
				)
			}
		}
	}
}
