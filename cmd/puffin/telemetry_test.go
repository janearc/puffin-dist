package main

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// The flag is born ON. A feature that ships off is a feature nobody
// remembers exists, and the staleness marker already tells the truth about
// what a frozen number is worth.
func TestCostIsOnByDefault(t *testing.T) {
	t.Setenv("PUFFIN_COST", "")
	costFlag.Lock()
	costFlag.on = true
	costFlag.Unlock()
	if !costEnabled("test") {
		t.Error("cost reporting defaults to off")
	}
}

// The environment turns it off with no flipr at all: the pre-onboarding
// path, and the one that works when the mesh is down.
func TestEnvTurnsCostOff(t *testing.T) {
	t.Setenv("PUFFIN_COST", "0")
	if costEnabled("test") {
		t.Error("PUFFIN_COST=0 did not turn it off")
	}
	t.Setenv("PUFFIN_COST", "false")
	if costEnabled("test") {
		t.Error("PUFFIN_COST=false did not turn it off")
	}
	t.Setenv("PUFFIN_COST", "1")
	if !costEnabled("test") {
		t.Error("PUFFIN_COST=1 did not turn it on")
	}
}

// Off means the column says nothing rather than repeating a frozen figure.
func TestCostCellIsSilentWhenOff(t *testing.T) {
	t.Setenv("PUFFIN_COST", "0")
	got := costCell(
		Compile(Corvid()),
		AgentSession{CostKnown: true, CostUSD: 499.56},
		false,
	)
	if strings.Contains(ansi.Strip(got), "499") {
		t.Errorf(
			"a disabled cost column still printed a figure: %q",
			got,
		)
	}
}

// Holding is defensible. Holding silently is not.
//
// Flipr's charter is that a service without its flags is down. This binary
// reports the outage loudly rather than refusing -- puffin is how you would
// look at flipr, and a diagnostic that will not start during an outage is
// unavailable in the one situation it exists for.
//
// The price of that exemption is saying so.
func TestHeldFlagsAreReported(t *testing.T) {
	costFlag.Lock()
	costFlag.held, costFlag.since, costFlag.why = false, time.Time{}, ""
	costFlag.Unlock()
	if held, _, _ := FliprHeld(); held {
		t.Fatal("reported held before anything failed")
	}

	costFlag.Lock()
	costFlag.held, costFlag.since, costFlag.why = true, time.Now().
		Add(-2*time.Hour),
		"dial tcp: refused"
	costFlag.Unlock()
	t.Cleanup(func() {
		costFlag.Lock()
		costFlag.held, costFlag.why = false, ""
		costFlag.Unlock()
	})

	held, since, why := FliprHeld()
	if !held {
		t.Fatal("a failed read was not reported as held")
	}
	if since < time.Hour {
		t.Errorf(
			"duration = %s; how long it has been down is the "+
				"useful half",
			since,
		)
	}
	if !strings.Contains(why, "refused") {
		t.Errorf("why = %q, want the underlying error", why)
	}
}

// The front page must say it, not merely know it.
func TestRosterSaysFlagsAreHeld(t *testing.T) {
	costFlag.Lock()
	costFlag.held, costFlag.since, costFlag.why = true, time.Now().
		Add(-time.Hour),
		"connection refused"
	costFlag.Unlock()
	t.Cleanup(func() {
		costFlag.Lock()
		costFlag.held, costFlag.why = false, ""
		costFlag.Unlock()
	})

	m := newModel("test", "")
	m.width, m.height, m.loading = 162, 44, false
	out := ansi.Strip(rosterView(m))
	if !strings.Contains(out, "flipr unreachable") {
		t.Errorf(
			"the roster does not say flipr is unreachable:\n%s",
			out,
		)
	}
	if !strings.Contains(out, "remembered flags") {
		t.Errorf(
			"the roster does not say the flags are remembered:\n%s",
			out,
		)
	}
}

// The one that actually exercises the read.
//
// TestHeldFlags sets costFlag by hand and therefore proves nothing about the
// code that sets it -- removing the recording entirely left it green, which is
// how this was found. This drives costEnabled against a domain that cannot
// resolve and asserts the failure is recorded, not merely survived.
func TestAFailedReadIsRecorded(t *testing.T) {
	t.Setenv("PUFFIN_COST", "")
	costFlag.Lock()
	costFlag.on, costFlag.held, costFlag.why = true, false, ""
	costFlag.when = time.Time{} // defeat the one-minute cache
	costFlag.Unlock()
	t.Cleanup(func() {
		costFlag.Lock()
		costFlag.held, costFlag.why, costFlag.when =
			false, "", time.Time{}
		costFlag.Unlock()
	})

	// nothing answers here, so latestFlags must fail
	_ = costEnabled("invalid.nonexistent.puffin-test")

	held, _, why := FliprHeld()
	if !held {
		t.Fatal(
			"a failed flag read was not recorded; puffin would " +
				"run on " +
				"remembered flags with a clean front page",
		)
	}
	if why == "" {
		t.Error(
			"recorded as held with no reason; the underlying " +
				"error is the useful half",
		)
	}
}
