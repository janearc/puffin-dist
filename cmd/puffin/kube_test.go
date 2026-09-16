package main

import (
	"strings"
	"testing"
	"time"
)

// parseDur keeps the age table readable.
func parseDur(s string) (time.Duration, error) { return time.ParseDuration(s) }

// parsePods against kubectl's real JSON shape: ready counting, restart
// summing, and the terminal distinction.
func TestParsePods(t *testing.T) {
	raw := []byte(`{"items":[` + "\n" +
		`{"metadata":{"namespace":"flipr","name":"flipr-abc",` +
		`"creationTimestamp":"2026-08-28T10:00:00Z"},` + "\n" +
		`		 "status":{"phase":"Running",` +
		`"containerStatuses":[{"ready":true,` +
		`"restartCount":0}]}},` + "\n" +
		`{"metadata":{"namespace":"local","name":"kafka-0",` +
		`"creationTimestamp":"2026-08-28T09:00:00Z"},` + "\n" +
		`		 "status":{"phase":"Running",` +
		`"containerStatuses":[{"ready":false,` +
		`"restartCount":2},{"ready":true,` +
		`"restartCount":1}]}},` + "\n" +
		`{"metadata":{"namespace":"local","name":"job-1",` +
		`"creationTimestamp":"2026-08-27T00:00:00Z"},` + "\n" +
		`		 "status":{"phase":"Succeeded",` +
		`"containerStatuses":[{"ready":false,` +
		`"restartCount":0}]}}]}`)
	pods, perr := parsePods(raw, "k3d-local")
	if perr != "" {
		t.Fatal(perr)
	}
	if len(pods) != 3 {
		t.Fatalf("pods: %d", len(pods))
	}
	// sorted namespace then name: flipr first
	if pods[0].Name != "flipr-abc" || !pods[0].AllReady {
		t.Fatalf("first: %+v", pods[0])
	}
	if !pods[1].Terminal {
		t.Fatal("Succeeded should be terminal")
	}
	kafka := pods[2]
	if kafka.Ready != "1/2" || kafka.AllReady || kafka.Restarts != 3 {
		t.Fatalf("kafka reduction wrong: %+v", kafka)
	}
	// junk is named, not swallowed
	if _, perr := parsePods([]byte("not json"), "x"); perr == "" ||
		!strings.Contains(perr, "not with pods") {
		t.Fatalf("junk: %q", perr)
	}
}

// humanAge renders the largest unit, like kubectl.
func TestHumanAge(t *testing.T) {
	for d, want := range map[string]string{
		"30s": "30s", "5m": "5m", "3h": "3h", "50h": "2d",
	} {
		dur, _ := parseDur(d)
		if got := humanAge(dur); got != want {
			t.Errorf("humanAge(%s)=%q want %q", d, got, want)
		}
	}
}
