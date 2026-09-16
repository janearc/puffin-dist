package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The host pane: what puffin is standing on.
//
// Everything here is READ-ONLY and stays that way. A colima stop with no
// profile flag stops every cluster the machine has, production included -- so
// this pane shows the VM and never offers to touch it. The one number that is
// not decoration is free disk: a floor nobody is shown is a floor nobody keeps.
//
// This reads THIS machine. When the gathering moves behind a backend the
// same fetch runs there and reports that host, so the pane names the host
// it is talking about rather than implying "here".

// DiskFree is one filesystem. Bytes, because a human-readable string is a
// rendering decision and this struct is the wire shape.
type DiskFree struct {
	Path       string
	FreeBytes  uint64
	TotalBytes uint64
}

// VM is one colima profile, as colima itself reports it -- plus what the linux
// inside it says about itself.
//
// There IS a machine in there, and it keeps its own uptime, load and memory:
// the colima VM has been up for a different length of time than the mac around
// it, and when the two disagree that is usually the answer to "why did
// everything just restart".
type VM struct {
	Name        string
	Status      string
	Arch        string
	CPUs        int
	MemoryBytes uint64
	DiskBytes   uint64
	Runtime     string
	// from inside: colima ssh runs a command in the VM
	Uptime     string
	Load       [3]float64
	MemUsedMB  int
	MemTotalMB int
	// Blocked is how many processes are in uninterruptible I/O wait, and
	// it is the number that distinguishes BUSY from stuck.
	//
	// Linux load average counts those processes as well as runnable ones,
	// so a load of 36 on four cores can mean thirteen times too much work,
	// or it can mean everything is queued behind a disk.
	//
	// Those want opposite responses and the load figure alone cannot tell
	// them apart: this estate lost BOTH clusters to the second kind --
	// repeated initdb runs saturated colima's virtual disk -- while the
	// load average said only "36" and invited a reading about cpu.
	//
	// It got one, from this tool's author, in writing.
	Blocked int
	// The VM's OWN disk, which is not the Mac's and is the one that
	// matters. Puffin reported "150 GiB free" all evening from the host's
	// filesystem while the disk both clusters actually run on sat at 69%
	// with 17G left.
	//
	// Both numbers were true; only one of them was about the thing that can
	// take the estate down.
	DiskUsedGB  float64
	DiskTotalGB float64
	// what docker is holding, and how much of it nothing is using. A build
	// system that does not manage its space leaves large files everywhere,
	// and on a shared virtual disk that is an outage with a delay on it.
	ImagesGB      float64
	VolumesGB     float64
	BuildCacheGB  float64
	ReclaimableGB float64
	InsideError   string
}

// Cluster is one k3d cluster on that VM.
type Cluster struct {
	Name           string
	ServersRunning int
	ServersCount   int
	AgentsRunning  int
	AgentsCount    int
}

// HostStats is the whole pane's data, in one struct that could be a message.
type HostStats struct {
	Hostname string
	Uptime   string
	Load     [3]float64
	Disks    []DiskFree
	VMs      []VM
	Clusters []Cluster
	Warnings []string // a source that failed says so; it never goes quiet
}

// diskFloorBytes is the estate's floor: never drop / or ~ below 50GB free.
const diskFloorBytes = 50 * 1024 * 1024 * 1024

// fetchHost reads the machine. Every source is optional and every failure is
// a warning rather than an empty screen: "colima is not installed" and "no
// VMs" are different answers, and the five-state lesson from the roster
// applies here too.
func fetchHost() HostStats {
	h := HostStats{}
	if n, err := os.Hostname(); err == nil {
		h.Hostname = n
	}
	for _, p := range []string{"/", os.Getenv("HOME")} {
		if p == "" {
			continue
		}
		var fs syscall.Statfs_t
		if err := syscall.Statfs(p, &fs); err != nil {
			h.Warnings = append(
				h.Warnings,
				fmt.Sprintf("cannot stat %s: %v", p, err),
			)
			continue
		}
		h.Disks = append(h.Disks, DiskFree{
			Path:       shortPath(p),
			FreeBytes:  fs.Bavail * uint64(fs.Bsize),
			TotalBytes: fs.Blocks * uint64(fs.Bsize),
		})
	}
	h.Disks = mergePools(h.Disks)
	if out, err := exec.Command("uptime").Output(); err == nil {
		h.Uptime, h.Load = parseUptime(string(out))
	} else {
		h.Warnings = append(h.Warnings, "uptime: "+err.Error())
	}
	vms, err := fetchVMs()
	if err != nil {
		h.Warnings = append(h.Warnings, "colima: "+err.Error())
	}
	h.VMs = vms
	cl, err := fetchClusters()
	if err != nil {
		h.Warnings = append(h.Warnings, "k3d: "+err.Error())
	}
	h.Clusters = cl
	return h
}

// fetchVMs asks colima. LIST ONLY -- this function is the entire colima
// surface puffin has, deliberately, and it does not mutate.
func fetchVMs() ([]VM, error) {
	out, err := exec.Command("colima", "list", "--json").Output()
	if err != nil {
		return nil, err
	}
	var vms []VM
	// colima emits one JSON object per line, not an array
	for _, line := range strings.Split(
		strings.TrimSpace(string(out)),
		"\n",
	) {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var p struct {
			Name    string `json:"name"`
			Status  string `json:"status"`
			Arch    string `json:"arch"`
			CPUs    int    `json:"cpus"`
			Memory  uint64 `json:"memory"`
			Disk    uint64 `json:"disk"`
			Runtime string `json:"runtime"`
		}
		if err := json.Unmarshal([]byte(line), &p); err != nil {
			return vms, fmt.Errorf("unreadable profile: %w", err)
		}
		v := VM{Name: p.Name, Status: p.Status, Arch: p.Arch,
			CPUs: p.CPUs, MemoryBytes: p.Memory,
			DiskBytes: p.Disk, Runtime: p.Runtime}
		// only a running profile can be asked; a stopped one has
		// nothing inside to answer, and asking would hang rather than
		// fail
		if strings.EqualFold(v.Status, "running") {
			askInside(&v)
		}
		vms = append(vms, v)
	}
	return vms, nil
}

// askInside runs one command in the VM and reads uptime, load and memory out
// of it. One ssh round trip rather than three: it costs about 200ms and this
// pane refreshes on a keystroke, not a timer.
//
// This is still READ-ONLY. `colima ssh -- <cmd>` is the same surface as
// `colima list` as far as the VM's lifecycle is concerned -- it starts
// nothing, stops nothing, and the command it runs reads two files.
func askInside(v *VM) {
	// three facts, one round trip: uptime, memory, and how many processes
	// are stuck in D state. The last is a count of what is waiting on a
	// device rather than on a cpu.
	//
	// The `|| true` is not decoration. grep -c exits 1 when the count is
	// zero, so on a healthy machine the whole command failed, this
	// function returned early, and uptime and memory were thrown away
	// with it.
	//
	// The pane read "up , load 0.00, mem 0/0 MB" on a VM that was
	// perfectly fine: an exit status meaning "found nothing" rather than
	// "went wrong", killing everything downstream of it.
	out, err := exec.Command(
		"colima",
		"ssh",
		"--profile",
		v.Name,
		"--",
		"sh",
		"-c",
		"uptime; free -m | sed -n 2p; ps -eo state= | grep -c '^D' "+
			"|| true; "+
			"df -k /var/lib/docker | tail -1; "+
			"docker system df --format "+
			"'{{.Type}}\t{{.Size}}\t{{.Reclaimable}}' "+
			"2>/dev/null || true",
	).Output()
	if err != nil {
		v.InsideError = err.Error()
		return
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) > 0 {
		v.Uptime, v.Load = parseUptime(lines[0])
	}
	if len(lines) > 1 {
		// "Mem: total used free shared buff/cache available"
		f := strings.Fields(lines[1])
		if len(f) >= 3 {
			v.MemTotalMB, _ = strconv.Atoi(f[1])
			v.MemUsedMB, _ = strconv.Atoi(f[2])
		}
	}
	if len(lines) > 2 {
		v.Blocked, _ = strconv.Atoi(strings.TrimSpace(lines[2]))
	}
	if len(lines) > 3 {
		// df -k: filesystem, 1k-blocks, used, available, use%, mount
		if f := strings.Fields(lines[3]); len(f) >= 4 {
			total, _ := strconv.ParseFloat(f[1], 64)
			used, _ := strconv.ParseFloat(f[2], 64)
			v.DiskTotalGB, v.DiskUsedGB =
				total/1024/1024, used/1024/1024
		}
	}
	for _, l := range lines[minInt(4, len(lines)):] {
		f := strings.Split(l, "\t")
		if len(f) < 3 {
			continue
		}
		size, reclaim := parseDockerSize(f[1]), parseDockerSize(f[2])
		switch strings.TrimSpace(f[0]) {
		case "Images":
			v.ImagesGB = size
		case "Local Volumes":
			v.VolumesGB = size
		case "Build Cache":
			v.BuildCacheGB = size
		}
		v.ReclaimableGB += reclaim
	}
}

// parseDockerSize reads docker's human sizes: 12.81GB, 139.3MB, 0B, and
// "1.699GB (13%)" -- the reclaimable column carries a percentage it does
// not need to hand back.
func parseDockerSize(s string) float64 {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "("); i > 0 {
		s = strings.TrimSpace(s[:i])
	}
	mult := 1.0
	switch {
	case strings.HasSuffix(s, "TB"):
		mult, s = 1024, strings.TrimSuffix(s, "TB")
	case strings.HasSuffix(s, "GB"):
		mult, s = 1, strings.TrimSuffix(s, "GB")
	case strings.HasSuffix(s, "MB"):
		mult, s = 1.0/1024, strings.TrimSuffix(s, "MB")
	case strings.HasSuffix(s, "kB"), strings.HasSuffix(s, "KB"):
		mult, s = 1.0/1024/1024, s[:len(s)-2]
	case strings.HasSuffix(s, "B"):
		mult, s = 1.0/1024/1024/1024, strings.TrimSuffix(s, "B")
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return n * mult
}

// busyKind reads the load against the blocked count and says which sort of
// busy this is. The whole point is that "load 36" is not an answer -- it is
// two answers wearing one number, and they want opposite responses.
func busyKind(v VM) (string, bool) {
	if v.Load[0] < float64(maxInt(v.CPUs, 1))*1.5 {
		return "", false
	}
	if v.Blocked > 2 {
		return fmt.Sprintf(
			"WAITING ON DISK: %d processes blocked on I/O, load "+
				"%.1f on %d cpu",
			v.Blocked,
			v.Load[0],
			v.CPUs,
		), true
	}
	return fmt.Sprintf(
		"cpu saturated: load %.1f on %d cpu, nothing blocked on I/O",
		v.Load[0],
		v.CPUs,
	), true
}

// fetchClusters asks k3d which clusters exist on that VM.
func fetchClusters() ([]Cluster, error) {
	out, err := exec.Command("k3d", "cluster", "list", "-o", "json").
		Output()
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Name           string `json:"name"`
		ServersRunning int    `json:"serversRunning"`
		ServersCount   int    `json:"serversCount"`
		AgentsRunning  int    `json:"agentsRunning"`
		AgentsCount    int    `json:"agentsCount"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	cl := make([]Cluster, 0, len(raw))
	for _, c := range raw {
		cl = append(
			cl,
			Cluster{Name: c.Name, ServersRunning: c.ServersRunning,
				ServersCount:  c.ServersCount,
				AgentsRunning: c.AgentsRunning,
				AgentsCount:   c.AgentsCount},
		)
	}
	return cl, nil
}

// parseUptime pulls the up-time and the three load averages out of uptime(1).
// Both BSD spellings appear in the wild ("load averages:" and "load average:")
// and the numbers may be comma- or space-separated.
func parseUptime(s string) (string, [3]float64) {
	var load [3]float64
	up := ""
	if i := strings.Index(s, " up "); i >= 0 {
		rest := s[i+4:]
		if j := strings.Index(rest, ", load"); j >= 0 {
			up = strings.TrimSpace(rest[:j])
		} else if j := strings.Index(rest, ","); j >= 0 {
			up = strings.TrimSpace(rest[:j])
		}
		if k := strings.Index(up, ", "); k >= 0 &&
			strings.Contains(up[k:], "user") {
			up = strings.TrimSpace(up[:k])
		}
	}
	if i := strings.Index(s, "load average"); i >= 0 {
		tail := s[i:]
		if j := strings.Index(tail, ":"); j >= 0 {
			fields := strings.FieldsFunc(
				tail[j+1:],
				func(r rune) bool {
					return r == ',' || r == ' ' || r == '\n'
				},
			)
			for n := 0; n < 3 && n < len(fields); n++ {
				load[n], _ = strconv.ParseFloat(fields[n], 64)
			}
		}
	}
	return up, load
}

// hostFetched carries the host read back into the loop.
type hostFetched struct{ h HostStats }

// hostPane is the pane itself: state, and nothing that is not state.
type hostPane struct {
	h HostStats
	// the cursor walks the things that can be started -- the colima profile
	// and the clusters -- because an action needs a subject and "start"
	// with no selection is how the wrong thing gets started
	cursor  int
	arm     *armed
	running bool
	out     []string
	ch      chan substrateOut
	note    string
}

// startable is one thing the cursor can sit on.
type startable struct {
	kind  string // colima | cluster
	name  string
	up    bool
	label string
}

// startables is the cursor's world: the profile, then the clusters.
func (p *hostPane) startables() []startable {
	var out []startable
	for _, v := range p.h.VMs {
		out = append(out, startable{kind: "colima", name: v.Name,
			up: strings.EqualFold(
				v.Status,
				"running",
			), label: "colima " + v.Name})
	}
	for _, c := range p.h.Clusters {
		out = append(out, startable{kind: "cluster", name: c.Name,
			up: c.ServersRunning >= c.ServersCount &&
				c.ServersCount > 0,
			label: "cluster " + c.Name})
	}
	return out
}

// at is whatever the cursor is on, and whether it is a thing that can be
// started at all: the list mixes clusters with rows that are only facts.
func (p *hostPane) at() (startable, bool) {
	list := p.startables()
	if p.cursor < 0 || p.cursor >= len(list) {
		return startable{}, false
	}
	return list[p.cursor], true
}

// Key opens the host pane from the roster.
func (p *hostPane) Key() string { return "v" }

// Title names the pane, and keys its refresh gate.
func (p *hostPane) Title() string { return "host" }

// Help is the footer: the two-keystroke arm, and nothing that reads like
// a one-press button.
func (p *hostPane) Help() string {
	return "j/k: move · s: start (press twice -- once to want it, once " +
		"to mean it) · r: refresh · q: back\n" +
		"puffin never STOPS colima: one profile hosts both clusters " +
		"here, so every colima stop takes prod down with it"
}

// Load measures this machine. The domain is ignored: a host is where the
// process is, not a name that resolves.
func (p *hostPane) Load(string) tea.Cmd {
	return func() tea.Msg { return hostFetched{fetchHost()} }
}

// Update folds in a measurement or a keystroke, and holds the arm state
// that turns a press into a confirmed intent.
func (p *hostPane) Update(msg tea.Msg) (pane, tea.Cmd) {
	switch msg := msg.(type) {
	case hostFetched:
		p.h = msg.h
		if n := len(p.startables()); p.cursor >= n {
			p.cursor = maxInt(0, n-1)
		}
	case substrateOut:
		if msg.line != "" {
			p.out = append(p.out, msg.line)
			if len(p.out) > 200 {
				p.out = p.out[len(p.out)-200:]
			}
		}
		if msg.done {
			p.running, p.ch = false, nil
			if msg.err != nil {
				p.note = "failed: " + msg.err.Error()
			} else {
				p.note = "done"
			}
			// the world changed, so re-read it rather than
			// believing the screen that was drawn before it changed
			return p, p.Load("")
		}
		return p, nextLine(p.ch)
	case tea.KeyMsg:
		switch msg.String() {
		case "down", "j":
			if p.cursor < len(p.startables())-1 {
				p.cursor++
			}
			p.arm = nil
		case "up", "k":
			if p.cursor > 0 {
				p.cursor--
			}
			p.arm = nil
		case "s":
			return p, p.arms()
		default:
			// any other key disarms: an armed action must not
			// survive an unrelated keystroke
			p.arm = nil
		}
	}
	return p, nil
}

// arms implements the two-keystroke gate. The first s names what will happen
// and waits; the second, within the window, does it. Anything else disarms.
func (p *hostPane) arms() tea.Cmd {
	if p.running {
		p.note = "already running something"
		return nil
	}
	t, ok := p.at()
	if !ok {
		return nil
	}
	if t.up {
		p.arm, p.note = nil, t.label+" is already up"
		return nil
	}
	if !p.arm.live() || p.arm.target != t.name {
		p.arm = &armed{verb: "start", target: t.name, at: time.Now()}
		p.note = ""
		return nil
	}
	// second press, and it means it
	p.arm = nil
	p.running, p.out, p.note = true, nil, "starting "+t.label
	var ch chan substrateOut
	var err error
	if t.kind == "colima" {
		ch, err = startColima(t.name)
	} else {
		ch, err = startCluster(t.name)
	}
	if err != nil {
		p.running, p.note = false, err.Error()
		return nil
	}
	p.ch = ch
	return nextLine(ch)
}

// View draws the disk floor first, because it is the one number here that
// is not decoration.
func (p *hostPane) View(s Styles, width, height int) string {
	var b strings.Builder
	h := p.h
	for _, w := range h.Warnings {
		b.WriteString(s.Caution.Render("! "+w) + "\n")
	}
	b.WriteString(s.Header.Render(orDash(h.Hostname)))
	if h.Uptime != "" {
		// a different colour from the hostname: uptime is the number
		// you are actually looking for after a reboot, and it should
		// not have to be picked out of a sentence
		b.WriteString(
			s.Dim.Render("  up ") + s.AccentAlt.Render(h.Uptime),
		)
	}
	b.WriteString(
		s.Dim.Render(
			fmt.Sprintf(
				"   load %.2f %.2f %.2f",
				h.Load[0],
				h.Load[1],
				h.Load[2],
			),
		) + "\n\n",
	)

	// disk first, and the floor is stated on the line rather than left for
	// the reader to remember: 50GB is a number nobody recalls at 03:20
	b.WriteString(s.Header.Render("disk") + "\n")
	for _, d := range h.Disks {
		line := pad(
			d.Path,
			22,
		) + pad(
			humanBytes(int64(d.FreeBytes))+" free",
			16,
		) +
			s.Dim.Render(
				"of "+humanBytes(int64(d.TotalBytes)),
			)
		if d.FreeBytes < diskFloorBytes {
			b.WriteString(
				"  " + s.Warn.Render(
					line,
				) + s.Warn.Render(
					"  under the 50GB floor",
				) + "\n",
			)
		} else {
			b.WriteString("  " + s.Row.Render(line) + "\n")
		}
	}

	// the section says what puffin is talking to, not just what it is
	// showing: every row under here is a thing s will run a colima or k3d
	// command against, and the operator should not have to infer that.
	b.WriteString("\n" + s.Header.Render("colima") +
		s.Dim.Render(
			"   these rows are live: s runs colima and k3d "+
				"commands against them",
		) + "\n")
	if len(h.VMs) == 0 {
		b.WriteString(s.Dim.Render("  no profiles") + "\n")
	}
	idx := 0
	for _, v := range h.VMs {
		on := idx == p.cursor
		marker := s.On(s.Row, on).Render("  ")
		if on {
			marker = s.On(s.RowSel, on).Render("\u25b8 ")
		}
		st := s.Warn
		if strings.EqualFold(v.Status, "running") {
			st = s.Ok
		}
		b.WriteString(marker + s.On(s.Row, on).Render(pad(v.Name, 14)) +
			s.On(st, on).Render(pad(v.Status, 10)) +
			s.On(s.Dim, on).
				Render(padTo(fmt.Sprintf(
					"%s · %d cpu · %s "+
						"ram · %s disk · %s",
					v.Arch,
					v.CPUs,
					humanBytes(int64(v.MemoryBytes)),
					humanBytes(
						int64(v.DiskBytes),
					),
					v.Runtime,
				),
					56)) +
			"\n")
		// what the linux inside says about itself, on its own line and
		// indented under the profile it belongs to
		if v.Uptime != "" {
			inside := fmt.Sprintf(
				"load %.2f %.2f %.2f",
				v.Load[0],
				v.Load[1],
				v.Load[2],
			)
			if v.MemTotalMB > 0 {
				inside += fmt.Sprintf(
					" · mem %d/%d MB",
					v.MemUsedMB,
					v.MemTotalMB,
				)
			}
			if v.Blocked > 0 {
				inside += fmt.Sprintf(
					" · %d blocked on I/O",
					v.Blocked,
				)
			}
			if v.DiskTotalGB > 0 {
				inside += fmt.Sprintf(" · disk %.0f/%.0fG",
					v.DiskUsedGB, v.DiskTotalGB)
			}
			b.WriteString("      " + s.Dim.Render("inside: up ") +
				s.AccentAlt.Render(
					v.Uptime,
				) + s.Dim.Render("  "+inside) + "\n")
			// and when it is genuinely struggling, say which kind,
			// because the answer decides what you do about it
			if note, busy := busyKind(v); busy {
				b.WriteString(
					"      " + s.Warn.Render(note) + "\n",
				)
			}
			// what the space is going to, and how much of it
			// nothing is using.
			//
			// A build system that does not manage its space leaves
			// large files everywhere, and on the disk both clusters
			// share that is an outage with a delay on it.
			if v.ImagesGB+v.VolumesGB+v.BuildCacheGB > 0 {
				b.WriteString(
					"      " + s.Dim.Render(fmt.Sprintf(
						"docker: %.1fG images · "+
							"%.1fG volumes · "+
							"%.1fG build cache · "+
							"%.1fG reclaimable",
						v.ImagesGB,
						v.VolumesGB,
						v.BuildCacheGB,
						v.ReclaimableGB,
					)) + "\n",
				)
			}
			if note, tight := diskNote(v); tight {
				b.WriteString(
					"      " + s.Warn.Render(note) + "\n",
				)
			}
		} else if v.InsideError != "" {
			b.WriteString("      " + s.Dim.Render("inside: no "+
				"answer") + "\n")
		}
		idx++
	}

	b.WriteString("\n" + s.Header.Render("k3d clusters") + "\n")
	if len(h.Clusters) == 0 {
		b.WriteString(s.Dim.Render("  none") + "\n")
	}
	for _, c := range h.Clusters {
		on := idx == p.cursor
		marker := s.On(s.Row, on).Render("  ")
		if on {
			marker = s.On(s.RowSel, on).Render("\u25b8 ")
		}
		st, label := s.Ok, "up"
		if c.ServersRunning < c.ServersCount ||
			(c.AgentsCount > 0 && c.AgentsRunning < c.AgentsCount) {
			st, label = s.Warn, "degraded"
		}
		// which cluster puffin is pointed at is the dev/prod boundary,
		// so it is marked here and not merely known the note is about
		// puffin, not about the cluster, and it must not read as a
		// status.
		//
		// The first cut appended "(prod: puffin will not start it)"
		// into a padded field, where it clipped to "will not star…" --
		// which looked exactly like something being wrong with a
		// perfectly healthy prod cluster, and was read that way.
		note := ""
		if "k3d-"+c.Name == kubeContext() {
			note = "puffin's context"
		}
		if "k3d-"+c.Name != homeContext() {
			if note != "" {
				note += " · "
			}
			note += "prod, read-only"
		}
		b.WriteString(marker + s.On(s.Row, on).Render(pad(c.Name, 14)) +
			s.On(st, on).Render(pad(label, 10)) +
			s.On(s.Dim, on).
				Render(pad(fmt.Sprintf(
					"servers %d/%d · "+
						"agents %d/%d",
					c.ServersRunning,
					c.ServersCount,
					c.AgentsRunning,
					c.AgentsCount,
				),
					28)) +
			s.On(s.AccentAlt, on).Render(padTo(note, 26)) + "\n")
		idx++
	}

	// the gate, and the exact command it will run. Naming the command is
	// the point: "start colima?" is a question about an intention, and
	// `colima start --profile default` is a question about a fact.
	if p.arm.live() {
		b.WriteString(
			"\n" + s.Caution.Render(
				"press s again to run:  "+p.command(
					p.arm.target,
				),
			) + "\n",
		)
		left := (armWindow - time.Since(p.arm.at)).Seconds()
		b.WriteString(s.Help.Render(
			"any other key cancels · expires in "+
				fmt.Sprintf("%.0fs", left)) + "\n")
	} else if p.note != "" {
		b.WriteString("\n" + s.Dim.Render(p.note) + "\n")
	}

	// colima's own words, in the little screen, so a start that is slow
	// looks like a start that is slow rather than a frozen tool
	if p.running || len(p.out) > 0 {
		title := "colima"
		if t, ok := p.at(); ok && t.kind == "cluster" {
			title = "k3d"
		}
		var box strings.Builder
		head := title + " says"
		if p.running {
			head += " · running"
		}
		box.WriteString(s.CRTDim.Render(padTo(head, width-10)) + "\n")
		show := p.out
		if len(show) > 10 {
			show = show[len(show)-10:]
		}
		for i := 0; i < 10; i++ {
			line := ""
			if i < len(show) {
				line = show[i]
			}
			box.WriteString(s.CRT.Render(padTo(line, width-10)))
			if i < 9 {
				box.WriteString("\n")
			}
		}
		b.WriteString("\n" + s.CRTFrame.Render(box.String()) + "\n")
	}
	return b.String()
}

// command is the exact thing that will run, shown before it runs. The
// profile is always explicit -- a colima command without one is the command
// that takes prod down, and puffin does not have an unqualified form.
func (p *hostPane) command(target string) string {
	if t, ok := p.at(); ok && t.kind == "cluster" {
		return "k3d cluster start " + target
	}
	return "colima start --profile " + target
}

// shortPath writes the home directory the way a human does.
func shortPath(p string) string {
	if home := os.Getenv("HOME"); home != "" && p == home {
		return "~"
	}
	return p
}

// mergePools folds filesystems that share their free space into one row.
//
// On macOS / and ~ are on different devices -- the sealed system volume and the
// data volume -- but one APFS container, so they draw from the same pool and
// report identical numbers.
//
// Showing "151.61 GiB free" twice is not two facts, it is one fact and a
// distraction, and on a screen whose job is to make the 50GB floor visible a
// duplicated row is noise sitting next to the number that matters.
//
// The signal is the numbers themselves rather than the device:
//
// firmlinks make the device name disagree with itself depending on who is asked
// (stat and df name different devices for the same path here), while two
// filesystems reporting byte-identical free AND total are sharing a pool
// whatever they are called.
func mergePools(disks []DiskFree) []DiskFree {
	var out []DiskFree
	for _, d := range disks {
		merged := false
		for i := range out {
			if out[i].FreeBytes == d.FreeBytes &&
				out[i].TotalBytes == d.TotalBytes {
				out[i].Path += " and " + d.Path
				merged = true
				break
			}
		}
		if !merged {
			out = append(out, d)
		}
	}
	return out
}

// vmDiskFloor is the share of the VM's disk that must stay free.
//
// A fraction rather than the estate's 50GB figure, because that number is
// about the Mac. The VM's disk is 57G and fixed, so 50G free is not a floor
// there, it is most of the disk. Fifteen percent of 57G is about 8G, which
// is roughly one more careless image pull.
const vmDiskFloor = 0.15

// diskNote warns when the VM's disk is filling, and says what could be
// reclaimed, because "you are nearly out of disk" without "and 26G of this
// is volumes nothing is using" is a problem statement rather than an answer.
func diskNote(v VM) (string, bool) {
	if v.DiskTotalGB <= 0 {
		return "", false
	}
	free := v.DiskTotalGB - v.DiskUsedGB
	if free/v.DiskTotalGB >= vmDiskFloor {
		return "", false
	}
	note := fmt.Sprintf(
		"the VM's disk is %.0f%% full, %.0fG left -- this is the "+
			"disk BOTH clusters run on",
		v.DiskUsedGB/v.DiskTotalGB*100,
		free,
	)
	if v.ReclaimableGB > 1 {
		note += fmt.Sprintf(
			"; %.0fG is reclaimable (docker system prune)",
			v.ReclaimableGB,
		)
	}
	return note, true
}
