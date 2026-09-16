package main

import (
	"errors"
	"math"
	"math/rand"
	"os/exec"
	"strconv"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// what a loop in puffin is allowed to do.
//
// Three rules, and the estate has had two of them written down for a while
// without puffin implementing either: the words backoff and jitter appear in
// zero non-test files in this repository. The third is the one that explains
// 2026-09-11, and it is not in any rule anywhere yet.
//
// Rule 1, backoff with jitter. A source that fails is asked again later, and
// later again, and the delay carries noise so that N panes recovering from
// one outage do not all return at the same instant. This is the estate rule
// verbatim: every network call, every retry, no exceptions.
//
// Rule 2, a spawn budget. At most n child processes a minute per source.
// Over it, the source is degraded and stops spawning until the minute turns.
// The picture says degraded; it never quietly shows an hour-old answer.
//
// Rule 3, one round at a time. A refresh is not started while the previous one
// is outstanding.
//
// This is the rule that matters here, because it is the only one of the three
// whose absence can be pointed at in running code: main.go schedules the next
// tick when a load starts, not when it returns, so a round that outlives its
// interval runs alongside the next.
//
// Sequential probes against a 3s timeout make that certain on a slow provider,
// and it is self-reinforcing -- more concurrent rounds, more load, slower
// answers, longer rounds. Backoff does not catch it, because nothing failed.
//
// The spawn budget does not catch it, because the volume is inside the
// interval. A provider answering slowly and correctly trips neither.
//
// None of this is the fix. The fix is the split, where the viewer holds no
// loop that touches the world at all. This is what the loops obey until then.

// backoffBase is the first delay after a failure, and backoffCap the ceiling.
// A ceiling matters more than the curve: without one, a source that is down
// overnight comes back hours later and the pane is a museum piece.
const (
	backoffBase = 2 * time.Second
	backoffCap  = 5 * time.Minute
	// jitterFrac is how much of the delay is noise, each way. A third is
	// enough to scatter a thundering herd without making the schedule
	// unreadable to somebody watching the log wondering why it is slow.
	jitterFrac = 0.33
)

// backoffFor is the delay before attempt n, counting the first failure as 1.
// Exponential, capped, then jittered. Never negative and never zero: a zero
// delay is a spin, which is the thing being prevented.
func backoffFor(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := float64(backoffBase) * math.Pow(2, float64(attempt-1))
	if d > float64(backoffCap) {
		d = float64(backoffCap)
	}
	// full-width jitter around the delay rather than only downward: a herd
	// that only ever shortens its wait converges again on the next round.
	noise := d * jitterFrac * (rand.Float64()*2 - 1)
	d += noise
	if d < float64(time.Second) {
		d = float64(time.Second)
	}
	return time.Duration(d)
}

// spawnBudget is how many child processes one source may start in a minute.
// Ten is the spec's default. It is deliberately low: every source in puffin
// today wants one or two commands per refresh, so ten is a ceiling nothing
// legitimate reaches, and anything that does has stopped being legitimate.
const spawnBudget = 10

// source is one thing a pane reads: the host's tmux, a kubernetes context,
// a log stream. It carries the three rules so that a caller cannot obey two
// of them and forget the third.
//
// It is deliberately not an interface. There is one implementation and the
// rules are the point; an interface here would let a source be written that
// declares itself well-behaved without being so.
type source struct {
	Name string

	mu          sync.Mutex
	inFlight    bool      // rule 3: a round is outstanding
	failures    int       // rule 1: consecutive failures, reset by success
	notBefore   time.Time // rule 1: earliest next attempt
	spawnWindow time.Time // rule 2: start of the current minute
	spawned     int       // rule 2: spawns inside that window
	// why the picture should say degraded, empty if not
	degraded string
	// kept so a success can clear the message it caused
	lastErr error
}

// newSource makes a source that has never been asked: the zero value is
// already correct, and this exists so a caller cannot forget the name.
func newSource(name string) *source { return &source{Name: name} }

// begin reports whether a refresh may start now, and why not when it may not.
// The reason is returned rather than logged because it belongs on screen:
// a pane that is not refreshing should say so, and "waiting 38s after 3
// failures" is a different thing for a reader than "budget spent".
func (s *source) begin(now time.Time) (ok bool, why string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inFlight {
		// RULE 3. Dropped, never queued: a queue turns a slow provider
		// into a backlog that outlives the reason for it.
		return false, "a refresh is already running"
	}
	if now.Before(s.notBefore) {
		return false, "waiting " + s.notBefore.Sub(now).
			Round(time.Second).
			String() +
			" after " + plural(
			s.failures,
			"failure",
		)
	}
	s.inFlight = true
	return true, ""
}

// done closes a round. Success clears the backoff; failure lengthens it.
// Calling done exactly once per begin that returned true is the contract,
// and the reason inFlight is not exported: nothing outside can forget.
func (s *source) done(now time.Time, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inFlight = false
	if err == nil {
		s.failures = 0
		s.notBefore = time.Time{}
		if s.degraded == "a refresh failed: "+errText(s.lastErr) {
			s.degraded = ""
		}
		s.lastErr = nil
		return
	}
	s.failures++
	s.lastErr = err
	s.notBefore = now.Add(backoffFor(s.failures))
	s.degraded = "a refresh failed: " + errText(err)
}

// spawnOK reports whether this source may start a child process now, and counts
// it when it may.
//
// The window is a whole minute rather than a sliding one on purpose: a sliding
// window needs a list of timestamps per source, and the question being answered
// is coarse enough that a minute that resets is both cheaper and easier to
// explain on screen.
func (s *source) spawnOK(now time.Time) (ok bool, why string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if now.Sub(s.spawnWindow) >= time.Minute {
		s.spawnWindow = now
		s.spawned = 0
		if s.degraded == budgetSpent {
			s.degraded = ""
		}
	}
	if s.spawned >= spawnBudget {
		s.degraded = budgetSpent
		return false, budgetSpent
	}
	s.spawned++
	return true, ""
}

const budgetSpent = "spawn budget spent for this minute"

// Degraded is what the pane should say, empty when it should say nothing.
func (s *source) Degraded() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.degraded
}

// lastErr is kept only so a later success can clear the message it caused,
// rather than leaving a stale reason under a source that is working again.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// plural spells a count with its noun, because "after 1 failures" on screen
// is the kind of thing that makes a reader distrust the rest of the row.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// paneSource is the source a pane's refreshes are gated on, made on first
// use and kept for the life of the program.
//
// Keyed by title rather than by pointer: bubbletea hands back a NEW pane
// value on every Update, so a pointer key would mint a fresh source each
// tick and the gate would never close -- which is the failure this is here
// to prevent, reintroduced by the bookkeeping meant to prevent it.
var paneSources struct {
	mu sync.Mutex
	m  map[string]*source
}

// paneSource is the gate a pane's refreshes go through, made on first use
// and kept for the life of the program.
func paneSource(p pane) *source {
	paneSources.mu.Lock()
	defer paneSources.mu.Unlock()
	if paneSources.m == nil {
		paneSources.m = map[string]*source{}
	}
	name := p.Title()
	if s, ok := paneSources.m[name]; ok {
		return s
	}
	s := newSource(name)
	paneSources.m[name] = s
	return s
}

// closeRounds ends the outstanding round on whichever panes are open, and it
// goes through done() so rule one is actually reached.
//
// An earlier cut of this set inFlight directly, which left backoff implemented
// and inert: failures never incremented, notBefore never advanced, and the
// degraded message existed only in tests. The one real caller forgot rule one
// structurally, which is exactly what source was shaped to prevent.
//
// A reply does not say which pane asked for it -- the message types are the
// panes' own and carry no source -- so both sides are closed on any reply.
//
// That is coarser than it could be and it is the safe direction: closing a
// round that was not open is a no-op, while leaving one open wedges the pane
// for the life of the program.
func closeRounds(msg tea.Msg, panes ...pane) {
	err := roundOutcome(msg)
	now := time.Now()
	for _, p := range panes {
		if p == nil {
			continue
		}
		paneSources.mu.Lock()
		s := paneSources.m[p.Title()]
		paneSources.mu.Unlock()
		if s != nil {
			s.done(now, err)
		}
	}
}

// roundOutcome reads success or failure out of a pane's reply.
//
// puffin has no error-returning loads: a source that fails appends to the
// view's Warnings and the pane draws them, which is the right shape for a
// screen and gives backoff nothing to count. So warnings ARE the failure
// signal, and a round that produced one is a round that did not work.
//
// A message this does not recognise counts as success. The alternative --
// treating the unknown as failure -- would put every pane into backoff the
// day somebody adds a reply type and forgets this switch, and a pane that
// silently stops refreshing is the worse failure.
func roundOutcome(msg tea.Msg) error {
	var warnings []string
	switch m := msg.(type) {
	case hostFetched:
		warnings = m.h.Warnings
	case deployFetched:
		warnings = m.v.Warnings
	case agentsFetched:
		warnings = m.v.Warnings
	case busFetched:
		warnings = m.v.Warnings
	}
	if len(warnings) == 0 {
		return nil
	}
	return errors.New(warnings[0])
}

// spawn runs a child process for a source, subject to the budget. Every
// exec.Command in a source's path goes through here; the budget is worth
// nothing as a definition, which is what it was before this.
//
// It returns the budget's reason rather than an exec error when it refuses,
// so a caller can tell "we did not ask" from "we asked and it failed" --
// they mean different things on a screen.
func (s *source) spawn(name string, args ...string) ([]byte, error) {
	if ok, why := s.spawnOK(time.Now()); !ok {
		return nil, errors.New(why)
	}
	return exec.Command(name, args...).Output()
}

// loadForced is a refresh somebody asked for: opening a pane, switching to
// one, splitting, or pressing r. It marks the round so a tick cannot start a
// second alongside it, and it never refuses.
//
// The review's finding was that these five sites started loads nothing was
// tracking, so the next tick saw inFlight false and began a second. That is the
// hole, and marking closes it.
//
// Refusing them was my over-correction and three tests said so within a minute:
// a pane opened has to fetch, a split has to load, r has to re-read. A person
// who has looked at the screen and decided it is wrong is not a clock that can
// wait.
//
// So the rule is about who asked. A tick that is early can wait, and rule
// three drops it. A keystroke is bounded by how fast somebody can press a
// key; the worst case is one extra concurrent round, not the unbounded
// accumulation a clock produces.
func loadForced(p pane, domain string) tea.Cmd {
	if p == nil {
		return nil
	}
	s := paneSource(p)
	s.mu.Lock()
	s.inFlight = true
	s.mu.Unlock()
	return p.Load(domain)
}
