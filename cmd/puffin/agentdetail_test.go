package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// The scratch directory is derived from the transcript's own path, not guessed.
//
// A transcript is ~/.claude/projects/<proj>/<id>.jsonl and the scratchpad is
// /private/tmp/claude-<uid>/<proj>/<id>/scratchpad -- the same two components
// under a different root, so it cannot drift from the session it claims to
// belong to.
func TestScratchDirIsDerivedFromTheTranscript(t *testing.T) {
	got := scratchDir("/src/.claude/projects/-src-puffin/abc-123.jsonl")
	want := filepath.Join(
		scratchRoot(),
		"-src-puffin",
		"abc-123",
		"scratchpad",
	)
	if got != want {
		t.Errorf("scratchDir = %q, want %q", got, want)
	}
	if scratchDir("") != "" {
		t.Error("an empty path produced a directory")
	}
}

// Whether there is any data in it is the part that matters. A scratchpad
// lives under /private/tmp: unbacked, reaped, and invisible
// everywhere else in puffin.
func TestScratchReportsWhatIsThere(t *testing.T) {
	root := t.TempDir()
	proj, id := "-proj", "sess"
	dir := filepath.Join(root, proj, id, "scratchpad")
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"a.txt", "nested/b.txt"} {
		if err := os.WriteFile(
			filepath.Join(dir, f),
			make([]byte, 1024),
			0o644,
		); err != nil {
			t.Fatal(err)
		}
	}
	got := walkScratch(dir)
	if !got.Exists || got.Files != 2 {
		t.Errorf(
			"Files = %d, exists = %v, want 2 and true",
			got.Files,
			got.Exists,
		)
	}
	if got.Bytes != 2048 {
		t.Errorf(
			"Bytes = %d, want 2048 (it must walk nested "+
				"directories)",
			got.Bytes,
		)
	}
	if got.Summary() == "empty" || got.Summary() == "none" {
		t.Errorf(
			"summary %q hides that there is data there",
			got.Summary(),
		)
	}
}

// An absent scratchpad and an empty one are different facts and must read
// differently: "none" is a session that never had one, "empty" is one that
// was cleaned up.
func TestAbsentAndEmptyAreDifferent(t *testing.T) {
	if got := (ScratchInfo{}).Summary(); got != "none" {
		t.Errorf("absent reads %q, want none", got)
	}
	if got := (ScratchInfo{Exists: true}).Summary(); got != "empty" {
		t.Errorf("empty reads %q, want empty", got)
	}
}

// Past the cap the count is a floor and says so. One session on this host
// held 7,417 files; statting all of them before drawing would make the view
// feel broken, and a number quietly rounded off is worse than a bounded one
// that admits it.
func TestOverTheCapReportsAFloor(t *testing.T) {
	s := ScratchInfo{
		Exists:  true,
		Files:   scratchWalkCap,
		Bytes:   1 << 20,
		Sampled: true,
		Newest:  time.Now(),
	}
	if !strings.Contains(s.Summary(), "+") {
		t.Errorf("a bounded walk did not say so: %q", s.Summary())
	}
}

// humanSince already ends in "ago". The first version of the detail view
// appended another and printed "22s ago ago" four times.
func TestDetailDoesNotSayAgoTwice(t *testing.T) {
	p := &agentPane{v: AgentView{Sessions: []AgentSession{{
		ID:      "x",
		Project: "dev/puffin",
		Name:    "n",
		Model:   "claude-opus-5",
		LastWrite: time.Now().
			Add(-time.Minute),
		LastHuman: time.Now().Add(-time.Hour),
		CostKnown: true,
		CostUSD:   1,
		CostAt:    time.Now().Add(-24 * time.Hour),
	}}}}
	p.detail = true
	out := ansi.Strip(p.View(Compile(Corvid()), 162, 44))
	if strings.Contains(out, "ago ago") {
		t.Errorf("the detail view says ago twice:\n%s", out)
	}
}

// The walk must not happen in the render path.
//
// A scratchpad holding a real working set is big enough to slow a render
// or exhaust memory. The 0.17s measurement was one directory on one day and
// bounds nothing.
func TestScratchIsMeasuredOffTheRenderPath(t *testing.T) {
	dir := t.TempDir()
	// a transcript path whose scratchpad does not exist and has never been
	// measured: the view must still draw, and say so
	p := &agentPane{v: AgentView{Sessions: []AgentSession{{
		ID:      "x",
		Project: "dev/puffin",
		Path:    filepath.Join(dir, "proj", "sess.jsonl"),
	}}}}
	p.detail = true
	out := ansi.Strip(p.View(Compile(Corvid()), 162, 44))
	if !strings.Contains(out, "measuring") {
		t.Errorf(
			"an unmeasured scratchpad did not say it was "+
				"measuring:\n%s",
			out,
		)
	}
}

// A command is only issued when there is something to find out.
func TestScratchCmdIsNilWhenKnown(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proj", "sess.jsonl")
	if got := scratchCmd(""); got != nil {
		t.Error("an empty path produced a command")
	}
	// prime the cache the way the message handler does
	d := scratchDir(path)
	scratchCache.Lock()
	scratchCache.at[d] = ScratchInfo{Path: d}
	scratchCache.Unlock()
	t.Cleanup(func() {
		scratchCache.Lock()
		delete(scratchCache.at, d)
		scratchCache.Unlock()
	})
	if got := scratchCmd(path); got != nil {
		t.Error("a measured scratchpad still produced a command")
	}
}
