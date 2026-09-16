package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

// The CLI against a flipr-shaped fake, including the piece the TUI never
// exercises and everything else depends on: newest-namespace resolution by the
// flags' own timestamps. Output goes through os.Stdout capture where the words
// matter.

// fakeFliprCLI serves ListNamespaces with two dodo namespaces -- one stale,
// one newer -- and a SetFlag that records what arrived.
func fakeFliprCLI(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	resetFlipr() // a fresh fake gets first contact
	var sets []string
	var publishes atomic.Int32
	mux := http.NewServeMux()
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
			publishes.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"flagsPublished":1}`))
		},
	)
	t.Cleanup(func() {
		// one client per process: however many screens read, puffin
		// declared itself once (kingfisher's review of PR 24)
		if n := publishes.Load(); n > 1 {
			t.Errorf(
				"puffin published its declaration %d times "+
					"in one process; a read must not be "+
					"a write",
				n,
			)
		}
	})
	// puffin's own namespace, read before a flip: flip.enabled on
	mux.HandleFunc(
		"/flipr.v1.FliprService/GetNamespace",
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write(
				[]byte(
					`{"namespace":{"service":"puffin",` +
						`"version":"v1",` +
						`"flags":[{"key":"flip.enable` +
						`d","value":{"boolValue":true` +
						`}}]}}`,
				),
			)
		},
	)
	mux.HandleFunc(
		"/flipr.v1.FliprService/ListNamespaces",
		func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"namespaces":[` + "\n" +
				`{"service":"dodo","version":"old1",` +
				`"flags":[` + "\n" +
				`{"key":"log.level",` +
				`"value":{"stringValue":"warn"},` +
				`"updatedAt":"2026-08-27T00:00:00Z"}]` +
				`},` + "\n" +
				`{"service":"dodo","version":"new2",` +
				`"flags":[` + "\n" +
				`{"key":"log.level",` +
				`"value":{"stringValue":"info"},` +
				`"updatedAt":"2026-08-28T12:00:00Z"}` +
				`,` + "\n" +
				`{"key":"panes.map.enabled",` +
				`"value":{"boolValue":true},` +
				`"description":"the map pane",` +
				`"updatedAt":"2026-08-28T12:00:00Z"}]` +
				`},` + "\n" +
				`{"service":"kestrel","version":"v1",` +
				`"flags":[` + "\n" +
				`{"key":"fetch.terrain",` +
				`"value":{"boolValue":false},` +
				`"expensive":true,` + "\n" +
				`"description":"on: fetches. off: does not.",` +
				`"updatedAt":"2026-08-28T11:00:00Z"}]` +
				`}]}`))
		},
	)
	mux.HandleFunc(
		"/flipr.v1.FliprService/SetFlag",
		func(w http.ResponseWriter, r *http.Request) {
			b := make([]byte, 4096)
			n, _ := r.Body.Read(b)
			sets = append(sets, string(b[:n]))
			w.Write([]byte(`{"flag":{"key":"k"}}`))
		},
	)
	srv := httptest.NewServer(mux)
	return srv, &sets
}

// proxyTo points the shared client at the fake for the test's duration.
func proxyTo(t *testing.T, addr string) {
	t.Helper()
	old := client.Transport
	client.Transport = &http.Transport{
		Proxy: func(r *http.Request) (*url.URL, error) {
			return url.Parse("http://" + addr)
		},
	}
	t.Cleanup(func() { client.Transport = old })
}

// capture runs f with stdout redirected and returns what it printed.
func capture(t *testing.T, f func() int) (string, int) {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	code := f()
	w.Close()
	os.Stdout = old
	buf := make([]byte, 1<<16)
	n, _ := r.Read(buf)
	return string(buf[:n]), code
}

// TestLatestFlagsResolvesTheNewestNamespace is the piece nobody sees and
// everybody depends on: dodo@new2 wins over dodo@old1, and its values are
// the ones returned.
func TestLatestFlagsResolvesTheNewestNamespace(t *testing.T) {
	srv, _ := fakeFliprCLI(t)
	defer srv.Close()
	proxyTo(t, srv.Listener.Addr().String())

	rows, err := latestFlags("faketest")
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]FlagRow{}
	for _, r := range rows {
		byKey[r.Service+"/"+r.Key] = r
	}
	ll := byKey["dodo/log.level"]
	if ll.Version != "new2" || ll.Value != "info" {
		t.Fatalf("stale namespace won: %+v", ll)
	}
	if _, ok := byKey["dodo/panes.map.enabled"]; !ok {
		t.Fatal("newest namespace's flags incomplete")
	}
	if len(rows) != 3 {
		t.Fatalf(
			"want 3 rows (2 dodo new2 + 1 kestrel), got %d",
			len(rows),
		)
	}
}

// TestCliGetPrintsTheBareValue: $(puffin get ...) must capture cleanly.
func TestCliGetPrintsTheBareValue(t *testing.T) {
	srv, _ := fakeFliprCLI(t)
	defer srv.Close()
	proxyTo(t, srv.Listener.Addr().String())

	out, code := capture(
		t,
		func() int {
			return cliGet(
				"faketest",
				"kestrel",
				"fetch.terrain",
			)
		},
	)
	if code != 0 || strings.TrimSpace(out) != "false" {
		t.Fatalf("got %q code %d", out, code)
	}
	_, code = capture(
		t,
		func() int { return cliGet("faketest", "kestrel", "nope") },
	)
	if code != 1 {
		t.Fatalf("missing flag should exit 1, got %d", code)
	}
}

// TestCliSetTypesByDeclaredKind: a boolean flag refuses banana before the
// wire, a conforming flip sends typed protojson with the reason.
func TestCliSetTypesByDeclaredKind(t *testing.T) {
	srv, sets := fakeFliprCLI(t)
	defer srv.Close()
	proxyTo(t, srv.Listener.Addr().String())

	_, code := capture(t, func() int {
		return cliSet(
			"faketest",
			[]string{
				"kestrel",
				"fetch.terrain",
				"banana",
				"--reason",
				"x",
			},
		)
	})
	if code != 2 || len(*sets) != 0 {
		t.Fatalf(
			"banana reached the wire: code %d, sets %d",
			code,
			len(*sets),
		)
	}
	_, code = capture(t, func() int {
		return cliSet(
			"faketest",
			[]string{"kestrel", "fetch.terrain", "true"},
		)
	})
	if code != 2 {
		t.Fatalf("reasonless set should exit 2, got %d", code)
	}
	out, code := capture(t, func() int {
		return cliSet(
			"faketest",
			[]string{
				"kestrel",
				"fetch.terrain",
				"true",
				"--reason",
				"cli test",
			},
		)
	})
	if code != 0 || !strings.Contains(out, "kestrel/fetch.terrain = true") {
		t.Fatalf("got %q code %d", out, code)
	}
	if len(*sets) != 1 ||
		!strings.Contains((*sets)[0], `"boolValue":true`) ||
		!strings.Contains((*sets)[0], `"reason":"cli test"`) {
		t.Fatalf("wire body wrong: %v", *sets)
	}
}

// TestCliFlagsFiltersAndMarksExpensive covers the listing shapes.
func TestCliFlagsFiltersAndMarksExpensive(t *testing.T) {
	srv, _ := fakeFliprCLI(t)
	defer srv.Close()
	proxyTo(t, srv.Listener.Addr().String())

	out, code := capture(
		t,
		func() int { return cliFlags("faketest", "kestrel") },
	)
	if code != 0 || !strings.Contains(out, "$ kestrel") {
		t.Fatalf("expensive marker missing: %q", out)
	}
	if strings.Contains(out, "dodo") {
		t.Fatal("filter leaked another service")
	}
	_, code = capture(
		t,
		func() int { return cliFlags("faketest", "ghost") },
	)
	if code != 1 {
		t.Fatalf("unknown service should exit 1, got %d", code)
	}
}

// TestRunCLIDispatch covers the top-level grammar and the help exit codes.
func TestRunCLIDispatch(t *testing.T) {
	if _, code := capture(
		t,
		func() int { return runCLI([]string{"help"}) },
	); code != 0 {
		t.Fatalf("help: %d", code)
	}
	if _, code := capture(
		t,
		func() int { return runCLI([]string{"wat"}) },
	); code != 2 {
		t.Fatalf("unknown: %d", code)
	}
	if _, code := capture(
		t,
		func() int { return runCLI([]string{"get", "only-two"}) },
	); code != 2 {
		t.Fatalf("bad arity: %d", code)
	}
}

// `puffin agents` has to answer the question you actually ask from a shell,
// which is not "what is running" but "is this session all right".
//
// A session that looks unhappy is one the command line should be able to
// report on. The row said "idle" and nothing else. It was at 99% of its
// context window, which is a different kind of idle -- a session waiting
// for a wall.
func TestCLIAgentsCarriesTheStats(t *testing.T) {
	out := captureStdout(t, func() { cliAgents() })
	if out == "" {
		t.Skip("no sessions on this host")
	}
	for _, col := range []string{
		"name",
		"project",
		"model",
		"state",
		"turns",
		"tokens",
		"context",
		"cost",
		"last log",
	} {
		if !strings.Contains(out, col) {
			t.Errorf(
				"the header is missing %q:\n%s",
				col,
				headerLine(out),
			)
		}
	}
	// Alignment is the point of a table. %-16s pads and never truncates, so
	// one long name shifts every column after it on that row alone -- and
	// the reason to assert it is that the header still looks fine.
	head := headerLine(out)
	// Columns are counted in runes, not bytes. pad() truncates with a
	// horizontal ellipsis, which is one column on screen and three bytes in
	// UTF-8, so a byte index into a row that contains one lands two columns
	// early and reports a misalignment that is not there.
	//
	// The header is ASCII and the rows are not, which is exactly the case
	// where the two measurements disagree.
	//
	// This was latent until 2026-09-06, when a session called
	// kingfisher-worker was the first name long enough for the 17-column
	// name field to truncate at all. trackAt already learned this lesson on
	// the cluster screen; the test had not.
	hr := []rune(head)
	col := runeIndex(hr, "state")
	if col < 0 {
		t.Fatalf("no state column in header: %s", head)
	}
	for _, line := range strings.Split(out, "\n")[1:] {
		if strings.TrimSpace(line) == "" ||
			strings.HasPrefix(line, "to rejoin") {
			continue
		}
		lr := []rune(line)
		if len(lr) < col+1 {
			t.Errorf(
				"row too short to reach the state column:\n%q",
				line,
			)
			continue
		}
		// the character just before the state column must be padding
		if lr[col-1] != ' ' {
			t.Errorf(
				"a field overflowed and pushed the state "+
					"column:\n%s\n%s",
				head,
				line,
			)
			break
		}
	}
}

// headerLine is the first line of a captured table.
func headerLine(s string) string {
	if i := strings.Index(s, "\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		io.Copy(&b, r)
		done <- b.String()
	}()
	fn()
	w.Close()
	os.Stdout = prev
	return <-done
}

// runeIndex is strings.Index in columns rather than bytes.
func runeIndex(hay []rune, needle string) int {
	n := []rune(needle)
	for i := 0; i+len(n) <= len(hay); i++ {
		if string(hay[i:i+len(n)]) == needle {
			return i
		}
	}
	return -1
}
