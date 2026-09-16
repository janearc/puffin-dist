package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Idempotency is not needed here, and this is what settles it, for whoever
// finds this file at three in the morning wondering whether to add a
// database.
//
// Puffin runs in several windows at once, and two kinds of state are shared:
// preferences, and "has anyone already said this". Neither needs a transaction,
// because the test is not "is it shared" -- it is "can two valid writes produce
// an invalid whole". Here they cannot.
//
// The preference fields are independent of each other, so a merge per field is
// not a compromise, it is the correct semantics. Setting a fold twice is
// setting it once.
//
// bbolt was considered and refused: it takes an exclusive flock, one writer per
// process, so the store meant to let several copies share state is the thing
// that stops the second one opening.
//
// If puffin ever grows ordering, cross-instance counters, or a write that is
// invalid halfway through, that is the moment -- and it would be sqlite,
// because multi-process writers with wal is the actual requirement.
//
// Until then: idempotent operations, merged on write, and no daemon anybody
// has to run.

// Remembered interface state.
//
// Puffin holds no state between invocations -- that is a real rule and this
// does not break it. The rule is about truth: a cached roster, a remembered
// flag value, a stale health reading are all things that can be wrong in a
// way that misleads, so puffin asks the mesh every time and holds nothing.
//
// A fold is not truth. It says which namespaces this operator does not want
// to look at, which cannot be wrong about the estate because it makes no
// claim about it. It is the same category as the theme, and DESIGN.md
// already sanctions that one.
//
// It is only safe because of something built earlier: a folded header
// carries the count and condition of what it hid, so a remembered fold
// cannot conceal a broken pod. If that ever stops being true, this file
// becomes a hazard and should go.
//
// What is remembered is decisions, never derived state. On the cluster screen
// the folds start from the rule -- prefix groups of more than two closed,
// namespaces open -- and the remembered entries are layered on top.
//
// A namespace that appears tomorrow gets the default rather than inheriting
// somebody's answer to a different question.

// UIState is the whole remembered interface, versioned so a future shape
// change can drop an old file instead of misreading it.
type UIState struct {
	Version int `json:"version"`
	// Folds is per kube context: folding kube-system in one cluster says
	// nothing about a different one, and pretending otherwise would carry
	// an answer across the boundary between them.
	Folds     map[string]map[string]bool `json:"folds,omitempty"`
	Theme     string                     `json:"theme,omitempty"`
	AgentSort int                        `json:"agentSort,omitempty"`
	Notify    bool                       `json:"notify,omitempty"`
	Watches   []Watch                    `json:"watches,omitempty"`
	Bird      *bool                      `json:"bird,omitempty"`
	Mascot    string                     `json:"mascot,omitempty"`
	// Guest is the character on the splash, set separately from the one in
	// the corner. They were one setting, and one setting cannot express
	// the splash guest pays its respects while the corner keeps its own.
	Guest         string `json:"guest,omitempty"`
	Mouse         *bool  `json:"mouse,omitempty"`
	SafeMode      *bool  `json:"safeMode,omitempty"`
	AgentSortDesc bool   `json:"agentSortDesc,omitempty"`
}

const uiStateVersion = 1

var (
	uiMu    sync.Mutex
	uiState = UIState{Version: uiStateVersion}
	// OFF until main turns it on. Persistence is opt-in by the binary, not
	// by the package: a test that drives the update loop would otherwise
	// write to the operator's real preferences and, worse, leak folds from
	// one test into the next through a package-level map.
	//
	// Found exactly that way -- three tests started failing the moment
	// folds were remembered.
	uiOn bool
)

// uiStatePath honours XDG_STATE_HOME. State, not cache: a cache is a copy of
// something authoritative elsewhere, and there is nowhere else this lives.
func uiStatePath() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "puffin", "ui.json")
	}
	return filepath.Join(
		os.Getenv("HOME"),
		".local",
		"state",
		"puffin",
		"ui.json",
	)
}

// loadUIState reads what was remembered. Every failure is silent and lands
// on the defaults: a preferences file is never worth an error message, and
// an unreadable one must not stop puffin from starting.
func loadUIState() {
	uiMu.Lock()
	defer uiMu.Unlock()
	uiOn = os.Getenv("PUFFIN_NO_STATE") == ""
	if !uiOn {
		return
	}
	b, err := os.ReadFile(uiStatePath())
	if err != nil {
		return
	}
	var s UIState
	if json.Unmarshal(b, &s) != nil || s.Version != uiStateVersion {
		// a shape puffin does not know is discarded, not guessed at
		return
	}
	if s.Folds == nil {
		s.Folds = map[string]map[string]bool{}
	}
	uiState = s
}

// saveUIState writes through a temp file and renames, so an interrupted
// write leaves the previous preferences rather than a truncated file that
// the next start silently discards.
//
// It also merges before writing, because this tool is good enough to run in
// several windows at once. Two instances, last writer wins: fold a namespace in
// one, cycle the theme in the other, and the second writes the map it loaded at
// startup over your fold.
//
// Nothing is corrupted and the answer is simply wrong -- the worst way to lose
// a preference, because there is nothing to notice.
//
// The merge is per-field rather than per-file: whatever this instance has
// touched wins, and everything it has not touched is taken from whatever is
// on disk now. That is right for preferences, where the fields are
// independent, and would be wrong for anything with invariants across them.
func saveUIState() {
	uiMu.Lock()
	defer uiMu.Unlock()
	if !uiOn {
		return
	}
	path := uiStatePath()
	mergeFromDisk(path)
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	uiState.Version = uiStateVersion
	b, err := json.MarshalIndent(uiState, "", "  ")
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0o644) != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// rememberFold records one fold decision for one cluster.
func rememberFold(context, key string, folded bool) {
	uiMu.Lock()
	if !uiOn {
		uiMu.Unlock()
		return
	}
	if uiState.Folds == nil {
		uiState.Folds = map[string]map[string]bool{}
	}
	if uiState.Folds[context] == nil {
		uiState.Folds[context] = map[string]bool{}
	}
	uiState.Folds[context][key] = folded
	touched["fold:"+context+"/"+key] = true
	uiMu.Unlock()
	saveUIState()
}

// applyFolds layers what was remembered over the rule's own defaults, so a
// namespace nobody has an opinion about gets the default and one they closed
// last week stays closed.
func applyFolds(context string, defaults map[string]bool) map[string]bool {
	uiMu.Lock()
	defer uiMu.Unlock()
	for k, v := range uiState.Folds[context] {
		defaults[k] = v
	}
	return defaults
}

// rememberTheme and rememberAgentSort are the other two decisions worth
// keeping. PUFFIN_THEME still wins: an environment variable is a deliberate
// statement about this run and outranks what was true last time.
func rememberTheme(name string) {
	uiMu.Lock()
	if !uiOn {
		uiMu.Unlock()
		return
	}
	uiState.Theme = name
	touched["theme"] = true
	uiMu.Unlock()
	saveUIState()
}

// rememberedTheme is the theme last cycled to, empty when none was.
func rememberedTheme() string {
	uiMu.Lock()
	defer uiMu.Unlock()
	return uiState.Theme
}

// rememberMascot and rememberedMascot are the corner bird's own choice of
// character, the same shape as theme: PUFFIN_MASCOT still wins over what was
// true last time, a deliberate statement about THIS run.
func rememberMascot(name string) {
	uiMu.Lock()
	if !uiOn {
		uiMu.Unlock()
		return
	}
	uiState.Mascot = name
	touched["mascot"] = true
	uiMu.Unlock()
	saveUIState()
}

// rememberGuest records the splash guest, which is a separate setting
// from the corner companion and was once the same one.
func rememberGuest(name string) {
	uiMu.Lock()
	if !uiOn {
		uiMu.Unlock()
		return
	}
	uiState.Guest = name
	touched["guest"] = true
	uiMu.Unlock()
	saveUIState()
}

// rememberedGuest is the splash guest last chosen, empty when none was.
func rememberedGuest() string {
	uiMu.Lock()
	defer uiMu.Unlock()
	return uiState.Guest
}

// rememberedMascot is the corner character last cycled to.
func rememberedMascot() string {
	uiMu.Lock()
	defer uiMu.Unlock()
	return uiState.Mascot
}

// rememberAgentSort stores the sort as an index, which is why the sorter
// list may only ever be appended to.
func rememberAgentSort(by int, desc bool) {
	uiMu.Lock()
	if !uiOn {
		uiMu.Unlock()
		return
	}
	uiState.AgentSort, uiState.AgentSortDesc = by, desc
	touched["sort"] = true
	uiMu.Unlock()
	saveUIState()
}

// rememberedAgentSort is the stored sort index and direction.
func rememberedAgentSort() (int, bool) {
	uiMu.Lock()
	defer uiMu.Unlock()
	return uiState.AgentSort, uiState.AgentSortDesc
}

// itoa keeps this file free of strconv, which it would otherwise import
// for one summary line.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// rememberNotify keeps the notification choice across runs: turning them on
// once should not be a thing you do every morning.
func rememberNotify(on bool) {
	uiMu.Lock()
	if !uiOn {
		uiMu.Unlock()
		return
	}
	uiState.Notify = on
	touched["notify"] = true
	uiMu.Unlock()
	saveUIState()
}

// rememberedNotify is whether notifications were turned on, which is off
// until somebody says otherwise.
func rememberedNotify() bool {
	uiMu.Lock()
	defer uiMu.Unlock()
	return uiState.Notify
}

// rememberWatches keeps standing instructions across runs: "tell me when the
// migration finishes" should outlive the session it was typed in.
func rememberWatches(list []Watch) {
	uiMu.Lock()
	if !uiOn {
		uiMu.Unlock()
		return
	}
	uiState.Watches = list
	touched["watches"] = true
	uiMu.Unlock()
	saveUIState()
}

// rememberedWatches are the standing instructions from previous runs.
func rememberedWatches() []Watch {
	uiMu.Lock()
	defer uiMu.Unlock()
	return uiState.Watches
}

// rememberedSafeMode defaults on. The cost of an extra keystroke is a
// keystroke; the cost of not being asked is a flag flipped on the wrong
// service at three in the morning.
//
// Read here and written nowhere: puffin has no key that turns it off, so
// the way to turn it off is to say so in ui.json. A setter nobody calls
// would be a claim that there is a key.
func rememberedSafeMode() bool {
	uiMu.Lock()
	defer uiMu.Unlock()
	if uiState.SafeMode == nil {
		return true
	}
	return *uiState.SafeMode
}

// rememberMouse keeps mouse reporting on or off across runs.
//
// It exists because reporting is a trade, not a feature: while puffin is
// listening for clicks the terminal cannot select text, so drag-to-copy stops
// working and the tool looks broken in a way that has nothing to do with
// puffin.
//
// Ghostty's shift-drag bypasses it, and expecting somebody to know that is not
// a design.
func rememberMouse(on bool) {
	uiMu.Lock()
	if !uiOn {
		uiMu.Unlock()
		return
	}
	uiState.Mouse = &on
	touched["mouse"] = true
	uiMu.Unlock()
	saveUIState()
}

// rememberedMouse is whether puffin holds the mouse. Unset means yes:
// clicking and the wheel are worth more than copying, by default.
func rememberedMouse() bool {
	uiMu.Lock()
	defer uiMu.Unlock()
	if uiState.Mouse == nil {
		// clicking and the wheel are worth more than copying, by
		// default
		return true
	}
	return *uiState.Mouse
}

// rememberedBird is whether the corner sprite is drawn at all. Read here
// and written nowhere, the same as safe mode: PUFFIN_BIRD decides it for
// a run, and ui.json decides it for good.
func rememberedBird() bool {
	uiMu.Lock()
	defer uiMu.Unlock()
	if uiState.Bird == nil {
		return true // on by default, until somebody says otherwise
	}
	return *uiState.Bird
}

// touched records which fields this instance has an opinion about. Only
// those are written over what is on disk.
var touched = map[string]bool{}

// mergeFromDisk folds in another instance's preferences before writing.
// Called with uiMu held.
func mergeFromDisk(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var other UIState
	if json.Unmarshal(b, &other) != nil || other.Version != uiStateVersion {
		return
	}
	if !touched["theme"] && other.Theme != "" {
		uiState.Theme = other.Theme
	}
	if !touched["sort"] {
		uiState.AgentSort, uiState.AgentSortDesc =
			other.AgentSort, other.AgentSortDesc
	}
	if !touched["notify"] && other.Notify {
		uiState.Notify = other.Notify
	}
	if !touched["safeMode"] && other.SafeMode != nil {
		uiState.SafeMode = other.SafeMode
	}
	if !touched["mouse"] && other.Mouse != nil {
		uiState.Mouse = other.Mouse
	}
	if !touched["bird"] && other.Bird != nil {
		uiState.Bird = other.Bird
	}
	if !touched["mascot"] && other.Mascot != "" {
		uiState.Mascot = other.Mascot
	}
	if !touched["guest"] && other.Guest != "" {
		uiState.Guest = other.Guest
	}
	if !touched["watches"] && len(other.Watches) > 0 {
		uiState.Watches = other.Watches
	}
	// folds merge per context and per KEY: two windows looking at the same
	// cluster are the case this exists for, and a fold this instance never
	// touched should survive the other one saving
	for ctx, keys := range other.Folds {
		if uiState.Folds == nil {
			uiState.Folds = map[string]map[string]bool{}
		}
		if uiState.Folds[ctx] == nil {
			uiState.Folds[ctx] = map[string]bool{}
		}
		for k, v := range keys {
			if !touched["fold:"+ctx+"/"+k] {
				uiState.Folds[ctx][k] = v
			}
		}
	}
}
