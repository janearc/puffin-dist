# seams

What puffin offers something that wants to draw on it, and what it expects
back. Written for puffin-auklet, which is a masked, articulating puffin
sprite with a cell canvas, published as `github.com/janearc/puffin-auklet`.

Puffin keeps the seam narrow on purpose. The two move at different speeds,
and a seam pinned to an API mid-change breaks twice a day. Puffin declares
what it needs; one adapter satisfies it, and that adapter should be the only
file that knows both names.

## 1. The overlay

```go
type Overlay interface {
    Draw(frame string, width, height int) string
    Tick() time.Duration
    Handle(Event)
}

SetOverlay(o Overlay)   // installs it, and subscribes it to events
```

`Draw` receives a **fully rendered frame** -- padded, styled, one string --
and returns one to print. Every screen in puffin ends up here: the roster,
the contract, flags, maps, the cluster, the pager and all six panes, split or
not. One call site, ten screens.

Two clauses. One is enforced:

- **The frame must keep its shape.** An overlay returning a different line
  count is discarded and the original is printed. The alternative is the
  interface jumping by a line with nothing to say which of two things did it.
- **`Draw` runs on the update loop, once per frame.** It must be fast, and
  it must not block on anything.

`Tick` is how often the overlay wants waking. Zero means never. A parked
sprite that blinks twice a minute should ask for a second, not 15fps: idle
ASCII animation reads as alive at rates that would look broken in video,
because the eye is reading glyph changes rather than motion.

Puffin renders correctly with no overlay installed, and that is a
requirement rather than an accident -- it is what lets the bird be built in
another repository without blocking this one.

### the compositing problem, and whose it is

Puffin hands over a **string with ANSI escapes in it**. Those carry no
geometry, so there is nothing to clip a sprite against -- the auklet's readme
says this better than this file will. Compositing has to happen in cell
space.

Puffin does not have a cell buffer. The auklet does. So the adapter owns the
conversion: parse the frame into cells, blit, produce the string once.

If that turns out to be the wrong division -- if puffin's views should render
cells natively -- say so, and that is a real change on this side rather than a
workaround on yours.

## 2. Events

```go
type Event struct {
    Kind    Kind    // AgentWaiting, AgentWorking, WatchFired, Broke,
                    // Recovered, Acted, Quiet
    Subject string  // what it happened to
    Detail  string  // one line, for a human
    At      time.Time
}

Listen(func(Event))
Emit(Event)
```

An installed overlay is subscribed automatically; `Handle` receives
everything.

**Everything emitted is a transition, never a state.** "An agent is waiting" is
true every five seconds; "an agent has just started waiting" is true once. This
was learned expensively here: a notifier that re-sends a condition is one people
mute, and a muted notifier is worse than none because it is still half-trusted.

A sprite that emotes on states is the same failure with a beak -- permanently
agitated, and turned off within a week.

What exists today, and where it comes from:

| Kind | emitted when |
|---|---|
| `AgentWaiting` | a Claude session's last turn ended in prose and it went quiet |
| `AgentWorking` | a session that was idle or waiting started again |
| `WatchFired` | a pattern an operator asked about appeared in logs, the bus, or an agent |
| `Acted` | puffin changed something and it worked: a flag flip, a workload restart |
| `Broke` | declared, not yet emitted -- the cluster pane knows, it just has not been wired |
| `Recovered` | same |
| `Quiet` | same: intended for winding down an idle animation |

If the sprite wants an event that is not here, it is probably one puffin
already computes and does not announce. Ask rather than deriving it from
`Detail` strings.

## 3. The palette

```go
Styles.Tokens map[string]string   // dodo's raw variables for the live theme
Styles.Theme  string              // its name: corvid, vaporwave, ...
```

Both projects read dodo's stylesheet, which is the right source and the wrong
number of times: two readers can disagree about which theme is current.

So puffin passes its **whole** parsed palette through -- every `--token`, not
the dozen a terminal paints with -- and the adapter should ink the stencil from
that rather than fetching the CSS again.

Puffin pulls themes live from `dodo.$PUFFIN_DOMAIN` at startup and caches
them; `t` cycles at runtime and `T` re-pulls. If the bird inks from
`Styles.Tokens`, it changes with the terminal for free.

Two colours in puffin are deliberately constant across every theme: the beak is
orange and the gopher is Go blue.

The auklet's own theme adapter already branches on background luminance rather
than renaming tokens, which is the harder and correct half of this -- `--ink` is
near-white on corvid and near-black on stardew.

## 4. Off

`PUFFIN_BIRD=off`, remembered in `~/.local/state/puffin/ui.json`, stored as
a pointer so "never said" stays distinguishable from "said no".

A bird animating in the corner while somebody reads a stack trace at three in
the morning is a bird they kill the tool over. The off switch is not a
concession, it is the thing that lets the bird be interesting the rest of the
time.

## what is missing, honestly

- puffin has no cell buffer of its own, so the adapter carries the parse
- `Broke`, `Recovered` and `Quiet` are declared but not yet emitted
- there is no way for an overlay to ask for a redraw out of band; it gets
  `Tick` and events, and that is all
- the split renders two panes by splicing rendered blocks and padding by
  visible width. That is the compositor, hand-rolled, for one case, and it
  should probably become the auklet's canvas once the adapter exists
