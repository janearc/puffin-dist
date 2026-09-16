//go:build live

package main

import (
	"strings"
	"testing"
)

// The live check, run deliberately with -tags live against the real local
// enclave: discovery must find flipr as a healthy, authorized, api-publishing
// citizen -- the roster's ground truth, not a fake's.
func TestLiveEnclave(t *testing.T) {
	e := Discover("test")
	for _, w := range e.Warnings {
		t.Logf("warning: %s", w)
	}
	var flipr *Service
	for i := range e.Services {
		t.Logf(
			"%s: state=%v citizen=%s api=%v flags=%d",
			e.Services[i].Name,
			e.Services[i].State,
			e.Services[i].Citizen,
			e.Services[i].HasAPI,
			e.Services[i].Flags,
		)
		if e.Services[i].Name == "flipr" {
			flipr = &e.Services[i]
		}
	}
	if flipr == nil {
		t.Fatal(
			"the enclave does not contain flipr; something is " +
				"very wrong",
		)
	}
	if flipr.State != StateHealthy || !flipr.HasAPI ||
		flipr.Citizen != "authorized" {
		t.Fatalf("flipr row: %+v", flipr)
	}
	api, err := FetchAPI("http://flipr.test:9800")
	if err != nil {
		t.Fatal(err)
	}
	if len(api.Services) == 0 || api.Services[0].Name != "FliprService" {
		t.Fatalf("descriptor: %+v", api.Services)
	}
	t.Logf("FliprService: %d methods, %d message types, %d bytes",
		len(api.Services[0].Methods), api.Messages, api.Bytes)
}

// TestLiveComposerSchema proves the composer's panel against the real flipr:
// SetFlagRequest arrives with its author's comments and the oneof grouped.
func TestLiveComposerSchema(t *testing.T) {
	api, err := FetchAPI("http://flipr.test:9800")
	if err != nil {
		t.Fatal(err)
	}
	var setFlag *APIMethod
	for _, s := range api.Services {
		for i := range s.Methods {
			if s.Methods[i].Name == "SetFlag" {
				setFlag = &s.Methods[i]
			}
		}
	}
	if setFlag == nil {
		t.Fatal("flipr has no SetFlag?")
	}
	docs := SchemaDoc(api.FDS, setFlag.InputFQN)
	if len(docs) == 0 {
		t.Fatal("no docs from the live descriptor")
	}
	byName := map[string]FieldDoc{}
	for _, d := range docs {
		byName[d.Name] = d
		t.Logf("%s %s — %s", d.Name, d.Type, d.Comment)
	}
	if !strings.Contains(byName["reason"].Comment, "flipping") {
		t.Errorf(
			"the reason field lost its author's words: %q",
			byName["reason"].Comment,
		)
	}
	if len(byName["value"].Nested) == 0 {
		t.Error("Value did not expand")
	}
}

// TestLiveOperator drives the three operator screens' data paths against
// the real mesh: flags from flipr, mounts and a listing and a HEAD from
// kingfisher, pods from kubectl with the explicit local context.
func TestLiveOperator(t *testing.T) {
	rows, err := fetchFlags("http://flipr.test:9800")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("flags=%d", len(rows))

	mounts, err := fetchMounts("http://kingfisher.test:9800")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("mounts=%d %v", len(mounts), mounts)
	if len(mounts) == 0 {
		t.Fatal("no mounts")
	}
	l, err := fetchListing("http://kingfisher.test:9800", mounts[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("listing=%d entries under %s", len(l.Entries), l.Mount)
	for _, e := range l.Entries {
		if !e.Dir {
			a, err := headFile(
				"http://kingfisher.test:9800",
				mounts[0]+e.Name,
			)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf(
				"attrs=%s available=%v size=%s type=%s",
				e.Name,
				a.Available(),
				humanBytes(a.Bytes),
				a.ContentType,
			)
			break
		}
	}
	v := fetchKube("k3d-local")
	if v.Err != "" {
		t.Fatalf("kube: %s", v.Err)
	}
	t.Logf("pods=%d", len(v.Pods))
}
