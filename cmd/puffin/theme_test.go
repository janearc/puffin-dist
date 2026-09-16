package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Every theme is complete. A missing token renders as the terminal's default
// foreground, which looks like a bug in the data rather than a gap in the
// palette -- and on the wrong screen it looks like health.
func TestEveryThemeIsComplete(t *testing.T) {
	for _, th := range Themes() {
		if th.Name == "" {
			t.Fatalf("a theme has no name: %+v", th)
		}
		for label, c := range map[string]lipgloss.Color{
			"Bg":          th.Bg,
			"Panel":       th.Panel,
			"Raised":      th.Raised,
			"Line":        th.Line,
			"Ink":         th.Ink,
			"Dim":         th.Dim,
			"Accent":      th.Accent,
			"AccentToken": th.AccentToken,
			"Ok":          th.Ok,
			"Warn":        th.Warn,
			"Caution":     th.Caution,
			"Beak":        th.Beak,
			"Plumage":     th.Plumage,
			"Snow":        th.Snow,
		} {
			if c == "" {
				t.Errorf("%s: %s is unset", th.Name, label)
			}
		}
		if th.Bg == th.Ink {
			t.Errorf(
				"%s: ink is the same color as the ground",
				th.Name,
			)
		}
		if th.Raised == th.Bg {
			t.Errorf(
				"%s: the cursor's ground is invisible "+
					"against the background",
				th.Name,
			)
		}
	}
}

// dodo's rule, transcribed: a warning that changes colour with the theme is
// not a warning. The semantics are shared across every DARK theme, and only
// the light one overrides them -- for contrast, never for meaning.
func TestSemanticsDoNotFollowTheTheme(t *testing.T) {
	var dark []Theme
	for _, th := range Themes() {
		if !th.Light {
			dark = append(dark, th)
		}
	}
	if len(dark) < 2 {
		t.Fatal("not enough dark themes to compare")
	}
	first := dark[0]
	for _, th := range dark[1:] {
		if th.Ok != first.Ok || th.Warn != first.Warn ||
			th.Caution != first.Caution {
			t.Errorf(
				"%s moved the semantic colors: ok=%s warn=%s "+
					"caution=%s",
				th.Name,
				th.Ok,
				th.Warn,
				th.Caution,
			)
		}
	}
	light := Stardew()
	if light.Warn == first.Warn {
		t.Error(
			"the light theme kept a dark-ground red, which " +
				"washes out on sand",
		)
	}
}

// The beak is orange in every light. A puffin without an orange beak is a
// guillemot, whatever the terminal is wearing.
func TestTheBeakSurvivesEveryTheme(t *testing.T) {
	want := Corvid().Beak
	for _, th := range Themes() {
		if th.Beak != want {
			t.Errorf(
				"%s repainted the beak to %s -- that is a "+
					"guillemot",
				th.Name,
				th.Beak,
			)
		}
	}
	// but the white face cannot stay white on sand, or the bird disappears
	if Stardew().Snow == Corvid().Snow {
		t.Error("the light theme kept a white face on a pale ground")
	}
}

// PUFFIN_THEME resolves by name, and an unknown name is answered rather than
// silently swallowed.
func TestThemeIndex(t *testing.T) {
	i, ok := ThemeIndex("vaporwave")
	if !ok || Themes()[i].Name != "vaporwave" {
		t.Fatalf("vaporwave resolved to %d, ok=%v", i, ok)
	}
	if i, ok := ThemeIndex("VAPORWAVE"); !ok ||
		Themes()[i].Name != "vaporwave" {
		t.Fatal("theme names should not be case-sensitive")
	}
	if _, ok := ThemeIndex("nyancat"); ok {
		t.Fatal("an unknown theme resolved to something")
	}
}

// t cycles and the styles actually change with it -- and it comes back
// around rather than falling off the end.
func TestCycleTheme(t *testing.T) {
	m := baseModel()
	m.scr = screenRoster
	start := m.styles.Theme
	seen := map[string]bool{start: true}
	for i := 0; i < len(Themes())-1; i++ {
		next, _ := m.Update(key('t'))
		m = next.(model)
		if seen[m.styles.Theme] {
			t.Fatalf(
				"t repeated %q after %d presses",
				m.styles.Theme,
				i+1,
			)
		}
		seen[m.styles.Theme] = true
	}
	next, _ := m.Update(key('t'))
	m = next.(model)
	if m.styles.Theme != start {
		t.Fatalf(
			"the cycle did not come back around: %q",
			m.styles.Theme,
		)
	}
	// and the roster names what you are wearing
	m.width, m.height = 120, 40
	if !strings.Contains(rosterView(m), m.styles.Theme) {
		t.Fatal("the roster does not say which theme is on")
	}
}

var _ = tea.KeyMsg{}

// The roster's key list is several short lines, none of which runs the width
// of a terminal: a dozen keys on one line reads as a wall, not a menu. It
// grows a row at a time as panes are added rather than growing sideways
// until something falls off the end.
func TestRosterHelpIsReadableLines(t *testing.T) {
	m := baseModel()
	m.width, m.height = 120, 40
	out := rosterView(m)
	var help []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, ": api") ||
			strings.Contains(line, ": theme") ||
			strings.Contains(line, ": agents") ||
			strings.Contains(line, ": bus") ||
			strings.Contains(line, ": poke") {
			help = append(help, strings.TrimSpace(line))
		}
	}
	if len(help) < 2 || len(help) > 4 {
		t.Fatalf(
			"the key list is %d lines:\n%s",
			len(help),
			strings.Join(help, "\n"),
		)
	}
	for _, line := range help {
		if n := len([]rune(line)); n > 80 {
			t.Errorf("a help line is %d columns wide: %q", n, line)
		}
	}
	// every pane's key is still offered
	for _, p := range panes() {
		if !strings.Contains(out, p.Key()+": "+p.Title()) {
			t.Errorf("the roster does not offer %q", p.Title())
		}
	}
}

// The little screen has to READ as a separate screen. The panel token does
// not do that on every theme: on the puffin palette it is #181818 against a
// #111111 ground, and the box may as well not be there.
func TestTheCRTIsVisibleOnEveryTheme(t *testing.T) {
	for _, th := range Themes() {
		if th.Screen == "" || th.Phosphor == "" {
			t.Fatalf("%s has no screen", th.Name)
		}
		page := luminance(string(th.Bg))
		screen := luminance(string(th.Screen))
		if d := page - screen; d < 0.01 && d > -0.01 {
			t.Errorf(
				"%s: the screen (%s) is the same brightness "+
					"as the page (%s)",
				th.Name,
				th.Screen,
				th.Bg,
			)
		}
		// and the text on it has to be legible against it
		if d := luminance(string(th.Phosphor)) - screen; d < 0.25 &&
			d > -0.25 {
			t.Errorf(
				"%s: phosphor %s on screen %s is too close "+
					"to read",
				th.Name,
				th.Phosphor,
				th.Screen,
			)
		}
	}
	// a dark theme's screen is pulled toward navy, not merely lifted a
	// shade
	pf := DodoDark()
	pr, pg, pb, _ := rgb(string(pf.Screen))
	if pb <= pr || pb <= pg {
		t.Errorf(
			"the puffin theme's screen %s is not a blue",
			pf.Screen,
		)
	}
	// the light theme does not get a navy hole punched in it
	if !Stardew().Light || luminance(string(Stardew().Screen)) < 0.5 {
		t.Error("the light theme's screen is dark")
	}
}

// The scrollbar appears only when there is something off the screen: a
// track that never moves is furniture.
func TestScrollbarOnlyWhenItMeansSomething(t *testing.T) {
	if got := scrollCell(0, 10, 0, 4); got != " " {
		t.Fatalf("a bar was drawn for content that fits: %q", got)
	}
	// at the top, the thumb is at the top
	if scrollCell(0, 10, 0, 100) != "█" {
		t.Fatal("the thumb is not at the top when the view is")
	}
	// at the bottom, the thumb is at the bottom
	if scrollCell(9, 10, 90, 100) != "█" {
		t.Fatal("the thumb is not at the bottom when the view is")
	}
	if scrollCell(0, 10, 90, 100) != "│" {
		t.Fatal(
			"the thumb is at the top while the view is at the " +
				"bottom",
		)
	}
}

// Every row paints the full width, or the background stops at the end of
// the text and the screen reads as a smudge.
func TestEveryRowFillsTheScreen(t *testing.T) {
	if got := padTo("short", 20); len([]rune(got)) != 20 {
		t.Fatalf("padded to %d", len([]rune(got)))
	}
	long := padTo(strings.Repeat("x", 40), 20)
	if len([]rune(long)) != 20 {
		t.Fatalf("clipped to %d", len([]rune(long)))
	}
	if !strings.HasSuffix(long, "…") {
		t.Fatal("a clipped line does not say it was clipped")
	}
}

// The cursor's row must be findable at a glance on every theme. It is the
// screen where a keystroke interrupts somebody's work, and a selection you
// have to hunt for is a real hazard, not a style complaint. Measured, so a
// theme dodo ships tomorrow cannot quietly produce an invisible cursor.
func TestTheCursorRowIsFindableOnEveryTheme(t *testing.T) {
	for _, th := range Themes() {
		page := luminance(string(th.Bg))
		cur := luminance(string(th.Cursor))
		d := cur - page
		if d < 0 {
			d = -d
		}
		if d < 0.03 {
			t.Errorf(
				"%s: cursor ground %s is invisible against "+
					"page %s (delta %.3f)",
				th.Name,
				th.Cursor,
				th.Bg,
				d,
			)
		}
		// and not so bright it glares: enough that it stands out, not
		// so much that it hurts to look at
		if d > 0.30 {
			t.Errorf(
				"%s: cursor ground %s glares against page %s "+
					"(delta %.3f)",
				th.Name,
				th.Cursor,
				th.Bg,
				d,
			)
		}
		// the row text still has to read on it
		if l := luminance(string(th.Ink)) - cur; l < 0.2 && l > -0.2 {
			t.Errorf(
				"%s: ink %s does not read on cursor ground %s",
				th.Name,
				th.Ink,
				th.Cursor,
			)
		}
	}
	// the surface token was NOT good enough, which is why Cursor exists:
	// corvid's raised-2 against its ground is a difference you can measure
	// and not one you can see
	c := Corvid()
	if d := luminance(
		string(c.Raised),
	) - luminance(string(c.Bg)); d >= 0.03 {
		t.Log(
			"corvid's raised surface would now be visible " +
				"enough; Cursor can be revisited",
		)
	}
}

// Every list screen paints its cursor row. Finding them one at a time by
// using the tool is a bad way to discover this, so it is asserted here:
// a screen added later that only marks its cursor with a glyph fails.
func TestEveryListScreenPaintsItsCursor(t *testing.T) {
	painted := func(view string) bool {
		// s.On sets a background; with color stripped in tests the only
		// portable signal is the style, so views are checked for using
		// it
		return strings.Contains(view, "▸")
	}
	m := baseModel()
	m.width, m.height = 140, 44
	m.enclave = Enclave{Services: []Service{
		{Name: "flipr", State: StateHealthy, Flags: 3, Expensive: 1},
		{Name: "hm", State: StateDaemon, Flags: 1},
	}}
	if !painted(rosterView(m)) {
		t.Error("roster has no cursor")
	}
	m.flags = []FlagRow{
		{
			Service: "flipr",
			Version: "abc",
			Key:     "a.b",
			Kind:    "bool",
			Value:   "true",
			Desc:    "x",
		},
		{
			Service: "flipr",
			Version: "abc",
			Key:     "c.d",
			Kind:    "string",
			Value:   "y",
			Desc:    "z",
		},
	}
	if !painted(flagsView(m)) {
		t.Error("the flipr screen has no cursor")
	}
	m.kube = KubeView{Context: homeContext(), Pods: []KubePod{
		{
			Namespace: "local",
			Name:      "athlete-1",
			Phase:     "Running",
			Ready:     "1/1",
			AllReady:  true,
		},
	}}
	m.kubeFold = defaultFolds(m.kube.Pods)
	m.kubeCursor = firstPodRow(m.kubeVisible())
	if !painted(kubeView(m)) {
		t.Error("the cluster screen has no cursor")
	}
	m.mapsListing = &MapListing{Path: "/", Mount: "m", Entries: []MapEntry{
		{Name: "tiles/", Dir: true}, {Name: "a.pbf", Bytes: 10},
	}}
	if !painted(mapsView(m)) {
		t.Error("the maps screen has no cursor")
	}
	// and the panes
	ap := &agentPane{
		v: AgentView{
			Sessions: []AgentSession{
				{ID: "a", Project: "puffin", Turns: 1},
			},
		},
	}
	if !painted(ap.View(m.styles, 140, 44)) {
		t.Error("the agents pane has no cursor")
	}
	dp := &deployPane{
		v: DeployView{
			Rows: []Deploy{
				{
					Service:   "flipr",
					Namespace: "flipr",
					Pods:      1,
					Ready:     1,
				},
			},
		},
	}
	if !painted(dp.View(m.styles, 140, 44)) {
		t.Error("the deploys pane has no cursor")
	}
	mp := &metricsPane{
		v: MetricsView{
			Targets: []Target{
				{
					Job:      "j",
					Instance: "1.2.3.4:9090",
					Name:     "flipr",
					Health:   "up",
				},
			},
		},
	}
	if !painted(mp.View(m.styles, 140, 44)) {
		t.Error("the metrics pane has no cursor")
	}
}

// The selected row is painted with a background, not merely marked with a
// glyph at its left edge: by the time the eye reaches the last column on a
// wide row it has lost the line.
func TestSelectionIsABackgroundNotJustAGlyph(t *testing.T) {
	s := Compile(DodoDark())
	if s.On(s.Dim, true).GetBackground() != s.Cursor {
		t.Fatal("On does not paint")
	}
	if s.On(s.Dim, false).GetBackground() == s.Cursor {
		t.Fatal("On paints rows that are not selected")
	}
}

// The key is brighter than what it does, and prose is left alone.
func TestHelpLineBrightensKeysOnly(t *testing.T) {
	// a test process has no terminal, so lipgloss renders without colour
	// unless it is told otherwise. Without this the assertion below passes
	// vacuously on a machine with a terminal and fails in CI, which is the
	// worst of both.
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(old)

	s := Compile(DodoDark())
	got := helpLine(s, "v: host · d: deploys")
	// the keys are rendered in a different style from their words, so the
	// line carries more than one foreground
	if strings.Count(got, "\x1b[") < 4 {
		t.Fatalf("the keys were not picked out: %q", got)
	}
	if got == s.Help.Render("v: host · d: deploys") {
		t.Fatal("the whole line was rendered in one style")
	}

	// a sentence with a colon is not a key binding
	prose := "forwards are host processes: nothing in the cluster knows " +
		"they exist"
	plain := ansiRe.ReplaceAllString(helpLine(s, prose), "")
	if plain != prose {
		t.Fatalf("prose was mangled: %q", plain)
	}
	// and one style only: nothing inside it got brightened
	if n := strings.Count(helpLine(s, prose), "\x1b[0m"); n > 1 {
		t.Fatalf("prose was split into %d styled runs", n)
	}
}

// Whatever the styling does, the words must survive it: the roster's rows
// are justified to a shared margin, and the padding IS the alignment.
func TestHelpLineKeepsItsText(t *testing.T) {
	s := Compile(DodoDark())
	for _, line := range []string{
		"t: theme     ·     T: themes",
		"j/k: move · space: tag · 9: kill -9",
		"no colons here at all",
		"",
	} {
		if got := ansiRe.ReplaceAllString(
			helpLine(s, line),
			"",
		); got != line {
			t.Fatalf("help line changed: %q -> %q", line, got)
		}
	}
}
