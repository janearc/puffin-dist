package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// An address is not an identifier. prometheus scrapes most of this estate as
// IP:port, and the pane resolves those to names before showing them.
func TestTargetsAreNamedNotAddressed(t *testing.T) {
	tg := Target{Instance: "10.42.0.57:9100"}
	if !tg.Addressed() {
		t.Fatal("an unresolved address claims to be a name")
	}
	tg.Name = "kingfisher"
	if tg.Addressed() {
		t.Fatal("a resolved target still reads as an address")
	}
}

// An address is not an identifier, so the pane resolves numeric targets to
// names. Telling the two apart is where that starts.
func TestIsIPish(t *testing.T) {
	for h, want := range map[string]bool{
		"10.42.0.57": true, "k3d-local-server-0": false,
		"localhost":                           false,
		"gaggle-host.local.svc.cluster.local": false,
	} {
		if got := isIPish(h); got != want {
			t.Errorf("isIPish(%q)=%v", h, got)
		}
	}
}

// The alerting line never reports a zero it cannot stand behind. An
// unauthenticated grafana returning an empty list is not evidence of an
// empty system -- and this estate's one alert lives in grafana, so getting
// this wrong would report "nothing can fire" about a system with an alert.
func TestUnauthenticatedEmptinessIsNotZero(t *testing.T) {
	s := Compile(DodoDark())
	anon := alertingLine(s, "grafana", 0, 0, true)
	if !strings.Contains(anon, "not authenticated") {
		t.Fatalf(
			"an unauthenticated zero was reported as a zero: %q",
			anon,
		)
	}
	if strings.Contains(anon, "NO ALERT RULES") {
		t.Fatalf("puffin claimed absence it cannot see: %q", anon)
	}
	// authenticated and genuinely empty IS worth shouting about
	real := alertingLine(s, "prometheus", 0, 0, false)
	if !strings.Contains(real, "NO ALERT RULES") {
		t.Fatalf("a real zero was softened: %q", real)
	}
	// a partial view says so even when it found rules
	partial := alertingLine(s, "grafana", 1, 0, true)
	if !strings.Contains(partial, "may be partial") {
		t.Fatalf("a partial view did not say so: %q", partial)
	}
	firing := alertingLine(s, "grafana", 1, 1, false)
	if !strings.Contains(firing, "FIRING") {
		t.Fatalf("a firing alert was not loud: %q", firing)
	}
}

// The cursor's row is painted, not merely marked: a wide row loses the eye
// before the last column.
func TestCursorRowIsPainted(t *testing.T) {
	// asserted on the style and not the rendered string: lipgloss strips
	// color when stdout is not a terminal, so a test that compares rendered
	// bytes passes for the wrong reason under go test and proves nothing
	s := Compile(DodoDark())
	if bg := s.On(s.Row, false).GetBackground(); bg == lipgloss.Color(
		s.Cursor,
	) {
		t.Fatalf(
			"an unselected row is painted like the cursor's: %v",
			bg,
		)
	}
	if bg := s.On(s.Row, true).GetBackground(); bg != s.Cursor {
		t.Fatalf("the cursor's row is not painted: %v", bg)
	}
	if s.Cursor == DodoDark().Bg {
		t.Fatal(
			"the cursor's ground is the same color as the " +
				"background",
		)
	}
}
