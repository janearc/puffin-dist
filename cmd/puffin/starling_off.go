//go:build nostarling

package main

// Starling support, compiled OUT. See starling_on.go for the contract these
// two files share and why the seam exists.
//
// This file must define exactly the symbols starling_on.go defines and
// nothing else, so that `go build -tags nostarling` is a complete build
// rather than a build with a hole in it.

// starlingBuiltIn reports whether this binary can speak to starling.
func starlingBuiltIn() bool { return false }

// starlingWhyNot names the absence for the one screen that reports it.
//
// It says built without rather than unavailable. An operator who sees a
// missing feature needs to know whether to go and fix starling or to go and
// rebuild puffin, and those are different afternoons.
func starlingWhyNot() string {
	return "built without starling (-tags " +
		"nostarling)"
}
