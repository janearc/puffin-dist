package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// timeNow keeps the lease assertions readable.
func timeNow() time.Time { return time.Now() }

// Discovery against mesh-shaped fakes: hm's truth shape, flipr's namespace
// shape, and the failure modes that must surface as warnings rather than
// silent absences.

// fakeMesh stands up one server answering as hm, flipr, and two probed
// services, routed by Host header the way traefik does -- so discovery's
// name-based addressing is exercised for real.
func fakeMesh(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	resetFlipr() // a fresh fake gets first contact
	mux := http.NewServeMux()
	mux.HandleFunc("/truth", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"authorized":[` + "\n" +
			`{"service":"flipr","state":"authorized","beats":313,` +
			`"cadence_ms":30000,` +
			`"expires_at":"2199-01-01T00:00:00Z"},` + "\n" +
			`{"service":"ghost","state":"expired","beats":5,` +
			`"cadence_ms":15000,` +
			`"expires_at":"2020-01-01T00:00:00Z"},` + "\n" +
			`{"service":"ingestd","state":"authorized",` +
			`"beats":146,"cadence_ms":30000,` +
			`"expires_at":"2199-01-01T00:00:00Z"}]}`))
	})
	// the published client makes first contact (Ping) and declares puffin's
	// own flag (PublishNamespace) before it lists; the fake answers both
	mux.HandleFunc(
		"/flipr.v1.FliprService/Ping",
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write(
				[]byte(
					`{"storeGeneration":"g1",` +
						`"version":"fake"}`,
				),
			)
		},
	)
	mux.HandleFunc(
		"/flipr.v1.FliprService/PublishNamespace",
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"flagsPublished":1}`))
		},
	)
	mux.HandleFunc(
		"/flipr.v1.FliprService/ListNamespaces",
		func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"namespaces":[` + "\n" +
				`{"service":"flipr","version":"v",` +
				`"flags":[{"expensive":true},` +
				`{"expensive":false}]},` + "\n" +
				`{"service":"_global","version":"_",` +
				`"flags":[{"expensive":false}]}]}`))
		},
	)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Host, "ingestd.") {
			// no route by design: traefik answers for itself, in
			// plaintext
			w.WriteHeader(404)
			w.Write([]byte("404 page not found"))
			return
		}
		if strings.HasPrefix(r.Host, "ghost.") {
			w.WriteHeader(503)
			w.Write(
				[]byte(
					`{"healthy":false,` +
						`"reason":"store gone"}`,
				),
			)
			return
		}
		w.Write([]byte(`{"healthy":true,"version":"abc1234"}`))
	})
	mux.HandleFunc("/api", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Host, "ghost.") ||
			strings.HasPrefix(r.Host, "ingestd.") {
			w.WriteHeader(404)
			w.Write([]byte("404 page not found"))
			return
		}
		// probe parses now, so the fake serves a real descriptor set
		w.Write(buildFDS(t))
	})
	srv := httptest.NewServer(mux)
	return srv, srv.Listener.Addr().String()
}

// TestDiscoverAgainstAFakeMesh checks the union, the folding, and the
// _global exclusion.
func TestDiscoverAgainstAFakeMesh(t *testing.T) {
	srv, addr := fakeMesh(t)
	defer srv.Close()

	// point name resolution at the fake: every *.faketest name dials the
	// fake server, which routes by Host -- traefik's shape exactly
	oldTransport := client.Transport
	client.Transport = &http.Transport{
		Proxy: func(r *http.Request) (*url.URL, error) {
			return url.Parse("http://" + addr)
		},
	}
	defer func() { client.Transport = oldTransport }()

	e := Discover("faketest")
	_ = e
	// NB Discover builds names as service.domain:9800; with the proxy the
	// fake sees the Host and routes. Assertions:
	if len(e.Warnings) != 0 {
		t.Fatalf("warnings from a healthy fake: %v", e.Warnings)
	}
	if len(e.Services) != 3 {
		t.Fatalf("want flipr+ghost+ingestd, got %+v", e.Services)
	}
	byName := map[string]Service{}
	for _, s := range e.Services {
		byName[s.Name] = s
	}
	if _, leaked := byName["_global"]; leaked {
		t.Fatal(
			"_global is a scope, not a service, and it leaked " +
				"into the roster",
		)
	}
	f := byName["flipr"]
	if f.State != StateHealthy || f.Citizen != "authorized" || !f.HasAPI ||
		f.Flags != 2 ||
		f.Expensive != 1 {
		t.Fatalf("flipr row wrong: %+v", f)
	}
	// the api column's versions come from the descriptor's package
	if len(f.APIVersions) != 1 || f.APIVersions[0] != "v1" {
		t.Fatalf("api versions wrong: %v", f.APIVersions)
	}
	// the lease rides along whole: beats, cadence, and a live TTL
	if f.Lease == nil || f.Lease.Beats != 313 ||
		f.Lease.CadenceMS != 30000 {
		t.Fatalf("lease lost in discovery: %+v", f.Lease)
	}
	if f.Lease.TTL(timeNow()) <= 0 {
		t.Fatal("a 2199 expiry should have positive TTL")
	}
	g := byName["ghost"]
	// ghost answers /health with a refusal, so it is unhealthy -- an honest
	// answer -- not down
	if g.State != StateUnhealthy || g.Citizen != "expired" {
		t.Fatalf("ghost row wrong: %+v", g)
	}
	if g.HasAPI {
		t.Fatal("a 404 page counted as an API")
	}
}

// TestDiscoverSurfacesSourceFailures checks a dead hm and a dead flipr
// become warnings, not empty silence -- "down" and "no citizens" are
// different answers.
func TestDiscoverSourceFailuresAreWarnings(t *testing.T) {
	oldTransport := client.Transport
	client.Transport = &http.Transport{
		Proxy: func(r *http.Request) (*url.URL, error) {
			return url.Parse(
				"http://127.0.0.1:1",
			) // nothing listens here
		},
	}
	defer func() { client.Transport = oldTransport }()

	e := Discover("faketest")
	if len(e.Warnings) != 2 {
		t.Fatalf("want hm+flipr warnings, got %v", e.Warnings)
	}
	if len(e.Services) != 0 {
		t.Fatalf("services from a dead mesh: %+v", e.Services)
	}
}

// TestResolveState is the matrix the incident demanded: outcome x citizenship
// x flags, every cell the truth it should be. kingfisher-ingestd is the
// daemon row; albatross is the absent row.
func TestResolveState(t *testing.T) {
	cases := []struct {
		outcome probeOutcome
		citizen string
		flags   int
		want    State
	}{
		{
			probeAnswered,
			"authorized",
			3,
			"",
		}, // wire answered: probe's word stands
		{
			probeNoRoute,
			"authorized",
			3,
			StateDaemon,
		}, // kingfisher-ingestd
		{
			probeNoRoute,
			"",
			5,
			StateAbsent,
		}, // albatross pre-migration
		{
			probeNoRoute,
			"expired",
			3,
			StateDown,
		}, // went quiet AND unrouted
		{
			probeNoRoute,
			"",
			-1,
			StateDown,
		}, // nobody knows it: down
		{probeUnreachable, "authorized", -1, StateDaemon},
		{probeUnreachable, "", 2, StateAbsent},
		{probeUnreachable, "expiring", -1, StateDown},
	}
	for _, c := range cases {
		if got := resolveState(
			c.outcome,
			c.citizen,
			c.flags,
		); got != c.want {
			t.Errorf("resolveState(%v, %q, %d) = %q, want %q",
				c.outcome, c.citizen, c.flags, got, c.want)
		}
	}
}

// TestDaemonShapeAgainstAFakeMesh adds the exact live incident to the fake:
// a citizen with no route must render as heartbeat, never down.
func TestDaemonShapeAgainstAFakeMesh(t *testing.T) {
	srv, addr := fakeMesh(t)
	defer srv.Close()
	oldTransport := client.Transport
	client.Transport = &http.Transport{
		Proxy: func(r *http.Request) (*url.URL, error) {
			return url.Parse("http://" + addr)
		},
	}
	defer func() { client.Transport = oldTransport }()

	// the fake's /truth includes ingestd as authorized; its /health and
	// /api answer traefik-shaped 404s for that host (no route by design)
	e := Discover("faketest")
	for _, s := range e.Services {
		if s.Name == "ingestd" {
			if s.State != StateDaemon {
				t.Fatalf(
					"a heartbeating unrouted daemon "+
						"rendered %q, want %q",
					s.State,
					StateDaemon,
				)
			}
			return
		}
	}
	t.Fatal("ingestd not discovered from hm's truth")
}
