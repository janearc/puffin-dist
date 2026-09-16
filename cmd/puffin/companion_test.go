package main

import (
	"strings"
	"testing"
	"time"

	"github.com/janearc/puffin-auklet/auklet"
)

// Choosing the companion. Everything needed for this existed -- the library
// ships several characters, mascotSprite resolved a name, rememberMascot
// persisted one -- except a way to say which. Nothing in puffin ever called
// rememberMascot, so the only lever was PUFFIN_MASCOT at launch.

// The roster comes from the library, not from a list here: registering
// another character is a line in that package and puffin should see it
// without being edited. The exception is notOurs, which is art this binary
// does not publish, and those are subtracted rather than listed.
func TestCompanionRosterComesFromAuklet(t *testing.T) {
	names := mascotNames()
	if len(names) != len(auklet.Characters())-len(notOurs) {
		t.Fatalf(
			"puffin knows %d characters, the library ships %d "+
				"and %d are not ours",
			len(names),
			len(auklet.Characters()),
			len(notOurs),
		)
	}
	for _, n := range names {
		if notOurs[n] {
			t.Errorf(
				"%s is not ours to publish and is in the "+
					"roster",
				n,
			)
		}
	}
	for _, want := range []string{"auklet", "gopher", "lamp"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s is not in the roster: %v", want, names)
		}
	}
	// and every one of them resolves to a sprite that can actually be drawn
	for _, n := range names {
		s, ok := spriteFor(n)
		if !ok {
			t.Errorf("%s is in the roster but does not resolve", n)
		}
		if s.Name == "" {
			t.Errorf("%s resolved to a nameless sprite", n)
		}
	}
}

// An unrecognised name is refused rather than silently standing the auklet
// in for it -- a setting that does nothing looks exactly like a setting that
// was ignored.
func TestUnknownCompanionIsRefused(t *testing.T) {
	if _, ok := spriteFor("wombat"); ok {
		t.Error("an unknown name resolved")
	}
	// but it still draws something rather than nothing: off is a choice, a
	// blank corner is a bug
	s, _ := spriteFor("wombat")
	if s.Name == "" {
		t.Error("a refused name left nothing to draw")
	}
}

// The cycle walks the roster and wraps.
func TestCompanionCycleWraps(t *testing.T) {
	names := mascotNames()
	at := names[0]
	seen := map[string]bool{at: true}
	for range names {
		at = nextMascot(at)
		seen[at] = true
	}
	if len(seen) != len(names) {
		t.Fatalf(
			"the cycle visited %d of %d: %v",
			len(seen),
			len(names),
			seen,
		)
	}
	if at != names[0] {
		t.Fatalf("the cycle ended on %q, not back at %q", at, names[0])
	}
	// a name that is not in the roster starts the walk rather than sticking
	if got := nextMascot("wombat"); got != names[0] {
		t.Errorf("from an unknown name: %q", got)
	}
}

// Choosing swaps the sprite on the LIVE overlay rather than building a
// second bird: SetOverlay subscribes the overlay to events and there is no
// unsubscribe, so a bird per keypress would leave the old ones listening and
// animating where nobody can see them.
func TestChoosingSwapsTheLiveBird(t *testing.T) {
	t.Setenv("PUFFIN_MASCOT", "")
	b := newBird()
	SetOverlay(b)
	t.Cleanup(func() { SetOverlay(nil) })
	before := b.sprite.Name

	name, ok := chooseMascot("gopher")
	if !ok || name != "gopher" {
		t.Fatalf("choose gopher: %q %v", name, ok)
	}
	if b.sprite.Name == before && before != "front" {
		t.Errorf(
			"the corner bird is still standing as %q",
			b.sprite.Name,
		)
	}
	// and a refused name changes nothing
	if _, ok := chooseMascot("wombat"); ok {
		t.Error("an unknown companion was accepted")
	}
}

// Swapping drops whatever was being performed: an emote is a sequence of
// poses belonging to the sprite that started it, and finishing one on a
// different body is how you get a gopher wearing a puffin's blink.
func TestSwappingDropsThePerformance(t *testing.T) {
	b := newBird()
	if e, ok := b.sprite.Emote("blink"); ok {
		b.playing, b.since = e, time.Now()
	}
	g, _ := spriteFor("gopher")
	b.setSprite(g)
	if b.playing.Length() != 0 || !b.since.IsZero() {
		t.Fatal("an emote survived the character change")
	}
}

// The guest seat shows your companion -- except when your companion is the
// auklet, because the auklet is already standing full-screen in the middle
// of the splash and a seabird's terminal does not host itself.
func TestTheGuestIsTheCompanionExceptAtHome(t *testing.T) {
	t.Setenv("PUFFIN_MASCOT", "auklet")
	if got := guestName(); got != "gopher" {
		t.Errorf(
			"with the auklet in the corner the guest is %q, want "+
				"gopher",
			got,
		)
	}
	for _, name := range []string{"gopher", "lamp", "lamp"} {
		t.Setenv("PUFFIN_MASCOT", name)
		if got := guestName(); got != name {
			t.Errorf("companion %q seats %q", name, got)
		}
	}
}

// The guest walks in, then loops. A guest that performs is one you watch
// instead of reading the screen; a guest frozen in one pose is a sticker.
func TestTheGuestWalksInThenIdlesForever(t *testing.T) {
	sprite, ok := spriteFor("gopher")
	if !ok {
		t.Fatal("no gopher")
	}
	walk, ok := sprite.Emote("walk-in")
	if !ok {
		t.Skip("this gopher has no walk-in")
	}
	// during the entrance it is performing
	if pose := guestPose(sprite, walk.Length()/2); len(pose) == 0 {
		t.Error("the guest is not doing anything while walking in")
	}
	// long after it, it is still doing something -- and not the same thing
	// at every moment, which is what "idle loop" has to mean
	var poses int
	seen := map[int]bool{}
	for i := 0; i < 200; i++ {
		at := walk.Length() + time.Duration(i)*250*time.Millisecond
		p := guestPose(sprite, at)
		if len(p) > 0 {
			poses++
		}
		seen[len(p)] = true
	}
	if poses == 0 {
		t.Fatal("the guest froze after walking in")
	}
	if len(seen) < 2 {
		t.Error(
			"the idle loop is one pose held forever, which is a " +
				"sticker",
		)
	}
	// and it loops: the same offset a full cycle later is the same pose
	base := walk.Length() + 3*time.Second
	var cycle time.Duration
	for _, n := range awkwardIdles {
		if e, ok := sprite.Emote(n); ok {
			cycle += e.Length() + 900*time.Millisecond
		}
	}
	if len(guestPose(sprite, base)) != len(guestPose(sprite, base+cycle)) {
		t.Error("the idle ring does not come back round")
	}
}

// A sprite with none of the awkward emotes rests rather than panicking.
func TestGuestPoseSurvivesASpriteWithNoIdles(t *testing.T) {
	lamp, ok := spriteFor("lamp")
	if !ok {
		t.Skip("no lamp")
	}
	// whatever it has or does not have, this must not panic and must return
	// a drawable answer at any moment
	for _, at := range []time.Duration{
		0,
		time.Second,
		time.Minute,
		time.Hour,
	} {
		guestPose(lamp, at)
	}
}

// C cycles the companion from any screen, the way t cycles the theme, and
// says who is standing there -- the corner is eight rows and two of the four
// are the same silhouette at that size.
func TestCKeyChoosesTheCompanion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PUFFIN_MASCOT", "")
	SetOverlay(newBird())
	t.Cleanup(func() { SetOverlay(nil) })

	m := baseModel()
	next, _ := m.Update(key('C'))
	m = next.(model)
	if m.companionNote == "" {
		t.Fatal("C said nothing about what it did")
	}
	if !strings.Contains(m.View(), "standing in the corner") {
		t.Error("the choice is not on screen")
	}

	// and from inside a pane, because the corner is on every screen
	p := baseModel()
	next, _ = p.Update(key('l'))
	p = next.(model)
	next, _ = p.Update(key('C'))
	if next.(model).companionNote == "" {
		t.Error("C did nothing inside a pane")
	}
}

// C must not be swallowed by a pane that is capturing input: a companion
// change mid-sentence would be a keystroke stolen from the message.
func TestCDoesNotFireWhileTyping(t *testing.T) {
	m := baseModel()
	next, _ := m.Update(key('l'))
	m = next.(model)
	next, _ = m.Update(key('/')) // the logs pane's search box
	m = next.(model)
	next, _ = m.Update(key('C'))
	m = next.(model)
	if m.companionNote != "" {
		t.Fatal(
			"C changed the companion while a search was being " +
				"typed",
		)
	}
	lp, ok := m.openPane.(*logsPane)
	if !ok {
		t.Fatalf("pane is %T", m.openPane)
	}
	if lp.query != "C" {
		t.Fatalf("the keystroke did not reach the box: %q", lp.query)
	}
}

// The splash seats the guest and it moves: the same frame twice is the same
// drawing, two different frames are not.
func TestTheSplashGuestIsAnimated(t *testing.T) {
	t.Setenv("PUFFIN_MASCOT", "gopher")
	a, ok := splashGuest(currentTheme(), 140, 45, 4)
	if !ok {
		t.Skip("no room or no theme for an animated guest")
	}
	again, _ := splashGuest(currentTheme(), 140, 45, 4)
	if a != again {
		t.Error("the same frame drew two different guests")
	}
	// somewhere in the first two hundred frames it must look different --
	// otherwise it is a still image on a clock
	moved := false
	for f := 0; f < 200; f++ {
		if g, _ := splashGuest(currentTheme(), 140, 45, f); g != a {
			moved = true
			break
		}
	}
	if !moved {
		t.Error("the guest never moved")
	}
	// and a terminal with no room for guests gets none rather than a
	// squeezed one
	if _, ok := splashGuest(currentTheme(), 60, 20, 4); ok {
		t.Error("a small terminal was given a guest anyway")
	}
}

// The environment still wins for the run, the same precedence PUFFIN_THEME
// has: a variable is a deliberate statement about THIS launch.
func TestEnvironmentOutranksTheRememberedCompanion(t *testing.T) {
	t.Setenv("PUFFIN_MASCOT", "lamp")
	if got := currentMascot(); got != "lamp" {
		t.Fatalf("companion %q", got)
	}
	t.Setenv("PUFFIN_MASCOT", "")
	if got := currentMascot(); got == "" {
		t.Fatal("with nothing set the companion has no name at all")
	}
}

// Making one act on purpose.
//
// Without this there is no way to: the corner performs on mesh events and
// otherwise idles once every forty to a hundred and twenty seconds, which is
// right for something in your peripheral vision and useless for "show me what
// this one does"
//
// -- and choosing between four characters you cannot ask to move is choosing
// blind.
func TestPokeWalksTheRepertoire(t *testing.T) {
	t.Setenv("PUFFIN_MASCOT", "gopher")
	b := newBird()
	SetOverlay(b)
	t.Cleanup(func() { SetOverlay(nil) })

	want := b.sprite.Emotes()
	if len(want) < 2 {
		t.Fatalf("the gopher has %d emotes", len(want))
	}
	seen := map[string]bool{}
	for range want {
		name, ok := pokeCompanion()
		if !ok {
			t.Fatal("the poke did nothing")
		}
		seen[name] = true
		if !b.performing() {
			t.Errorf(
				"%s was cued but the bird is not performing",
				name,
			)
		}
	}
	if len(seen) != len(want) {
		t.Errorf(
			"the walk visited %d of %d emotes",
			len(seen),
			len(want),
		)
	}
	// and it comes back round rather than running out
	if _, ok := pokeCompanion(); !ok {
		t.Error("the repertoire ran out instead of wrapping")
	}
}

// A poke cuts straight past whatever was running: the key IS the
// instruction, and waiting politely for an idle blink to finish reads as the
// key having done nothing.
func TestPokeInterruptsWhateverWasPlaying(t *testing.T) {
	b := newBird()
	SetOverlay(b)
	t.Cleanup(func() { SetOverlay(nil) })
	if e, ok := b.sprite.Emote("sleepy"); ok {
		b.playing, b.since = e, time.Now()
	}
	name, ok := pokeCompanion()
	if !ok {
		t.Fatal("the poke did nothing")
	}
	if got, _ := b.sprite.Emote(name); got.Length() != b.playing.Length() {
		t.Error("the poke queued behind what was already running")
	}
}

// Changing character starts the walk again: the repertoire belongs to the
// character, and resuming at somebody else's index skips the front of it.
func TestPokeRestartsOnANewCharacter(t *testing.T) {
	t.Setenv("PUFFIN_MASCOT", "")
	b := newBird()
	SetOverlay(b)
	t.Cleanup(func() { SetOverlay(nil) })
	pokeCompanion()
	pokeCompanion()
	if b.pokeNext == 0 {
		t.Fatal("the walk did not advance")
	}
	g, _ := spriteFor("gopher")
	b.setSprite(g)
	if b.pokeNext != 0 {
		t.Fatalf("the new character resumed at %d", b.pokeNext)
	}
}

// With nobody in the corner the key says which kind of nothing it is, rather
// than looking broken.
func TestPokeWithNoCompanionSaysSo(t *testing.T) {
	SetOverlay(nil)
	if _, ok := pokeCompanion(); ok {
		t.Fatal("an empty corner performed")
	}
	m := baseModel().pokeCompanion()
	if !strings.Contains(m.companionNote, "nobody in the corner") {
		t.Errorf("note %q", m.companionNote)
	}
}

// e cues from any screen, and names what it cued: the difference between
// shifty and look-away is two cells at eight rows, and the name is how you
// learn which is which.
func TestEKeyCuesAndNames(t *testing.T) {
	t.Setenv("PUFFIN_MASCOT", "gopher")
	SetOverlay(newBird())
	t.Cleanup(func() { SetOverlay(nil) })

	m := baseModel()
	next, _ := m.Update(key('e'))
	m = next.(model)
	if !strings.Contains(m.companionNote, "gopher: ") {
		t.Fatalf("note %q", m.companionNote)
	}
	if !strings.Contains(m.View(), "gopher: ") {
		t.Error("what was cued is not on screen")
	}
	// and from inside a pane
	p := baseModel()
	next, _ = p.Update(key('l'))
	next, _ = next.(model).Update(key('e'))
	if next.(model).companionNote == "" {
		t.Error("e did nothing inside a pane")
	}
	// but not while a pane is capturing: a cue mid-sentence is a keystroke
	// stolen from the message
	c := baseModel()
	next, _ = c.Update(key('l'))
	next, _ = next.(model).Update(key('/'))
	next, _ = next.(model).Update(key('e'))
	c = next.(model)
	if c.companionNote != "" {
		t.Fatal("e fired while a search was being typed")
	}
	if lp, ok := c.openPane.(*logsPane); !ok || lp.query != "e" {
		t.Error("the keystroke did not reach the search box")
	}
}

// The companion keys belong on the screen you would go looking on.
//
// They shipped documented only in the pane help lines, which is the one
// place nobody looks for a setting -- so the feature existed, worked, and
// persisted to ui.json, and could not be found -- even by somebody whose
// own state file already had a mascot set in it.
func TestTheRosterAdvertisesTheCompanionKeys(t *testing.T) {
	t.Setenv("PUFFIN_MASCOT", "gopher")
	m := baseModel()
	m.scr = screenRoster
	out := m.View()
	if !strings.Contains(out, "C: gopher") {
		t.Error(
			"the roster does not say how to change the " +
				"companion, or who it is",
		)
	}
	if !strings.Contains(out, "e: poke") {
		t.Error("the roster does not say how to make it act")
	}
	// and it tracks the choice rather than naming one forever
	t.Setenv("PUFFIN_MASCOT", "lamp")
	if out := m.View(); !strings.Contains(out, "C: lamp") {
		t.Error(
			"the roster names a companion that is not the one " +
				"standing there",
		)
	}
}

// The guest is drawn whole, at the height it is shown.
//
// The first cut copied the splash bird's idiom -- render at twice the frame's
// height and let the canvas crop it -- which is a deliberate portrait trick for
// the auklet, whose head sits in the top third of its art.
//
// Applied to a character drawn full-body it shows the top half at double scale,
// tiled and unreadable: what arrives on screen is not the character asked for,
// it is a distorted slice of one.
func TestTheGuestIsDrawnWholeNotCropped(t *testing.T) {
	for _, name := range []string{"gopher", "lamp", "lamp"} {
		t.Setenv("PUFFIN_MASCOT", name)
		sprite, ok := spriteFor(guestName())
		if !ok {
			t.Fatalf("%s does not resolve", name)
		}
		g, ok := splashGuest(currentTheme(), 150, 46, 40)
		if !ok {
			t.Skip("no room or no theme")
		}
		lines := strings.Split(strings.TrimRight(g, "\n"), "\n")
		if len(lines) != guestRows {
			t.Errorf(
				"%s: drew %d rows, want %d",
				name,
				len(lines),
				guestRows,
			)
		}
		// the width has to be the one that keeps the character's own
		// proportions at the height shown -- asking for twice the
		// height is what produced a double-scale crop
		want := sprite.ColsFor(guestRows)
		if got := visibleWidthOf(lines[0]); got != want {
			t.Errorf("%s: %d columns wide, want %d (ColsFor(%d))",
				name, got, want, guestRows)
		}
		if wrong := sprite.ColsFor(guestRows * 2); want == wrong {
			continue // degenerate sprite; nothing to distinguish
		}
	}
}
