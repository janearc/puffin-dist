package main

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// A cost figure is dated, because the record it comes from is not.
//
// A client can stop reporting telemetry with no notice at all, and then
// the last figure written just stands there.
//
// The harness writes a cost-state at checkpoints and at exit, never per turn,
// and the record carries no timestamp. So puffin dates it by position -- the
// newest timestamped record at or before it -- and says so when the figure has
// fallen behind the work it claims to price.
//
// One seen here was thirteen hours behind on a session that had done half its
// turns since.
func TestCostSaysWhenItIsStale(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		name  string
		sess  AgentSession
		stale bool
	}{
		{"thirteen hours behind", AgentSession{
			CostKnown: true, CostUSD: 384.28,
			CostAt:    now.Add(-13 * time.Hour),
			LastWrite: now}, true},
		{"checkpointed a minute ago", AgentSession{
			CostKnown: true, CostUSD: 1.50,
			CostAt: now.Add(-time.Minute), LastWrite: now}, false},
		{"written at exit, so it is the last word", AgentSession{
			CostKnown: true, CostUSD: 0.08,
			CostAt: now, LastWrite: now}, false},
		{"no cost at all is not a stale cost", AgentSession{
			CostKnown: false, LastWrite: now}, false},
		{"a cost we could not date is not claimed stale", AgentSession{
			CostKnown: true, CostUSD: 5, LastWrite: now}, false},
	} {
		if got := c.sess.CostStale(); got != c.stale {
			t.Errorf(
				"%s: CostStale()=%v want %v",
				c.name,
				got,
				c.stale,
			)
		}
	}
}

// The mark has to reach the screen, or the fact is known and not said.
func TestStaleCostIsMarkedInBothSurfaces(t *testing.T) {
	now := time.Now()
	stale := AgentSession{ID: "a", Project: "dev/puffin", CostKnown: true,
		CostUSD: 384.28, CostAt: now.Add(
			-13 * time.Hour,
		), LastWrite: now}
	fresh := stale
	fresh.CostAt = now

	s := Compile(Corvid())
	if got := ansi.Strip(costCell(s, stale, false)); !strings.Contains(
		got,
		"~$384.28",
	) {
		t.Errorf(
			"the pane drew a thirteen-hour-old figure as "+
				"current: %q",
			got,
		)
	}
	if got := ansi.Strip(costCell(s, fresh, false)); strings.Contains(
		got,
		"~",
	) {
		t.Errorf("a current figure was marked stale: %q", got)
	}
}

// The dating happens in the parser, and the first version of these tests
// did not reach it: they built an AgentSession by hand with CostAt already
// set, so removing the line that stamps it changed nothing and the mutation
// passed. This one reads a transcript.
func TestCostIsDatedByPosition(t *testing.T) {
	dir := t.TempDir()
	// a cost-state carries no timestamp of its own, so it takes the newest
	// one at or before it -- 10:00 here, not the 22:00 that follows
	lines := []string{
		`{"type":"user","cwd":"/tmp/x",` +
			`"timestamp":"2026-09-01T09:00:00Z",` +
			`"promptSource":"typed"}`,
		`{"type":"assistant","timestamp":"2026-09-01T10:00:00Z",` +
			`"message":{"model":"claude-opus-5",` +
			`"usage":{"output_tokens":1}}}`,
		`{"type":"cost-state","totalCostUSD":384.28,` +
			`"totalLinesAdded":0,"totalLinesRemoved":0}`,
		`{"type":"assistant","timestamp":"2026-09-01T22:00:00Z",` +
			`"message":{"model":"claude-opus-5",` +
			`"usage":{"output_tokens":1}}}`,
	}
	p := writeTranscript(t, dir, "dated", lines...)

	s, err := readTranscript(p)
	if err != nil {
		t.Fatal(err)
	}
	if !s.CostKnown {
		t.Fatal("the cost was not read at all")
	}
	want := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	if !s.CostAt.Equal(want) {
		t.Errorf(
			"CostAt = %v, want %v -- the figure must be dated by "+
				"the newest "+
				"record at or before it, not by the end of "+
				"the file",
			s.CostAt,
			want,
		)
	}
	if s.CostAt.IsZero() {
		t.Error(
			"the cost was never dated; a figure that cannot be " +
				"dated cannot be called stale",
		)
	}
}

// Cost is per RUN. A resumed session starts a fresh counter.
//
// lights-dev read $384.28 at 3,684 turns and $115.29 at 7,137 -- the cost
// appearing to FALL while the work doubled, because puffin took the last
// cost-state and a reboot had started a new series. Its real spend was the
// sum of both runs, $499.56.
func TestCostSumsEveryRun(t *testing.T) {
	dir := t.TempDir()
	p := writeTranscript(
		t,
		dir,
		"resumed",
		`{"type":"assistant","timestamp":"2026-08-30T12:00:00Z",`+
			`"message":{"model":"claude-opus-5",`+
			`"usage":{"output_tokens":1}}}`,
		`{"type":"cost-state","startTime":1788117533848,`+
			`"totalCostUSD":384.28,"totalLinesAdded":100,`+
			`"totalLinesRemoved":10}`,
		`{"type":"assistant","timestamp":"2026-08-31T15:00:00Z",`+
			`"message":{"model":"claude-opus-5",`+
			`"usage":{"output_tokens":1}}}`,
		`{"type":"cost-state","startTime":1788215489554,`+
			`"totalCostUSD":115.29,"totalLinesAdded":50,`+
			`"totalLinesRemoved":5}`,
	)
	s, err := readTranscript(p)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := s.CostUSD, 384.28+115.29; got < want-0.01 ||
		got > want+0.01 {
		t.Errorf(
			"CostUSD = %.2f, want %.2f -- the second run's "+
				"counter replaced the first "+
				"instead of adding to it",
			got,
			want,
		)
	}
	if s.CostRuns != 2 {
		t.Errorf("CostRuns = %d, want 2", s.CostRuns)
	}
	if s.LinesAdded != 150 || s.LinesRemoved != 15 {
		t.Errorf(
			"lines = +%d/-%d, want +150/-15",
			s.LinesAdded,
			s.LinesRemoved,
		)
	}
}

// Within a run, take the highest figure rather than the last one.
//
// A provider that resets a counter overnight breaks last-wins, which
// assumes a counter only climbs. It is not ours and it is free to
// start over, so the rule is never to report less than something already
// seen.
func TestCostNeverGoesDownWithinARun(t *testing.T) {
	dir := t.TempDir()
	p := writeTranscript(
		t,
		dir,
		"reset",
		`{"type":"assistant","timestamp":"2026-09-01T10:00:00Z",`+
			`"message":{"model":"claude-opus-5",`+
			`"usage":{"output_tokens":1}}}`,
		`{"type":"cost-state","startTime":1,"totalCostUSD":100.00,`+
			`"totalLinesAdded":0,"totalLinesRemoved":0}`,
		`{"type":"cost-state","startTime":1,"totalCostUSD":12.00,`+
			`"totalLinesAdded":0,"totalLinesRemoved":0}`,
	)
	s, err := readTranscript(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.CostUSD < 99.99 {
		t.Errorf(
			"CostUSD = %.2f, want at least 100.00 -- a counter "+
				"that reset mid-run "+
				"must not lose what was already reported",
			s.CostUSD,
		)
	}
}

// A session that has never been resumed reads exactly as before.
func TestSingleRunIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	p := writeTranscript(
		t,
		dir,
		"one",
		`{"type":"assistant","timestamp":"2026-09-01T10:00:00Z",`+
			`"message":{"model":"claude-opus-5",`+
			`"usage":{"output_tokens":1}}}`,
		`{"type":"cost-state","startTime":7,"totalCostUSD":12.34,`+
			`"totalLinesAdded":3,"totalLinesRemoved":1}`,
	)
	s, _ := readTranscript(p)
	if got := s.CostUSD; got < 12.33 || got > 12.35 {
		t.Errorf("CostUSD = %.2f, want 12.34", got)
	}
	if s.CostRuns != 1 {
		t.Errorf("CostRuns = %d, want 1", s.CostRuns)
	}
}
