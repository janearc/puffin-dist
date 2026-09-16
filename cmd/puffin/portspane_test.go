package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// The ports pane is an audit, and its editing is deliberately asymmetric:
// stopping a forward is one key and a confirmation, starting one asks for a
// name and says out loud that a forward is host state no repository knows
// about.
//
// These tests hold it to both halves -- and never press the confirmation key,
// because the thing behind it kills a process.

// portsFixture is a pane already holding all four sources, so the drawing
// is tested against every kind of port at once.
func portsFixture() *portsPane {
	return &portsPane{v: PortView{
		Context: "k3d-local",
		Host: HostFacts{
			Host:   "kestrel",
			User:   "dev",
			Uptime: "9:32",
		},
		Forwards: []Forward{
			{
				Local:  "8080",
				Remote: "svc/flipr:9800",
				NS:     "flipr",
				PID:    4242,
				Raw: "kubectl port-forward svc/flipr " +
					"8080:9800",
			},
			{
				Local:  "5432",
				Remote: "svc/postgres:5432",
				NS:     "local",
				PID:    4243,
				Raw: "kubectl port-forward svc/postgres " +
					"5432:5432",
			},
		},
		Services: []SvcPort{
			{
				Name:  "traefik",
				NS:    "kube-system",
				Type:  "LoadBalancer",
				Ports: []string{"80:30080"},
			},
		},
		Mappings: []Mapping{
			{
				Container: "k3d-local-serverlb",
				Published: "0.0.0.0:8443->443/tcp",
			},
		},
	}}
}

// The audit is only true for the machine puffin runs on. Reading "none"
// without knowing whose "none" is how you conclude the estate is clean while
// somebody else's laptop holds a tunnel open into the cluster.
func TestPortsPaneNamesWhoseAuditThisIs(t *testing.T) {
	out := portsFixture().View(Compile(DodoDark()), 140, 44)
	if !strings.Contains(out, "kestrel") || !strings.Contains(out, "dev") {
		t.Error("the pane did not say whose machine this is")
	}
	if !strings.Contains(out, "k3d-local") {
		t.Error("the pane did not say which cluster")
	}
	if !strings.Contains(out, "THIS machine only") {
		t.Error("the pane did not bound its own claim")
	}
}

// Each source has to appear as itself. An ad-hoc forward drawn like a
// declared service is the fact this pane exists to show.
func TestPortsPaneListsAllThreeSources(t *testing.T) {
	out := portsFixture().View(Compile(DodoDark()), 140, 44)
	for _, want := range []string{
		":8080", "svc/flipr:9800", "pid 4242", // the ad-hoc forwards
		"traefik", "LoadBalancer", // services that escape the cluster
		"k3d-local-serverlb", // what k3d published at creation
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the audit is missing %q", want)
		}
	}
}

// An empty audit is the good outcome and says so in words, per section --
// "none" against the wrong heading is how the wrong conclusion gets drawn.
func TestPortsPaneEmptySections(t *testing.T) {
	p := &portsPane{
		v: PortView{
			Context: "k3d-local",
			Host:    HostFacts{Host: "kestrel"},
		},
	}
	out := p.View(Compile(DodoDark()), 140, 44)
	if !strings.Contains(out, "nothing is standing in for a name") {
		t.Error("no forwards was not stated")
	}
	if !strings.Contains(
		out,
		"everything is ClusterIP, reachable by name",
	) {
		t.Error("no escaping services was not stated")
	}
	p.v.Warnings = []string{"host processes: ps failed"}
	if out := p.View(Compile(DodoDark()), 140, 44); !strings.Contains(
		out,
		"ps failed",
	) {
		t.Error(
			"a warning was hidden, so a partial audit looked " +
				"complete",
		)
	}
}

// Stopping asks first, and what it shows is what it would run.
func TestPortsPaneStopArmsAConfirmation(t *testing.T) {
	p := portsFixture()
	p.cursor = 0
	p.Update(key2('x'))
	if p.pending == nil {
		t.Fatal("x did not ask before killing a process")
	}
	i := p.pending.intent
	if i.Verb != "stop forward" ||
		!strings.Contains(i.Wire, "kill -TERM 4242") {
		t.Fatalf("intent: %+v", i)
	}
	if !strings.Contains(p.View(Compile(DodoDark()), 140, 44), "4242") {
		t.Error("the confirmation does not show what it would signal")
	}
	// any key but the confirmation cancels, and says so
	p.Update(key2('z'))
	if p.pending != nil {
		t.Fatal("the confirmation survived a wrong key")
	}
	if p.note != "cancelled" {
		t.Fatalf("note %q", p.note)
	}
	// and with nothing to stop, x says so rather than arming on nothing
	empty := &portsPane{v: PortView{Host: HostFacts{Host: "kestrel"}}}
	empty.Update(key2('x'))
	if empty.pending != nil {
		t.Fatal("x armed with no forwards")
	}
	if empty.note != "no forwards to stop" {
		t.Fatalf("note %q", empty.note)
	}
}

// Starting one is typed, and the composer owns the keyboard while it is
// open so q does not close the pane mid-word.
func TestPortsPaneComposeAForward(t *testing.T) {
	p := portsFixture()
	p.Update(key2('n'))
	if !p.Capturing() {
		t.Fatal("n did not open the composer")
	}
	for _, r := range "flipr 8080q" {
		p.Update(key2(r))
	}
	if p.entry != "flipr 8080q" {
		t.Fatalf(
			"entry %q -- q closed the pane instead of being typed",
			p.entry,
		)
	}
	p.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if p.entry != "flipr 8080" {
		t.Fatalf("entry %q", p.entry)
	}
	if !strings.Contains(
		p.View(Compile(DodoDark()), 140, 44),
		"flipr 8080",
	) {
		t.Error("what is being typed is not on screen")
	}
	p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if p.pending == nil {
		t.Fatal("enter did not arm the forward")
	}
	// what it shows is the exact kubectl it would run
	if w := p.pending.intent.Wire; !strings.Contains(w, "port-forward") ||
		!strings.Contains(
			w,
			"svc/flipr",
		) || !strings.Contains(w, "8080:8080") {
		t.Fatalf("wire %q", w)
	}
	p.Update(key2('z')) // cancel; never confirm, it would start a process

	// a malformed entry is refused with the reason, and arms nothing
	p.Update(key2('n'))
	for _, r := range "nonsense" {
		p.Update(key2(r))
	}
	p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if p.pending != nil {
		t.Fatal("a malformed forward armed anyway")
	}
	if p.note == "" {
		t.Error(
			"a refusal with no reason is a refusal nobody can " +
				"act on",
		)
	}
	// esc abandons the composer
	p.Update(key2('n'))
	p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if p.typing || p.entry != "" {
		t.Fatal("esc did not abandon the composer")
	}
}

// The cursor moves without refetching, and a refresh keeps the cursor
// where it was.
func TestPortsPaneMovesAndReReads(t *testing.T) {
	p := portsFixture()
	p.Update(key2('j'))
	if p.cursor != 1 {
		t.Fatalf("j: %d", p.cursor)
	}
	p.Update(key2('j'))
	if p.cursor != 1 {
		t.Fatalf("j past the end: %d", p.cursor)
	}
	p.Update(key2('k'))
	p.Update(key2('k'))
	if p.cursor != 0 {
		t.Fatalf("k past the start: %d", p.cursor)
	}
	// a read that comes back shorter must not leave the cursor past the end
	p.cursor = 1
	p.Update(portsFetched{PortView{Host: HostFacts{Host: "kestrel"}}})
	if p.cursor != 0 {
		t.Fatalf("cursor %d after an empty read", p.cursor)
	}
	// acting on a forward re-reads: the audit must reflect what just
	// changed
	np, cmd := p.Update(portsActed{note: "stopped 4242 (svc/flipr:9800)"})
	if cmd == nil {
		t.Fatal("the pane did not re-read after acting")
	}
	if np.(*portsPane).note == "" {
		t.Error("what happened was not reported")
	}
	if !strings.Contains(
		p.View(Compile(DodoDark()), 140, 44),
		"stopped 4242",
	) {
		t.Error("the receipt is not on screen")
	}
}

// Nothing may render outside the terminal, at any size.
func TestPortsPaneDrawsInATinyWindow(t *testing.T) {
	p := portsFixture()
	for _, wh := range [][2]int{{40, 6}, {20, 0}, {200, 60}} {
		if out := p.View(Compile(DodoDark()), wh[0], wh[1]); out == "" {
			t.Errorf("%dx%d drew nothing", wh[0], wh[1])
		}
	}
}
