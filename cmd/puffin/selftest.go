package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// puffin selftest: the tool that watches the estate, watching itself.
//
// `go test ./...` prints nothing for eleven seconds and then a word. That is
// fine for a machine and poor for a person, who is sitting there wondering
// whether it is working or hung -- the same complaint that made the bus pane
// say what it is doing while it does it.
//
// So this drives `go test -json` and renders it: which package, which test,
// how many so far, and every failure in full the moment it happens rather
// than at the end. --quiet collapses the whole thing to one line and an exit
// code, for anything that is not a person.
//
// It needs the source, which the installed binary does not carry. It finds
// the module by walking up from the working directory, and says so plainly
// when it cannot rather than reporting a test failure that is really a
// missing checkout.

// testEvent is one line of `go test -json`.
type testEvent struct {
	// run | pass | fail | skip | output | build-output | build-fail | start
	Action  string  `json:"Action"`
	Package string  `json:"Package"`
	Test    string  `json:"Test"`
	Output  string  `json:"Output"`
	Elapsed float64 `json:"Elapsed"`
}

// cliSelftest runs the suite.
func cliSelftest(args []string) int {
	// --quiet is the operator asking for one line; a pipe is the same
	// request made by circumstance, and both are honoured.
	//
	// They differ in one way: an explicitly quiet run says nothing about
	// failures beyond the count, while a piped one still prints them,
	// because something is going to read that file.
	quiet, live, cover := false, false, false
	for _, a := range args {
		switch a {
		case "--quiet", "-q":
			quiet = true
		case "--live":
			live = true
		case "--cover":
			cover = true
		}
	}
	dir, err := moduleRoot()
	if err != nil {
		fmt.Fprintln(
			os.Stderr,
			"puffin selftest needs puffin's source, and this is "+
				"not it:",
		)
		fmt.Fprintln(os.Stderr, "  "+err.Error())
		fmt.Fprintln(
			os.Stderr,
			"run it from a checkout -- the installed binary does "+
				"not carry its own tests",
		)
		return 2
	}

	cmdArgs := []string{"test", "-json", "-count=1"}
	// coverage is measured by the same run rather than by a second one:
	// two runs can disagree, and the number that matters is the one from
	// the tests that just passed.
	profile := ""
	if cover {
		f, err := os.CreateTemp("", "puffin-cover-*.out")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		profile = f.Name()
		f.Close()
		defer os.Remove(profile)
		cmdArgs = append(cmdArgs, "-coverprofile="+profile)
	}
	if live {
		// the live suites talk to the real cluster. Opt-in, always: a
		// selftest that quietly starts poking a cluster is not a
		// selftest.
		cmdArgs = append(cmdArgs, "-tags", "live")
	}
	cmdArgs = append(cmdArgs, "./...")

	cmd := exec.Command("go", cmdArgs...)
	cmd.Dir = dir
	out, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	// stderr is captured rather than passed through.
	//
	// `go test -json` puts the compiler's actual diagnostics there while
	// the json stream only says "[build failed]"
	//
	// -- so passing it through printed the useful half in the middle of a
	// progress line, and swallowing it lost the only sentence that says
	// which file and which line.
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	started := time.Now()
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	r := newRun(quiet)
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var e testEvent
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		r.handle(e)
	}
	waitErr := cmd.Wait()
	for _, l := range strings.Split(
		strings.TrimSpace(errBuf.String()),
		"\n",
	) {
		if l != "" {
			r.pkgOut = append(r.pkgOut, l)
		}
	}
	code := r.report(time.Since(started), waitErr == nil, dir)
	if profile != "" && waitErr == nil {
		// the floor is a gate, not a remark. Coverage was asked for, so
		// a run that comes in under it fails -- otherwise `make
		// release` prints "below the floor" and ships anyway, which is
		// what it did for every release before this one.
		if !reportCoverage(dir, profile, quiet) && code == 0 {
			code = 1
		}
	}
	return code
}

// run accumulates what the suite has done so far.
type run struct {
	quiet   bool
	passed  int
	failed  int
	skipped int
	// output is kept per test and thrown away when it passes: a passing
	// test's log is noise, and keeping all of it turns a test run into a
	// memory profile
	out      map[string][]string
	failures []failure
	// pkgOut is what the package said with no test attached: build errors,
	// vet complaints, a panic outside a test
	pkgOut   []string
	lastDraw time.Time
	current  string
}

// isNoise drops the lines `go test` prints when everything is fine, so what
// is left is what went wrong.
func isNoise(l string) bool {
	switch {
	case strings.HasPrefix(l, "ok  "), strings.HasPrefix(l, "?   "),
		strings.HasPrefix(l, "PASS"), strings.HasPrefix(l, "=== RUN"),
		strings.HasPrefix(l, "--- "+
			"PASS"), strings.HasPrefix(l, "coverage:"):
		return true
	}
	return false
}

type failure struct {
	name string
	pkg  string
	body []string
}

// newRun sets up the progress line, or leaves it off entirely: piping the
// output somewhere means the redraws are noise in a file.
func newRun(quiet bool) *run {
	// a progress line that repaints in place needs a terminal to repaint
	// in. Piped into a file or a pager it is not progress, it is a hundred
	// copies of a counter with escape codes between them -- which is
	// exactly what it produced the first time it was run through a pipe.
	return &run{
		quiet: quiet || !stdoutIsTerminal(),
		out:   map[string][]string{},
	}
}

// handle folds one event in, and redraws at most a few times a second --
// a progress line that repaints on every event spends more time rendering
// than the tests spend running.
func (r *run) handle(e testEvent) {
	key := e.Package + "." + e.Test
	switch e.Action {
	case "build-output", "build-fail":
		// Go emits compiler diagnostics as their OWN action, not as
		// test output, and not on stderr.
		//
		// Handling only output/pass/fail meant a tree that does not
		// compile reported "[build failed]" with the file and line
		// silently dropped -- the one sentence that tells you where to
		// look.
		if t := strings.TrimRight(e.Output, "\n"); t != "" {
			r.pkgOut = append(r.pkgOut, t)
		}
	case "output":
		if e.Test != "" {
			r.out[key] = append(
				r.out[key],
				strings.TrimRight(e.Output, "\n"),
			)
			return
		}
		// output with no test attached is the package talking, and that
		// is where a build error arrives: `go test -json` puts compiler
		// diagnostics in the stream rather than on stderr.
		//
		// Dropping it made a tree that does not compile report "0
		// passed, 0 failed" and say nothing about why -- a
		// green-looking answer to a broken build, which is the exact
		// failure this tool exists to catch elsewhere.
		if t := strings.TrimRight(e.Output, "\n"); t != "" &&
			!isNoise(t) {
			r.pkgOut = append(r.pkgOut, t)
			if len(r.pkgOut) > 200 {
				r.pkgOut = r.pkgOut[len(r.pkgOut)-200:]
			}
		}
	case "run":
		if e.Test != "" {
			r.current = e.Test
		}
	case "pass":
		if e.Test != "" {
			r.passed++
			delete(r.out, key)
		}
	case "skip":
		if e.Test != "" {
			r.skipped++
			delete(r.out, key)
		}
	case "fail":
		if e.Test == "" {
			return
		}
		r.failed++
		r.failures = append(r.failures, failure{
			name: e.Test, pkg: shortPkg(
				e.Package,
			), body: r.out[key]})
		delete(r.out, key)
		// a failure is printed the moment it happens rather than saved
		// for the end: the whole reason to watch a test run is to be
		// able to stop it early
		r.clear()
		fmt.Printf("FAIL  %s\n", e.Test)
		for _, l := range r.failures[len(r.failures)-1].body {
			if t := strings.TrimSpace(l); t != "" &&
				!strings.HasPrefix(t, "=== RUN") {
				fmt.Println("      " + t)
			}
		}
	}
	if !r.quiet && time.Since(r.lastDraw) > 80*time.Millisecond {
		r.draw()
		r.lastDraw = time.Now()
	}
}

// draw paints the progress line in place.
func (r *run) draw() {
	name := r.current
	if len(name) > 44 {
		name = name[:43] + "…"
	}
	fmt.Printf("\r\033[2K  %d passed  %d failed  %d skipped   %s",
		r.passed, r.failed, r.skipped, name)
}

// clear takes the progress line back off the screen before the result is
// printed, so the answer is not written over a spinner.
func (r *run) clear() {
	if !r.quiet {
		fmt.Print("\r\033[2K")
	}
}

// report prints the ending. In quiet mode that is one line, because the
// consumer is a script and a script reads the exit code.
func (r *run) report(took time.Duration, ok bool, dir string) int {
	if !r.quiet {
		r.clear()
	}
	// a run that did not finish cleanly explains itself in every mode. The
	// quiet form is for scripts, and a script's human reads the log after
	// it goes red -- printing only a count there is how a build error
	// becomes ten minutes of confusion.
	if !ok && r.failed == 0 {
		fmt.Printf(
			"%d passed, %d failed, %d skipped in %s\n",
			r.passed,
			r.failed,
			r.skipped,
			took.Round(time.Millisecond),
		)
		fmt.Println(
			"the suite did not finish, and no individual test " +
				"failed.",
		)
		fmt.Println(
			"that is a build error, a vet failure, or a panic " +
				"outside a test:",
		)
		for _, l := range r.pkgOut {
			fmt.Println("  " + l)
		}
		if len(r.pkgOut) == 0 {
			fmt.Println(
				"  (go test said nothing useful; try `go " +
					"build ./...` in " + dir + ")",
			)
		}
		return 1
	}
	if r.quiet {
		fmt.Printf(
			"%d passed, %d failed, %d skipped in %s\n",
			r.passed,
			r.failed,
			r.skipped,
			took.Round(time.Millisecond),
		)
		if ok && r.failed == 0 {
			return 0
		}
		return 1
	}
	sort.Slice(
		r.failures,
		func(
			i,
			j int,
		) bool {
			return r.failures[i].name < r.failures[j].name
		},
	)
	if r.failed > 0 {
		fmt.Println()
		fmt.Printf("%d failed:\n", r.failed)
		for _, f := range r.failures {
			fmt.Printf("  %s  (%s)\n", f.name, f.pkg)
		}
	}
	fmt.Printf("\n%d passed", r.passed)
	if r.skipped > 0 {
		fmt.Printf(", %d skipped", r.skipped)
	}
	if r.failed > 0 {
		fmt.Printf(", %d FAILED", r.failed)
	}
	fmt.Printf(" in %s\n", took.Round(time.Millisecond))
	if r.failed > 0 {
		return 1
	}
	return 0
}

// shortPkg trims the module prefix off a package path, which is the same
// for every line and so carries nothing.
func shortPkg(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// moduleName is what puffin's own go.mod says. Checked rather than
// assumed, so a selftest started inside somebody else's checkout refuses
// rather than reporting their result as puffin's.
const moduleName = "github.com/janearc/puffin-dist"

// moduleRoot walks up looking for puffin's own go.mod. Walking up rather
// than assuming the working directory means it works from a subdirectory,
// and checking the module name rather than just finding a go.mod means it
// refuses to run somebody else's tests and call the result puffin's.
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		gomod := filepath.Join(dir, "go.mod")
		if b, err := os.ReadFile(gomod); err == nil {
			if strings.Contains(string(b), "module "+moduleName) {
				return dir, nil
			}
			return "", fmt.Errorf(
				"%s is a go module, but not puffin's",
				dir,
			)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod above %s", mustGetwd())
		}
		dir = parent
	}
}

// mustGetwd returns an empty string when the working directory cannot be
// read, because a self-test that cannot find itself should say so in its
// output rather than panic before it runs.
func mustGetwd() string {
	d, _ := os.Getwd()
	return d
}

// coverFloor is the estate's floor, and it is a floor rather than a target.
//
// Goodhart is an axiom here: once a measure becomes a target it stops
// measuring. So this number is a tripwire -- it says when something has been
// left untested, and it never says the tests are good.
//
// The goalpost is DESIGN.md and the end-to-end path, and a green 90% with
// no end-to-end test is not tested.
var coverFloor = 90.0

// reportCoverage prints what the run measured, and where the gap is.
//
// The gap is the useful half. A single percentage tells you there is
// something to do and nothing about what, so this names the least-covered
// functions -- which, for a TUI, is nearly always the render path, and
// saying so out loud is better than rediscovering it every time.
func reportCoverage(dir, profile string, quiet bool) bool {
	out, err := exec.Command("go", "tool", "cover", "-func="+profile).
		Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "coverage: "+err.Error())
		return false
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 {
		return false
	}
	total := lines[len(lines)-1]
	pct := 0.0
	if i := strings.LastIndex(total, "\t"); i >= 0 {
		fmt.Sscanf(strings.TrimSpace(total[i:]), "%f%%", &pct)
	}

	met := pct >= coverFloor
	mark := "at or above"
	if pct < coverFloor {
		mark = "below"
	}
	fmt.Printf("coverage %.1f%% of statements · %s the %.0f%% floor\n",
		pct, mark, coverFloor)
	if quiet {
		return met
	}

	// the uncovered functions, biggest first by name length of file, which
	// is a proxy for nothing -- so just list the zeroes, and cap it
	var zeroes []string
	for _, l := range lines[:len(lines)-1] {
		if strings.HasSuffix(strings.TrimSpace(l), "0.0%") {
			f := strings.Fields(l)
			if len(f) >= 2 {
				// file:line:  funcname
				zeroes = append(
					zeroes,
					strings.TrimSuffix(f[1], ":"),
				)
			}
		}
	}
	if len(zeroes) == 0 {
		return met
	}
	shown := zeroes
	if len(shown) > 12 {
		shown = shown[:12]
	}
	fmt.Printf("%d function(s) with no coverage at all, including: %s\n",
		len(zeroes), strings.Join(shown, ", "))
	if len(zeroes) > len(shown) {
		fmt.Printf(
			"  puffin selftest --cover | grep 0.0%% for the rest\n",
		)
	}
	return met
}
