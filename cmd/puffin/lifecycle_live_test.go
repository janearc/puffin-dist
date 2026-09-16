//go:build live

package main

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The lifecycle integration suite, run deliberately with -tags live.
//
// It builds its own workload, does everything to it, and takes it away again.
//
// Testing start and stop by bouncing a service somebody is using is not a test,
// it is an outage with a good intention behind it -- and it proves less,
// because a service that happens to run one replica cannot tell "restored the
// recorded count" apart from "fell back to one".
//
// The fixture runs TWO, so the annotation has something to prove.
//
//	go test -tags live -run TestLive ./...

const (
	fixtureName = "puffin-lifecycle-fixture"
	fixtureNS   = "local"
	// busybox is already on the local node, so the fixture never waits on
	// a pull and never reaches the network
	fixtureImage    = "busybox:1.36"
	fixtureReplicas = 2
)

// fixtureManifest is the whole workload. Nothing in dev exists that was not
// created by code in a repository, and that includes test scaffolding.
func fixtureManifest() string {
	return `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ` + fixtureName + `
  namespace: ` + fixtureNS + `
  labels:
    app.kubernetes.io/name: ` + fixtureName + `
    app.kubernetes.io/managed-by: puffin-tests
spec:
  replicas: ` + strconv.Itoa(fixtureReplicas) + `
  selector:
    matchLabels:
      app.kubernetes.io/name: ` + fixtureName + `
  template:
    metadata:
      labels:
        app.kubernetes.io/name: ` + fixtureName + `
    spec:
      terminationGracePeriodSeconds: 0
      containers:
        - name: sleep
          image: ` + fixtureImage + `
          imagePullPolicy: IfNotPresent
          command: ["sh", "-c", "echo fixture up; exec sleep 3600"]
          resources:
            requests: {cpu: 1m, memory: 8Mi}
            limits: {cpu: 50m, memory: 32Mi}
`
}

// kubectlFixture runs kubectl against the fixture's cluster.
func kubectlFixture(t *testing.T, stdin string, args ...string) string {
	t.Helper()
	full := append([]string{"--context", kubeContext()}, args...)
	c := exec.Command("kubectl", full...)
	if stdin != "" {
		c.Stdin = strings.NewReader(stdin)
	}
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf(
			"kubectl %s: %v\n%s",
			strings.Join(args, " "),
			err,
			out,
		)
	}
	return strings.TrimSpace(string(out))
}

// withFixture stands the workload up and guarantees it comes down, whether
// the test passes, fails or panics.
func withFixture(t *testing.T) {
	t.Helper()
	if kubeContext() != homeContext() {
		t.Fatalf(
			"the live suite builds and destroys workloads; it "+
				"runs against %s only, not %q",
			homeContext(),
			kubeContext(),
		)
	}
	kubectlFixture(t, fixtureManifest(), "apply", "-f", "-")
	t.Cleanup(func() {
		c := exec.Command(
			"kubectl",
			"--context",
			kubeContext(),
			"-n",
			fixtureNS,
			"delete",
			"deployment",
			fixtureName,
			"--ignore-not-found",
			"--wait=false",
		)
		if out, err := c.CombinedOutput(); err != nil {
			t.Errorf(
				"the fixture was left behind: %v\n%s",
				err,
				out,
			)
		}
	})
	kubectlFixture(t, "", "-n", fixtureNS, "rollout", "status",
		"deployment/"+fixtureName, "--timeout=90s")
}

// specReplicas reads the fixture's replica count off the cluster.
func specReplicas(t *testing.T) int {
	t.Helper()
	s := kubectlFixture(
		t,
		"",
		"-n",
		fixtureNS,
		"get",
		"deployment",
		fixtureName,
		"-o",
		"jsonpath={.spec.replicas}",
	)
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("replicas: %q", s)
	}
	return n
}

// waitForPods blocks until the fixture has n pods, or gives up loudly.
func waitForPods(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		out := kubectlFixture(t, "", "-n", fixtureNS, "get", "pods",
			"-l", "app.kubernetes.io/name="+fixtureName,
			"-o", "jsonpath={.items[*].metadata.name}")
		var pods []string
		if out != "" {
			pods = strings.Fields(out)
		}
		if len(pods) == n {
			return pods
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"wanted %d pods, still %d after 90s",
				n,
				len(pods),
			)
		}
		time.Sleep(2 * time.Second)
	}
}

// The round trip that the manual poking could not actually prove: a
// two-replica workload goes to zero and comes back as TWO. Restored from
// the annotation, not from the fallback.
func TestLiveStopAndStartRestoresTheRealCount(t *testing.T) {
	withFixture(t)
	if got := specReplicas(t); got != fixtureReplicas {
		t.Fatalf("fixture came up at %d", got)
	}
	w, err := ResolveTarget(kubeContext(), fixtureNS, fixtureName)
	if err != nil {
		t.Fatal(err)
	}
	if w.Kind != "Deployment" || w.Replicas != fixtureReplicas {
		t.Fatalf("resolved %+v", w)
	}

	if err := StopWorkload(kubeContext(), w); err != nil {
		t.Fatal(err)
	}
	if got := specReplicas(t); got != 0 {
		t.Fatalf("stop left %d replicas", got)
	}
	waitForPods(t, 0)

	// the count is on the workload, where start will look for it
	ann := kubectlFixture(
		t,
		"",
		"-n",
		fixtureNS,
		"get",
		"deployment",
		fixtureName,
		"-o",
		"jsonpath={.metadata.annotations."+escapeJSONPath(
			prevReplicas,
		)+"}",
	)
	if ann != strconv.Itoa(fixtureReplicas) {
		t.Fatalf("recorded %q, want %d", ann, fixtureReplicas)
	}

	// start must resolve the workload with NO pods to point at
	w2, err := ResolveTarget(kubeContext(), fixtureNS, fixtureName)
	if err != nil {
		t.Fatalf("a stopped workload could not be resolved: %v", err)
	}
	if w2.Replicas != 0 {
		t.Fatalf("stopped workload reads as %d", w2.Replicas)
	}
	if err := StartWorkload(kubeContext(), w2); err != nil {
		t.Fatal(err)
	}
	if got := specReplicas(t); got != fixtureReplicas {
		t.Fatalf(
			"start restored %d, want %d -- the annotation was "+
				"not honoured",
			got,
			fixtureReplicas,
		)
	}
	waitForPods(t, fixtureReplicas)
}

// Stopping by POD name has to reach the Deployment. If it stopped at the
// ReplicaSet the pods would be back within seconds and the count would
// read two again.
func TestLiveStopByPodNameReachesTheDeployment(t *testing.T) {
	withFixture(t)
	pods := waitForPods(t, fixtureReplicas)

	w, err := ResolveTarget(kubeContext(), fixtureNS, pods[0])
	if err != nil {
		t.Fatal(err)
	}
	if w.Kind != "Deployment" || w.Name != fixtureName {
		t.Fatalf("a pod name resolved to %+v, not the deployment", w)
	}
	if err := StopWorkload(kubeContext(), w); err != nil {
		t.Fatal(err)
	}
	waitForPods(t, 0)
	// and it stays down -- a ReplicaSet-level scale would not
	time.Sleep(8 * time.Second)
	if got := specReplicas(t); got != 0 {
		t.Fatalf("the workload came back on its own: %d replicas", got)
	}
}

// Restart rolls the pods and leaves the replica count alone.
func TestLiveRestartRollsWithoutScaling(t *testing.T) {
	withFixture(t)
	before := waitForPods(t, fixtureReplicas)
	w, err := ResolveTarget(kubeContext(), fixtureNS, fixtureName)
	if err != nil {
		t.Fatal(err)
	}
	if err := RestartWorkload(kubeContext(), w); err != nil {
		t.Fatal(err)
	}
	kubectlFixture(t, "", "-n", fixtureNS, "rollout", "status",
		"deployment/"+fixtureName, "--timeout=90s")
	after := waitForPods(t, fixtureReplicas)
	if specReplicas(t) != fixtureReplicas {
		t.Fatal("restart changed the replica count")
	}
	same := map[string]bool{}
	for _, p := range before {
		same[p] = true
	}
	for _, p := range after {
		if same[p] {
			t.Fatalf(
				"pod %s survived the roll; nothing was "+
					"restarted",
				p,
			)
		}
	}
}

// Logs come back from a real pod, and the fixture says something on start
// so there is a known string to find.
func TestLiveLogs(t *testing.T) {
	withFixture(t)
	pods := waitForPods(t, fixtureReplicas)
	body, err := PodLogs(kubeContext(), fixtureNS, pods[0], 50, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "fixture up") {
		t.Fatalf("logs did not carry the container's output: %q", body)
	}
}

// The refusals, against a real cluster rather than a fake: a DaemonSet does
// not scale, and a foreign context is refused before kubectl is reached.
func TestLiveRefusals(t *testing.T) {
	withFixture(t)
	ds, err := ResolveTarget(
		kubeContext(),
		"kube-system",
		"svclb-traefik-08dc25a2",
	)
	if err == nil && !ds.Scalable {
		if err := StopWorkload(kubeContext(), ds); err == nil {
			t.Fatal("a daemonset was scaled")
		}
	}
	w, err := ResolveTarget(kubeContext(), fixtureNS, fixtureName)
	if err != nil {
		t.Fatal(err)
	}
	if err := StopWorkload("k3d-fleet", w); err == nil {
		t.Fatal("a foreign context was allowed to stop a workload")
	}
	if specReplicas(t) != fixtureReplicas {
		t.Fatal("the refused stop changed the cluster anyway")
	}
}
