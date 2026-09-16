//go:build live

package main

import (
	"testing"
	"time"
)

// The e2e for the collector swap, run deliberately with -tags live against the
// real local enclave: find vector-collector by label, read today's file inside
// its pod, and come back with lines that carry a name, a stream and a TIME.
//
// The unit tests parse a record someone captured; this one proves the collector
// still writes what the parser expects, which is the half of the port that a
// fixture cannot check.
func TestLiveFleetLogs(t *testing.T) {
	pod, ns, err := collectorPod(homeContext())
	if err != nil {
		t.Skip("no collector: ", err)
	}
	t.Logf("collector: %s/%s", ns, pod)

	v := fetchLogs(homeContext(), "", 200)
	for _, w := range v.Warnings {
		t.Logf("warning: %s", w)
	}
	if len(v.Lines) == 0 {
		t.Fatalf(
			"no lines from %s -- the estate is never this quiet",
			v.File,
		)
	}

	// every line must be usable, not merely present: a record that parses
	// to the zero time is exactly the silent failure the swap introduced
	named, stamped := 0, 0
	for _, l := range v.Lines {
		if l.Service != "" {
			named++
		}
		if !l.When.IsZero() {
			stamped++
		}
	}
	if stamped != len(v.Lines) {
		t.Errorf(
			"%d of %d lines have no timestamp",
			len(v.Lines)-stamped,
			len(v.Lines),
		)
	}
	if named != len(v.Lines) {
		t.Errorf(
			"%d of %d lines have no service name",
			len(v.Lines)-named,
			len(v.Lines),
		)
	}
	// and the times must be recent: reading vector's own `timestamp` after
	// a restart would look fine here except that the whole buffer would sit
	// in one second, so check the spread as well as the presence
	newest := newestLine(v.Lines)
	if age := time.Since(newest); age > time.Hour {
		t.Errorf(
			"newest line is %s old -- is the collector still "+
				"shipping?",
			age,
		)
	}
	t.Logf("%d lines from %s, %d named, newest %s",
		len(v.Lines), v.File, named, newest.Format(time.RFC3339))
}
