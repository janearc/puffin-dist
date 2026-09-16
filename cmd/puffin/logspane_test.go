package main

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The logs pane is the screen a cluster is managed from, and until now nothing
// rendered it: View was reached only incidentally, by a full-model draw that
// handed it no lines.
//
// So every claim the pane makes on screen -- following or scrolled back, which
// lines matched, what is being waited for, which stream a line came from -- was
// unverified. These tests hand it real buffers and read what it drew.

// logFixture is a buffer with the shapes the pane treats differently: a
// structured line, a plain one, and something on stderr.
func logFixture() []LogLine {
	base := time.Date(2026, 8, 30, 23, 54, 38, 0, time.UTC)
	return []LogLine{
		{
			When:    base,
			Service: "kingfisher",
			Pod:     "kingfisher-ingestd-79f7994cd7-f9pxr",
			NS:      "local",
			Stream:  "stdout",
			Text: `{"level": "info",` +
				` "event": "no_pipeline_yet"}`,
		},
		{
			When:    base.Add(time.Second),
			Service: "athlete",
			Pod:     "athlete-59c5b49dc7-wv6f5",
			NS:      "local",
			Stream:  "stdout",
			Text:    "plain line about a lap",
		},
		{
			When:    base.Add(2 * time.Second),
			Service: "hm",
			Pod:     "hm-5bf58fd9f8-7h2tm",
			NS:      "local",
			Stream:  "stderr",
			Text:    "refused an off-contract record",
		},
	}
}

// logsWith is a pane holding a buffer, so search and following are tested
// without a cluster.
func logsWith(lines []LogLine) *logsPane {
	p := &logsPane{lines: logTailDefault}
	p.v = LogView{
		Lines: lines,
		File:  "/fleet-logs/containers-2026.08.30.jsonl",
	}
	p.cursor = len(lines) - 1
	return p
}

// The empty pane must say what it would read rather than showing a blank
// screen: "nothing here" and "I have not looked yet" are different states and
// the operator is entitled to know which one they are in.
func TestLogsPaneEmptySaysWhatItWouldRead(t *testing.T) {
	p := &logsPane{}
	out := p.View(Compile(DodoDark()), 120, 40)
	if !strings.Contains(out, "no lines yet") {
		t.Fatal("an empty pane did not say it was empty")
	}
	p.v.Warnings = []string{"cannot find the log collector"}
	out = p.View(Compile(DodoDark()), 120, 40)
	if !strings.Contains(out, "cannot find the log collector") {
		t.Fatal(
			"a warning was not shown; a pane that hides its own " +
				"failure is worse than an empty one",
		)
	}
}

// Every line in the buffer reaches the screen with the two things that make
// it useful: when, and who said it.
func TestLogsPaneDrawsNameAndTime(t *testing.T) {
	out := logsWith(logFixture()).View(Compile(DodoDark()), 120, 40)
	for _, want := range []string{
		"kingfisher",
		"athlete",
		"hm",
		"23:54:38",
		"23:54:40",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the pane did not draw %q", want)
		}
	}
	// how much, over what span, from where -- "3 lines" alone is
	// unanswerable, because 1000 lines of this estate is two minutes on a
	// quiet day and twenty seconds when valhalla is being probed
	if !strings.Contains(out, "3 lines") {
		t.Error("the pane did not say how much it is showing")
	}
	if !strings.Contains(out, "23:54:38–23:54:40") {
		t.Error("the pane did not say what span the buffer covers")
	}
	if !strings.Contains(out, "containers-2026.08.30.jsonl") {
		t.Error("the pane did not say where the lines came from")
	}
	// and from whom, loudest first: when one service is two thirds of the
	// screen, scrolling will not find the other third
	if !strings.Contains(out, "3 services") {
		t.Error("the pane did not say who wrote the buffer")
	}
}

// A structured line is reordered so the message survives the terminal's
// width, and its level is lifted into a column of its own. json keys arrive
// alphabetically, which buries what happened in the middle of the line.
func TestLogsPaneLiftsTheLevel(t *testing.T) {
	out := logsWith(logFixture()).View(Compile(DodoDark()), 120, 40)
	if !strings.Contains(out, "info") {
		t.Error("a structured line's level was not lifted out")
	}
	if !strings.Contains(out, "no_pipeline_yet") {
		t.Error("the event was lost from a structured line")
	}
}

// Following versus scrolled back is the pane's most consequential claim: a
// screen that has stopped following looks identical to one that is following
// until the next refresh arrives, and then you have missed something.
func TestLogsPaneSaysWhetherItIsFollowing(t *testing.T) {
	p := logsWith(logFixture())
	if out := p.View(Compile(DodoDark()), 120, 40); !strings.Contains(
		out,
		"following",
	) {
		t.Fatal("a pane at the bottom did not say it was following")
	}
	p.cursor = 0
	out := p.View(Compile(DodoDark()), 120, 40)
	if !strings.Contains(out, "scrolled back") {
		t.Fatal("a scrolled-back pane still claimed to be following")
	}
	if !strings.Contains(out, "G to follow again") {
		t.Error("the pane did not say how to get back")
	}
}

// A refresh follows the bottom only if you were already there. This is the
// bargain tail -f makes, and getting it wrong makes the pane unreadable at
// exactly the moment you are reading it.
func TestLogsPaneRefreshDoesNotYankAScrolledCursor(t *testing.T) {
	p := logsWith(logFixture())
	p.cursor = 0
	next := logFixture()
	next = append(
		next,
		LogLine{When: time.Now(), Service: "dodo", Text: "new arrival"},
	)
	np, _ := p.Update(fleetLogsFetched{LogView{Lines: next}})
	if got := np.(*logsPane).cursor; got != 0 {
		t.Fatalf("a refresh moved a scrolled-back cursor to %d", got)
	}
	// but a cursor at the bottom rides the new line in
	p = logsWith(logFixture())
	np, _ = p.Update(fleetLogsFetched{LogView{Lines: next}})
	if got := np.(*logsPane).cursor; got != len(next)-1 {
		t.Fatalf("a following cursor did not follow: %d", got)
	}
}

// The search is the pane's whole point: type a pattern, see which lines have
// it, walk between them. n and N wrap, because every pager wraps and the
// absence of it is what people notice.
func TestLogsPaneSearchMarksAndWalks(t *testing.T) {
	p := logsWith(logFixture())
	p.Update(key2('/'))
	if !p.Capturing() {
		t.Fatal("/ did not open the search box")
	}
	for _, r := range "contract" {
		p.Update(key2(r))
	}
	if !strings.Contains(
		p.View(Compile(DodoDark()), 120, 40),
		"/contract",
	) {
		t.Error("what is being typed is not on screen")
	}
	p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if p.Capturing() {
		t.Fatal("enter did not close the search box")
	}
	if len(p.matches) != 1 || p.matches[0] != 2 {
		t.Fatalf("matches: %v, want just the stderr line", p.matches)
	}
	if !strings.Contains(
		p.View(Compile(DodoDark()), 120, 40),
		"1 matches",
	) {
		t.Error("the pane did not say how many lines matched")
	}
	if p.cursor != 2 {
		t.Fatalf("enter did not jump to the match: cursor %d", p.cursor)
	}
	// one match, so n wraps back onto it rather than going nowhere
	p.Update(key2('n'))
	if p.cursor != 2 {
		t.Fatalf("n did not wrap: cursor %d", p.cursor)
	}
	p.Update(key2('N'))
	if p.cursor != 2 {
		t.Fatalf("N did not wrap: cursor %d", p.cursor)
	}
	// esc drops the search entirely
	p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if p.query != "" || p.matches != nil {
		t.Fatal("esc did not clear the search")
	}
}

// A search matches on the service as well as the text, because "show me
// kingfisher" is the commonest thing anyone types into a log screen.
func TestLogsPaneSearchesTheServiceName(t *testing.T) {
	p := logsWith(logFixture())
	p.query = "kingfisher"
	p.find()
	if len(p.matches) != 1 || p.matches[0] != 0 {
		t.Fatalf("matches: %v, want the kingfisher line", p.matches)
	}
	// and /slashes/ mean a regex here exactly as they do in a watch
	p.query = "/^plain/"
	p.find()
	if len(p.matches) != 1 || p.matches[0] != 1 {
		t.Fatalf("regex matches: %v", p.matches)
	}
}

// Backspace and esc in the search box, which are the two ways a typed query
// gets taken back.
func TestLogsPaneSearchBoxEditing(t *testing.T) {
	p := logsWith(logFixture())
	p.Update(key2('/'))
	for _, r := range "hmx" {
		p.Update(key2(r))
	}
	p.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if p.query != "hm" {
		t.Fatalf("backspace: query %q", p.query)
	}
	p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if p.typing || p.query != "" {
		t.Fatal("esc did not abandon the search")
	}
	// backspace on an empty box is not an error
	p.Update(key2('/'))
	p.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if p.query != "" {
		t.Fatalf("query %q", p.query)
	}
}

// W turns the current search into a standing watch, and says so on screen. A
// standing instruction that is invisible is one you forget you gave.
func TestLogsPaneWatchFromTheSearchBox(t *testing.T) {
	t.Cleanup(func() { restoreWatches(nil); enableNotify(false) })
	restoreWatches(nil)
	p := logsWith(logFixture())

	// W with nothing typed watches nothing rather than watching everything
	p.Update(key2('W'))
	if len(watchesFor("logs")) != 0 {
		t.Fatal("W with an empty search box created a watch")
	}
	p.query = "off-contract"
	p.Update(key2('W'))
	if len(watchesFor("logs")) != 1 {
		t.Fatalf("W did not create the watch: %v", watchesFor("logs"))
	}
	if !strings.Contains(
		p.View(Compile(DodoDark()), 120, 40),
		"watching: off-contract",
	) {
		t.Error("the pane does not show what it is waiting for")
	}
	// pressing it again takes the watch back
	p.Update(key2('W'))
	if len(watchesFor("logs")) != 0 {
		t.Fatal("W did not toggle the watch off")
	}
}

// Visual line mode: v marks, the pane says how much is selected, y takes it,
// and v again leaves -- the way it does in vim, rather than needing esc for
// something that is not modal in any other sense.
func TestLogsPaneVisualSelection(t *testing.T) {
	p := logsWith(logFixture())
	p.cursor = 0
	p.Update(key2('v'))
	if !p.vis.on {
		t.Fatal("v did not enter visual mode")
	}
	p.Update(key2('j'))
	out := p.View(Compile(DodoDark()), 120, 40)
	if !strings.Contains(out, "visual 2 lines") {
		t.Error("the pane did not say how much is selected")
	}
	p.Update(key2('y'))
	if p.vis.on {
		t.Error("y did not leave visual mode")
	}
	if p.yanked == "" {
		t.Error(
			"y reported nothing; a yank with no receipt is a " +
				"yank you repeat",
		)
	}
	if !strings.Contains(p.View(Compile(DodoDark()), 120, 40), p.yanked) {
		t.Error("the yank receipt is not on screen")
	}
	// any key clears the receipt: it is a receipt, not a status
	p.Update(key2('j'))
	if p.yanked != "" {
		t.Error("the yank receipt outlived the next keystroke")
	}
	// esc leaves visual mode without taking anything
	p.Update(key2('v'))
	p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if p.vis.on {
		t.Error("esc did not leave visual mode")
	}
}

// Y takes the whole buffer, for the times the thing you want to paste is all
// of it.
func TestLogsPaneYankAll(t *testing.T) {
	p := logsWith(logFixture())
	p.Update(key2('Y'))
	if p.yanked == "" {
		t.Fatal("Y reported nothing")
	}
	// what goes on the clipboard is the line as a person wants it in a
	// ticket: time, where it came from, and what it said
	raw := p.rawLines()
	if len(raw) != 3 ||
		!strings.Contains(
			raw[2],
			"local/hm-5bf58fd9f8-7h2tm stderr refused",
		) {
		t.Fatalf("raw line: %q", raw[2])
	}
}

// The vim motions, which are the reason the pane is usable at 20000 lines.
func TestLogsPaneMotions(t *testing.T) {
	lines := make([]LogLine, 200)
	base := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	for i := range lines {
		lines[i] = LogLine{
			When:    base.Add(time.Duration(i) * time.Second),
			Service: "athlete",
			Text:    "line",
		}
	}
	p := logsWith(lines)
	// the window has to be established by a render before the page keys
	// know how far a page is
	p.View(Compile(DodoDark()), 120, 40)
	page := p.page()
	if page < 1 {
		t.Fatalf("page size %d", page)
	}

	p.Update(key2('g'))
	if p.cursor != 0 {
		t.Fatalf("g: cursor %d", p.cursor)
	}
	p.Update(key2('G'))
	if p.cursor != len(lines)-1 {
		t.Fatalf("G: cursor %d", p.cursor)
	}
	p.Update(key2('k'))
	if p.cursor != len(lines)-2 {
		t.Fatalf("k: cursor %d", p.cursor)
	}
	p.Update(key2('j'))
	if p.cursor != len(lines)-1 {
		t.Fatalf("j: cursor %d", p.cursor)
	}
	// j at the bottom and k at the top stay put rather than wrapping
	p.Update(key2('j'))
	if p.cursor != len(lines)-1 {
		t.Fatalf("j past the end: %d", p.cursor)
	}
	p.cursor = 0
	p.Update(key2('k'))
	if p.cursor != 0 {
		t.Fatalf("k past the start: %d", p.cursor)
	}

	for _, k := range []tea.KeyMsg{
		{Type: tea.KeyCtrlF},
		{Type: tea.KeyCtrlD},
		{Type: tea.KeyCtrlB},
	} {
		p.Update(k)
	}
	if p.cursor <= 0 || p.cursor >= len(lines)-1 {
		t.Fatalf("the page keys went nowhere useful: %d", p.cursor)
	}
	p.Update(tea.KeyMsg{Type: tea.KeyCtrlU})

	// H, M, L are the screen's top, middle and bottom -- vim's meaning, not
	// the buffer's ends
	p.View(Compile(DodoDark()), 120, 40)
	p.Update(key2('H'))
	top := p.cursor
	p.Update(key2('L'))
	bottom := p.cursor
	p.Update(key2('M'))
	if !(top <= p.cursor && p.cursor <= bottom) {
		t.Fatalf(
			"M landed outside the screen: %d not in [%d,%d]",
			p.cursor,
			top,
			bottom,
		)
	}
	if bottom-top+1 > p.window {
		t.Fatalf(
			"H..L spans %d rows, more than the %d on screen",
			bottom-top+1,
			p.window,
		)
	}
}

// B cycles how much history is in front of you, and re-reads. How much is a
// question only the operator can answer.
func TestLogsPaneBufferSizeCycles(t *testing.T) {
	p := logsWith(logFixture())
	p.lines = logTailSteps[0]
	seen := map[int]bool{}
	for range logTailSteps {
		_, cmd := p.Update(key2('B'))
		if cmd == nil {
			t.Fatal("B did not re-read")
		}
		seen[p.lines] = true
	}
	if len(seen) != len(logTailSteps) {
		t.Fatalf("B visited %v, want all of %v", seen, logTailSteps)
	}
	// an unrecognised size resets to the first step rather than sticking
	p.lines = 77
	p.Update(key2('B'))
	if p.lines != logTailSteps[0] {
		t.Fatalf("lines %d", p.lines)
	}
}

// esc belongs to the pane while there is something to take back, and to the
// event loop otherwise -- which is what makes esc leave the pane at all.
func TestLogsPaneHandlesEsc(t *testing.T) {
	p := logsWith(logFixture())
	if p.HandlesEsc() {
		t.Error("an idle pane claimed esc")
	}
	p.query = "boom"
	if !p.HandlesEsc() {
		t.Error("a pane with a live search did not claim esc")
	}
	p.query, p.typing = "", true
	if !p.HandlesEsc() {
		t.Error("a pane with the search box open did not claim esc")
	}
}

// Load says how much to pull and defaults rather than pulling nothing.
func TestLogsPaneLoadDefaults(t *testing.T) {
	p := &logsPane{}
	if cmd := p.Load(""); cmd == nil {
		t.Fatal("Load fetched nothing")
	}
	if p.lines != logTailDefault {
		t.Fatalf(
			"lines %d, want the default %d",
			p.lines,
			logTailDefault,
		)
	}
}

// newestLine is the high-water mark, and an empty buffer has none -- opening
// the pane must not announce the whole of history.
func TestNewestLine(t *testing.T) {
	if got := newestLine(nil); !got.IsZero() {
		t.Fatalf("an empty buffer has a newest line: %v", got)
	}
	lines := logFixture()
	want := lines[2].When
	if got := newestLine(lines); !got.Equal(want) {
		t.Fatalf("newest %v want %v", got, want)
	}
}

// The pane must draw at a terminal too small to be reasonable rather than
// panicking: puffin is opened in a tmux split as often as full screen.
func TestLogsPaneDrawsInATinyWindow(t *testing.T) {
	p := logsWith(logFixture())
	for _, wh := range [][2]int{{40, 6}, {20, 0}, {200, 60}} {
		if out := p.View(Compile(DodoDark()), wh[0], wh[1]); out == "" {
			t.Errorf("%dx%d drew nothing", wh[0], wh[1])
		}
	}
}

// A search box that can run commands is not a search box. The query is
// typed by an operator and goes through sh -c inside a pod.
func TestShellQuote(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"boom", "'boom'"},
		{"", "''"},
		{"it's", `'it'\''s'`},
		{"; rm -rf /", "'; rm -rf /'"},
		{"$(whoami)", "'$(whoami)'"},
	} {
		if got := shellQuote(tc.in); got != tc.want {
			t.Errorf(
				"shellQuote(%q) = %s, want %s",
				tc.in,
				got,
				tc.want,
			)
		}
	}
}

// serviceOf is the whole naming rule: what the controller appended comes
// off, and anything that is not a controller's suffix stays on. A service
// misnamed here is a service the operator cannot find.
func TestServiceOf(t *testing.T) {
	for _, tc := range []struct{ pod, want string }{
		{"athlete-59c5b49dc7-wv6f5", "athlete"},
		{"kingfisher-ingestd-79f7994cd7-f9pxr", "kingfisher-ingestd"},
		{"kafka-0", "kafka"},
		{"vector-collector-c594f5884-qc46v", "vector-collector"},
		{"", ""},
		// a bare name keeps its shape rather than being trimmed to
		// nothing
		{"postgres", "postgres"},
		// hyphenated but not a controller suffix: the last segment is
		// too long to be a pod id and the one before is not a hash
		{"kestrel-e2e-somethingelse", "kestrel-e2e-somethingelse"},
		// uppercase is not what kubernetes generates, so it is not a
		// suffix
		{"athlete-59C5B49DC7-WV6F5", "athlete-59C5B49DC7-WV6F5"},
	} {
		if got := serviceOf(tc.pod); got != tc.want {
			t.Errorf(
				"serviceOf(%q) = %q, want %q",
				tc.pod,
				got,
				tc.want,
			)
		}
	}
}

// jump with no cursor sitting on a match lands on the first one after it,
// and wraps to the top when there is none.
func TestLogsPaneJumpWraps(t *testing.T) {
	p := logsWith(logFixture())
	p.query = "kingfisher"
	p.find()
	p.cursor = 2 // past the only match, which is line 0
	p.jump(0)
	if p.cursor != 0 {
		t.Fatalf("jump did not wrap to the first match: %d", p.cursor)
	}
	// and a jump with nothing to jump to leaves the cursor alone
	p.query = "nothing matches this"
	p.find()
	p.cursor = 1
	p.jump(1)
	if p.cursor != 1 {
		t.Fatalf("jump moved with no matches: %d", p.cursor)
	}
}

// page never returns zero: the page keys must move, even before the first
// render has said how tall the window is.
func TestLogsPanePageIsNeverZero(t *testing.T) {
	p := &logsPane{}
	if got := p.page(); got != 1 {
		t.Fatalf("page with no window: %d, want 1", got)
	}
	p.window = 40
	if got := p.page(); got != 39 {
		t.Fatalf(
			"page: %d, want the window less the line that stays "+
				"on screen",
			got,
		)
	}
}

// The filter runs IN THE POD, and that is the whole distinction: / searches the
// thousand lines that happened to be pulled, f searches the file.
//
// A buffer of 1000 lines is about two minutes of this estate, so an in-buffer
// search for a quiet service answers "no matches" when the truth is "not in the
// last two minutes" -- which is the failure that made the pane feel unreliable
// rather than empty.
func TestLogsPaneFilterGoesToThePod(t *testing.T) {
	p := logsWith(logFixture())
	p.Update(key2('f'))
	if !p.Capturing() {
		t.Fatal("f did not open the filter box")
	}
	for _, r := range "postgresx" {
		p.Update(key2(r))
	}
	p.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if p.filter != "postgres" {
		t.Fatalf("filter %q", p.filter)
	}
	// what is being typed says where it will run, because the two boxes do
	// very different things and look alike
	out := p.View(Compile(DodoDark()), 140, 44)
	if !strings.Contains(out, "filter: postgres") ||
		!strings.Contains(out, "grep in the pod") {
		t.Error("the filter box does not say it runs in the pod")
	}

	// enter re-reads with the filter applied, from the top: the buffer
	// about to arrive has nothing to do with the one on screen
	p.cursor = 2
	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter did not re-read")
	}
	if p.filtering {
		t.Error("the filter box stayed open")
	}
	if p.cursor != 0 {
		t.Errorf(
			"cursor %d -- a filtered read kept a position from "+
				"the unfiltered buffer",
			p.cursor,
		)
	}
	if !strings.Contains(
		p.View(Compile(DodoDark()), 140, 44),
		"filtered in the pod: postgres",
	) {
		t.Error(
			"a standing filter is not on screen; an invisible " +
				"filter is one you forget you set",
		)
	}
	// esc while typing abandons the box without changing what is set
	p.Update(key2('f'))
	p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if p.filtering || p.filter != "postgres" {
		t.Fatalf("esc changed the filter to %q", p.filter)
	}
}

// The filter is what Load actually sends, or it is decoration.
func TestLogsPaneLoadSendsTheFilter(t *testing.T) {
	p := &logsPane{filter: "postgres"}
	if cmd := p.Load(""); cmd == nil {
		t.Fatal("Load fetched nothing")
	}
	if p.filter != "postgres" {
		t.Fatal("Load dropped the filter")
	}
}

// esc puts down one thing at a time, and the search before the filter: they
// are different sizes of "never mind".
func TestLogsPaneEscUnwindsInOrder(t *testing.T) {
	p := logsWith(logFixture())
	p.filter, p.query = "postgres", "boom"
	p.find()
	if !p.HandlesEsc() {
		t.Fatal(
			"a pane holding a search and a filter did not claim " +
				"esc",
		)
	}
	p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if p.query != "" {
		t.Fatal("esc did not drop the search first")
	}
	if p.filter != "postgres" {
		t.Fatal("esc dropped the filter along with the search")
	}
	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if p.filter != "" {
		t.Fatal("esc did not drop the filter")
	}
	if cmd == nil {
		t.Error("clearing the filter did not re-read the estate")
	}
	if p.HandlesEsc() {
		t.Error("a pane holding nothing still claimed esc")
	}
}

// An empty result with a filter set is a REAL answer -- the grep ran over
// the whole file -- and a different one from not having looked yet.
func TestLogsPaneEmptyFilterResultSaysSo(t *testing.T) {
	p := &logsPane{filter: "postgres"}
	p.v = LogView{File: "/fleet-logs/containers-2026.08.30.jsonl"}
	out := p.View(Compile(DodoDark()), 140, 44)
	if !strings.Contains(
		out,
		"nothing in containers-2026.08.30.jsonl matches postgres",
	) {
		t.Fatalf("an empty filtered read said: %q", out)
	}
	if !strings.Contains(out, "the whole file was searched") {
		t.Error(
			"the pane did not say how far it looked, so the " +
				"answer means nothing",
		)
	}
}

// The cursor comes back on the LINE, not on the index. The buffer is a fresh
// tail every ten seconds: every line that arrived shifts every older line
// down, so keeping the index slides the reader down the screen -- quietly,
// which is worse than being yanked, because a slide looks like holding still.
func TestLogsPaneHoldsItsPlaceAcrossARefresh(t *testing.T) {
	base := logFixture()
	p := logsWith(base)
	p.cursor = 0 // standing on the kingfisher line, scrolled back
	p.Update(key2('j'))
	p.Update(key2('k'))
	want := p.v.Lines[0]

	// two new lines arrive at the bottom and two old ones fall off the top
	next := append([]LogLine{}, base...)
	for i := 0; i < 2; i++ {
		next = append(next, LogLine{
			When: base[2].When.Add(
				time.Duration(i+1) * time.Second,
			),
			Service: "valhalla", Pod: "valhalla-1",
			Stream: "stdout", Text: "probe"})
	}
	p.Update(fleetLogsFetched{LogView{Lines: next}})

	if got := p.v.Lines[p.cursor]; got.Text != want.Text ||
		!got.When.Equal(want.When) {
		t.Fatalf("the cursor slid from %q to %q", want.Text, got.Text)
	}
	if p.drifted {
		t.Error(
			"the pane claimed the line was gone when it was " +
				"still there",
		)
	}
}

// And when the line really has gone, the pane SAYS so rather than landing
// the cursor somewhere near and letting the reader believe they held still.
func TestLogsPaneSaysWhenYourLineScrolledAway(t *testing.T) {
	p := logsWith(logFixture())
	p.cursor = 0
	p.Update(key2('j'))
	p.Update(key2('k'))

	gone := []LogLine{
		{
			When:    time.Now(),
			Service: "valhalla",
			Pod:     "valhalla-1",
			Text:    "all new",
		},
	}
	p.Update(fleetLogsFetched{LogView{Lines: gone}})
	if !p.drifted {
		t.Fatal("the anchored line aged out and the pane said nothing")
	}
	if !strings.Contains(
		p.View(Compile(DodoDark()), 140, 44),
		"scrolled out of the buffer",
	) {
		t.Error("the drift is not on screen")
	}
	// and following again clears it: at the bottom there is nothing to hold
	p.Update(key2('G'))
	p.Update(fleetLogsFetched{LogView{Lines: gone}})
	if p.drifted {
		t.Error("following still claims to have drifted")
	}
}

// How much log, over what span, and from whom -- the three questions "1000
// lines" cannot answer.
func TestLogsPaneSaysHowMuchAndFromWhom(t *testing.T) {
	lines := logFixture()
	for i := 0; i < 20; i++ {
		lines = append(lines, LogLine{
			When: lines[2].When.Add(
				time.Duration(i) * time.Second,
			),
			Service: "valhalla", Pod: "valhalla-1",
			Stream: "stdout", Text: "probe"})
	}
	p := logsWith(lines)
	out := p.View(Compile(DodoDark()), 160, 44)
	// the loudest service first: knowing it is 20 of the 23 lines is the
	// difference between filtering and scrolling
	src := p.sources(160)
	if !strings.HasPrefix(src, "4 services · valhalla 20") {
		t.Fatalf("sources: %q", src)
	}
	if !strings.Contains(out, "valhalla 20") {
		t.Error("the breakdown is not on screen")
	}
	// the span, so a search that found nothing can be judged
	span, from, to := p.span()
	if span != 21*time.Second {
		t.Errorf("span %s", span)
	}
	if from.After(to) {
		t.Error("the span runs backwards")
	}
	// a long list is truncated rather than wrapping the header
	if w := 40; len([]rune(p.sources(w))) > w {
		t.Errorf(
			"sources overflowed a %d-column header: %q",
			w,
			p.sources(w),
		)
	}
	// and an empty buffer has no sources line rather than a lie
	if got := (&logsPane{}).sources(80); got != "" {
		t.Errorf("an empty buffer listed sources: %q", got)
	}
}

// A buffer covering ten minutes is not ten minutes old, and the two are read
// differently at speed -- so the span is a duration, never an age.
func TestHumanSpanIsNotAnAge(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{45 * time.Second, "45s"},
		{10*time.Minute + 25*time.Second, "10m25s"},
		{21*time.Hour + 45*time.Minute, "21h45m"},
		{50 * time.Hour, "2d2h"},
	} {
		got := humanSpan(tc.d)
		if got != tc.want {
			t.Errorf(
				"humanSpan(%s) = %q, want %q",
				tc.d,
				got,
				tc.want,
			)
		}
		if strings.Contains(got, "ago") {
			t.Errorf(
				"humanSpan(%s) = %q -- a span is not an age",
				tc.d,
				got,
			)
		}
	}
}

// The file the pane reads is found, not computed.
//
// It used to be "containers-" + time.Now().Format(...) -- the local date --
// while the collector names its files in UTC.
//
// Between the two midnights puffin read yesterday's file and showed a frozen
// screen with no sign of it: at 19:23 PDT the newest line on screen was
// 23:59:59Z, two and a half hours stale, and it would have stayed that way
// until local midnight.
//
// A timezone-correct computation would have fixed the symptom and kept the
// assumption; asking the pod which file is newest has no assumption in it.
func TestLogReadScriptFindsTheNewestFile(t *testing.T) {
	sh := logReadScript("/fleet-logs", "", 1000)
	if strings.Contains(sh, time.Now().Format("2006.01.02")) {
		t.Fatal(
			"the script still computes a date from this " +
				"machine's clock",
		)
	}
	if !strings.Contains(sh, "ls -1 /fleet-logs/containers-*.jsonl") {
		t.Error("the script does not look for the collector's files")
	}
	if !strings.Contains(sh, "| sort | tail -1") {
		t.Error("the newest file is not chosen deterministically")
	}
	if !strings.Contains(sh, `tail -n 1000 "$f"`) {
		t.Error("the read is not bounded in the pod")
	}
	// it names what it read, so the header says where the lines came from
	// rather than where they were assumed to be
	if !strings.Contains(sh, `echo "FILE:$f"`) {
		t.Error("the script does not say which file it used")
	}
	// and an empty directory is a stated answer, not an empty screen
	if !strings.Contains(sh, "NOFILES") {
		t.Error("no files at all is not reported")
	}
}

// A filter still runs in the pod, and is still quoted: a search box that can
// run commands is not a search box.
func TestLogReadScriptQuotesTheFilter(t *testing.T) {
	sh := logReadScript("/fleet-logs", "it's; rm -rf /", 200)
	if !strings.Contains(sh, `grep -F -- 'it'\''s; rm -rf /' "$f"`) {
		t.Fatalf("the filter is not quoted: %s", sh)
	}
	if !strings.Contains(sh, "| tail -n 200") {
		t.Error("the filtered read is not bounded")
	}
	// the filename is still a shell variable, never interpolated text
	if strings.Contains(sh, "containers-2026") {
		t.Error("a filename was baked into the script")
	}
}
