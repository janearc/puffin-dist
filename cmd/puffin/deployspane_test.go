package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// The deploys pane answers one question: is the thing running the thing
// flipr says was deployed. Its hardest requirement is not the comparison but
// the refusal -- it must never say "confirmed" about a service it could not
// actually check, and it must say which half could not answer.

// deployFixture is a pane already holding rows, one of each verdict, so
// the drawing is tested against all three answers.
func deployFixture() *deployPane {
	return &deployPane{v: DeployView{
		Context: "k3d-local",
		Rows: []Deploy{
			{
				Service:    "flipr",
				Namespace:  "flipr",
				Image:      "ghcr.io/janearc/flipr:435fd50",
				Tag:        "435fd50",
				Digest:     "sha256:aaaa",
				BuildID:    "435fd50",
				Pods:       2,
				Ready:      2,
				Reported:   "435fd50",
				UsesFlipr:  true,
				DeclaredNS: "flipr",
				FliprNS:    "flipr",
				NSState:    "ok",
				Health:     "ok",
				Agree:      AgreeYes,
			},
			{
				Service:    "athlete",
				Namespace:  "local",
				Image:      "kestrel-panes:dev",
				Tag:        "dev",
				Digest:     "sha256:bbbb",
				Pods:       1,
				Ready:      1,
				Reported:   "c065933",
				UsesFlipr:  true,
				DeclaredNS: "athlete",
				FliprNS:    "athlete",
				NSState:    "ok",
				Health:     "ok",
				Agree:      AgreeNo,
				BuildID:    "e664b0a",
				SharedWith: []string{"explorer"},
			},
			{
				Service:   "kingfisher",
				Namespace: "local",
				Image:     "kingfisher:dev",
				Tag:       "dev",
				Digest:    "sha256:cccc",
				Pods:      1,
				Ready:     0,
				Reported:  "",
				Health:    "no route",
				NSState:   "missing",
				Agree:     AgreeUnknowable,
			},
			{
				Service:   "dodo",
				Namespace: "local",
				Image:     "dodo:dev",
				Tag:       "dev",
				Digest:    "sha256:dddd",
				Pods:      1,
				Ready:     1,
				Reported:  "",
				Health:    "no version",
				NSState:   "undeclared",
				Agree:     AgreeUnknowable,
			},
		},
	}}
}

// Before the verdicts, what is being compared. A column of verdicts you have
// to decode is a column people learn to skip.
func TestDeployPaneExplainsItselfFirst(t *testing.T) {
	out := deployFixture().View(Compile(DodoDark()), 180, 44)
	if !strings.Contains(out, "flipr records the commit") {
		t.Error("the pane did not say what it is comparing")
	}
	if !strings.Contains(out, "1 wrong build") ||
		!strings.Contains(out, "2 uncheckable") ||
		!strings.Contains(out, "1 confirmed") {
		t.Errorf("the tally is wrong: %q", firstLines(out, 6))
	}
}

// "31 uncheckable" is a number to shrug at; "24 report no version" is a
// ticket. The pane counts WHY.
func TestDeployPaneCountsWhyNotJustHowMany(t *testing.T) {
	out := deployFixture().View(Compile(DodoDark()), 180, 44)
	if !strings.Contains(out, "answer /health with no version") {
		t.Error("the pane did not say why a service was uncheckable")
	}
	if !strings.Contains(out, "have no route to ask") {
		t.Error("a service with no route was not counted separately")
	}
	// the flag namespace is a configuration check, not a build check, and
	// it gets its own line
	if !strings.Contains(out, "no usable flag namespace") {
		t.Error("a broken flag namespace was not reported")
	}
	if !strings.Contains(out, "declare none and fall back") {
		t.Error(
			"an undeclared namespace was not distinguished from " +
				"a broken one",
		)
	}
}

// cannotSay names which half could not answer, because "the image is not
// stamped" and "the service cannot be reached" are different things to fix.
func TestCannotSayNamesTheMissingHalf(t *testing.T) {
	for _, tc := range []struct {
		name string
		d    Deploy
		want string
	}{
		{"no route", Deploy{Service: "kingfisher", Health: "no route"},
			"it cannot be asked what it is running"},
		{"no version", Deploy{Service: "dodo", Health: "ok"},
			"answers /health without a version"},
		{
			"neither is a commit",
			Deploy{Service: "x", Tag: "dev", Reported: "1.2.3"},
			"neither is a commit"},
		{"tag says nothing but the binary does",
			Deploy{Service: "x", Tag: "dev", Reported: "435fd50"},
			"says nothing, but it reports 435fd50"},
		{"reports a non-commit", Deploy{
			Service: "x", Tag: "435fd50",
			BuildID: "435fd50", Reported: "1.2.3"},
			"which is not a commit"},
	} {
		if got := cannotSay(tc.d); !strings.Contains(got, tc.want) {
			t.Errorf(
				"%s: %q does not say %q",
				tc.name,
				got,
				tc.want,
			)
		}
	}
}

// A verdict of NO must show both sides, so the reader can see which one to
// go and fix.
func TestDeployPaneShowsBothSidesOfADisagreement(t *testing.T) {
	out := deployFixture().View(Compile(DodoDark()), 180, 44)
	if !strings.Contains(
		out,
		"NO -- image is e664b0a, it reports c065933",
	) {
		t.Error("a disagreement did not show both commits")
	}
	if !strings.Contains(out, "yes, 435fd50") {
		t.Error("a confirmed row did not name the commit it confirmed")
	}
}

// Two services on one digest is worth saying out loud: one build, two names.
func TestDeployPaneNamesSharedImages(t *testing.T) {
	p := deployFixture()
	p.cursor = 1
	out := p.View(Compile(DodoDark()), 180, 44)
	if !strings.Contains(out, "one build, two names") {
		t.Error("a shared image was not called out")
	}
	if !strings.Contains(out, "explorer") {
		t.Error("the pane did not say who it is shared with")
	}
}

// Before anything came back the pane says what it is asking, rather than
// rendering an empty table that reads as "nothing is deployed".
func TestDeployPaneEmptyStates(t *testing.T) {
	p := &deployPane{}
	if out := p.View(Compile(DodoDark()), 180, 44); !strings.Contains(
		out,
		"asking kubectl and flipr",
	) {
		t.Error("an unloaded pane did not say what it was doing")
	}
	p.v.Warnings = []string{"flipr: connection refused"}
	if out := p.View(Compile(DodoDark()), 180, 44); !strings.Contains(
		out,
		"flipr: connection refused",
	) {
		t.Error("a warning was hidden")
	}
	// a filter that matches nothing says so, and says how to get out of it
	p = deployFixture()
	p.filter = "nosuchtag"
	out := p.View(Compile(DodoDark()), 180, 44)
	if !strings.Contains(out, "nothing is running tag :nosuchtag") {
		t.Error("an empty filter did not explain itself")
	}
	if !strings.Contains(out, "esc clears") {
		t.Error("the pane did not say how to clear the filter")
	}
}

// f cycles the tags, esc clears, and the cursor never lands on a row nobody
// can see -- everything that indexes the list goes through rows().
func TestDeployPaneFilterCycles(t *testing.T) {
	p := deployFixture()
	tags := p.tags() // dev, 435fd50 sorted
	p.cursor = 3

	p.Update(key2('f'))
	if p.filter != tags[0] {
		t.Fatalf("first f: filter %q, want %q", p.filter, tags[0])
	}
	if p.cursor != 0 {
		t.Fatalf("the filter left the cursor at %d", p.cursor)
	}
	if !strings.Contains(
		p.View(Compile(DodoDark()), 180, 44),
		"tag :"+tags[0]+" only",
	) {
		t.Error("the pane does not say a filter is on")
	}
	if !p.HandlesEsc() {
		t.Error("a filtered pane did not claim esc")
	}
	for range tags {
		p.Update(key2('f'))
	}
	if p.filter != "" {
		t.Fatalf("f did not cycle back to everything: %q", p.filter)
	}
	if p.HandlesEsc() {
		t.Error("an unfiltered pane claimed esc")
	}
	// esc clears whatever is set
	p.Update(key2('f'))
	p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if p.filter != "" || p.cursor != 0 {
		t.Fatalf("esc left filter %q cursor %d", p.filter, p.cursor)
	}
}

// s cycles the sort, and the selected service follows the sort rather than
// the cursor staying on a row number that now means something else.
func TestDeployPaneSortKeepsTheSelection(t *testing.T) {
	p := deployFixture()
	p.cursor = 0
	p.sel = p.at()
	first := p.sel

	for i := range deploySorts {
		p.Update(key2('s'))
		if p.sortBy != (i+1)%len(deploySorts) {
			t.Fatalf("sortBy %d", p.sortBy)
		}
		if got := p.at(); got != first {
			t.Fatalf(
				"sort %d moved the selection from %q to %q",
				p.sortBy,
				first,
				got,
			)
		}
	}
	// the header marks which column is sorted
	p.sortBy = 0
	if !strings.Contains(p.View(Compile(DodoDark()), 180, 44), "service▾") {
		t.Error("the sorted column is not marked")
	}
}

// The cursor stops at both ends. A cursor that runs past the last row
// indexes into nothing and takes the pane with it.
func TestDeployPaneMovesAndClamps(t *testing.T) {
	p := deployFixture()
	p.Update(key2('j'))
	if p.cursor != 1 || p.sel != "athlete" {
		t.Fatalf("j: cursor %d sel %q", p.cursor, p.sel)
	}
	for i := 0; i < 10; i++ {
		p.Update(key2('j'))
	}
	if p.cursor != len(p.v.Rows)-1 {
		t.Fatalf("j past the end: %d", p.cursor)
	}
	for i := 0; i < 10; i++ {
		p.Update(key2('k'))
	}
	if p.cursor != 0 {
		t.Fatalf("k past the start: %d", p.cursor)
	}
	// a read that comes back shorter must not leave the cursor past the end
	p.cursor = 3
	p.Update(deployFetched{DeployView{Rows: deployFixture().v.Rows[:1]}})
	if p.cursor != 0 {
		t.Fatalf("cursor %d after a shorter read", p.cursor)
	}
}

// The table scrolls and says how much is off screen: a wrong build hidden
// below the fold with no sign of it is the failure this pane exists to stop.
func TestDeployPaneSaysWhatIsOffScreen(t *testing.T) {
	p := deployFixture()
	for i := 0; i < 40; i++ {
		p.v.Rows = append(
			p.v.Rows,
			Deploy{Service: "svc", Namespace: "local", Tag: "dev",
				Pods:   1,
				Ready:  1,
				Agree:  AgreeUnknowable,
				Health: "ok"},
		)
	}
	p.cursor = len(p.v.Rows) - 1
	if out := p.View(Compile(DodoDark()), 180, 30); !strings.Contains(
		out,
		"more above",
	) {
		t.Error("rows scrolled off the top with no sign of it")
	}
	p.cursor, p.offset = 0, 0
	if out := p.View(Compile(DodoDark()), 180, 30); !strings.Contains(
		out,
		"more below",
	) {
		t.Error("rows below the fold with no sign of it")
	}
}

// Nothing may render outside the terminal, at any size.
func TestDeployPaneDrawsInATinyWindow(t *testing.T) {
	p := deployFixture()
	for _, wh := range [][2]int{{40, 6}, {20, 0}, {200, 60}} {
		if out := p.View(Compile(DodoDark()), wh[0], wh[1]); out == "" {
			t.Errorf("%dx%d drew nothing", wh[0], wh[1])
		}
	}
}

// firstLines is for error messages: the whole render is too much to read in
// a test failure.
func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
