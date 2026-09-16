package main

import "testing"

// A guest keeps its own colours; the auklet follows puffin's theme.
//
// The guest characters were drawn with themes of their own, so that they
// render in their right colours whatever theme is in use. Without that they
// look wrong in every theme.
//
// They did. puffin mapped its own palette onto every character, so the
// gopher was drawn in whatever ink the current dodo theme happened to
// carry rather than in Go's blue.
func TestGuestsKeepTheirOwnColours(t *testing.T) {
	b := newBird()

	// the gopher is Go's own blue, whatever puffin is wearing
	b.character = "gopher"
	want, ok := nativeTheme("gopher")
	if !ok {
		t.Fatal("the gopher has no native theme; it shipped with one")
	}
	if got := b.themeFor(Corvid()); got.Dark != want.Dark {
		t.Errorf(
			"gopher drawn in %v, want its own %v",
			got.Dark,
			want.Dark,
		)
	}

	// and it does not change when puffin's theme does. Stardew is dodo's
	// light theme, so it is the strongest available contrast to Corvid --
	// if anything were going to repaint the gopher it would be this.
	first := b.themeFor(Corvid())
	second := b.themeFor(Stardew())
	if first.Dark != second.Dark {
		t.Errorf(
			"the gopher changed colour with puffin's theme: %v "+
				"then %v",
			first.Dark,
			second.Dark,
		)
	}
}

// The auklet is puffin's own bird and belongs to puffin's palette. A mascot
// that ignored the estate's skin would be opting out of the thing the theme
// system exists for.
func TestTheAukletStillFollowsPuffin(t *testing.T) {
	b := newBird()
	b.character = "auklet"
	dark := b.themeFor(Corvid())
	light := b.themeFor(Stardew())
	if dark.Dark == light.Dark && dark.Light == light.Light {
		t.Error(
			"the auklet did not change with puffin's theme; it " +
				"is supposed to",
		)
	}
}

// The cache is keyed on the character as well as the theme. It was keyed on
// the theme alone, so pressing C left the previous character's colours on
// the new one until the theme was also cycled -- a guest wearing the last
// guest's ink.
func TestSwitchingCharacterRepaintsImmediately(t *testing.T) {
	b := newBird()
	p := Corvid()

	b.character = "auklet"
	auk := b.themeFor(p)
	b.character = "gopher"
	gopher := b.themeFor(p)

	if auk.Dark == gopher.Dark {
		t.Error(
			"switching from the auklet to the gopher kept the " +
				"auklet's colours",
		)
	}
}

// The corner is a cutout: a theme carrying its own background paints an
// opaque rectangle, which is the sticker the Draw path exists to avoid.
func TestNativeThemesDropTheirBackground(t *testing.T) {
	for _, name := range []string{"gopher", "lamp", "lamp"} {
		got, ok := nativeTheme(name)
		if !ok {
			t.Errorf(
				"%s has no native theme; it shipped with one",
				name,
			)
			continue
		}
		if got.Background != nil {
			t.Errorf(
				"%s kept its background %v; the corner is a "+
					"cutout",
				name,
				got.Background,
			)
		}
	}
	if _, ok := nativeTheme("auklet"); ok {
		t.Error(
			"the auklet claimed a native theme; it follows " +
				"puffin's",
		)
	}
	if _, ok := nativeTheme("nobody"); ok {
		t.Error("an unknown character claimed a native theme")
	}
}
