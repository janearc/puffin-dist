package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// The registry is the seam's promise: a pane is reachable by its key without
// the event loop having been edited to know about it.
func TestPanesAreReachableFromTheRegistry(t *testing.T) {
	m := baseModel()
	m.paneReg = panes()
	m.scr = screenRoster
	for key, p := range m.paneReg {
		next, cmd := m.Update(
			tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)},
		)
		got := next.(model)
		if got.scr != screenPane || got.openPane == nil {
			t.Fatalf("%q did not open a pane", key)
		}
		if got.openPane.Title() != p.Title() {
			t.Fatalf("%q opened %q", key, got.openPane.Title())
		}
		if cmd == nil {
			t.Fatalf(
				"%q opened %q without fetching anything",
				key,
				p.Title(),
			)
		}
		// q hands the operator back to the roster and drops the pane
		back, _ := got.Update(key2('q'))
		if b := back.(model); b.scr != screenRoster ||
			b.openPane != nil {
			t.Fatalf("q left %v with pane %v", b.scr, b.openPane)
		}
	}
}

// key2 spells one rune keystroke, which is most of what these tests send.
func key2(
	r rune,
) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

// uptime(1) is parsed rather than trusted: both BSD spellings, and the load
// numbers are the point.
func TestParseUptime(t *testing.T) {
	up, load := parseUptime(
		"15:36  up  9:32, 4 users, load averages: 1.32 1.77 1.72\n",
	)
	if up != "9:32" {
		t.Fatalf("uptime: %q", up)
	}
	if load != [3]float64{1.32, 1.77, 1.72} {
		t.Fatalf("load: %v", load)
	}
	_, load = parseUptime(
		"15:36 up 3 days, 1 user, load average: 0.50, 0.40, 0.30\n",
	)
	if load != [3]float64{0.5, 0.4, 0.3} {
		t.Fatalf("comma-separated load: %v", load)
	}
}

// The disk floor is shown as a breach, not left for the reader to compute.
// 50GB is a number nobody recalls at 03:20.
func TestDiskFloorIsCalledOut(t *testing.T) {
	p := &hostPane{h: HostStats{
		Hostname: "kestrel",
		Disks: []DiskFree{
			{
				Path:       "/",
				FreeBytes:  20 * 1024 * 1024 * 1024,
				TotalBytes: 500 * 1024 * 1024 * 1024,
			},
			{
				Path:       "/src",
				FreeBytes:  200 * 1024 * 1024 * 1024,
				TotalBytes: 500 * 1024 * 1024 * 1024,
			},
		},
	}}
	out := p.View(Compile(DodoDark()), 120, 40)
	if !strings.Contains(out, "under the 50GB floor") {
		t.Fatal("a breached floor was not called out")
	}
	if strings.Count(out, "under the 50GB floor") != 1 {
		t.Fatal("a healthy filesystem was called a breach")
	}
}

// The pane says which cluster puffin is pointed at, because that is the
// dev/prod boundary and knowing it is not the same as being shown it.
func TestHostPaneMarksPuffinsContext(t *testing.T) {
	p := &hostPane{h: HostStats{Clusters: []Cluster{
		{Name: "fleet", ServersRunning: 1, ServersCount: 1},
		{Name: "local", ServersRunning: 1, ServersCount: 1},
	}}}
	out := p.View(Compile(DodoDark()), 120, 40)
	i := strings.Index(out, "puffin's context")
	if i < 0 {
		t.Fatal("the pane does not say which cluster puffin talks to")
	}
	if !strings.Contains(out[:i], "local") ||
		strings.Contains(out[:i], "fleet\n") {
		t.Fatal("the mark landed on the wrong cluster")
	}
}

// A colima that is not running is not a green light.
func TestStoppedVMIsNotGreen(t *testing.T) {
	p := &hostPane{
		h: HostStats{
			VMs: []VM{
				{
					Name:    "default",
					Status:  "Stopped",
					Runtime: "docker",
				},
			},
		},
	}
	out := p.View(Compile(DodoDark()), 120, 40)
	if !strings.Contains(out, "Stopped") {
		t.Fatal("the pane hid a stopped VM")
	}
}

// podsJSON is kubectl's real shape, reduced to what the reconciliation reads:
// two services on ONE image (athlete and explorer really do share one), and a
// service whose tag carries a commit.
const podsJSON = `{"items":[` + "\n" +
	` {"metadata":{"name":"athlete-59c5b49dc7-wv6f5","namespace":"local",` +
	`"labels":{"app.kubernetes.io/name":"athlete"}},` + "\n" +
	`  "status":{"containerStatuses":[{"ready":true,` +
	`"image":"docker.io/library/kestrel-panes:dev",` +
	`"imageID":"sha256:4539a0f843c0f1979e21cdd99121cfbbbb0ac85a"}` +
	`]}},` + "\n" +
	` {"metadata":{"name":"explorer-796b8495c6-9qvlq",` +
	`"namespace":"local","labels":{"app.kubernetes.io/name":"expl` +
	`orer"}},` + "\n" +
	`  "status":{"containerStatuses":[{"ready":true,` +
	`"image":"docker.io/library/kestrel-panes:dev",` +
	`"imageID":"sha256:4539a0f843c0f1979e21cdd99121cfbbbb0ac85a"}` +
	`]}},` + "\n" +
	` {"metadata":{"name":"flipr-1","namespace":"flipr",` +
	`"labels":{"app.kubernetes.io/name":"flipr"}},` + "\n" +
	`  "status":{"containerStatuses":[{"ready":true,` +
	`"image":"ghcr.io/janearc/flipr:435fd50",` +
	`"imageID":"sha256:aaaabbbbccccdddd"}]}},` + "\n" +
	` {"metadata":{"name":"flipr-2","namespace":"flipr",` +
	`"labels":{"app.kubernetes.io/name":"flipr"}},` + "\n" +
	`  "status":{"containerStatuses":[{"ready":false,` +
	`"image":"ghcr.io/janearc/flipr:435fd50",` +
	`"imageID":"sha256:aaaabbbbccccdddd"}]}}]}`

// Replicas fold into one deployed thing, and readiness is counted across them.
func TestReduceDeploysFoldsReplicas(t *testing.T) {
	rows, err := reduceDeploys([]byte(podsJSON))
	if err != "" {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows: %d, want one per service", len(rows))
	}
	var flipr Deploy
	for _, r := range rows {
		if r.Service == "flipr" {
			flipr = r
		}
	}
	if flipr.Pods != 2 || flipr.Ready != 1 {
		t.Fatalf("replicas did not fold: %+v", flipr)
	}
	if flipr.Tag != "435fd50" || flipr.BuildID != "435fd50" {
		t.Fatalf("build id not read from the tag: %+v", flipr)
	}
	if flipr.Digest != "aaaabbbbcccc" {
		t.Fatalf("digest: %q", flipr.Digest)
	}
}

// One image under two names is worth saying: a redeploy of one is a
// redeploy of both, and nothing else on the screen would show it.
func TestSharedImageIsNamed(t *testing.T) {
	rows, _ := reduceDeploys([]byte(podsJSON))
	for _, r := range rows {
		if r.Service == "athlete" {
			if len(r.SharedWith) != 1 ||
				r.SharedWith[0] != "explorer" {
				t.Fatalf(
					"shared image not reported: %+v",
					r.SharedWith,
				)
			}
			return
		}
	}
	t.Fatal("athlete missing")
}

// The reconciliation is three-valued. A `:dev` tag cannot agree with
// anything, and calling that agreement is a green light on a cut wire.
func TestReconcileIsThreeValued(t *testing.T) {
	if got := reconcile("", "435fd50"); got != AgreeUnknowable {
		t.Fatal("a tag with no build id was given a verdict")
	}
	if got := reconcile("435fd50", ""); got != AgreeUnknowable {
		t.Fatal(
			"a service flipr has never heard of was given a " +
				"verdict",
		)
	}
	if got := reconcile("435fd50", "435fd50"); got != AgreeYes {
		t.Fatal("a match was not read as agreement")
	}
	if got := reconcile("f9cc8c1", "435fd50"); got != AgreeNo {
		t.Fatal("a stale deploy was not caught")
	}
	// a full sha deployed against flipr's short namespace still agrees
	if got := reconcile("435fd50a1b2c3d4e5f", "435fd50"); got != AgreeYes {
		t.Fatal("a long digest did not match its short namespace")
	}
}

// The disagreement is the screen: it says so, and it says what flipr thinks.
func TestDisagreementIsLoud(t *testing.T) {
	p := &deployPane{v: DeployView{Rows: []Deploy{
		{
			Service:   "kestrel",
			Namespace: "local",
			Tag:       "f9cc8c1",
			BuildID:   "f9cc8c1",
			Digest:    "abc123abc123",
			Reported:  "435fd50",
			Pods:      1,
			Ready:     1,
			Agree:     AgreeNo,
		},
	}}}
	out := p.View(Compile(DodoDark()), 140, 40)
	if !strings.Contains(out, "NO --") ||
		!strings.Contains(out, "435fd50") {
		t.Fatalf("the wrong build is not called out plainly:\n%s", out)
	}
}

// A tag that cannot answer says so rather than showing green.
func TestUnknowableIsNotGreen(t *testing.T) {
	p := &deployPane{v: DeployView{Rows: []Deploy{
		{
			Service:   "athlete",
			Namespace: "local",
			Tag:       "dev",
			Digest:    "4539a0f843c0",
			Pods:      1,
			Ready:     1,
			Agree:     AgreeUnknowable,
		},
	}}}
	out := p.View(Compile(DodoDark()), 140, 40)
	if !strings.Contains(out, "cannot tell") {
		t.Fatalf(
			"the pane implied a verdict it does not have:\n%s",
			out,
		)
	}
	if !strings.Contains(out, "1 uncheckable") {
		t.Fatal("an uncheckable row was not counted as uncheckable")
	}
}

// imageTag handles the shapes registries actually produce.
func TestImageTag(t *testing.T) {
	for image, want := range map[string]string{
		"docker.io/library/dodo:dev":         "dev",
		"ghcr.io/janearc/flipr:435fd50":      "435fd50",
		"localhost:5000/puffin:abc1234":      "abc1234",
		"docker.io/library/kafka":            "latest",
		"grafana/grafana:11.3.0@sha256:aaaa": "11.3.0",
	} {
		if got := imageTag(image); got != want {
			t.Errorf("imageTag(%q)=%q want %q", image, got, want)
		}
	}
}

// The three-valued rule binds flipr's side too. The live estate really does
// run hm on namespace `_` and peacock on `peacock-dev`; neither is a commit,
// and shouting disagrees at them is a false alarm on a screen whose only job
// is to be believed.
func TestANonCommitNamespaceCannotDisagree(t *testing.T) {
	for _, ns := range []string{"_", "peacock-dev", "latest"} {
		if got := reconcile("28a0764", ns); got != AgreeUnknowable {
			t.Fatalf(
				"a non-commit self-report %q was given a "+
					"verdict: %v",
				ns,
				got,
			)
		}
	}
	// and the pane says which side could not answer
	d := Deploy{
		Service:  "hm",
		Tag:      "28a0764",
		BuildID:  "28a0764",
		Reported: "_",
	}
	if !strings.Contains(cannotSay(d), "not a commit") {
		t.Fatalf(
			"the pane does not name the side that cannot answer: "+
				"%q",
			cannotSay(d),
		)
	}
	d = Deploy{Service: "athlete", Tag: "dev", Reported: "435fd50"}
	if !strings.Contains(cannotSay(d), "it reports 435fd50") {
		t.Fatalf(
			"the pane does not fall back to what the service "+
				"reports: %q",
			cannotSay(d),
		)
	}
}

// On macOS / and ~ are on different devices -- the sealed system volume and
// the data volume -- but one APFS container, so they share a pool and report
// identical numbers. One fact shown twice is a distraction sitting next to
// the number the screen exists for.
func TestSharedPoolsAreOneRow(t *testing.T) {
	same := []DiskFree{
		{
			Path:       "/",
			FreeBytes:  162_800_000_000,
			TotalBytes: 994_000_000_000,
		},
		{
			Path:       "~",
			FreeBytes:  162_800_000_000,
			TotalBytes: 994_000_000_000,
		},
	}
	got := mergePools(same)
	if len(got) != 1 {
		t.Fatalf("%d rows for one pool", len(got))
	}
	if !strings.Contains(got[0].Path, "/") ||
		!strings.Contains(got[0].Path, "~") {
		t.Fatalf("the merged row lost a path: %q", got[0].Path)
	}
	// genuinely separate volumes stay separate
	diff := []DiskFree{
		{Path: "/", FreeBytes: 100, TotalBytes: 200},
		{Path: "/Volumes/scratch", FreeBytes: 50, TotalBytes: 200},
	}
	if len(mergePools(diff)) != 2 {
		t.Fatal("two real volumes were merged")
	}
	// and the floor still fires on a merged row
	p := &hostPane{h: HostStats{Disks: mergePools([]DiskFree{
		{Path: "/", FreeBytes: 20 << 30, TotalBytes: 500 << 30},
		{Path: "~", FreeBytes: 20 << 30, TotalBytes: 500 << 30},
	})}}
	out := p.View(Compile(DodoDark()), 120, 40)
	if strings.Count(out, "under the 50GB floor") != 1 {
		t.Fatal("the floor warning was lost or doubled by the merge")
	}
}

// Two keystrokes: the first says you want it, the second says you mean it.
// The rule for anything touching the virtual machine or a cluster.
func TestColimaNeedsTwoKeystrokes(t *testing.T) {
	p := &hostPane{h: HostStats{
		VMs: []VM{
			{Name: "default", Status: "Stopped", Runtime: "docker"},
		},
		Clusters: []Cluster{
			{Name: "local", ServersRunning: 0, ServersCount: 1},
		},
	}}
	// the first press arms and runs nothing
	pane, cmd := p.Update(key('s'))
	hp := pane.(*hostPane)
	if cmd != nil || hp.running {
		t.Fatal("one keystroke started something")
	}
	if !hp.arm.live() || hp.arm.target != "default" {
		t.Fatalf("nothing was armed: %+v", hp.arm)
	}
	// the screen names the exact command, not an intention
	out := hp.View(Compile(DodoDark()), 140, 44)
	if !strings.Contains(out, "colima start --profile default") {
		t.Fatalf("the armed command is not named:\n%s", out)
	}
	// any other key disarms
	pane, _ = hp.Update(key('j'))
	if pane.(*hostPane).arm.live() {
		t.Fatal("an unrelated keystroke left the action armed")
	}
}

// A cluster that is not the dev one is refused outright: starting fleet is a
// prod action and this tool does not have that approval.
func TestPuffinWillNotStartTheProdCluster(t *testing.T) {
	if _, err := startCluster("fleet"); err == nil {
		t.Fatal("puffin agreed to start the prod cluster")
	} else if !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("unclear refusal: %v", err)
	}
	if _, err := startCluster(
		strings.TrimPrefix(homeContext(), "k3d-"),
	); err != nil {
		// it may fail for other reasons in a sandbox; it must not
		// refuse
		if strings.Contains(err.Error(), "refusing") {
			t.Fatalf("puffin refused its own dev cluster: %v", err)
		}
	}
}

// There is no colima stop. One profile hosts both clusters here, so every
// colima stop takes prod down -- a verb that cannot be offered honestly is
// not offered, and the pane says so rather than leaving it to be discovered.
func TestThereIsNoColimaStop(t *testing.T) {
	p := &hostPane{}
	if strings.Contains(strings.ToLower(p.Help()), "stop colima") {
		t.Fatal("a stop verb is advertised")
	}
	if !strings.Contains(p.Help(), "never STOPS colima") {
		t.Fatalf(
			"the pane does not say why there is no stop: %q",
			p.Help(),
		)
	}
	// and the only colima command puffin can build names a profile
	if _, err := startColima(""); err == nil {
		t.Fatal("puffin built a colima command with no profile")
	}
}

// The pane says these rows are live, because "colima  default  Stopped" on
// its own reads as a report, not as a thing you can act on.
func TestTheHostPaneSaysItIsTalkingToColima(t *testing.T) {
	p := &hostPane{
		h: HostStats{VMs: []VM{{Name: "default", Status: "Running"}}},
	}
	out := p.View(Compile(DodoDark()), 140, 44)
	if !strings.Contains(out, "s runs colima and k3d commands") {
		t.Fatalf("the pane does not say it is live:\n%s", out)
	}
}

// A note about what puffin will do must never read as a status of the thing
// it is about. The first cut appended "(prod: puffin will not start it)"
// into a padded field where it clipped to "will not star…", next to a
// perfectly healthy cluster -- and it was read as an outage.
func TestAPolicyNoteIsNotAStatus(t *testing.T) {
	p := &hostPane{h: HostStats{Clusters: []Cluster{
		{Name: "fleet", ServersRunning: 1, ServersCount: 1},
		{Name: "local", ServersRunning: 1, ServersCount: 1},
	}}}
	out := p.View(Compile(DodoDark()), 140, 44)
	var fleet string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "fleet") {
			fleet = l
		}
	}
	if fleet == "" {
		t.Fatal("no fleet row")
	}
	if !strings.Contains(fleet, "up") {
		t.Fatalf("a healthy cluster does not read as up: %q", fleet)
	}
	// nothing on the row may be clipped: a truncated note is a note whose
	// meaning is whatever the reader fears
	if strings.Contains(
		fleet,
		"…",
	) {
		t.Fatalf(
			"the row clips: "+
				"%q",
			fleet,
		)
	}
	if !strings.Contains(
		fleet,
		"read-only",
	) {
		t.Fatalf(
			"the prod note is missing: %q",
			fleet,
		)
	}
}

// A build stamp is read from the POD SPEC, which is the one place that works
// for every language at once -- this estate is about half typescript, so a
// Go-only answer covers a minority of it.
//
// And the value must be commit-shaped: DODO_FLIPR_VERSION=dodo-dev is already
// sitting in a pod spec and it is a flag namespace, not a build.
func TestBuildStampIsReadButNotBelievedBlindly(t *testing.T) {
	const podsWithEnv = `{"items":[` + "\n" +
		`{"metadata":{"name":"a-1","namespace":"local",` +
		`"labels":{"app.kubernetes.io/name":"stamped"}}` +
		`,` + "\n" +
		`	  "spec":{"containers":[{"env":[{"name":"BUILD_REV",` +
		`"value":"9f3c21a"}]}]},` + "\n" +
		`"status":{"containerStatuses":[{"ready":true,` +
		`"image":"x/stamped:dev",` +
		`"imageID":"sha256:aaaa"}]}},` + "\n" +
		`{"metadata":{"name":"b-1","namespace":"local",` +
		`"labels":{"app.kubernetes.io/name":"namespaced"}}` +
		`,` + "\n" +
		`"spec":{"containers":[{"env":[{"name":"DODO_FLIPR_VERSION",` +
		`"value":"dodo-dev"},{"name":"PEACOCK_VERSION",` +
		`"value":"peacock-dev"}]}]},` + "\n" +
		`	  "status":{"containerStatuses":[{"ready":true,` +
		`"image":"x/namespaced:dev",` +
		`"imageID":"sha256:bbbb"}]}}]}`
	rows, err := reduceDeploys([]byte(podsWithEnv))
	if err != "" {
		t.Fatal(err)
	}
	by := map[string]Deploy{}
	for _, r := range rows {
		by[r.Service] = r
	}
	if got := by["stamped"]; got.Stamped != "9f3c21a" ||
		got.BuildID != "9f3c21a" {
		t.Fatalf("a stamped commit was not read: %+v", got)
	}
	// a :dev tag says nothing, so the stamp becomes the build id
	if by["stamped"].Tag != "dev" {
		t.Fatalf("tag: %q", by["stamped"].Tag)
	}
	// and a flag namespace in a *_VERSION variable is NOT a build
	if got := by["namespaced"]; got.Stamped != "" || got.BuildID != "" {
		t.Fatalf("a flag namespace was believed as a build: %+v", got)
	}
}

// The split is a layout, not a feature: any pane beside any other, and the
// panes do not know it happened -- they are handed a width and render into
// it, which they already did.
func TestSplitIsOptInAndReplaceable(t *testing.T) {
	m := baseModel()
	m.paneReg = panes()
	m.scr = screenRoster
	// open logs
	next, _ := m.Update(key('l'))
	m = next.(model)
	if m.openPane == nil || m.rightPane != nil {
		t.Fatal("opening a pane should not split")
	}
	// | splits, duplicating the focused pane and focusing the new side
	next, cmd := m.Update(key('|'))
	m = next.(model)
	if m.rightPane == nil || m.focus != right || cmd == nil {
		t.Fatal("| did not split and load")
	}
	if m.openPane == m.rightPane {
		t.Fatal(
			"the split shares one pane instance: two logs would " +
				"share a cursor",
		)
	}
	if m.openPane.Title() != m.rightPane.Title() {
		t.Fatal("| should duplicate the focused pane")
	}
	// a pane key replaces the focused side
	next, _ = m.Update(key('b'))
	m = next.(model)
	if m.rightPane.Title() != "bus" || m.openPane.Title() != "logs" {
		t.Fatalf(
			"replace hit the wrong side: %s | %s",
			m.openPane.Title(),
			m.rightPane.Title(),
		)
	}
	// ctrl+w moves the keyboard
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlW})
	m = next.(model)
	if m.focus != left {
		t.Fatal("ctrl+w did not move focus")
	}
	// q closes the focused side, not the whole screen
	next, _ = m.Update(key('q'))
	m = next.(model)
	if m.scr != screenPane {
		t.Fatal("q with a split open left the pane screen entirely")
	}
	if m.rightPane != nil || m.openPane.Title() != "bus" {
		t.Fatalf("q closed the wrong side: "+
			"%v", m.openPane.Title())
	}
	// and now q leaves
	next, _ = m.Update(key('q'))
	if next.(model).scr != screenRoster {
		t.Fatal("q on a single pane did not leave")
	}
}

// Columns are padded by visible width. Padding by len() counts colour codes
// as characters and tears the right column into a staircase.
func TestJoinColumnsIgnoresColour(t *testing.T) {
	s := Compile(DodoDark())
	left := s.Ok.Render("green") + "\n" + s.Warn.Render("red")
	right := "one\ntwo"
	out := joinColumns(s, left, right, 20)
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "│") {
			t.Fatalf("no divider on a line: %q", line)
		}
		// the divider must sit at the same visible column on every line
		at := visibleWidth(strings.SplitN(line, "│", 2)[0])
		if at != 21 {
			t.Fatalf("divider at column %d, not 21: %q", at, line)
		}
	}
	if visibleWidth(s.Ok.Render("green")) != 5 {
		t.Fatal("visibleWidth counted escape bytes as columns")
	}
	// a shorter column still gets a divider on every row
	if n := len(
		strings.Split(joinColumns(s, "a", "b\nc\nd", 10), "\n"),
	); n != 3 {
		t.Fatalf("%d rows, want 3", n)
	}
}

// "Load 36" is not an answer. It is two answers wearing one number.
//
// Linux load average counts processes in uninterruptible I/O wait as well as
// runnable ones, so a high figure means either far too much work for the cpus
// or everything queued behind a device -- and those want opposite responses.
//
// This estate lost BOTH clusters to the second kind: repeated initdb runs
// saturated colima's virtual disk until neither k3d cluster could reach its
// storage. The load average said 36. It was read here as cpu, in writing, which
// is the mistake this test exists to stop the screen from making.
func TestBusyKindSeparatesDiskFromCPU(t *testing.T) {
	// blocked processes and a high load: the disk is the problem
	stuck := VM{CPUs: 4, Load: [3]float64{36.8, 53.2, 41.4}, Blocked: 19}
	note, busy := busyKind(stuck)
	if !busy {
		t.Fatal("a load of 36 on four cpus was not called busy")
	}
	if !strings.Contains(note, "WAITING ON DISK") {
		t.Fatalf(
			"io saturation was reported as something else: %q",
			note,
		)
	}

	// the same load with nothing blocked is genuinely cpu
	hot := VM{CPUs: 4, Load: [3]float64{36.8, 53.2, 41.4}, Blocked: 0}
	note, busy = busyKind(hot)
	if !busy || strings.Contains(note, "DISK") {
		t.Fatalf("cpu saturation was reported as io: %q", note)
	}
	if !strings.Contains(note, "nothing blocked") {
		t.Fatalf("the cpu case does not say why it is not io: %q", note)
	}

	// a busy but healthy machine says nothing at all: a warning that fires
	// on a normal afternoon is one people learn to scroll past
	for _, ok := range []VM{
		{CPUs: 4, Load: [3]float64{2.1, 1.8, 1.5}},
		{CPUs: 4, Load: [3]float64{5.9, 4.0, 3.0}, Blocked: 1},
		{CPUs: 8, Load: [3]float64{9.0, 8.0, 7.0}},
	} {
		if _, busy := busyKind(ok); busy {
			t.Errorf(
				"a healthy machine was called busy: load "+
					"%.1f on %d cpu",
				ok.Load[0],
				ok.CPUs,
			)
		}
	}

	// and a VM puffin could not ask is not reported as idle
	if _, busy := busyKind(VM{CPUs: 0, Load: [3]float64{0, 0, 0}}); busy {
		t.Error("an unread VM was called busy")
	}
}

// The VM's disk is not the Mac's, and it is the one that matters. Puffin
// reported "150 GiB free" all evening from the host filesystem while the
// disk both clusters actually run on sat at 69% with 17G left. Both numbers
// were true; only one was about the thing that can take the estate down.
func TestVMDiskHasItsOwnFloor(t *testing.T) {
	// a build workflow that leaves its space unmanaged: nearly full, and
	// most of what is there is reclaimable
	tight := VM{DiskTotalGB: 56.8, DiskUsedGB: 50.0, ReclaimableGB: 26.0}
	note, warn := diskNote(tight)
	if !warn {
		t.Fatal("a disk at 88% did not warn")
	}
	if !strings.Contains(note, "BOTH clusters") {
		t.Fatalf("the warning does not say whose disk it is: %q", note)
	}
	if !strings.Contains(note, "reclaimable") {
		t.Fatalf(
			"a warning with 26G reclaimable does not mention it: "+
				"%q",
			note,
		)
	}

	// the estate's 50GB figure is about the Mac and would fire forever on a
	// 57G disk, so the VM's floor is a fraction
	ok := VM{DiskTotalGB: 56.8, DiskUsedGB: 37.0, ReclaimableGB: 6.6}
	if _, warn := diskNote(ok); warn {
		t.Error(
			"a disk with 35% free warned; the mac's 50GB floor " +
				"leaked into the VM",
		)
	}

	// and a VM puffin could not read is not reported as full
	if _, warn := diskNote(VM{}); warn {
		t.Error("an unread disk warned")
	}
}

// docker reports sizes in whatever unit suits it, and the reclaimable column
// carries a percentage that is not part of the number.
func TestParseDockerSize(t *testing.T) {
	for in, want := range map[string]float64{
		"12.81GB":       12.81,
		"1.699GB (13%)": 1.699,
		"139.3MB":       139.3 / 1024,
		"0B":            0,
		"":              0,
		"not a size":    0,
	} {
		got := parseDockerSize(in)
		if diff := got - want; diff > 0.001 || diff < -0.001 {
			t.Errorf(
				"parseDockerSize(%q) = %f, want %f",
				in,
				got,
				want,
			)
		}
	}
}

// Nothing puffin renders may be wider than the terminal it is rendered for.
//
// This is the "split pane sadness" bug and the cause was not where either of us
// looked.
//
// lipgloss pads every line of a multi-line render out to the widest one, so ONE
// over-long line -- the split's help footer, which lists both panes' keys --
// widened the entire frame past the window and the columns spilled off the
// edge.
//
// The panes were the suspects and they were innocent.
func TestNothingRendersWiderThanTheTerminal(t *testing.T) {
	for _, w := range []int{100, 140, 200} {
		m := newModel("test", "")
		m.width, m.height = w, 44
		m.scr = screenPane
		for k, mk := range paneFactories() {
			m.openPane = mk()
			for _, split := range []bool{false, true} {
				m.rightPane = nil
				if split {
					m.rightPane = mk()
				}
				for _, line := range strings.Split(
					paneView(m),
					"\n",
				) {
					if got := visibleWidth(line); got > w {
						t.Fatalf(
							"pane %q split=%v at "+
								"width %d "+
								"rendered a "+
								"%d-wide line",
							k,
							split,
							w,
							got,
						)
					}
				}
			}
		}
	}
}
