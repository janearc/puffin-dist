package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/janearc/puffin-auklet/auklet"
	"github.com/janearc/puffin-auklet/canvas"
)

// a glance is a pose, and it must differ from the resting bird -- the whole
// point of gaze is that you can see it happened.
func TestBirdGazeChangesTheFrame(t *testing.T) {
	rest := auklet.SideView.Gaze(0, 0)
	if len(rest) != 0 {
		t.Fatalf(
			"gaze(0,0) should be the resting pose, got %d overlays",
			len(rest),
		)
	}
	if len(auklet.SideView.Gaze(-1, -1)) == 0 {
		t.Fatal("gaze up-left produced no overlays")
	}
}

// an event starts a performance, and the performance ends on its own --
// there is no timer to fire and no state to get stuck in.
func TestBirdEmotesAndReturnsToRest(t *testing.T) {
	b := newBird()
	if b.performing() {
		t.Fatal("a fresh bird is already performing")
	}
	b.Handle(Event{Kind: WatchFired})
	if !b.performing() {
		t.Fatal("WatchFired started no emote")
	}
	if got := b.Tick(); got > 200*time.Millisecond {
		t.Fatalf(
			"mid-emote tick is %v, too slow to render the frames",
			got,
		)
	}
	b.since = time.Now().Add(-time.Hour) // long past the end
	if b.performing() {
		t.Fatal("the emote never ended")
	}
	if got := b.Tick(); got < time.Second {
		t.Fatalf("at rest the bird still wants %v", got)
	}
}

// the small bird blinks and does not look: a look changes two cells at eight
// rows, which is a flicker rather than a glance.
func TestSmallBirdKeepsBlinkAndDropsGaze(t *testing.T) {
	if rowsFor(24, 40) != birdRowsSmall || rowsFor(40, 40) != birdRowsEyes {
		t.Fatalf("rowsFor picked the wrong heights: %d, %d",
			rowsFor(24, 40), rowsFor(40, 40))
	}
	// the bug: a tall window with a short page. The bird used to ask for
	// the big body, not fit in eleven lines of roster, and draw nothing.
	if got := rowsFor(45, 11); got != birdRowsSmall {
		t.Fatalf(
			"a tall terminal with an eleven-line page picked %d "+
				"rows",
			got,
		)
	}
	pose := append(auklet.SideView.Blink, auklet.SideView.Gaze(-1, 0)...)
	kept := withoutGaze(pose)
	if len(kept) != len(auklet.SideView.Blink) {
		t.Fatalf(
			"withoutGaze kept %d overlays, want %d",
			len(kept),
			len(auklet.SideView.Blink),
		)
	}
	for _, o := range kept {
		if o.Name == "gaze" {
			t.Fatal("a gaze overlay survived")
		}
	}
}

// an unknown cue is dropped, not approximated.
func TestUnknownEmoteIsDropped(t *testing.T) {
	b := newBird()
	b.play(auklet.SideView, "moonwalk")
	if b.performing() {
		t.Fatal("puffin invented an emote it was never given")
	}
}

// the crop is the feature: the splash bird must be exactly splashRows tall,
// however tall the sprite it was cut from.
func TestSplashBirdIsCropped(t *testing.T) {
	out, ok := splashBird(currentTheme(), 120, 40, 0)
	if !ok {
		t.Fatal(
			"splash bird refused a roomy terminal with the " +
				"default theme",
		)
	}
	if n := len(strings.Split(out, "\n")); n != splashRows {
		t.Fatalf("splash bird is %d rows, want %d", n, splashRows)
	}
}

// a terminal too small gets the hand-drawn bird instead, not a smudge.
func TestSplashBirdRefusesSmallTerminals(t *testing.T) {
	if _, ok := splashBird(currentTheme(), 40, 20, 0); ok {
		t.Fatal("splash bird drew into a terminal with no room for it")
	}
}

// the opening beat: looking down at the letters, then up at the reader.
func TestSplashBirdLooksThenBlinks(t *testing.T) {
	early, _ := splashBird(currentTheme(), 120, 40, 0)
	late, _ := splashBird(currentTheme(), 120, 40, 12)
	if early == late {
		t.Fatal("the splash bird never changes pose")
	}
}

// A stranger's bird, from a text file, through puffin's own render path.
//
// This is the test the auklet's author cannot write from their side, and it
// is the one that says whether publishing the library actually gives anybody
// anything: somebody who does not write Go draws a grid, saves it, and it
// renders here with no code change. If this is awkward, the API is wrong.
func TestStrangersBirdDropsIn(t *testing.T) {
	f, err := os.Open("testdata/stranger.sprite")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	sp, err := auklet.ParseSprite(f)
	if err != nil {
		t.Fatalf("a hand-written sprite file did not parse: %v", err)
	}
	if sp.Name != "stranger" {
		t.Fatalf("sprite is named %q", sp.Name)
	}

	// puffin's own theme mapping, not the auklet's demo one
	th, ok := aukletTheme(currentTheme())
	if !ok {
		t.Fatal("the default theme failed validation")
	}

	rows := birdRowsEyes
	cols := sp.ColsFor(rows)
	if cols <= 0 {
		t.Fatalf(
			"a stranger's sprite reports %d columns at %d rows",
			cols,
			rows,
		)
	}
	c := canvas.New(cols, rows)
	c.Blit(sp.CellsAt(th, auklet.Quadrant, cols, rows, nil), 0, 0)
	out := c.String()
	if strings.TrimSpace(ansiRe.ReplaceAllString(out, "")) == "" {
		t.Fatal("a stranger's bird rendered nothing")
	}

	// finally, through the corner splice, which is where a wrong width
	// would show up as a staircase rather than an error
	frame := strings.TrimRight(
		strings.Repeat(strings.Repeat("-", 100)+"\n", 30),
		"\n",
	)
	spliced := spliceCorner(frame, out, cols, rows, 120)
	if countLines(spliced) != countLines(frame) {
		t.Fatalf(
			"splicing a stranger's bird changed the frame's "+
				"height: %d -> %d",
			countLines(frame),
			countLines(spliced),
		)
	}
}

// The stock vocabulary reaches a bird the auklet never drew.
//
// This replaces a test that asserted the opposite.
//
// buildEmotes used to run only in the auklet's package init for its own two
// sprites, so a bird loaded from a file declared a blink pose and got no blink
// emote -- everything puffin drives the corner with was unavailable to exactly
// the people the file format was added for.
//
// Reported, fixed upstream, and this is the assertion that it stays fixed.
func TestStrangersBirdGetsTheStockVocabulary(t *testing.T) {
	f, err := os.Open("testdata/stranger.sprite")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sp, err := auklet.ParseSprite(f)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"blink", "startled", "curious"} {
		if _, ok := sp.Emote(want); !ok {
			t.Fatalf(
				"a stranger's bird has no %q emote; it has %v",
				want,
				sp.Emotes(),
			)
		}
	}
}

// the bird does something unprompted, eventually, and not on a rhythm you
// can learn.
func TestBirdIdles(t *testing.T) {
	b := newBird()
	now := time.Now()
	b.idle(auklet.SideView, now) // arms the first wait
	if b.performing() {
		t.Fatal("the bird performed the instant it was drawn")
	}
	b.idle(auklet.SideView, now.Add(idleFloor/2))
	if b.performing() {
		t.Fatal("the bird did not wait out the floor")
	}
	b.idle(auklet.SideView, now.Add(idleFloor+time.Second))
	if !b.performing() {
		t.Fatal("the bird never did anything on its own")
	}
	// and an event still wins: idling must not swallow the reaction
	b.Handle(Event{Kind: WatchFired})
	if !b.performing() {
		t.Fatal("an event during an idle emote did nothing")
	}
}

// The storyboard, asserted as a sequence rather than as a picture.
//
// The opening is storyboarded on the MGM lion: profile, pan to the fourth wall,
// rawr, turn away and back, rawr again. Each beat is checked by what it must be
// TRUE of -- the bird faces left at the start, faces you by the time it roars,
// and the pan passes through every drawing rather than cutting.
func TestSplashPlaysTheStoryboard(t *testing.T) {
	views := auklet.Views()
	if len(views) < 4 {
		t.Skipf(
			"the pan needs four drawings; the auklet ships %d",
			len(views),
		)
	}
	frames := map[int]string{}
	for fr := 0; fr <= 36; fr++ {
		out, ok := splashBird(currentTheme(), 130, 44, fr)
		if !ok {
			t.Fatal("the splash bird refused a roomy terminal")
		}
		frames[fr] = out
	}
	// the pan: every step differs from the one before it. A step that
	// repeats is a drawing that did not survive resampling, and the pan
	// would read as a cut.
	for fr := 4; fr <= 6; fr++ {
		if frames[fr] == frames[fr-1] {
			t.Fatalf(
				"pan frame %d is identical to %d: the turn "+
					"reads as a cut",
				fr,
				fr-1,
			)
		}
	}
	// it starts in profile and arrives head-on, and those are different
	if frames[0] == frames[7] {
		t.Fatal("the bird ended the pan facing the way it started")
	}
	// both rawrs happen, and each is a change from the frame before
	for _, attack := range []int{12, 30} {
		if frames[attack] == frames[attack-1] {
			t.Fatalf(
				"the rawr at frame %d did not open the beak",
				attack,
			)
		}
	}
	// the glance away and the return
	if frames[22] == frames[21] {
		t.Fatal("the bird never turned away")
	}
	if frames[26] == frames[22] {
		t.Fatal("the bird never came back")
	}
}

// With nothing set, PUFFIN_MASCOT unset and nothing remembered, the corner
// stays the puffin it has always been -- an existing session's behaviour
// must not change under it.
func TestMascotDefaultsToAuklet(t *testing.T) {
	t.Setenv("PUFFIN_MASCOT", "")
	if got := mascotSprite(); got.Name != "side" {
		t.Fatalf(
			"with nothing set, mascot should default to auklet's "+
				"SideView, got %q",
			got.Name,
		)
	}
}

// PUFFIN_MASCOT is a statement about THIS run and outranks what was
// remembered -- the same rule TestEnvironmentOutranksTheRememberedTheme
// checks for the theme, applied to the mascot's own precedence.
func TestMascotEnvironmentOutranksRemembered(t *testing.T) {
	enableUIState(t)
	rememberMascot("gopher")
	t.Setenv("PUFFIN_MASCOT", "")
	if got := mascotSprite(); got.Name != "front" {
		t.Fatalf(
			"the remembered mascot was ignored: got sprite %q",
			got.Name,
		)
	}
	t.Setenv("PUFFIN_MASCOT", "auklet")
	if got := mascotSprite(); got.Name != "side" {
		t.Fatalf(
			"the environment lost to a remembered preference: "+
				"got sprite %q",
			got.Name,
		)
	}
}

// A cue for a character that does not exist is dropped rather than
// approximated -- play()'s own rule, applied one level up: an unrecognised
// PUFFIN_MASCOT falls back to auklet rather than refusing to draw at all.
func TestMascotUnknownNameFallsBackToAuklet(t *testing.T) {
	t.Setenv("PUFFIN_MASCOT", "nonexistent-bird")
	if got := mascotSprite(); got.Name != "side" {
		t.Fatalf(
			"an unknown mascot name should fall back to auklet, "+
				"got %q",
			got.Name,
		)
	}
}

// Every character other than auklet stands on its front view -- the one
// each was actually built and posed for, per mascotSprite's own reasoning.
func TestMascotUsesFrontViewForOtherCharacters(t *testing.T) {
	for _, name := range []string{"gopher", "lamp"} {
		t.Setenv("PUFFIN_MASCOT", name)
		if got := mascotSprite(); got.Name != "front" {
			t.Errorf(
				"%s: want the front view, got %q",
				name,
				got.Name,
			)
		}
	}
	t.Setenv("PUFFIN_MASCOT", "auklet")
	if got := mascotSprite(); got.Name != "side" {
		t.Fatalf(
			"auklet should still be the side view, got %q",
			got.Name,
		)
	}
}
