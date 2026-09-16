package main

import (
	"os"
	"strings"
	"sync"
	"time"
)

// Whether to report cost at all.
//
// 2026-09-02: the client stopped emitting telemetry.
//
// The cost-state records stopped updating, the metrics topics went dry, and
// puffin's figures froze at whatever was last written -- one session reported
// $986.43 while the last record puffin could see said $499.56, which is not an
// error in the arithmetic but the end of the input.
//
// The code stays, behind a flag, for the day the numbers come back.
//
// So the reading stays and the showing is gated. Deleting it would mean
// rewriting it from scratch on the day the numbers return, and the code is
// correct -- it was fixed twice on 2026-09-02, once for staleness and once
// for per-run summing.
//
// THE FLAG IS BORN ON. A flag that ships off is a feature nobody remembers
// exists, and the staleness marker already tells the truth about what the
// numbers are worth. Turn it off when the frozen figures become noise.

// costFlagKey is the flag, in puffin's own flipr namespace.
const (
	costFlagService = "puffin"
	costFlagKey     = "cost.enabled"
)

// costFlag is read once and cached for a minute. puffin draws sixty times a
// second and flipr is not a thing to ask on every frame; a minute is short
// enough that turning it off is felt and long enough that nobody notices
// the cost of asking.
var costFlag = struct {
	sync.Mutex
	on   bool
	when time.Time
	// held records that the last read failed and this answer is stale.
	// Holding is defensible; holding silently is not, and the first
	// version did the second.
	held  bool
	since time.Time
	why   string
}{on: true}

// costEnabled says whether cost should be shown.
//
// Flipr's charter is that a service without its flags is down, by design, and
// that rule binds the thing doing WORK.
//
// This binary reports the outage loudly and does not make it an error, because
// puffin is how you would look at flipr, and a diagnostic tool that refuses to
// start during an outage is unavailable in the only situation it was built for.
//
// A daemon that collects and acts inherits the strict reading instead:
// doing either without knowing its flags is exactly the state the charter
// forbids. The exemption is for the thing that only looks.
//
// So this holds its last answer and records that it is holding. The first
// version held in silence, which is the actual defect regardless of which
// policy wins: flipr being down and puffin not saying so is wrong under
// either reading.
//
// PUFFIN_COST=0 turns it off with no flipr at all, which is the
// pre-onboarding path and is not a normal mode.
func costEnabled(domain string) bool {
	if v := os.Getenv("PUFFIN_COST"); v != "" {
		return v != "0" && !strings.EqualFold(v, "false")
	}
	costFlag.Lock()
	defer costFlag.Unlock()
	if time.Since(costFlag.when) < time.Minute {
		return costFlag.on
	}
	costFlag.when = time.Now()
	rows, err := latestFlags(domain)
	if err != nil {
		// hold, and say so. Flipr being unreachable is not an
		// instruction, but it is not nothing either.
		if !costFlag.held {
			costFlag.held, costFlag.since, costFlag.why =
				true, time.Now(), err.Error()
		}
		return costFlag.on
	}
	costFlag.held, costFlag.why = false, ""
	for _, r := range rows {
		if r.Service == costFlagService && r.Key == costFlagKey {
			costFlag.on = r.Value != "false" && r.Value != "0"
			return costFlag.on
		}
	}
	// flipr answered and has never heard of the flag: born ON, because a
	// feature that ships off is a feature nobody remembers exists
	costFlag.on = true
	return costFlag.on
}

// FliprHeld reports that puffin is running on remembered flags, since when,
// and why. Empty when the last read succeeded.
//
// This exists so a surface can SAY it rather than a comment claiming it is
// fine. Under flipr's charter running without flags is a degraded state,
// and a degraded state that looks identical to a healthy one is the failure
// this repository keeps finding.
func FliprHeld() (bool, time.Duration, string) {
	costFlag.Lock()
	defer costFlag.Unlock()
	if !costFlag.held {
		return false, 0, ""
	}
	return true, time.Since(costFlag.since), costFlag.why
}
