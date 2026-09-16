package main

import (
	"strings"
	"testing"
)

const furnJSON = `{"items":[` + "\n" +
	` {"kind":"Deployment","metadata":{"name":"surrealdb",` +
	`"namespace":"local"},` + "\n" +
	`  "spec":{"replicas":0,"template":{"spec":{"containers":[{"image":"s` +
	`urrealdb/surrealdb:v3.2.0"}]}}},` + "\n" +
	`  "status":{"readyReplicas":0}},` + "\n" +
	` {"kind":"Deployment","metadata":{"name":"postgres",` +
	`"namespace":"local"},` + "\n" +
	`  "spec":{"replicas":1,"template":{"spec":{"containers":[{"image":"l` +
	`ocal-postgres:18.6-pgvector0.8.6"}]}}},` + "\n" +
	`  "status":{"readyReplicas":1}},` + "\n" +
	` {"kind":"Deployment","metadata":{"name":"dodo",` +
	`"namespace":"local"},` + "\n" +
	`  "spec":{"replicas":1,"template":{"spec":{"containers":[{"image":"d` +
	`odo:dev"}]}}},` + "\n" +
	`  "status":{"readyReplicas":1}}]}`

// A furnishing is a workload the roster does not claim. Defined by
// membership, not by image name, because the image cannot decide:
// local-postgres carries no registry prefix and is not ours, dodo:dev
// carries none and is.
func TestFurnishingsAreWhatTheRosterDoesNotClaim(t *testing.T) {
	got := parseFurnishings([]byte(furnJSON), map[string]bool{"dodo": true})
	if len(got) != 2 {
		t.Fatalf(
			"got %d furnishings, want 2 (dodo is a member)",
			len(got),
		)
	}
	for _, f := range got {
		if f.Name == "dodo" {
			t.Error("a roster member was listed as a furnishing")
		}
	}
}

// A database missing from the front page: it was scaled to zero and
// nothing said so.
func TestScaledToZeroIsNotHealthy(t *testing.T) {
	got := parseFurnishings([]byte(furnJSON), nil)
	for _, f := range got {
		if f.Name == "surrealdb" && f.Healthy() {
			t.Error("a deployment at 0/0 reported healthy")
		}
	}
}

// A sidecar counts. The postgres deployment runs local-postgres AND
// quay.io/prometheuscommunity/postgres-exporter; taking only the first
// container reported it as a purely local build while an upstream image ran
// beside it, unseen. A sidecar is NOT the workload.
//
// postgres-exporter is not postgres. It is a monitoring sidecar that reads
// postgres. Marking the deployment as borrowed because its exporter is
// borrowed states a true fact about the exporter and a false one about the
// database.
func TestASidecarDoesNotDecideProvenance(t *testing.T) {
	f := Furnishing{Name: "postgres", Containers: []Container{
		{
			Name: "postgres",
			Image: "local-postgres:18.6-pgvector0.8.6" +
				"-postgis3.6.4-hc",
		},
		{
			Name: "exporter",
			Image: "quay.io/prometheuscommunity/" +
				"postgres-exporter:v0.17.1",
		},
	}}
	if f.Upstream() {
		t.Error("postgres was marked borrowed because its exporter is")
	}
	if f.Primary().Name != "postgres" {
		t.Errorf(
			"primary is %q, want the container named after the "+
				"workload",
			f.Primary().Name,
		)
	}
	if n := len(f.Sidecars()); n != 1 {
		t.Errorf("%d sidecars, want 1", n)
	}
	// and it is not hidden either
	if !strings.Contains(f.Image(), "+1 sidecar") {
		t.Errorf("the row hides the sidecar: %q", f.Image())
	}
}

// When nothing is named after the workload, spec order decides -- a weaker
// signal, used only when the better one is absent.
func TestPrimaryFallsBackToSpecOrder(t *testing.T) {
	f := Furnishing{Name: "athlete", Containers: []Container{
		{Name: "app", Image: "kestrel-panes:dev"},
		{Name: "sidecar", Image: "quay.io/x/y:1"},
	}}
	if f.Primary().Image != "kestrel-panes:dev" {
		t.Errorf(
			"primary = %q, want the first container",
			f.Primary().Image,
		)
	}
	if f.Upstream() {
		t.Error(
			"a sidecar decided provenance through the fallback " +
				"path",
		)
	}
}

// Every container is kept, not just the first.
func TestAllContainersAreParsed(t *testing.T) {
	out := []byte(
		`{"items":[{"kind":"Deployment",` +
			`"metadata":{"name":"postgres",` +
			`"namespace":"local"},` + "\n" +
			`	  "spec":{"replicas":1,` +
			`"template":{"spec":{"containers":[` + "\n" +
			`	    {"name":"postgres",` +
			`"image":"local-postgres:18.6"},` + "\n" +
			`	    {"name":"exporter",` +
			`"image":"quay.io/prometheuscommunity/postgre` +
			`s-exporter:v0.17.1"}]}}},` + "\n" +
			`	  "status":{"readyReplicas":1}}]}`,
	)
	got := parseFurnishings(out, nil)
	if len(got) != 1 || len(got[0].Containers) != 2 {
		t.Fatalf("parsed %d containers, want 2", len(got[0].Containers))
	}
}

// The caret is about where the image came from, not who wrote the software.
// postgres is upstream software in a locally built image and must not be
// marked as pulled from a registry.
func TestUpstreamIsAboutTheImageNotTheAuthor(t *testing.T) {
	for _, c := range []struct {
		image string
		up    bool
	}{
		{"surrealdb/surrealdb:v3.2.0", true},
		{"ghcr.io/valhalla/valhalla:3.5.1", true},
		{"registry.k8s.io/kube-state-metrics/" +
			"kube-state-metrics:v2.13.0", true},
		{"local-postgres:18.6-pgvector0.8.6-postgis3.6.4-hc", false},
		{"peacock:f09f441", false},
		{"dodo:dev", false},
	} {
		f := Furnishing{Containers: []Container{{Image: c.image}}}
		if got := f.Upstream(); got != c.up {
			t.Errorf(
				"%s: Upstream()=%v want %v",
				c.image,
				got,
				c.up,
			)
		}
	}
}

// The default order puts the broken first, because a furnishing at 0/0 is
// the whole reason for the block and alphabetical order buries it.
func TestBrokenSortsFirst(t *testing.T) {
	rows := parseFurnishings([]byte(furnJSON), nil)
	sortFurnishings(rows, 0)
	if rows[0].Name != "surrealdb" {
		t.Errorf(
			"first row is %q, want the broken one first",
			rows[0].Name,
		)
	}
	if furnSortName(0) != "broken first" {
		t.Errorf("sort 0 is %q", furnSortName(0))
	}
}

// Same lesson as agentSorts: the chosen order is an index, so appending is
// safe and inserting is not.
func TestFurnSortOrderIsFrozen(t *testing.T) {
	want := []string{"broken first", "name", "image", "borrowed first"}
	for i, w := range want {
		if i >= len(furnSorts) || furnSorts[i].name != w {
			t.Fatalf(
				"sort %d changed; a remembered choice would "+
					"now mean something else",
				i,
			)
		}
	}
}

// kubectl failing must not cost the page.
func TestBadJSONYieldsNothing(t *testing.T) {
	if got := parseFurnishings([]byte("nope"), nil); got != nil {
		t.Errorf("garbage produced %v", got)
	}
}

// The block says how many are broken, because that is the number worth
// seeing before any of the rows.
func TestBlockHeadlinesTheBroken(t *testing.T) {
	m := newModel("test", "")
	m.furnishings = parseFurnishings([]byte(furnJSON), nil)
	out := furnishingsBlock(Compile(Corvid()), m)
	if !strings.Contains(out, "1 not running") {
		t.Errorf(
			"the header does not count the broken:\n%s",
			strings.SplitN(out, "\n", 3)[1],
		)
	}
}
