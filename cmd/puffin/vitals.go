package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// puffin reports on nineteen services and says nothing about itself.
//
// On 2026-09-11 the host went to load 77 for eight minutes and came back.
// puffin was sampled at 71% cpu while it was happening, but only once, by hand,
// after the fact -- so what we have of the event is node_load1 at a two-minute
// scrape and one `ps` reading somebody happened to take.
//
// Every account of what puffin was doing is a reconstruction, including three
// of mine that were wrong.
//
// This is the smallest thing that would have answered it: puffin writing
// down what puffin was doing, once a minute, while it runs.
//
// It measures itself and nothing else. Not the cluster, not the host's other
// processes -- a tool that reports on its neighbours during an incident it may
// have caused is the least trustworthy witness available. Own cpu, own memory,
// own goroutines, own children, and the host's load for context.
//
// It costs one sample a minute. getrusage and a Getppid scan are syscalls,
// not processes; this must never be a thing that shows up in the storm it is
// trying to describe.

// vitalsEvery is the sampling interval. A minute matches the granularity of
// load1 and is far below the eight minutes the one event we have lasted, so
// a repeat lands eight rows rather than one.
const vitalsEvery = time.Minute

// vitalsPath is where the record goes. Under runtime/static because it has
// to survive the sweep that clears dynamic: an incident record deleted on a
// weekly cycle is no record at all.
func vitalsPath() string {
	return filepath.Join(
		os.Getenv("HOME"),
		"mesh",
		"runtime",
		"static",
		"puffin-vitals.log",
	)
}

// vitals is one sample: what this process was doing at one moment.
type vitals struct {
	At time.Time
	// cumulative, so a delta between rows is the burn
	CPUUser   time.Duration
	CPUSys    time.Duration
	RSS       uint64 // bytes, high-water, from the runtime rather than ps
	Goroutine int
	Children  int
	Load1     float64
	Pane      string
}

// sampleVitals reads this process's own counters.
//
// Cumulative CPU rather than a percentage: a percentage is already an average
// over a window somebody else chose, and the window is the thing that misled us
// last time -- macOS ps reports a decaying one-minute average, which reads like
// a lifetime figure and is not.
//
// Two rows and a subtraction give an honest interval.
func sampleVitals(pane string) vitals {
	v := vitals{
		At:        time.Now(),
		Goroutine: runtime.NumGoroutine(),
		Pane:      pane,
	}

	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err == nil {
		v.CPUUser = time.Duration(
			ru.Utime.Sec,
		)*time.Second + time.Duration(
			ru.Utime.Usec,
		)*time.Microsecond
		v.CPUSys = time.Duration(
			ru.Stime.Sec,
		)*time.Second + time.Duration(
			ru.Stime.Usec,
		)*time.Microsecond
	}

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	v.RSS = ms.Sys

	v.Children = countChildren()
	v.Load1 = load1()
	return v
}

// countChildren counts processes whose parent is us. The fork-storm question
// -- "is puffin spawning" -- is exactly this number, and it was the one
// nobody could answer during the event. One `ps`, parsed; cheaper than
// pgrep and it cannot itself fan out.
func countChildren() int {
	// where the kernel will just tell us, ask it. A sampler that forks to
	// find out whether it is forking has answered its own question wrong,
	// and on a host with /proc no process is needed at all.
	if n, ok := childrenFromProc(os.Getpid()); ok {
		return n
	}
	// otherwise one ps, through the budget like every other spawn. The
	// sampler is the last thing that should be exempt from the rule it
	// exists to measure: a telemetry loop that cannot be capped is a
	// telemetry loop that can become the incident.
	out, err := selfSource.spawn("ps", "-Ao", "ppid=")
	if err != nil {
		// -1, never 0: "could not count" and "counted none" are
		// different answers
		return -1
	}
	return countPPIDs(
		string(out),
		os.Getpid(),
	) - 1 // discount the ps we just ran
}

// countPPIDs counts how many of the parent pids in ps output are ours.
// Split out from countChildren so the parsing can be tested without a host
// that happens to have children at the moment the test runs.
func countPPIDs(out string, pid int) int {
	me := strconv.Itoa(pid)
	n := 0
	for _, f := range strings.Fields(out) {
		if f == me {
			n++
		}
	}
	return n
}

// childrenFromProc counts our direct children out of /proc, and says
// whether it could. Every thread has its own children file, so all of them
// are read: a child forked from a goroutine on another thread is still our
// child, and reading only the main thread's would report zero for it.
//
// The second return separates "no /proc" from "no children", which is the
// same distinction countChildren's -1 exists for.
func childrenFromProc(pid int) (int, bool) {
	tasks, err := os.ReadDir("/proc/" + strconv.Itoa(pid) + "/task")
	if err != nil {
		return 0, false
	}
	n, read := 0, false
	for _, t := range tasks {
		b, err := os.ReadFile(
			"/proc/" + strconv.Itoa(
				pid,
			) + "/task/" + t.Name() + "/children",
		)
		if err != nil {
			continue
		}
		read = true
		n += len(strings.Fields(string(b)))
	}
	return n, read
}

// load1 is the host's one-minute load, for context only. A row that says
// puffin was quiet while the host was at 70 is as useful as the opposite,
// and without it a quiet row cannot be told from a quiet host.
func load1() float64 {
	// /proc first, for the same reason as the child count: it is a file
	// read rather than a process, and the cheapest measurement is the one
	// that cannot become the thing being measured.
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		return firstFloat(string(b))
	}
	out, err := selfSource.spawn("sysctl", "-n", "vm.loadavg")
	if err != nil {
		return -1
	}
	return firstFloat(string(out))
}

// firstFloat reads the leading number out of a load average line, in
// either spelling: /proc/loadavg is three bare numbers, and sysctl wraps
// the same three in braces. -1 when there is no number there, because a
// missing reading and a load of zero are different facts.
func firstFloat(s string) float64 {
	f := strings.Fields(strings.Trim(s, "{} \n"))
	if len(f) == 0 {
		return -1
	}
	l, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return -1
	}
	return l
}

// String is the on-disk row: one json object per line, which is what the
// estate's logging standard says and what vector reads.
//
// An earlier cut wrote a hand-formatted line and argued in a comment that a
// person with awk is the reader. That was an exception recorded nowhere a
// reader of the standard would find it, which is how a standard stops being
// one. jq reads this as well as awk read the other.
func (v vitals) String() string {
	b, err := json.Marshal(struct {
		At         string  `json:"at"`
		Level      string  `json:"level"`
		Msg        string  `json:"msg"`
		CPUUser    float64 `json:"cpu_user_s"`
		CPUSys     float64 `json:"cpu_sys_s"`
		RSSMB      uint64  `json:"rss_mb"`
		Goroutines int     `json:"goroutines"`
		Children   int     `json:"children"`
		Load1      float64 `json:"load1"`
		Pane       string  `json:"pane"`
	}{
		At:         v.At.Format(time.RFC3339),
		Level:      "info",
		Msg:        "puffin vitals",
		CPUUser:    round2(v.CPUUser.Seconds()),
		CPUSys:     round2(v.CPUSys.Seconds()),
		RSSMB:      v.RSS / (1 << 20),
		Goroutines: v.Goroutine,
		Children:   v.Children,
		Load1:      v.Load1,
		Pane:       v.Pane,
	})
	if err != nil {
		// a sampler that cannot describe itself still says when it
		// could not
		return fmt.Sprintf(
			`{"level":"warn","msg":"puffin vitals unmarshalable",`+
				`"at":%q}`,
			v.At.Format(time.RFC3339),
		)
	}
	return string(b)
}

// round2 keeps the seconds readable. Cumulative CPU to the microsecond is
// noise in a row somebody subtracts two of.
func round2(f float64) float64 { return math.Round(f*100) / 100 }

// selfSource is the sampler's own budget. Named rather than anonymous so a
// degraded sampler shows up as "vitals" in whatever reads sources, not as a
// nameless one nobody can place.
var selfSource = newSource("vitals")

// writeVitals appends one row. Failure is silent by design: a telemetry
// write that interrupts the program it measures has inverted its purpose,
// and a missing row is already visible as a gap in the timestamps.
func writeVitals(v vitals) {
	f, err := os.OpenFile(
		vitalsPath(),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY,
		0o644,
	)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, v.String())
}

// startVitals samples until the program ends. The goroutine is deliberately
// unstoppable: it holds no resource worth reclaiming, and an incident that
// kills the sampler before the process is exactly the incident nobody gets
// to read about afterwards.
func startVitals(nameOf func() string) {
	go func() {
		for {
			pane := ""
			if nameOf != nil {
				pane = nameOf()
			}
			writeVitals(sampleVitals(pane))
			time.Sleep(vitalsEvery)
		}
	}()
}

// notePane records which pane has the screen, for the sampler to stamp on its
// next row.
//
// A plain mutex-guarded string rather than threading state through the model:
// the model is a value passed by bubbletea and there are three places a pane
// becomes the open one, so the alternative was four signature changes to carry
// one label.
var paneNow struct {
	mu   sync.Mutex
	name string
}

// notePane records which pane is open, so a sample can say what puffin
// was showing when it was taken.
func notePane(name string) {
	paneNow.mu.Lock()
	paneNow.name = name
	paneNow.mu.Unlock()
}

// openPaneName is what the sampler asks. Empty means the roster, which is
// the screen with no pane open rather than a missing answer.
func openPaneName() string {
	paneNow.mu.Lock()
	defer paneNow.mu.Unlock()
	if paneNow.name == "" {
		return "roster"
	}
	return paneNow.name
}
