package main

import "testing"

// The splash bird wears the puffin's own colours, not the theme's.
//
// Asserted on the style's foreground rather than on rendered output:
// lipgloss strips colour when there is no TTY, so a test that renders and
// looks for escape codes finds none and passes while the bird is any colour
// at all. The first version of this test did exactly that.
func TestSplashBirdIgnoresTheTheme(t *testing.T) {
	want, ok := referenceTheme()
	if !ok {
		t.Fatal("the reference bird's own theme does not validate")
	}

	var first Styles
	for i, p := range []Theme{
		Corvid(),
		Vaporwave(),
		Stardew(),
		Bladerunner(),
	} {
		ink := splashInk(Compile(p))
		if got := ink.Plumage.GetForeground(); got != want.Dark {
			t.Errorf(
				"%s: cap painted %v, want the puffin's own %v",
				p.Name,
				got,
				want.Dark,
			)
		}
		if got := ink.Snow.GetForeground(); got != want.Light {
			t.Errorf(
				"%s: face painted %v, want %v",
				p.Name,
				got,
				want.Light,
			)
		}
		if got := ink.Beak.GetForeground(); got != want.BeakTip {
			t.Errorf(
				"%s: beak painted %v, want %v",
				p.Name,
				got,
				want.BeakTip,
			)
		}
		if i == 0 {
			first = ink
			continue
		}
		if ink.Plumage.GetForeground() !=
			first.Plumage.GetForeground() {
			t.Errorf(
				"%s: the bird changed colour with the theme",
				p.Name,
			)
		}
	}
}

// Everything around the bird still belongs to the theme. The splash is a
// portrait on a themed page, not a themed portrait -- if the title and help
// stopped following the theme too, that would be a different bug.
func TestSplashChromeStillFollowsTheTheme(t *testing.T) {
	a := splashInk(Compile(Corvid()))
	b := splashInk(Compile(Vaporwave()))
	if a.Header.GetForeground() == b.Header.GetForeground() {
		t.Error(
			"the splash header did not change with the theme; " +
				"only the bird should be fixed",
		)
	}
	if a.Help.GetForeground() == b.Help.GetForeground() {
		t.Error("the splash help did not change with the theme")
	}
}
