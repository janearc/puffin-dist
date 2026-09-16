package main

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The update loop driven like an operator: keys in, screens out. These are
// the branches the earlier suite never reached because the operator screens
// did not exist when it was written.

// key wraps a rune keypress.
func key(
	r rune,
) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

// baseModel builds a model on the roster with one service.
func baseModel() model {
	// built the way main builds it, so a test can never pass against a
	// model shape the binary does not actually construct
	m := newModel("test", "")
	m.width, m.height = 120, 40
	m.enclave = Enclave{
		Services: []Service{{Name: "flipr", State: StateHealthy}},
	}
	return m
}

// TestFlagsScreenFlow: f opens flags, j/k move, enter opens the editor
// under the flag, esc closes it.
func TestFlagsScreenFlow(t *testing.T) {
	m := baseModel()
	next, cmd := m.Update(key('f'))
	m = next.(model)
	if m.scr != screenFlags || cmd == nil {
		t.Fatal("f did not open flags with a fetch")
	}
	next, _ = m.Update(flagsFetched{rows: []FlagRow{
		{
			Service:   "kestrel",
			Version:   "v1",
			Key:       "fetch.terrain",
			Kind:      "bool",
			Value:     "false",
			Expensive: true,
		},
		{
			Service: "kestrel",
			Version: "v1",
			Key:     "fetch.osrm",
			Kind:    "bool",
			Value:   "false",
		},
	}})
	m = next.(model)
	next, _ = m.Update(key('j'))
	m = next.(model)
	if m.flagCursor != 1 {
		t.Fatalf("cursor: %d", m.flagCursor)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if !m.editing {
		t.Fatal("enter did not open the editor")
	}
	// bools arrive pre-toggled: current false -> editor holds true
	if m.editValue.Value() != "true" {
		t.Fatalf("pre-toggle: %q", m.editValue.Value())
	}
	out := m.View()
	if !strings.Contains(out, "reason") {
		t.Fatal("editor does not ask for the reason")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if m.editing {
		t.Fatal("esc did not close the editor")
	}
}

// TestFlagsEditorRefusesReasonless: enter with no reason stays put and says
// why, because flipr would refuse it anyway.
func TestFlagsEditorRefusesReasonless(t *testing.T) {
	m := baseModel()
	next, _ := m.Update(key('f'))
	m = next.(model)
	next, _ = m.Update(flagsFetched{rows: []FlagRow{
		{
			Service: "s",
			Version: "v",
			Key:     "k",
			Kind:    "bool",
			Value:   "false",
		}}})
	m = next.(model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // reason empty
	m = next.(model)
	if cmd != nil || !strings.Contains(m.flagNote, "refuse") {
		t.Fatalf(
			"reasonless flip left the building: note=%q",
			m.flagNote,
		)
	}
}

// TestMapsScreenFlow: m opens the mounts, enter descends a dir, enter on a
// file runs the assessment, backspace walks up.
func TestMapsScreenFlow(t *testing.T) {
	m := baseModel()
	next, cmd := m.Update(key('m'))
	m = next.(model)
	if m.scr != screenMaps || cmd == nil {
		t.Fatal("m did not open maps")
	}
	next, _ = m.Update(
		listingFetched{l: &MapListing{Path: "/", Mount: "(mounts)",
			Entries: []MapEntry{
				{Name: "/econ/", Dir: true, Bytes: -1},
			}}},
	)
	m = next.(model)
	next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if cmd == nil || len(m.mapsStack) != 1 {
		t.Fatal("enter did not descend")
	}
	next, _ = m.Update(
		listingFetched{l: &MapListing{Path: "/econ/", Mount: "/econ/",
			Entries: []MapEntry{
				{Name: "res8.json", Bytes: 72104},
			}}},
	)
	m = next.(model)
	next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if cmd == nil {
		t.Fatal("enter on a file did not assess")
	}
	next, _ = m.Update(
		attrsFetched{
			a: MapAttrs{
				Status:      200,
				Bytes:       72104,
				ContentType: "application/json",
			},
		},
	)
	m = next.(model)
	if !strings.Contains(m.View(), "AVAILABLE") {
		t.Fatal("assessment not rendered")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = next.(model)
	if len(m.mapsStack) != 0 {
		t.Fatal("backspace did not walk up")
	}
}

// TestKubeScreenFlow: c opens the cluster, r refreshes it, q backs out.
func TestKubeScreenFlow(t *testing.T) {
	m := baseModel()
	next, cmd := m.Update(key('c'))
	m = next.(model)
	if m.scr != screenKube || cmd == nil {
		t.Fatal("c did not open the cluster screen")
	}
	next, _ = m.Update(kubeFetched{v: KubeView{Context: "k3d-local",
		Pods: []KubePod{
			{
				Namespace: "flipr",
				Name:      "flipr-x",
				Phase:     "Running",
				Ready:     "1/1",
				AllReady:  true,
			},
		}}})
	m = next.(model)
	if !strings.Contains(m.View(), "flipr-x") {
		t.Fatal("pods not rendered")
	}
	next, cmd = m.Update(key('r'))
	m = next.(model)
	if cmd == nil {
		t.Fatal("r did not refresh the cluster")
	}
	next, _ = m.Update(key('q'))
	m = next.(model)
	if m.scr != screenRoster {
		t.Fatal("q did not back out")
	}
}

// TestCliStatusAndTruth run against the fake mesh through the proxy.
func TestCliStatusAndTruth(t *testing.T) {
	srv, addr := fakeMesh(t)
	defer srv.Close()
	proxyTo(t, addr)

	out, code := capture(t, func() int { return cliStatus("faketest") })
	if code != 0 || !strings.Contains(out, "flipr") ||
		!strings.Contains(out, "authorized") {
		t.Fatalf("status: %q code %d", out, code)
	}
	out, code = capture(t, func() int { return cliTruth("faketest") })
	if code != 0 || !strings.Contains(out, "beats=313") {
		t.Fatalf("truth: %q code %d", out, code)
	}
	_ = time.Now
}
