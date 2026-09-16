package main

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The agents pane is the one that can end somebody's work, so most of what is
// tested here is the gate rather than the grid: every rung -- /quit, TERM, kill
// -9, Escape, C-c
//
// -- asks first, names what it will send, and treats any key but the one it
// asked for as no. No test in this file ever presses the confirming key.

// agentFixture is a pane already holding sessions, so the tests below
// exercise drawing rather than fetching.
func agentFixture() *agentPane {
	now := time.Now()
	p := &agentPane{}
	p.v = AgentView{
		Host: "kestrel", Tmux: true, Files: 3, Read: now,
		Sessions: []AgentSession{
			{
				ID:        "a",
				Project:   "puffin",
				Cwd:       "/src/puffin",
				Branch:    "pane-coverage",
				Model:     "opus-5",
				Effort:    "high",
				Turns:     12,
				Context:   120000,
				Peak:      400000,
				LastKind:  "text",
				LastWrite: now.Add(-time.Minute),
				Tmux: TmuxPane{
					Target:  "dev:0.1",
					Session: "dev",
				},
				TmuxCount:    1,
				CostUSD:      1.25,
				CostKnown:    true,
				LinesAdded:   40,
				LinesRemoved: 3,
			},
			{ID: "b", Project: "albatross", Cwd: "/src/albatross",
				Model:     "sonnet-5",
				Turns:     4,
				Context:   20000,
				Peak:      20000,
				LastKind:  "tool",
				LastWrite: now.Add(-time.Hour),
				Tmux: TmuxPane{
					Target: "dev:0.2",
				}, TmuxCount: 1},
			{ID: "c", Project: "peacock", Cwd: "/src/peacock",
				Model:      "haiku-4.5",
				Turns:      2,
				LastKind:   "user",
				LastWrite:  now.Add(-2 * time.Hour),
				TmuxShared: "dev:0.3"},
		},
	}
	p.sel = "a"
	return p
}

// Every column the pane promises has to appear, because a column that
// silently renders empty reads as a session with nothing in it.
func TestAgentPaneDrawsTheRoster(t *testing.T) {
	out := agentFixture().View(Compile(DodoDark()), 200, 50)
	for _, want := range []string{
		"puffin",
		"albatross",
		"peacock",
		"opus-5",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the roster is missing %q", want)
		}
	}
	// effort rides in the model cell: one fact about how a session is run
	if !strings.Contains(out, "opus-5/high") {
		t.Error("the effort is not shown beside the model")
	}
}

// An empty roster is a state, and so is a tmux that is not running.
func TestAgentPaneEmptyStates(t *testing.T) {
	p := &agentPane{v: AgentView{Host: "kestrel"}}
	if out := p.View(Compile(DodoDark()), 160, 44); out == "" {
		t.Fatal("an empty pane drew nothing at all")
	}
	p.v.Warnings = []string{"tmux: no server running"}
	if out := p.View(Compile(DodoDark()), 160, 44); !strings.Contains(
		out,
		"no server running",
	) {
		t.Error("a warning was hidden")
	}
}

// Tagging is how a fleet is acted on at once, and it moves on so tagging
// three in a row is three keystrokes rather than six.
func TestAgentPaneTagging(t *testing.T) {
	p := agentFixture()
	p.cursor = 0
	p.Update(key(' '))
	if !p.tagged["a"] {
		t.Fatal("space did not tag the row under the cursor")
	}
	if p.cursor != 1 {
		t.Fatalf("tagging did not move on: cursor %d", p.cursor)
	}
	p.Update(key(' '))
	if len(p.tagged) != 2 {
		t.Fatalf("tagged %v", p.tagged)
	}
	// tagging the same row again puts it down
	p.cursor = 0
	p.Update(key(' '))
	if p.tagged["a"] {
		t.Error("space did not untag")
	}
	// # says what is held and what can be done with it
	p.Update(key('#'))
	if !strings.Contains(p.note, "tagged") {
		t.Errorf("note %q", p.note)
	}
	// esc is "put down what you are holding"
	p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if len(p.tagged) != 0 {
		t.Error("esc did not drop the tags")
	}
	// and with nothing held, # says how to hold something
	p.Update(key('#'))
	if !strings.Contains(p.note, "space tags the row") {
		t.Errorf("note %q", p.note)
	}
}

// /quit is the gentlest rung: the agent exits on its own terms and stays
// resumable. It still asks, and it still names the exact tmux command.
func TestAgentPaneQuitAsksAndNamesTheCommand(t *testing.T) {
	p := agentFixture()
	p.cursor, p.sel = 0, "a"
	p.Update(key('Q'))
	if p.pending == nil {
		t.Fatal("Q sent /quit without asking")
	}
	if w := p.pending.intent.Wire; !strings.Contains(
		w,
		"send-keys -t dev:0.1",
	) ||
		!strings.Contains(w, "/quit") {
		t.Fatalf("wire %q", w)
	}
	// any key but the one it asked for is no
	p.Update(key('z'))
	if p.pending != nil {
		t.Fatal("a wrong key left the action armed")
	}
	if p.note != "cancelled" {
		t.Errorf("note %q", p.note)
	}
	// a session with no pane cannot be quit, and says why rather than
	// silently doing nothing
	p.cursor, p.sel = 2, "c"
	p.Update(key('Q'))
	if p.pending != nil {
		t.Fatal("armed a quit for a session with no pane")
	}
	if !strings.Contains(p.note, "nothing to quit") {
		t.Errorf("note %q", p.note)
	}
}

// TERM and kill -9 are different rungs. Both go through the pane's children,
// which means asking tmux -- and when tmux cannot answer, the pane refuses
// and says why instead of arming a signal it cannot describe. Signalling the
// wrong pid is the one outcome worse than not signalling.
func TestAgentPaneSignalRungs(t *testing.T) {
	for _, k := range []rune{'T', '9'} {
		p := agentFixture()
		p.cursor, p.sel = 0, "a"
		p.Update(key(k))
		if p.pending != nil {
			// a real tmux answered: then it must name the signal it
			// will send and what it will send it to
			w := p.pending.intent.Wire
			if !strings.Contains(w, "kill -") {
				t.Errorf(
					"%c: wire %q does not name a signal",
					k,
					w,
				)
			}
			if !strings.Contains(w, "puffin") {
				t.Errorf(
					"%c: wire %q does not say which "+
						"session",
					k,
					w,
				)
			}
			p.Update(key('z')) // cancel; never confirm
			continue
		}
		// no tmux server here, which is the ordinary case in a test:
		// the refusal has to carry its reason
		if !strings.Contains(p.note, "nothing signalable") {
			t.Errorf(
				"%c: refused with %q, which says nothing "+
					"about why",
				k,
				p.note,
			)
		}
	}
	// with nothing to act on at all, the rung says so rather than arming
	empty := &agentPane{}
	empty.Update(key('T'))
	if empty.pending != nil ||
		!strings.Contains(empty.note, "nothing to signal") {
		t.Errorf("armed on an empty roster: %q", empty.note)
	}
}

// Escape stops a thought and keeps the context; C-c is the harder one. They
// are asked for BY NAME and wait for a deliberate y.
func TestAgentPaneInterruptsAskByName(t *testing.T) {
	for _, tc := range []struct{ key, wantKey, wantLabel string }{
		{"i", "Escape", "stop generating"},
		{"X", "C-c", "interrupt (C-c)"},
	} {
		p := agentFixture()
		p.cursor, p.sel = 0, "a"
		p.Update(key([]rune(tc.key)[0]))
		if p.ask == nil {
			t.Fatalf("%s did not ask", tc.key)
		}
		if p.ask.key != tc.wantKey || p.ask.label != tc.wantLabel {
			t.Errorf("%s: ask %+v", tc.key, p.ask)
		}
		if !strings.Contains(
			p.View(Compile(DodoDark()), 200, 50),
			tc.wantLabel,
		) {
			t.Errorf("%s: the question is not on screen", tc.key)
		}
		// anything but y is no
		p.Update(key('n'))
		if p.ask != nil {
			t.Errorf("%s: the question survived a no", tc.key)
		}
		if p.note != "cancelled" {
			t.Errorf("%s: note %q", tc.key, p.note)
		}
	}
	// a session whose pane belongs to a newer session is not addressable,
	// and the pane says exactly that rather than sending keys elsewhere
	p := agentFixture()
	p.cursor, p.sel = 2, "c"
	p.Update(key('i'))
	if p.ask != nil {
		t.Fatal("asked to interrupt a session with no pane of its own")
	}
	if !strings.Contains(p.note, "is not what is typing in there") {
		t.Errorf("note %q", p.note)
	}
}

// The composer owns the keyboard while it is open: without that, q closes
// the pane mid-sentence and the message becomes a screen change.
func TestAgentPaneComposerOwnsTheKeyboard(t *testing.T) {
	p := agentFixture()
	p.cursor, p.sel = 0, "a"
	p.Update(key('w'))
	if !p.Capturing() {
		t.Fatal("w did not open the composer")
	}
	for _, r := range "quit now" {
		p.Update(key(r))
	}
	if got := p.compose.Value(); got != "quit now" {
		t.Fatalf("composed %q -- keys leaked to the pane", got)
	}
	p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if p.writing {
		t.Fatal("esc did not close the composer")
	}
	if p.note != "not sent" {
		t.Errorf(
			"note %q -- an abandoned message must say it was not "+
				"sent",
			p.note,
		)
	}
	// enter with no pane to send to says so rather than pretending
	p.cursor, p.sel = 2, "c"
	p.Update(key('w'))
	if p.writing {
		t.Fatal("the composer opened for a session with no pane")
	}
	if !strings.Contains(p.note, "not what is typing in there") {
		t.Errorf("note %q", p.note)
	}
}

// A message is sent to the pane the session actually holds.
func TestAgentPaneSendsToTheRightPane(t *testing.T) {
	p := agentFixture()
	p.cursor, p.sel = 0, "a"
	p.Update(key('w'))
	for _, r := range "hello" {
		p.Update(key(r))
	}
	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter sent nothing")
	}
	if p.writing {
		t.Error("the composer stayed open after sending")
	}
}

// The CRT has two channels: what the session has DONE, and what its pane is
// showing. tab switches, and the pane says which one you are looking at.
func TestAgentPaneTabSwitchesChannel(t *testing.T) {
	p := agentFixture()
	p.cursor, p.sel = 0, "a"
	if p.live {
		t.Fatal("the pane started on the live channel")
	}
	p.Update(tea.KeyMsg{Type: tea.KeyTab})
	if !p.live {
		t.Fatal("tab did not switch channel")
	}
	p.Update(tea.KeyMsg{Type: tea.KeyTab})
	if p.live {
		t.Fatal("tab did not switch back")
	}
}

// A tail that arrives for a session the cursor has already left is dropped
// rather than shown under the wrong name -- and following is only resumed
// when you were already at the bottom.
func TestAgentPaneTailFollowing(t *testing.T) {
	p := agentFixture()
	p.sel = "a"
	lines := make([]TailLine, 100)
	for i := range lines {
		lines[i] = TailLine{Kind: "text", Text: "line"}
	}
	p.Update(tailFetched{id: "a", lines: lines})
	if p.tailFor != "a" || len(p.tail) != 100 {
		t.Fatalf(
			"the tail did not land: for=%q %d lines",
			p.tailFor,
			len(p.tail),
		)
	}
	if !p.tailFollow {
		t.Error("a fresh tail did not follow")
	}
	// scrolled back, and a new tail must not yank the view to the bottom
	p.Update(key('K'))
	if p.tailFollow {
		t.Fatal("page-up did not stop following")
	}
	at := p.tailOff
	p.Update(
		tailFetched{
			id: "a",
			lines: append(
				lines,
				TailLine{Kind: "text", Text: "new"},
			),
		},
	)
	if p.tailOff != at {
		t.Errorf(
			"a refresh moved a scrolled-back tail from %d to %d",
			at,
			p.tailOff,
		)
	}
	// paging back down to the end resumes following
	for i := 0; i < 20; i++ {
		p.Update(key('J'))
	}
	if !p.tailFollow {
		t.Error("scrolling back to the bottom did not resume following")
	}
	// a tail for a session nobody is looking at is dropped
	p.Update(
		tailFetched{
			id:    "zzz",
			lines: []TailLine{{Kind: "text", Text: "someone else"}},
		},
	)
	if p.tailFor != "a" {
		t.Fatal("a late tail was shown under the wrong session")
	}
	// an error reading the tail is reported rather than blank
	p.Update(tailFetched{id: "a", lines: nil, err: errFake})
	if p.tailErr == "" {
		t.Error("a failed tail read was silent")
	}
}

// The notification toggle is a preference and it persists, but the pane must
// also SAY which way it is set.
func TestAgentPaneNotifyToggle(t *testing.T) {
	t.Cleanup(func() { enableNotify(false) })
	p := agentFixture()
	before := notifyEnabled()
	p.Update(key('!'))
	if notifyEnabled() == before {
		t.Fatal("! did not toggle notifications")
	}
	p.Update(key('!'))
	if notifyEnabled() != before {
		t.Fatal("! did not toggle back")
	}
}

// W watches whatever the filter names, and esc clears the filter rather than
// leaving the pane.
func TestAgentPaneWatchAndFilter(t *testing.T) {
	t.Cleanup(func() { restoreWatches(nil); enableNotify(false) })
	restoreWatches(nil)
	p := agentFixture()

	// with no filter there is nothing to watch
	p.Update(key('W'))
	if len(watchesFor("agents")) != 0 {
		t.Fatal("W watched everything")
	}
	p.filter = "opus-5"
	if !p.HandlesEsc() {
		t.Error("a filtered pane did not claim esc")
	}
	p.Update(key('W'))
	if len(watchesFor("agents")) != 1 {
		t.Fatalf("W did not watch the filter: %v", watchesFor("agents"))
	}
	p.Update(key('W'))
	if len(watchesFor("agents")) != 0 {
		t.Fatal("W did not toggle the watch off")
	}
	p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if p.filter != "" {
		t.Fatalf("esc left the filter %q", p.filter)
	}
	if p.HandlesEsc() {
		t.Error("an unfiltered pane claimed esc")
	}
}

// Sorting is a view change: it never reaches the list, and the selection
// follows the session rather than the row number.
func TestAgentPaneSortKeys(t *testing.T) {
	p := agentFixture()
	p.cursor, p.sel = 1, "b"
	for range agentSorts {
		p.Update(key('s'))
		if p.selected() != "b" {
			t.Fatalf(
				"sort %d moved the selection to %q",
				p.sortBy,
				p.selected(),
			)
		}
	}
	desc := p.sortDesc
	p.Update(key('S'))
	if p.sortDesc == desc {
		t.Fatal("S did not reverse the sort")
	}
	if p.selected() != "b" {
		t.Fatalf("reversing moved the selection to %q", p.selected())
	}
}

// The tail follows the cursor. A tail that keeps showing the previous
// row is worse than none, because it looks like an answer.
func TestAgentPaneMovesAndReReadsTheTail(t *testing.T) {
	p := agentFixture()
	p.cursor = 0
	_, cmd := p.Update(key('j'))
	if p.cursor != 1 || p.sel != "b" {
		t.Fatalf("j: cursor %d sel %q", p.cursor, p.sel)
	}
	if cmd == nil {
		t.Error("moving did not read the new session's tail")
	}
	for i := 0; i < 5; i++ {
		p.Update(key('j'))
	}
	if p.cursor != len(p.v.Sessions)-1 {
		t.Fatalf("j past the end: %d", p.cursor)
	}
	for i := 0; i < 5; i++ {
		p.Update(key('k'))
	}
	if p.cursor != 0 {
		t.Fatalf("k past the start: %d", p.cursor)
	}
}

// Acting on a session re-reads: the roster must show what just changed.
func TestAgentPaneActedReReads(t *testing.T) {
	p := agentFixture()
	p.tagged = map[string]bool{"a": true}
	np, cmd := p.Update(agentActed{note: "sent /quit to puffin"})
	if cmd == nil {
		t.Fatal("the pane did not re-read after acting")
	}
	ap := np.(*agentPane)
	if ap.note != "sent /quit to puffin" {
		t.Errorf("note %q", ap.note)
	}
	if len(ap.tagged) != 0 {
		t.Error("the tags survived the action they were for")
	}
	// tmux reporting back lands the same way
	p.Update(tmuxDone{note: "sent"})
	if p.note != "sent" {
		t.Errorf("note %q", p.note)
	}
	p.Update(tmuxDone{err: errFake})
	if !strings.Contains(p.note, "colima exited 1") {
		t.Errorf("a tmux failure was not reported: %q", p.note)
	}
}

// Nothing may render outside the terminal, at any size a font change can
// produce.
func TestAgentPaneDrawsInATinyWindow(t *testing.T) {
	p := agentFixture()
	for _, wh := range [][2]int{{40, 6}, {20, 0}, {240, 60}} {
		if out := p.View(Compile(DodoDark()), wh[0], wh[1]); out == "" {
			t.Errorf("%dx%d drew nothing", wh[0], wh[1])
		}
	}
}

// The filter is a chip you click, and everything that indexes the list goes
// through visible() -- so a filter cannot leave the cursor pointing at a row
// nobody can see.
func TestAgentPaneFilterHidesRows(t *testing.T) {
	p := agentFixture()
	if len(p.visible()) != 3 {
		t.Fatalf("unfiltered: %d rows", len(p.visible()))
	}
	p.filter = shortModel("opus-5")
	vis := p.visible()
	if len(vis) != 1 || vis[0].ID != "a" {
		t.Fatalf("filtered to %d rows: %+v", len(vis), vis)
	}
	// a filter matching nothing hides everything rather than falling back
	// to showing all of it
	p.filter = "no-such-model"
	if len(p.visible()) != 0 {
		t.Fatalf("an unmatched filter showed %d rows", len(p.visible()))
	}
	// and the cursor cannot address a row that is not there
	p.cursor = 2
	if got := p.selected(); got != "" {
		t.Errorf("selected %q from an empty filter", got)
	}
}

// Clicking is documented in the help line, so it has to work: a header
// sorts, the same header again reverses, a chip filters and unfilters, a row
// selects, and the wheel walks the list.
func TestAgentPaneMouse(t *testing.T) {
	p := agentFixture()
	// the hitboxes are recorded by a render, so click after drawing
	p.View(Compile(DodoDark()), 220, 50)

	if len(p.hitHeader) == 0 {
		t.Fatal("the render recorded no clickable headers")
	}
	h := p.hitHeader[0]
	var want int
	for i, srt := range agentSorts {
		if srt.name == h.name {
			want = i
		}
	}
	p.sortBy = (want + 1) % len(agentSorts)
	p.Click(h.x, h.y, 0)
	if p.sortBy != want {
		t.Fatalf(
			"clicking %q sorted by %d, want %d",
			h.name,
			p.sortBy,
			want,
		)
	}
	desc := p.sortDesc
	p.Click(h.x, h.y, 0)
	if p.sortDesc == desc {
		t.Error("clicking the sorted column again did not reverse it")
	}

	// a chip filters, and clicking it again clears
	p.View(Compile(DodoDark()), 220, 50)
	if len(p.hitChips) == 0 {
		t.Fatal("the render recorded no clickable model chips")
	}
	c := p.hitChips[0]
	p.Click(c.x, c.y, 0)
	if p.filter != c.name {
		t.Fatalf("clicking the %q chip set filter %q", c.name, p.filter)
	}
	p.Click(c.x, c.y, 0)
	if p.filter != "" {
		t.Fatalf("clicking the chip again left filter %q", p.filter)
	}

	// a row selects it. A record is two lines, so the second record starts
	// linesPerRecord down -- hitTop+1 is the first record's meter line and
	// selects the first record, which is TestEitherLineOfARecordSelectsIt.
	p.View(Compile(DodoDark()), 220, 50)
	if p.hitRows > linesPerRecord {
		p.Click(0, p.hitTop+linesPerRecord, 0)
		if p.cursor != p.offset+1 {
			t.Errorf(
				"clicking the second record put the cursor "+
					"at %d",
				p.cursor,
			)
		}
	}
	// a click below the last row does not select a row that is not there
	p.Click(0, p.hitTop+p.hitRows+5, 0)

	// the wheel walks the list and stops at the ends
	p.cursor = 0
	p.Click(0, p.hitTop, 1)
	if p.cursor != 1 {
		t.Fatalf("wheel down: %d", p.cursor)
	}
	p.Click(0, p.hitTop, -1)
	p.Click(0, p.hitTop, -1)
	if p.cursor != 0 {
		t.Fatalf("wheel up past the start: %d", p.cursor)
	}
	for i := 0; i < 10; i++ {
		p.Click(0, p.hitTop, 1)
	}
	if p.cursor != len(p.visible())-1 {
		t.Fatalf("wheel down past the end: %d", p.cursor)
	}
}

// A signal that partly failed says which half, because "sent to 3" and "2
// sent, 1 failed" are different things to do next about.
func TestSignalNoteReportsPartialFailure(t *testing.T) {
	if got := signalNote("kill -9", 3, nil); got != "kill -9 sent to 3 "+
		"session(s)" {
		t.Errorf("clean: %q", got)
	}
	got := signalNote("terminate", 3, []string{"peacock: no pane"})
	if !strings.Contains(got, "2 sent, 1 failed") {
		t.Errorf("partial: %q", got)
	}
	if !strings.Contains(got, "peacock: no pane") {
		t.Errorf("the reason is missing: %q", got)
	}
}
