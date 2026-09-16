package main

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// The bus pane read `exec -n local kafka-0` with the context as a variable and
// the namespace written as a constant, so it worked against the dev network and
// nowhere else.
//
// Pointed at fleet -- where kafka-0 lives in a namespace called fleet --
// kubectl said "pods kafka-0 not found" and the pane said "listing topics: exit
// status 1", which names neither the cluster it looked in nor the thing it
// could not find.

// The namespace is remembered per cluster, and never shared between them: a
// broker found in local must not be assumed to sit in local in fleet.
func TestKafkaNamespaceIsPerCluster(t *testing.T) {
	nsCache.mu.Lock()
	nsCache.byContext = map[string]string{
		"k3d-local": "local",
		"k3d-fleet": "fleet",
	}
	nsCache.mu.Unlock()
	t.Cleanup(func() {
		nsCache.mu.Lock()
		nsCache.byContext = nil
		nsCache.mu.Unlock()
	})

	for ctx, want := range map[string]string{
		"k3d-local": "local",
		"k3d-fleet": "fleet",
	} {
		got, err := kafkaNS(ctx)
		if err != nil {
			t.Fatalf("%s: %v", ctx, err)
		}
		if got != want {
			t.Errorf("%s: kafka in %q, want %q", ctx, got, want)
		}
	}
}

// A cluster with no broker is told apart from a broker that could not be
// reached: "not running here" and "kubectl failed" are different things to
// do next about.
func TestKafkaNamespaceSaysWhenThereIsNoBroker(t *testing.T) {
	nsCache.mu.Lock()
	nsCache.byContext = nil
	nsCache.mu.Unlock()
	t.Cleanup(func() {
		nsCache.mu.Lock()
		nsCache.byContext = nil
		nsCache.mu.Unlock()
	})
	// a context that cannot exist: kubectl fails, and the error says which
	// cluster was asked rather than only that something exited non-zero
	_, err := kafkaNS("k3d-there-is-no-such-cluster")
	if err == nil {
		t.Fatal("a missing cluster answered with a namespace")
	}
	if !strings.Contains(err.Error(), "k3d-there-is-no-such-cluster") {
		t.Errorf(
			"the error does not say which cluster was asked: %v",
			err,
		)
	}
	if !strings.Contains(err.Error(), kafkaPod) {
		t.Errorf(
			"the error does not say what was being looked for: %v",
			err,
		)
	}
}

// kubectlErr is the difference between a diagnosis and a shrug, and this
// file spent three call sites throwing it away.
func TestKubectlErrKeepsWhatKubectlSaid(t *testing.T) {
	said := `Error from server (NotFound): pods "kafka-0" not found`
	err := kubectlErr(&exec.ExitError{Stderr: []byte(said + "\n")})
	if err.Error() != said {
		t.Fatalf("got %q, want %q", err.Error(), said)
	}
	// with nothing on stderr the original survives rather than becoming an
	// empty string
	plain := errors.New("exit status 1")
	if got := kubectlErr(plain); got.Error() != "exit status 1" {
		t.Fatalf("got %q", got.Error())
	}
}
