package main

import (
	"strings"
	"testing"
	"time"
)

// Word hooks: a watch says what the companion does about it.
//
// The companion idles in one setting and has a hook for characters or words.
// The pattern is the word; the emote is what happens. It costs a field because
// the pipeline was already there -- a watch matches arriving lines, emits an
// event, and the corner bird is already subscribed and already emotes on it.
//
// All that was missing was letting a watch say which, rather than every watch
// producing the same startle.

// withCompanion builds a sprite with one character standing in the
// corner, and puts the package globals back afterwards.
func withCompanion(t *testing.T, name string) *bird {
	t.Helper()
	t.Setenv("PUFFIN_MASCOT", name)
	b := newBird()
	SetOverlay(b)
	t.Cleanup(func() { SetOverlay(nil) })
	return b
}

// The hook is honoured over what the kind usually means.
func TestAWatchEmoteOverridesTheDefault(t *testing.T) {
	b := withCompanion(t, "gopher")
	// the default for a fired watch
	b.Handle(Event{Kind: WatchFired})
	startled, _ := b.sprite.Emote("startled")
	if b.playing.Length() != startled.Length() {
		t.Fatal("a fired watch did not startle by default")
	}
	// and the hook, which wins
	want, ok := b.sprite.Emote("laughing")
	if !ok {
		t.Skip("this character does not laugh")
	}
	b.Handle(Event{Kind: WatchFired, Emote: "laughing"})
	if b.playing.Length() != want.Length() {
		t.Fatal("the event asked for laughing and got something else")
	}
}

// A hook set while one character was on is kept verbatim when another takes
// over: the cue is dropped rather than approximated, which is quieter than
// substituting something nobody asked for.
func TestAnUnknownCueIsDroppedNotApproximated(t *testing.T) {
	b := withCompanion(t, "lamp")
	if e, ok := b.sprite.Emote("blink"); ok {
		b.playing, b.since = e, time.Now()
	}
	before := b.playing.Length()
	b.Handle(
		Event{Kind: WatchFired, Emote: "waka"},
	) // ms pacman's, not the lamp's
	if b.playing.Length() != before {
		t.Fatal("the lamp performed an emote it does not have")
	}
}

// The hook rides all the way from the watch to the corner, on every stream
// that carries watches.
func TestTheHookReachesTheCornerFromEveryPane(t *testing.T) {
	t.Cleanup(func() { restoreWatches(nil); enableNotify(false) })

	got := ""
	counting := true
	Listen(func(e Event) {
		if counting && e.Kind == WatchFired {
			got = e.Emote
		}
	})
	t.Cleanup(func() { counting = false })

	// logs
	restoreWatches(
		[]Watch{{Pattern: "boom", Where: "logs", Emote: "startled"}},
	)
	p := &logsPane{}
	p.v = LogView{Lines: logFixture()}
	p.check() // baseline
	p.v.Lines = append(p.v.Lines, LogLine{When: p.seen.Add(time.Second),
		Service: "postgres", Text: "boom"})
	p.check()
	if got != "startled" {
		t.Errorf("logs: the corner was told %q", got)
	}

	// bus
	got = ""
	restoreWatches(
		[]Watch{
			{Pattern: "kingfisher", Where: "bus", Emote: "curious"},
		},
	)
	bp := &busPane{seen: map[string]bool{}}
	bp.v.Messages = []BusMessage{{Schema: "observability.v1.Heartbeat",
		Fields: map[string]string{
			"id":           "1",
			"service_name": "kingfisher",
		},
		Order: []string{"id", "service_name"}}}
	bp.check()
	if got != "curious" {
		t.Errorf("bus: the corner was told %q", got)
	}
}

// Cycling walks the companion's own repertoire, and the empty string -- "do
// whatever the kind usually means" -- is in the ring rather than reachable
// only by deleting the watch.
func TestCyclingWalksTheRepertoireAndIncludesTheDefault(t *testing.T) {
	b := withCompanion(t, "gopher")
	t.Cleanup(func() { restoreWatches(nil) })
	restoreWatches([]Watch{{Pattern: "postgres", Where: "logs"}})

	names := b.sprite.Emotes()
	seen := map[string]bool{}
	for i := 0; i < len(names)+1; i++ {
		got, ok := cycleWatchEmote("postgres", "logs")
		if !ok {
			t.Fatal("nothing to cycle")
		}
		seen[got] = true
	}
	if !seen[""] {
		t.Error("the default is not reachable by cycling")
	}
	if len(seen) < 2 {
		t.Errorf("the cycle only ever produced %v", seen)
	}
	// a pattern nobody is watching is refused rather than inventing a watch
	if _, ok := cycleWatchEmote("nothing-is-watching-this", "logs"); ok {
		t.Error("cycling created a watch out of nowhere")
	}
}

// Choosing previews: choosing an emote you cannot see is choosing from a
// list of words.
func TestCyclingPreviewsTheChoice(t *testing.T) {
	b := withCompanion(t, "gopher")
	t.Cleanup(func() { restoreWatches(nil) })
	restoreWatches([]Watch{{Pattern: "postgres", Where: "logs"}})

	for i := 0; i < 4; i++ {
		name, _ := cycleWatchEmote("postgres", "logs")
		if name == "" {
			continue // the default cues nothing, correctly
		}
		if !b.performing() {
			t.Fatalf("cycling to %q showed nothing", name)
		}
		return
	}
	t.Error("four cycles produced no preview")
}

// A hook you cannot see is one you forget you set, so it is in the label the
// panes already draw.
func TestTheHookIsOnScreen(t *testing.T) {
	t.Cleanup(func() { restoreWatches(nil); enableNotify(false) })
	restoreWatches(
		[]Watch{
			{Pattern: "postgres", Where: "logs", Emote: "startled"},
		},
	)

	w := watchesFor("logs")[0]
	if got := w.Label(); got != "postgres → startled" {
		t.Fatalf("label %q", got)
	}
	p := logsWith(logFixture())
	if !strings.Contains(
		p.View(Compile(DodoDark()), 140, 44),
		"postgres → startled",
	) {
		t.Error("the hook is not on the screen that owns the watch")
	}
	// a watch with no hook reads exactly as it did before
	restoreWatches([]Watch{{Pattern: "postgres", Where: "logs"}})
	if got := watchesFor("logs")[0].Label(); got != "postgres" {
		t.Fatalf("an unhooked watch reads %q", got)
	}
}

// E cycles from the pane, off the search box -- the same idiom W uses.
func TestEKeyHooksTheCurrentSearch(t *testing.T) {
	withCompanion(t, "gopher")
	t.Cleanup(func() { restoreWatches(nil); enableNotify(false) })
	restoreWatches(nil)

	p := logsWith(logFixture())
	p.query = "off-contract"
	p.Update(key2('W'))
	if len(watchesFor("logs")) != 1 {
		t.Fatal("W did not set the watch")
	}
	p.Update(key2('E'))
	if got := watchesFor("logs")[0].Emote; got == "" {
		t.Fatal("E did not hook the watch")
	}
	// with nothing typed there is nothing to hook, and it is not an error
	p.query = ""
	p.Update(key2('E'))
}

// Watches persist with their hooks: a standing instruction that forgets what
// it meant on restart is worse than one that was never set.
func TestHooksSurviveARestart(t *testing.T) {
	t.Cleanup(func() { restoreWatches(nil) })
	restoreWatches(
		[]Watch{
			{Pattern: "postgres", Where: "logs", Emote: "startled"},
		},
	)
	saved := snapshotWatches()
	if len(saved) != 1 || saved[0].Emote != "startled" {
		t.Fatalf("snapshot: %+v", saved)
	}
	restoreWatches(nil)
	restoreWatches(saved)
	if got := watchesFor("logs")[0].Emote; got != "startled" {
		t.Fatalf("after a restart the hook is %q", got)
	}
}
