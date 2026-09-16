package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"
)

// A crash that leaves nothing behind is the one you cannot fix.
//
// puffin runs under tea.WithAltScreen. When it panics, bubbletea restores the
// terminal -- which leaves the alt screen -- and only THEN does the trace reach
// stderr, into the normal buffer that the shell immediately redraws over. The
// trace is written and thrown away.
//
// What the operator sees is a prompt and nothing else, which is exactly the
// report that came back the first time this happened: "no coredump, no
// nothing".
//
// For a tool that is left running for days that is the worst shape a
// failure can take, because the crash you care about is always the one you
// cannot describe afterwards.
//
// So the trace goes to a FILE before it is re-raised, and the path is printed
// on the way out. The re-raise is deliberate: swallowing a panic turns a loud
// crash into a quiet wrong answer, and this repository has spent a week on bugs
// that surfaced as something other than what they were.

// crashLogPath is beside the other state puffin keeps, rather than in a
// directory of its own -- one place to look, and it is already the place.
func crashLogPath() string {
	return filepath.Join(
		os.Getenv("HOME"),
		".local",
		"state",
		"puffin",
		"crash.log",
	)
}

// catchCrash writes the panic and its stack, says where, and re-raises.
//
// Deferred from main, which is where bubbletea's own recover re-panics to.
// Appending rather than truncating: the second crash is usually the
// informative one, and a handler that overwrites the first report is a
// handler that loses the pattern.
func catchCrash() {
	r := recover()
	if r == nil {
		return
	}
	stack := debug.Stack()
	path := crashLogPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
		if f, err := os.OpenFile(
			path,
			os.O_APPEND|os.O_CREATE|os.O_WRONLY,
			0o644,
		); err == nil {
			fmt.Fprintf(
				f,
				"\n=== puffin crash %s ===\nbuild: %s "+
					"%s\npanic: %v\n\n%s\n",
				time.Now().
					Format(time.RFC3339),
				buildVersion,
				buildSubject,
				r,
				stack,
			)
			f.Close()
		}
	}
	// stderr as well, because a person watching a foreground puffin should
	// not have to be told twice where to look. One line: the trace is in
	// the file and repeating it here is what got lost last time.
	fmt.Fprintf(
		os.Stderr,
		"puffin crashed: %v\nthe trace is in %s\n",
		r,
		path,
	)
	panic(r)
}
