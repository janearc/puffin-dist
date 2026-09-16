package main

import (
	"strings"
	"testing"
)

// dodo's real files, trimmed to the shapes that matter: the base :root that
// IS corvid, and one override block keyed off data-theme.
const midnightCSS = `
/* midnight -- the default skin */
:root{
  --ground:#0b0e14; --raised:#11161f; --raised-2:#151b26;
  --panel:var(--raised); --panel-2:var(--raised-2);
  --ink:#e6edf7; --dim:#8b98ad; --faint:#5f6b7e;
  --rule:#232d3d; --rule-strong:#2b3648;
  --amber:#e8a33d; --blue:#5cb0f0; --cyan:#5ad7cd;
  --accent:#e8a33d;
  --shadow-1: 0 1px 2px rgba(0,0,0,.4);
}`

const dynamicCSS = `
[data-theme="vaporwave"] {
  --ground: #160d2b; --raised: #1c0f36; --raised-2: #241245;
  --ink: #f2e6ff; --dim: #9d84c4; --rule: #432c6b;
  --cyan: #56d0ff;
  --accent: #ff6ec7;
  --ground-wash: linear-gradient(180deg, #2a0f4d 0%, #160d2b 100%);
}
[data-theme="stardew"] {
  --ground: #f3e3c3; --raised: #fcf5e5; --raised-2: #e8d7b4;
  --ink: #4a3524; --dim: #8a7355; --rule: #cbb08a;
  --cyan: #56a89c; --accent: #3f8f4a;
}`

// The base file's :root is corvid -- dodo's own words, "corvid needs no
// block: it IS midnight, one palette in two spellings."
func TestParseThemesReadsDodosTokens(t *testing.T) {
	themes := parseThemes([]string{midnightCSS, dynamicCSS})
	if len(themes) < 4 {
		t.Fatalf("parsed %d themes", len(themes))
	}
	by := map[string]Theme{}
	for _, th := range themes {
		by[th.Name] = th
	}
	corvid, ok := by["corvid"]
	if !ok {
		t.Fatal("the base :root did not become corvid")
	}
	if string(corvid.Bg) != "#0b0e14" || string(corvid.Ink) != "#e6edf7" {
		t.Fatalf("corvid tokens wrong: %+v", corvid)
	}
	// the current spelling wins: --raised is the first step up, not --panel
	if string(corvid.Panel) != "#11161f" ||
		string(corvid.Raised) != "#151b26" {
		t.Fatalf(
			"surface levels wrong: panel=%s raised=%s",
			corvid.Panel,
			corvid.Raised,
		)
	}
	vw := by["vaporwave"]
	if string(vw.Accent) != "#ff6ec7" ||
		string(vw.AccentToken) != "#56d0ff" {
		t.Fatalf("vaporwave accents wrong: %+v", vw)
	}
	// an override inherits everything it does not restate
	if vw.Warn != corvid.Warn {
		t.Fatal("a themed override moved a semantic colour")
	}
	// and the palette puffin has always worn is still on the end
	if _, ok := by["puffin"]; !ok {
		t.Fatal("the puffin palette was dropped when dodo answered")
	}
}

// A gradient, a var() reference or a shadow is not a colour a terminal cell
// can be painted with. Taking one produces an invisible pane, not an error.
func TestOnlyFlatColoursAreTaken(t *testing.T) {
	bad := []string{
		"var(--raised)",
		"linear-gradient(180deg, #2a0f4d 0%)",
		"0 1px 2px rgba(0,0,0,.4)",
		"rgba(255,110,199,.09)",
		"#12345",
		"",
	}
	for _, bad := range bad {
		if isHex(bad) {
			t.Errorf("%q was taken as a colour", bad)
		}
	}
	for _, good := range []string{"#0b0e14", "#fff", " #F2E6FF "} {
		if !isHex(strings.TrimSpace(good)) {
			t.Errorf("%q was rejected", good)
		}
	}
	// --panel is `var(--raised)` in the real file, so the fallback chain
	// must skip it rather than paint a pane with the literal string
	themes := parseThemes([]string{midnightCSS, dynamicCSS})
	for _, th := range themes {
		for _, c := range []string{
			string(th.Bg),
			string(th.Panel),
			string(th.Raised),
			string(th.Ink),
		} {
			if !isHex(c) {
				t.Errorf(
					"%s carries an unpaintable token %q",
					th.Name,
					c,
				)
			}
		}
	}
}

// Light is measured, not listed: a theme dodo adds tomorrow gets the
// light-ground handling without puffin being taught its name.
func TestLightGroundIsMeasured(t *testing.T) {
	by := map[string]Theme{}
	for _, th := range parseThemes([]string{midnightCSS, dynamicCSS}) {
		by[th.Name] = th
	}
	if !by["stardew"].Light {
		t.Error("stardew's pale ground was not detected as light")
	}
	if by["vaporwave"].Light || by["corvid"].Light {
		t.Error("a dark theme was read as light")
	}
	// and the bird stays visible on the light one
	if by["stardew"].Snow == by["corvid"].Snow {
		t.Error("a white face survived onto a pale ground")
	}
}

// A pull that lands must not move the operator to a different skin: they
// keep wearing what they were wearing, by NAME.
func TestAPullKeepsWhatYouAreWearing(t *testing.T) {
	m := baseModel()
	m = m.setTheme(1) // vaporwave in the baked list
	worn := m.styles.Theme
	got := m.adoptThemes(themesFetched{
		themes: parseThemes(
			[]string{midnightCSS, dynamicCSS},
		), source: "dodo"})
	if got.styles.Theme != worn {
		t.Fatalf(
			"the pull moved the operator from %q to %q",
			worn,
			got.styles.Theme,
		)
	}
	if got.themeSrc != "dodo" {
		t.Fatalf("source: %q", got.themeSrc)
	}
	if string(got.themes[got.theme].Accent) != "#ff6ec7" {
		t.Fatal("the live tokens did not replace the baked ones")
	}
}

// PUFFIN_THEME wins when the pull finally brings the theme it named.
func TestAPullHonoursWhatWasAskedFor(t *testing.T) {
	m := newModel(
		"test",
		"stardew",
	) // not in the baked list under that index
	m = m.adoptThemes(themesFetched{
		themes: parseThemes(
			[]string{midnightCSS, dynamicCSS},
		), source: "dodo"})
	if m.styles.Theme != "stardew" {
		t.Fatalf(
			"wearing %q, not what PUFFIN_THEME asked for",
			m.styles.Theme,
		)
	}
	if m.themeNote != "" {
		t.Fatalf(
			"still complaining after the theme arrived: %q",
			m.themeNote,
		)
	}
}

// dodo unreachable is not a blank screen and not a silent stale palette: the
// baked themes stay on and the roster says so.
func TestUnreachableDodoIsSaidOutLoud(t *testing.T) {
	m := baseModel()
	m = m.adoptThemes(themesFetched{err: errNoDodo{}})
	if m.themeSrc != "baked" {
		t.Fatalf("source: %q", m.themeSrc)
	}
	if !strings.Contains(m.themeNote, "unreachable") {
		t.Fatalf("the fallback was silent: %q", m.themeNote)
	}
	m.width, m.height = 120, 40
	if !strings.Contains(rosterView(m), "baked") {
		t.Fatal("the roster does not say where its palette came from")
	}
}

type errNoDodo struct{}

// The stub fails the way a real unreachable service does, so the fallback
// is tested against the error it will actually see.
func (errNoDodo) Error() string { return "dial tcp: connection refused" }
