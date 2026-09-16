package main

import (
	"os"
	"strings"
	"time"

	"github.com/janearc/puffin-auklet/auklet"
	"github.com/janearc/puffin-auklet/canvas"
	"github.com/janearc/puffin-auklet/themes"
)

// The bird, wired in.
//
// This is the only file that knows both names, which is what SEAMS.md
// promised. Everything puffin-side is the Overlay interface; everything
// auklet-side is a sprite, a theme and a canvas.
//
// It sits in the bottom-right corner, blinks when something happens, and
// otherwise does nothing at all.
//
// That is deliberate and it is most of the design: a bird that fidgets in your
// peripheral vision while you read a stack trace is one you turn off within a
// week, and then the tool has an off feature instead of a mascot.

// How tall the corner bird stands, and it is two numbers because the eye
// detail does not survive the smaller one.
//
// These are measured, not chosen. The auklet's author counted cells: at 8 rows,
// looking left and looking right differ by two cells, which is a flicker rather
// than a look; at 11 it is three; at 14 it is six. A blink at 8 rows changes
// three cells and still reads.
//
// So the small bird blinks and does nothing else, and anything eye-detailed
// waits for the room to do it in.
const (
	birdRowsSmall = 8
	birdRowsEyes  = 11
)

// birdGutter is how many columns a screen should leave free on the right so
// the bird has somewhere to stand.
//
// The corner bird works on the roster because puffin renders to its content and
// the terminal is usually wider -- the bird lives in that gap.
//
// A pane has no gap: it pads its rows to the full width, and spliceCorner
// leaves any row that already reaches the bird's column alone, so the bird was
// skipped on every row and panes had no bird at all.
//
// It is ZERO, and kept as a function on purpose: reserving a strip was the
// first fix and it was the wrong one.
//
// The bird composites onto the text now (see compositeCorner), so nothing has
// to be moved out of its way, and a pane rendered narrower to make room for a
// mascot was a bad trade even while it worked. The seam stays in case a screen
// ever genuinely cannot be drawn over.
func birdGutter(width, height int) int { return 0 }

// rowsFor picks the height from the room available, and the room is the
// smaller of the terminal and what puffin actually drew into it.
//
// Passing only the terminal height was a bug with a silent failure: the bird
// chose the eleven-row body because the window was tall, then spliceCorner
// found ten lines of roster to sit in, refused, and drew nothing at all.
//
// A sparse front page on a big screen had no bird, which is exactly backwards
// -- that is the page with the most room to spare.
func rowsFor(height, contentLines int) int {
	room := height
	if contentLines > 0 && contentLines < room {
		room = contentLines
	}
	if room >= 30 {
		return birdRowsEyes
	}
	return birdRowsSmall
}

// idleFloor and idleSpread bound how long the bird waits before doing
// something unprompted.
//
// The original design said a mascot that fidgets in your peripheral vision
// while you read a stack trace is one you kill the tool over, and that is still
// true -- so this is deliberately long and deliberately random.
//
// A fixed interval is worse than none: the eye learns a rhythm in about three
// cycles and then anticipates it, which is precisely the distraction the long
// wait was buying off.
const (
	idleFloor  = 40 * time.Second
	idleSpread = 80 * time.Second
)

// idleEmotes are what the bird does when nothing is happening. Nothing
// startled, nothing loud: it looks around, it settles, it blinks.
var idleEmotes = []string{"blink", "look-away", "shifty", "curious", "blink"}

// mascotSprite resolves which character stands in the corner, and which of its
// views: auklet keeps SideView, the profile this whole corner was measured and
// tuned around (birdRowsSmall/birdRowsEyes, the gaze-stripping at eight rows).
//
// Every other character stands on its front view -- the one each was actually
// built and posed for; none of them has SideView's history of being sized for a
// small silhouette.
//
// PUFFIN_MASCOT wins over what was remembered, the same precedence PUFFIN_THEME
// has: an environment variable is a deliberate statement about THIS run.
//
// An unrecognised name falls back to auklet rather than refusing to draw -- the
// same "off is a first-class answer" instinct the bird toggle already has,
// applied to a name instead of a boolean.
func mascotSprite() auklet.Sprite {
	name := os.Getenv("PUFFIN_MASCOT")
	if name == "" {
		name = rememberedMascot()
	}
	sprite, _ := spriteFor(name)
	return sprite
}

// spriteFor resolves one name to the view that character stands on, and says
// whether the name was recognised. The bool is what lets a chooser refuse a
// name rather than quietly standing the auklet in for it.
func spriteFor(name string) (auklet.Sprite, bool) {
	if name == "" || name == "auklet" {
		return auklet.SideView, name != ""
	}
	if notOurs[name] {
		return auklet.SideView, false
	}
	for _, cs := range auklet.Characters() {
		if cs.Name != name {
			continue
		}
		for _, s := range cs.Views {
			if s.Name == "front" {
				return s, true
			}
		}
		// a character with no front view stands on whatever it has
		// rather than being silently replaced by the auklet -- that
		// replacement reads as "the setting did nothing", which is the
		// bug report
		if len(cs.Views) > 0 {
			return cs.Views[0], true
		}
	}
	return auklet.SideView, false
}

// notOurs are characters the library ships that puffin does not publish. The
// art belongs to somebody else and is not licensable on terms this release can
// meet; the library may carry it, this binary does not offer it.
//
// A name here is refused by spriteFor as well, so setting it by hand gets the
// same answer as looking for it in the chooser.
var notOurs = map[string]bool{"mspacman": true}

// mascotNames is the roster, in the order the chooser walks it. It comes
// from auklet rather than from a list here: registering a fifth character
// is a line in that package, and puffin should not need editing to see it.
func mascotNames() []string {
	cs := auklet.Characters()
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		if notOurs[c.Name] {
			continue
		}
		out = append(out, c.Name)
	}
	return out
}

// currentMascot is who is standing in the corner right now.
func currentMascot() string {
	if name := os.Getenv("PUFFIN_MASCOT"); name != "" {
		return name
	}
	if name := rememberedMascot(); name != "" {
		return name
	}
	return "auklet"
}

// standingMascot is who is actually in the corner right now, which is not
// the same question as currentMascot().
//
// currentMascot reads PUFFIN_MASCOT first, and that value never changes for the
// life of the process.
//
// So cycling from it went guest -> auklet -> guest -> auklet forever: the first
// C worked and every one after it asked the same question and got the same
// answer: only one C worked, and after that you were stuck.
//
// The bird knows who it is. Ask the bird. currentMascot stays the answer
// for "who should be standing there at startup", which is a different
// question and still the right one for the environment to answer.
func standingMascot() string {
	if b, ok := theOverlay.(*bird); ok && b != nil && b.character != "" {
		return b.character
	}
	return currentMascot()
}

// nextMascot is the one after the current one, wrapping.
func nextMascot(current string) string {
	names := mascotNames()
	if len(names) == 0 {
		return current
	}
	for i, n := range names {
		if n == current {
			return names[(i+1)%len(names)]
		}
	}
	return names[0]
}

// pokeCompanion cues the next thing in the character's own repertoire, and
// says what it was.
//
// Without this there is no way to make one act on purpose.
//
// The corner bird performs on mesh events -- an agent waiting, a watch firing,
// puffin having done something -- and otherwise idles once every forty to a
// hundred and twenty seconds, which is the right cadence for something living
// in your peripheral vision and the wrong one for "show me what this one does".
//
// Choosing between four characters you cannot ask to move is choosing blind.
//
// It walks the sprite's OWN emote list rather than a list here, so a character
// with a walk-in and a spin offers those and a character without them does not.
//
// Naming each one is most of the point: the difference between shifty and
// look-away is two cells at eight rows, and the name is how you learn which is
// which.
func pokeCompanion() (string, bool) {
	b, ok := theOverlay.(*bird)
	if !ok || b == nil {
		return "", false
	}
	names := b.sprite.Emotes()
	if len(names) == 0 {
		return "", false
	}
	name := names[b.pokeNext%len(names)]
	b.pokeNext++
	// straight past whatever it was doing: the poke IS the instruction, and
	// waiting politely for an idle blink to finish reads as the key having
	// done nothing.
	b.playing, b.since = auklet.Emote{}, time.Time{}
	b.play(b.sprite, name)
	return name, true
}

// companionEmotes is what the character in the corner can actually be asked
// to do. Empty when there is nobody there, which is a real answer.
func companionEmotes() []string {
	b, ok := theOverlay.(*bird)
	if !ok || b == nil {
		return nil
	}
	return b.sprite.Emotes()
}

// previewEmote cues one by name, for the times something is being chosen
// rather than having happened. A no-op when the corner is empty.
func previewEmote(name string) {
	b, ok := theOverlay.(*bird)
	if !ok || b == nil || name == "" {
		return
	}
	b.playing, b.since = auklet.Emote{}, time.Time{}
	b.play(b.sprite, name)
}

// chooseMascot changes who stands in the corner, now and next time.
//
// The bird resolves its sprite once at construction on purpose -- a mascot that
// drifts mid-session is a bug -- but a keystroke is not drift, it is the
// operator saying so.
//
// The sprite is swapped on the LIVE overlay rather than by building a second
// bird: SetOverlay subscribes the overlay to events and there is no
// unsubscribe, so a new bird per keypress would leave the old ones listening
// and animating where nobody can see them.
func chooseMascot(name string) (string, bool) {
	sprite, ok := spriteFor(name)
	if !ok {
		return currentMascot(), false
	}
	rememberMascot(name)
	if b, isBird := theOverlay.(*bird); isBird {
		b.character = name
		b.setSprite(sprite)
	}
	return name, true
}

type bird struct {
	// which character/view this corner bird is, resolved once at
	// construction -- PUFFIN_MASCOT is a statement about the run, not
	// something that should drift mid-session.
	sprite auklet.Sprite

	// character is which one is standing there, by name. The sprite is a
	// VIEW ("front", "side") and does not carry it, and the theme lookup
	// needs the character rather than the view.
	character     string
	lastCharacter string

	// the performance in progress, if any: an emote plus when it started.
	// Nothing else is state -- Emote.Pose is a pure function of elapsed
	// time, and past the end it returns nothing, so the bird returns to
	// rest by arithmetic rather than by a timer that has to fire.
	playing auklet.Emote
	since   time.Time

	// when to do something unprompted next, and where in the list to take
	// it from. A counter rather than a random pick: three blinks in a row
	// look broken, and an ordered walk cannot produce them.
	idleAt   time.Time
	idleNext int

	// where in the character's repertoire the poke key has got to. A walk
	// rather than a random pick, for the same reason the idles walk: a
	// random one repeats itself and looks broken.
	pokeNext int

	lastTheme string
	theme     auklet.Theme
	// the theme failed validation; draw nothing rather than a blob
	bad bool
}

// newBird makes one.
func newBird() *bird {
	return &bird{sprite: mascotSprite(), character: currentMascot()}
}

// setSprite stands a different character in the corner. Whatever was being
// performed is dropped: an emote is a sequence of poses belonging to the
// sprite that started it, and finishing one on a different body is how you
// get a gopher wearing a puffin's blink.
func (b *bird) setSprite(s auklet.Sprite) {
	b.sprite = s
	b.playing = auklet.Emote{}
	b.since = time.Time{}
	b.idleNext = 0
	// the repertoire belongs to the character, so the walk through it
	// starts again rather than resuming at somebody else's index
	b.pokeNext = 0
}

// Tick is slow at rest and fast mid-performance, and it is asked every time
// so the fast rate costs only the seconds it is used.
//
// The stock emotes hold frames for 60 to 700 milliseconds, so 80 renders
// every one of them at its own timing; a second would swallow whole frames
// and turn a double-take into a single blink.
func (b *bird) Tick() time.Duration {
	if b.performing() {
		return 80 * time.Millisecond
	}
	return time.Second
}

// idle does something unprompted when it has been quiet long enough. It is
// called from Draw rather than from a clock of its own, so a bird nobody is
// looking at -- puffin headless, the overlay switched off -- performs to
// nobody exactly never.
func (b *bird) idle(sp auklet.Sprite, now time.Time) {
	if b.idleAt.IsZero() {
		b.idleAt = now.Add(idleFloor)
		return
	}
	if b.performing() || now.Before(b.idleAt) {
		return
	}
	b.play(sp, idleEmotes[b.idleNext%len(idleEmotes)])
	b.idleNext++
	// the interval wanders: the spread comes off the clock rather than a
	// random source, which keeps the whole thing reproducible for anyone
	// rendering frames from a script.
	b.idleAt = now.Add(
		idleFloor + time.Duration(now.UnixNano()%int64(idleSpread)),
	)
}

// performing says whether an emote is still running.
func (b *bird) performing() bool {
	if len(b.playing.Frames) == 0 {
		return false
	}
	_, ok := b.playing.Pose(time.Since(b.since))
	return ok
}

// play starts an emote by name, if the sprite has one. A cue for an emote
// that does not exist is dropped rather than approximated: a bird doing
// something else instead of what it was asked is worse than a bird at rest.
func (b *bird) play(s auklet.Sprite, name string) {
	if e, ok := s.Emote(name); ok {
		b.playing, b.since = e, time.Now()
	}
}

// Handle reacts to things that happened: a blink, and a glance toward the
// page.
//
// Blink and gaze are the vocabulary, and the limit is the art, not taste.
//
// The sprite is one flat grid with nothing drawn behind it, so a part can move
// exactly as far as there is art behind it -- the pupil has a drawn socket to
// uncover, so it moves; the beak has nothing behind it, so moving it would open
// a hole in the head.
//
// Blink, look, and (with the front view) talk is the whole surface.
//
// The direction is a direction, not a position: only the sign survives the
// auklet's clamp, and at eight rows there is nothing between looking left and
// looking further left. The bird sits in the bottom-right corner, so everything
// puffin draws is up and to its left; what the kind chooses is how far up.
func (b *bird) Handle(e Event) {
	sp := b.sprite
	// what the event asked for wins over what its kind usually means. This
	// is the whole of the word hook: a watch carries an emote, the event
	// carries it here, and the corner does that instead of the default.
	//
	// A cue this character does not have is dropped by play() rather than
	// approximated -- the existing rule, and the right one:
	//
	// a bird doing something else instead of what it was asked is worse
	// than a bird at rest, and it means a hook set while the gopher was on
	// does not turn into nonsense when the lamp takes over.
	if e.Emote != "" {
		b.play(sp, e.Emote)
		return
	}
	switch e.Kind {
	case AgentWaiting:
		b.play(sp, "curious")
	case WatchFired, Broke:
		// startled flinches shut and snaps its eyes open.
		//
		// It used to be a double blink, which degraded to nothing on a
		// short terminal; surprise is a scaled eye now, and a scaled
		// eye changes six cells at eight rows where a moving pupil
		// changes one.
		b.play(sp, "startled")
	case Acted, Recovered:
		// puffin's own doing: it notices, it does not startle
		b.play(sp, "blink")
	}
}

// Draw composites the bird into the frame's bottom-right corner.
//
// The bird is drawn on an opaque ground the colour of the page rather than as a
// cutout. A cutout is the better rendering and it needs the frame as cells;
// puffin renders to a styled string, and escape sequences carry no geometry, so
// there is nothing to clip against.
//
// Splicing an opaque box in by visible column is honest and costs one thing:
// the box hides whatever is under it. In the bottom-right corner that is
// padding, which is why the bird sits there.
func (b *bird) Draw(frame string, width, height int) string {
	if width < 40 || height < 16 {
		// no room; a bird crushed into a corner is a smudge
		return frame
	}
	t := b.themeFor(currentTheme())
	if b.bad {
		return frame
	}
	// What is behind the bird is the terminal, not the theme.
	//
	// This used to be currentTheme().Bg, on the reasoning that the sprite
	// sits on the page and should carry the page's colour. That holds only
	// if puffin paints the page that colour, and it does not -- no style
	// here sets a page background.
	//
	// So the value was the theme's idea of a page laid over a terminal that
	// is whatever the terminal is.
	//
	// Under every dark theme those agree closely enough to look right.
	// Stardew is dodo's light theme, Bg #f3e3c3, and in a dark terminal it
	// drew a cream halo around every half-covered cell of the silhouette.
	// It is dark against light, and nothing subtler than that.
	//
	// nil is transparent: the half-cell keeps whatever is actually behind
	// it. That is right in a light terminal and a dark one, and it is what
	// compositing over content will need -- a bird flying over a map has to
	// keep the map, not the page.
	b.idle(b.sprite, time.Now())
	rows := rowsFor(height, countLines(frame))
	cols := b.sprite.ColsFor(rows)
	pose, _ := b.playing.Pose(time.Since(b.since))
	// nothing being performed, so the eyes are free: look at the pointer.
	//
	// Only when at rest.
	//
	// An emote is a piece of timing that owns the face for its duration,
	// and a gaze laid over a blink is two things driving the same cells --
	// the library has a whole Part mechanism for saying so, and the
	// cheapest way to honour it is not to fight in the first place.
	//
	// A stale pointer looks at nothing: Toward returns 0,0 when the mouse
	// has not been seen recently or puffin is not holding it, and Gaze of
	// 0,0 is no overlay at all.
	//
	// So a pet stops staring at the corner the mouse left two minutes ago,
	// and one in a terminal with mouse reporting off never starts.
	if len(pose) == 0 {
		x, y, ok := cornerAt(countLines(frame), cols, rows, width)
		if ok {
			dx, dy := MousePointer().Toward(x+cols/2, y+rows/2)
			pose = b.sprite.Gaze(dx, dy)
		}
	}
	if rows < birdRowsEyes {
		// a moving pupil is what does not survive eight rows; a scaled
		// one does, so this strips the gaze overlays and leaves
		// everything else, surprise included.
		pose = withoutGaze(pose)
	}
	// The glyph IS the compositing, and no alpha channel is needed for it.
	//
	// A quadrant cell carries a foreground and a background, so a cell that
	// is half bird and half not draws the bird in the foreground and lets
	// the background be whatever is behind -- the edge blends without
	// anything being blended.
	//
	// So NO background is set here at all. Filling the whole canvas painted
	// an opaque rectangle, which is what made it look like a sticker;
	// filling only the bird's own cells fixed the rectangle and kept a
	// smaller version of the same mistake, which stardew made visible.
	return compositeCorner(
		frame,
		companionCells(b.sprite, t, cols, rows, pose),
		cols,
		rows,
		width,
	)
}

// companionCells is the sprite as cells, with NO background set on any of
// them.
//
// It is a named function rather than a call inlined into Draw because the bug
// it encodes was invisible from outside: Draw returns a composited string,
// styleFor renders through lipgloss, and lipgloss strips colour when there is
// no TTY
//
// -- so a test that rendered the frame and looked for a background found
// nothing under every theme, halo or no halo.
//
// A test can see cells. It cannot see a string that has had its colour removed.
func companionCells(
	sp auklet.Sprite,
	t auklet.Theme,
	cols, rows int,
	pose auklet.Pose,
) [][]canvas.Cell {
	return sp.CellsAt(t, auklet.Quadrant, cols, rows, pose)
}

// withoutGaze strips the pupil-shifting overlays from a pose and keeps
// everything else, which is what the small bird can actually show.
//
// The filter is by NAME and not by Part, and that is not a shortcut: blink and
// gaze are both PartEyes, correctly, because they drive the same region and
// must not be played over each other.
//
// But they do not measure the same -- a blink changes three cells at 8 rows and
// reads; a look changes two and flickers. So the emote still plays, and still
// plays with its own timing, which is most of what makes a double-take read as
// a double-take even when you cannot see the pupils.
func withoutGaze(p auklet.Pose) auklet.Pose {
	var out auklet.Pose
	for _, o := range p {
		if o.Name == "gaze" {
			continue
		}
		out = append(out, o)
	}
	return out
}

// themeFor maps puffin's palette onto the bird's roles, recompiling only
// when the theme actually changes -- Validate is a per-selection cost, not a
// per-frame one.
//
// The mapping is close to a rename because puffin's theme already carries the
// bird's colours: it has been painting a puffin since before this package
// existed. What it must NOT carry across is Ok, Warn and Caution.
//
// They are semantic, they are meant to read the same in every skin, and a
// warning colour spent on a beak stops being a warning.
func (b *bird) themeFor(p Theme) auklet.Theme {
	// the cache is keyed on BOTH, because the character can change without
	// the theme doing so -- pressing C used to leave the previous
	// character's colours on the new one until the theme was also cycled
	if p.Name == b.lastTheme && b.character == b.lastCharacter {
		return b.theme
	}
	b.lastTheme, b.lastCharacter = p.Name, b.character
	if t, ok := nativeTheme(b.character); ok {
		b.theme, b.bad = t, false
		return b.theme
	}
	t, ok := aukletTheme(p)
	b.theme, b.bad = t, !ok
	return b.theme
}

// referenceTheme is the puffin as drawn -- the auklet library calls it
// "atlantic", "the reference bird, as drawn".
//
// The splash uses it instead of puffin's palette, because the splash is a
// portrait rather than a widget. Puffins are black, and their faces are white;
// a theme that paints the cap pink is the same mistake as inverting it,
// arriving by a different route.
//
// Under vaporwave the splash bird simply stopped being a puffin.
//
// The corner auklet still follows the theme. It sits inside the interface
// and has to live with whatever the page is wearing; the splash is a full
// screen of bird and owes the page nothing.
func referenceTheme() (auklet.Theme, bool) {
	t := auklet.DefaultTheme()
	t.Background = nil // cutout, same as everywhere else
	if t.Validate() != nil {
		return auklet.Theme{}, false
	}
	return t, true
}

// nativeTheme is a character's OWN colours, when the character shipped with
// some.
//
// The auklet is puffin's bird and belongs to puffin's palette: it is drawn
// from dodo's stylesheet and it should follow the theme, because a mascot
// that ignores the skin everything else wears is the mascot opting out of
// the thing the theme system exists for.
//
// The guests are the opposite case. The gopher is Go's blue, not an
// invented teal; the lamp is a lamp. Those were chosen, by whoever drew
// them, and repainting them in puffin's palette is not theming, it is
// damage: they come out looking wrong in every theme.
//
// So: a character with its own theme keeps it, and only the auklet follows
// puffin. The Background is dropped either way, because the corner is a
// cutout and a theme's own background paints an opaque rectangle -- which
// is the "sticker" the Draw comment is about.
func nativeTheme(character string) (auklet.Theme, bool) {
	if character == "" || character == "auklet" {
		return auklet.Theme{}, false
	}
	for _, n := range themes.All {
		if n.Name != character {
			continue
		}
		t := n.Theme
		t.Background = nil
		if t.Validate() != nil {
			// a character whose own theme does not survive the
			// validator falls back rather than drawing a blob
			return auklet.Theme{}, false
		}
		return t, true
	}
	return auklet.Theme{}, false
}

// aukletTheme is the mapping itself, without the cache, so the splash can
// ask for it too. The second return says whether the result is drawable: a
// theme whose contrasts have collapsed still renders, it just renders a
// blob, and a blob reads as a bug in the terminal.
func aukletTheme(p Theme) (auklet.Theme, bool) {
	// the auklet ships its own adapter for dodo's themes, generated from
	// the same stylesheet puffin pulls at startup.
	//
	// Where it has an opinion, take it: it branches on the page's luminance
	// for the roles a pale ground needs different answers for, and it has
	// been tuned against the art rather than inferred from a terminal
	// palette.
	if v, ok := themes.Dodo[p.Name]; ok {
		t := v.Auklet()
		t.Background = nil // cutout: see Draw
		if t.Validate() == nil {
			return t, true
		}
	}
	hand := auklet.Theme{
		Background: nil, // cutout: see Draw
		Dark:       p.Plumage,
		Light:      p.Snow,
		Wing:       p.Line,
		BeakBase:   p.Dim,
		BeakBand:   p.AccentToken,
		BeakTip:    p.Beak,
		Feet:       p.Beak,
		EyeRing:    p.Accent,
		Pupil:      p.Plumage,
		Stripe:     p.Dim,
	}
	// once per selection, and the answer decides whether to draw at all: a
	// theme whose contrasts have collapsed still renders, it just renders a
	// blob, and a blob in the corner reads as a bug in the terminal.
	//
	// puffin's own mapping fails this on stardew, and the reason is worth
	// recording: puffin darkens Snow on a pale ground so the bird stays
	// visible against the page, which collapses it against Plumage. The two
	// adjustments are each correct for their own screen and wrong together.
	//
	// The auklet's generated theme above handles it, so this path is the
	// fallback for a theme it has never heard of.
	return hand, hand.Validate() == nil
}

// spliceCorner puts a rendered block into the bottom-right of a frame,
// replacing what was there.
//
// By visible column, not by byte: the frame is full of escape sequences and
// len() counts them as characters. That is the same arithmetic the split
// needed, and getting it wrong tears the right-hand side into a staircase.
func spliceCorner(frame, block string, w, h, width int) string {
	lines := strings.Split(frame, "\n")
	bl := strings.Split(block, "\n")
	if len(lines) < h+2 {
		return frame
	}
	// the bird goes in the terminal's unused width, not the content's. The
	// first cut measured the widest row in the frame and put the bird two
	// columns left of that, which on a frame whose rows are all the same
	// width means every row is "too long" and nothing draws at all.
	//
	// Puffin renders to its content and the terminal is usually wider; that
	// gap is where a mascot belongs.
	start := width - w - 2
	if start < 1 {
		return frame
	}
	// one row of clearance above the last line, so the bird is not sitting
	// on the help text
	top := len(lines) - h - 1
	for i := 0; i < h && i < len(bl); i++ {
		row := lines[top+i]
		at := visibleWidth(row)
		// a row that already reaches into the bird's column is left
		// alone rather than truncated.
		//
		// Losing a character of somebody's log line to a mascot is not
		// a trade worth making, and a bird with a bite out of it is a
		// visible, honest consequence of a narrow terminal.
		if at > start {
			continue
		}
		lines[top+i] = row + strings.Repeat(" ", start-at) + bl[i]
	}
	return strings.Join(lines, "\n")
}

// splashRows is how tall the cropped opening bird stands, in cells.
const splashRows = 14

// splashBird is the opening shot: the front view, cropped to the head and
// chest, big and centred.
//
// The crop is the whole trick and it costs nothing. The sprite is rendered at
// full height into cells and blitted into a canvas that is shorter than it --
// Blit clips, so the rows past the canvas's bottom are simply never written.
//
// No masking, no second art file, no viewport arithmetic: a torso is a full
// bird in a short frame.
//
// Cropping is also what makes the front view usable here. Head-on, a puffin
// is two white cheeks split by a blade of beak, and that reads at this size;
// the feet and the belly below it do not, and at fourteen rows they are what
// you would be spending the height on.
//
// It returns false when there is nothing worth drawing -- a theme that
// failed validation, or a terminal too small -- and the caller keeps the
// hand-drawn bird, which owes nothing to any of this.
// guestName is who sits in the splash's guest seat.
//
// It is the companion -- so choosing one changes both places it appears --
// except when the companion is the auklet, because the auklet is already
// standing full-screen in the middle of this very screen and a seabird's
// terminal does not host itself.
//
// That case is the default, and it is why the splash has had a gopher in the
// corner since the first commit. guestName is who stands on the splash, which
// is its own setting.
//
// It followed the corner companion, so choosing a guest for the corner also put
// her on the splash and there was no way to say "the gopher pays its respects
// while somebody else lives in the corner".
//
// One setting cannot answer two questions -- the same mistake that made C
// stick, arriving in a different place on the same day.
//
// Resolution order matches every other setting here: environment, then the
// remembered value, then a default. The default is the old behaviour, so a
// puffin that has never been told anything looks exactly as it did.
func guestName() string {
	if n := os.Getenv("PUFFIN_GUEST"); n != "" {
		return n
	}
	if n := rememberedGuest(); n != "" {
		return n
	}
	// the fallback: the companion, unless the companion is the auklet, in
	// which case the gopher -- two puffins on one splash is not a guest,
	// it is a reflection
	if name := currentMascot(); name != "auklet" {
		return name
	}
	return "gopher"
}

// awkwardIdles is what the guest does, forever: it shifts its weight, looks
// away, blinks, looks back. Awkward is the whole of it -- a guest that performs
// is a guest you watch instead of reading the screen, and the point of the
// corner is that nothing there asks for your eye.
var awkwardIdles = []string{"shifty", "look-away", "blink", "curious"}

// splashFPS is the splash clock, as a duration, so a frame count can be
// turned into the elapsed time the emotes are written in.
const splashFPS = 125 * time.Millisecond

// guestRows is how tall the guest stands. Smaller than the bird on purpose:
// it is a guest.
const guestRows = 12

// splashGuest draws the companion in the corner, animated: it walks in if it
// knows how, then loops the awkward idles.
//
// Same bargain splashBird makes -- it returns false when the room or the
// theme will not support it, and the caller falls back to the hand-drawn
// art. A guest that renders as a blob is worse than a guest that is drawn.
func splashGuest(p Theme, width, height, frame int) (string, bool) {
	if width < 90 || height < guestRows+20 {
		return "", false
	}
	// the guest on the splash is a guest for the same reason it is a guest
	// in the corner, so it gets its own colours here too. Doing this in one
	// place and not the other is how the same character comes out looking
	// like two characters.
	name := guestName()
	t, ok := nativeTheme(name)
	if !ok {
		if t, ok = aukletTheme(p); !ok {
			return "", false
		}
	}
	sprite, ok := spriteFor(name)
	if !ok {
		return "", false
	}

	// drawn whole, at the height it is shown. The first cut copied the
	// splash bird's idiom -- render at twice the frame's height and let the
	// canvas crop it -- and that is a deliberate portrait trick for the
	// auklet, whose head sits in the top third of its art.
	//
	// Applied to a character drawn full-body it shows the top half at
	// double scale: Ms Pac-Man came out as a smear of repeated eyes, which
	// is exactly what "looked like the gopher but real distorted" is.
	pose := guestPose(sprite, time.Duration(frame)*splashFPS)
	cols := sprite.ColsFor(guestRows)
	c := canvas.New(cols, guestRows)
	// transparent, for the same reason the corner is: see Draw. The guest
	// carried the theme's page colour and stardew's page is cream.
	c.Blit(sprite.CellsAt(t, auklet.Quadrant, cols, guestRows, pose), 0, 0)
	return c.String(), true
}

// guestPose is the whole performance: the entrance once, then the idles on a
// loop. Time-based rather than frame-indexed, because the emotes carry their
// own timing and a table of frame numbers here would be a second opinion
// about it -- which is how the two get out of step.
func guestPose(sprite auklet.Sprite, elapsed time.Duration) auklet.Pose {
	if e, ok := sprite.Emote("walk-in"); ok {
		if elapsed < e.Length() {
			if pose, ok := e.Pose(elapsed); ok {
				return pose
			}
			return nil
		}
		elapsed -= e.Length()
	}
	// the idle ring: each emote in turn, with a beat of rest between them.
	// Back to back they read as a twitch; the gap is what makes it look
	// like a creature deciding to do something.
	const rest = 900 * time.Millisecond
	var ring []auklet.Emote
	var total time.Duration
	for _, name := range awkwardIdles {
		e, ok := sprite.Emote(name)
		if !ok {
			continue
		}
		ring = append(ring, e)
		total += e.Length() + rest
	}
	if len(ring) == 0 || total <= 0 {
		return nil
	}
	at := elapsed % total
	for _, e := range ring {
		if at < e.Length() {
			if pose, ok := e.Pose(at); ok {
				return pose
			}
			return nil
		}
		at -= e.Length()
		if at < rest {
			return nil // at rest, which is a pose too
		}
		at -= rest
	}
	return nil
}

// splashBird draws the opening portrait. The second return is false when
// the terminal is too small to hold a bird worth drawing, so the caller
// can skip the splash rather than render a smear.
func splashBird(p Theme, width, height, frame int) (string, bool) {
	if width < 60 || height < splashRows+12 {
		return "", false
	}
	t, ok := referenceTheme()
	if !ok {
		return "", false
	}
	// full height, then cropped: the head lands in the top third of the
	// art, so twice the frame's height puts the chest at the cut.
	full := splashRows * 2

	// The opening, storyboarded on the MGM lion:
	//
	//   profile, facing left
	//   pan to the fourth wall
	//   rawr
	//   turn away and back
	//   rawr again
	//
	// It plays at eight frames a second, which is the whole reason the
	// splash clock got faster: at three a second a four-frame pan is a
	// slideshow of four birds rather than one bird turning. The eye reads
	// the change between frames, not the frames.
	//
	// The pan is four drawings -- 90, 60, 30, 0 degrees -- and stepping
	// Views() forward IS the pan. The glance is a step back to 30 and
	// forward again, which is why the storyboard needed intermediates at
	// all: with only a profile and a front there is nowhere for it to go.
	view, pose, drop := auklet.FrontView, auklet.Pose(nil), 0
	views := auklet.Views()
	at := func(i int) auklet.Sprite {
		return views[clampInt(
			i,
			0,
			len(views)-1,
		)]
	}
	switch {
	case frame < 4:
		view = at(0) // profile, held: the bird is there before it moves
	case frame < 7:
		view = at(frame - 3) // 60, 30, 0
	case frame == 8 || frame == 9:
		pose = view.Blink // arriving, and looking at you
	case frame == 12 || frame == 30:
		// the attack: widest beak, and the body drops a cell under it.
		// The drop is the caller's because the caller owns size and
		// position.
		pose, drop = view.Mouth(3), 1
	case frame == 13 || frame == 31:
		pose = view.Mouth(3)
	case frame == 14 || frame == 32:
		pose = view.Mouth(2)
	case frame == 15 || frame == 33:
		pose = view.Mouth(1)
	case frame == 22 || frame == 23:
		view = at(2) // the glance away, fifteen degrees in her telling
	case frame == 24:
		view = at(2)
		pose = view.Blink
	}

	// the columns come from the current view: the profile is wider than the
	// front, so a fixed width would letterbox the pan
	cols := view.ColsFor(full)
	c := canvas.New(cols, splashRows)
	cells := view.CellsAt(t, auklet.Quadrant, cols, full, pose)
	behind := p.Bg
	for y := range cells {
		for x := range cells[y] {
			if cells[y][x].R != 0 && cells[y][x].BG == nil {
				cells[y][x].BG = behind
			}
		}
	}
	// clipped at splashRows: this is the crop, and the same clip absorbs
	// the settle -- a bird dropped by a cell loses a cell off the bottom
	// rather than making the block taller, so the layout never moves.
	c.Blit(cells, 0, drop)
	return c.String(), true
}
