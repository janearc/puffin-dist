package main

import (
	"os"
	"strings"
	"testing"
)

// TestCatchCrashLeavesEvidence is the regression test for a crash that left
// none. It asserts the two things that were missing: the trace reaches a
// file, and the panic still propagates rather than being swallowed.
func TestCatchCrashLeavesEvidence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	reraised := func() (r any) {
		defer func() { r = recover() }()
		defer catchCrash()
		panic("the corner bird fell over")
	}()

	if reraised == nil {
		t.Fatal(
			"catchCrash swallowed the panic; a quiet crash is " +
				"worse than a loud one",
		)
	}

	b, err := os.ReadFile(crashLogPath())
	if err != nil {
		t.Fatalf("no crash log written: %v", err)
	}
	for _, want := range []string{"puffin crash", "the corner bird fell " +
		"over", "catchCrash"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("crash log missing %q\ngot:\n%s", want, b)
		}
	}
}

// TestCatchCrashAppends keeps the first report when a second arrives.
func TestCatchCrashAppends(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, msg := range []string{"first", "second"} {
		func() {
			defer func() { recover() }()
			defer catchCrash()
			panic(msg)
		}()
	}
	b, _ := os.ReadFile(crashLogPath())
	if !strings.Contains(string(b), "first") ||
		!strings.Contains(string(b), "second") {
		t.Errorf("second crash overwrote the first:\n%s", b)
	}
}
