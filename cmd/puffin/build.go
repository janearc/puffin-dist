package main

import (
	"fmt"
	"runtime/debug"
	"strings"
	"time"
)

// Puffin's own build.
//
// This pane spent the evening telling the estate that nothing can say which
// build is running. Puffin saying it too would have been a bad look, and it
// costs nothing: Go stamps the commit and the build time into every binary
// automatically, with no ldflags and no build system involved.
//
// vcs.modified is reported rather than swallowed. A binary built from a
// dirty tree is not the commit it names, and a tool that reports a commit it
// does not exactly match is the failure mode this whole screen exists to
// catch. Puffin is not exempt from its own rule.

// buildSubject is the commit's subject line, stamped by tools/install. Go
// stamps the revision and the build time for free but not the message: the
// message lives in the repository and the installed binary runs from somewhere
// else.
//
// A plain `go build` leaves this empty, and empty is reported as unstamped
// rather than guessed at.
var buildSubject string

// buildVersion is the release tag, stamped by whatever publishes a release --
// the homebrew formula does it from the tag it was rendered for.
//
// Empty for every build that is not a release, which is most of them and is the
// honest answer: a development build claiming a version number is how you end
// up chasing a bug in a release that never contained the code.
var buildVersion string

// Build is what this binary is.
type Build struct {
	Revision string
	Time     time.Time
	Dirty    bool
	Go       string
	Subject  string
	// Version is the release tag when this binary was built for one, and
	// empty otherwise. Empty is the honest answer for a development
	// build: one claiming a version number is how an afternoon gets spent
	// chasing a bug in a release that never contained the code.
	Version string
}

// thisBuild reads the stamp Go left in the binary.
func thisBuild() Build {
	b := Build{Subject: buildSubject, Version: buildVersion}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return b
	}
	b.Go = info.GoVersion
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			b.Revision = s.Value
		case "vcs.time":
			b.Time, _ = time.Parse(time.RFC3339, s.Value)
		case "vcs.modified":
			b.Dirty = s.Value == "true"
		}
	}
	return b
}

// Short is the commit as a human reads it, with the dirty mark that says the
// tree had uncommitted changes when this was built.
func (b Build) Short() string {
	if b.Revision == "" {
		return "unstamped"
	}
	r := b.Revision
	if len(r) > 7 {
		r = r[:7]
	}
	if b.Dirty {
		return r + "+dirty"
	}
	return r
}

// Line is the one-line form for a footer or a status bar.
func (b Build) Line() string {
	parts := []string{"puffin " + b.Short()}
	if !b.Time.IsZero() {
		parts = append(
			parts,
			"built "+b.Time.Local().Format("2006-01-02 15:04"),
		)
	}
	if b.Go != "" {
		parts = append(parts, b.Go)
	}
	return strings.Join(parts, " · ")
}

// String is the CLI form: what `puffin version` prints.
func (b Build) String() string {
	// the optional features, named. A build without starling is a
	// deliberate configuration rather than a fault, so it is silent
	// everywhere in the interface and stated HERE, once, where somebody
	// asking "why can this binary not do that" will actually look.
	//
	// Reported for an unstamped build too.
	//
	// The feature set is a fact about the compiler invocation and does not
	// depend on vcs information being available -- and a plain `go build`,
	// which is the one that carries no tags because nothing passed any, is
	// exactly the build somebody will be holding when they ask.
	feat := "features starling"
	if !starlingBuiltIn() {
		feat = "features no starling"
	}
	if b.Revision == "" {
		return "puffin (unstamped: built without vcs " +
			"information)\n" + feat
	}
	dirty := ""
	if b.Dirty {
		dirty = "  (built from a modified tree)"
	}
	return fmt.Sprintf(
		"puffin %s\nbuilt   %s\ngo      %s\n%s%s",
		b.Revision,
		b.Time.Local().Format(time.RFC3339),
		b.Go,
		feat,
		dirty,
	)
}
