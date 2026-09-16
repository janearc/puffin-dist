package main

import (
	"fmt"
	"strings"
	"testing"
)

// cluster builds a pod list shaped like a real one: a noisy kube-system, a
// deployment with enough replicas to fold, and a pair that is not worth
// folding.
func cluster() []KubePod {
	p := func(ns, name string) KubePod {
		return KubePod{
			Namespace: ns,
			Name:      name,
			Phase:     "Running",
			Ready:     "1/1",
			AllReady:  true,
		}
	}
	return []KubePod{
		p("kube-system", "coredns-1"),
		p("kube-system", "svclb-traefik-a"),
		p("kube-system", "svclb-traefik-b"),
		p("kube-system", "svclb-traefik-c"),
		p("local", "athlete-6c9998d74b-a"),
		p("local", "athlete-6c9998d74b-b"),
		p("local", "athlete-6c9998d74b-c"),
		p("local", "dodo-1"),
		p("local", "flipr-1"),
	}
}

// The prefix rule, exactly as ruled: more than two of a `word-` fold; two do
// not, because a fold that saves one line is not worth a keystroke.
func TestFoldsOnlyWhatIsWorthFolding(t *testing.T) {
	rows := kubeRows(cluster(), defaultFolds(cluster()))
	var labels []string
	for _, r := range rows {
		labels = append(labels, r.Label)
	}
	got := strings.Join(labels, " ")
	for _, want := range []string{
		"kube-system",
		"svclb-",
		"local",
		"athlete-",
		"dodo-1",
		"flipr-1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q missing from %q", want, got)
		}
	}
	// the folded groups' members are gone, not merely invisible: the cursor
	// indexes rows, so a hidden-but-counted pod is an off-by-three
	for _, gone := range []string{
		"svclb-traefik-a",
		"athlete-6c9998d74b-a",
	} {
		if strings.Contains(got, gone) {
			t.Fatalf("%q survived its fold: %q", gone, got)
		}
	}
	// coredns is alone under its prefix and is shown as itself
	if !strings.Contains(got, "coredns-1") {
		t.Fatalf("a lone pod was folded away: %q", got)
	}
}

// A fold hides pods; it must not hide their condition. The header carries the
// rollup, or a pod that is not up disappears behind a tidy screen.
//
// CrashLoopBackOff counts as waiting rather than broken -- the screen's
// existing classification, a not-ready pod is waiting -- and the point here is
// that the fold reports it at all.
func TestAFoldNeverHidesABrokenPod(t *testing.T) {
	pods := cluster()
	pods[4].Phase, pods[4].AllReady, pods[4].Ready =
		"CrashLoopBackOff", false, "0/1"
	var group kubeRow
	for _, r := range kubeRows(pods, defaultFolds(pods)) {
		if r.Kind == rowGroup && r.Label == "athlete-" {
			group = r
		}
	}
	if group.Count != 3 ||
		group.Health[podWaiting]+group.Health[podBroken] != 1 {
		t.Fatalf("the fold did not carry the broken pod: %+v", group)
	}
	chip := healthChip(Compile(DodoDark()), group.Health)
	if !strings.Contains(chip, "waiting") {
		t.Fatalf(
			"the header does not report the pod that is not up: %q",
			chip,
		)
	}
	if !strings.Contains(chip, "2 "+
		"up") {
		t.Fatalf("the header lost the healthy count: %q", chip)
	}
}

// Terminal pods count as neither up nor broken, on a header for the same
// reason they do in the headline: Succeeded finished, it did not fail.
func TestTerminalPodsAreNotBroken(t *testing.T) {
	pods := cluster()
	for i := 4; i < 7; i++ {
		pods[i].Phase, pods[i].Terminal, pods[i].AllReady =
			"Succeeded", true, false
	}
	for _, r := range kubeRows(pods, defaultFolds(pods)) {
		if r.Kind == rowGroup && r.Label == "athlete-" &&
			r.Health[podUp]+r.Health[podWaiting]+
				r.Health[podBroken]+r.Health[podFailed] != 0 {
			t.Fatalf(
				"finished pods counted as something: %+v",
				r.Health,
			)
		}
	}
}

// space toggles, right only opens, h collapses the thing the cursor is in
// and leaves the cursor on what closed.
func TestFoldKeys(t *testing.T) {
	m := baseModel()
	m.scr = screenKube
	next, _ := m.Update(
		kubeFetched{
			v: KubeView{Context: homeContext(), Pods: cluster()},
		},
	)
	m = next.(model)

	// walk to the athlete- group and open it
	target := rowIndex(m.kubeVisible(), "local/athlete")
	if target == 0 {
		t.Fatal("no athlete- group")
	}
	m.kubeCursor = target
	before := len(m.kubeVisible())
	next, _ = m.Update(key(' '))
	m = next.(model)
	if len(m.kubeVisible()) != before+3 {
		t.Fatalf(
			"space did not unfold: %d -> %d",
			before,
			len(m.kubeVisible()),
		)
	}
	next, _ = m.Update(key(' '))
	m = next.(model)
	if len(m.kubeVisible()) != before {
		t.Fatalf("space did not fold back: %d", len(m.kubeVisible()))
	}

	// h from a pod collapses its namespace and lands the cursor on it
	m.kubeCursor = rowIndex(m.kubeVisible(), "local/dodo-1")
	next, _ = m.Update(key('h'))
	m = next.(model)
	rows := m.kubeVisible()
	if rows[m.kubeCursor].Key != "local" || !rows[m.kubeCursor].Folded {
		t.Fatalf(
			"h did not collapse into the namespace: %+v",
			rows[m.kubeCursor],
		)
	}
	for _, r := range rows {
		if r.Kind == rowPod && r.NS == "local" {
			t.Fatal("a collapsed namespace still shows its pods")
		}
	}
}

// The lifecycle keys refuse a header rather than acting on whatever pod
// happens to be nearby.
func TestLifecycleKeysRefuseAFold(t *testing.T) {
	m := baseModel()
	m.scr = screenKube
	next, _ := m.Update(
		kubeFetched{
			v: KubeView{Context: homeContext(), Pods: cluster()},
		},
	)
	m = next.(model)
	m.kubeCursor = 0 // the kube-system header
	for _, k := range []rune{'x', 's', 'R', 'l'} {
		next, cmd := m.Update(key(k))
		got := next.(model)
		if cmd != nil || got.kubeBusy {
			t.Fatalf("%c acted on a header", k)
		}
		if !strings.Contains(got.kubeNote, "not a pod") {
			t.Fatalf("%c said %q", k, got.kubeNote)
		}
	}
}

// The window keeps the cursor on screen and otherwise holds still: a list
// that re-centres on every keystroke is unreadable.
func TestWindowOffset(t *testing.T) {
	if got := windowOffset(100, 0, 10, 0); got != 0 {
		t.Fatalf("top: %d", got)
	}
	if got := windowOffset(100, 12, 10, 0); got != 3 {
		t.Fatalf("cursor past the bottom edge: %d", got)
	}
	if got := windowOffset(100, 5, 10, 3); got != 3 {
		t.Fatalf(
			"the window moved for a cursor already inside it: %d",
			got,
		)
	}
	if got := windowOffset(100, 2, 10, 5); got != 2 {
		t.Fatalf("cursor above the top edge: %d", got)
	}
	if got := windowOffset(100, 99, 10, 0); got != 90 {
		t.Fatalf("the end: %d", got)
	}
	if got := windowOffset(4, 3, 10, 0); got != 0 {
		t.Fatalf(
			"a list shorter than the window must not scroll: %d",
			got,
		)
	}
}

// A cluster taller than the terminal renders a window, not the whole list,
// and says how much is off each edge.
func TestTallClusterIsWindowed(t *testing.T) {
	var pods []KubePod
	for i := 0; i < 60; i++ {
		pods = append(
			pods,
			KubePod{
				Namespace: "local",
				Name:      string(rune('a'+i%26)) + "pod",
				Phase:     "Running",
				Ready:     "1/1",
				AllReady:  true,
			},
		)
	}
	m := baseModel()
	m.scr, m.height, m.width = screenKube, 24, 120
	next, _ := m.Update(
		kubeFetched{v: KubeView{Context: homeContext(), Pods: pods}},
	)
	m = next.(model)
	m.kubeCursor = len(m.kubeVisible()) - 1
	m.kubeOffset = windowOffset(
		len(m.kubeVisible()),
		m.kubeCursor,
		m.kubeWindow(),
		m.kubeOffset,
	)
	out := kubeView(m)
	if strings.Count(out, "\n") > m.height {
		t.Fatalf(
			"the screen is %d lines for a %d-line terminal",
			strings.Count(out, "\n"),
			m.height,
		)
	}
	if !strings.Contains(out, "above") {
		t.Fatalf(
			"the window does not say what is off the top:\n%s",
			out,
		)
	}
	if !strings.Contains(out, "█") {
		t.Fatal("no scrollbar on a cluster that does not fit")
	}
}

// A contract too tall for the terminal is windowed, and the method under
// the cursor stays on screen -- the example packet below it is the reason
// anyone opened the screen.
func TestBigContractIsWindowed(t *testing.T) {
	var meths []APIMethod
	for i := 0; i < 40; i++ {
		meths = append(meths, APIMethod{
			Name: "Method" + string(
				rune('A'+i%26),
			) + string(
				rune('0'+i/26),
			),
			Input: "In", Output: "Out", InputFQN: "pkg.In"})
	}
	m := baseModel()
	m.scr, m.height, m.width = screenDetail, 24, 120
	m.apiFor = "kingfisher"
	m.api = &API{Services: []APIService{{Name: "pkg.Big", Methods: meths}}}
	m.methodCursor = 39
	m.detailOffset = windowOffset(
		len(detailRows(m.api)),
		detailRowOf(
			detailRows(m.api),
			m.methodCursor,
		),
		m.detailWindow(),
		0,
	)
	out := detailView(m)
	if strings.Count(out, "\n") > m.height {
		t.Fatalf(
			"%d lines for a %d-line terminal",
			strings.Count(out, "\n"),
			m.height,
		)
	}
	if !strings.Contains(out, meths[39].Name) {
		t.Fatal("the method under the cursor is off the screen")
	}
	// the count is in a note now, and a bar runs down the right edge: a
	// window that only speaks when you reach an end reads as a list that
	// silently stopped, which is what "scrolling doesn't work" looks like
	// from the outside
	if !strings.Contains(out, "above") {
		t.Fatalf("the window does not say what it is hiding:\n%s", out)
	}
	if !strings.Contains(out, "█") {
		t.Fatal("no scrollbar on a contract that does not fit")
	}
}

// The cluster screen shows ONE cluster and must say which, and what it is
// leaving out. Looking for a service that lives elsewhere and finding an
// empty list reads as an outage -- which is how a healthy prod cluster got
// reported as down.
func TestTheClusterScreenNamesWhatItIsNotShowing(t *testing.T) {
	m := baseModel()
	m.scr, m.width, m.height = screenKube, 140, 44
	m.kube = KubeView{Context: homeContext(), Pods: []KubePod{
		{
			Namespace: "local",
			Name:      "athlete-1",
			Phase:     "Running",
			Ready:     "1/1",
			AllReady:  true,
		},
	}}
	m.kubeFold = defaultFolds(m.kube.Pods)
	m.clusters = []Cluster{
		{Name: "local", ServersRunning: 1, ServersCount: 1},
		{Name: "fleet", ServersRunning: 1, ServersCount: 1},
	}
	out := kubeView(m)
	if !strings.Contains(out, homeContext()) {
		t.Fatal("the screen does not name the cluster it is showing")
	}
	if !strings.Contains(out, "not shown") ||
		!strings.Contains(out, "fleet") {
		t.Fatalf(
			"the screen does not name the cluster it is "+
				"hiding:\n%s",
			out,
		)
	}
	// and it says whether the one it is not showing is alive, because "not
	// shown" without a state invites the same wrong conclusion
	if !strings.Contains(out, "fleet (up)") {
		t.Fatalf("the hidden cluster's state is not given:\n%s", out)
	}
	// one cluster and nothing to disclaim: no line at all
	m.clusters = []Cluster{
		{Name: "local", ServersRunning: 1, ServersCount: 1},
	}
	if strings.Contains(kubeView(m), "not shown") {
		t.Fatal("a disclaimer was shown with nothing to disclaim")
	}
}

// A key that silently does nothing is a key the operator concludes is
// broken. The screen opens with the cursor on a POD, space has nothing to
// fold there, and the first cut just returned -- so folds looked unopenable.
func TestSpaceOnAPodSaysWhy(t *testing.T) {
	m := baseModel()
	m.scr, m.width, m.height = screenKube, 140, 44
	next, _ := m.Update(
		kubeFetched{
			v: KubeView{Context: homeContext(), Pods: cluster()},
		},
	)
	m = next.(model)
	// it opens on a pod, which is the whole problem
	if m.kubeVisible()[m.kubeCursor].Kind != rowPod {
		t.Fatal(
			"the screen no longer opens on a pod; this test's " +
				"premise is stale",
		)
	}
	next, _ = m.Update(key(' '))
	m = next.(model)
	if !strings.Contains(m.kubeNote, "pod") {
		t.Fatalf("space on a pod said nothing: %q", m.kubeNote)
	}
	// and on a fold it still works, and says nothing
	m.kubeCursor = rowIndex(m.kubeVisible(), "local/athlete")
	before := len(m.kubeVisible())
	next, _ = m.Update(key(' '))
	m = next.(model)
	if len(m.kubeVisible()) <= before {
		t.Fatal("space on a fold did not open it")
	}
	if m.kubeNote != "" {
		t.Fatalf("a working keystroke left a complaint: %q", m.kubeNote)
	}
}

// Every pod under a header is accounted for. Three kestrel jobs sat in
// Error behind a fold that reported nothing about them, because Failed and
// Succeeded were both "terminal" and terminal was counted nowhere.
func TestAFailedJobIsNotHiddenByAFold(t *testing.T) {
	pods := []KubePod{
		{
			Namespace: "local",
			Name:      "kestrel-e2e-1",
			Phase:     "Failed",
			Terminal:  true,
		},
		{
			Namespace: "local",
			Name:      "kestrel-e2e-2",
			Phase:     "Failed",
			Terminal:  true,
		},
		{
			Namespace: "local",
			Name:      "kestrel-e2e-3",
			Phase:     "Succeeded",
			Terminal:  true,
		},
	}
	var group kubeRow
	for _, r := range kubeRows(pods, defaultFolds(pods)) {
		if r.Kind == rowGroup {
			group = r
		}
	}
	if group.Count != 3 {
		t.Fatalf("group: %+v", group)
	}
	if group.Health[podFailed] != 2 || group.Health[podDone] != 1 {
		t.Fatalf("failed jobs were not counted: %+v", group.Health)
	}
	// the buckets account for every pod under the header
	sum := 0
	for _, n := range group.Health {
		sum += n
	}
	if sum != group.Count {
		t.Fatalf("%d pods but %d accounted for", group.Count, sum)
	}
	chip := healthChip(Compile(DodoDark()), group.Health)
	if !strings.Contains(chip, "2 failed") {
		t.Fatalf("the header does not report the failures: %q", chip)
	}
}

// A scrollbar is drawn only when something is hidden, and its thumb is
// where the view is. A track that cannot move is furniture.
func TestScrollbarSaysWhereYouAre(t *testing.T) {
	// nothing hidden, nothing drawn
	for _, cell := range scrollbar(5, 10, 0) {
		if cell != " " {
			t.Fatalf(
				"a bar was drawn for a list that fits: %q",
				cell,
			)
		}
	}
	top := scrollbar(100, 10, 0)
	if top[0] != "█" || top[9] != "│" {
		t.Fatalf("at the top the thumb is not at the top: %v", top)
	}
	bottom := scrollbar(100, 10, 90)
	if bottom[9] != "█" || bottom[0] != "│" {
		t.Fatalf(
			"at the bottom the thumb is not at the bottom: %v",
			bottom,
		)
	}
	// the thumb is proportional: a long list gets a short thumb
	long, short := 0, 0
	for _, c := range scrollbar(1000, 10, 0) {
		if c == "█" {
			long++
		}
	}
	for _, c := range scrollbar(20, 10, 0) {
		if c == "█" {
			short++
		}
	}
	if long >= short {
		t.Fatalf(
			"thumb does not shrink with the list: %d vs %d",
			long,
			short,
		)
	}
	if long < 1 {
		t.Fatal("the thumb vanished entirely on a very long list")
	}
}

// The note gives numbers, because "more below" does not tell you whether it
// is two more or two hundred.
func TestScrollNoteCounts(t *testing.T) {
	if got := scrollNote(10, 10, 0); got != "" {
		t.Fatalf("a note for a list that fits: %q", got)
	}
	if got := scrollNote(100, 10, 0); got != "90 below" {
		t.Fatalf("%q", got)
	}
	if got := scrollNote(100, 10, 90); got != "90 above" {
		t.Fatalf("%q", got)
	}
	if got := scrollNote(100, 10, 45); got != "45 above · 45 below" {
		t.Fatalf("%q", got)
	}
}

// The wheel scrolls the older screens, which it did not: mouse handling was
// built for the panes, so on the contract screen the wheel silently did
// nothing -- which reads as a broken scroll rather than an absent one.
func TestWheelScrollsLegacyScreens(t *testing.T) {
	m := baseModel()
	m.width, m.height = 140, 30
	var meths []APIMethod
	for i := 0; i < 40; i++ {
		meths = append(
			meths,
			APIMethod{
				Name:     fmt.Sprintf("Method%02d", i),
				InputFQN: "pkg.In",
			},
		)
	}
	m.scr, m.api, m.apiFor = screenDetail, &API{
		Services: []APIService{{Name: "pkg.Big", Methods: meths}},
	}, "kingfisher"
	down := m.scrollLegacy(1)
	if down.methodCursor != 1 {
		t.Fatalf(
			"the wheel did not move the contract cursor: %d",
			down.methodCursor,
		)
	}
	// and it cannot be scrolled off either end
	up := m.scrollLegacy(-1)
	if up.methodCursor != 0 {
		t.Fatalf("scrolled above the first method: %d", up.methodCursor)
	}
	far := m
	for i := 0; i < 100; i++ {
		far = far.scrollLegacy(1)
	}
	if far.methodCursor != len(meths)-1 {
		t.Fatalf("scrolled past the last method: %d", far.methodCursor)
	}
	// an empty screen does not panic
	empty := baseModel()
	empty.scr = screenKube
	empty.scrollLegacy(1)
}

// the bar stays in its column whatever the row does. This is the "scrollbars
// are a little messy" bug: rows longer than the pad column pushed the track
// right, and the bar came out as two ragged columns instead of one edge.
func TestTrackStaysInItsColumn(t *testing.T) {
	short := "svc"
	long := strings.Repeat("x", 120)
	cursorish := "kingfisher-ingestd-7f9c   12 pods   space folds"
	for _, line := range []string{short, long, cursorish, ""} {
		got := trackAt(line, 74, "│")
		w := 0
		for i, r := range []rune(got) {
			if r == '│' {
				w = i
				break
			}
		}
		if w != 74 {
			t.Fatalf(
				"track landed at column %d for a %d-wide row",
				w,
				len([]rune(line)),
			)
		}
	}
}
