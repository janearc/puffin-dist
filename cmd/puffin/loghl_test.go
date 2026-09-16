package main

import (
	"regexp"
	"strings"
	"testing"
)

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

// The highlighter's one hard law, extended to logs: stripped of colour, the
// output is byte-identical to the input.
//
// A highlighter that edits content is a liar with good taste -- and chroma
// edits it twice if you let it, once by appending a newline and once through
// lipgloss padding a multi-line token out to its widest line.
func TestLogHighlightPreservesContent(t *testing.T) {
	s := Compile(DodoDark())
	for _, line := range []string{
		`Aug 30 00:26:45 gluttony sshd[1234]: Accepted publickey for ` +
			`dev from 10.0.0.4 port 22`,
		`<134>Aug 30 00:26:45 host tag: message`,
		`2026-08-30T00:26:45.416526902Z stdout F 5301 200 356`,
		`{"level":"error","msg":"connection refused","attempt":3}`,
		`traefik | 10.42.0.1:53421 - - [30/Aug/2026] "GET /health HTT` +
			`P/1.1" 200 12`,
		`plain words with no structure at all`,
		``,
		`{"broken":`,
	} {
		got := ansiRe.ReplaceAllString(HighlightLog(line, s), "")
		if got != line {
			t.Errorf(
				"content changed:\n in: %q\nout: %q",
				line,
				got,
			)
		}
	}
}

// syslog is parsed into fields, which is how the tools that do this well
// work -- chroma has no syslog lexer at all. The host is the host wherever
// it appears, and a number in the message is not a pid.
func TestSyslogIsParsedByField(t *testing.T) {
	s := Compile(DodoDark())
	line := `Aug 30 00:26:45 gluttony sshd[1234]: Accepted publickey for ` +
		`dev`
	out, ok := highlightSyslog(line, s)
	if !ok {
		t.Fatal("a plain syslog line was not recognised")
	}
	if ansiRe.ReplaceAllString(out, "") != line {
		t.Fatal("the syslog path edited content")
	}
	// the ISO form is syslog too
	if _, ok := highlightSyslog(
		`2026-08-30T00:26:45Z gluttony kernel: oom`,
		s,
	); !ok {
		t.Error("an ISO-timestamped syslog line was not recognised")
	}
	// and a line that is merely wordy is not syslog
	if _, ok := highlightSyslog(
		`this: has a colon but is not syslog`,
		s,
	); ok {
		t.Error("a non-syslog line was parsed as syslog")
	}
}

// A json payload inside a prefixed line is read as json: the estate logs
// structured, and the payload is the part worth reading properly.
func TestJSONPayloadIsFoundInsideALine(t *testing.T) {
	if got, ok := asJSON(`app | {"level":"warn","n":1}`); !ok ||
		!strings.HasPrefix(got, "{") {
		t.Fatalf("payload not found: %q %v", got, ok)
	}
	if _, ok := asJSON(`no json here { not really`); ok {
		t.Error("a brace made a non-json line look like json")
	}
}

// Earlier rules claim their text so later ones cannot repaint it: a level
// word inside a quoted string stays a string.
func TestHighlightRulesDoNotOverlap(t *testing.T) {
	s := Compile(DodoDark())
	line := `msg="error happened" count=5`
	out := HighlightLog(line, s)
	if ansiRe.ReplaceAllString(out, "") != line {
		t.Fatal("overlapping rules ate bytes")
	}
}

// Structured lines are reordered so the message survives the terminal's
// width. flipr's startup line is the case: json keys arrive alphabetically,
// so "addr" and "db" come first, "msg" sits in the middle, and truncating at
// the pane's width shows you the listen address and loses the sentence.
func TestStructuredLinePutsTheMessageFirst(t *testing.T) {
	const flipr = `{"addr":"0.0.0.0:15100","db":"/state/flipr.db",` +
		`"expensive":27,` +
		`"flags":42,"generation":"1787925500610981110-1",` +
		`"level":"info",` +
		`"msg":"flipr listening","namespaces":12,"startup_us":848276,` +
		`"ts":"2026-08-30T03:24:23.41733423Z","version":"435fd50"}`

	level, text, ok := structuredLine(flipr)
	if !ok {
		t.Fatal("a json log line was not recognised")
	}
	if level != "info" {
		t.Fatalf("level: %q", level)
	}
	if !strings.HasPrefix(text, "flipr listening") {
		t.Fatalf("the message is not first:\n%s", text)
	}
	// the timestamp is dropped: the pane has a column for it, and repeating
	// it costs the width the message needs
	if strings.Contains(text, "2026-08-30T03:24") {
		t.Fatalf("the timestamp was repeated:\n%s", text)
	}
	// and nothing else is lost
	for _, want := range []string{
		"flags=42",
		"expensive=27",
		"version=435fd50",
		"addr=0.0.0.0:15100",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("%q missing from:\n%s", want, text)
		}
	}
	// the same line renders the same way every time: the tail is sorted
	if _, again, _ := structuredLine(flipr); again != text {
		t.Fatal("the field order is not stable between renders")
	}
}

// A line that is not json is left alone rather than mangled into one.
func TestUnstructuredLinesAreUntouched(t *testing.T) {
	for _, raw := range []string{
		"2026-08-30T00:26:45Z stdout F 5301 200 356",
		"plain words",
		"",
		"{not json",
	} {
		if _, _, ok := structuredLine(raw); ok {
			t.Errorf("%q was treated as structured", raw)
		}
	}
}

// Only the levels that mean something get a colour. A line where every
// field is coloured is a line where nothing is emphasised.
func TestLevelColours(t *testing.T) {
	s := Compile(DodoDark())
	if levelStyle(s, "error").GetForeground() != s.Warn.GetForeground() {
		t.Error("error is not the warn colour")
	}
	if levelStyle(s, "WARN").GetForeground() != s.Caution.GetForeground() {
		t.Error("warn is not cautioned, or is case-sensitive")
	}
	if levelStyle(s, "debug").GetForeground() != s.Dim.GetForeground() {
		t.Error("debug is not dimmed")
	}
}

// A nested object becomes its keys rather than its contents: this is a list,
// and the whole object is one keystroke away in the pager.
func TestFlattenKeepsLinesToOneLine(t *testing.T) {
	got := flatten(map[string]any{"b": 1, "a": 2})
	if got != "{a,b}" {
		t.Fatalf("nested object: %q", got)
	}
	if got := flatten([]any{1, 2, 3}); got != "[3]" {
		t.Fatalf("array: %q", got)
	}
	if got := flatten(42.0); got != "42" {
		t.Fatalf("a whole number rendered as a float: %q", got)
	}
	if got := flatten("a\nb"); strings.Contains(got, "\n") {
		t.Fatalf("a newline survived: %q", got)
	}
}
