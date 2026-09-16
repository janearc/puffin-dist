package main

import (
	"strings"
	"testing"
)

// A forward's command line is parsed for what can be parsed, and kept whole
// regardless: a flag order this does not expect must not become a silently
// wrong row on a screen whose next keystroke kills a process.
func TestParseForwardKeepsTheWholeCommand(t *testing.T) {
	f := parseForward(
		"  4021 kubectl --context k3d-local -n local port-forward " +
			"svc/flipr 8080:80",
	)
	if f.PID != 4021 {
		t.Fatalf("pid is %d", f.PID)
	}
	if f.NS != "local" || f.Ctx != "k3d-local" {
		t.Fatalf("namespace/context are %q/%q", f.NS, f.Ctx)
	}
	if f.Remote != "svc/flipr" || f.Local != "8080" {
		t.Fatalf("target is %q on :%s", f.Remote, f.Local)
	}
	if !strings.Contains(f.Raw, "port-forward svc/flipr 8080:80") {
		t.Fatalf("the raw command was lost: %q", f.Raw)
	}
}

// An unparseable line still yields a row with its command intact, rather
// than being dropped.
func TestParseForwardSurvivesSurprises(t *testing.T) {
	f := parseForward(
		"  99 kubectl port-forward --address 0.0.0.0 pod/x 1:2",
	)
	if f.PID != 99 {
		t.Fatalf("a line with unfamiliar flags was dropped: %+v", f)
	}
	if f.Raw == "" {
		t.Fatal("the raw command is empty")
	}
}

// ClusterIP services are not findings: they are reachable by name, which is
// the arrangement this screen exists to protect.
func TestOnlyServicesThatEscapeAreListed(t *testing.T) {
	raw := []byte(`{"items":[` + "\n" +
		`{"metadata":{"name":"flipr","namespace":"local"},` +
		`"spec":{"type":"ClusterIP",` +
		`"ports":[{"port":80}]}},` + "\n" +
		`	 {"metadata":{"name":"edge","namespace":"local"},` +
		`"spec":{"type":"NodePort","ports":[{"port":80,` +
		`"nodePort":30080}]}}]}`)
	got := parseServicePorts(raw)
	if len(got) != 1 || got[0].Name != "edge" {
		t.Fatalf("expected only the NodePort service, got %+v", got)
	}
	if got[0].Ports[0] != "30080→80" {
		t.Fatalf("the port pair reads %q", got[0].Ports[0])
	}
}

// What the confirmation shows is what will run.
func TestForwardCommandIsTheCommand(t *testing.T) {
	got := forwardCommand("k3d-local", "local", "svc/flipr", "8080", "80")
	want := "kubectl --context k3d-local -n local port-forward svc/flipr " +
		"8080:80"
	if got != want {
		t.Fatalf("got %q", got)
	}
}

// The entry is forgiving about everything except the port, which is the only
// part that cannot be guessed.
func TestParseForwardEntry(t *testing.T) {
	cases := []struct {
		in                        string
		ns, target, local, remote string
	}{
		{"flipr 8080", "local", "svc/flipr", "8080", "8080"},
		{"svc/flipr 8080:80", "local", "svc/flipr", "8080", "80"},
		{
			"kafka/schema-registry 8081:8081",
			"kafka",
			"svc/schema-registry",
			"8081",
			"8081",
		},
		{"  flipr   9000:80  ", "local", "svc/flipr", "9000", "80"},
	}
	for _, c := range cases {
		ns, target, local, remote, err := parseForwardEntry(
			c.in,
			"local",
		)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if ns != c.ns || target != c.target || local != c.local ||
			remote != c.remote {
			t.Fatalf(
				"%q -> %s %s %s:%s, want %s %s %s:%s",
				c.in,
				ns,
				target,
				local,
				remote,
				c.ns,
				c.target,
				c.local,
				c.remote,
			)
		}
	}
}

// A bare name becomes a service, never a pod: a pod name is a moving target
// and forwarding to one is how you debug a pod that was replaced an hour ago.
func TestBareNameBecomesAService(t *testing.T) {
	_, target, _, _, err := parseForwardEntry("flipr 80", "local")
	if err != nil || target != "svc/flipr" {
		t.Fatalf("bare name became %q (%v)", target, err)
	}
}

// A port that is not a port is refused rather than handed to kubectl.
func TestBadPortsAreRefused(t *testing.T) {
	for _, bad := range []string{"flipr " +
		"eighty", "flipr 0", "flipr 99999", "flipr", "flipr 80 90"} {
		if _, _, _, _, err := parseForwardEntry(
			bad,
			"local",
		); err == nil {
			t.Fatalf("%q was accepted", bad)
		}
	}
}

// The audit says whose machine it is about, because a forward is a process
// on one host and "none" means nothing without a name attached to it.
func TestPortViewNamesItsHost(t *testing.T) {
	f := hostFacts()
	if f.Host == "" || f.User == "" {
		t.Fatalf("host facts came back blank: %+v", f)
	}
}

// uptime(1) carries the load averages and the host pane reports those better.
func TestTidyUptimeDropsLoad(t *testing.T) {
	got := tidyUptime(
		"23:42  up 16:19, 3 users, load average: 2.39 2.27 2.17",
	)
	if strings.Contains(got, "load average") {
		t.Fatalf("the load averages survived: %q", got)
	}
	if !strings.HasPrefix(got, "up ") {
		t.Fatalf("uptime reads %q", got)
	}
}

// A pane's reply has to be routed by the model or the pane renders its zero
// value forever -- which, for an audit, looks exactly like a clean estate.
// The ports pane shipped that way for an hour.
func TestPortsReplyReachesThePane(t *testing.T) {
	m := newModel("test", "")
	m.width, m.height = 120, 40
	p := &portsPane{}
	m.openPane, m.scr = p, screenPane

	msg := portsFetched{v: PortView{
		Context: "k3d-local",
		Host:    HostFacts{Host: "gluttony", User: "dev"},
		Forwards: []Forward{{PID: 7, Local: "8080", Remote: "svc/flipr",
			NS: "local", Raw: "kubectl port-forward svc/flipr " +
				"8080:80"}},
	}}
	out, _ := m.Update(msg)
	m = out.(model)
	got := ansiRe.ReplaceAllString(paneView(m), "")
	for _, want := range []string{
		"gluttony",
		"k3d-local",
		"svc/flipr",
		"8080",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf(
				"the pane never saw its reply: %q missing",
				want,
			)
		}
	}
}
