package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// puffin -- a tui for the enclave: every service that publishes an api.
// which is to say, all of them.
//
// v0 is the operator loop: discover the enclave, see citizenship and health
// at a glance, browse any member's API from its own descriptor. Flags,
// scripting, macros and the theme endpoint are the next tranches;
// DESIGN.md carries the map.

// screen names which view the model is showing.
type screen int

const (
	screenSplash screen = iota
	screenRoster
	screenDetail
	screenInvoke
	screenPager
	screenFlags
	screenMaps
	screenKube
	screenPane // anything built behind the pane seam
)

// model is the whole app state, bubbletea-style: one struct, one update.
type model struct {
	styles Styles
	// theme indexes themes; t cycles it. The choice is NOT persisted:
	// puffin holds no state between invocations, the same reason it holds
	// no roster. PUFFIN_THEME is how a choice outlives a session.
	theme int
	// themes is the LIVE list: baked at startup, replaced by dodo's when
	// the pull lands. Held on the model rather than read from a package
	// function so there is one list, and the index always indexes it.
	themes    []Theme
	themeSrc  string // dodo | cache | baked
	themeWant string // what PUFFIN_THEME asked for, so a pull can honour it
	themeNote string
	// companionNote says who was just chosen. The corner is eight rows and
	// two of the four characters are the same silhouette at that size, so
	// the change needs a word as well as a redraw.
	companionNote string
	// mouse is whether puffin is holding the mouse. See toggleMouse.
	mouse   bool
	width   int
	height  int
	scr     screen
	domain  string
	enclave Enclave
	// furnishings are what the cluster runs that the enclave does not
	// claim -- postgres, prometheus, surrealdb. See furniture.go.
	furnishings []Furnishing
	furnSort    int
	loading     bool
	cursor      int
	// first visible body row on the front page. The roster outgrew the
	// terminal the moment the furnishings arrived, and the frame clamp
	// answers an overlong screen by dropping the help off the bottom.
	rosterOffset int
	api          *API
	apiErr       string
	apiFor       string
	// the act-on-it half: which method is under the cursor, the editable
	// request, and what came back from firing it
	methodCursor int
	// first visible contract line: big APIs outgrow the screen
	detailOffset int
	editor       textarea.Model
	invokeSvc    APIService
	invokeMethod APIMethod
	schemaDocs   []FieldDoc
	result       *InvokeResult
	invokeErr    string
	firing       bool
	pager        viewport.Model
	// the flipr screen
	flags      []FlagRow
	flagCursor int
	flagsErr   string
	editing    bool
	editValue  textarea.Model
	editReason textarea.Model
	editFocus  int // 0 value, 1 reason
	flagNote   string
	// the maps browser
	mapsBase    string
	mapsStack   []string // path segments from the mount down
	mapsListing *MapListing
	mapsCursor  int
	mapsAttrs   *MapAttrs
	mapsErr     string
	// the kube screen
	kube       KubeView
	kubeCursor int // indexes visible rows, not pods
	kubeNote   string
	kubeBusy   bool
	kubeAsk    *kubeAction // a disruptive change awaiting y/n
	// clusters is what k3d says exists, so the cluster screen can name the
	// ones it is not showing rather than looking like an empty estate
	clusters []Cluster
	kubeFold map[string]bool
	// first visible row: the list is taller than the terminal
	kubeOffset int
	// the panes: self-contained screens behind the pane seam. The model
	// holds the registry and which one is open, and nothing else about
	// them -- that is the entire point of the seam.
	paneReg  map[string]pane
	openPane pane
	// the split is OPT-IN and off by default: a full-width pane is the
	// right shape most of the time, and a tool that halves your screen
	// because it can is one you fight
	rightPane pane
	focus     side
	// the pager is shared, so it remembers where it was opened from
	pagerFrom screen
	// the splash's 90s heart: a frame counter driven by ticks
	splashFrame int
}

// flagsFetched, listingFetched, attrsFetched and kubeFetched carry the
// operator screens' data into the loop.
type flagsFetched struct {
	rows []FlagRow
	err  error
}
type listingFetched struct {
	l   *MapListing
	err error
}
type attrsFetched struct {
	a   MapAttrs
	err error
}
type kubeFetched struct{ v KubeView }

// kubeAction is a start, stop or restart that has been resolved to a
// workload and is waiting for the operator to confirm it.
type kubeAction struct {
	verb string // start, stop, restart
	pod  string
	w    KubeWorkload
}

// kubeResolved carries a pod's controller back so the confirmation can name
// what will actually change -- "stop athlete-6c9998d74b-rflj9" is a lie,
// deployment/athlete is what moves.
type kubeResolved struct {
	verb string
	pod  string
	w    KubeWorkload
	err  error
}

// kubeActed carries a finished lifecycle change back into the loop.
type kubeActed struct {
	note string
	err  error
}

// logsFetched carries a pod's logs back into the loop.
type logsFetched struct {
	pod  string
	body string
	err  error
}
type flagFlipped struct {
	res InvokeResult
	err error
}

// splashTick advances the opening animation.
type splashTick struct{}

// tickSplash schedules the next animation frame. Tasteful means slow: the
// 90s were 300ms.
func tickSplash() tea.Cmd {
	// eight frames a second. It was three, which was fine for a typewriter
	// and useless for a pan: four drawings at three a second is a slideshow
	// of four birds, not one bird turning. The eye reads the change between
	// frames.
	return tea.Tick(
		125*time.Millisecond,
		func(time.Time) tea.Msg { return splashTick{} },
	)
}

// discovered carries a finished discovery into the update loop.
type discovered struct{ e Enclave }

// fired carries an invocation result back into the loop.
type fired struct {
	res InvokeResult
	err error
}

// invokeCmd fires the edited request off the ui goroutine.
func invokeCmd(base, svc, method, body string) tea.Cmd {
	return func() tea.Msg {
		res, err := Invoke(base, svc, method, body)
		return fired{res, err}
	}
}

// apiFetched carries a finished descriptor fetch.
type apiFetched struct {
	name string
	api  *API
	err  error
}

// operator-screen commands, all off the ui goroutine.

// flagsCmd fetches the flag store for the flags screen, resolved to each
// service's newest namespace -- the same set the shell door edits, because a
// row you can point at is a row you can flip.
func flagsCmd(domain string) tea.Cmd {
	return func() tea.Msg {
		rows, err := fetchFlags(fmt.Sprintf("http://flipr.%s", domain))
		return flagsFetched{newestOnly(rows), err}
	}
}

// listingCmd fetches one directory index for the maps browser.
func listingCmd(base, path string) tea.Cmd {
	return func() tea.Msg {
		l, err := fetchListing(base, path)
		return listingFetched{l, err}
	}
}

// attrsCmd runs the availability assessment: one HEAD, on request.
func attrsCmd(base, path string) tea.Cmd {
	return func() tea.Msg {
		a, err := headFile(base, path)
		return attrsFetched{a, err}
	}
}

// kubeCmd reads the cluster through kubectl, context always explicit.
func kubeCmd() tea.Cmd {
	return func() tea.Msg { return kubeFetched{fetchKube(kubeContext())} }
}

// logsCmd reads a pod's logs off the ui goroutine. 500 lines is a screenful
// in the pager and a small enough read to feel instant; -f belongs at the
// shell, where a stream can be interrupted.
func logsCmd(ns, pod string) tea.Cmd {
	return func() tea.Msg {
		body, err := PodLogs(kubeContext(), ns, pod, 500, false)
		return logsFetched{pod, body, err}
	}
}

// resolveCmd walks the pod under the cursor up to its controller.
func resolveCmd(verb, ns, pod string) tea.Cmd {
	return func() tea.Msg {
		ctx := kubeContext()
		// the guard first, so a foreign context is reported as the
		// reason rather than surfacing later as a confusing kubectl
		// refusal
		if err := guardContext(ctx); err != nil {
			return kubeResolved{verb, pod, KubeWorkload{}, err}
		}
		w, err := resolveWorkload(ctx, ns, pod)
		return kubeResolved{verb, pod, w, err}
	}
}

// actCmd applies a confirmed lifecycle change and reports it in words.
func actCmd(a kubeAction) tea.Cmd {
	return func() tea.Msg {
		ctx := kubeContext()
		var err error
		switch a.verb {
		case "stop":
			err = StopWorkload(ctx, a.w)
		case "start":
			err = StartWorkload(ctx, a.w)
		case "restart":
			err = RestartWorkload(ctx, a.w)
		}
		if err != nil {
			return kubeActed{"", err}
		}
		return kubeActed{pastTense(a.verb) + " " + a.w.Target(), nil}
	}
}

// pastTense reports a finished action in the words an operator would use.
func pastTense(verb string) string {
	switch verb {
	case "stop":
		return "stopped"
	case "start":
		return "started"
	case "restart":
		return "restarting"
	}
	return verb
}

// flipCmd fires one flip through the published client (flipr.go).
func flipCmd(base string, row FlagRow, newValue, reason string) tea.Cmd {
	return func() tea.Msg {
		res, err := flip(base, row, newValue, reason)
		return flagFlipped{res, err}
	}
}

// discoverCmd runs discovery off the ui goroutine.
func discoverCmd(domain string) tea.Cmd {
	return func() tea.Msg { return discovered{Discover(domain)} }
}

// fetchAPICmd pulls one service's contract.
func fetchAPICmd(name, domain string) tea.Cmd {
	return func() tea.Msg {
		api, err := FetchAPI(fmt.Sprintf("http://%s.%s", name, domain))
		return apiFetched{name: name, api: api, err: err}
	}
}

// Init starts on the splash, warms discovery behind it, and starts the
// animation clock.
func (m model) Init() tea.Cmd {
	// the theme pull rides along with discovery and blocks nothing: the
	// bird is painted in the baked palette and reskinned if dodo answers
	return tea.Batch(
		discoverCmd(m.domain),
		tickSplash(),
		themesCmd(m.domain),
		tickOverlay(),
	)
}

// newModel is the one place a model is built, so the theme index and the
// compiled styles cannot drift apart: they are a single decision.
//
// An unrecognised PUFFIN_THEME is carried as a note and said out loud rather
// than silently ignored -- a theme that quietly does nothing is an evening
// spent wondering why.
func newModel(domain, wantTheme string) model {
	idx, note := 0, ""
	if wantTheme == "" {
		// PUFFIN_THEME still wins: an environment variable is a
		// deliberate statement about THIS run and outranks last time's
		// preference
		if remembered := rememberedTheme(); remembered != "" {
			if i, ok := ThemeIndex(remembered); ok {
				idx = i
			}
		}
	}
	if wantTheme != "" {
		if i, ok := ThemeIndex(wantTheme); ok {
			idx = i
		} else {
			names := make([]string, 0, len(Themes()))
			for _, t := range Themes() {
				names = append(names, t.Name)
			}
			note = fmt.Sprintf("no theme called %q; have "+
				"%s", wantTheme, strings.Join(names, ", "))
		}
	}
	setLiveTheme(Themes()[idx])
	return model{
		styles: Compile(Themes()[idx]), theme: idx, themeNote: note,
		themes: Themes(), themeSrc: "baked", themeWant: wantTheme,
		domain:  domain,
		scr:     screenRoster,
		paneReg: panes(),
		mouse:   rememberedMouse(),
	}
}

// startTick starts a pane's clock if it asked for one. A pane that does not
// implement ticker, or asks for a non-positive interval, simply does not
// tick -- there is no default cadence, because a wrong one is worse than
// none.
func startTick(p pane) tea.Cmd {
	t, ok := p.(ticker)
	if !ok {
		return nil
	}
	d := t.Tick()
	if d <= 0 {
		return nil
	}
	return tickPane(p.Title(), d)
}

// setTheme moves to a theme by index and recompiles. The two fields are
// never assigned apart.
func (m model) setTheme(i int) model {
	n := len(m.themes)
	if n == 0 {
		return m
	}
	m.theme = ((i % n) + n) % n
	m.styles = Compile(m.themes[m.theme])
	setLiveTheme(m.themes[m.theme])
	m.themeNote = ""
	return m
}

// adoptThemes swaps in the list dodo served, keeping the operator on the theme
// they were wearing rather than on the index they happened to hold:
//
// the list can gain or reorder themes, and landing on a different skin because
// a background pull finished is a screen changing under you for no reason you
// can see.
func (m model) adoptThemes(msg themesFetched) model {
	if len(msg.themes) == 0 {
		// nothing usable: keep the baked ones and say why. A silently
		// stale palette is how you stop trusting a screen.
		if msg.err != nil {
			m.themeNote = "themes: dodo unreachable, wearing " +
				"baked (" + msg.err.Error() + ")"
		}
		return m
	}
	worn := m.styles.Theme
	m.themes, m.themeSrc = msg.themes, msg.source
	// PUFFIN_THEME wins if the pull finally brought the theme it named
	if m.themeWant != "" {
		if i, ok := indexIn(m.themes, m.themeWant); ok {
			m.themeNote = ""
			return m.setTheme(i)
		}
	}
	if i, ok := indexIn(m.themes, worn); ok {
		return m.setTheme(i)
	}
	return m.setTheme(0)
}

// indexIn finds a theme by name in a list.
func indexIn(list []Theme, name string) (int, bool) {
	for i, t := range list {
		if strings.EqualFold(t.Name, name) {
			return i, true
		}
	}
	return 0, false
}

// Update is the whole event loop.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case overlayTick:
		// the overlay asks for its own cadence and gets it, or nothing:
		// this used to be missing entirely, so the bird only ever
		// animated when something ELSE caused a redraw.
		//
		// A blink that lands only when you press a key is not a blink,
		// it is a coincidence.
		return m, tickOverlay()

	case splashTick:
		if m.scr == screenSplash {
			m.splashFrame++
			return m, tickSplash()
		}
		return m, nil // the clock stops when the bird flies

	case themesFetched:
		return m.adoptThemes(msg), nil

	case discovered:
		m.enclave = msg.e
		m.loading = false
		if m.cursor >= len(m.enclave.Services) {
			m.cursor = 0
		}
		// the furnishings are defined against the roster, so they can
		// only be worked out once the roster has answered
		return m, furnishingsCmd(m.enclave)

	case furnishingsFetched:
		m.furnishings = msg.f
		return m, nil

	case apiFetched:
		if msg.name == m.apiFor {
			m.api = msg.api
			if msg.err != nil {
				m.apiErr = msg.err.Error()
			}
		}
		return m, nil

	case flagsFetched:
		m.flags, m.flagsErr = msg.rows, ""
		if msg.err != nil {
			m.flagsErr = msg.err.Error()
		}
		if m.flagCursor >= len(m.flags) {
			m.flagCursor = 0
		}
		return m, nil

	case flagFlipped:
		if msg.err != nil {
			m.flagNote = msg.err.Error()
		} else if msg.res.Status != 200 {
			m.flagNote = msg.res.Body // flipr's refusal, verbatim
		} else {
			m.flagNote = "flipped"
			m.editing = false
			Emit(Event{
				Kind:    Acted,
				Subject: "flipr",
				Detail:  "flag flipped",
			})
			// refetch: the store is the truth
			return m, flagsCmd(m.domain)
		}
		return m, nil

	case listingFetched:
		m.mapsListing, m.mapsErr, m.mapsAttrs = msg.l, "", nil
		if msg.err != nil {
			m.mapsErr = msg.err.Error()
		}
		m.mapsCursor = 0
		return m, nil

	case attrsFetched:
		if msg.err != nil {
			m.mapsErr = msg.err.Error()
		} else {
			a := msg.a
			m.mapsAttrs = &a
		}
		return m, nil

	case kubeFetched:
		m.kube = msg.v
		first := m.kubeFold == nil
		if first {
			// the opening fold set is computed once per visit: a
			// refresh must not re-fold what the operator just
			// opened.
			//
			// The rule's defaults come first and the remembered
			// decisions are layered over them, so a namespace that
			// appeared today gets the rule rather than inheriting
			// somebody's answer about a different one.
			m.kubeFold = applyFolds(
				m.kube.Context,
				defaultFolds(m.kube.Pods),
			)
		}
		rows := m.kubeVisible()
		if first {
			m.kubeCursor = firstPodRow(rows)
		}
		if m.kubeCursor >= len(rows) {
			m.kubeCursor = max(0, len(rows)-1)
		}
		m.kubeOffset = windowOffset(
			len(rows),
			m.kubeCursor,
			m.kubeWindow(),
			m.kubeOffset,
		)
		return m, nil

	case kubeResolved:
		m.kubeBusy = false
		if msg.err != nil {
			m.kube.Err, m.kubeAsk = msg.err.Error(), nil
			return m, nil
		}
		m.kube.Err = ""
		m.kubeAsk = &kubeAction{verb: msg.verb, pod: msg.pod, w: msg.w}
		return m, nil

	case kubeActed:
		m.kubeBusy, m.kubeAsk = false, nil
		if msg.err != nil {
			m.kube.Err, m.kubeNote = msg.err.Error(), ""
			return m, nil
		}
		m.kube.Err, m.kubeNote = "", msg.note
		// a change puffin made, and it worked
		Emit(Event{Kind: Acted, Subject: "cluster", Detail: msg.note})
		return m, kubeCmd()

	case logsFetched:
		m.kubeBusy = false
		if msg.err != nil {
			m.kube.Err = msg.err.Error()
			return m, nil
		}
		m.kube.Err = ""
		vp := viewport.New(min(m.width-6, 160), m.height-7)
		vp.SetContent(msg.body)
		m.pager, m.pagerFrom, m.scr = vp, screenKube, screenPager
		return m, nil

	case fired:
		m.firing = false
		if msg.err != nil {
			m.invokeErr = msg.err.Error()
			return m, nil
		}
		m.result = &msg.res
		m.invokeErr = ""
		// a page's worth stays inline; more opens the pager,
		// highlighted. "large packets should be syntax highlighted and
		// opened in a pager"
		if strings.Count(msg.res.Body, "\n") > max(6, m.height-20) {
			vp := viewport.New(min(m.width-6, 120), m.height-7)
			vp.SetContent(HighlightJSON(msg.res.Body, m.styles))
			m.pager, m.pagerFrom = vp, screenInvoke
			m.scr = screenPager
		}
		return m, nil

	case paneTick:
		// a tick for a pane that is no longer open is dropped, and by
		// not rescheduling it the clock stops on its own. Either side
		// may be the one ticking.
		if m.scr != screenPane {
			return m, nil
		}
		for _, p := range []pane{m.openPane, m.rightPane} {
			if p != nil && p.Title() == msg.pane {
				// Rule 3, one round at a time.
				//
				// The clock keeps running whatever happens -- a
				// pane that stops ticking never recovers
				//
				// -- but a tick that arrives while the previous
				// load is still outstanding is dropped rather
				// than queued.
				//
				// Without this, the next tick was scheduled at
				// the moment the load started, so a round that
				// outlived its interval ran alongside the next
				// one.
				//
				// Sequential probes against a 3s timeout make
				// that certain on a slow provider, and it
				// compounds: more concurrent rounds, more load,
				// slower answers, longer rounds.
				//
				// Nothing fails, so backoff never fires; the
				// volume is inside the interval, so the spawn
				// budget never fires. See loops.go.
				now := time.Now()
				if ok, _ := paneSource(p).begin(now); !ok {
					return m, startTick(p)
				}
				return m, tea.Batch(
					p.Load(m.domain),
					startTick(p),
				)
			}
		}
		return m, nil

	// NOTE: a pane's reply must be listed here or it never arrives, and the
	// pane renders its zero value forever -- which looks exactly like an
	// audit that found nothing. The ports pane shipped that way for an
	// hour: empty context, no services, no mappings, all of it convincing.
	//
	// If a new pane comes up permanently empty, this list is the first
	// place to look.
	case hostFetched,
		deployFetched,
		agentsFetched,
		metricsFetched,
		tailFetched,
		toastExpired,
		fleetLogsFetched,
		busFetched,
		portsFetched,
		portsActed,
		agentActed:
		// RULE 3's other half: a reply closes the round its tick
		// opened, so the next tick is allowed.
		//
		// Closing here rather than in each pane's own case is
		// deliberate
		//
		// -- this list is already the one place every reply must pass
		// through, and a gate that opens in one place and closes in
		// eleven is a gate that will be left open by the twelfth pane
		// somebody writes.
		closeRounds(msg, m.openPane, m.rightPane)
		// data messages go to BOTH sides: two logs panes each asked for
		// their own read, and a reply that only reached the left one
		// would leave the right permanently empty
		var cmds []tea.Cmd
		if m.openPane != nil {
			p, cmd := m.openPane.Update(msg)
			m.openPane, cmds = p, append(cmds, cmd)
		}
		if m.rightPane != nil {
			p, cmd := m.rightPane.Update(msg)
			m.rightPane, cmds = p, append(cmds, cmd)
		}
		return m, tea.Batch(cmds...)

	case tea.MouseMsg:
		// recorded before it is dispatched.
		//
		// Every branch below returns from somewhere -- the splash
		// swallows it, a legacy screen takes only the wheel, a pane
		// without clicker drops it
		//
		// -- so anything that only wants to KNOW where the pointer is
		// would have had to be wired into each of those branches to
		// find out.
		//
		// See mouse.go.
		seeMouse(msg, m.width, m.height)
		// clicks go to the open pane, in the pane's own coordinates.
		// Panes that do not implement clicker simply do not respond,
		// which is the same bargain ticker makes.
		if m.scr == screenSplash {
			m.scr = screenRoster
			return m, nil
		}
		// the wheel scrolls the legacy screens too.
		//
		// It did not, because mouse handling was built for the panes
		// and the older screens were never given any -- so on the
		// contract screen the wheel silently did nothing, which reads
		// as a broken scroll rather than an unimplemented one.
		if m.scr != screenPane {
			switch msg.Button {
			case tea.MouseButtonWheelUp:
				return m.scrollLegacy(-1), nil
			case tea.MouseButtonWheelDown:
				return m.scrollLegacy(1), nil
			}
			return m, nil
		}
		if m.openPane == nil {
			return m, nil
		}
		c, ok := m.focused().(clicker)
		if !ok {
			return m, nil
		}
		wheel := 0
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			wheel = -1
		case tea.MouseButtonWheelDown:
			wheel = 1
		case tea.MouseButtonLeft:
			if msg.Action != tea.MouseActionPress {
				return m, nil
			}
		default:
			return m, nil
		}
		return m, c.Click(msg.X-paneBodyLeft, msg.Y-paneBodyTop, wheel)

	case tea.KeyMsg:
		if m.scr == screenSplash {
			m.scr = screenRoster
			return m, nil
		}
		// the flag editor owns typing while open
		if m.scr == screenFlags && m.editing {
			switch msg.String() {
			case "esc":
				m.editing, m.flagNote = false, ""
				return m, nil
			case "tab":
				m.editFocus = 1 - m.editFocus
				if m.editFocus == 0 {
					m.editValue.Focus()
					m.editReason.Blur()
				} else {
					m.editValue.Blur()
					m.editReason.Focus()
				}
				return m, nil
			case "ctrl+s", "enter":
				// enter sends from the reason box; in the value
				// box it is a keystroke (strings may want it --
				// but flags are one-line, so enter sends there
				// too)
				row := m.flags[m.flagCursor]
				reason := strings.TrimSpace(
					m.editReason.Value(),
				)
				if reason == "" {
					m.flagNote = "flipr will refuse a " +
						"" +
						"flip with no reason"
					return m, nil
				}
				base := fmt.Sprintf("http://flipr.%s", m.domain)
				val := strings.TrimSpace(m.editValue.Value())
				return m, flipCmd(base, row, val, reason)
			}
			var cmd tea.Cmd
			if m.editFocus == 0 {
				m.editValue, cmd = m.editValue.Update(msg)
			} else {
				m.editReason, cmd = m.editReason.Update(msg)
			}
			return m, cmd
		}

		// the pager scrolls; esc or q hands back to the composer with
		// the result still inline
		if m.scr == screenPager {
			switch msg.String() {
			case "esc", "q":
				m.scr = m.pagerFrom
				if m.scr == screenSplash {
					m.scr = screenInvoke
				}
				return m, nil
			}
			var cmd tea.Cmd
			m.pager, cmd = m.pager.Update(msg)
			return m, cmd
		}
		// a pending start/stop/restart owns the keyboard until it is
		// answered. A change that takes a service down gets one
		// deliberate keystroke, never a stray one, and anything that is
		// not yes is no.
		if m.scr == screenKube && m.kubeAsk != nil {
			if msg.String() == "y" {
				a := *m.kubeAsk
				m.kubeBusy, m.kubeNote = true, ""
				return m, actCmd(a)
			}
			m.kubeAsk, m.kubeNote = nil, "cancelled"
			return m, nil
		}
		// the invoke screen owns its keys: the textarea eats typing,
		// and only ctrl+s (send) and esc (back) belong to puffin
		if m.scr == screenInvoke {
			switch msg.String() {
			case "esc":
				m.scr = screenDetail
				m.result, m.invokeErr = nil, ""
				return m, nil
			case "ctrl+s":
				m.firing = true
				m.result, m.invokeErr = nil, ""
				base := fmt.Sprintf(
					"http://%s.%s",
					m.apiFor,
					m.domain,
				)
				return m, invokeCmd(
					base,
					m.invokeSvc.FullName,
					m.invokeMethod.Name,
					m.editor.Value(),
				)
			}
			var cmd tea.Cmd
			m.editor, cmd = m.editor.Update(msg)
			return m, cmd
		}
		// a pane owns its keyboard except for the two verbs that belong
		// to puffin: leaving, and asking again
		if m.scr == screenPane && m.openPane != nil {
			// a pane that is capturing input owns every key except
			// the one that always belongs to the terminal.
			//
			// Without this, q closes the pane in the middle of a
			// sentence you were writing to an agent, and your
			// message becomes a screen change.
			if c, ok := m.focused().(capturer); ok &&
				c.Capturing() {
				if msg.String() == "ctrl+c" {
					return m, tea.Quit
				}
				return m.updateFocused(msg)
			}
			// while split, a pane key replaces the focused side:
			// that is how logs|bus is reached, and it reads as "put
			// this here"
			if m.rightPane != nil {
				if mk, ok := paneFactories()[msg.String()]; ok {
					p := mk()
					if m.focus == right {
						m.rightPane = p
					} else {
						m.openPane = p
						notePane(p.Title())
					}
					return m, loadForced(p, m.domain)
				}
			}
			// a pane may claim esc for itself -- the agents pane
			// uses it to clear a filter -- and only when it says it
			// wants it
			if msg.String() == "esc" {
				if e, ok := m.focused().(escaper); ok &&
					e.HandlesEsc() {
					return m.updateFocused(msg)
				}
			}
			switch msg.String() {
			case "q", "esc", "left":
				// leaving closes the focused side first: with
				// two panes open, q closing both is a keystroke
				// that throws away more than it was asked to
				if m.rightPane != nil {
					if m.focus == right {
						m.rightPane = nil
					} else {
						m.openPane, m.rightPane =
							m.rightPane, nil
					}
					m.focus = left
					return m, nil
				}
				m.scr, m.openPane = screenRoster, nil
				notePane("")
				return m, nil
			case "ctrl+c":
				return m, tea.Quit
			case "r":
				return m, loadForced(m.focused(), m.domain)
			case "|":
				// split, or unsplit. Splitting duplicates the
				// focused pane because that is the common case
				// -- two logs, two topics -- and any pane key
				// then replaces the focused side.
				if m.rightPane != nil {
					m.rightPane, m.focus = nil, left
					return m, nil
				}
				key := m.openPane.Key()
				if mk, ok := paneFactories()[key]; ok {
					p := mk()
					m.rightPane, m.focus = p, right
					return m, loadForced(p, m.domain)
				}
				return m, nil
			case "ctrl+w":
				if m.rightPane != nil {
					m.focus = right - m.focus
				}
				return m, nil
			case "t":
				// the theme belongs to puffin, not to whichever
				// screen you are on: a keystroke that works on
				// the roster and silently does nothing three
				// screens deep is worse than not having it
				m = m.setTheme(m.theme + 1)
				rememberTheme(m.styles.Theme)
				return m, nil
			case "T":
				return m, themesCmd(m.domain)
			case "C":
				// the companion, for the same reason: it stands
				// in the corner of every screen, so it is
				// chosen from every screen
				return m.nextCompanion(), nil
			case "e":
				return m.pokeCompanion(), nil
			}
			return m.updateFocused(msg)
		}
		// the roster's pane keys: one keystroke each, from the
		// registry, so a new pane needs no edit here
		if m.scr == screenRoster {
			if p, ok := m.paneReg[msg.String()]; ok {
				m.scr, m.openPane = screenPane, p
				notePane(p.Title())
				return m, tea.Batch(
					loadForced(p, m.domain),
					startTick(p),
				)
			}
		}
		switch msg.String() {
		case "M":
			// bound on the screens that are NOT panes,
			// deliberately:
			//
			// the logs pane uses M for vim's middle-of-screen, and
			// a global key that shadows a vim motion inside a pager
			// is a worse trade than a setting you reach from the
			// front page.
			return m.toggleMouse()
		case "F":
			// only where the block is drawn: a key that silently
			// does nothing three screens deep is worse than not
			// having it
			if m.scr == screenRoster {
				m.furnSort = (m.furnSort + 1) % len(furnSorts)
			}
			return m, nil
		case "t":
			m = m.setTheme(m.theme + 1)
			rememberTheme(m.styles.Theme)
			return m, nil
		case "C":
			return m.nextCompanion(), nil
		case "e":
			return m.pokeCompanion(), nil
		case "T":
			// ask dodo again: presentation is meant to be one thing
			// across the estate, so a theme change over there is a
			// keystroke away here rather than a restart
			return m, themesCmd(m.domain)
		case "q", "ctrl+c":
			if m.scr != screenRoster {
				m.scr = screenRoster
				return m, nil
			}
			return m, tea.Quit
		case "f":
			m.scr, m.flagNote = screenFlags, ""
			return m, flagsCmd(m.domain)
		case "m":
			m.scr = screenMaps
			m.mapsBase = fmt.Sprintf(
				"http://kingfisher.%s",
				m.domain,
			)
			m.mapsStack, m.mapsListing, m.mapsAttrs = nil, nil, nil
			return m, func() tea.Msg {
				mounts, err := fetchMounts(m.mapsBase)
				if err != nil {
					return listingFetched{nil, err}
				}
				l := &MapListing{Path: "/", Mount: "(mounts)"}
				for _, mt := range mounts {
					l.Entries = append(l.Entries, MapEntry{
						Name:  mt,
						Dir:   true,
						Bytes: -1,
					})
				}
				return listingFetched{l, nil}
			}
		case "c":
			m.scr = screenKube
			if len(m.clusters) == 0 {
				// asked once per visit and never on the hot
				// path: this is one line of context, not a
				// source of truth
				if cl, err := fetchClusters(); err == nil {
					m.clusters = cl
				}
			}
			m.kube = KubeView{Context: kubeContext()}
			m.kubeCursor, m.kubeNote, m.kubeAsk = 0, "", nil
			m.kubeFold, m.kubeOffset = nil, 0
			return m, kubeCmd()
		case "l":
			if m.scr == screenKube {
				p, ok := m.kubePodAt(m.kubeCursor)
				if !ok {
					m.kubeNote = "that is a fold, not a " +
						"pod -- space opens it"
					return m, nil
				}
				m.kubeBusy, m.kubeNote = true, ""
				return m, logsCmd(p.Namespace, p.Name)
			}
		case "s", "x", "R":
			// resolve first, confirm second: the confirmation has
			// to name the workload that actually moves, not the pod
			// under the cursor
			if m.scr == screenKube {
				p, ok := m.kubePodAt(m.kubeCursor)
				if !ok {
					m.kubeNote = "that is a fold, not a " +
						"pod -- space opens it"
					return m, nil
				}
				verb := map[string]string{
					"s": "start",
					"x": "stop",
					"R": "restart",
				}[msg.String()]
				m.kubeBusy, m.kubeNote = true, ""
				return m, resolveCmd(verb, p.Namespace, p.Name)
			}
		case "r":
			if m.scr == screenKube {
				return m, kubeCmd()
			}
			if m.scr == screenFlags {
				return m, flagsCmd(m.domain)
			}
			m.loading = true
			return m, discoverCmd(m.domain)
		case "up", "k":
			switch m.scr {
			case screenDetail:
				if m.methodCursor > 0 {
					m.methodCursor--
				}
				rows := detailRows(m.api)
				at := detailRowOf(rows, m.methodCursor)
				m.detailOffset = windowOffset(
					len(rows), at,
					m.detailWindow(), m.detailOffset)
			case screenFlags:
				if m.flagCursor > 0 {
					m.flagCursor--
				}
			case screenMaps:
				if m.mapsCursor > 0 {
					m.mapsCursor--
				}
			case screenKube:
				if m.kubeCursor > 0 {
					m.kubeCursor--
				}
				m.kubeOffset = windowOffset(
					len(m.kubeVisible()),
					m.kubeCursor,
					m.kubeWindow(),
					m.kubeOffset,
				)
			case screenRoster:
				m = m.rosterScroll(-1)
			default:
				if m.cursor > 0 {
					m.cursor--
				}
			}
		case "down", "j":
			switch m.scr {
			case screenDetail:
				if m.methodCursor < m.methodCount()-1 {
					m.methodCursor++
				}
				rows := detailRows(m.api)
				at := detailRowOf(rows, m.methodCursor)
				m.detailOffset = windowOffset(
					len(rows), at,
					m.detailWindow(), m.detailOffset)
			case screenFlags:
				if m.flagCursor < len(m.flags)-1 {
					m.flagCursor++
				}
			case screenMaps:
				ml := m.mapsListing
				if ml != nil &&
					m.mapsCursor < len(ml.Entries)-1 {
					m.mapsCursor++
				}
			case screenKube:
				if m.kubeCursor < len(m.kubeVisible())-1 {
					m.kubeCursor++
				}
				m.kubeOffset = windowOffset(
					len(m.kubeVisible()),
					m.kubeCursor,
					m.kubeWindow(),
					m.kubeOffset,
				)
			case screenRoster:
				m = m.rosterScroll(1)
			default:
				if m.cursor < len(m.enclave.Services)-1 {
					m.cursor++
				}
			}
		// a page at a time, on the one screen whose body is taller than
		// its window by design. The cursor stays where it is: this
		// moves the view, not the selection.
		case "pgup", "pgdown":
			if m.scr == screenRoster {
				d := m.rosterWindow()
				if msg.String() == "pgup" {
					d = -d
				}
				m = m.rosterPage(d)
			}
		case "backspace", "left", "h":
			// one rule everywhere: left is up, and up from the top
			// is out. The screens with a hierarchy climb it first;
			// the ones without leave immediately.
			if m.scr == screenDetail || m.scr == screenFlags {
				m.scr = screenRoster
				return m, nil
			}
			// on the cluster screen this is the file-tree grammar:
			// collapse what the cursor is in, and put the cursor on
			// what closed, so a second press walks up the hierarchy
			// rather than doing nothing twice
			if m.scr == screenKube {
				rows := m.kubeVisible()
				if len(rows) == 0 {
					m.scr = screenRoster
					return m, nil
				}
				if m.kubeCursor >= len(rows) {
					m.kubeCursor = len(rows) - 1
				}
				key := rows[m.kubeCursor].Key
				if r := rows[m.kubeCursor]; !r.Foldable() ||
					m.kubeFold[key] {
					key = parentKey(rows, m.kubeCursor)
				}
				// already collapsed to a folded namespace:
				// there is nothing above this, so left means
				// out.
				//
				// Up from the top is out is the same rule every
				// file manager uses, and a key that silently
				// does nothing at the top is the one people
				// report.
				if r := rows[m.kubeCursor]; r.Depth == 0 &&
					r.Kind == rowNS &&
					m.kubeFold[key] {
					m.scr = screenRoster
					return m, nil
				}
				m.kubeFold[key] = true
				rememberFold(m.kube.Context, key, true)
				m.kubeCursor = rowIndex(m.kubeVisible(), key)
				m.kubeOffset = windowOffset(
					len(m.kubeVisible()),
					m.kubeCursor,
					m.kubeWindow(),
					m.kubeOffset,
				)
				return m, nil
			}
			if m.scr == screenMaps && len(m.mapsStack) == 0 {
				m.scr = screenRoster
				return m, nil
			}
			if m.scr == screenMaps && len(m.mapsStack) > 0 {
				m.mapsStack = m.mapsStack[:len(m.mapsStack)-1]
				path := "/" + strings.Join(m.mapsStack, "")
				if len(m.mapsStack) == 0 {
					// back to the mount table
					return m, mountsCmd(m.mapsBase)
				}
				return m, listingCmd(m.mapsBase, path)
			}
		case " ", "right":
			// space toggles the fold under the cursor; right only
			// opens, so the two halves of the grammar stay
			// predictable
			if m.scr == screenKube {
				rows := m.kubeVisible()
				if len(rows) == 0 {
					return m, nil
				}
				// the cursor can be past the end after a
				// refresh shrank the list; indexing it would
				// panic rather than misbehave
				if m.kubeCursor >= len(rows) {
					m.kubeCursor = len(rows) - 1
				}
				if !rows[m.kubeCursor].Foldable() {
					// a key that silently does nothing is a
					// key the operator concludes is broken
					// -- which is exactly what happened
					// here:
					//
					// the screen opens with the cursor on a
					// POD, and space on a pod has nothing
					// to fold
					m.kubeNote = "that row is a pod · " +
						"move to a > or v line to " +
						"fold it"
					return m, nil
				}
				m.kubeNote = ""

				key := rows[m.kubeCursor].Key
				if msg.String() == "right" {
					m.kubeFold[key] = false
				} else {
					m.kubeFold[key] = !m.kubeFold[key]
				}
				rememberFold(
					m.kube.Context,
					key,
					m.kubeFold[key],
				)
				m.kubeOffset = windowOffset(
					len(m.kubeVisible()),
					m.kubeCursor,
					m.kubeWindow(),
					m.kubeOffset,
				)
				return m, nil
			}
		case "enter":
			if m.scr == screenKube {
				rows := m.kubeVisible()
				if m.kubeCursor >= len(rows) {
					m.kubeCursor = maxInt(0, len(rows)-1)
				}
				if len(rows) > 0 &&
					rows[m.kubeCursor].Foldable() {
					key := rows[m.kubeCursor].Key
					m.kubeFold[key] = !m.kubeFold[key]
					rememberFold(
						m.kube.Context,
						key,
						m.kubeFold[key],
					)
					m.kubeOffset = windowOffset(
						len(m.kubeVisible()),
						m.kubeCursor,
						m.kubeWindow(),
						m.kubeOffset,
					)
				}
				return m, nil
			}
			if m.scr == screenFlags && len(m.flags) > 0 {
				// the flag under the cursor becomes the edit:
				// bools arrive pre-toggled, strings and ints
				// arrive current for editing
				row := m.flags[m.flagCursor]
				ev := textarea.New()
				ev.SetHeight(1)
				ev.SetWidth(40)
				if row.Kind == "bool" {
					if row.Value == "true" {
						ev.SetValue("false")
					} else {
						ev.SetValue("true")
					}
				} else {
					ev.SetValue(row.Value)
				}
				rv := textarea.New()
				rv.SetHeight(1)
				rv.SetWidth(60)
				rv.Placeholder = "reason (flipr requires " +
					"it)"
				rv.Focus()
				m.editValue, m.editReason = ev, rv
				m.editFocus = 1
				m.editing, m.flagNote = true, ""
				return m, nil
			}
			if m.scr == screenMaps &&
				m.mapsListing != nil &&
				len(m.mapsListing.Entries) > 0 {
				e := m.mapsListing.Entries[m.mapsCursor]
				if e.Dir {
					m.mapsStack = append(
						m.mapsStack,
						e.Name,
					)
					return m, listingCmd(
						m.mapsBase,
						"/"+strings.Join(
							m.mapsStack,
							"",
						),
					)
				}
				// a file: the assessment. One HEAD, on request.
				return m, attrsCmd(
					m.mapsBase,
					"/"+strings.Join(
						m.mapsStack,
						"",
					)+e.Name,
				)
			}
			if m.scr == screenRoster &&
				len(m.enclave.Services) > 0 {
				svc := m.enclave.Services[m.cursor]
				m.scr = screenDetail
				m.api, m.apiErr, m.apiFor = nil, "", svc.Name
				m.methodCursor, m.detailOffset = 0, 0
				return m, fetchAPICmd(svc.Name, m.domain)
			}
			if m.scr == screenDetail && m.api != nil {
				svc, meth, ok := m.methodAt(m.methodCursor)
				if !ok {
					return m, nil
				}
				skeleton, err := Skeleton(
					m.api.FDS,
					meth.InputFQN,
				)
				if err != nil {
					m.invokeErr = err.Error()
					return m, nil
				}
				ed := textarea.New()
				ed.SetValue(skeleton)
				wide := m.width >= 130
				edw := min(m.width-8, 90)
				if wide {
					edw = min(m.width/2-6, 80)
				}
				ed.SetWidth(edw)
				ed.SetHeight(min(m.height-14, 18))
				ed.Focus()
				m.editor, m.invokeSvc = ed, svc
				m.invokeMethod = meth
				m.schemaDocs = SchemaDoc(
					m.api.FDS,
					meth.InputFQN,
				)
				m.result, m.invokeErr = nil, ""
				m.scr = screenInvoke
			}
		}
	}
	return m, nil
}

// View renders the current screen.
func (m model) View() string {
	if m.width == 0 {
		return ""
	}
	switch m.scr {
	case screenSplash:
		return splashView(m.styles, m.width, m.height, m.splashFrame)
	case screenDetail:
		return detailView(m)
	case screenInvoke:
		return invokeView(m)
	case screenPager:
		return pagerView(m)
	case screenFlags:
		return flagsView(m)
	case screenMaps:
		return mapsView(m)
	case screenKube:
		return kubeView(m)
	case screenPane:
		return paneView(m)
	default:
		return rosterView(m)
	}
}

// detailView is one member's contract, from its own mouth.
func detailView(m model) string {
	s := m.styles
	var b strings.Builder
	b.WriteString(
		s.Beak.Render(
			"puffin",
		) + s.Subtitle.Render(
			"  ·  "+m.apiFor+".test  ·  the contract, "+
				"self-described",
		) + "\n\n",
	)

	switch {
	case m.apiErr != "":
		b.WriteString(
			s.Warn.Render("cannot read /api: "+m.apiErr) + "\n",
		)
		b.WriteString(
			s.Dim.Render(
				"in the enclave's antechamber: reachable, "+
					"but not publishing. the gap is the "+
					"finding.",
			) + "\n",
		)
	case m.api == nil:
		b.WriteString(s.Dim.Render("reading the descriptor...") + "\n")
	default:
		b.WriteString(
			s.Dim.Render(
				fmt.Sprintf(
					"%d bytes of contract, %d message "+
						"types",
					m.api.Bytes,
					m.api.Messages,
				),
			) + "\n\n",
		)
		// a large contract is windowed rather than allowed to run off
		// the top: the method under the cursor and its example packet
		// are what this screen is for, and a service publishing forty
		// methods used to push both out of the terminal
		rows := detailRows(m.api)
		size := m.detailWindow()
		off := windowOffset(
			len(rows),
			detailRowOf(rows, m.methodCursor),
			size,
			m.detailOffset,
		)
		last := off + size
		if last > len(rows) {
			last = len(rows)
		}
		// a bar down the right edge, so a contract that runs past the
		// window says so continuously rather than only at the moment
		// you reach an end
		bar := scrollbar(len(rows), last-off, off)
		for i := off; i < last; i++ {
			r := rows[i]
			track := s.Dim.Render(" " + bar[i-off])
			if r.Index < 0 {
				b.WriteString(
					trackAt(
						padTo(
							s.Accent.Render(
								r.Service,
							),
							0,
						),
						76,
						track,
					) + "\n",
				)
				continue
			}
			on := r.Index == m.methodCursor
			marker := s.On(s.Row, on).Render("  ")
			if on {
				marker = s.On(s.RowSel, on).Render("▸ ")
			}
			// through trackAt like every other row: padTo pads but
			// does not truncate, so a long input/output pair pushed
			// the bar right and the column came apart
			sig := padTo(r.Method.Input+" → "+r.Method.Output, 52)
			b.WriteString(
				trackAt(
					marker+s.On(s.Row, on).
						Render(pad(r.Method.Name, 22))+
						s.On(s.Dim, on).Render(sig),
					76,
					track,
				) + "\n",
			)
		}
		if note := scrollNote(len(rows), last-off, off); note != "" {
			b.WriteString(
				s.Dim.Render(
					"  "+note+" · j/k or the wheel",
				) + "\n",
			)
		}
		b.WriteString("\n")
		// the example packet for the method under the cursor: what you
		// would send, shown before you commit to the composer. Computed
		// from the descriptor on every render; it is microseconds.
		if _, meth, ok := m.methodAt(m.methodCursor); ok {
			if skel, err := Skeleton(
				m.api.FDS,
				meth.InputFQN,
			); err == nil {
				b.WriteString(
					s.Header.Render(
						"example request — "+meth.Name,
					) + "\n",
				)
				b.WriteString(
					s.Panel.Render(
						HighlightJSON(skel, s),
					) + "\n\n",
				)
			}
		}
		b.WriteString(
			s.Help.Render(
				"enter: open the composer and FIRE it",
			) + "\n",
		)
	}
	b.WriteString(helpLine(s, "j/k: method · enter: invoke · q: back"))
	return drawOverlay(
		lipgloss.NewStyle().Padding(1, 2).Render(b.String()),
		m.width,
		m.height,
	)
}

// methodCount totals methods across the parsed services.
func (m model) methodCount() int {
	if m.api == nil {
		return 0
	}
	n := 0
	for _, s := range m.api.Services {
		n += len(s.Methods)
	}
	return n
}

// methodAt resolves a flat cursor into (service, method).
func (m model) methodAt(i int) (APIService, APIMethod, bool) {
	if m.api == nil {
		return APIService{}, APIMethod{}, false
	}
	for _, s := range m.api.Services {
		if i < len(s.Methods) {
			return s, s.Methods[i], true
		}
		i -= len(s.Methods)
	}
	return APIService{}, APIMethod{}, false
}

// invokeView is the request editor and the response, side by side with the
// truth: what will be sent, where, and what came back.
func invokeView(m model) string {
	s := m.styles
	var b strings.Builder
	b.WriteString(s.Beak.Render("puffin") +
		s.Subtitle.Render(
			"  ·  "+m.invokeSvc.FullName+"/"+s.Accent.Render(
				m.invokeMethod.Name,
			),
		) + "\n")
	b.WriteString(
		s.Dim.Render(
			m.invokeMethod.Input+" → "+m.invokeMethod.Output,
		) + "\n\n",
	)
	editor := m.editor.View()
	schema := renderSchema(s, m.schemaDocs, m.width/2-6)
	if m.width >= 130 {
		// side by side: the request being built next to what the author
		// said it should be
		b.WriteString(
			lipgloss.JoinHorizontal(
				lipgloss.Top,
				editor,
				"   ",
				schema,
			) + "\n\n",
		)
	} else {
		b.WriteString(schema + "\n" + editor + "\n\n")
	}
	switch {
	case m.firing:
		b.WriteString(s.Caution.Render("firing...") + "\n")
	case m.invokeErr != "":
		b.WriteString(s.Warn.Render(m.invokeErr) + "\n")
	case m.result != nil:
		code := s.Ok
		if m.result.Status >= 400 {
			code = s.Warn
		}
		b.WriteString(
			code.Render(
				fmt.Sprintf("HTTP %d", m.result.Status),
			) + "\n",
		)
		b.WriteString(
			s.Panel.Render(HighlightJSON(m.result.Body, s)) + "\n",
		)
	}
	b.WriteString(
		helpLine(
			s,
			"ctrl+s: send · esc: back · the skeleton shows one "+
				"arm per oneof; swap it if you need another",
		),
	)
	return drawOverlay(
		lipgloss.NewStyle().Padding(1, 2).Render(b.String()),
		m.width,
		m.height,
	)
}

// pagerView is the big-packet reader: the response, highlighted, scrolling.
func pagerView(m model) string {
	s := m.styles
	code := s.Ok
	if m.result != nil && m.result.Status >= 400 {
		code = s.Warn
	}
	status := ""
	if m.result != nil {
		status = code.Render(fmt.Sprintf("HTTP %d", m.result.Status))
	}
	header := s.Beak.Render("puffin") +
		s.Subtitle.Render("  ·  "+m.invokeSvc.FullName+
			"/"+m.invokeMethod.Name+"  ·  ") + status +
		s.Dim.Render(
			fmt.Sprintf("   %3.0f%%", m.pager.ScrollPercent()*100),
		)
	help := helpLine(
		s,
		"j/k, pgup/pgdn: scroll · esc: back to the composer",
	)
	return lipgloss.NewStyle().Padding(1, 2).Render(
		header + "\n\n" + m.pager.View() + "\n\n" + help)
}

// pad right-pads to width, truncating politely.
func pad(v string, w int) string {
	if len(v) > w-1 {
		v = v[:w-2] + "…"
	}
	return v + strings.Repeat(" ", w-len([]rune(v)))
}

// orDash renders empty as a dash.
func orDash(v string) string {
	if v == "" {
		return "-"
	}
	return v
}

// main runs the bird, or a subcommand when the shell asks plainly.
func main() {
	defer catchCrash()
	if len(os.Args) > 1 {
		os.Exit(runCLI(os.Args[1:]))
	}
	domain := os.Getenv("PUFFIN_DOMAIN")
	if domain == "" {
		// the cluster's own domain, where its service names resolve
		domain = "test"
	}
	runTUI(domain)
}

// mountsCmd reads the mount table and renders it as a listing, which is
// what "up" from the top of a map browse lands on.
//
// A command of its own rather than a closure inside the key switch: six
// levels of indent in there leave a field name no room, and the reason
// this exists has nothing to do with which key was pressed.
func mountsCmd(base string) tea.Cmd {
	return func() tea.Msg {
		mounts, err := fetchMounts(base)
		if err != nil {
			return listingFetched{nil, err}
		}
		l := &MapListing{Path: "/", Mount: "(mounts)"}
		for _, mt := range mounts {
			l.Entries = append(l.Entries, MapEntry{
				Name: mt, Dir: true, Bytes: -1,
			})
		}
		return listingFetched{l, nil}
	}
}

// runTUI opens the interface. Split out from main so that global flags can
// be taken off argv and still end up here -- "puffin --context k3d-fleet"
// is a TUI invocation with an opinion, not a subcommand.
func runTUI(domain string) {
	loadUIState()
	// puffin has never recorded what puffin was doing; see vitals.go
	startVitals(openPaneName)
	// the notifier is a consumer of events now, not the place they are
	// sent. A sprite that emotes at the screen wants the same ones.
	listenNotify()
	SetOverlay(newBird())
	// notifications are opt-in and remembered: PUFFIN_NOTIFY turns them on
	// from a cold start, and ! toggles them in the agents pane
	enableNotify(notifyFromEnv() || rememberedNotify())
	restoreWatches(rememberedWatches())
	m := newModel(domain, os.Getenv("PUFFIN_THEME"))
	m.scr, m.loading = screenSplash, true
	opts := []tea.ProgramOption{tea.WithAltScreen()}
	if rememberedMouse() {
		opts = append(opts, tea.WithMouseCellMotion())
	}
	// the pointer's record starts out agreeing with the terminal, so a
	// consumer asking before the first mouse event gets "not watching"
	// rather than a zero position that reads as the top left corner
	mouseReporting(rememberedMouse())
	if _, err := tea.NewProgram(m, opts...).Run(); err != nil {
		fmt.Fprintf(
			os.Stderr,
			`{"level":"fatal","msg":"puffin crashed",`+
				`"err":%q}`+"\n",
			err.Error(),
		)
		os.Exit(1)
	}
}

// focused is the pane with the keyboard.
func (m model) focused() pane {
	if m.rightPane != nil && m.focus == right {
		return m.rightPane
	}
	return m.openPane
}

// updateFocused hands a message to the focused side and puts the result
// back where it came from.
func (m model) updateFocused(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.rightPane != nil && m.focus == right {
		p, cmd := m.rightPane.Update(msg)
		m.rightPane = p
		return m, cmd
	}
	p, cmd := m.openPane.Update(msg)
	m.openPane = p
	return m, cmd
}

// scrollLegacy moves the cursor on whichever older screen is open. The
// cursor IS the scroll on these screens: they window around it, so moving it
// is what the wheel should do.
func (m model) scrollLegacy(d int) model {
	switch m.scr {
	case screenDetail:
		m.methodCursor = clampInt(
			m.methodCursor+d,
			0,
			m.methodCount()-1,
		)
		rows := detailRows(m.api)
		m.detailOffset = windowOffset(
			len(rows),
			detailRowOf(
				rows,
				m.methodCursor,
			),
			m.detailWindow(),
			m.detailOffset,
		)
	case screenKube:
		rows := m.kubeVisible()
		m.kubeCursor = clampInt(m.kubeCursor+d, 0, len(rows)-1)
		m.kubeOffset = windowOffset(
			len(rows),
			m.kubeCursor,
			m.kubeWindow(),
			m.kubeOffset,
		)
	case screenFlags:
		m.flagCursor = clampInt(m.flagCursor+d, 0, len(m.flags)-1)
	case screenMaps:
		if m.mapsListing != nil {
			m.mapsCursor = clampInt(
				m.mapsCursor+d,
				0,
				len(m.mapsListing.Entries)-1,
			)
		}
	case screenPager:
		m.pager.ScrollDown(d)
		if d < 0 {
			m.pager.ScrollUp(-d)
			m.pager.ScrollDown(-d)
			m.pager.ScrollUp(-d)
		}
	case screenRoster:
		return m.rosterScroll(d)
	default:
		m.cursor = clampInt(m.cursor+d, 0, len(m.enclave.Services)-1)
	}
	return m
}

// clampInt keeps an index inside a list, and copes with an empty one.
func clampInt(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// nextCompanion stands the next character in the corner, and remembers it.
//
// A cycle rather than a picker because there are four of them and a screen
// to choose between four is a screen nobody opens twice. The name is said
// out loud, because the corner is small and the gopher and the auklet are
// the same silhouette at eight rows.
func (m model) nextCompanion() model {
	name, ok := chooseMascot(nextMascot(standingMascot()))
	if !ok {
		m.themeNote = "companion: nothing called that"
		return m
	}
	m.companionNote = name + " is standing in the corner"
	return m
}

// pokeCompanion asks the companion to do something, now.
//
// The corner performs on events and otherwise idles on a slow clock, which
// is right for a thing that lives beside your work and useless for finding
// out what it can do -- and you cannot meaningfully choose between four
// characters you have no way to ask to move.
func (m model) pokeCompanion() model {
	name, ok := pokeCompanion()
	if !ok {
		// off is a first-class answer here, so say which off it is
		// rather than letting the key look broken
		m.companionNote = "there is nobody in the corner · " +
			"PUFFIN_BIRD=1 brings one back"
		return m
	}
	m.companionNote = currentMascot() + ": " + name + " · e for the next " +
		"one"
	return m
}

// toggleMouse hands the mouse back to the terminal, or takes it again.
//
// Mouse reporting is a trade and not a feature. While puffin is listening for
// clicks, the terminal never sees the drag, so selecting text to copy stops
// working -- and it stops working silently, which reads as a broken terminal
// rather than as a tool holding the mouse on purpose.
//
// Ghostty's shift-drag bypasses it and expecting anyone to know that is not a
// design.
//
// Keyboard selection is the better answer and puffin has it. This stays
// anyway, because it costs twenty lines and it is the escape hatch for the
// day the yank does not reach whatever you are pasting into.
func (m model) toggleMouse() (tea.Model, tea.Cmd) {
	m.mouse = !m.mouse
	rememberMouse(m.mouse)
	// the pointer's own record of whether anything can still arrive.
	// Without this a consumer watches a position that stopped being updated
	// and has no way to tell that from a mouse holding still.
	mouseReporting(m.mouse)
	if m.mouse {
		return m, tea.EnableMouseCellMotion
	}
	return m, tea.DisableMouse
}

// mouseHelp says which way the trade is currently set, because "M: mouse"
// tells you nothing about what pressing it will do.
func mouseHelp(on bool) string {
	if on {
		return "M: free the mouse"
	}
	return "M: take the mouse"
}
