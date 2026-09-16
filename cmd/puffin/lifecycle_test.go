package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// The lifecycle layer: the pod-to-workload walk, the context guard, and the
// confirmation the TUI puts in front of anything that takes a service down.
//
// The kubectl calls themselves are not fakeable without pretending, so the
// reductions are tested and the execs are not -- the same line DESIGN.md
// already draws around fetchKube.

// podJSON is a pod owned by a ReplicaSet, the common case.
const podJSON = `{"metadata":{"namespace":"local",` +
	`"name":"athlete-6c9998d74b-rflj9",` + "\n" +
	` "ownerReferences":[{"kind":"ReplicaSet",` +
	`"name":"athlete-6c9998d74b","controller":true}]}}`

// A pod has no start or stop. What moves is the controller behind it, and
// finding that is the first step of every lifecycle verb.
func TestWorkloadFromPodFindsController(t *testing.T) {
	w, err := workloadFromPod([]byte(podJSON), "default")
	if err != nil {
		t.Fatal(err)
	}
	if w.Kind != "ReplicaSet" || w.Name != "athlete-6c9998d74b" {
		t.Fatalf("owner: %+v", w)
	}
	// the pod's own namespace wins over the one passed in
	if w.Namespace != "local" {
		t.Fatalf("namespace: %q", w.Namespace)
	}
}

// A pod created directly has no controller, and there is nothing to scale.
// Saying so plainly beats scaling something adjacent.
func TestWorkloadFromPodRefusesOrphan(t *testing.T) {
	_, err := workloadFromPod(
		[]byte(`{"metadata":{"namespace":"local"}}`),
		"local",
	)
	if err == nil {
		t.Fatal("an orphan pod was accepted as scalable")
	}
	if !strings.Contains(err.Error(), "no controller") {
		t.Fatalf("unhelpful refusal: %v", err)
	}
}

// Only the controlling reference counts; a pod can carry others.
func TestControllerOfIgnoresNonControllers(t *testing.T) {
	no := false
	yes := true
	refs := []ownerRef{
		{Kind: "Bystander", Name: "b", Controller: &no},
		{Kind: "ReplicaSet", Name: "rs", Controller: &yes},
	}
	got, ok := controllerOf(refs)
	if !ok || got.Kind != "ReplicaSet" {
		t.Fatalf("picked %+v (ok=%v)", got, ok)
	}
	if _, ok := controllerOf(refs[:1]); ok {
		t.Fatal("a non-controller was treated as the controller")
	}
}

// The context is the dev/prod boundary on this machine. Reads are free;
// anything that can take a service down is not.
func TestGuardContextHoldsTheProdBoundary(t *testing.T) {
	os.Unsetenv(mutationEnvVar)
	if err := guardContext(homeContext()); err != nil {
		t.Fatalf("the dev network was refused: %v", err)
	}
	err := guardContext("k3d-fleet")
	if err == nil {
		t.Fatal("a foreign context was allowed to mutate")
	}
	if !strings.Contains(err.Error(), "k3d-fleet") {
		t.Fatalf("the refusal does not name the context: %v", err)
	}
	t.Setenv(mutationEnvVar, "1")
	if err := guardContext("k3d-fleet"); err != nil {
		t.Fatalf("the deliberate override did not work: %v", err)
	}
}

// A DaemonSet does not scale, and puffin does not guess what stopping one
// should mean.
func TestStopRefusesUnscalable(t *testing.T) {
	t.Setenv(mutationEnvVar, "1")
	w := KubeWorkload{
		Kind:      "DaemonSet",
		Name:      "svclb",
		Namespace: "kube-system",
	}
	if err := StopWorkload(homeContext(), w); err == nil {
		t.Fatal("a daemonset was scaled")
	}
	if err := StartWorkload(homeContext(), w); err == nil {
		t.Fatal("a daemonset was started")
	}
}

// Stopping something already stopped, and starting something already up,
// are both no-ops worth saying out loud rather than silently re-scaling.
func TestScaleRefusesRedundantMoves(t *testing.T) {
	up := KubeWorkload{
		Kind:      "Deployment",
		Name:      "athlete",
		Namespace: "local",
		Replicas:  2,
		Scalable:  true,
	}
	down := KubeWorkload{
		Kind:      "Deployment",
		Name:      "athlete",
		Namespace: "local",
		Replicas:  0,
		Scalable:  true,
	}
	if err := StartWorkload(homeContext(), up); err == nil ||
		!strings.Contains(err.Error(), "already running") {
		t.Fatalf("starting a running workload: %v", err)
	}
	if err := StopWorkload(homeContext(), down); err == nil ||
		!strings.Contains(err.Error(), "already stopped") {
		t.Fatalf("stopping a stopped workload: %v", err)
	}
}

// The screen says what happened in kubectl's own spelling for the target,
// and in the past tense for what is finished, so a report is not mistaken
// for an intention.
func TestTargetAndPastTense(t *testing.T) {
	w := KubeWorkload{Kind: "StatefulSet", Name: "kafka"}
	if w.Target() != "statefulset/kafka" {
		t.Fatalf("target: %q", w.Target())
	}
	for verb, want := range map[string]string{
		"stop": "stopped", "start": "started", "restart": "restarting",
	} {
		if got := pastTense(verb); got != want {
			t.Fatalf("%s -> %q, want %q", verb, got, want)
		}
	}
}

// The annotation key carries dots and a slash, both of which jsonpath reads
// as structure unless they are escaped.
func TestEscapeJSONPath(t *testing.T) {
	got := escapeJSONPath("puffin.janearc.dev/previous-replicas")
	if got != `puffin\.janearc\.dev\/previous-replicas` {
		t.Fatalf("escaped: %s", got)
	}
}

// A verb with no -n has to land somewhere, and the somewhere is one
// setting rather than a different guess in each caller.
func TestNSFlagDefaultsToTheConfiguredNamespace(t *testing.T) {
	ns, rest := nsFlag([]string{"athlete"})
	if ns != defaultNamespace() || len(rest) != 1 || rest[0] != "athlete" {
		t.Fatalf("ns=%q rest=%v", ns, rest)
	}
	ns, rest = nsFlag([]string{"-n", "kube-system", "coredns"})
	if ns != "kube-system" || len(rest) != 1 || rest[0] != "coredns" {
		t.Fatalf("ns=%q rest=%v", ns, rest)
	}
}

// kubeModel puts the operator on the cluster screen with two pods, arriving
// the way the screen really arrives -- through kubeFetched, so the fold set
// and the opening cursor are the ones the operator would get.
func kubeModel() model {
	m := baseModel()
	m.scr = screenKube
	next, _ := m.Update(
		kubeFetched{v: KubeView{Context: homeContext(), Pods: []KubePod{
			{
				Namespace: "local",
				Name:      "athlete-1",
				Phase:     "Running",
				Ready:     "1/1",
				AllReady:  true,
			},
			{
				Namespace: "local",
				Name:      "dodo-1",
				Phase:     "Running",
				Ready:     "1/1",
				AllReady:  true,
			},
		}}},
	)
	return next.(model)
}

// j/k move the cursor on the cluster screen, over ROWS -- headers included,
// pods that folded away excluded. The screen opens on the first pod, which
// is the thing an operator came to act on.
func TestKubeCursorMoves(t *testing.T) {
	m := kubeModel()
	rows := m.kubeVisible()
	if rows[m.kubeCursor].Kind != rowPod ||
		rows[m.kubeCursor].Label != "athlete-1" {
		t.Fatalf("opened on %+v, not the first pod", rows[m.kubeCursor])
	}
	start := m.kubeCursor
	next, _ := m.Update(key('j'))
	m = next.(model)
	if m.kubeCursor != start+1 {
		t.Fatalf("cursor: %d", m.kubeCursor)
	}
	next, _ = m.Update(key('k'))
	m = next.(model)
	if m.kubeCursor != start {
		t.Fatalf("cursor: %d", m.kubeCursor)
	}
}

// Nothing disruptive happens on one keystroke: x asks, and only y acts.
func TestStopAsksBeforeItActs(t *testing.T) {
	m := kubeModel()
	next, cmd := m.Update(key('x'))
	m = next.(model)
	if cmd == nil || !m.kubeBusy {
		t.Fatal("x did not go resolve the workload")
	}
	if m.kubeAsk != nil {
		t.Fatal("x asked before it knew what it would change")
	}
	// the resolve comes back and the confirmation appears, naming the
	// workload rather than the pod
	next, _ = m.Update(
		kubeResolved{verb: "stop", pod: "athlete-1", w: KubeWorkload{
			Kind:      "Deployment",
			Name:      "athlete",
			Namespace: "local",
			Replicas:  1,
			Scalable:  true}},
	)
	m = next.(model)
	if m.kubeAsk == nil || m.kubeAsk.verb != "stop" {
		t.Fatal("no confirmation was raised")
	}
	if !strings.Contains(kubeView(m), "deployment/athlete") {
		t.Fatal(
			"the confirmation does not name the workload that " +
				"moves",
		)
	}
	// anything that is not y is no
	next, cmd = m.Update(key('n'))
	m = next.(model)
	if m.kubeAsk != nil || cmd != nil {
		t.Fatal("n did not cancel")
	}
	if m.kubeNote != "cancelled" {
		t.Fatalf("note: %q", m.kubeNote)
	}
}

// y is the one keystroke that acts.
func TestConfirmActs(t *testing.T) {
	m := kubeModel()
	m.kubeAsk = &kubeAction{verb: "stop", pod: "athlete-1", w: KubeWorkload{
		Kind: "Deployment", Name: "athlete",
		Namespace: "local", Replicas: 1, Scalable: true}}
	_, cmd := m.Update(key('y'))
	if cmd == nil {
		t.Fatal("y did not act")
	}
}

// A resolve that fails leaves no pending action behind to be confirmed by a
// later stray keystroke.
func TestFailedResolveClearsTheAsk(t *testing.T) {
	m := kubeModel()
	m.kubeBusy = true
	next, _ := m.Update(kubeResolved{verb: "stop", err: os.ErrNotExist})
	m = next.(model)
	if m.kubeAsk != nil || m.kubeBusy {
		t.Fatal("a failed resolve left a pending action")
	}
	if m.kube.Err == "" {
		t.Fatal("a failed resolve said nothing")
	}
}

// l opens the logs in the shared pager, and esc comes back to the cluster
// screen rather than to the composer.
func TestLogsOpenInPagerAndReturnToKube(t *testing.T) {
	m := kubeModel()
	next, cmd := m.Update(key('l'))
	m = next.(model)
	if cmd == nil || !m.kubeBusy {
		t.Fatal("l did not fetch logs")
	}
	next, _ = m.Update(
		logsFetched{
			pod:  "athlete-1",
			body: "kestrel e2e -- live local",
		},
	)
	m = next.(model)
	if m.scr != screenPager || m.pagerFrom != screenKube {
		t.Fatalf("screen=%v from=%v", m.scr, m.pagerFrom)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if m.scr != screenKube {
		t.Fatalf("esc landed on %v, not the cluster screen", m.scr)
	}
}

// A finished action refetches: the cluster is the truth, the screen borrows
// it -- the same rule the flag editor follows.
func TestActedRefetches(t *testing.T) {
	m := kubeModel()
	m.kubeAsk = &kubeAction{verb: "stop"}
	next, cmd := m.Update(kubeActed{note: "stopped deployment/athlete"})
	m = next.(model)
	if cmd == nil {
		t.Fatal("a finished action did not refetch the cluster")
	}
	if m.kubeAsk != nil || m.kubeNote != "stopped deployment/athlete" {
		t.Fatalf("ask=%v note=%q", m.kubeAsk, m.kubeNote)
	}
}

// A refetch that returns fewer pods must not leave the cursor past the end.
func TestCursorSurvivesAShrinkingCluster(t *testing.T) {
	m := kubeModel()
	m.kubeCursor = len(m.kubeVisible()) - 1
	next, _ := m.Update(
		kubeFetched{v: KubeView{Context: homeContext(), Pods: []KubePod{
			{Namespace: "local", Name: "dodo-1", Phase: "Running"},
		}}},
	)
	m = next.(model)
	if m.kubeCursor >= len(m.kubeVisible()) {
		t.Fatalf(
			"cursor left past the end: %d of %d",
			m.kubeCursor,
			len(m.kubeVisible()),
		)
	}
}

// fakeKubectl swaps the runner for one that records what was asked and
// answers from a table keyed by a substring of the command.
type fakeKubectl struct {
	calls   [][]string
	answers map[string]string
	fail    map[string]bool
}

// install puts the fake in front of the real binary for one test and
// takes it away again, so a suite cannot leak a stub into the next file.
func (f *fakeKubectl) install(t *testing.T) {
	t.Helper()
	prev := kubectlRunner
	t.Cleanup(func() { kubectlRunner = prev })
	kubectlRunner = func(args ...string) ([]byte, error) {
		f.calls = append(f.calls, args)
		joined := strings.Join(args, " ")
		for k := range f.fail {
			if strings.Contains(joined, k) {
				return nil, os.ErrNotExist
			}
		}
		for k, v := range f.answers {
			if strings.Contains(joined, k) {
				return []byte(v), nil
			}
		}
		return []byte(""), nil
	}
}

// verbs returns the kubectl verb of each recorded call, in order.
func (f *fakeKubectl) verbs() []string {
	var out []string
	for _, c := range f.calls {
		for _, a := range c {
			switch a {
			case "annotate", "scale", "get", "logs", "rollout":
				out = append(out, a)
			}
			if a == "annotate" || a == "scale" || a == "logs" ||
				a == "rollout" {
				break
			}
		}
	}
	return out
}

// The ordering that matters: the replica count is recorded before the
// workload goes to zero. Reversed, the annotation records zero and the
// service can never be started back to what it was running.
func TestStopRecordsBeforeItScales(t *testing.T) {
	f := &fakeKubectl{}
	f.install(t)
	w := KubeWorkload{
		Kind:      "Deployment",
		Name:      "athlete",
		Namespace: "local",
		Replicas:  3,
		Scalable:  true,
	}
	if err := StopWorkload(homeContext(), w); err != nil {
		t.Fatal(err)
	}
	got := f.verbs()
	if len(got) != 2 || got[0] != "annotate" || got[1] != "scale" {
		t.Fatalf("order was %v, want annotate then scale", got)
	}
	if !strings.Contains(strings.Join(f.calls[0], " "), "=3") {
		t.Fatalf("recorded the wrong count: %v", f.calls[0])
	}
	if !strings.Contains(strings.Join(f.calls[1], " "), "--replicas=0") {
		t.Fatalf("did not scale to zero: %v", f.calls[1])
	}
	// the context is explicit on every call, always
	for _, c := range f.calls {
		if c[0] != "--context" || c[1] != homeContext() {
			t.Fatalf(
				"a call went out without an explicit "+
					"context: %v",
				c,
			)
		}
	}
}

// Start restores the recorded count, not a hardcoded one.
func TestStartRestoresTheRecordedCount(t *testing.T) {
	f := &fakeKubectl{
		answers: map[string]string{
			"jsonpath={.metadata.annotations": "3",
		},
	}
	f.install(t)
	w := KubeWorkload{
		Kind:      "Deployment",
		Name:      "athlete",
		Namespace: "local",
		Replicas:  0,
		Scalable:  true,
	}
	if err := StartWorkload(homeContext(), w); err != nil {
		t.Fatal(err)
	}
	last := strings.Join(f.calls[len(f.calls)-1], " ")
	if !strings.Contains(last, "--replicas=3") {
		t.Fatalf("started at the wrong count: %s", last)
	}
}

// No annotation means one replica -- a visible guess, not a crash.
func TestStartWithoutAnAnnotationUsesOne(t *testing.T) {
	f := &fakeKubectl{}
	f.install(t)
	w := KubeWorkload{
		Kind:      "Deployment",
		Name:      "athlete",
		Namespace: "local",
		Replicas:  0,
		Scalable:  true,
	}
	if err := StartWorkload(homeContext(), w); err != nil {
		t.Fatal(err)
	}
	last := strings.Join(f.calls[len(f.calls)-1], " ")
	if !strings.Contains(last, "--replicas=1") {
		t.Fatalf("want one replica, got: %s", last)
	}
}

// An unreadable annotation is not a reason to fail to start.
func TestStartSurvivesAnUnreadableAnnotation(t *testing.T) {
	f := &fakeKubectl{
		answers: map[string]string{
			"jsonpath={.metadata.annotations": "not-a-number",
		},
	}
	f.install(t)
	w := KubeWorkload{
		Kind:      "Deployment",
		Name:      "athlete",
		Namespace: "local",
		Replicas:  0,
		Scalable:  true,
	}
	if err := StartWorkload(homeContext(), w); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(
		strings.Join(f.calls[len(f.calls)-1], " "),
		"--replicas=1",
	) {
		t.Fatal("an unreadable annotation should fall back to one")
	}
}

// The walk stops at the Deployment, never at the ReplicaSet: scaling a
// ReplicaSet is undone by its Deployment within seconds.
func TestResolveWorkloadWalksPastTheReplicaSet(t *testing.T) {
	f := &fakeKubectl{answers: map[string]string{
		"get pod": podJSON,
		"get replicaset": `{"metadata":{"ownerReferences":[
		 {"kind":"Deployment","name":"athlete","controller":true}]}}`,
		"jsonpath={.spec.replicas}": "2",
	}}
	f.install(t)
	w, err := resolveWorkload(
		homeContext(),
		"local",
		"athlete-6c9998d74b-rflj9",
	)
	if err != nil {
		t.Fatal(err)
	}
	if w.Kind != "Deployment" || w.Name != "athlete" {
		t.Fatalf("walked to %+v", w)
	}
	if !w.Scalable || w.Replicas != 2 {
		t.Fatalf("scalable=%v replicas=%d", w.Scalable, w.Replicas)
	}
}

// A DaemonSet pod resolves, but is marked unscalable rather than refused --
// the screen still wants to show what owns it.
func TestResolveWorkloadMarksDaemonSetUnscalable(t *testing.T) {
	f := &fakeKubectl{answers: map[string]string{
		"get pod": `{"metadata":{"namespace":"kube-system",` +
			`"ownerReferences":[` + "\n" +
			`		 {"kind":"DaemonSet","name":"svclb",` +
			`"controller":true}]}}`,
	}}
	f.install(t)
	w, err := resolveWorkload(homeContext(), "kube-system", "svclb-x")
	if err != nil {
		t.Fatal(err)
	}
	if w.Kind != "DaemonSet" || w.Scalable {
		t.Fatalf("%+v", w)
	}
}

// The shell has no pod to point at when a service is stopped, so a bare
// workload name has to resolve too.
func TestResolveTargetFallsBackToTheWorkloadName(t *testing.T) {
	f := &fakeKubectl{
		fail:    map[string]bool{"get pod": true},
		answers: map[string]string{"get deployment": "2"},
	}
	f.install(t)
	w, err := ResolveTarget(homeContext(), "local", "athlete")
	if err != nil {
		t.Fatal(err)
	}
	if w.Kind != "Deployment" || w.Name != "athlete" || w.Replicas != 2 {
		t.Fatalf("%+v", w)
	}
}

// A name that resolves to nothing has to come back naming the thing that
// was not found: an error that does not is a second search for the reader.
func TestResolveTargetSaysWhenNothingMatches(t *testing.T) {
	f := &fakeKubectl{fail: map[string]bool{"get": true}}
	f.install(t)
	if _, err := ResolveTarget(
		homeContext(),
		"local",
		"nope",
	); err == nil ||
		!strings.Contains(err.Error(), "nope") {
		t.Fatalf("unhelpful: %v", err)
	}
}

// An empty log is an answer, not a blank screen.
func TestPodLogsNamesTheSilence(t *testing.T) {
	f := &fakeKubectl{answers: map[string]string{"logs": "   \n"}}
	f.install(t)
	body, err := PodLogs(homeContext(), "local", "athlete-1", 10, false)
	if err != nil {
		t.Fatal(err)
	}
	if body != "(no output)" {
		t.Fatalf("body: %q", body)
	}
}

// -p asks for the dead container's log, which is the one that says why a
// pod is restarting.
func TestPodLogsPassesPrevious(t *testing.T) {
	f := &fakeKubectl{answers: map[string]string{"logs": "boom"}}
	f.install(t)
	if _, err := PodLogs(
		homeContext(),
		"local",
		"athlete-1",
		50,
		true,
	); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(f.calls[0], " ")
	if !strings.Contains(joined, "--previous") ||
		!strings.Contains(joined, "--tail=50") {
		t.Fatalf("call: %s", joined)
	}
}

// Restart rolls rather than scaling, so the replica count is left alone.
func TestRestartRolls(t *testing.T) {
	f := &fakeKubectl{}
	f.install(t)
	w := KubeWorkload{
		Kind:      "Deployment",
		Name:      "athlete",
		Namespace: "local",
		Replicas:  1,
		Scalable:  true,
	}
	if err := RestartWorkload(homeContext(), w); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(f.calls[0], " ")
	if !strings.Contains(joined, "rollout restart deployment/athlete") {
		t.Fatalf("call: %s", joined)
	}
}

// The guard sits in front of every mutation, so a foreign context reaches
// kubectl for reads and never for changes.
func TestGuardStopsMutationsBeforeKubectl(t *testing.T) {
	os.Unsetenv(mutationEnvVar)
	f := &fakeKubectl{}
	f.install(t)
	w := KubeWorkload{
		Kind:      "Deployment",
		Name:      "athlete",
		Namespace: "local",
		Replicas:  1,
		Scalable:  true,
	}
	for _, err := range []error{
		StopWorkload("k3d-fleet", w),
		StartWorkload("k3d-fleet", KubeWorkload{
			Kind:      "Deployment",
			Name:      "a",
			Namespace: "n",
			Scalable:  true,
		}),
		RestartWorkload("k3d-fleet", w),
	} {
		if err == nil {
			t.Fatal(
				"a mutation was allowed against a foreign " +
					"context",
			)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("kubectl was reached anyway: %v", f.calls)
	}
}

// The shell door, driven through the same swappable runner. These print
// what an operator reads at 03:20, so the words are the assertion.

// The body reaches stdout whole, and the tail is bounded, because an
// unbounded log at a shell prompt is a scroll nobody asked for.
func TestCliLogsPrintsTheBody(t *testing.T) {
	f := &fakeKubectl{
		answers: map[string]string{"logs": "fixture up\nstill up"},
	}
	f.install(t)
	out, code := capture(
		t,
		func() int { return cliLogs([]string{"athlete-1"}) },
	)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out, "still up") {
		t.Fatalf("out: %q", out)
	}
	// the namespace is the configured default, and the tail is bounded
	joined := strings.Join(f.calls[0], " ")
	if !strings.Contains(joined, "-n "+defaultNamespace()) ||
		!strings.Contains(joined, "--tail=500") {
		t.Fatalf("call: %s", joined)
	}
}

// The flags reach kubectl as given, including -p, which is the one that
// answers why a pod is restarting.
func TestCliLogsFlags(t *testing.T) {
	f := &fakeKubectl{answers: map[string]string{"logs": "x"}}
	f.install(t)
	_, code := capture(t, func() int {
		return cliLogs(
			[]string{
				"-n",
				"kube-system",
				"coredns-1",
				"--tail",
				"20",
				"-p",
			},
		)
	})
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	joined := strings.Join(f.calls[0], " ")
	for _, want := range []string{"-n " +
		"kube-system", "--tail=20", "--previous", "coredns-1"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("call %q is missing %q", joined, want)
		}
	}
}

// A bad argument is refused before the wire, with the usage line, rather
// than passed through for kubectl to complain about.
func TestCliLogsRefusesNonsense(t *testing.T) {
	f := &fakeKubectl{}
	f.install(t)
	if _, code := capture(
		t,
		func() int { return cliLogs(nil) },
	); code != 2 {
		t.Fatalf("no pod named: exit %d, want the usage code", code)
	}
	if _, code := capture(t, func() int {
		return cliLogs([]string{"pod", "--tail", "lots"})
	}); code != 2 {
		t.Fatalf("--tail lots: exit %d", code)
	}
	if len(f.calls) != 0 {
		t.Fatalf("kubectl was reached on a usage error: %v", f.calls)
	}
}

// The report names the workload and the namespace, because "stopped
// athlete" and "stopped deployment/athlete in local" are different amounts
// of information in an incident.
func TestCliLifecycleReportsWhatItDid(t *testing.T) {
	for _, tc := range []struct {
		verb, want string
	}{
		{"stop", "stopped deployment/athlete in " +
			"local"},
		{
			"restart",
			"restarting deployment/athlete in local",
		},
	} {
		f := &fakeKubectl{answers: map[string]string{
			"get pod": `{"metadata":{"namespace":"local",` +
				`"ownerReferences":[` + "\n" +
				`{"kind":"Deployment","name":"athlete",` +
				`"controller":true}]}}`,
			"jsonpath={.spec.replicas}": "2",
		}}
		f.install(t)
		out, code := capture(
			t,
			func() int {
				return cliLifecycle(
					tc.verb,
					[]string{"athlete"},
				)
			},
		)
		if code != 0 {
			t.Fatalf("%s: exit %d (%s)", tc.verb, code, out)
		}
		if !strings.Contains(out, tc.want) {
			t.Fatalf(
				"%s printed %q, want %q",
				tc.verb,
				out,
				tc.want,
			)
		}
	}
}

// A verb with the wrong number of arguments prints its usage and exits
// non-zero, so a script can tell it did nothing.
func TestCliLifecycleUsage(t *testing.T) {
	f := &fakeKubectl{}
	f.install(t)
	if _, code := capture(
		t,
		func() int { return cliLifecycle("stop", nil) },
	); code != 2 {
		t.Fatal("a bare stop was accepted")
	}
	if _, code := capture(t, func() int {
		return cliLifecycle("stop", []string{"a", "b"})
	}); code != 2 {
		t.Fatal("two names were accepted")
	}
	if len(f.calls) != 0 {
		t.Fatalf("kubectl was reached on a usage error: %v", f.calls)
	}
}

// The guard fires before the resolve, so an operator pointed at the wrong
// cluster is told THAT, not told the deployment does not exist there.
func TestCliLifecycleGuardsBeforeItLooks(t *testing.T) {
	os.Unsetenv(mutationEnvVar)
	t.Setenv("PUFFIN_KUBE_CONTEXT", "k3d-fleet")
	f := &fakeKubectl{}
	f.install(t)
	if _, code := capture(
		t,
		func() int { return cliLifecycle("stop", []string{"athlete"}) },
	); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if len(f.calls) != 0 {
		t.Fatalf("a foreign cluster was contacted anyway: %v", f.calls)
	}
}

// An unknown name says which name and which namespace, because those are
// the two things wrong when this happens.
func TestCliLifecycleReportsAnUnknownName(t *testing.T) {
	f := &fakeKubectl{fail: map[string]bool{"get": true}}
	f.install(t)
	if _, code := capture(
		t,
		func() int { return cliLifecycle("start", []string{"nope"}) },
	); code != 1 {
		t.Fatal("an unknown name was accepted")
	}
}

// runCLI has to route the new verbs, which is the one thing a subcommand
// can get wrong that no other test would notice.
func TestRunCLIRoutesTheNewVerbs(t *testing.T) {
	f := &fakeKubectl{answers: map[string]string{"logs": "hello"}}
	f.install(t)
	out, code := capture(
		t,
		func() int { return runCLI([]string{"logs", "athlete-1"}) },
	)
	if code != 0 || !strings.Contains(out, "hello") {
		t.Fatalf("logs routed to exit %d, out %q", code, out)
	}
	out, _ = capture(t, func() int { return runCLI([]string{"help"}) })
	for _, verb := range []string{"logs", "stop", "start", "restart"} {
		if !strings.Contains(out, "puffin "+verb) {
			t.Fatalf("help does not mention %s", verb)
		}
	}
}

// kubectl's stderr is the useful half of its failures; an exit status alone
// says nothing an operator can act on.
func TestKubectlErrCarriesStderr(t *testing.T) {
	c := exec.Command(
		"sh",
		"-c",
		`echo 'Error from server (NotFound): pods "x" not found' >&2;`+
			` exit 1`,
	)
	_, err := c.Output()
	got := kubectlErr(err)
	if !strings.Contains(got.Error(), "NotFound") {
		t.Fatalf("stderr was dropped: %v", got)
	}
	// and a failure with nothing on stderr still says something
	c2 := exec.Command("sh", "-c", "exit 3")
	_, err2 := c2.Output()
	if kubectlErr(err2) == nil {
		t.Fatal("a bare failure produced no error")
	}
}

// The tea.Cmd closures are one-liners over tested functions, but they are
// also where a message gets built with the wrong fields. Run them.

// The command hands its answer back as a message, so the read happens off
// the render path and the screen redraws when it lands.
func TestLogsCmdProducesAFetchedMessage(t *testing.T) {
	f := &fakeKubectl{answers: map[string]string{"logs": "body"}}
	f.install(t)
	msg := logsCmd("local", "athlete-1")().(logsFetched)
	if msg.pod != "athlete-1" || msg.body != "body" || msg.err != nil {
		t.Fatalf("%+v", msg)
	}
}

// The verb has to survive resolution, or the confirmation names one
// action and the act performs another.
func TestResolveCmdCarriesTheVerbThrough(t *testing.T) {
	t.Setenv("PUFFIN_KUBE_CONTEXT", homeContext())
	f := &fakeKubectl{answers: map[string]string{
		"get pod": `{"metadata":{"namespace":"local","ownerReferences":[
		 {"kind":"Deployment","name":"athlete","controller":true}]}}`,
		"jsonpath={.spec.replicas}": "1",
	}}
	f.install(t)
	msg := resolveCmd("stop", "local", "athlete-1")().(kubeResolved)
	if msg.err != nil || msg.verb != "stop" || msg.w.Name != "athlete" {
		t.Fatalf("%+v", msg)
	}
}

// A foreign context is refused at resolution, before kubectl is reached,
// so the reason on screen is the guard rather than a confusing refusal.
func TestResolveCmdReportsTheGuard(t *testing.T) {
	os.Unsetenv(mutationEnvVar)
	t.Setenv("PUFFIN_KUBE_CONTEXT", "k3d-fleet")
	f := &fakeKubectl{}
	f.install(t)
	msg := resolveCmd("stop", "local", "athlete-1")().(kubeResolved)
	if msg.err == nil {
		t.Fatal("the guard did not reach the screen")
	}
	if len(f.calls) != 0 {
		t.Fatal("a foreign cluster was contacted from the tui")
	}
}

// The note says what moved, in the workload's name rather than the pod's,
// because the pod is back before the screen redraws.
func TestActCmdReportsInWords(t *testing.T) {
	t.Setenv("PUFFIN_KUBE_CONTEXT", homeContext())
	f := &fakeKubectl{}
	f.install(t)
	w := KubeWorkload{
		Kind:      "Deployment",
		Name:      "athlete",
		Namespace: "local",
		Replicas:  2,
		Scalable:  true,
	}
	msg := actCmd(kubeAction{verb: "stop", w: w})().(kubeActed)
	if msg.err != nil || msg.note != "stopped deployment/athlete" {
		t.Fatalf("%+v", msg)
	}
	// and a refusal comes back as one
	bad := actCmd(kubeAction{
		verb: "stop",
		w:    KubeWorkload{Kind: "DaemonSet", Name: "d"},
	})().(kubeActed)
	if bad.err == nil {
		t.Fatal("a daemonset stop reported success")
	}
}
