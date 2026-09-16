package main

import (
	"strings"
	"testing"
	"time"
)

// The headless path had no tests at all -- 0% covered -- which was not an
// oversight so much as a consequence: the newness rules were buried inside a
// function that shells out to kubectl, and code that mixes I/O with a
// decision cannot be checked without the I/O.

// The pattern is positional and may start with a dash: /-\d+/ is a
// perfectly reasonable thing to wait for, which is why the flags are parsed
// by hand rather than handed to the flag package.
func TestParseWatchFlags(t *testing.T) {
	rest, once, quiet, interval := parseWatchFlags(
		[]string{
			"--once",
			"connection",
			"refused",
			"--interval",
			"30s",
			"--quiet",
		},
	)
	if !once || !quiet {
		t.Fatalf("once=%v quiet=%v", once, quiet)
	}
	if interval != 30*time.Second {
		t.Fatalf("interval: %v", interval)
	}
	if strings.Join(rest, " ") != "connection refused" {
		t.Fatalf("pattern: %q", rest)
	}
	// a pattern that starts with a dash survives
	rest, _, _, _ = parseWatchFlags([]string{"/-\\d+/"})
	if len(rest) != 1 || rest[0] != "/-\\d+/" {
		t.Fatalf("a dashed pattern was eaten: %q", rest)
	}
	// a bad duration is ignored rather than fatal: the default is right
	_, _, _, bad := parseWatchFlags([]string{"--interval", "later", "x"})
	if bad != 0 {
		t.Fatalf("a nonsense interval was accepted: %v", bad)
	}
}

// The cadence belongs to what is being watched, not to the watcher.
func TestDefaultIntervalPerSource(t *testing.T) {
	if defaultInterval("agents") >= defaultInterval("bus") {
		t.Fatal("agents should be checked more often than the bus")
	}
	if defaultInterval("logs") == 0 || defaultInterval("nonsense") == 0 {
		t.Fatal(
			"every source needs a cadence, including an unknown " +
				"one",
		)
	}
}

// Logs: a high-water mark. The first look reports nothing, or starting a
// watch would immediately report the thousand lines already in the buffer --
// which makes --once fire instantly and the notification meaningless.
func TestHeadlessLogNewness(t *testing.T) {
	w := &Watch{Pattern: "migration failed"}
	w.compile()
	seen := newSeenSet()
	base := time.Now().Add(-time.Hour)
	lines := []LogLine{
		{
			When:    base,
			Service: "postgres",
			Text:    "migration failed at step 3",
		},
		{
			When:    base.Add(time.Second),
			Service: "postgres",
			Text:    "quiet",
		},
	}
	if hits := newLogHits(lines, w, seen); len(hits) != 0 {
		t.Fatalf("the first look reported %d hits", len(hits))
	}
	seen.first = false
	// an older line is not new
	if hits := newLogHits(lines, w, seen); len(hits) != 0 {
		t.Fatalf("old lines reported as new: %v", hits)
	}
	// a genuinely new match is
	lines = append(lines, LogLine{
		When: base.Add(
			time.Minute,
		), Service: "postgres", Text: "migration failed at step 9"})
	hits := newLogHits(lines, w, seen)
	if len(hits) != 1 || !strings.Contains(hits[0], "step 9") {
		t.Fatalf("new match not reported: %v", hits)
	}
	// and a new line that does not match is not a hit
	lines = append(lines, LogLine{
		When: base.Add(
			2 * time.Minute,
		), Service: "postgres", Text: "all fine"})
	if hits := newLogHits(lines, w, seen); len(hits) != 0 {
		t.Fatalf("a non-matching line was reported: %v", hits)
	}
}

// The bus has no cursor, so a record is identified by its idempotency key:
// the tail is re-read every interval and a heartbeat must not fire twice.
func TestHeadlessBusNewness(t *testing.T) {
	w := &Watch{Pattern: "hm"}
	w.compile()
	seen := newSeenSet()
	msgs := []BusMessage{
		{Schema: "observability.v1.ServiceHealthHeartbeat",
			Fields: map[string]string{
				"service_name":    "hm",
				"idempotency_key": "hb-1",
			},
			Order: []string{"service_name", "idempotency_key"}},
	}
	if hits := newBusHits("observability.events", msgs, w, seen); len(
		hits,
	) != 0 {
		t.Fatal("the first look announced a record")
	}
	seen.first = false
	// the same record read again is not new
	if hits := newBusHits("observability.events", msgs, w, seen); len(
		hits,
	) != 0 {
		t.Fatalf("a re-read record fired again: %v", hits)
	}
	// a different record is
	msgs = append(msgs, BusMessage{
		Schema: "observability.v1.ServiceHealthHeartbeat",
		Fields: map[string]string{
			"service_name":    "hm",
			"idempotency_key": "hb-2",
		},
		Order: []string{"service_name", "idempotency_key"}})
	if hits := newBusHits("observability.events", msgs, w, seen); len(
		hits,
	) != 1 {
		t.Fatalf("a new record was not reported: %v", hits)
	}
	// and the same key on a different topic is a different record
	if hits := newBusHits("flipr.oplog", msgs, w, seen); len(hits) != 2 {
		t.Fatalf("topic is not part of the identity: %v", hits)
	}
}

// Agents: a state change. "waiting" is true every interval; "has just
// started waiting" is true once.
func TestHeadlessAgentNewness(t *testing.T) {
	w := &Watch{Pattern: "waiting"}
	w.compile()
	seen := newSeenSet()
	now := time.Now()
	working := []AgentSession{
		{ID: "a", Project: "puffin", Model: "claude-opus-5",
			LastKind: "tool", LastWrite: now},
	}
	if hits := newAgentHits(working, w, seen, now); len(hits) != 0 {
		t.Fatal("the first look announced a state")
	}
	seen.first = false
	// still working: nothing
	if hits := newAgentHits(working, w, seen, now); len(hits) != 0 {
		t.Fatalf("an unchanged state fired: %v", hits)
	}
	// it finishes its turn
	waiting := []AgentSession{
		{ID: "a", Project: "puffin", Model: "claude-opus-5",
			LastKind: "text", LastWrite: now.Add(-3 * time.Minute)},
	}
	hits := newAgentHits(waiting, w, seen, now)
	if len(hits) != 1 || !strings.Contains(hits[0], "waiting") {
		t.Fatalf("the transition was not reported: %v", hits)
	}
	// and it stays waiting without firing again
	if hits := newAgentHits(waiting, w, seen, now); len(hits) != 0 {
		t.Fatalf("a held state fired again: %v", hits)
	}
}

// A source puffin does not know is an error, not silence.
func TestHeadlessUnknownSource(t *testing.T) {
	w := &Watch{Pattern: "x"}
	w.compile()
	_, err := watchOnce(
		"nonsense",
		"",
		"test",
		homeContext(),
		w,
		newSeenSet(),
	)
	if err == nil {
		t.Fatal("an unknown source was accepted")
	}
	if !strings.Contains(err.Error(), "logs") {
		t.Fatalf("the error does not name the sources: %v", err)
	}
}

// The usage message is examples, not a grammar: one people can copy is one
// they do not have to parse.
func TestWatchUsageIsCopyable(t *testing.T) {
	for _, want := range []string{
		"puffin watch logs", "puffin watch bus", "puffin watch agents",
		"--once", "--interval", "--quiet", "/slashes/",
	} {
		if !strings.Contains(watchUsage, want) {
			t.Errorf("usage does not mention %q", want)
		}
	}
	if !strings.Contains(watchUsage, "examples:") {
		t.Error("usage has no examples")
	}
}
