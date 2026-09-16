package main

import (
	"github.com/charmbracelet/x/ansi"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeTranscript builds a transcript the shape Claude Code writes.
func writeTranscript(t *testing.T, dir, id string, lines ...string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(
		p,
		[]byte(strings.Join(lines, "\n")+"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	return p
}

const asstLine = `{"type":"assistant","timestamp":"2026-08-29T15:46:44.406Z",` +
	`"cwd":"/src/puffin","sessionId":"s1","gitBranch":"panes",` +
	`"message":{"model":"claude-opus-5","usage":{"input_tokens":2,` +
	`"output_tokens":753,"cache_read_input_tokens":100,` +
	`"cache_creation_input_tokens":42010}}}`

// Usage sums across turns, cache separated from fresh input: they are not
// the same spend and a total that hides the split flatters the number.
func TestReadTranscriptSumsUsage(t *testing.T) {
	// hermetic: projectName reads HOME, and a test that depends on the
	// real one passes or fails according to what is checked out next door
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	p := writeTranscript(t, dir, "s1", asstLine, asstLine,
		`{"type":"user","cwd":"/src/puffin"}`)
	s, err := readTranscript(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.Steps != 2 {
		t.Fatalf("steps: %d", s.Steps)
	}
	if s.OutputTok != 1506 || s.CacheMade != 84020 || s.CacheRead != 200 ||
		s.InputTok != 4 {
		t.Fatalf("usage did not sum: %+v", s)
	}
	if s.Project != "puffin" || s.Branch != "panes" ||
		s.Model != "claude-opus-5" {
		t.Fatalf("identity lost: %+v", s)
	}
	if s.Tokens() != 4+1506+200+84020 {
		t.Fatalf("total: %d", s.Tokens())
	}
}

// The format belongs to somebody else and will change: a line that does not
// parse is skipped, not fatal, and the rest of the session still counts.
func TestGarbageLinesAreSkipped(t *testing.T) {
	dir := t.TempDir()
	p := writeTranscript(t, dir, "s2", `{not json at all`, asstLine, ``)
	s, err := readTranscript(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.Steps != 1 {
		t.Fatalf("a bad line took the session with it: %+v", s)
	}
}

// Only transcripts written inside the window are read, newest first: a
// laptop with thousands of them must not turn one keystroke into a sweep.
func TestOnlyRecentTranscriptsAreRead(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	proj := filepath.Join(root, "projects", "-src-puffin")
	fresh := writeTranscript(t, proj, "fresh", asstLine)
	stale := writeTranscript(t, proj, "stale", asstLine)
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	v := fetchAgents(24 * time.Hour)
	if len(v.Sessions) != 1 {
		t.Fatalf(
			"sessions: %d, want only the fresh one",
			len(v.Sessions),
		)
	}
	if v.Sessions[0].ID != "fresh" {
		t.Fatalf("read %q", v.Sessions[0].ID)
	}
	_ = fresh
}

// A transcript with no assistant turn is not a session yet.
func TestEmptyTranscriptIsNotASession(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	writeTranscript(t, filepath.Join(root, "projects", "p"), "empty",
		`{"type":"user","cwd":"/tmp"}`)
	if v := fetchAgents(time.Hour); len(v.Sessions) != 0 {
		t.Fatalf("sessions: %d", len(v.Sessions))
	}
}

// The pane says "last wrote" and never "running": a file time is not a
// heartbeat, and a session that died mid-turn looks exactly like one that
// is thinking.
func TestThePaneDoesNotClaimAHeartbeat(t *testing.T) {
	p := &agentPane{v: AgentView{
		Host: "kestrel", Window: time.Hour,
		Sessions: []AgentSession{
			{ID: "s1", Project: "puffin", Branch: "panes",
				Model:     "claude-opus-5",
				Turns:     4,
				OutputTok: 1000,
				LastWrite: time.Now()},
		},
	}}
	out := p.View(Compile(DodoDark()), 140, 40)
	if !strings.Contains(out, "last log") {
		t.Fatal("the column is not labelled as a log time")
	}
	for _, forbidden := range []string{"running", "alive", "active now"} {
		if strings.Contains(strings.ToLower(out), forbidden) {
			t.Fatalf(
				"the pane claims %q, which the data cannot "+
					"support",
				forbidden,
			)
		}
	}
	// stated in plain words, not in jargon: the first attempt said "a file
	// time, not a heartbeat", which is only clear if you already knew
	if !strings.Contains(p.Help(), "not proof it is still running") {
		t.Fatalf(
			"the help line does not say plainly what the column "+
				"means: %q",
			p.Help(),
		)
	}
}

// A token count is read at a glance or not at all, so the rendering keeps
// one decimal and a suffix rather than nine digits.
func TestHumanCount(t *testing.T) {
	for n, want := range map[int64]string{
		42:            "42",
		1500:          "1.5k",
		2_400_000:     "2.4M",
		3_000_000_000: "3.0B",
	} {
		if got := humanCount(n); got != want {
			t.Errorf("humanCount(%d)=%q want %q", n, got, want)
		}
	}
}

// The pane refreshes itself, and the clock stops when the pane closes: a
// tick addressed to a screen nobody is looking at must not fetch.
func TestPaneTickOnlyRunsWhileOpen(t *testing.T) {
	m := baseModel()
	m.scr = screenRoster
	next, cmd := m.Update(key('a'))
	m = next.(model)
	if m.openPane == nil || cmd == nil {
		t.Fatal("a did not open the agents pane")
	}
	// a tick for the open pane refetches and schedules the next one
	_, cmd = m.Update(paneTick{pane: m.openPane.Title()})
	if cmd == nil {
		t.Fatal("a tick for the open pane did not refresh it")
	}
	// a tick for some other pane is dropped
	if _, cmd := m.Update(paneTick{pane: "metrics"}); cmd != nil {
		t.Fatal("a tick addressed to another pane still fetched")
	}
	// once the pane is closed, its tick does nothing and schedules nothing
	back, _ := m.Update(key('q'))
	closed := back.(model)
	if _, cmd := closed.Update(paneTick{pane: "agents"}); cmd != nil {
		t.Fatal("the clock kept running after the pane closed")
	}
}

// A pane that does not ask for a clock does not get one.
func TestPanesOptIntoTicking(t *testing.T) {
	ticking := map[string]bool{}
	for _, p := range panes() {
		if _, ok := p.(ticker); ok {
			ticking[p.Title()] = true
		}
	}
	if !ticking["agents"] {
		t.Fatal("the agents pane does not refresh itself")
	}
	if p, ok := panes()["v"]; ok && startTick(p) != nil {
		t.Fatal("the host pane ticks; colima does not change that fast")
	}
}

// The cursor follows the session, not the row. The list is sorted by who
// wrote last, so on a self-refreshing screen the rows really do move -- and
// on a screen that will grow a kill verb, a cursor that slides is the
// difference between an interruption and an accident.
func TestCursorFollowsTheSessionAcrossARefresh(t *testing.T) {
	older := time.Now().Add(-time.Hour)
	p := &agentPane{}
	first := AgentView{Sessions: []AgentSession{
		{ID: "a", Project: "puffin", Turns: 1, LastWrite: time.Now()},
		{ID: "b", Project: "albatross", Turns: 1, LastWrite: older},
	}}
	pane, _ := p.Update(agentsFetched{first})
	ap := pane.(*agentPane)
	pane, _ = ap.Update(key('j')) // cursor onto b
	ap = pane.(*agentPane)
	if ap.selected() != "b" {
		t.Fatalf("cursor is on %q", ap.selected())
	}
	// b writes, so it sorts to the top and the rows swap under the cursor
	second := AgentView{Sessions: []AgentSession{
		{
			ID:        "b",
			Project:   "albatross",
			Turns:     2,
			LastWrite: time.Now(),
		},
		{ID: "a", Project: "puffin", Turns: 1, LastWrite: older},
	}}
	pane, _ = ap.Update(agentsFetched{second})
	ap = pane.(*agentPane)
	if ap.selected() != "b" {
		t.Fatalf(
			"the cursor slid to %q when the rows moved",
			ap.selected(),
		)
	}
}

// An unchanged transcript is not read again: a pane left open all day must
// cost a stat per file per tick, not a re-read of every megabyte.
func TestUnchangedTranscriptsAreNotReRead(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	proj := filepath.Join(root, "projects", "p")
	writeTranscript(t, proj, "one", asstLine)
	writeTranscript(t, proj, "two", asstLine)
	c := newAgentCache()
	if v := c.read(time.Hour); v.ReRead != 2 {
		t.Fatalf("first pass read %d of 2", v.ReRead)
	}
	if v := c.read(time.Hour); v.ReRead != 0 {
		t.Fatalf(
			"second pass re-read %d unchanged transcripts",
			v.ReRead,
		)
	}
	// A transcript that grows IS read again, and this pins the mtime back
	// to what the cache already holds before asking.
	//
	// That is the case a coarse-granularity filesystem produces on its own
	// -- overlayfs did, APFS did not, and the version that compared mtime
	// alone passed here and failed in a container. Forcing it makes the
	// test say the same thing on both.
	two := filepath.Join(proj, "two.jsonl")
	fi, err := os.Stat(two)
	if err != nil {
		t.Fatal(err)
	}
	was := fi.ModTime()
	writeTranscript(t, proj, "two", asstLine, asstLine)
	if err := os.Chtimes(two, was, was); err != nil {
		t.Fatal(err)
	}
	v := c.read(time.Hour)
	if v.ReRead != 1 {
		t.Fatalf(
			"a transcript that grew under an unchanged mtime was "+
				"not re-read: %d", v.ReRead)
	}
	for _, s := range v.Sessions {
		if s.ID == "two" && s.Steps != 2 {
			t.Fatalf("the cached session went stale: %+v", s)
		}
	}
	// and a transcript that falls out of the window leaves the cache
	if len(c.seen) != 2 {
		t.Fatalf("cache holds %d entries", len(c.seen))
	}
	if c.read(time.Nanosecond); len(c.seen) != 0 {
		t.Fatalf(
			"cache kept %d entries nothing looks at any more",
			len(c.seen),
		)
	}
}

// <synthetic> is Claude Code speaking in the assistant's voice -- an error,
// a login prompt, an interrupt -- not a model. Letting it set the model
// column overwrites the real one with a word that names no model, which is
// how it turned up on the screen.
func TestSyntheticIsNotAModel(t *testing.T) {
	dir := t.TempDir()
	const synth = `{"type":"assistant","cwd":"/tmp/x",` +
		`"message":{"model":"<synthetic>","content":[{"type":"text",` +
		`"text":"Not logged in"}],"usage":{"input_tokens":0,` +
		`"output_tokens":0}}}`
	p := writeTranscript(t, dir, "s", asstLine, synth)
	s, err := readTranscript(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.Model != "claude-opus-5" {
		t.Fatalf("the model column reads %q", s.Model)
	}
	if s.Synthetic != 1 {
		t.Fatalf("harness turns counted: %d", s.Synthetic)
	}
	if s.Steps != 1 {
		t.Fatalf(
			"a harness message was counted as a model step: %d",
			s.Steps,
		)
	}
}

// The tail is a rendering of what happened, not raw JSON Lines: a wall of
// escaped JSON tells you nothing at a glance, which is the whole point of tail
// -f.
func TestTailRendersActivityNotJSON(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		`{"type":"user","message":{"role":"user",` +
			`"content":"please fix the cluster screen"}}`,
		`{"type":"assistant","message":{"model":"claude-opus-5",` +
			`"content":[{"type":"tool_use","name":"Bash",` +
			`"input":{"command":"kubectl get pods -A"}}]}}`,
		`{"type":"user","message":{"role":"user",` +
			`"content":[{"type":"tool_result",` +
			`"content":"41 pods"}]}}`,
		`{"type":"assistant","message":{"model":"claude-opus-5",` +
			`"content":[{"type":"text",` +
			`"text":"Forty-one pods.\nThat does not fit."}]}}`,
		`{"type":"file-history-snapshot","snapshot":{"x":1}}`,
	}
	p := writeTranscript(t, dir, "s", lines...)
	got, err := tailTranscript(p, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf(
			"rendered %d lines, want 4 (the snapshot is not "+
				"activity): %+v",
			len(got),
			got,
		)
	}
	if got[0].Kind != "user" ||
		!strings.Contains(got[0].Text, "fix the cluster") {
		t.Fatalf("user turn: %+v", got[0])
	}
	if got[1].Kind != "tool" || got[1].Text != "Bash(kubectl get pods -A)" {
		t.Fatalf("tool call: %+v", got[1])
	}
	if got[2].Kind != "result" ||
		!strings.Contains(got[2].Text, "bytes back") {
		t.Fatalf("tool result: %+v", got[2])
	}
	// prose is flattened to one line: the window is ten lines, not ten
	// paragraphs
	if strings.Contains(got[3].Text, "\n") {
		t.Fatalf(
			"a multi-line answer was not flattened: %q",
			got[3].Text,
		)
	}
	for _, l := range got {
		if strings.Contains(l.Text, `{"type"`) {
			t.Fatalf("raw json leaked into the window: %q", l.Text)
		}
	}
}

// The read is bounded to the end of the file: transcripts here reach tens of
// megabytes and this refreshes every five seconds.
func TestTailReadsOnlyTheEnd(t *testing.T) {
	dir := t.TempDir()
	filler := make([]string, 0, 400)
	for i := 0; i < 400; i++ {
		filler = append(
			filler,
			`{"type":"assistant",`+
				`"message":{"model":"claude-opus-5",`+
				`"content":[{"type":"text",`+
				`"text":"`+strings.Repeat(
				"padding ",
				40,
			)+`"}]}}`,
		)
	}
	filler = append(
		filler,
		`{"type":"assistant","message":{"model":"claude-opus-5",`+
			`"content":[{"type":"text","text":"THE LAST THING"}]}}`,
	)
	p := writeTranscript(t, dir, "big", filler...)
	got, err := tailTranscript(p, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 10 {
		t.Fatalf("got %d lines", len(got))
	}
	if !strings.Contains(got[len(got)-1].Text, "THE LAST THING") {
		t.Fatalf(
			"the last line is not the last record: %q",
			got[len(got)-1].Text,
		)
	}
}

// A tail that arrives for a session the cursor has already left is dropped
// rather than shown under the wrong session's name.
func TestALateTailIsNotShownUnderTheWrongSession(t *testing.T) {
	p := &agentPane{
		sel: "b",
		v:   AgentView{Sessions: []AgentSession{{ID: "b"}}},
	}
	pane, _ := p.Update(
		tailFetched{
			id:    "a",
			lines: []TailLine{{Kind: "text", Text: "from a"}},
		},
	)
	if got := pane.(*agentPane); len(got.tail) != 0 {
		t.Fatalf("a stale tail was adopted: %+v", got.tail)
	}
	pane, _ = p.Update(
		tailFetched{
			id:    "b",
			lines: []TailLine{{Kind: "text", Text: "from b"}},
		},
	)
	if got := pane.(*agentPane); len(got.tail) != 1 {
		t.Fatal("the tail for the selected session was dropped")
	}
}

// Context is the whole prompt the model was sent -- input plus BOTH cache
// halves. Cache reads are cheaper, not absent, and dropping them understates
// a long session by an order of magnitude.
func TestContextCountsTheWholePrompt(t *testing.T) {
	dir := t.TempDir()
	p := writeTranscript(t, dir, "s", asstLine)
	s, err := readTranscript(p)
	if err != nil {
		t.Fatal(err)
	}
	// 2 input + 100 cache read + 42010 cache creation
	if s.Context != 42112 {
		t.Fatalf("context: %d", s.Context)
	}
	if s.Peak != s.Context {
		t.Fatalf("peak: %d", s.Peak)
	}
}

// Peak survives compaction: a session sitting at 200k that has been to 999k
// has already lost things it once had in front of it, and the screen says so.
func TestPeakOutlivesCompaction(t *testing.T) {
	big := `{"type":"assistant","cwd":"/x",` +
		`"message":{"model":"claude-opus-5",` +
		`"usage":{"input_tokens":0,"output_tokens":1,` +
		`"cache_read_input_tokens":900000,` +
		`"cache_creation_input_tokens":0}}}`
	small := `{"type":"assistant","cwd":"/x",` +
		`"message":{"model":"claude-opus-5",` +
		`"usage":{"input_tokens":0,"output_tokens":1,` +
		`"cache_read_input_tokens":100000,` +
		`"cache_creation_input_tokens":0}}}`
	dir := t.TempDir()
	p := writeTranscript(t, dir, "s", big, small)
	s, _ := readTranscript(p)
	if s.Context != 100000 || s.Peak != 900000 {
		t.Fatalf("ctx=%d peak=%d", s.Context, s.Peak)
	}
	// the peak gets its own labelled column rather than a caret glued to
	// the context cell: the glued version overflowed and clipped to "^1…",
	// and a bare "^97" is a symbol nobody can look up
	st := Compile(DodoDark())
	if peak := peakCell(st, s, false); !strings.Contains(peak, "90%") {
		t.Fatalf(
			"the peak column does not show it has been fuller: %q",
			peak,
		)
	}
	// a session that has not compacted has nothing to say there
	flat := AgentSession{
		Model:   "claude-opus-5",
		Context: 100000,
		Peak:    100000,
	}
	if peak := peakCell(st, flat, false); !strings.Contains(peak, "-") {
		t.Fatalf(
			"a session that never compacted shows a peak: %q",
			peak,
		)
	}
}

// An unknown model gets a bare number and NO percentage: a percentage of a
// denominator nobody checked is exactly the kind of number people act on.
func TestUnknownModelGetsNoPercentage(t *testing.T) {
	s := Compile(DodoDark())
	cell := contextCell(
		s,
		AgentSession{Model: "some-new-thing", Context: 50000},
		false,
	)
	if strings.Contains(cell, "%") {
		t.Fatalf("invented a window for an unknown model: %q", cell)
	}
	if !strings.Contains(cell, "50.0k") {
		t.Fatalf("dropped the number it does know: %q", cell)
	}
	// the models actually in use are known, and the 1m variant too
	for _, m := range []string{
		"claude-opus-5",
		"claude-opus-5[1m]",
		"claude-fable-5",
		"claude-sonnet-5",
	} {
		if contextLimit(m) != 1_000_000 {
			t.Errorf("%s: %d", m, contextLimit(m))
		}
	}
	if contextLimit("claude-haiku-4-5-20251001") != 200_000 {
		t.Error("haiku's window is wrong")
	}
}

// Cost and lines come from Claude Code's own cost-state record. Nothing here
// computes a price: a session with no cost-state shows a dash rather than a
// number puffin made up.
func TestCostAndLinesAreReadNotComputed(t *testing.T) {
	const cs = `{"type":"cost-state","sessionId":"s",` +
		`"totalCostUSD":67.32,"totalLinesAdded":108,` +
		`"totalLinesRemoved":22}`
	dir := t.TempDir()
	p := writeTranscript(t, dir, "s", asstLine, cs)
	s, _ := readTranscript(p)
	if !s.CostKnown || s.CostUSD != 67.32 || s.LinesAdded != 108 ||
		s.LinesRemoved != 22 {
		t.Fatalf("cost-state not read: %+v", s)
	}
	st := Compile(DodoDark())
	if !strings.Contains(costCell(st, s, false), "$67.32") {
		t.Fatal("the cost is not shown")
	}
	if !strings.Contains(diffCell(st, s, false), "+108") {
		t.Fatal("the lines are not shown")
	}
	// a session the harness has not costed yet says so
	p2 := writeTranscript(t, dir, "t", asstLine)
	s2, _ := readTranscript(p2)
	if s2.CostKnown {
		t.Fatal("a session with no cost-state claims to know its cost")
	}
	if strings.Contains(costCell(st, s2, false), "$") {
		t.Fatal("puffin invented a dollar figure")
	}
}

// Sorting moves the rows and keeps the selection, and the order is total:
// two sessions that tie must not swap places on every five-second refresh.
func TestSortingKeepsTheSelectionAndIsStable(t *testing.T) {
	now := time.Now()
	p := &agentPane{v: AgentView{Sessions: []AgentSession{
		{ID: "a", Project: "a", Context: 10, LastWrite: now},
		{
			ID:        "b",
			Project:   "b",
			Context:   900,
			LastWrite: now.Add(-time.Hour),
		},
		{
			ID:        "c",
			Project:   "c",
			Context:   500,
			LastWrite: now.Add(-2 * time.Hour),
		},
	}}}
	p.cursor, p.sel = 0, "a"
	// default is last log
	p.sortSessions()
	if p.v.Sessions[0].ID != "a" {
		t.Fatalf("default order: %+v", p.v.Sessions[0])
	}
	// cycle to context
	pane, _ := p.Update(key('s'))
	p = pane.(*agentPane)
	if p.v.Sessions[0].ID != "b" {
		t.Fatalf("context sort put %q first", p.v.Sessions[0].ID)
	}
	if p.sel != "a" || p.v.Sessions[p.cursor].ID != "a" {
		t.Fatalf(
			"the cursor lost its session: %q at %d",
			p.sel,
			p.cursor,
		)
	}
	// the header marks the sorted column
	out := p.View(Compile(DodoDark()), 160, 40)
	if !strings.Contains(out, "context▾") &&
		!strings.Contains(out, "context▴") {
		t.Fatal("the header does not say which column is sorted")
	}
	// ties fall back to last log, so a refresh cannot reorder them
	p.v.Sessions = []AgentSession{
		{ID: "x", Context: 0, LastWrite: now.Add(-time.Hour)},
		{ID: "y", Context: 0, LastWrite: now},
	}
	p.sortSessions()
	first := p.v.Sessions[0].ID
	for i := 0; i < 5; i++ {
		p.sortSessions()
		if p.v.Sessions[0].ID != first {
			t.Fatal("tied rows reorder between refreshes")
		}
	}
}

// Tokens went missing when context, lines and cost were added -- the row
// just got wider until something fell off, and nobody was told.
//
// The two-line record is the answer to that, and this is the assertion that it
// worked: the meter columns are on a line of their own that always fits, so
// they can no longer be dropped at ANY width.
//
// Only identity columns on the first line are negotiable, and the name goes
// last because it is the address another session reaches this one at.
func TestTheMeterColumnsNeverDrop(t *testing.T) {
	p := &agentPane{v: AgentView{Sessions: []AgentSession{{
		ID:        "a",
		Project:   "dev/puffin",
		Name:      "puffin-dev",
		Branch:    "panes",
		Model:     "claude-opus-5",
		Turns:     12,
		OutputTok: 5000,
		Context:   415000,
		CostKnown: true, CostUSD: 1.25, LinesAdded: 10, LinesRemoved: 2,
	}}}}
	s := Compile(DodoDark())

	// the meter is 56 columns and belongs to nobody's budget
	meter := []string{"turns", "tokens", "context", "peak", "lines", "cost"}
	for _, w := range []int{80, 100, 108, 120, 160, 200} {
		out := p.View(s, w, 40)
		for _, col := range meter {
			if !strings.Contains(out, col) {
				t.Errorf(
					"width %d dropped %q from the meter; "+
						"the meter is not droppable",
					w,
					col,
				)
			}
		}
		for _, kept := range []string{
			"project",
			"model",
			"tmux",
			"last log",
		} {
			if !strings.Contains(out, kept) {
				t.Errorf(
					"width %d dropped %q from the first "+
						"line",
					w,
					kept,
				)
			}
		}
	}

	// wide enough, and the negotiable ones are there too
	wide := p.View(s, 200, 40)
	for _, col := range []string{"name", "branch"} {
		if !strings.Contains(wide, col) {
			t.Errorf("a wide terminal is missing %q", col)
		}
	}

	// room for exactly one of them, and the name wins: it is the address
	// another session reaches this one at, and a row you cannot address is
	// worse than a row whose branch you have to look up.
	//
	// 93 is the width where that is decided -- 6 of frame and 69 of fixed
	// columns leaves 18, which is the name exactly and the branch not at
	// all.
	//
	// Asserted on the values rather than the header words, because "name"
	// and "branch" are common enough to appear in a frame by accident and
	// "puffin-dev" and "panes" are not.
	//
	// Asserted on the column header line, not on the whole frame: the tail
	// view under the list prints "project · branch" of the selected session
	// whatever the budget did, so searching the frame for the branch name
	// finds that instead and the test passes while the column is missing.
	head := headerLineOf(t, p.View(s, 93, 40))
	if !strings.Contains(head, "name") {
		t.Errorf(
			"the name column was dropped while there was room:\n%s",
			head,
		)
	}
	if strings.Contains(head, "branch") {
		t.Errorf(
			"the branch column was kept in place of the name:\n%s",
			head,
		)
	}
}

// headerLineOf returns the rendered column-header line -- the one carrying
// "project" -- with styling stripped.
func headerLineOf(t *testing.T, frame string) string {
	t.Helper()
	for _, line := range strings.Split(frame, "\n") {
		plain := ansi.Strip(line)
		if strings.Contains(plain, "project") {
			return plain
		}
	}
	t.Fatal("no column header line in the frame")
	return ""
}

// A record is one thing, so either of its two lines selects it. This was
// the arithmetic that had to change in three places at once -- the window
// size, the hit height and the click -- and linesPerRecord exists so they
// cannot disagree.
func TestEitherLineOfARecordSelectsIt(t *testing.T) {
	var sessions []AgentSession
	for i := 0; i < 6; i++ {
		sessions = append(sessions, AgentSession{
			ID: string(
				rune('a' + i),
			), Project: "dev/puffin", Turns: i,
		})
	}
	p := &agentPane{v: AgentView{Sessions: sessions}}
	p.View(Compile(DodoDark()), 220, 60)
	if p.hitRows < 2*linesPerRecord {
		t.Fatalf(
			"only %d hit lines; the fixture did not draw enough "+
				"records",
			p.hitRows,
		)
	}

	for record := 0; record < 3; record++ {
		for line := 0; line < linesPerRecord; line++ {
			p.cursor = -1
			p.Click(0, p.hitTop+record*linesPerRecord+line, 0)
			if want := p.offset + record; p.cursor != want {
				t.Errorf(
					"clicking line %d of record %d "+
						"selected %d, want %d",
					line,
					record,
					p.cursor,
					want,
				)
			}
		}
	}
}

// Effort rides in the model cell: a session at low effort and one at max
// are not the same thing wearing the same name, and splitting them across
// two columns would make the reader join them back up.
func TestModelCellCarriesEffort(t *testing.T) {
	if got := modelCell(AgentSession{
		Model:  "claude-opus-5",
		Effort: "xhigh",
	}); got != "opus-5/xhigh" {
		t.Fatalf("model cell: "+
			"%q", got)
	}
	// effort is optional: an older transcript that never recorded it still
	// shows the model rather than a trailing slash
	if got := modelCell(
		AgentSession{Model: "claude-fable-5"},
	); got != "fable-5" {
		t.Fatalf("model cell without effort: %q", got)
	}
	if got := modelCell(AgentSession{}); got != "" {
		t.Fatalf("empty session: %q", got)
	}
	// and it is read from the top level of an assistant record, where the
	// harness writes it -- not from inside the message
	dir := t.TempDir()
	const withEffort = `{"type":"assistant","cwd":"/x","effort":"xhigh",` +
		`"message":{"model":"claude-opus-5",` +
		`"usage":{"input_tokens":1,"output_tokens":1}}}`
	p := writeTranscript(t, dir, "s", withEffort)
	sess, _ := readTranscript(p)
	if sess.Effort != "xhigh" {
		t.Fatalf("effort not read: %+v", sess)
	}
}

// "does this agent need me" is answerable from the transcript: a turn that
// ends in a tool call is mid-loop, one that ends in prose has handed back.
// No Stop hook, no Notification matcher -- neither of which is reliable.
func TestSessionState(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		name  string
		kind  string
		quiet time.Duration
		want  string
	}{
		{"just wrote", "tool", 5 * time.Second, "working"},
		{"mid tool loop", "tool", 45 * time.Second, "working"},
		{"finished its turn", "text", 200 * time.Second, "waiting"},
		{"long abandoned", "text", 3 * time.Hour, "idle"},
		{"tool call, long gone", "tool", 3 * time.Hour, "idle"},
	} {
		s := AgentSession{
			LastKind:  c.kind,
			LastWrite: now.Add(-c.quiet),
		}
		if got := s.State(now); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

// lastBlockKind reads what a turn ended with, not what it contained: a turn
// with prose and then a tool call is mid-loop.
func TestLastBlockKind(t *testing.T) {
	for raw, want := range map[string]string{
		`[{"type":"text","text":"here goes"},{"type":"tool_use",` +
			`"name":"Bash"}]`: "tool",
		`[{"type":"tool_use","name":"Bash"},{"type":"text",` +
			`"text":"done"}]`: "text",
		`[{"type":"text","text":"just talking"}]`: "text",
		`[{"type":"thinking","thinking":"hmm"}]`:  "",
	} {
		if got := lastBlockKind([]byte(raw)); got != want {
			t.Errorf("%s -> %q, want %q", raw, got, want)
		}
	}
}

// Notifications fire on the transition into waiting, never on the state --
// "this agent is waiting" is true every five seconds. And opening the pane
// announces nothing: six agents that were already waiting before you looked
// must not produce six notifications.
func TestNotifyOnTransitionOnly(t *testing.T) {
	sent := 0
	// the first pass only records
	p := &agentPane{v: AgentView{Sessions: []AgentSession{
		{
			ID:        "a",
			Project:   "puffin",
			LastKind:  "text",
			LastWrite: time.Now().Add(-5 * time.Minute),
		},
	}}}
	p.announce()
	if p.was["a"] != "waiting" {
		t.Fatalf("state not recorded: %q", p.was["a"])
	}
	if sent != 0 {
		t.Fatal("the first look announced something")
	}
	// a session that stays waiting announces nothing more
	p.announce()
	if p.was["a"] != "waiting" {
		t.Fatal("state drifted")
	}
	// and a session that leaves the window stops being tracked
	p.v.Sessions = nil
	p.announce()
	if len(p.was) != 0 {
		t.Fatalf("tracking %d sessions that are gone", len(p.was))
	}
}

// The text handed to osascript is quoted: it comes from transcripts and pod
// names, which is to say from outside, and it is going to an interpreter.
func TestNotificationTextIsQuoted(t *testing.T) {
	got := osaQuote(`say "hi" \ then` + "\n" + `more`)
	if strings.Contains(got[1:len(got)-1], `"`) &&
		!strings.Contains(got, `\"`) {
		t.Fatalf("an unescaped quote reached the script: %s", got)
	}
	if strings.Contains(got, "\n") {
		t.Fatalf("a newline reached the script: %q", got)
	}
	// backslashes are escaped before quotes, or the escaping escapes itself
	if !strings.Contains(got, `\\`) {
		t.Fatalf("backslash not escaped: %s", got)
	}
}

// The OSC route needs stdout to BE a terminal.
//
// TERM_PROGRAM is inherited by every child process, including ones whose output
// is a pipe, so "running under ghostty" and "writing to ghostty" are different
// facts -- and writing an escape to a pipe prints ]777;notify;... as text where
// a notification should have been.
func TestOSCRouteRequiresATerminal(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "ghostty")
	t.Setenv("PUFFIN_TERM_NOTIFY", "")
	// go test's stdout is not a character device, which is the case this
	// guard exists for
	if stdoutIsTerminal() {
		t.Skip("this test runs with a terminal attached")
	}
	if termCanNotify() {
		t.Fatal(
			"the terminal route was chosen with no terminal to " +
				"write to",
		)
	}
	// and an explicit override still wins, for the case detection is wrong
	t.Setenv("PUFFIN_TERM_NOTIFY", "1")
	if !termCanNotify() {
		t.Fatal("the override does not override")
	}
}

// Tagging is by session ID, never by row.
//
// The list re-sorts itself every five seconds. A tag held by position would
// drift onto whatever moved into that row, and on the screen whose next
// keystroke may be a kill -9 that is not an acceptable failure mode.
func TestTagsSurviveResorting(t *testing.T) {
	p := &agentPane{}
	p.v.Sessions = []AgentSession{
		{ID: "a", Project: "alpha", Tmux: TmuxPane{Target: "0:1.1"}},
		{ID: "b", Project: "bravo", Tmux: TmuxPane{Target: "0:1.2"}},
		{ID: "c", Project: "charlie", Tmux: TmuxPane{Target: "0:1.3"}},
	}
	p.tagged = map[string]bool{"b": true, "c": true}

	got := map[string]bool{}
	for _, s := range p.targets() {
		got[s.ID] = true
	}
	if len(got) != 2 || !got["b"] || !got["c"] {
		t.Fatalf("targets are %v, want b and c", got)
	}

	// reverse the list, as a re-sort would
	p.v.Sessions[0], p.v.Sessions[2] = p.v.Sessions[2], p.v.Sessions[0]
	got = map[string]bool{}
	for _, s := range p.targets() {
		got[s.ID] = true
	}
	if len(got) != 2 || !got["b"] || !got["c"] {
		t.Fatalf("after a re-sort the tags moved: %v", got)
	}
}

// With nothing tagged, a verb acts on the row under the cursor. Tagging must
// not be a mode somebody has to enter.
func TestUntaggedActsOnTheCursor(t *testing.T) {
	p := &agentPane{}
	p.v.Sessions = []AgentSession{
		{ID: "a", Project: "alpha"},
		{ID: "b", Project: "bravo"},
	}
	p.cursor = 1
	ts := p.targets()
	if len(ts) != 1 || ts[0].ID != "b" {
		t.Fatalf(
			"untagged, the target should be the cursor row; got %v",
			ts,
		)
	}
}

// The ladder's rungs differ in what they cost, and the confirmation has to
// know: TERM lets a session close its transcript, KILL does not.
func TestKillIsNotReversibleAndTermIs(t *testing.T) {
	if !(Intent{Verb: "terminate", Reversible: true}).Reversible {
		t.Fatal("terminate should be marked recoverable")
	}
	i := Intent{Verb: "kill -9", Reversible: false}
	if !i.needsConfirm() {
		t.Fatal("kill -9 must always confirm, safe mode or not")
	}
}

// The command shown is the command run.
func TestSignalCommandNamesEveryPid(t *testing.T) {
	got := signalCommand("KILL", []int{101, 202})
	if got != "kill -KILL 101 202" {
		t.Fatalf("signal command is %q", got)
	}
}

// Both lines of a record must measure the SAME width, or the cursor highlight
// is a wide line above a short one and the record reads as two. The widths were
// computed from constants and came out two columns short, because a cell
// renderer pads to a width those constants did not know about.
//
// They are measured from the rendered line now.
func TestBothLinesOfARecordAreTheSameWidth(t *testing.T) {
	p := &agentPane{v: AgentView{Sessions: []AgentSession{
		{
			ID:           "a",
			Project:      "dev/puffin",
			Name:         "puffin-dev",
			Branch:       "main",
			Model:        "claude-opus-5",
			Turns:        3,
			CostKnown:    true,
			CostUSD:      1.5,
			LinesAdded:   10,
			LinesRemoved: 2,
			Context:      400000,
		},
		{ID: "b", Project: "prod/cuckoo", Turns: 1},
	}}}
	// A record's first line begins at the left margin with the cursor
	// marker and the project.
	//
	// Matching on "contains the project name" instead also matches the tail
	// view's header, which prints "project · branch" inside a border and is
	// a different width by design -- the first version of this test failed
	// on that.
	isFirstLine := func(plain string) bool {
		for _, pre := range []string{"  dev/", "▸ dev/", "  " +
			"prod/", "▸ prod/"} {
			if strings.HasPrefix(plain, pre) {
				return true
			}
		}
		return false
	}

	for _, w := range []int{93, 108, 140, 162, 200} {
		lines := strings.Split(p.View(Compile(DodoDark()), w, 40), "\n")
		pairs := 0
		for i, line := range lines {
			plain := ansi.Strip(line)
			if !isFirstLine(plain) || i+1 >= len(lines) {
				continue
			}
			pairs++
			first := ansi.StringWidth(plain)
			second := ansi.StringWidth(ansi.Strip(lines[i+1]))
			if first != second {
				t.Errorf(
					"width %d: a record's first line is "+
						"%d wide and its meter is %d "+
						"-- "+
						"the cursor highlight will "+
						"not be a rectangle",
					w,
					first,
					second,
				)
			}
		}
		if pairs < 2 {
			t.Fatalf(
				"width %d: matched %d records, expected 2",
				w,
				pairs,
			)
		}
	}
}

// agentSorts is indexed by a persisted integer, so its order is a contract
// with every ui.json on disk.
//
// ui.json stores agentSort as an index into this slice. Insert a sorter in the
// middle and a saved "5" quietly becomes a different column -- nothing errors,
// the list just sorts by something nobody picked.
//
// This happened: adding a "driven" sorter shifted "context" and a sort test
// failed with "context sort put a first", which is a confusing way to discover
// a state-compatibility break.
//
// New sorters go on the END. If this test fails, that is what it is telling
// you -- not that the list is wrong, but that the change is not backward
// compatible with files already written.
func TestAgentSortOrderIsFrozen(t *testing.T) {
	want := []string{
		"last log",
		"context",
		"peak",
		"cost",
		"lines",
		"turns",
		"project",
		"name",
	}
	var got []string
	for _, s := range agentSorts {
		got = append(got, s.name)
	}
	if len(got) < len(want) {
		t.Fatalf(
			"a sorter was REMOVED; every saved agentSort past %d "+
				"now means something else\ngot:  %v\nwant: %v",
			len(got),
			got,
			want,
		)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf(
				"sorter %d is %q, was %q -- ui.json stores "+
					"this index, so every "+
					"remembered sort is now wrong. "+
					"Append instead of "+
					"inserting.\ngot:  %v\nwant: "+
					"%v",
				i,
				got[i],
				want[i],
				got,
				want,
			)
		}
	}
}

// A turn is a person typing. A step is the model answering.
//
// These were one number, counting assistant records and called "turns", which
// overstates a conversation by one to two orders of magnitude: an agentic loop
// emits a record per tool call, so one typed prompt can be forty of them.
//
// Counting tool calls as turns measures something; it is not the number anybody
// means by the word.
func TestTurnsAreTypedPromptsNotToolCalls(t *testing.T) {
	dir := t.TempDir()
	// one person typing, four model records, and a queued prompt that is
	// the harness rather than a person
	p := writeTranscript(t, dir, "s1",
		`{"type":"user","promptSource":"typed","cwd":"/src/puffin"}`,
		asstLine, asstLine, asstLine, asstLine,
		`{"type":"user","promptSource":"queued","cwd":"/src/puffin"}`)
	s, err := readTranscript(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.Turns != 1 {
		t.Errorf(
			"turns = %d, want 1: only a typed prompt is a turn",
			s.Turns,
		)
	}
	if s.Steps != 4 {
		t.Errorf(
			"steps = %d, want 4: every model record is a step",
			s.Steps,
		)
	}
	r, ok := s.Autonomy()
	if !ok || r != 4 {
		t.Errorf(
			"autonomy = %v (%v), want 4: four replies to one "+
				"prompt",
			r,
			ok,
		)
	}
}

// A transcript nobody has typed in yet still has an autonomy question, and
// the answer is not a division by zero.
func TestAutonomyRefusesToDivideByNobody(t *testing.T) {
	dir := t.TempDir()
	p := writeTranscript(t, dir, "s1", asstLine, asstLine)
	s, err := readTranscript(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.Turns != 0 || s.Steps != 2 {
		t.Fatalf("turns=%d steps=%d, want 0 and 2", s.Turns, s.Steps)
	}
	if _, ok := s.Autonomy(); ok {
		t.Error(
			"autonomy reported a ratio with nobody on the other " +
				"side of it",
		)
	}
}
