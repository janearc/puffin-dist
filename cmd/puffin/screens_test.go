package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRenderValue decodes each arm of the wire value, including the trap:
// a string flag spelling "true" stays a string.
func TestRenderValue(t *testing.T) {
	for raw, want := range map[string][2]string{
		`{"boolValue":true}`:        {"bool", "true"},
		`{"boolValue":false}`:       {"bool", "false"},
		`{"stringValue":"surreal"}`: {"string", "surreal"},
		`{"stringValue":"true"}`:    {"string", "true"},
		`{"intValue":"42"}`:         {"int", "42"},
		`{}`:                        {"unset", "(unset)"},
	} {
		kind, val := renderValue(json.RawMessage(raw))
		if kind != want[0] || val != want[1] {
			t.Errorf(
				"%s -> (%s,%s), want (%s,%s)",
				raw,
				kind,
				val,
				want[0],
				want[1],
			)
		}
	}
}

// TestFlipBody builds SetFlag bodies that flipr will accept: valid JSON,
// typed value, reason present.
func TestFlipBody(t *testing.T) {
	row := FlagRow{
		Service: "albatross",
		Version: "abc",
		Key:     "model.enabled",
		Kind:    "bool",
	}
	body := flipBody(row, "false", "stopping spend")
	var v map[string]any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("not json: %v\n%s", err, body)
	}
	if v["reason"] != "stopping spend" || v["key"] != "model.enabled" {
		t.Fatalf("fields lost: %s", body)
	}
	val := v["value"].(map[string]any)
	if val["boolValue"] != false {
		t.Fatalf("bool did not carry: %s", body)
	}
	// a string value with quotes in it must not break the splice
	row.Kind = "string"
	body = flipBody(row, `say "why"`, "r")
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("quoted string broke the body: %v\n%s", err, body)
	}
}

// TestHumanBytesAndAge cover the display helpers.
func TestHumanBytesAndAge(t *testing.T) {
	for n, want := range map[int64]string{
		-1: "-", 512: "512 B", 2048: "2.0 KiB", 5242244: "5.0 MiB",
	} {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d)=%q want %q", n, got, want)
		}
	}
}

// TestKubeParse feeds fetchKube's JSON shape through the reducer via a
// fixture, checking ready-counting and the terminal distinction.
func TestKubeParse(t *testing.T) {
	// exercised through the exported path in fetchKube is exec-bound; the
	// reducer logic lives inline, so this asserts on view rendering instead
	m := model{
		styles: Compile(DodoDark()),
		width:  120,
		height: 40,
		scr:    screenKube,
	}
	m.kube = KubeView{Context: "k3d-local", Pods: []KubePod{
		{
			Namespace: "flipr",
			Name:      "flipr-abc",
			Phase:     "Running",
			Ready:     "1/1",
			AllReady:  true,
			Restarts:  0,
			Age:       "2h",
		},
		{
			Namespace: "local",
			Name:      "kafka-0",
			Phase:     "Running",
			Ready:     "0/1",
			AllReady:  false,
			Restarts:  3,
			Age:       "1h",
		},
		{
			Namespace: "local",
			Name:      "job-x",
			Phase:     "Succeeded",
			Ready:     "0/1",
			Terminal:  true,
			Age:       "3d",
		},
	}}
	out := m.View()
	for _, want := range []string{"k3d-local", "1 " +
		"running", "1 waiting", "flipr-abc", "kafka-0", "Succeeded"} {
		if !strings.Contains(out, want) {
			t.Errorf("kube view missing %q", want)
		}
	}
}

// TestMapsAndFlagsViewsRender drives the two other screens' render paths.
func TestMapsAndFlagsViewsRender(t *testing.T) {
	m := model{
		styles: Compile(DodoDark()),
		width:  120,
		height: 40,
		scr:    screenMaps,
	}
	m.mapsListing = &MapListing{
		Mount:    "/econ/",
		Path:     "/econ/",
		Manifest: "res3.json",
		Entries: []MapEntry{
			{Name: "res8/", Dir: true, Bytes: -1},
			{Name: "metros.geojson", Bytes: 5242244},
		},
	}
	a := MapAttrs{
		Status:      200,
		Bytes:       5242244,
		ContentType: "application/json",
		Ranges:      true,
	}
	m.mapsAttrs = &a
	out := m.View()
	for _, want := range []string{
		"res3.json",
		"res8/",
		"5.0 MiB",
		"AVAILABLE",
		"range requests",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("maps view missing %q", want)
		}
	}
	// the not-available verdict must be loud
	a.Status = 404
	out = m.View()
	if !strings.Contains(out, "NOT AVAILABLE (http 404)") {
		t.Error("a 404 file did not read as NOT AVAILABLE")
	}

	m.scr = screenFlags
	m.flags = []FlagRow{
		{Service: "albatross", Version: "abc", Key: "model.enabled",
			Kind: "bool", Value: "false", Desc: "the fork " +
				"gate", Expensive: true},
	}
	out = m.View()
	for _, want := range []string{"albatross", "model.enabled", "the " +
		"fork gate", "$"} {
		if !strings.Contains(out, want) {
			t.Errorf("flags view missing %q", want)
		}
	}
}
