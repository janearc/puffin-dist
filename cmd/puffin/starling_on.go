//go:build !nostarling

package main

// Starling support, compiled IN. See starling_off.go for the other half.
//
// puffin can watch agents with nothing but a filesystem — it reads Claude
// Code session transcripts off disk. Talking to them needs starling, and
// nobody running this should be forced into a second service to use the
// first one.
//
// So starling is a build-time option and not a runtime one. A runtime flag
// would leave the dependency compiled in, which is most of what "forced"
// means to somebody packaging this. `go build -tags nostarling` produces a
// binary that does not link it at all.
//
// The seam exists before the code it guards, deliberately. Adding the
// feature first and the flag second means retrofitting every call site, and
// a call site that was written without a seam in mind is the one that will
// not have it.

// starlingBuiltIn reports whether this binary can speak to starling.
//
// Every display that would show anything starling-derived asks this first.
//
// It is a build fact, not a health fact: a false here means the feature was
// never compiled in, which is a configuration and NOT a degraded state.
// starling being built in and unreachable is the degraded state, and that one
// is reported loudly wherever it matters — see FliprHeld for the shape.
func starlingBuiltIn() bool { return true }

// starlingWhyNot is the one sentence a screen shows if it has a reason to
// mention the absence at all. Empty when starling is compiled in.
func starlingWhyNot() string { return "" }
