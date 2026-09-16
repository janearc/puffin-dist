package main

import (
	"strings"
	"testing"
	"time"
)

// fakeBird stands in for the sprite while it is being drawn elsewhere.
type fakeBird struct {
	drawn  int
	events []Event
	grow   bool // misbehave: return a taller frame than it was given
}

// The stub records what it was handed, so the overlay is tested on what
// reaches the sprite rather than on what the sprite draws.
func (b *fakeBird) Draw(frame string, w, h int) string {
	b.drawn++
	if b.grow {
		return frame + "\nan extra line nobody asked for"
	}
	return strings.Replace(frame, " ", "~", 1)
}

// A plain interval; these tests do not exercise the clock.
func (b *fakeBird) Tick() time.Duration { return time.Second }

// Events are kept rather than acted on, so a test can assert which ones
// reached the sprite.
func (b *fakeBird) Handle(e Event) { b.events = append(b.events, e) }

// An overlay reaches every screen from one place, and puffin renders
// correctly with none installed -- which is the property that lets the bird
// be developed in another repository without blocking this one.
func TestOverlayIsOptionalAndReachesEveryScreen(t *testing.T) {
	t.Cleanup(func() { SetOverlay(nil) })
	m := baseModel()
	m.width, m.height = 120, 40

	SetOverlay(nil)
	if got := drawOverlay("plain frame", 120, 40); got != "plain frame" {
		t.Fatalf("no overlay changed the frame: %q", got)
	}

	b := &fakeBird{}
	SetOverlay(b)
	t.Setenv("PUFFIN_BIRD", "1")
	if got := drawOverlay("plain frame", 120, 40); got == "plain frame" {
		t.Fatal("the overlay did not draw")
	}
	// and it is reachable from the screens themselves
	before := b.drawn
	_ = rosterView(m)
	if b.drawn == before {
		t.Fatal("the roster does not go through the overlay hook")
	}
}

// An overlay that changes the frame's shape is refused. The alternative is
// the interface jumping by a line with nothing to say which of the two
// things caused it.
func TestAnOverlayCannotResizeTheFrame(t *testing.T) {
	t.Cleanup(func() { SetOverlay(nil) })
	t.Setenv("PUFFIN_BIRD", "1")
	SetOverlay(&fakeBird{grow: true})
	frame := "one\ntwo\nthree"
	if got := drawOverlay(frame, 80, 24); got != frame {
		t.Fatalf(
			"a frame-resizing overlay was allowed through:\n%q",
			got,
		)
	}
}

// Off is a first-class answer: a bird animating while somebody reads a stack
// trace at three in the morning is a bird they kill the tool over.
func TestTheBirdCanBeTurnedOff(t *testing.T) {
	t.Cleanup(func() { SetOverlay(nil) })
	SetOverlay(&fakeBird{})
	for _, off := range []string{"0", "off"} {
		t.Setenv("PUFFIN_BIRD", off)
		if got := drawOverlay("frame", 80, 24); got != "frame" {
			t.Fatalf("PUFFIN_BIRD=%s still drew", off)
		}
	}
}

// Transitions reach every listener, and the notifier is one of them rather
// than the place events are sent. A producer that names its consumer can
// only ever have one.
func TestEventsReachEveryListener(t *testing.T) {
	t.Cleanup(
		func() {
			listeners.mu.Lock()
			listeners.fs = nil
			listeners.mu.Unlock()
		},
	)
	listeners.mu.Lock()
	listeners.fs = nil
	listeners.mu.Unlock()

	var a, b []Event
	Listen(func(e Event) { a = append(a, e) })
	Listen(func(e Event) { b = append(b, e) })
	Emit(Event{Kind: AgentWaiting, Subject: "puffin", Detail: "finished"})
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("listeners got %d and %d", len(a), len(b))
	}
	if a[0].At.IsZero() {
		t.Fatal("an event arrived with no time on it")
	}
	// an overlay is subscribed when it is installed
	bird := &fakeBird{}
	SetOverlay(bird)
	Emit(Event{Kind: WatchFired, Subject: "logs · boom"})
	if len(bird.events) != 1 || bird.events[0].Kind != WatchFired {
		t.Fatalf(
			"the overlay did not receive the event: %+v",
			bird.events,
		)
	}
}

// The bird inks a stencil from dodo's roles, which is more of the palette
// than a terminal paints with. Both must be wearing the same theme rather
// than each fetching the stylesheet and hoping.
func TestThemeCarriesTheWholePalette(t *testing.T) {
	themes := parseThemes([]string{midnightCSS, dynamicCSS})
	for _, th := range themes {
		if th.Name == "puffin" {
			// the baked one has no dodo tokens, by definition
			continue
		}
		if len(th.Tokens) == 0 {
			t.Fatalf("%s carries no raw tokens", th.Name)
		}
		// the roles a stencil needs are more than the ones puffin
		// paints
		for _, want := range []string{"--ground", "--ink", "--dim"} {
			if th.Tokens[want] == "" {
				t.Errorf("%s is missing %s", th.Name, want)
			}
		}
		if Compile(th).Tokens == nil {
			t.Errorf("%s: tokens did not survive Compile", th.Name)
		}
	}
}

// The bird, wired to the real sprite.
func TestBirdDrawsInTheCorner(t *testing.T) {
	b := newBird()
	frame := strings.Repeat("puffin · the enclave\n", 20)
	out := b.Draw(frame, 100, 30)
	if out == frame {
		t.Fatal("the bird drew nothing on a frame with room for it")
	}
	if countLines(out) != countLines(frame) {
		t.Fatalf("the bird changed the frame's shape: %d lines, was %d",
			countLines(out), countLines(frame))
	}
	// it lives in the terminal's unused width. The first cut measured the
	// widest content row and drew two columns left of that, so a frame
	// whose rows are all the same width had no room anywhere and nothing
	// appeared.
	if b.Draw(frame, 24, 30) != frame {
		t.Fatal("the bird drew into a terminal too narrow for it")
	}
	if b.Draw(frame, 100, 8) != frame {
		t.Fatal("the bird drew into a frame too short for it")
	}
}

// Every theme puffin can wear must produce a bird that reads, or none at
// all -- a theme whose contrasts have collapsed still renders, it just
// renders a blob, and a blob in the corner looks like a broken terminal.
func TestEveryThemeInksAReadableBird(t *testing.T) {
	b := newBird()
	for _, th := range Themes() {
		b.lastTheme = ""
		at := b.themeFor(th)
		if b.bad {
			t.Errorf(
				"%s produces an unreadable bird: %v",
				th.Name,
				at.Validate(),
			)
		}
	}
}

// The bird draws ON TOP of text, and gives back every cell it does not
// cover.
//
// This replaces a test that asserted the opposite -- a row reaching into the
// bird's columns used to be skipped, on the reasoning that losing a character
// of somebody's log line to a mascot is not a trade worth making.
//
// The reasoning was sound and the conclusion was wrong: it meant panes, whose
// rows are padded to the full width, had no bird on any row at all.
//
// Compositing is the version that costs nothing. Where the sprite has a
// glyph the text is covered, exactly as anything drawn on top of anything
// else covers it; where the sprite is transparent the original cell is
// written back with its styling intact.
func TestTheBirdDrawsOverText(t *testing.T) {
	b := newBird()
	long := strings.Repeat("x", 96)
	frame := strings.TrimRight(strings.Repeat(long+"\n", 20), "\n")
	out := b.Draw(frame, 100, 30)
	if out == frame {
		t.Fatal("the bird drew nothing over a full-width frame")
	}
	if countLines(out) != countLines(frame) {
		t.Fatalf(
			"the frame changed height: %d -> %d",
			countLines(frame),
			countLines(out),
		)
	}
	lines := strings.Split(out, "\n")
	// the untouched rows at the top are byte-identical: only the bird's
	// rectangle is ever reparsed
	for i := 0; i < 5; i++ {
		if lines[i] != long {
			t.Fatalf(
				"row %d was rewritten and should not have "+
					"been: %q",
				i,
				lines[i],
			)
		}
	}
	// and in the bird's rows, the left of the frame survives untouched
	painted := 0
	for _, l := range lines {
		if !strings.HasPrefix(l, strings.Repeat("x", 60)) {
			t.Fatalf("the bird ate content to its left: %q", l)
		}
		if l != long {
			painted++
		}
	}
	if painted == 0 {
		t.Fatal("no row carries the bird")
	}
}

// The cell parser is the half of compositing everything else rests on: an
// attribute it does not understand must survive it untouched, because puffin's
// log highlighting is full of them.
func TestCellsRoundTripStyling(t *testing.T) {
	for _, in := range []string{
		"plain",
		"\x1b[31mred\x1b[0m and plain",
		"\x1b[1m\x1b[4mbold underlined\x1b[0m",
		"\x1b[38;2;255;0;128mtruecolour\x1b[0m tail",
		"",
	} {
		got := renderCells(cellsOf(in))
		if stripSGR(got) != stripSGR(in) {
			t.Fatalf("text changed: %q -> %q", in, got)
		}
		if ansiRe.MatchString(in) && !ansiRe.MatchString(got) {
			t.Fatalf("styling was lost: %q -> %q", in, got)
		}
	}
}

// stripSGR leaves the text a reader would see, so an assertion about
// words is not an assertion about styling.
func stripSGR(s string) string { return ansiRe.ReplaceAllString(s, "") }
