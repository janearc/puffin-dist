package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// escSeq strips escape sequences, so tests can assert the highlighter changed
// presentation and nothing else.
var escSeq = regexp.MustCompile("\x1b\\[[0-9;]*m")

// TestHighlightPreservesContent is the highlighter's one hard law: stripped
// of color, the output is byte-identical to the input. A highlighter that
// edits content is a liar with good taste.
func TestHighlightPreservesContent(t *testing.T) {
	for _, src := range []string{
		"{\n  \"key\": \"value\",\n  \"n\": -12.5e3,\n  \"on\": " +
			"true,\n  \"off\": false,\n  \"nil\": null\n}",
		`{"nested":{"deep":[1,2,{"x":"y"}]}}`,
		`{"escaped":"a \"quote\" and a colon: inside"}`,
		"404 page not found", // non-JSON must pass through whole
		// truncated JSON must not panic or eat bytes
		`{"trailing`,
	} {
		got := escSeq.ReplaceAllString(
			HighlightJSON(src, Compile(DodoDark())),
			"",
		)
		if got != src {
			t.Errorf("content changed:\n in: %q\nout: %q", src, got)
		}
	}
}

// TestHighlightColorsByJob spot-checks the token jobs: a key and a value of
// identical text must wear different colors.
func TestHighlightColorsByJob(t *testing.T) {
	// tests run without a tty, where lipgloss correctly strips color; force
	// a profile so the assertion is about the highlighter, not the pipe
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(old)
	out := HighlightJSON(`{"same": "same"}`, Compile(DodoDark()))
	if !strings.Contains(out, "\x1b[") {
		t.Fatal("no color emitted at all")
	}
	// the two "same" tokens must not be styled identically: split on the
	// colon and compare the styled halves
	halves := strings.SplitN(out, ":", 2)
	if len(halves) == 2 &&
		halves[0] == strings.TrimSuffix(
			strings.TrimSpace(halves[1]),
			"}",
		) {
		t.Error(
			"key and value styled identically; the job " +
				"distinction is lost",
		)
	}
}
