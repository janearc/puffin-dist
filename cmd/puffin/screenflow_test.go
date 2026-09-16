package main

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The screens above the panes: the contract browser, the request composer,
// the pager, and the split. These are the paths a person walks to act on a
// service, and until now the loop was tested at the roster and left the rest
// to be discovered in use.

// apiModel is a model sitting on the detail screen with a real descriptor
// parsed from a real FileDescriptorSet -- the same shape a service serves.
func apiModel(t *testing.T) model {
	t.Helper()
	api, err := ParseAPI(buildFDS(t))
	if err != nil {
		t.Fatal(err)
	}
	m := baseModel()
	m.scr, m.api, m.apiFor = screenDetail, api, "x"
	return m
}

// enter on a method opens the composer with a skeleton of the request, so
// the first thing on screen is a valid message rather than an empty box.
func TestInvokeComposerOpensWithASkeleton(t *testing.T) {
	m := apiModel(t)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if m.scr != screenInvoke {
		t.Fatal("enter on a method did not open the composer")
	}
	if m.invokeMethod.Name != "Ping" {
		t.Fatalf("composing %q", m.invokeMethod.Name)
	}
	if m.editor.Value() == "" {
		t.Error("the composer opened with an empty request")
	}
	out := m.View()
	if !strings.Contains(out, "Ping") {
		t.Error("the composer does not say what it will call")
	}
	if !strings.Contains(out, "PingRequest") ||
		!strings.Contains(out, "PingResponse") {
		t.Error(
			"the composer does not say what goes in and what " +
				"comes back",
		)
	}

	// the textarea eats typing; only ctrl+s and esc belong to puffin
	next, _ = m.Update(key('q'))
	m = next.(model)
	if m.scr != screenInvoke {
		t.Fatal("q closed the composer instead of being typed into it")
	}
	// esc goes back to the contract, and drops any previous result
	m.invokeErr = "an old error"
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if m.scr != screenDetail {
		t.Fatal("esc did not return to the contract")
	}
	if m.invokeErr != "" {
		t.Error("a stale error survived leaving the screen")
	}
}

// ctrl+s fires, and while it is in flight the screen says so rather than
// looking like nothing happened.
func TestInvokeFires(t *testing.T) {
	m := apiModel(t)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = next.(model)
	if cmd == nil {
		t.Fatal("ctrl+s sent nothing")
	}
	if !m.firing {
		t.Error("the screen does not say it is firing")
	}

	// a small reply stays inline
	next, _ = m.Update(
		fired{res: InvokeResult{Status: 200, Body: "{\"ok\":true}"}},
	)
	m = next.(model)
	if m.scr != screenInvoke {
		t.Fatal("a one-line reply opened the pager")
	}
	if m.firing {
		t.Error("the screen still claims to be firing")
	}
	if m.result == nil || !strings.Contains(m.View(), "ok") {
		t.Error("the reply is not on screen")
	}

	// an error is reported rather than left blank
	next, _ = m.Update(fired{err: errFake})
	m = next.(model)
	if m.invokeErr == "" {
		t.Fatal("a failed call was silent")
	}
	if !strings.Contains(m.View(), "colima exited 1") {
		t.Error("the failure is not on screen")
	}
}

// A large packet opens the pager, highlighted, and esc hands back to the
// composer with the result still there.
func TestBigRepliesOpenThePager(t *testing.T) {
	m := apiModel(t)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)

	body := "{\n" + strings.Repeat("  \"key\": \"value\",\n", 60) + "}"
	next, _ = m.Update(fired{res: InvokeResult{Status: 200, Body: body}})
	m = next.(model)
	if m.scr != screenPager {
		t.Fatal("a large reply stayed inline")
	}
	if out := m.View(); out == "" {
		t.Fatal("the pager rendered nothing")
	}
	// it scrolls
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	m = next.(model)
	if m.pager.YOffset == 0 {
		t.Error("the pager did not scroll")
	}
	// and esc goes back where it came from, with the result still inline
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if m.scr != screenInvoke {
		t.Fatalf("esc left the pager to screen %v", m.scr)
	}
	if m.result == nil {
		t.Error("the result was dropped on the way back")
	}
}

// methodAt walks the services in order: an index past the end is not a
// method, and asking before the contract arrived is not an error.
func TestMethodAt(t *testing.T) {
	m := apiModel(t)
	if _, meth, ok := m.methodAt(0); !ok || meth.Name != "Ping" {
		t.Fatalf("method 0: %+v %v", meth, ok)
	}
	if _, _, ok := m.methodAt(9); ok {
		t.Error("an index past the end returned a method")
	}
	var empty model
	if _, _, ok := empty.methodAt(0); ok {
		t.Error("a model with no contract returned a method")
	}
}

// The wheel scrolls the legacy screens too. It did not, and on the contract
// screen it silently did nothing -- which reads as a broken scroll rather
// than an unimplemented one.
func TestWheelScrollsTheOlderScreens(t *testing.T) {
	wheel := func(m model, up bool) model {
		btn := tea.MouseButtonWheelDown
		if up {
			btn = tea.MouseButtonWheelUp
		}
		next, _ := m.Update(
			tea.MouseMsg{Button: btn, Action: tea.MouseActionPress},
		)
		return next.(model)
	}

	// the roster
	m := baseModel()
	m.enclave = Enclave{
		Services: []Service{{Name: "a"}, {Name: "b"}, {Name: "c"}},
	}
	m = wheel(m, false)
	if m.cursor != 1 {
		t.Errorf("roster wheel: cursor %d", m.cursor)
	}
	m = wheel(m, true)
	if m.cursor != 0 {
		t.Errorf("roster wheel up: cursor %d", m.cursor)
	}

	// the contract screen
	d := apiModel(t)
	before := d.methodCursor
	d = wheel(d, false)
	if d.methodCursor == before && d.methodCount() > 1 {
		t.Error("the contract screen did not scroll")
	}

	// the flags screen
	f := baseModel()
	f.scr = screenFlags
	f.flags = []FlagRow{{Key: "a"}, {Key: "b"}, {Key: "c"}}
	f = wheel(f, false)
	if f.flagCursor != 1 {
		t.Errorf("flags wheel: cursor %d", f.flagCursor)
	}

	// the maps browser
	mp := baseModel()
	mp.scr = screenMaps
	mp.mapsListing = &MapListing{
		Entries: []MapEntry{{Name: "a"}, {Name: "b"}},
	}
	mp = wheel(mp, false)
	if mp.mapsCursor != 1 {
		t.Errorf("maps wheel: cursor %d", mp.mapsCursor)
	}

	// the kube screen
	k := baseModel()
	k.scr = screenKube
	k.kube = KubeView{Pods: []KubePod{
		{
			Name:      "a",
			Namespace: "local",
			Phase:     "Running",
			Ready:     "1/1",
			AllReady:  true,
		},
		{
			Name:      "b",
			Namespace: "local",
			Phase:     "Running",
			Ready:     "1/1",
			AllReady:  true,
		}}}
	k = wheel(k, false)
	if k.kubeCursor < 0 {
		t.Errorf("kube wheel: cursor %d", k.kubeCursor)
	}

	// a wheel with no list under it must not move anything off the end
	e := baseModel()
	e.enclave = Enclave{}
	e = wheel(e, false)
	if e.cursor != 0 {
		t.Errorf("an empty roster scrolled to %d", e.cursor)
	}
}

// Mouse reporting is a trade, not a feature: while puffin holds the mouse,
// selecting text to copy stops working, and it stops silently.
func TestMouseIsATradeYouCanTakeBack(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := baseModel()
	before := m.mouse
	next, cmd := m.Update(key('M'))
	m = next.(model)
	if m.mouse == before {
		t.Fatal("M did not change the mouse setting")
	}
	if cmd == nil {
		t.Error("the terminal was not told about the change")
	}
	// and the screen says which way the trade is set
	if !strings.Contains(strings.ToLower(m.View()), "mouse") {
		t.Error("the roster does not say how the mouse is set")
	}
	next, _ = m.Update(key('M'))
	if next.(model).mouse != before {
		t.Fatal("M did not toggle back")
	}
}

// A change that takes a service down gets one deliberate keystroke, and
// anything that is not yes is no.
func TestKubeConfirmationTakesOnlyYes(t *testing.T) {
	m := baseModel()
	m.scr = screenKube
	m.kubeAsk = &kubeAction{
		verb: "restart",
		pod:  "flipr-1",
		w: KubeWorkload{
			Kind:      "Deployment",
			Name:      "flipr",
			Namespace: "flipr",
			Replicas:  1,
			Scalable:  true,
		},
	}
	next, cmd := m.Update(key('n'))
	m = next.(model)
	if m.kubeAsk != nil {
		t.Fatal("n left the question standing")
	}
	if cmd != nil {
		t.Fatal("n ran something")
	}
	if m.kubeNote != "cancelled" {
		t.Errorf("note %q", m.kubeNote)
	}
	// y is the one key that acts
	m.kubeAsk = &kubeAction{
		verb: "restart",
		pod:  "flipr-1",
		w: KubeWorkload{
			Kind:      "Deployment",
			Name:      "flipr",
			Namespace: "flipr",
			Replicas:  1,
			Scalable:  true,
		},
	}
	_, cmd = m.Update(key('y'))
	if cmd == nil {
		t.Fatal("y did not act")
	}
}

// The split: | duplicates the focused pane, a pane key replaces the focused
// side, ctrl+w moves focus, and q closes one side rather than both.
func TestSplitFocusAndReplace(t *testing.T) {
	m := baseModel()
	next, _ := m.Update(key('l')) // open the logs pane
	m = next.(model)
	if m.scr != screenPane || m.openPane == nil {
		t.Fatal("l did not open a pane")
	}

	next, _ = m.Update(key('|'))
	m = next.(model)
	if m.rightPane == nil {
		t.Fatal("| did not split")
	}
	if m.focus != right {
		t.Error("the split did not focus the new side")
	}
	if m.rightPane.Title() != m.openPane.Title() {
		t.Error("the split did not duplicate the focused pane")
	}

	// a pane key replaces the focused side, which is how logs|bus is
	// reached
	next, _ = m.Update(key('b'))
	m = next.(model)
	if m.rightPane.Title() != "bus" {
		t.Fatalf("right is %q", m.rightPane.Title())
	}
	if m.openPane.Title() != "logs" {
		t.Fatalf("the left side changed to %q", m.openPane.Title())
	}

	// ctrl+w moves focus back
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlW})
	m = next.(model)
	if m.focus != left {
		t.Error("ctrl+w did not move focus")
	}
	if out := m.View(); out == "" {
		t.Fatal("the split rendered nothing")
	}

	// q closes the focused side only: closing both throws away more than
	// was asked for
	next, _ = m.Update(key('q'))
	m = next.(model)
	if m.rightPane != nil {
		t.Fatal("q closed both sides")
	}
	if m.scr != screenPane {
		t.Fatal("q left the pane screen entirely")
	}
	if m.openPane.Title() != "bus" {
		t.Fatalf("closing the left side left %q", m.openPane.Title())
	}
	// and again, with one pane, it hands back to the roster
	next, _ = m.Update(key('q'))
	m = next.(model)
	if m.scr != screenRoster || m.openPane != nil {
		t.Fatal("q did not return to the roster")
	}
}

// r re-reads the focused pane, and the theme keys work everywhere rather
// than only on the front page.
func TestPaneScreenGlobalKeys(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := baseModel()
	next, _ := m.Update(key('l'))
	m = next.(model)

	_, cmd := m.Update(key('r'))
	if cmd == nil {
		t.Error("r did not re-read the pane")
	}
	before := m.styles.Theme
	next, _ = m.Update(key('t'))
	m = next.(model)
	if m.styles.Theme == before {
		t.Error("t did not change the theme from inside a pane")
	}
	if _, cmd := m.Update(key('T')); cmd == nil {
		t.Error("T did not ask dodo for themes")
	}
}

// A pane that is capturing input owns every key except the one that always
// belongs to the terminal.
func TestACapturingPaneKeepsItsKeystrokes(t *testing.T) {
	m := baseModel()
	next, _ := m.Update(key('l'))
	m = next.(model)
	next, _ = m.Update(key('/')) // the logs pane's search box
	m = next.(model)

	next, _ = m.Update(key('q'))
	m = next.(model)
	if m.scr != screenPane {
		t.Fatal("q closed the pane while the search box was open")
	}
	lp, ok := m.openPane.(*logsPane)
	if !ok {
		t.Fatalf("pane is %T", m.openPane)
	}
	if lp.query != "q" {
		t.Fatalf("the keystroke did not reach the box: %q", lp.query)
	}
	// ctrl+c still quits, because it always belongs to the terminal
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC}); cmd == nil {
		t.Error("ctrl+c did not quit from a capturing pane")
	}
}

// A tick for a pane that is no longer open is dropped, and by not
// rescheduling it the clock stops on its own.
func TestPaneTicksStopWhenThePaneCloses(t *testing.T) {
	m := baseModel()
	next, _ := m.Update(key('l'))
	m = next.(model)
	title := m.openPane.Title()

	_, cmd := m.Update(paneTick{pane: title})
	if cmd == nil {
		t.Fatal("an open pane's tick did not refresh it")
	}
	next, _ = m.Update(key('q'))
	m = next.(model)
	if _, cmd := m.Update(paneTick{pane: title}); cmd != nil {
		t.Fatal("a closed pane kept ticking")
	}
}

// A pane's reply must reach BOTH sides: two logs panes each asked for their
// own read, and a reply that only reached the left one would leave the right
// permanently empty.
func TestPaneRepliesReachBothSides(t *testing.T) {
	m := baseModel()
	next, _ := m.Update(key('l'))
	m = next.(model)
	next, _ = m.Update(key('|'))
	m = next.(model)

	v := LogView{Lines: []LogLine{{Service: "flipr", Text: "hello"}}}
	next, _ = m.Update(fleetLogsFetched{v})
	m = next.(model)
	for name, p := range map[string]pane{
		"left":  m.openPane,
		"right": m.rightPane,
	} {
		lp, ok := p.(*logsPane)
		if !ok {
			t.Fatalf("%s is %T", name, p)
		}
		if len(lp.v.Lines) != 1 {
			t.Errorf("%s pane got %d lines", name, len(lp.v.Lines))
		}
	}
}

// The message handlers: every reply that reaches the loop is a state change,
// and most of them are the difference between a screen that says something
// true and one that keeps showing what it had before.
func TestLoopMessagesLandWhereTheyBelong(t *testing.T) {
	m := baseModel()

	// a window resize
	next, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 60})
	m = next.(model)
	if m.width != 200 || m.height != 60 {
		t.Fatalf("size %dx%d", m.width, m.height)
	}

	// discovery, and a cursor that cannot survive a shorter roster
	m.cursor = 5
	next, _ = m.Update(
		discovered{e: Enclave{Services: []Service{{Name: "flipr"}}}},
	)
	m = next.(model)
	if m.loading {
		t.Error("the roster still claims to be loading")
	}
	if m.cursor != 0 {
		t.Errorf("cursor %d after a shorter roster", m.cursor)
	}

	// a contract that arrives for a service nobody is looking at any more
	// must not be shown under the wrong name
	m.apiFor = "flipr"
	api, err := ParseAPI(buildFDS(t))
	if err != nil {
		t.Fatal(err)
	}
	next, _ = m.Update(apiFetched{name: "somebody-else", api: api})
	m = next.(model)
	if m.api != nil {
		t.Fatal("a late contract was shown under the wrong service")
	}
	next, _ = m.Update(apiFetched{name: "flipr", api: api, err: errFake})
	m = next.(model)
	if m.api == nil || m.apiErr == "" {
		t.Fatal("the contract and its error did not both land")
	}

	// flags, with the cursor clamped
	m.flagCursor = 9
	next, _ = m.Update(flagsFetched{rows: []FlagRow{{Key: "a"}}})
	m = next.(model)
	if m.flagCursor != 0 {
		t.Errorf("flag cursor %d", m.flagCursor)
	}
	next, _ = m.Update(flagsFetched{err: errFake})
	m = next.(model)
	if m.flagsErr == "" {
		t.Error("a flags failure was silent")
	}

	// a listing replaces whatever attributes were on screen: they belonged
	// to a file in the directory you just left
	m.mapsAttrs = &MapAttrs{}
	next, _ = m.Update(listingFetched{l: &MapListing{Path: "/"}})
	m = next.(model)
	if m.mapsAttrs != nil {
		t.Error("stale attributes survived a new listing")
	}
	next, _ = m.Update(attrsFetched{a: MapAttrs{}})
	m = next.(model)
	if m.mapsAttrs == nil {
		t.Error("attributes did not land")
	}
	next, _ = m.Update(attrsFetched{err: errFake})
	m = next.(model)
	if m.mapsErr == "" {
		t.Error("an attributes failure was silent")
	}
}

// flipr's refusal is shown verbatim: a flag that would not flip and a screen
// that says "flipped" is the worst pair of facts this tool could hold.
func TestFlagFlipOutcomes(t *testing.T) {
	m := baseModel()
	m.scr, m.editing = screenFlags, true

	next, _ := m.Update(flagFlipped{err: errFake})
	m = next.(model)
	if m.flagNote == "" || !m.editing {
		t.Error("a failed flip closed the editor or said nothing")
	}
	next, _ = m.Update(
		flagFlipped{
			res: InvokeResult{
				Status: 422,
				Body:   "reason is required",
			},
		},
	)
	m = next.(model)
	if m.flagNote != "reason is required" {
		t.Errorf(
			"flipr's refusal was not shown verbatim: %q",
			m.flagNote,
		)
	}
	if !m.editing {
		t.Error("a refused flip closed the editor")
	}
	// a flip that worked closes the editor and re-reads: the store is the
	// truth
	next, cmd := m.Update(flagFlipped{res: InvokeResult{Status: 200}})
	m = next.(model)
	if m.editing {
		t.Error("a successful flip left the editor open")
	}
	if cmd == nil {
		t.Error(
			"the screen believed itself instead of re-reading " +
				"flipr",
		)
	}
}

// The cluster screen's replies: a resolve names the workload that actually
// moves, and an action that worked re-reads rather than believing itself.
func TestKubeRepliesLand(t *testing.T) {
	m := baseModel()
	m.scr, m.kubeBusy = screenKube, true

	next, _ := m.Update(kubeResolved{err: errFake})
	m = next.(model)
	if m.kubeBusy || m.kube.Err == "" || m.kubeAsk != nil {
		t.Fatal(
			"a failed resolve left the screen busy, silent, or " +
				"asking",
		)
	}
	next, _ = m.Update(kubeResolved{verb: "restart", pod: "flipr-1",
		w: KubeWorkload{
			Kind:      "Deployment",
			Name:      "flipr",
			Namespace: "flipr",
		}})
	m = next.(model)
	if m.kubeAsk == nil || m.kubeAsk.w.Name != "flipr" {
		t.Fatal(
			"the confirmation does not name the workload that " +
				"moves",
		)
	}

	next, cmd := m.Update(kubeActed{note: "restarted deployment/flipr"})
	m = next.(model)
	if m.kubeAsk != nil || m.kubeBusy {
		t.Error("the screen stayed busy or kept asking after acting")
	}
	if m.kubeNote == "" {
		t.Error("what happened was not reported")
	}
	if cmd == nil {
		t.Error("the screen did not re-read after changing the cluster")
	}
	next, _ = m.Update(kubeActed{err: errFake})
	m = next.(model)
	if m.kube.Err == "" {
		t.Error("a failed action was silent")
	}

	// pod logs open the pager, and esc returns to the cluster screen
	next, _ = m.Update(
		logsFetched{body: strings.Repeat("a log line\n", 50)},
	)
	m = next.(model)
	if m.scr != screenPager {
		t.Fatal("logs did not open the pager")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if got := next.(model).scr; got != screenKube {
		t.Fatalf(
			"esc left the pager to %v, not the cluster screen",
			got,
		)
	}
	next, _ = m.Update(logsFetched{err: errFake})
	if next.(model).kube.Err == "" {
		t.Error("a failed log read was silent")
	}
}

// The cluster screen is a file tree: space folds, right only opens, left
// climbs, and up from the top is out.
func TestKubeFoldGrammar(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := baseModel()
	m.scr = screenKube
	m.kube = KubeView{Context: "k3d-local", Pods: []KubePod{
		{
			Namespace: "local",
			Name:      "flipr-1",
			Phase:     "Running",
			Ready:     "1/1",
			AllReady:  true,
		},
		{
			Namespace: "local",
			Name:      "dodo-1",
			Phase:     "Running",
			Ready:     "1/1",
			AllReady:  true,
		},
		{
			Namespace: "kube-system",
			Name:      "traefik-1",
			Phase:     "Running",
			Ready:     "1/1",
			AllReady:  true,
		},
	}}
	next, _ := m.Update(kubeFetched{v: m.kube})
	m = next.(model)
	rows := m.kubeVisible()
	if len(rows) == 0 {
		t.Fatal("the cluster screen has no rows")
	}

	// space on a pod has nothing to fold, and says so rather than looking
	// broken -- which is exactly what happened, because the screen opens
	// with the cursor on a pod
	for i, r := range rows {
		if !r.Foldable() {
			m.kubeCursor = i
			break
		}
	}
	next, _ = m.Update(key(' '))
	m = next.(model)
	if !strings.Contains(m.kubeNote, "that row is a pod") {
		t.Errorf("space on a pod said %q", m.kubeNote)
	}

	// space on a fold toggles it, and the row count changes
	for i, r := range m.kubeVisible() {
		if r.Foldable() {
			m.kubeCursor = i
			break
		}
	}
	before := len(m.kubeVisible())
	next, _ = m.Update(key(' '))
	m = next.(model)
	if len(m.kubeVisible()) >= before {
		t.Error("folding a namespace did not hide its pods")
	}
	// right only ever opens
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = next.(model)
	if len(m.kubeVisible()) != before {
		t.Error("right did not open the fold")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = next.(model)
	if len(m.kubeVisible()) != before {
		t.Error("right closed a fold that was already open")
	}
	// enter toggles too
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if len(m.kubeVisible()) >= before {
		t.Error("enter did not fold")
	}
	// left on a collapsed top-level namespace means out
	next, _ = m.Update(key('h'))
	m = next.(model)
	if m.scr != screenRoster {
		t.Fatalf(
			"left from a collapsed top row went to %v, not out",
			m.scr,
		)
	}
}

// l on the cluster screen reads a pod's logs, and refuses politely when the
// cursor is on a fold rather than a pod.
func TestKubeLogsAndLifecycleKeysNeedAPod(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := baseModel()
	m.scr = screenKube
	m.kube = KubeView{Context: "k3d-local", Pods: []KubePod{
		{
			Namespace: "local",
			Name:      "flipr-1",
			Phase:     "Running",
			Ready:     "1/1",
			AllReady:  true,
		}}}
	next, _ := m.Update(kubeFetched{v: m.kube})
	m = next.(model)

	// put the cursor on a fold row
	for i, r := range m.kubeVisible() {
		if r.Foldable() {
			m.kubeCursor = i
			break
		}
	}
	for _, k := range []rune{'l', 's', 'x', 'R'} {
		next, cmd := m.Update(key(k))
		mm := next.(model)
		if cmd != nil {
			t.Errorf("%c acted on a fold", k)
		}
		if !strings.Contains(mm.kubeNote, "that is a fold") {
			t.Errorf("%c said %q", k, mm.kubeNote)
		}
	}
	// and on a pod, each of them goes and asks
	for i, r := range m.kubeVisible() {
		if !r.Foldable() {
			m.kubeCursor = i
			break
		}
	}
	for _, k := range []rune{'l', 's', 'x', 'R'} {
		if _, cmd := m.Update(key(k)); cmd == nil {
			t.Errorf("%c on a pod did nothing", k)
		}
	}
}

// The flag editor: tab moves between value and reason, and flipr will refuse
// a flip with no reason -- so puffin says so before spending the round trip.
func TestFlagEditorFocusAndRefusal(t *testing.T) {
	m := baseModel()
	m.scr = screenFlags
	m.flags = []FlagRow{{Key: "puffin.demo", Kind: "bool", Value: "false"}}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if !m.editing {
		t.Fatal("enter did not open the editor")
	}
	// a bool arrives pre-toggled: the edit you almost certainly want
	if m.editValue.Value() != "true" {
		t.Errorf("the editor opened at %q", m.editValue.Value())
	}
	// the reason box has the focus, because it is the part flipr requires
	if m.editFocus != 1 {
		t.Errorf("focus %d", m.editFocus)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(model)
	if m.editFocus != 0 {
		t.Error("tab did not move to the value")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(model)
	if m.editFocus != 1 {
		t.Error("tab did not move back")
	}
	// enter with no reason does not spend a round trip
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if cmd != nil {
		t.Fatal("a reasonless flip was sent")
	}
	if !strings.Contains(m.flagNote, "refuse a flip with no reason") {
		t.Errorf("note %q", m.flagNote)
	}
	// with a reason it goes
	for _, r := range "because" {
		next, _ = m.Update(key(r))
		m = next.(model)
	}
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter}); cmd == nil {
		t.Fatal("a flip with a reason was not sent")
	}
	// esc abandons the edit
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if m.editing {
		t.Fatal("esc did not close the editor")
	}
}

// The maps browser is a hierarchy: enter descends, left climbs, and left at
// the top leaves.
func TestMapsNavigation(t *testing.T) {
	m := baseModel()
	m.scr, m.mapsBase = screenMaps, "http://kingfisher.test"
	m.mapsListing = &MapListing{
		Path:  "/",
		Mount: "(mounts)",
		Entries: []MapEntry{
			{Name: "usgs/", Dir: true, Bytes: -1},
			{Name: "readme.txt", Bytes: 120},
		},
	}
	// enter on a directory descends and asks for the listing
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if cmd == nil || len(m.mapsStack) != 1 {
		t.Fatalf("enter on a directory: stack %v", m.mapsStack)
	}
	// left climbs back, and at the mount table it asks for the mounts again
	next, cmd = m.Update(key('h'))
	m = next.(model)
	if len(m.mapsStack) != 0 || cmd == nil {
		t.Fatalf("left: stack %v", m.mapsStack)
	}
	// left at the top is out
	next, _ = m.Update(key('h'))
	m = next.(model)
	if m.scr != screenRoster {
		t.Fatal("left at the mount table did not leave")
	}
	// enter on a FILE asks for its attributes rather than descending
	m.scr = screenMaps
	m.mapsCursor = 1
	next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if cmd == nil {
		t.Fatal("enter on a file did nothing")
	}
	if len(m.mapsStack) != 0 {
		t.Errorf("enter on a file descended into it: %v", m.mapsStack)
	}
}

// The splash's clock stops when the bird flies: a frame that keeps ticking
// behind another screen is work nobody sees.
func TestSplashClockStops(t *testing.T) {
	m := baseModel()
	m.scr = screenSplash
	next, cmd := m.Update(splashTick{})
	m = next.(model)
	if cmd == nil || m.splashFrame == 0 {
		t.Fatal("the splash did not animate")
	}
	m.scr = screenRoster
	if _, cmd := m.Update(splashTick{}); cmd != nil {
		t.Fatal("the splash kept ticking after the bird flew")
	}
	// the overlay asks for its own cadence and gets it -- and gets nothing
	// when there is no bird, which is a first-class answer: a sprite
	// animating in the corner while somebody reads a stack trace at three
	// in the morning is a sprite they kill the tool over
	t.Setenv("PUFFIN_BIRD", "0")
	if _, cmd := m.Update(overlayTick{}); cmd != nil {
		t.Fatal("a disabled overlay kept asking to be woken")
	}
	t.Setenv("PUFFIN_BIRD", "1")
	SetOverlay(tickingOverlay{})
	t.Cleanup(func() { SetOverlay(nil) })
	if _, cmd := m.Update(overlayTick{}); cmd == nil {
		t.Fatal(
			"an enabled overlay was not rescheduled, so it only " +
				"animates when something else redraws",
		)
	}
}

// tickingOverlay is the smallest thing that satisfies the seam: it asks for
// a cadence and draws nothing.
type tickingOverlay struct{}

// The stub draws nothing and only ticks, so a test can assert that the
// clock keeps running without the sprite changing the frame.
func (tickingOverlay) Draw(frame string, _, _ int) string { return frame }

// The interval is the point of this stub.
func (tickingOverlay) Tick() time.Duration { return time.Second }

// Events are ignored; this stub exists for its clock.
func (tickingOverlay) Handle(Event) {}
