package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Discovery. The enclave is every service that publishes an API, which is
// a self-applying definition: membership is answering /api, not being on a
// list. So puffin holds NO roster. It asks the mesh who exists:
//
//   - hm's truth table: who is a citizen (heartbeat-authorized on the bus)
//   - flipr's namespaces: who has onboarded flags
//   - then each candidate is probed by NAME for /health, Ping, and /api
//
// A service in hm but not answering /api is in the enclave's antechamber:
// listed, marked, not browsable. The gap IS the finding.

// State is the truth about one member, and there are five of them because there
// are five different truths.
//
// Flattening these into up/down is how puffin called a healthy spool daemon
// dead: the .test wildcard resolves everything, traefik answers 404 for names
// with no route, and "no route", "not deployed" and "down" are not the same
// word.
type State string

const (
	// StateHealthy: answers /health with 200. The web-routed happy path.
	StateHealthy State = "healthy"
	// StateUnhealthy: answers /health with a refusal and, being an honest
	// service, says why.
	StateUnhealthy State = "unhealthy"
	// StateDaemon: no route to probe -- traefik 404s the name -- but hm
	// shows a live heartbeat. A spool daemon's normal shape: alive by
	// citizenship, unreachable by design. A spool daemon lives here.
	//
	// The label says what the thing IS; "heartbeat" said how we knew, and
	// how-we-know is not a health status.
	StateDaemon State = "daemon"
	// StateDown: expected to be alive and is not -- a lapsed citizen, or a
	// route that answers nothing. The only state that earns red.
	StateDown State = "down"
	// StateAbsent: known to the mesh (flags in flipr) but not deployed and
	// not heartbeating. albatross before its migration. Dim, not red.
	StateAbsent State = "absent"
)

// Lease is hm's citizenship detail for one member: presence IS
// authorization in this mesh, so the lease is the authorization's terms.
type Lease struct {
	State     string // authorized | expiring | expired
	Beats     int
	CadenceMS int
	ExpiresAt time.Time
}

// TTL is how long the lease has left; negative means lapsed.
func (l Lease) TTL(now time.Time) time.Duration { return l.ExpiresAt.Sub(now) }

// Service is one enclave member as puffin sees it.
type Service struct {
	Name       string
	Env        string // which environment, from which resolver answered
	Version    string // from Ping, "" when unreachable
	State      State
	HealthNote string
	Citizen    string // hm's state: authorized | expiring | expired | ""
	Lease      *Lease // the full lease, when hm holds one
	HasAPI     bool   // answers /api with bytes that parse
	// the proto package versions published: ["v1"], or ["v1","v2"]
	APIVersions []string
	// flags in the latest flipr namespace, -1 when none
	Flags     int
	StaleNS   int // older version namespaces awaiting retirement
	Expensive int
}

// Enclave is a discovery result.
type Enclave struct {
	Services []Service
	// discovery-source failures, shown rather than swallowed
	Warnings []string
	When     time.Time
}

// client is the one http client, bounded: a TUI that hangs on a dead service
// is worse than one that says dead.
var client = &http.Client{Timeout: 3 * time.Second}

// hmTruth fetches the citizenship table, lease terms included.
func hmTruth(base string) (map[string]Lease, error) {
	resp, err := client.Get(base + "/truth")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Authorized []struct {
			Service   string    `json:"service"`
			State     string    `json:"state"`
			Beats     int       `json:"beats"`
			CadenceMS int       `json:"cadence_ms"`
			ExpiresAt time.Time `json:"expires_at"`
		} `json:"authorized"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("hm /truth is not hm-shaped: %w", err)
	}
	leases := map[string]Lease{}
	for _, a := range out.Authorized {
		leases[a.Service] = Lease{State: a.State, Beats: a.Beats,
			CadenceMS: a.CadenceMS, ExpiresAt: a.ExpiresAt}
	}
	return leases, nil
}

// fliprNamespaces fetches who has onboarded flags. Counts come from the
// latest version namespace only -- summing across stale deploy namespaces
// is how six flags read as sixty-eight and arrived on the screen as a
// flooding scare. Stale versions are reported as their own number instead.
func fliprNamespaces(base string) (map[string][3]int, error) {
	namespaces, err := listNamespaces(base)
	if err != nil {
		return nil, err
	}
	// newest namespace per service wins; the rest count as stale
	type best struct {
		when          string
		flags, expens int
		versions      int
	}
	perSvc := map[string]best{}
	for _, ns := range namespaces {
		when := ""
		expens := 0
		for _, f := range ns.GetFlags() {
			if f.GetUpdatedAt() > when {
				when = f.GetUpdatedAt()
			}
			if f.GetExpensive() {
				expens++
			}
		}
		b := perSvc[ns.GetService()]
		b.versions++
		if when >= b.when || b.when == "" {
			b.when, b.flags, b.expens = when, len(
				ns.GetFlags(),
			), expens
		}
		perSvc[ns.GetService()] = b
	}
	counts := map[string][3]int{}
	for svc, b := range perSvc {
		counts[svc] = [3]int{b.flags, b.expens, b.versions - 1}
	}
	return counts, nil
}

// probeOutcome is what the wire actually said, before citizenship weighs in.
type probeOutcome int

const (
	probeAnswered probeOutcome = iota // health JSON arrived, any status
	// traefik answered for a name with no route
	probeNoRoute
	probeUnreachable // nothing answered at all
)

// probe asks one named service how it is. Every check is independent: a
// service with health but no /api is reported exactly so.
//
// The classification that matters most is the one that went wrong: a 200 is
// only an answer when the body is health-shaped. The .test wildcard resolves
// every name and traefik happily 404s the unrouted ones, so "the request
// completed" means nothing by itself.
//
// Absent-but-routed is not down and it is not up; it is traefik talking about
// itself.
func probe(name, env, domain string) (Service, probeOutcome) {
	s := Service{Name: name, Env: env, Flags: -1}
	// The bare name IS the address: the edge on :80 proxies every name in
	// the domain -- host daemons by specific router, everything else
	// falling through the catch-all to the cluster's own proxy.
	//
	// One port, no fallback, deliberately: a second port would let a name
	// defined on BOTH edges answer as two different services unnoticed. The
	// proxy's own live router table is the truth when this needs
	// re-deriving.
	base := fmt.Sprintf("http://%s.%s", name, domain)

	outcome := probeUnreachable
	if resp, err := client.Get(base + "/health"); err == nil {
		body, _ := readCapped(resp)
		var h struct {
			Healthy *bool  `json:"healthy"`
			OK      *bool  `json:"ok"`
			Status  string `json:"status"`
			Version string `json:"version"`
			Reason  string `json:"reason"`
		}
		if json.Unmarshal(body, &h) == nil &&
			(h.Healthy != nil || h.OK != nil || h.Status != "") {
			outcome = probeAnswered
			healthy := (h.Healthy != nil && *h.Healthy) ||
				(h.OK != nil && *h.OK) ||
				h.Status == "ok"
			if healthy && resp.StatusCode == 200 {
				s.State = StateHealthy
			} else {
				s.State = StateUnhealthy
			}
			s.Version = h.Version
			s.HealthNote = h.Reason
		} else {
			// something answered, but not as a health endpoint: an
			// unrouted name behind the wildcard, or a service with
			// no /health
			outcome = probeNoRoute
		}
	}

	if resp, err := client.Get(base + "/api"); err == nil {
		body, _ := readCapped(resp)
		// membership is parsing, not sniffing: the bytes must be a real
		// descriptor set naming at least one service.
		//
		// The versions on the roster come from the same parse -- the
		// proto package versions the service actually publishes,
		// straight from its own mouth.
		if resp.StatusCode == 200 {
			if api, err := ParseAPI(body); err == nil {
				s.HasAPI = true
				s.APIVersions = api.Versions
			}
		}
	}
	return s, outcome
}

// resolveState combines what the wire said with what hm knows. The wire wins
// when it answered; citizenship decides the unrouted and unreachable cases.
func resolveState(outcome probeOutcome, citizen string, flags int) State {
	if outcome == probeAnswered {
		return "" // probe already set it; sentinel for "keep"
	}
	switch citizen {
	case "authorized":
		// alive by heartbeat, unreachable by design: the daemon shape
		return StateDaemon
	case "expiring", "expired":
		// was alive, went quiet, and nothing answers: earned the red
		return StateDown
	}
	if flags >= 0 {
		// the mesh knows the name but nothing runs: not deployed, not
		// dead
		return StateAbsent
	}
	return StateDown
}

// readCapped drains a response body with a sanity cap.
func readCapped(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for len(buf) < 1<<20 {
		n, err := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	return buf, nil
}

// Discover walks the sources and probes the union. Source failures become
// warnings on the result, never silent absences -- "hm is down" and "no
// citizens" are different answers.
func Discover(domain string) Enclave {
	e := Enclave{When: time.Now()}
	candidates := map[string]bool{}

	citizens, err := hmTruth(fmt.Sprintf("http://hm.%s", domain))
	if err != nil {
		e.Warnings = append(e.Warnings, "hm: "+err.Error())
		citizens = map[string]Lease{}
	}
	for name := range citizens {
		candidates[name] = true
	}

	flags, err := fliprNamespaces(fmt.Sprintf("http://flipr.%s", domain))
	if err != nil {
		e.Warnings = append(e.Warnings, "flipr: "+err.Error())
		flags = map[string][3]int{}
	}
	for name := range flags {
		if !strings.HasPrefix(
			name,
			"_",
		) { // _global is a scope, not a service
			candidates[name] = true
		}
	}

	names := make([]string, 0, len(candidates))
	for n := range candidates {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		s, outcome := probe(name, domain, domain)
		if l, ok := citizens[name]; ok {
			s.Citizen = l.State
			lease := l
			s.Lease = &lease
		}
		if c, ok := flags[name]; ok {
			s.Flags, s.Expensive, s.StaleNS = c[0], c[1], c[2]
		}
		if st := resolveState(outcome, s.Citizen, s.Flags); st != "" {
			s.State = st
		}
		e.Services = append(e.Services, s)
	}
	return e
}
