package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// The furnishings: what runs in the cluster that the enclave does not claim.
//
// The roster asks the mesh who exists, and a service is a member because it
// answers -- hm holds its lease, flipr holds its flags, it serves /api. That is
// the right definition and it has a consequence: postgres, prometheus, kafka,
// surrealdb and valhalla are invisible.
//
// They are furnished rather than written -- borrowed images, shipped through
// kustomize -- so they publish no contract and belong to no roster.
//
// That is a hole with teeth in it: the front page said the enclave was
// fine while the database it sits on was not mentioned at all.
//
// A furnishing is defined here as a workload the cluster is running that the
// roster does not list. Not by image name, which cannot decide: a locally built
// postgres tagged with its extensions carries no registry prefix, and so does a
// service of ours built the same afternoon.
//
// Membership is the fact puffin already has and the only one that does not
// require guessing.

// Furnishing is one borrowed thing, reduced to what an operator reads.
type Furnishing struct {
	Name      string
	Namespace string
	// Containers is every container, name and image, in spec order.
	//
	// It was containers[0], which silently dropped the rest: a postgres
	// deployment runs the database AND an upstream metrics exporter beside
	// it, so it read as a purely local build while quietly running an
	// upstream image nobody could see.
	//
	// A provenance mark built on the first container is a provenance mark
	// that lies about the sidecar.
	Containers []Container
	Ready      int
	Want       int
	Kind       string // Deployment | StatefulSet
}

// Upstream reports whether the image came from somewhere else. A slash before
// the first colon means a registry or an org owns it.
//
// Not a judgement about who wrote the software -- postgres is upstream software
// in a locally built image -- only about where this image came from, which is
// what an operator chasing a version actually needs. Container is one
// container's identity.
type Container struct{ Name, Image string }

// Primary is the container the workload is FOR. The rest are sidecars.
//
// A postgres deployment that runs a postgres-exporter beside postgres is
// not running two databases. The exporter is a monitoring sidecar that
// reads postgres, and its provenance is a fact about the sidecar rather
// than about the database.
//
// The rule is kubernetes' own convention and it is found, not guessed: the
// container named after the workload is the primary. Falling back to the
// first only when nothing matches, because spec order is a weaker signal
// than a name and should not be relied on when a better one exists.
func (f Furnishing) Primary() Container {
	for _, c := range f.Containers {
		if c.Name == f.Name {
			return c
		}
	}
	if len(f.Containers) > 0 {
		return f.Containers[0]
	}
	return Container{}
}

// Sidecars are everything that is not the primary.
func (f Furnishing) Sidecars() []Container {
	p := f.Primary()
	var out []Container
	for _, c := range f.Containers {
		if c.Name != p.Name {
			out = append(out, c)
		}
	}
	return out
}

// Upstream is about the primary container only.
//
// It briefly meant "any container", which marked the postgres deployment as
// borrowed because its exporter is. That is a true fact about the exporter
// and a false one about postgres, and the column was claiming the second.
func (f Furnishing) Upstream() bool {
	return imageIsUpstream(
		f.Primary().Image,
	)
}

// imageIsUpstream reports whether one reference came from elsewhere. A
// slash before the tag means a registry or an org owns it.
func imageIsUpstream(ref string) bool {
	if i := strings.LastIndex(ref, ":"); i > 0 {
		ref = ref[:i]
	}
	return strings.Contains(ref, "/")
}

// Image is what the row shows: the primary, and a note when something else
// rides along. The sidecar is not hidden and is not conflated with the
// thing it watches.
func (f Furnishing) Image() string {
	p := f.Primary()
	if p.Image == "" {
		return "-"
	}
	if n := len(f.Sidecars()); n > 0 {
		return fmt.Sprintf("%s  +%d sidecar", p.Image, n)
	}
	return p.Image
}

// Healthy is every replica the spec asked for, running.
func (f Furnishing) Healthy() bool { return f.Want > 0 && f.Ready == f.Want }

// fetchFurnishings lists workloads and removes the ones the roster claims.
// members is the set of names the enclave already accounts for.
func fetchFurnishings(ctx string, members map[string]bool) []Furnishing {
	out, err := exec.Command("kubectl", "--context", ctx,
		"get", "deployments,statefulsets", "-A", "-o", "json").Output()
	if err != nil {
		return nil
	}
	return parseFurnishings(out, members)
}

// parseFurnishings is the reduction, split out so it is testable without a
// cluster.
func parseFurnishings(out []byte, members map[string]bool) []Furnishing {
	var raw struct {
		Items []struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Spec struct {
				Replicas *int `json:"replicas"`
				Template struct {
					Spec struct {
						Containers []struct {
							Name  string `json:"name"`
							Image string `json:"image"`
						} `json:"containers"`
					} `json:"spec"`
				} `json:"template"`
			} `json:"spec"`
			Status struct {
				ReadyReplicas int `json:"readyReplicas"`
			} `json:"status"`
		} `json:"items"`
	}
	if json.Unmarshal(out, &raw) != nil {
		return nil
	}
	var out2 []Furnishing
	for _, it := range raw.Items {
		if members[it.Metadata.Name] {
			continue // the roster already speaks for it
		}
		if len(it.Spec.Template.Spec.Containers) == 0 {
			continue
		}
		want := 1
		if it.Spec.Replicas != nil {
			want = *it.Spec.Replicas
		}
		kind := it.Kind
		if kind == "" {
			kind = "Deployment"
		}
		cs := make(
			[]Container,
			0,
			len(it.Spec.Template.Spec.Containers),
		)
		for _, c := range it.Spec.Template.Spec.Containers {
			cs = append(cs, Container{Name: c.Name, Image: c.Image})
		}
		out2 = append(out2, Furnishing{
			Name:       it.Metadata.Name,
			Namespace:  it.Metadata.Namespace,
			Containers: cs,
			Ready:      it.Status.ReadyReplicas,
			Want:       want,
			Kind:       kind,
		})
	}
	sort.Slice(
		out2,
		func(i, j int) bool { return out2[i].Name < out2[j].Name },
	)
	return out2
}

// furnishingsBlock draws what the roster does not speak for.
//
// It is a block rather than more roster rows, because these are a different
// kind of thing and saying so is the point: the roster is the enclave, and
// this is what the enclave is standing on. Merging them would suggest
// postgres publishes a contract, which it does not and should not.
//
// Sorted with the broken first, unconditionally. A furnishing at 0/0 is the
// reason to have this block at all -- surrealdb was scaled to zero and the
// front page had never mentioned it -- and a list that buries that
// alphabetically has answered the wrong question.
func furnishingsBlock(s Styles, m model) string {
	if len(m.furnishings) == 0 {
		return ""
	}
	rows := make([]Furnishing, len(m.furnishings))
	copy(rows, m.furnishings)
	sortFurnishings(rows, m.furnSort)

	var b strings.Builder
	broken := 0
	for _, f := range rows {
		if !f.Healthy() {
			broken++
		}
	}
	head := fmt.Sprintf("furnishings · %d", len(rows))
	if broken > 0 {
		head += fmt.Sprintf(" · %d not running", broken)
	}
	b.WriteString("\n" + s.Header.Render(head) +
		s.Dim.Render("  ·  F sorts: "+furnSortName(m.furnSort)) + "\n")

	for _, f := range rows {
		ready := fmt.Sprintf("%d/%d", f.Ready, f.Want)
		style := s.Dim
		if !f.Healthy() {
			style = s.Warn
		}
		// the caret is the whole taxonomy: an image with a registry or
		// an org before it came from somewhere else, and that is the
		// fact an operator chasing a version needs. It is not a claim
		// about who wrote the software.
		mark := " "
		if f.Upstream() {
			mark = "^"
		}
		b.WriteString("  " + s.Dim.Render(mark+" ") +
			s.Row.Render(pad(f.Name, 26)) +
			style.Render(pad(ready, 7)) +
			s.Dim.Render(padTo(f.Image(), 52)) + "\n")
	}
	return b.String()
}

// The sort orders, by position. Appended to, never inserted into: the
// chosen order is remembered, and inserting one silently redefines a saved
// preference -- which agentSorts learned on 2026-09-01.
var furnSorts = []struct {
	name string
	less func(a, b Furnishing) bool
}{
	{"broken first", func(a, b Furnishing) bool {
		if a.Healthy() != b.Healthy() {
			return !a.Healthy()
		}
		return a.Name < b.Name
	}},
	{"name", func(a, b Furnishing) bool { return a.Name < b.Name }},
	{"image", func(a, b Furnishing) bool { return a.Image() < b.Image() }},
	{"borrowed first", func(a, b Furnishing) bool {
		if a.Upstream() != b.Upstream() {
			return a.Upstream()
		}
		return a.Name < b.Name
	}},
}

// furnSortName wraps the index so cycling the sort key cannot run off the
// end of the list.
func furnSortName(i int) string { return furnSorts[i%len(furnSorts)].name }

// sortFurnishings orders the rows in place. Stable, so a second key never
// shuffles rows that tie on the first.
func sortFurnishings(rows []Furnishing, by int) {
	less := furnSorts[by%len(furnSorts)].less
	sort.SliceStable(
		rows,
		func(i, j int) bool { return less(rows[i], rows[j]) },
	)
}

// furnishingsFetched carries the list back into the model.
type furnishingsFetched struct{ f []Furnishing }

// furnishingsCmd works out what the cluster runs that the roster does not
// claim. It runs after discovery, because the answer is defined against it.
func furnishingsCmd(e Enclave) tea.Cmd {
	members := make(map[string]bool, len(e.Services))
	for _, s := range e.Services {
		members[s.Name] = true
	}
	ctx := kubeContext()
	return func() tea.Msg {
		return furnishingsFetched{f: fetchFurnishings(ctx, members)}
	}
}
