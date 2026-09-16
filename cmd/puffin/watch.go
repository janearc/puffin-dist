package main

import (
	"regexp"
	"strings"
	"sync"
)

// Watches: wait for a thing to appear, and say so.
//
// Leave a log or an agent open, wait for a pattern, get told.
//
// This is the payoff for the two pieces already built -- panes that refresh
// themselves, and a notifier that fires on transitions -- and it is the
// thing a log pane is actually for. Nobody watches a log. They wait for one
// line in it, and doing that with their eyes is the expensive way.
//
// A pattern is a substring by default. Regexes have no place in a tool's own
// internals, but this one is different: the pattern is the operator's, typed at
// the moment it is needed, which is the case regular expressions exist for.
//
// So a pattern wrapped in slashes is a regex and everything else is plain text
// -- the same bargain every editor makes, and the plain case stays plain.

// Watch is one thing being waited for.
type Watch struct {
	Pattern string `json:"pattern"`
	Where   string `json:"where"` // logs | agents | bus
	// Emote is what the companion does when this fires: a hook for
	// characters or words, where the pattern is the word and the emote is
	// what happens.
	//
	// It costs a field because the pipeline was already built -- a watch
	// matches arriving lines, emits an event, and the corner bird is
	// already subscribed to that stream and already emotes on it.
	//
	// All that was missing was letting a watch say which, rather than every
	// watch producing the same startle.
	//
	// Empty means the default for the event's kind, so every watch that
	// already exists in ui.json keeps behaving exactly as it did.
	Emote string `json:"emote,omitempty"`
	re    *regexp.Regexp
	bad   string
}

// compile prepares a watch. A pattern in /slashes/ is a regex; anything else
// is matched as a case-insensitive substring.
func (w *Watch) compile() {
	w.re, w.bad = nil, ""
	p := w.Pattern
	if len(p) > 2 && strings.HasPrefix(p, "/") &&
		strings.HasSuffix(p, "/") {
		re, err := regexp.Compile("(?i)" + p[1:len(p)-1])
		if err != nil {
			w.bad = err.Error()
			return
		}
		w.re = re
	}
}

// Matches says whether a line is the thing being waited for.
func (w *Watch) Matches(line string) bool {
	if w.bad != "" {
		return false
	}
	if w.re != nil {
		return w.re.MatchString(line)
	}
	return strings.Contains(
		strings.ToLower(line),
		strings.ToLower(w.Pattern),
	)
}

// Label is how the watch reads on screen. The emote is part of the label
// because a hook you cannot see is one you forget you set -- the same reason
// the watch itself is shown rather than merely kept.
func (w Watch) Label() string {
	out := w.Pattern
	switch {
	case w.bad != "":
		out += " (not a valid pattern)"
	case w.re != nil:
		out += " (regex)"
	}
	if w.Emote != "" {
		out += " → " + w.Emote
	}
	return out
}

// watches is the live set, kept in one place so the panes and the
// preferences file agree about what is being waited for.
var watches = struct {
	mu   sync.Mutex
	list []*Watch
}{}

// addWatch starts waiting for something. Adding the same pattern twice is a
// no-op rather than two notifications.
func addWatch(pattern, where string) *Watch {
	watches.mu.Lock()
	defer watches.mu.Unlock()
	for _, w := range watches.list {
		if w.Pattern == pattern && w.Where == where {
			return w
		}
	}
	w := &Watch{Pattern: pattern, Where: where}
	w.compile()
	watches.list = append(watches.list, w)
	rememberWatches(snapshotWatches())
	return w
}

// dropWatch stops waiting.
func dropWatch(pattern, where string) {
	watches.mu.Lock()
	out := watches.list[:0]
	for _, w := range watches.list {
		if w.Pattern != pattern || w.Where != where {
			out = append(out, w)
		}
	}
	watches.list = out
	watches.mu.Unlock()
	rememberWatches(snapshotWatches())
}

// watchesFor is the set that applies to one pane.
// cycleWatchEmote advances what the companion does about one watch, and
// previews it by cueing the corner immediately -- choosing an emote you
// cannot see is choosing from a list of words.
//
// The choices come from the current companion's own repertoire rather than a
// list here, so the gopher offers its walk-in and ms pacman offers her spin.
//
// A hook set while one character was on is kept verbatim when another takes
// over: play() drops a cue the new character lacks, which is quieter than
// substituting something it did not ask for.
//
// The empty string is part of the ring, and deliberately first: "whatever
// the kind usually means" is a real answer and has to be reachable by going
// round rather than only by deleting the watch.
func cycleWatchEmote(pattern, where string) (string, bool) {
	names := companionEmotes()
	if len(names) == 0 {
		return "", false
	}
	ring := append([]string{""}, names...)

	watches.mu.Lock()
	var next string
	var found bool
	for _, w := range watches.list {
		if w.Pattern != pattern || w.Where != where {
			continue
		}
		at := 0
		for i, n := range ring {
			if n == w.Emote {
				at = i
			}
		}
		next = ring[(at+1)%len(ring)]
		w.Emote, found = next, true
	}
	watches.mu.Unlock()
	if !found {
		return "", false
	}
	rememberWatches(snapshotWatches())
	previewEmote(next)
	return next, true
}

// watchesFor is the watches that apply to one source, so a line arriving
// is tested against those and not against every pattern ever typed.
func watchesFor(where string) []*Watch {
	watches.mu.Lock()
	defer watches.mu.Unlock()
	var out []*Watch
	for _, w := range watches.list {
		if w.Where == where {
			out = append(out, w)
		}
	}
	return out
}

// snapshotWatches is the persistable form.
func snapshotWatches() []Watch {
	out := make([]Watch, 0, len(watches.list))
	for _, w := range watches.list {
		out = append(
			out,
			Watch{
				Pattern: w.Pattern,
				Where:   w.Where,
				Emote:   w.Emote,
			},
		)
	}
	return out
}

// restoreWatches puts back what was being waited for last time. A watch is a
// standing instruction -- "tell me when the migration finishes" outlives the
// session you typed it in.
func restoreWatches(saved []Watch) {
	watches.mu.Lock()
	defer watches.mu.Unlock()
	watches.list = nil
	for i := range saved {
		// field by field on purpose -- re and bad are derived, not
		// stored, and compile() is what sets them.
		//
		// Every field that IS stored has to be named here: the emote
		// was added to the struct and silently dropped by this loop, so
		// a hook survived until the next restart and then quietly
		// became "whatever the kind usually means".
		w := &Watch{
			Pattern: saved[i].Pattern,
			Where:   saved[i].Where,
			Emote:   saved[i].Emote,
		}
		w.compile()
		watches.list = append(watches.list, w)
	}
}
