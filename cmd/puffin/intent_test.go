package main

import (
	"strings"
	"testing"
)

// The confirm key must not be predictable, or it is a speed bump rather than
// a check: a prompt answered the same way every time gets answered before it
// is read.
func TestConfirmKeyVaries(t *testing.T) {
	seen := map[string]bool{}
	for seed := int64(0); seed < 200; seed++ {
		k := confirmKeyFor(seed)
		if len(k) != 1 {
			t.Fatalf("confirm key %q is not one character", k)
		}
		seen[k] = true
	}
	if len(seen) < 10 {
		t.Fatalf(
			"only %d distinct confirm keys in 200 draws",
			len(seen),
		)
	}
	// and never a key whose meaning is already spoken for
	for _, banned := range []string{"q", "r", "j", "k", "i", "l"} {
		if strings.Contains(confirmAlphabet, banned) {
			t.Fatalf(
				"%q is in the confirm alphabet and already "+
					"means something",
				banned,
			)
		}
	}
}

// What is shown is what will be sent. The intent carries the request, so a
// screen cannot honestly describe one thing and send another.
func TestFliprIntentShowsTheRealBody(t *testing.T) {
	row := FlagRow{
		Service: "kestrel",
		Version: "v1",
		Key:     "fetch.terrain",
		Kind:    "bool",
	}
	i := fliprIntent(row, "true", "testing the confirm screen", "test")
	if !strings.Contains(i.Wire, `"fetch.terrain"`) {
		t.Fatalf("the wire does not name the flag: %s", i.Wire)
	}
	if !strings.Contains(i.Wire, `"boolValue": true`) {
		t.Fatalf("the wire does not carry the new value: %s", i.Wire)
	}
	if !strings.Contains(i.Wire, "testing the confirm screen") {
		t.Fatalf("the reason is not in the request: %s", i.Wire)
	}
	// and it is readable: one-line protojson is correct and useless here
	if !strings.Contains(i.Wire, "\n") {
		t.Fatal("the body was not formatted for reading")
	}
	if i.Wire != pretty(
		flipBody(row, "true", "testing the confirm screen"),
	) {
		t.Fatal("the intent's wire is not the body flipr would receive")
	}
}

// What is shown before a confirmation has to BE the command that runs. A
// summary written by the same code that is about to act cannot be wrong
// in an interesting way; the command can.
func TestLifecycleIntentIsTheCommand(t *testing.T) {
	w := KubeWorkload{
		Kind:      "Deployment",
		Name:      "athlete",
		Namespace: "local",
		Replicas:  3,
	}
	i := lifecycleIntent("stop", w, "k3d-local")
	for _, want := range []string{"kubectl", "--context " +
		"k3d-local", "deployment/athlete", "--replicas=0"} {
		if !strings.Contains(i.Wire, want) {
			t.Fatalf("the command is missing %q: %s", want, i.Wire)
		}
	}
	// the pod is not the target: stopping a pod is not a thing
	if strings.Contains(i.Target, "-") &&
		!strings.Contains(i.Target, "Deployment") {
		t.Fatalf(
			"the target names a pod rather than a workload: %s",
			i.Target,
		)
	}
}

// safe mode confirms everything; with it off, only what cannot be undone.
func TestSafeModeDecidesWhatAsks(t *testing.T) {
	reversible := Intent{Reversible: true}
	permanent := Intent{Reversible: false}
	if !permanent.needsConfirm() {
		t.Fatal("an irreversible action skipped the confirmation")
	}
	if !safeMode() {
		t.Fatal("safe mode is meant to default on")
	}
	if !reversible.needsConfirm() {
		t.Fatal("safe mode did not confirm a reversible action")
	}
}
