package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// The port audit.
//
// Audit what is forwarded out of the cluster, and then edit it.
//
// Worth naming the tension once, because it runs against the rule: services are
// addressed by NAME here, never by address and port, and a screen full of port
// numbers is the shape of the thing that rule exists to prevent. This screen is
// not an argument against it.
//
// It is the opposite -- an inventory of every place a port is currently
// standing in for a name, so the ones that should not exist can be found and
// stopped.
//
// Four sources, because a port reaches the host four different ways and
// only one of them is written down anywhere:
//
//	forwards  kubectl port-forward running on THIS host. Ad-hoc by
//	          definition: a process somebody started in a terminal, which
//	          dies with the terminal and is in no repository. The rule is
//	          that nothing exists that was not created by code in a
//	          repository, and these are the exception that walks in every
//	          day.
//	services  NodePort and LoadBalancer services. Declared in a manifest,
//	          so these ARE in a repository -- but they are the ones that
//	          survive a reboot, which makes them worth seeing beside the
//	          ad-hoc ones.
//	mappings  what k3d published on the docker side when the cluster was
//	          created. Not editable without recreating the cluster, and
//	          shown for exactly that reason: an unexplained port on the
//	          host is usually one of these.
//	routers   traefik's live routing table -- the names that resolve
//	          without a port at all, which is what everything here is
//	          supposed to look like.

// Forward is one kubectl port-forward process on this host.
type Forward struct {
	PID    int
	Local  string // the port on this machine
	Remote string // service or pod, and its port
	NS     string
	Ctx    string
	Raw    string // the full command line, so nothing is hidden by parsing
}

// SvcPort is a service that reaches beyond the cluster on its own.
type SvcPort struct {
	Name, NS, Type string
	Ports          []string
}

// Mapping is a docker-published port on a k3d container.
type Mapping struct {
	Container string
	Published string
}

// HostFacts is who this audit is about.
//
// It matters more here than on any other screen: a port-forward is a process on
// ONE machine, and the list is only true for the machine puffin is running on.
//
// Reading "none" without knowing whose "none" it is is how you conclude the
// estate is clean while somebody else's laptop is holding a forward open into
// the cluster.
type HostFacts struct {
	Host string
	User string
	// Uptime is the host's, because a forward cannot be older than the boot
	// that is holding it.
	Uptime string
}

// PortView is the whole picture.
type PortView struct {
	Host     HostFacts
	Forwards []Forward
	Services []SvcPort
	Mappings []Mapping
	Warnings []string
	Context  string
}

// fetchPorts gathers all four sources. Each failure is a warning rather than
// an error: three quarters of an inventory is worth having, and a source
// that is missing is itself a finding.
func fetchPorts(kubeCtx string) PortView {
	v := PortView{Context: kubeCtx, Host: hostFacts()}

	if fs, err := hostForwards(); err != nil {
		v.Warnings = append(v.Warnings, "host processes: "+err.Error())
	} else {
		v.Forwards = fs
	}

	out, err := exec.Command("kubectl", "--context", kubeCtx,
		"get", "svc", "-A", "-o", "json").Output()
	if err != nil {
		v.Warnings = append(v.Warnings, "services: "+err.Error())
	} else {
		v.Services = parseServicePorts(out)
	}

	if ms, err := k3dMappings(kubeCtx); err != nil {
		v.Warnings = append(v.Warnings, "docker: "+err.Error())
	} else {
		v.Mappings = ms
	}
	return v
}

// hostForwards finds every kubectl port-forward running here.
//
// By command line rather than by asking kubectl, because there is nobody to
// ask: a port-forward is a process, not cluster state. Nothing in the
// cluster knows it exists, which is exactly why they accumulate.
func hostForwards() ([]Forward, error) {
	out, err := exec.Command("ps", "-eo", "pid=,command=").Output()
	if err != nil {
		return nil, err
	}
	var fs []Forward
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "port-forward") {
			continue
		}
		// ps itself, and this very grep, are not port-forwards
		if strings.Contains(line, "ps -eo") {
			continue
		}
		f := parseForward(line)
		if f.PID != 0 {
			fs = append(fs, f)
		}
	}
	sort.Slice(fs, func(i, j int) bool { return fs[i].Local < fs[j].Local })
	return fs, nil
}

// parseForward pulls what it can from a command line and keeps the whole
// line regardless. A flag order this does not expect must not turn into a
// silently wrong row: the raw command is always shown.
func parseForward(line string) Forward {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return Forward{}
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		return Forward{}
	}
	f := Forward{PID: pid, Raw: strings.Join(fields[1:], " ")}
	for i := 1; i < len(fields); i++ {
		switch fields[i] {
		case "-n", "--namespace":
			if i+1 < len(fields) {
				f.NS = fields[i+1]
			}
		case "--context":
			if i+1 < len(fields) {
				f.Ctx = fields[i+1]
			}
		}
		// the target and the port pair are positional, after the verb
		if fields[i] == "port-forward" {
			for j := i + 1; j < len(fields); j++ {
				a := fields[j]
				if strings.HasPrefix(a, "-") {
					j++ // skip its value
					continue
				}
				if strings.Contains(a, ":") && f.Remote != "" {
					f.Local = strings.SplitN(a, ":", 2)[0]
					continue
				}
				if f.Remote == "" {
					f.Remote = a
				}
			}
		}
	}
	return f
}

// parseServicePorts keeps only the services that reach past the cluster.
func parseServicePorts(raw []byte) []SvcPort {
	var doc struct {
		Items []struct {
			Metadata struct{ Name, Namespace string } `json:"metadata"`
			Spec     struct {
				Type  string `json:"type"`
				Ports []struct {
					Port     int    `json:"port"`
					NodePort int    `json:"nodePort"`
					Name     string `json:"name"`
				} `json:"ports"`
			} `json:"spec"`
		} `json:"items"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	var out []SvcPort
	for _, it := range doc.Items {
		if it.Spec.Type == "ClusterIP" || it.Spec.Type == "" {
			// in-cluster only: reachable by name, which is the
			// point
			continue
		}
		s := SvcPort{
			Name: it.Metadata.Name,
			NS:   it.Metadata.Namespace,
			Type: it.Spec.Type,
		}
		for _, p := range it.Spec.Ports {
			if p.NodePort != 0 {
				s.Ports = append(
					s.Ports,
					fmt.Sprintf(
						"%d→%d",
						p.NodePort,
						p.Port,
					),
				)
			} else {
				s.Ports = append(s.Ports, itoa(p.Port))
			}
		}
		out = append(out, s)
	}
	sort.Slice(
		out,
		func(i, j int) bool { return out[i].Name < out[j].Name },
	)
	return out
}

// k3dMappings reads what docker publishes for the cluster's containers.
func k3dMappings(kubeCtx string) ([]Mapping, error) {
	name := strings.TrimPrefix(kubeCtx, "k3d-")
	out, err := exec.Command("docker", "ps",
		"--filter", "name=k3d-"+name,
		"--format", "{{.Names}}\t{{.Ports}}").Output()
	if err != nil {
		return nil, err
	}
	var ms []Mapping
	for _, line := range strings.Split(
		strings.TrimSpace(string(out)),
		"\n",
	) {
		f := strings.SplitN(line, "\t", 2)
		if len(f) != 2 || strings.TrimSpace(f[1]) == "" {
			// a container publishing nothing is not a finding
			continue
		}
		ms = append(ms, Mapping{Container: f[0], Published: f[1]})
	}
	return ms, nil
}

// forwardCommand builds the port-forward that will be run, so the
// confirmation shows the command rather than a description of it.
func forwardCommand(kubeCtx, ns, target, local, remote string) string {
	return strings.Join([]string{
		"kubectl", "--context", kubeCtx, "-n", ns,
		"port-forward", target, local + ":" + remote,
	}, " ")
}

// hostFacts reads the few things about this machine the audit needs to be
// interpretable. Every one of them is allowed to be missing: an audit that
// refuses to run because it could not read the uptime is worse than one
// that says "unknown".
func hostFacts() HostFacts {
	f := HostFacts{Host: "unknown", User: "unknown"}
	if h, err := os.Hostname(); err == nil {
		f.Host = h
	}
	if u := os.Getenv("USER"); u != "" {
		f.User = u
	}
	if out, err := exec.Command("uptime").Output(); err == nil {
		f.Uptime = tidyUptime(string(out))
	}
	return f
}

// tidyUptime keeps the part of uptime(1) that is about time and drops the
// load averages, which the host pane already reports and reports better.
func tidyUptime(raw string) string {
	s := strings.TrimSpace(raw)
	if i := strings.Index(s, "load average"); i > 0 {
		s = strings.TrimRight(strings.TrimSpace(s[:i]), ",")
	}
	if i := strings.Index(s, "up "); i >= 0 {
		s = s[i:]
	}
	if i := strings.Index(s, ","); i > 0 {
		if j := strings.Index(s[i:], "user"); j > 0 {
			s = strings.TrimSpace(s[:i])
		}
	}
	return s
}
