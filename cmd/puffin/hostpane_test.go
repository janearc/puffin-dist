package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// errFake stands in for whatever colima failed with.
var errFake = errors.New("colima exited 1")

// The host pane is where the estate's two most expensive failures are
// visible before they happen: the disk both clusters share filling up, and a
// VM that is not busy but stuck. Both were learned the hard way, and both
// are claims the pane has to make in words.

// hostFixture is a pane already holding a measurement, so the tests
// exercise the arm state and the drawing rather than the probes.
func hostFixture() *hostPane {
	return &hostPane{h: HostStats{
		Hostname: "kestrel",
		Uptime:   "9:32",
		Load:     [3]float64{1.32, 1.77, 1.72},
		Disks: []DiskFree{
			{
				Path:       "/",
				FreeBytes:  200 << 30,
				TotalBytes: 500 << 30,
			},
			{
				Path:       "/src",
				FreeBytes:  300 << 30,
				TotalBytes: 500 << 30,
			},
		},
		VMs: []VM{{
			Name:        "default",
			Status:      "Running",
			Arch:        "aarch64",
			CPUs:        4,
			MemoryBytes: 16 << 30,
			DiskBytes:   200 << 30,
			Runtime:     "docker",
			Uptime:      "3 days", Load: [3]float64{1.1, 1.0, 0.9},
			MemUsedMB: 8000, MemTotalMB: 16000,
			ImagesGB:      12.5,
			VolumesGB:     3,
			BuildCacheGB:  8,
			ReclaimableGB: 14,
			DiskUsedGB:    100, DiskTotalGB: 200,
		}},
		Clusters: []Cluster{
			{Name: "local", ServersRunning: 1, ServersCount: 1},
			{Name: "fleet", ServersRunning: 1, ServersCount: 1},
		},
	}}
}

// What the linux inside says about itself: the host's own numbers are not
// the VM's, and the VM is where the clusters actually run.
func TestHostPaneShowsWhatTheVMSaysAboutItself(t *testing.T) {
	out := hostFixture().View(Compile(DodoDark()), 160, 50)
	if !strings.Contains(out, "inside: up") ||
		!strings.Contains(out, "3 days") {
		t.Error("the VM's own uptime is not shown")
	}
	if !strings.Contains(out, "mem 8000/16000 MB") {
		t.Error("the VM's memory is not shown")
	}
	if !strings.Contains(out, "docker: 12.5G images") ||
		!strings.Contains(out, "14.0G reclaimable") {
		t.Error("where the VM's disk went is not shown")
	}
	if !strings.Contains(out, "disk 100/200G") {
		t.Error("the VM's disk usage is not shown")
	}
}

// A VM that cannot be reached from outside says so rather than rendering as
// a VM with no load.
func TestHostPaneSaysWhenTheInsideDidNotAnswer(t *testing.T) {
	p := hostFixture()
	p.h.VMs[0].Uptime = ""
	p.h.VMs[0].InsideError = "ssh: connection refused"
	if out := p.View(Compile(DodoDark()), 160, 50); !strings.Contains(
		out,
		"inside: no answer",
	) {
		t.Error(
			"a VM that did not answer was drawn as one with " +
				"nothing to say",
		)
	}
}

// BUSY and stuck want opposite responses, and load alone cannot tell them
// apart. This estate lost both clusters to the second kind while the load
// average said only "36" and invited a reading about cpu.
func TestBusyIsNotTheSameAsStuck(t *testing.T) {
	stuck := VM{CPUs: 4, Load: [3]float64{36, 30, 20}, Blocked: 13}
	note, busy := busyKind(stuck)
	if !busy {
		t.Fatal("a load of 36 on 4 cpu was not flagged at all")
	}
	if !strings.Contains(note, "WAITING ON DISK") ||
		!strings.Contains(note, "13 processes blocked") {
		t.Fatalf("a stuck VM was not named as stuck: %q", note)
	}

	saturated := VM{CPUs: 4, Load: [3]float64{12, 10, 8}, Blocked: 0}
	note, busy = busyKind(saturated)
	if !busy {
		t.Fatal("a saturated VM was not flagged")
	}
	if !strings.Contains(note, "cpu saturated") ||
		!strings.Contains(note, "nothing blocked") {
		t.Fatalf("a busy VM was named wrongly: %q", note)
	}

	// and an ordinary load says nothing at all
	if _, busy := busyKind(VM{CPUs: 4, Load: [3]float64{1.1, 1, 1}}); busy {
		t.Error("an idle VM was flagged as busy")
	}
	// a VM reporting no cpu count is still measured against something
	if _, busy := busyKind(VM{CPUs: 0, Load: [3]float64{4, 4, 4}}); !busy {
		t.Error(
			"a VM with an unknown cpu count escaped the check " +
				"entirely",
		)
	}

	// the note reaches the screen
	p := hostFixture()
	p.h.VMs[0].Load, p.h.VMs[0].Blocked = [3]float64{36, 30, 20}, 13
	if out := p.View(Compile(DodoDark()), 160, 50); !strings.Contains(
		out,
		"WAITING ON DISK",
	) {
		t.Error("the stuck note did not reach the pane")
	}
}

// The VM's disk is the disk BOTH clusters run on, so filling it is an outage
// with a delay on it.
func TestVMDiskPressureIsNamedAsShared(t *testing.T) {
	tight := VM{DiskUsedGB: 190, DiskTotalGB: 200, ReclaimableGB: 14}
	note, hot := diskNote(tight)
	if !hot {
		t.Fatal("a 95% full VM disk was not flagged")
	}
	if !strings.Contains(note, "BOTH clusters") {
		t.Errorf("the note does not say who else is affected: %q", note)
	}
	if !strings.Contains(note, "docker system prune") {
		t.Errorf("the note does not say what to do: %q", note)
	}
	// a comfortable disk says nothing, and an unknown one does not guess
	if _, hot := diskNote(VM{DiskUsedGB: 10, DiskTotalGB: 200}); hot {
		t.Error("a mostly empty disk was flagged")
	}
	if _, hot := diskNote(VM{}); hot {
		t.Error("a VM with no disk figures was flagged anyway")
	}
	// reclaimable space is only mentioned when there is some
	note, _ = diskNote(VM{DiskUsedGB: 190, DiskTotalGB: 200})
	if strings.Contains(note, "reclaimable") {
		t.Errorf(
			"nothing to reclaim, but the note offered it: %q",
			note,
		)
	}
}

// Empty is a state, and a different one from "not installed".
func TestHostPaneEmptyStatesAndWarnings(t *testing.T) {
	p := &hostPane{h: HostStats{Hostname: "kestrel",
		Warnings: []string{"colima: executable file not found"}}}
	out := p.View(Compile(DodoDark()), 140, 44)
	if !strings.Contains(out, "executable file not found") {
		t.Error(
			"a warning was hidden, so a missing tool looked like " +
				"an empty machine",
		)
	}
	if !strings.Contains(out, "no profiles") {
		t.Error("no VMs was not stated")
	}
	if !strings.Contains(out, "none") {
		t.Error("no clusters was not stated")
	}
	// a hostname that never came back is a dash, not a blank
	p.h.Hostname = ""
	if out := p.View(Compile(DodoDark()), 140, 44); !strings.Contains(
		out,
		"-",
	) {
		t.Error("an unknown hostname rendered as nothing at all")
	}
}

// A degraded cluster is not an up cluster.
func TestHostPaneMarksDegradedClusters(t *testing.T) {
	p := hostFixture()
	p.h.Clusters = []Cluster{
		{Name: "local", ServersRunning: 1, ServersCount: 1,
			AgentsRunning: 0, AgentsCount: 2},
	}
	out := p.View(Compile(DodoDark()), 140, 44)
	if !strings.Contains(out, "degraded") {
		t.Error("a cluster missing its agents was drawn as up")
	}
	if !strings.Contains(out, "agents 0/2") {
		t.Error("the pane did not say what was missing")
	}
}

// The gate refuses what it cannot do honestly, and says why, rather than
// arming an action that would do nothing.
func TestHostPaneArmRefusals(t *testing.T) {
	// something already up cannot be started
	p := &hostPane{
		h: HostStats{VMs: []VM{{Name: "default", Status: "Running"}}},
	}
	p.Update(key('s'))
	if p.arm.live() {
		t.Fatal("the pane armed a start for something already running")
	}
	if !strings.Contains(p.note, "already up") {
		t.Fatalf("note %q", p.note)
	}
	if !strings.Contains(
		p.View(Compile(DodoDark()), 140, 44),
		"already up",
	) {
		t.Error("the refusal is not on screen")
	}

	// one thing at a time
	p = &hostPane{
		running: true,
		h: HostStats{
			VMs: []VM{{Name: "default", Status: "Stopped"}},
		},
	}
	p.Update(key('s'))
	if p.arm.live() ||
		!strings.Contains(p.note, "already running something") {
		t.Fatalf(
			"a second start was armed while one was running: %q",
			p.note,
		)
	}

	// and with nothing selectable at all, s does nothing rather than panic
	p = &hostPane{}
	p.Update(key('s'))
	if p.arm.live() {
		t.Fatal("the pane armed with nothing to act on")
	}
}

// An armed action expires. A gate that never expires is a keystroke waiting
// to be pressed by accident an hour later.
func TestHostPaneArmExpires(t *testing.T) {
	p := &hostPane{
		h: HostStats{VMs: []VM{{Name: "default", Status: "Stopped"}}},
	}
	p.Update(key('s'))
	if !p.arm.live() {
		t.Fatal("nothing armed")
	}
	p.arm.at = time.Now().Add(-armWindow - time.Second)
	if p.arm.live() {
		t.Fatal("an armed action outlived its window")
	}
	// and the expired arm is not shown as live on screen
	if strings.Contains(
		p.View(Compile(DodoDark()), 140, 44),
		"press s again",
	) {
		t.Error("the pane still offered an expired confirmation")
	}
}

// colima's own words, in the little screen, so a slow start looks slow
// rather than frozen.
func TestHostPaneStreamsTheSubstrateOutput(t *testing.T) {
	p := hostFixture()
	p.running = true
	p.Update(substrateOut{line: "INFO[0000] starting colima"})
	np, cmd := p.Update(substrateOut{line: "INFO[0001] provisioning ..."})
	if cmd == nil {
		t.Fatal("the pane stopped reading the stream")
	}
	p = np.(*hostPane)
	out := p.View(Compile(DodoDark()), 140, 44)
	if !strings.Contains(out, "provisioning") {
		t.Error("what colima said is not on screen")
	}
	if !strings.Contains(out, "running") {
		t.Error("the box does not say it is still going")
	}

	// the buffer is bounded: a long start must not grow without limit
	for i := 0; i < 300; i++ {
		p.Update(substrateOut{line: "line"})
	}
	if len(p.out) > 200 {
		t.Fatalf("the output buffer grew to %d lines", len(p.out))
	}

	// done stops it, says how it ended, and re-reads the world rather than
	// believing the screen drawn before it changed
	np, cmd = p.Update(substrateOut{done: true})
	p = np.(*hostPane)
	if p.running {
		t.Error("the pane still claims to be running after done")
	}
	if p.note != "done" {
		t.Errorf("note %q", p.note)
	}
	if cmd == nil {
		t.Error("the pane did not re-read after the world changed")
	}
	// and a failure says so instead
	p.running = true
	np, _ = p.Update(substrateOut{done: true, err: errFake})
	if !strings.Contains(np.(*hostPane).note, "failed: ") {
		t.Errorf(
			"a failed start was not reported: %q",
			np.(*hostPane).note,
		)
	}
}

// A read that comes back with fewer things must not leave the cursor past
// the end of the list.
func TestHostPaneFetchClampsCursor(t *testing.T) {
	p := hostFixture()
	p.cursor = 2
	p.Update(hostFetched{HostStats{Hostname: "kestrel"}})
	if p.cursor != 0 {
		t.Fatalf("cursor %d after a read with nothing in it", p.cursor)
	}
}

// j and k walk everything startable -- VMs and clusters in one list -- and
// disarm on the way, because an armed action must not survive a move.
func TestHostPaneMovesAcrossVMsAndClusters(t *testing.T) {
	p := hostFixture()
	if n := len(p.startables()); n != 3 {
		t.Fatalf("%d startables, want 1 VM and 2 clusters", n)
	}
	p.Update(key('s'))
	p.Update(key('j'))
	if p.arm.live() {
		t.Error("moving left the action armed")
	}
	if p.cursor != 1 {
		t.Fatalf("j: %d", p.cursor)
	}
	// the command named is the one for what is now selected
	if got := p.command("local"); got != "k3d cluster start local" {
		t.Fatalf("command %q", got)
	}
	p.Update(key('j'))
	p.Update(key('j'))
	if p.cursor != 2 {
		t.Fatalf("j past the end: %d", p.cursor)
	}
	p.Update(key('k'))
	p.Update(key('k'))
	p.Update(key('k'))
	if p.cursor != 0 {
		t.Fatalf("k past the start: %d", p.cursor)
	}
	if got := p.command("default"); got != "colima start --profile "+
		"default" {
		t.Fatalf("command %q", got)
	}
}

// Nothing may render outside the terminal, at any size.
func TestHostPaneDrawsInATinyWindow(t *testing.T) {
	p := hostFixture()
	for _, wh := range [][2]int{{40, 6}, {20, 0}, {200, 60}} {
		if out := p.View(Compile(DodoDark()), wh[0], wh[1]); out == "" {
			t.Errorf("%dx%d drew nothing", wh[0], wh[1])
		}
	}
}
