package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// a sample has to carry the two numbers the incident wanted and could not
// get: whether puffin was spawning, and what the host was doing at the time.
func TestSampleVitalsCarriesTheIncidentFields(t *testing.T) {
	v := sampleVitals("agents")
	if v.CPUUser <= 0 {
		t.Errorf(
			"cpu_user is %v; a running process has used some "+
				"user time",
			v.CPUUser,
		)
	}
	if v.Goroutine < 1 {
		t.Errorf("goroutines is %d", v.Goroutine)
	}
	if v.Children < 0 {
		t.Errorf(
			"children is %d; -1 means the count failed",
			v.Children,
		)
	}
	if v.Load1 < 0 {
		t.Errorf("load1 is %v; -1 means the read failed", v.Load1)
	}
	if v.Pane != "agents" {
		t.Errorf("pane is %q, want the one it was given", v.Pane)
	}
}

// the row is json, one object per line, per the estate's logging standard.
// every field it promises has to be present and the line has to parse.
func TestVitalsRowIsOneJSONObject(t *testing.T) {
	row := sampleVitals("logs").String()
	if strings.Count(row, "\n") != 0 {
		t.Fatalf(
			"row spans lines; vector reads one object per "+
				"line:\n  %s",
			row,
		)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(row), &got); err != nil {
		t.Fatalf("row is not json: %v\n  %s", err, row)
	}
	for _, want := range []string{
		"at",
		"level",
		"msg",
		"cpu_user_s",
		"cpu_sys_s",
		"rss_mb",
		"goroutines",
		"children",
		"load1",
		"pane",
	} {
		if _, ok := got[want]; !ok {
			t.Errorf("row is missing %q:\n  %s", want, row)
		}
	}
	if got["pane"] != "logs" {
		t.Errorf("pane = %v, want the one it was given", got["pane"])
	}
}

// an unwritable path must not take the program down with it: telemetry that
// interrupts what it measures has inverted its purpose.
func TestWriteVitalsSurvivesAnUnwritablePath(t *testing.T) {
	t.Setenv("HOME", filepath.Join(t.TempDir(), "nonexistent"))
	writeVitals(sampleVitals("roster")) // must not panic
}

// the row lands on disk, appended, one line per sample.
func TestWriteVitalsAppends(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(
		filepath.Join(home, "mesh", "runtime", "static"),
		0o755,
	); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	writeVitals(sampleVitals("a"))
	writeVitals(sampleVitals("b"))
	b, err := os.ReadFile(vitalsPath())
	if err != nil {
		t.Fatal(err)
	}
	if n := len(strings.Split(strings.TrimSpace(string(b)), "\n")); n != 2 {
		t.Errorf("wrote %d rows, want 2:\n%s", n, b)
	}
}

// no pane open is the roster, not an empty field: a blank would read as a
// failed lookup rather than as the screen she was actually on.
func TestOpenPaneNameDefaultsToRoster(t *testing.T) {
	notePane("")
	if got := openPaneName(); got != "roster" {
		t.Errorf("openPaneName() = %q, want roster", got)
	}
	notePane("deploys")
	if got := openPaneName(); got != "deploys" {
		t.Errorf("openPaneName() = %q, want deploys", got)
	}
	notePane("")
}

// The two readings that differ by platform are parsed by their own
// functions, so the shapes can be checked without a host that happens to
// have children or load at the moment the test runs.
func TestFirstFloatReadsBothLoadSpellings(t *testing.T) {
	for in, want := range map[string]float64{
		"1.94 2.01 2.11 1/512 9931\n": 1.94, // /proc/loadavg
		"{ 1.94 2.01 2.11 }":          1.94, // sysctl, already trimmed
		"":                            -1,
		"nonsense":                    -1,
	} {
		if got := firstFloat(in); got != want {
			t.Errorf("firstFloat(%q) = %v, want %v", in, got, want)
		}
	}
}

// -1 is "could not read" and 0 is "read, and there were none". A parser
// that collapses them is the one that made the incident unanswerable.
func TestCountPPIDsCountsOnlyOurs(t *testing.T) {
	out := " 1\n 4243\n 4243\n 900\n"
	if got := countPPIDs(out, 4243); got != 2 {
		t.Errorf("counted %d of our own, want 2", got)
	}
	if got := countPPIDs(out, 7); got != 0 {
		t.Errorf("counted %d for a pid with no children, want 0", got)
	}
}

// On a host with /proc the child count must come from the kernel and not
// from a process, because a sampler that forks to find out whether it is
// forking has answered its own question wrong. Skipped where there is no
// /proc, which is where the ps fallback is the only answer.
func TestChildrenComeFromProcWhereThereIsOne(t *testing.T) {
	if _, err := os.Stat("/proc/self/task"); err != nil {
		t.Skip(
			"no /proc on this host; the ps fallback is the path " +
				"here",
		)
	}
	if _, ok := childrenFromProc(os.Getpid()); !ok {
		t.Error(
			"/proc is present and the count still fell through " +
				"to ps",
		)
	}
}
