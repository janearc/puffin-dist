package main

import (
	"strings"
	"testing"
)

// The companion is a cutout: it must not paint a background.
//
// Draw used to fill every one of the sprite's own cells with currentTheme().Bg,
// on the reasoning that the sprite sits on the page and should carry the page's
// colour.
//
// puffin does not paint the page that colour -- no style sets a page background
// -- so the value was the theme's idea of a page laid over whatever the
// terminal actually is.
//
// Under the dark themes those agree closely enough to look right. Stardew
// is dodo's light theme, Bg #f3e3c3, and in a dark terminal it drew a cream
// halo around every half-covered cell of the silhouette. It is dark
// against light, and nothing subtler than that.
func TestCompanionDrawsNoBackground(t *testing.T) {
	t.Setenv("PUFFIN_BIRD", "1")
	t.Setenv("PUFFIN_MASCOT", "lamp")

	frame := strings.TrimRight(
		strings.Repeat(strings.Repeat(" ", 60)+"\n", 24),
		"\n",
	)
	prev := currentTheme()
	t.Cleanup(func() { setLiveTheme(prev) })
	for _, p := range []Theme{Corvid(), Stardew(), Vaporwave()} {
		setLiveTheme(p)
		b := newBird()
		SetOverlay(b)
		th := b.themeFor(p)
		cols := b.sprite.ColsFor(8)
		for _, row := range companionCells(b.sprite, th, cols, 8, nil) {
			for _, cell := range row {
				// A sprite cell may carry a background of its
				// OWN: a quadrant that is half one of the
				// character's colours and half another sets
				// both.
				//
				// What it must never carry is the theme's page
				// colour, which is the halo -- puffin does not
				// paint the page, so that value is a guess
				// about a terminal it cannot see.
				if cell.BG != nil && cell.BG == p.Bg {
					t.Fatalf(
						"%s: a companion cell "+
							"carries the theme's "+
							"page colour %v; "+
							"in a dark terminal "+
							"a light "+
							"theme "+
							"haloes the "+
							"silhouette",
						p.Name,
						cell.BG,
					)
				}
			}
		}
		_ = b.Draw(
			frame,
			60,
			24,
		) // must not panic with no background set
	}
}

// The sprite's ink must not change with the theme either -- that is the
// guest's own palette, tested elsewhere -- but the two together are what
// make the corner look the same in a light theme and a dark one.
func TestGuestInkIsIdenticalAcrossLightAndDark(t *testing.T) {
	b := newBird()
	b.character = "lamp"
	dark := b.themeFor(Corvid())
	light := b.themeFor(Stardew())
	if dark.Dark != light.Dark || dark.BeakTip != light.BeakTip {
		t.Errorf(
			"lamp differs between a dark and a light theme: "+
				"%v/%v vs %v/%v",
			dark.Dark,
			dark.BeakTip,
			light.Dark,
			light.BeakTip,
		)
	}
}
