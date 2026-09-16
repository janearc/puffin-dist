package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// Whether a pod has room is not what its phase says.
//
// The question being asked of a database is whether it has enough memory.
// Running means it has not been killed yet. Answering the other question meant
// leaving for grafana.
func TestMemCellSaysHowClose(t *testing.T) {
	s := Compile(Corvid())
	mb := int64(1 << 20)

	for _, c := range []struct {
		name string
		pod  KubePod
		want string
	}{
		{
			"comfortable",
			KubePod{MemUsed: 185 * mb, MemLimit: 2048 * mb},
			"185Mi/2Gi 9%",
		},
		{
			"at the ceiling",
			KubePod{MemUsed: 900 * mb, MemLimit: 1000 * mb},
			"900Mi/1000Mi 90%",
		},
		{"unbounded", KubePod{MemUsed: 100 * mb}, "100Mi ∞"},
		{"not measured", KubePod{MemLimit: 512 * mb}, "-"},
		{
			"finished",
			KubePod{
				MemUsed:  50 * mb,
				MemLimit: 512 * mb,
				Terminal: true,
			},
			"-",
		},
	} {
		got := strings.TrimSpace(ansi.Strip(memCell(s, c.pod, false)))
		if got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// An absent measurement is not zero bytes. metrics-server can be missing,
// starting, or simply not reporting a pod, and a pod drawn as using nothing
// reads as idle rather than unmeasured -- which is the shape of bug this
// repository keeps finding: uncertain state presented as truth.
func TestUnmeasuredIsNotZero(t *testing.T) {
	s := Compile(Corvid())
	got := ansi.Strip(
		memCell(s, KubePod{MemUsed: 0, MemLimit: 1 << 30}, false),
	)
	if strings.Contains(got, "0%") || strings.Contains(got, "0Mi") {
		t.Errorf("an unmeasured pod rendered as using nothing: "+
			"%q", got)
	}
}

// A container with no limit makes the POD unlimited: one unbounded
// container can take the node down whatever its neighbours declared.
func TestOneUnboundedContainerUnboundsThePod(t *testing.T) {
	out := []byte(`{"items":[` + "\n" +
		`	  {"metadata":{"namespace":"local","name":"mixed",` +
		`"creationTimestamp":"2026-09-01T00:00:00Z"},` + "\n" +
		`	   "spec":{"containers":[` + "\n" +
		`{"resources":{"limits":{"memory":"512Mi"}}},` + "\n" +
		`	     {"resources":{"limits":{}}}]},` + "\n" +
		`	   "status":{"phase":"Running",` +
		`"containerStatuses":[{"ready":true,` +
		`"restartCount":0}]}},` + "\n" +
		`	  {"metadata":{"namespace":"local","name":"bounded",` +
		`"creationTimestamp":"2026-09-01T00:00:00Z"},` + "\n" +
		`	   "spec":{"containers":[` + "\n" +
		`{"resources":{"limits":{"memory":"2Gi"}}},` + "\n" +
		`{"resources":{"limits":{"memory":"128Mi"}}}]},` + "\n" +
		`	   "status":{"phase":"Running",` +
		`"containerStatuses":[{"ready":true,` +
		`"restartCount":0}]}}]}`)
	pods, errs := parsePods(out, "k3d-local")
	if errs != "" {
		t.Fatal(errs)
	}
	by := map[string]KubePod{}
	for _, p := range pods {
		by[p.Name] = p
	}
	if by["mixed"].MemLimit != 0 {
		t.Errorf(
			"a pod with one unbounded container reported a limit "+
				"of %d",
			by["mixed"].MemLimit,
		)
	}
	if want := int64(2<<30 + 128<<20); by["bounded"].MemLimit != want {
		t.Errorf(
			"bounded limit = %d, want %d (the sum of its "+
				"containers)",
			by["bounded"].MemLimit,
			want,
		)
	}
}

// Usage is summed across containers, and comes from the metrics API rather
// than `kubectl top` -- top prints a table for a person, rounded and
// reshaped at kubectl's discretion; the raw endpoint carries units.
func TestPodMemorySumsContainers(t *testing.T) {
	out := []byte(
		`{"items":[{"metadata":{"namespace":"local",` +
			`"name":"postgres-x"},` + "\n" +
			`"containers":[{"usage":{"memory":"170156Ki"}},` +
			`{"usage":{"memory":"20768Ki"}}]}]}`,
	)
	used := parsePodMemory(out)
	want := int64(170156+20768) * 1024
	if used["local/postgres-x"] != want {
		t.Errorf(
			"summed to %d, want %d",
			used["local/postgres-x"],
			want,
		)
	}
}

// metrics-server being absent must not cost the operator the pod list.
func TestMetricsFailureIsNotFatal(t *testing.T) {
	if got := parsePodMemory([]byte("not json")); got != nil {
		t.Errorf(
			"garbage from the metrics API produced %v rather "+
				"than nil",
			got,
		)
	}
}
