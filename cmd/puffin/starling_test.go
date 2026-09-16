package main

import (
	"strings"
	"testing"
)

// The two halves of the build seam must agree, under either tag.
//
// This file carries no build tag on purpose, so it runs against both
// `go test ./...` and `go test -tags nostarling ./...`. The invariant is the
// same in both directions and only one of them can be true at a time, which
// is what makes it a usable check rather than a restatement of the constant.
func TestStarlingSeamAgreesWithItself(t *testing.T) {
	built, why := starlingBuiltIn(), starlingWhyNot()
	switch {
	case built && why != "":
		t.Errorf(
			"starling is built in and the seam gives a reason it "+
				"is not: %q",
			why,
		)
	case !built && why == "":
		// A silent absence is the failure this half exists to prevent.
		// An operator seeing a missing feature has to know whether to
		// fix starling or to rebuild puffin, and those are different
		// afternoons.
		t.Error("starling is not built in and the seam gives no reason")
	}
	if !built && !strings.Contains(why, "nostarling") {
		t.Errorf(
			"the reason does not name the build tag that caused "+
				"it: %q",
			why,
		)
	}
}

// `puffin version` is the one place the feature set is stated, because a
// build without starling is a configuration rather than a fault and has no
// business announcing itself on every screen. If it is not here it is
// nowhere, which is why this is asserted rather than assumed.
func TestVersionNamesTheFeatureSet(t *testing.T) {
	out := thisBuild().String()
	if !strings.Contains(out, "features") {
		t.Fatalf("version does not state the feature set:\n%s", out)
	}
	want := "features starling"
	if !starlingBuiltIn() {
		want = "features no starling"
	}
	if !strings.Contains(out, want) {
		t.Errorf("version does not say %q:\n%s", want, out)
	}
}
