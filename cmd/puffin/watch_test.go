package main

import (
	"testing"
	"time"
)

// A pattern is plain text unless it is wrapped in slashes. Regexes have no
// place in the tool's own internals -- but this pattern is the operator's,
// typed at the moment it is needed, which is what regular expressions are
// for. The plain case stays plain.
func TestPatternIsPlainUnlessAsked(t *testing.T) {
	plain := &Watch{Pattern: "connection refused"}
	plain.compile()
	if !plain.Matches("ERROR: Connection Refused from 10.0.0.1") {
		t.Fatal("plain matching is not case-insensitive")
	}
	if plain.Matches("everything is fine") {
		t.Fatal("plain matching matched nothing in particular")
	}
	// a plain pattern with regex characters is still plain: this is the
	// whole point, and "cost: $1.50 (est)" must not be a syntax error
	odd := &Watch{Pattern: "cost: $1.50 (est)"}
	odd.compile()
	if odd.bad != "" {
		t.Fatalf("a plain pattern was compiled as a regex: %s", odd.bad)
	}
	if !odd.Matches("total cost: $1.50 (est) today") {
		t.Fatal("a pattern with punctuation did not match itself")
	}

	re := &Watch{Pattern: "/error|panic/"}
	re.compile()
	if re.re == nil {
		t.Fatal("slashes did not make a regex")
	}
	if !re.Matches("a PANIC happened") || re.Matches("all good") {
		t.Fatal("the regex does not behave")
	}
	// a broken pattern says so and matches nothing, rather than matching
	// everything or crashing the pane it is running in
	bad := &Watch{Pattern: "/unclosed[/"}
	bad.compile()
	if bad.bad == "" {
		t.Fatal("an invalid regex was accepted")
	}
	if bad.Matches("anything") {
		t.Fatal("a broken watch matched")
	}
}

// Watches fire for lines that arrive, not for the buffer that was already
// there: opening the pane must not announce a thousand lines of history.
func TestWatchesOnlyFireForNewLines(t *testing.T) {
	t.Cleanup(func() { restoreWatches(nil); enableNotify(false) })
	restoreWatches([]Watch{{Pattern: "migration failed", Where: "logs"}})

	base := time.Now().Add(-time.Hour)
	p := &logsPane{}
	p.v = LogView{Lines: []LogLine{
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
	}}
	// the first look only sets the high-water mark
	p.check()
	if !p.seen.Equal(base.Add(time.Second)) {
		t.Fatalf("high-water mark not set: %v", p.seen)
	}
	// a line older than the mark is not new
	p.v.Lines = append(p.v.Lines, LogLine{
		When: base, Service: "postgres", Text: "migration failed " +
			"again"})
	before := p.seen
	p.check()
	if !p.seen.Equal(before) {
		t.Fatal("an older line moved the mark forward")
	}
	// and a genuinely new line does move it
	p.v.Lines = append(p.v.Lines, LogLine{
		When: base.Add(
			time.Minute,
		), Service: "postgres", Text: "migration failed at step 9"})
	p.check()
	if !p.seen.After(before) {
		t.Fatal("a new line did not move the mark")
	}
}

// A watch is a standing instruction and survives a restart: "tell me when
// the migration finishes" outlives the session it was typed in.
func TestWatchesArePersistedAndDeduped(t *testing.T) {
	enableUIState(t)
	t.Cleanup(func() { restoreWatches(nil) })
	restoreWatches(nil)
	addWatch("migration failed", "logs")
	addWatch(
		"migration failed",
		"logs",
	) // the same thing twice is one thing
	addWatch("/panic/", "logs")
	if got := len(watchesFor("logs")); got != 2 {
		t.Fatalf("%d watches, want 2", got)
	}
	saved := rememberedWatches()
	if len(saved) != 2 {
		t.Fatalf("%d persisted", len(saved))
	}
	// a fresh process restores them, compiled
	restoreWatches(saved)
	for _, w := range watchesFor("logs") {
		if w.Pattern == "/panic/" && w.re == nil {
			t.Fatal("a restored regex was not compiled")
		}
	}
	dropWatch("migration failed", "logs")
	if got := len(watchesFor("logs")); got != 1 {
		t.Fatalf("%d watches after dropping one", got)
	}
}

// Highlighting the LINE says "it is in here somewhere" and leaves you
// reading. Highlighting the TERM is the answer to the question you typed.
func TestMatchSpansFindTheTerm(t *testing.T) {
	plain := &Watch{Pattern: "refused"}
	plain.compile()
	line := "dial tcp: connection refused, refused again"
	spans := plain.matchSpans(line)
	if len(spans) != 2 {
		t.Fatalf("%d spans, want 2: %v", len(spans), spans)
	}
	for _, sp := range spans {
		if line[sp[0]:sp[1]] != "refused" {
			t.Fatalf("span points at %q", line[sp[0]:sp[1]])
		}
	}
	// case-insensitively, the way the search matches
	if got := plain.matchSpans("REFUSED"); len(got) != 1 {
		t.Fatalf("case-insensitive spans: %v", got)
	}

	re := &Watch{Pattern: "/conn\\w+/"}
	re.compile()
	if got := re.matchSpans(line); len(got) != 1 ||
		line[got[0][0]:got[0][1]] != "connection" {
		t.Fatalf("regex spans: %v", got)
	}

	// a zero-width match would paint nothing forever: /x*/ matches the
	// empty string at every position, and a renderer walking those spans
	// makes no progress
	zero := &Watch{Pattern: "/x*/"}
	zero.compile()
	for _, sp := range zero.matchSpans("abc") {
		if sp[1] <= sp[0] {
			t.Fatal("a zero-width span survived")
		}
	}
	// and nothing at all is not a crash
	if got := plain.matchSpans(""); got != nil {
		t.Fatalf("spans in an empty line: %v", got)
	}
	var nilWatch *Watch
	if got := nilWatch.matchSpans("anything"); got != nil {
		t.Fatal("a nil matcher produced spans")
	}
}

// Painting must not lose or duplicate content: the highlighter's law applies
// here too, since this is the same job in a different colour.
func TestPaintMatchesPreservesTheLine(t *testing.T) {
	s := Compile(DodoDark())
	w := &Watch{Pattern: "refused"}
	w.compile()
	line := "dial tcp: connection refused, refused again"
	got := ansiRe.ReplaceAllString(
		paintMatches(s, line, w.matchSpans(line), s.Row, false),
		"",
	)
	if got != line {
		t.Fatalf(
			"painting changed the line:\n in: %q\nout: %q",
			line,
			got,
		)
	}
	// with no spans it still renders the whole line
	got = ansiRe.ReplaceAllString(
		paintMatches(s, line, nil, s.Row, false),
		"",
	)
	if got != line {
		t.Fatalf("unmatched line changed: %q", got)
	}
}

// The match ground sits below the cursor's, or a search matching forty lines
// leaves you unable to find which one you are standing on.
func TestMatchGroundIsBelowTheCursor(t *testing.T) {
	for _, th := range Themes() {
		page := luminance(string(th.Bg))
		match := luminance(string(th.Match))
		cursor := luminance(string(th.Cursor))
		dm, dc := abs(match-page), abs(cursor-page)
		if dm < 0.02 {
			t.Errorf(
				"%s: a matched line is invisible (delta %.3f)",
				th.Name,
				dm,
			)
		}
		if dm >= dc {
			t.Errorf(
				"%s: the match ground (%.3f) is not below "+
					"the cursor's (%.3f)",
				th.Name,
				dm,
				dc,
			)
		}
	}
}

// abs keeps the timing comparisons readable, where the tolerance is the
// point and the sign is not.
func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
