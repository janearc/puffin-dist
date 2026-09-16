package main

import (
	"errors"

	tea "github.com/charmbracelet/bubbletea"
	"testing"
	"time"
)

// RULE 1. The delay grows, it is capped, and it is never zero -- a zero
// delay is the spin this exists to prevent.
func TestBackoffGrowsAndIsCapped(t *testing.T) {
	var prev time.Duration
	for attempt := 1; attempt <= 6; attempt++ {
		d := backoffFor(attempt)
		if d < time.Second {
			t.Fatalf(
				"attempt %d gave %v; never below a second",
				attempt,
				d,
			)
		}
		if d > backoffCap*2 {
			t.Fatalf(
				"attempt %d gave %v; past the cap even "+
					"allowing for jitter",
				attempt,
				d,
			)
		}
		if attempt > 1 && attempt < 5 && d < prev/2 {
			t.Errorf(
				"attempt %d gave %v after %v; it should be "+
					"growing",
				attempt,
				d,
				prev,
			)
		}
		prev = d
	}
	// far past the cap, the delay stays inside it plus jitter rather than
	// running away: a source down overnight comes back in minutes, not
	// hours
	for attempt := 20; attempt <= 40; attempt += 10 {
		if d := backoffFor(attempt); float64(
			d,
		) > float64(
			backoffCap,
		)*(1+jitterFrac)+float64(
			time.Second,
		) {
			t.Errorf(
				"attempt %d gave %v; the cap is %v",
				attempt,
				d,
				backoffCap,
			)
		}
	}
	// attempt 0 is a caller bug, not a crash
	if d := backoffFor(0); d < time.Second {
		t.Errorf("attempt 0 gave %v", d)
	}
}

// The jitter has to actually vary, or N panes recovering from one outage
// return in lockstep and the herd is intact.
func TestBackoffJitters(t *testing.T) {
	seen := map[time.Duration]bool{}
	for i := 0; i < 50; i++ {
		seen[backoffFor(4)] = true
	}
	if len(seen) < 10 {
		t.Errorf(
			"50 draws gave %d distinct delays; that is not jitter",
			len(seen),
		)
	}
}

// RULE 3, and the one that explains 2026-09-11: a second round cannot start
// while the first is outstanding, and the refusal says why.
func TestOneRoundAtATime(t *testing.T) {
	s := newSource("host")
	now := time.Now()
	if ok, _ := s.begin(now); !ok {
		t.Fatal("the first round was refused")
	}
	ok, why := s.begin(now)
	if ok {
		t.Fatal(
			"a second round started while the first was " +
				"outstanding",
		)
	}
	if why == "" {
		t.Error(
			"refused without saying why; the reason belongs on " +
				"screen",
		)
	}
	s.done(now, nil)
	if ok, why := s.begin(now); !ok {
		t.Errorf("refused after the round finished: %s", why)
	}
}

// A slow provider must not accumulate rounds. Ten ticks against one
// outstanding round produce one round, not ten.
func TestSlowProviderDoesNotAccumulate(t *testing.T) {
	s := newSource("pufback")
	now := time.Now()
	started := 0
	for i := 0; i < 10; i++ {
		if ok, _ := s.begin(
			now.Add(time.Duration(i) * time.Second),
		); ok {
			started++
		}
	}
	if started != 1 {
		t.Errorf(
			"%d rounds started; a provider is asked once at a time",
			started,
		)
	}
}

// RULE 1 again, at the source: failure backs off and says so, success clears
// both the delay and the message it caused.
func TestFailureBacksOffAndSuccessClears(t *testing.T) {
	s := newSource("kubernetes")
	now := time.Now()
	s.begin(now)
	s.done(now, errors.New("connection refused"))
	if s.Degraded() == "" {
		t.Error("a failed round left the pane saying nothing")
	}
	if ok, why := s.begin(now); ok {
		t.Error("began again immediately after a failure")
	} else if why == "" {
		t.Error("refused without a reason")
	}
	// past the backoff, it may try again
	later := now.Add(backoffCap * 2)
	ok, why := s.begin(later)
	if !ok {
		t.Fatalf("still refused long after the backoff: %s", why)
	}
	s.done(later, nil)
	if d := s.Degraded(); d != "" {
		t.Errorf("success left a stale reason: %q", d)
	}
}

// RULE 2. Ten spawns a minute, then degraded, then the minute turns and it
// is allowed again -- and the degraded message clears with it.
func TestSpawnBudget(t *testing.T) {
	s := newSource("host")
	now := time.Now()
	for i := 0; i < spawnBudget; i++ {
		if ok, why := s.spawnOK(now); !ok {
			t.Fatalf(
				"refused spawn %d of %d: %s",
				i+1,
				spawnBudget,
				why,
			)
		}
	}
	ok, why := s.spawnOK(now)
	if ok {
		t.Error("spawned past the budget")
	}
	if why != budgetSpent {
		t.Errorf("why = %q, want %q", why, budgetSpent)
	}
	if s.Degraded() != budgetSpent {
		t.Errorf(
			"the pane does not say the budget is spent: %q",
			s.Degraded(),
		)
	}
	next := now.Add(time.Minute)
	if ok, why := s.spawnOK(next); !ok {
		t.Errorf("still refused after the minute turned: %s", why)
	}
	if s.Degraded() != "" {
		t.Errorf("degraded outlived the window: %q", s.Degraded())
	}
}

// The count that mattered during the incident: a source cannot be made to
// spawn without limit no matter how often it is asked.
func TestSpawnBudgetHoldsUnderAFlood(t *testing.T) {
	s := newSource("host")
	now := time.Now()
	allowed := 0
	for i := 0; i < 1000; i++ {
		if ok, _ := s.spawnOK(now); ok {
			allowed++
		}
	}
	if allowed != spawnBudget {
		t.Errorf(
			"a thousand attempts produced %d spawns; the budget "+
				"is %d",
			allowed,
			spawnBudget,
		)
	}
}

// "after 1 failures" on screen is how a reader learns to distrust the rest
// of the row.
func TestPluralReadsCorrectly(t *testing.T) {
	for _, c := range []struct {
		n    int
		want string
	}{
		{1, "1 failure"},
		{0, "0 failures"},
		{2, "2 failures"},
		{11, "11 failures"},
	} {
		if got := plural(c.n, "failure"); got != c.want {
			t.Errorf("plural(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

// the gate has to reopen. A gate that closes and never opens wedges the
// pane for the life of the program, which is worse than the overlap it was
// added to prevent -- so this is the test that matters most in the file.
func TestGateReopensAfterAReply(t *testing.T) {
	s := newSource("deploys")
	now := time.Now()
	for round := 1; round <= 5; round++ {
		ok, why := s.begin(now)
		if !ok {
			t.Fatalf(
				"round %d refused after %d clean replies: %s",
				round,
				round-1,
				why,
			)
		}
		if ok, _ := s.begin(now); ok {
			t.Fatalf("round %d started twice", round)
		}
		s.done(now, nil) // the reply
	}
}

// a source that has never been asked is not outstanding: a pane added later
// must not inherit a closed gate from the zero value.
func TestFreshSourceIsNotOutstanding(t *testing.T) {
	if ok, why := newSource("new").begin(time.Now()); !ok {
		t.Errorf("a fresh source refused its first round: %s", why)
	}
}

// Item 1 of the review, as a test rather than a promise: the path the
// running program actually takes has to reach backoff. closeRounds is that
// path, and an earlier cut of it set inFlight directly, which left rule one
// implemented and never executed.
func TestCloseRoundsReachesBackoff(t *testing.T) {
	p := &fakePane{title: "host"}
	s := paneSource(p)
	s.done(time.Now(), nil) // start clean regardless of other tests
	if ok, _ := s.begin(time.Now()); !ok {
		t.Fatal("could not begin")
	}
	// a reply carrying a warning is a failed round
	closeRounds(
		hostFetched{
			h: HostStats{Warnings: []string{"colima: not running"}},
		},
		p,
	)
	if s.failures == 0 {
		t.Error(
			"a warning-bearing reply did not count as a failure; " +
				"backoff is inert",
		)
	}
	if s.Degraded() == "" {
		t.Error("a failed round left the pane saying nothing")
	}
	if ok, _ := s.begin(time.Now()); ok {
		t.Error(
			"began again immediately after a failure; notBefore " +
				"was not advanced",
		)
	}
}

// and a clean reply clears it, or a source that once failed never refreshes
// again.
func TestCloseRoundsClearsOnAGoodReply(t *testing.T) {
	p := &fakePane{title: "deploys"}
	s := paneSource(p)
	s.begin(time.Now())
	closeRounds(hostFetched{h: HostStats{Warnings: []string{"boom"}}}, p)
	s.notBefore = time.Time{} // step past the backoff without sleeping
	s.begin(time.Now())
	closeRounds(hostFetched{h: HostStats{}}, p)
	if s.failures != 0 {
		t.Errorf(
			"failures = %d after a clean reply, want 0",
			s.failures,
		)
	}
	if d := s.Degraded(); d != "" {
		t.Errorf("a clean reply left a stale reason: %q", d)
	}
}

// an unknown message must not put every pane into backoff: a reply type
// added later without touching roundOutcome would otherwise stop the panes
// that use it, silently.
func TestUnknownReplyCountsAsSuccess(t *testing.T) {
	if err := roundOutcome(struct{}{}); err != nil {
		t.Errorf("an unrecognised message counted as failure: %v", err)
	}
}

// ITEM 2: the budget has a real call site now. The sampler's own spawns go
// through it, so the thing that measures runaways cannot become one.
func TestSelfSourceSpawnsAreBudgeted(t *testing.T) {
	s := newSource("vitals")
	now := time.Now()
	allowed := 0
	for i := 0; i < spawnBudget+5; i++ {
		if ok, _ := s.spawnOK(now); ok {
			allowed++
		}
	}
	if allowed != spawnBudget {
		t.Errorf(
			"%d spawns allowed, budget is %d",
			allowed,
			spawnBudget,
		)
	}
	if _, err := s.spawn("true"); err == nil {
		t.Error("spawn past the budget was permitted")
	}
}

// a pane stub, so the gate can be tested on the path main.go uses rather
// than only on a bare source.
type fakePane struct{ title string }

// The stub implements pane so the gate can be tested on the path main.go
// takes rather than only on a bare source.
func (f *fakePane) Title() string { return f.title }

// Unused by these tests; the interface requires it.
func (f *fakePane) Key() string { return "f" }

// Loading nothing is the point: these tests drive the gate, not a fetch.
func (f *fakePane) Load(string) tea.Cmd { return nil }

// Unused by these tests; the interface requires it.
func (f *fakePane) Update(tea.Msg) (pane, tea.Cmd) { return f, nil }

// Unused by these tests; the interface requires it.
func (f *fakePane) View(Styles, int, int) string { return "" }

// Unused by these tests; the interface requires it.
func (f *fakePane) Help() string { return "" }
