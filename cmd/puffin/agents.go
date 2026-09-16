package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// The agents pane: what the Claude sessions on this machine have been doing.
//
// Claude Code writes one JSON Lines transcript per session under
// ~/.claude/projects/<slugged-cwd>/<session-id>.jsonl, and every assistant
// line carries the model and the token usage for that turn. That is the
// whole data source: no daemon, no configuration, nothing to install.
//
// Two honesties are built in rather than bolted on.
//
// First, this reads this laptop. It is a host-local view and the pane says
// so, because a screen in a mesh tool that looks like a fleet view and is
// not one is worse than no screen.
//
// Second, a recently written transcript is not a running process. The file says
// when bytes last landed in it, and that is all it says, so the column is "last
// log" and not "status". A session that died mid-turn and one that is thinking
// look identical from here, and puffin does not get to guess between them.

// AgentSession is one Claude Code session, reduced to what an operator reads.
type AgentSession struct {
	ID   string
	Path string
	Cwd  string // the full working directory: the join key to tmux
	Tmux TmuxPane
	// panes sharing that directory; >1 means the target is a pick
	TmuxCount int
	// TmuxShared names the pane a newer session in the same directory
	// holds. This session is not addressable: it is not what is typing in
	// there.
	TmuxShared string
	Project    string // the directory the session was working in
	// Name is the session's agent name -- the address other sessions use
	// to message it. Empty is a real answer and a useful one: a session
	// with no name cannot be reached, so the dash in this column means
	// "not addressable" rather than "not known".
	Name   string
	Branch string
	Model  string
	// Turns is what a person typed. Steps is what the model produced.
	//
	// These were one number called "turns", counting assistant records, and
	// it was wrong by one to two orders of magnitude as a description of a
	// conversation. Measured across one machine: 11702 assistant records
	// against 704 typed prompts in one project, 1769 against 11 in another.
	//
	// Counting tool calls as turns is a defensible measurement of
	// something; it is not the number anybody means by the word.
	//
	// So "turns" is reclaimed for the meaning everybody already has, and
	// the agentic loop's own count gets its own honest name. The ratio
	// between them is the interesting figure -- see Autonomy.
	Turns int
	Steps int
	// harness-authored turns: errors, login prompts, interrupts
	Synthetic int
	// Context is what the LAST request actually carried: input plus both
	// cache halves, which is the whole prompt the model was sent. Peak is
	// the largest this session has reached.
	//
	// The two differ because compaction drops the prompt back down, and a
	// session sitting at 200k that has twice been to 999k has a different
	// story from one that has never been near the ceiling.
	Context int64
	Peak    int64
	// Effort is what the harness asked for on the last turn. It rides in
	// the model cell rather than a column of its own: "opus-5/xhigh" is one
	// fact about how a session is being run, and splitting it across two
	// columns would make the reader join it back up.
	Effort string
	// LastKind is what the session did LAST: a tool call, a sentence, or a
	// message from you.
	//
	// It is the difference between an agent that is mid-loop and one that
	// has finished its turn and is waiting -- which is the whole question
	// "does this need me" reduces to, and it is answerable from the
	// transcript without any hook.
	LastKind string
	// From Claude Code's own cost-state record: the dollars it computed and
	// the lines it changed. Not puffin's arithmetic -- the harness writes
	// this, which is why the agents pane can show money at all.
	CostUSD      float64
	LinesAdded   int
	LinesRemoved int
	CostKnown    bool
	// Wall and API are the two clocks the harness keeps, and they answer
	// different questions. Wall is how long this session has existed; a
	// session open for two days is a two-day wall figure.
	//
	// API is how long it spent waiting on a model, which on a long session
	// is a small fraction of it. Reporting only one of them makes a session
	// that sat idle for three days look like one that worked for three
	// days.
	//
	// Both ride the cost record, so both inherit its staleness: see CostAt.
	Wall      time.Duration
	API       time.Duration
	InputTok  int64
	OutputTok int64
	CacheRead int64
	CacheMade int64
	LastWrite time.Time
	// LastHuman is when a person last typed into this session, which is a
	// different fact from when it last wrote a line.
	//
	// A session can be busy for a week with nobody driving it: a session
	// whose last typed prompt was eight days ago can still be writing
	// today, growing its context the whole time.
	//
	// "idle" does not say that and neither does "working", and the
	// distinction is the whole reason to look at the column.
	//
	// Zero means no typed prompt was found, which for a long transcript
	// means the same thing as a very old one and is rendered as such.
	LastHuman time.Time
	// CostAt is when the cost figure was written, which is not when it was
	// read and is often not recent.
	//
	// The harness flushes a cost-state at checkpoints and at exit, not per
	// turn, so a live session's cost is whatever was last written -- and
	// the record carries no timestamp of its own. It is dated here by
	// position: the newest timestamped record at or before it in the file.
	//
	// A client that stops returning telemetry leaves the last figure
	// standing. One seen here was thirteen hours stale, on a session that
	// had done half its work since, and puffin rendered it like any other
	// number.
	CostAt time.Time
	// CostRuns is how many runs the figure was assembled from. More than
	// one means the session was resumed, which is the case that made the
	// cost appear to fall.
	CostRuns int
}

// Tokens is everything charged for this session, cache included.
func (s AgentSession) Tokens() int64 {
	return s.InputTok + s.OutputTok + s.CacheRead + s.CacheMade
}

// AgentView is the pane's data.
type AgentView struct {
	Host     string
	Root     string
	Window   time.Duration
	Files    int  // transcripts in the window
	ReRead   int  // how many were actually read this pass, the rest cached
	Tmux     bool // a tmux server answered
	Read     time.Time
	Sessions []AgentSession
	Warnings []string
}

// agentWindow is how far back a transcript still counts as interesting, and
// agentFileCap bounds the read: a laptop with 2400 transcripts must not turn
// one keystroke into a filesystem sweep.
const (
	agentWindow  = 24 * time.Hour
	agentFileCap = 60
)

// claudeRoot honours CLAUDE_CONFIG_DIR the way Claude Code itself does.
func claudeRoot() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	return filepath.Join(os.Getenv("HOME"), ".claude")
}

// agentCache remembers what each transcript said and when it last changed. The
// pane refreshes itself every few seconds, and re-reading sixty multi-megabyte
// transcripts on every tick to discover that four of them moved is heat, not
// work.
//
// A transcript is unchanged when its mtime AND its size both match what was
// cached.
//
// Mtime alone is not enough and that was a real bug, not a theoretical one: a
// filesystem whose mtime granularity is coarser than the interval between
// appends hands back the same timestamp for a file that grew, and puffin then
// follows a live session by showing an old one.
//
// Measured on overlayfs in a container; APFS was fine, which is how it survived
// this long.
//
// Size closes it because a transcript is append-only JSONL: it grows or it
// does not change. A file rewritten in place to exactly its old length
// inside one mtime tick is the case this still misses, and the harness does
// not write that.
//
// Both facts come from the stat the pass already does, so the promise this
// cache was written around -- one stat per file per tick, no re-read --
// costs the same as it did.
type agentCache struct{ seen map[string]cachedSession }

type cachedSession struct {
	mod  time.Time
	size int64
	s    AgentSession
}

// newAgentCache starts a cache with its map made, because a nil map here
// panics on the first write rather than on the first read.
func newAgentCache() *agentCache {
	return &agentCache{seen: map[string]cachedSession{}}
}

// fetchAgents reads the recent transcripts with no cache: the one-shot form,
// for the live suite and the tests.
func fetchAgents(window time.Duration) AgentView {
	return newAgentCache().read(window)
}

// read reads the recent transcripts. Every failure is a warning and the rest
// still renders: one unreadable transcript is not a blank screen.
func (c *agentCache) read(window time.Duration) AgentView {
	v := AgentView{
		Window: window,
		Root:   filepath.Join(claudeRoot(), "projects"),
		Read:   time.Now(),
	}
	v.Host, _ = os.Hostname()
	paths, err := filepath.Glob(filepath.Join(v.Root, "*", "*.jsonl"))
	if err != nil {
		v.Warnings = append(
			v.Warnings,
			"cannot list transcripts: "+err.Error(),
		)
		return v
	}
	cut := time.Now().Add(-window)
	type stamped struct {
		path string
		mod  time.Time
		size int64
	}
	var recent []stamped
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil || fi.ModTime().Before(cut) {
			continue
		}
		recent = append(recent, stamped{p, fi.ModTime(), fi.Size()})
	}
	sort.Slice(
		recent,
		func(
			i,
			j int,
		) bool {
			return recent[i].mod.After(recent[j].mod)
		},
	)
	v.Files = len(recent)
	if len(recent) > agentFileCap {
		v.Warnings = append(v.Warnings, fmt.Sprintf(
			"%d transcripts in the window, reading the %d newest",
			len(recent),
			agentFileCap,
		))
		recent = recent[:agentFileCap]
	}
	live := map[string]bool{}
	for _, r := range recent {
		live[r.path] = true
		var s AgentSession
		hit, ok := c.seen[r.path]
		if ok && hit.mod.Equal(r.mod) && hit.size == r.size {
			s = hit.s
		} else {
			read, err := readTranscript(r.path)
			if err != nil {
				v.Warnings = append(
					v.Warnings,
					filepath.Base(r.path)+": "+err.Error(),
				)
				continue
			}
			s = read
			v.ReRead++
		}
		s.LastWrite = r.mod
		c.seen[r.path] = cachedSession{mod: r.mod, size: r.size, s: s}
		if s.Steps == 0 {
			// a transcript the model has never answered in is not a
			// session yet. Steps rather than Turns: a typed prompt
			// with no reply is exactly the case this is here to
			// skip.
			continue
		}
		v.Sessions = append(v.Sessions, s)
	}
	sort.Slice(v.Sessions, func(i, j int) bool {
		return v.Sessions[i].LastWrite.After(v.Sessions[j].LastWrite)
	})
	// the tmux join: which pane, if any, each session is running in
	if panes, err := tmuxPanes(); err != nil {
		v.Warnings = append(v.Warnings, "tmux: "+err.Error())
	} else if len(panes) > 0 {
		byPath, n := tmuxByPath(panes)
		// The join runs one way: a pane belongs to at most ONE session
		// -- the most recently written in that directory, and the
		// sessions are already in that order.
		//
		// Four sessions have run in one checkout today and exactly one
		// of them is the thing typing in that pane.
		//
		// Handing the same address to all four means "say something to
		// this agent" can deliver a sentence into a live pane while you
		// believe you are addressing a session that ended yesterday.
		//
		// The rest are marked as sharing the directory, which is true
		// and harmless.
		taken := map[string]bool{}
		for i := range v.Sessions {
			cwd := v.Sessions[i].Cwd
			p, ok := byPath[cwd]
			if !ok {
				continue
			}
			if taken[cwd] {
				v.Sessions[i].TmuxShared = p.Target
				continue
			}
			taken[cwd] = true
			v.Sessions[i].Tmux = p
			v.Sessions[i].TmuxCount = n[cwd]
		}
		v.Tmux = true
	}
	// transcripts that fell out of the window are dropped from the cache
	// too: a pane left open all day must not grow a map of every session
	// this laptop has ever run
	for path := range c.seen {
		if !live[path] {
			delete(c.seen, path)
		}
	}
	return v
}

// transcriptLine is one record of a session transcript, reduced to the
// fields puffin reads. Named rather than declared inside the loop that
// reads it, because four levels of anonymous struct is four levels of
// indent for the field that matters least.
type transcriptLine struct {
	Type      string `json:"type"`
	Cwd       string `json:"cwd"`
	GitBranch string `json:"gitBranch"`
	AgentName string `json:"agentName"`
	// promptSource distinguishes a person typing from the harness
	// queueing or a system message. Only "typed" is a human.
	PromptSource string `json:"promptSource"`
	Timestamp    string `json:"timestamp"`
	// effort is top-level on an assistant record, not inside the message:
	// the harness records what it asked for, beside what came back.
	Effort  string         `json:"effort"`
	Message transcriptBody `json:"message"`
}

// transcriptBody is the message the model produced, and what it cost.
type transcriptBody struct {
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
	Usage   usageCounts     `json:"usage"`
}

// usageCounts is the token accounting the harness writes per message. The
// two cache figures are the reason a turn count and a token count say
// different things about the same conversation.
type usageCounts struct {
	Input     int64 `json:"input_tokens"`
	Output    int64 `json:"output_tokens"`
	CacheRead int64 `json:"cache_read_input_tokens"`
	CacheMade int64 `json:"cache_creation_input_tokens"`
}

// readTranscript sums one session's turns. Unparseable lines are skipped
// rather than fatal: the format is somebody else's and it will change.
func readTranscript(path string) (AgentSession, error) {
	f, err := os.Open(path)
	if err != nil {
		return AgentSession{}, err
	}
	defer f.Close()
	s := AgentSession{
		ID:   strings.TrimSuffix(filepath.Base(path), ".jsonl"),
		Path: path,
	}
	// the newest timestamp read so far, for dating cost-state
	var seen time.Time
	// Cost is per run, not per session.
	//
	// A resumed session starts a new cost-state series with a fresh
	// startTime and a counter that begins again at zero. Taking the last
	// record therefore reports the current run's spend and silently
	// discards every earlier one.
	//
	// One session went from $384.28 at 3,684 turns to $115.29 at 7,137,
	// which is the cost appearing to fall while the work doubled.
	//
	// So: the latest figure per run, summed. Keyed on startTime because
	// that is what actually identifies a run; the records carry nothing
	// else that distinguishes one from the next.
	costPerRun := map[int64]float64{}
	addedPerRun := map[int64]int{}
	wallPerRun := map[int64]int64{}
	apiPerRun := map[int64]int64{}
	removedPerRun := map[int64]int{}
	sc := bufio.NewScanner(f)
	// transcript lines carry whole tool results and run long
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		var line transcriptLine
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue
		}
		if line.Cwd != "" {
			s.Cwd, s.Project = line.Cwd, projectName(line.Cwd)
		}
		if line.GitBranch != "" && line.GitBranch != "HEAD" {
			s.Branch = line.GitBranch
		}
		// last one wins: a session can be renamed while it runs, and
		// the name that matters is the one it answers to now
		if line.AgentName != "" {
			s.Name = line.AgentName
		}
		// a person typing is a turn, wherever it falls in the file and
		// whether or not the record carries a timestamp
		if line.Type == "user" && line.PromptSource == "typed" {
			s.Turns++
		}
		if line.Timestamp != "" {
			if ts, err := time.Parse(
				time.RFC3339,
				line.Timestamp,
			); err == nil {
				if ts.After(seen) {
					seen = ts
				}
				if line.PromptSource == "typed" &&
					ts.After(s.LastHuman) {
					s.LastHuman = ts
				}
			}
		}
		if line.Type == "cost-state" {
			// cumulative snapshots: the last one wins
			var cs struct {
				TotalCostUSD float64 `json:"totalCostUSD"`
				LinesAdded   int     `json:"totalLinesAdded"`
				LinesRemoved int     `json:"totalLinesRemoved"`
				StartTime    int64   `json:"startTime"`
				// milliseconds, both of them
				TotalDuration int64 `json:"totalDuration"`
				APIDuration   int64 `json:"totalAPIDuration"`
			}
			if json.Unmarshal(sc.Bytes(), &cs) == nil {
				// The highest figure within a run, not the last
				// one.
				//
				// A provider that resets a counter overnight
				// breaks any reader that assumes a number only
				// climbs, and last-wins is exactly that reader.
				//
				// Max cannot report less than something already
				// seen, which is the property that matters when
				// the thing on the other end is free to start
				// over.
				//
				// keyed on the run's start, which is the only
				// thing in these records that identifies one
				run := cs.StartTime
				if cs.TotalCostUSD > costPerRun[run] {
					costPerRun[run] = cs.TotalCostUSD
				}
				if cs.LinesAdded > addedPerRun[run] {
					addedPerRun[run] = cs.LinesAdded
				}
				if cs.LinesRemoved > removedPerRun[run] {
					removedPerRun[run] = cs.LinesRemoved
				}
				// max within the run, for the same reason the
				// cost is: a counter that is assumed to climb
				// reports nonsense the day it does not
				if cs.TotalDuration > wallPerRun[run] {
					wallPerRun[run] = cs.TotalDuration
				}
				if cs.APIDuration > apiPerRun[run] {
					apiPerRun[run] = cs.APIDuration
				}
				s.CostKnown = true
				// dated by position: the record has no
				// timestamp, so it takes the newest one seen at
				// or before it
				s.CostAt = seen
			}
			continue
		}
		if line.Type != "assistant" {
			continue
		}
		// <synthetic> is Claude Code speaking in the assistant's voice
		// -- "Not logged in", an interrupt notice, an API error --
		// rather than anything a model produced. Its usage is all
		// zeros.
		//
		// Letting it set the model overwrites the real one with a word
		// that names no model, which is exactly how it turned up on
		// this screen.
		if line.Message.Model != "" &&
			line.Message.Model != "<synthetic>" {
			s.Model = line.Message.Model
		}
		if line.Effort != "" {
			s.Effort = line.Effort
		}
		if line.Message.Model == "<synthetic>" {
			s.Synthetic++
			continue
		}
		s.Steps++
		s.LastKind = lastBlockKind(line.Message.Content)
		// the prompt for this request: everything the model was sent,
		// cached or not. Cache reads are still context -- cheaper, not
		// absent -- and dropping them understates a long session by an
		// order of magnitude.
		u := line.Message.Usage
		s.Context = u.Input + u.CacheRead + u.CacheMade
		if s.Context > s.Peak {
			s.Peak = s.Context
		}
		s.InputTok += line.Message.Usage.Input
		s.OutputTok += line.Message.Usage.Output
		s.CacheRead += line.Message.Usage.CacheRead
		s.CacheMade += line.Message.Usage.CacheMade
	}
	if err := sc.Err(); err != nil {
		return s, err
	}
	// every run's spend, added up: the session's cost is what it cost, not
	// what the current run has cost so far
	for st, c := range costPerRun {
		s.CostUSD += c
		s.Wall += time.Duration(wallPerRun[st]) * time.Millisecond
		s.API += time.Duration(apiPerRun[st]) * time.Millisecond
		s.LinesAdded += addedPerRun[st]
		s.LinesRemoved += removedPerRun[st]
	}
	s.CostRuns = len(costPerRun)
	return s, nil
}

// humanCount renders token counts at a glance. Tokens, never dollars: this
// estate has no price table and puffin will not invent one -- the same rule
// that keeps the roster's `$` an expensive-flag count and not a meter.
func humanCount(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprintf("%d", n)
}

// shortModel drops the vendor prefix nobody reads twice.
func shortModel(m string) string {
	m = strings.TrimPrefix(m, "claude-")
	if i := strings.Index(m, "-2"); i > 0 { // strip a trailing date stamp
		m = m[:i]
	}
	return m
}

// agentsFetched carries the read back into the loop.
type agentsFetched struct{ v AgentView }

type agentPane struct {
	v       AgentView
	cache   *agentCache
	tail    []TailLine
	tailFor string
	tailErr string
	cursor  int
	offset  int
	// the CRT's other channel: what the tmux pane is actually showing
	live bool
	// sorting: which column, and which way. The cursor follows the session
	// by id, so re-sorting moves the rows and keeps your selection -- which
	// is the whole reason that was built before this.
	sortBy   int
	sortDesc bool
	// what each session was doing last time we looked, so a notification
	// fires on the transition into waiting rather than every five seconds
	// for as long as it stays there
	was map[string]string
	// filter shows one model's sessions only. Clicking the "fable-5 x4"
	// chip in the summary is the fastest way to ask "what is fable doing",
	// which on a busy laptop is the actual question.
	filter string
	// layout is recorded while rendering so a click can be resolved against
	// what was actually drawn, rather than against a second calculation of
	// where things should have been. Two calculations drift; one does not.
	hitHeader []hitBox
	hitChips  []hitBox
	// detail replaces the tail with everything about the selected session
	// that does not fit a column -- including the scratchpad, which lives
	// under /private/tmp and is otherwise invisible.
	detail bool

	hitTop  int // pane row where the session list starts
	hitRows int // how many session rows were drawn
	// tagged sessions, by id.
	//
	// Acting on several sessions at once means marking them first, so
	// space tags and the verb acts on the tagged set.
	//
	// By ID and not by row, because the list re-sorts under you every five
	// seconds -- a tag held by position would drift onto whatever moved
	// into that row, which on a screen whose next keystroke is a kill is
	// not an acceptable failure.
	//
	// An id that leaves the list takes its tag with it.
	tagged map[string]bool
	// pending is a confirmation waiting for its randomised key.
	pending *pending
	// the little screen scrolls, and follows the bottom until you scroll
	// away from it -- the same bargain tail -f makes
	tailOff    int
	tailFollow bool
	// writing to an agent: the composer, and the confirmation an interrupt
	// has to pass. Saying something to a working agent is a change, and
	// changes are confirmed here for the reason they are on the cluster
	// screen -- one deliberate keystroke, never a stray one.
	compose textarea.Model
	writing bool
	ask     *tmuxAsk
	note    string
	// sel is the session under the cursor, remembered by id.
	//
	// The list is sorted by who wrote last, so on a screen that refreshes
	// itself the rows genuinely move
	//
	// -- and a cursor that holds an index ends up on a different session
	// than the one you were reading, which on a screen that can kill things
	// is the difference between an interruption and an accident.
	sel string
}

// Tick is the pane's own cadence. Five seconds is chosen against what it
// watches: a transcript gains a turn every few seconds when a session is
// working, and the read is cached by mtime, so a tick that finds nothing
// changed costs a stat per file and nothing else.
func (p *agentPane) Tick() time.Duration { return 5 * time.Second }

// Key opens the agents pane from the roster.
func (p *agentPane) Key() string { return "a" }

// Title names the pane, and is what its refresh gate is keyed by, so two
// panes must never share one.
func (p *agentPane) Title() string { return "agents" }

// Help is the footer: the sorts, the tags and the kill ladder, which are
// the keys nobody guesses.
func (p *agentPane) Help() string {
	return "j/k: move · enter/d: detail · space: tag · w: write · Q: " +
		"/quit · i: stop generating · X: C-c · T: term · 9: kill -9 " +
		"· tab: transcript/live\n" +
		"model reads model/effort · peak is the fullest its context " +
		"has been, so a peak above context means it " +
		"compacted\n" +
		"s: sort · S: reverse · !: notifications · W: watch the " +
		"current filter · click a header to sort, a chip to " +
		"filter · q: back · \"last log\" is when it last " +
		"wrote to disk, which is not proof it is still running"
}

// Load reads this host's transcripts. The domain is ignored: sessions are
// files on disk, not a service that can be addressed elsewhere.
func (p *agentPane) Load(string) tea.Cmd {
	if p.cache == nil {
		p.cache = newAgentCache()
		p.sortBy, p.sortDesc = rememberedAgentSort()
		if p.sortBy >= len(agentSorts) {
			p.sortBy = 0
		}
	}
	c := p.cache
	return func() tea.Msg { return agentsFetched{c.read(agentWindow)} }
}

// Update folds in a refresh or a keystroke. The tagged set survives a
// refresh because it is keyed by session id rather than by row.
func (p *agentPane) Update(msg tea.Msg) (pane, tea.Cmd) {
	switch msg := msg.(type) {
	case agentActed:
		p.note, p.tagged = msg.note, nil
		return p, p.Load("")
	case agentsFetched:
		p.v = msg.v
		// follow the session, not the row: the sort moves under a
		// refresh
		found := false
		if p.sel != "" {
			for i, s := range p.visible() {
				if s.ID == p.sel {
					p.cursor, found = i, true
					break
				}
			}
		}
		if !found {
			if n := len(p.visible()); p.cursor >= n {
				p.cursor = max(0, n-1)
			}
			p.sel = p.selected()
		}
		p.sortSessions()
		p.announce()
		return p, p.watch()

	case tailFetched:
		// a tail that arrives for a session the cursor has already left
		// is dropped rather than shown under the wrong name
		if msg.id != p.sel {
			return p, nil
		}
		sameSession := p.tailFor == msg.id
		p.tail, p.tailFor, p.tailErr = msg.lines, msg.id, ""
		if msg.err != nil {
			p.tailErr = msg.err.Error()
		}
		// following means new lines push the view along; scrolled back
		// means they do not, and a screen that yanks you to the bottom
		// while you are reading is worse than no screen
		if !sameSession || p.tailFollow {
			p.tailOff = maxInt(0, len(p.tail)-tailLines)
			p.tailFollow = true
		}
		return p, nil

	case tmuxDone:
		p.note = msg.note
		if msg.err != nil {
			p.note = msg.err.Error()
		}
		return p, p.watch()

	case tea.KeyMsg:
		// sorting first: it is a view change and never reaches the list
		if !p.writing && p.ask == nil {
			switch msg.String() {
			case "s":
				p.sortBy = (p.sortBy + 1) % len(agentSorts)
				p.sortSessions()
				rememberAgentSort(p.sortBy, p.sortDesc)
				return p, nil
			case "S":
				p.sortDesc = !p.sortDesc
				p.sortSessions()
				rememberAgentSort(p.sortBy, p.sortDesc)
				return p, nil
			}
		}
		// the composer owns typing while it is open
		if p.writing {
			switch msg.String() {
			case "esc":
				p.writing, p.note = false, "not sent"
				return p, nil
			case "enter":
				sess, ok := p.session()
				if !ok || sess.Tmux.Target == "" {
					p.writing, p.note = false, "no tmux "+
						"pane for that session"
					return p, nil
				}
				text := strings.TrimSpace(p.compose.Value())
				p.writing = false
				return p, sendCmd(sess.Tmux.Target, text)
			}
			var cmd tea.Cmd
			p.compose, cmd = p.compose.Update(msg)
			return p, cmd
		}
		// an interrupt waits for a deliberate y; anything else is no
		if p.ask != nil {
			a := *p.ask
			p.ask = nil
			if msg.String() == "y" {
				return p, keyCmd(a.target, a.key, a.label)
			}
			p.note = "cancelled"
			return p, nil
		}
		// a confirmation owns the keyboard while it is up, and the key
		// it wants is different every time -- see confirmKeyFor.
		// Anything else cancels, which is the safe direction for a
		// wrong guess.
		if p.pending != nil {
			pend := p.pending
			p.pending = nil
			if msg.String() == pend.key {
				return p, pend.do()
			}
			p.note = "cancelled"
			return p, nil
		}
		switch msg.String() {
		case "enter":
			// enter opens what the cursor is on.
			//
			// That is the roster's rule -- enter on a service opens
			// its contract -- and it was bound here only inside the
			// composer, so on the list itself the most obvious key
			// on the keyboard did nothing.
			//
			// `d` still toggles; this only ever opens, because
			// enter is not a back key.
			if !p.detail {
				p.detail = true
				if sess, ok := p.session(); ok {
					return p, scratchCmd(sess.Path)
				}
			}
			return p, nil
		case "esc":
			// the ladder is "put down what you are holding",
			// innermost first. The detail view is a thing you
			// opened over the list, so it comes off before the
			// selection underneath it.
			if p.detail {
				p.detail = false
				return p, nil
			}
			if len(p.tagged) > 0 {
				p.tagged, p.note = nil, ""
				return p, nil
			}
			if p.filter != "" {
				p.filter, p.cursor = "", 0
				p.sel = p.selected()
				return p, p.watch()
			}
		case "down", "j":
			if p.cursor < len(p.visible())-1 {
				p.cursor++
			}
			p.sel = p.selected()
			return p, p.watch()
		case "up", "k":
			if p.cursor > 0 {
				p.cursor--
			}
			p.sel = p.selected()
			return p, p.watch()
		case "pgup", "shift+up", "K":
			p.tailOff = maxInt(0, p.tailOff-tailLines)
			p.tailFollow = false
			return p, nil
		case "pgdown", "shift+down", "J":
			p.tailOff += tailLines
			if p.tailOff >= maxInt(0, len(p.tail)-tailLines) {
				p.tailOff = maxInt(0, len(p.tail)-tailLines)
				// scrolled back to the bottom: follow again
				p.tailFollow = true
			}
			return p, nil
		case "E":
			// what the companion does about the watch on the
			// current filter
			if p.filter != "" {
				cycleWatchEmote(p.filter, "agents")
			}
			return p, nil
		case "W":
			// watch whatever the filter names: click fable-5, press
			// W, and puffin tells you when a fable session changes
			// state
			if p.filter == "" {
				return p, nil
			}
			for _, w := range watchesFor("agents") {
				if w.Pattern == p.filter {
					dropWatch(p.filter, "agents")
					return p, nil
				}
			}
			addWatch(p.filter, "agents")
			enableNotify(true)
			rememberNotify(true)
			return p, nil
		case "!":
			enableNotify(!notifyEnabled())
			rememberNotify(notifyEnabled())
			return p, nil
		case "d":
			p.detail = !p.detail
			if p.detail {
				// measure off the render path; the view says
				// "measuring" until the message lands
				if sess, ok := p.session(); ok {
					return p, scratchCmd(sess.Path)
				}
			}
			return p, nil
		case "tab":
			// the CRT has two channels: what the session has DONE,
			// from its transcript, and what its pane is showing
			// right now
			p.live = !p.live
			return p, p.watch()
		case "w":
			sess, ok := p.session()
			if !ok || sess.Tmux.Target == "" {
				p.note = tmuxWhyNot(sess)
				return p, nil
			}
			ta := textarea.New()
			ta.SetHeight(1)
			ta.SetWidth(80)
			ta.Placeholder = "say something to " +
				"" + sess.Project + " in " + sess.Tmux.Target
			ta.Focus()
			p.compose, p.writing, p.note = ta, true, ""
			return p, nil
		case " ":
			// tag or untag the row under the cursor
			sess, ok := p.session()
			if !ok {
				return p, nil
			}
			if p.tagged == nil {
				p.tagged = map[string]bool{}
			}
			if p.tagged[sess.ID] {
				delete(p.tagged, sess.ID)
			} else {
				p.tagged[sess.ID] = true
			}
			// move on, so tagging three in a row is three
			// keystrokes and not six
			if p.cursor < len(p.visible())-1 {
				p.cursor++
			}
			return p, nil
		case "#":
			if len(p.tagged) == 0 {
				p.note = "nothing tagged · space tags the " +
					"row under the cursor"
				return p, nil
			}
			p.note = fmt.Sprintf("%d tagged · T: terminate · K: "+
				"kill -9 · esc: clear",
				len(p.tagged))
			return p, nil
		case "Q":
			// /quit is the gentlest rung there is and it was
			// missing: the agent exits on its OWN terms, writes its
			// state, and closes its transcript properly.
			//
			// Escape stops a thought; this ends a session and
			// leaves it resumable.
			return p.armQuit()
		case "T", "9":
			// 9 rather than K: K is already page-up here, and "9"
			// is what the rung is called out loud anyway
			sig, name := "TERM", "terminate"
			if msg.String() == "9" {
				sig, name = "KILL", "kill -9"
			}
			return p.armSignal(sig, name)
		case "i", "X":
			sess, ok := p.session()
			if !ok || sess.Tmux.Target == "" {
				p.note = tmuxWhyNot(sess)
				return p, nil
			}
			// Escape stops an agent mid-thought and it keeps its
			// context; C-c is the harder one. They are asked for by
			// name because they do very different things to work in
			// flight.
			key, label := "Escape", "stop generating"
			if msg.String() == "X" {
				key, label = "C-c", "interrupt (C-c)"
			}
			p.ask = &tmuxAsk{
				key:    key,
				label:  label,
				target: sess.Tmux.Target,
				sess:   sess.Project,
			}
			return p, nil
		}
	}
	// Moving the cursor with the detail open selects a different session,
	// and its scratchpad has not been measured. Asked for here rather than
	// at every key that can move the cursor, because that list grows and
	// the one that gets forgotten is the bug.
	//
	// scratchCmd returns nil when the answer is already known, so this
	// costs nothing on a session already measured.
	if p.detail {
		if sess, ok := p.session(); ok {
			return p, scratchCmd(sess.Path)
		}
	}
	return p, nil
}

// watch reads the tail of whatever the cursor is on. The read is off the
// main loop: it is bounded, but it is still a file.
func (p *agentPane) watch() tea.Cmd {
	s, ok := p.session()
	if !ok {
		return nil
	}
	p.tailFollow = p.tailFollow || p.tailOff == 0
	if p.live && s.Tmux.Target != "" {
		return captureCmd(s.ID, s.Tmux.Target, tailBuffer)
	}
	return tailCmd(s.ID, s.Path, tailBuffer)
}

// tmuxAsk is an interrupt waiting for a deliberate yes.
type tmuxAsk struct {
	key    string // Escape | C-c
	label  string
	target string
	sess   string
}

// Capturing says the composer owns the keyboard. Without it, q closes the
// pane in the middle of a sentence, and the message you were writing to an
// agent becomes a screen change instead.
func (p *agentPane) Capturing() bool { return p.writing }

// session is what the cursor is on.
func (p *agentPane) session() (AgentSession, bool) {
	v := p.visible()
	if p.cursor < 0 || p.cursor >= len(v) {
		return AgentSession{}, false
	}
	return v[p.cursor], true
}

// tmuxDone carries a write back into the loop.
type tmuxDone struct {
	note string
	err  error
}

// sendCmd types a line into a pane. keyCmd sends one named key.
func sendCmd(target, text string) tea.Cmd {
	return func() tea.Msg {
		if err := tmuxSend(target, text); err != nil {
			return tmuxDone{err: err}
		}
		return tmuxDone{note: "sent to " + target}
	}
}

// keyCmd sends one key to a tmux pane and reports what it did, so the
// screen can say which target was written to rather than implying one.
func keyCmd(target, key, label string) tea.Cmd {
	return func() tea.Msg {
		if err := tmuxKey(target, key); err != nil {
			return tmuxDone{err: err}
		}
		return tmuxDone{note: label + " sent to " + target}
	}
}

// captureCmd reads what the pane is showing right now -- the other channel
// for the CRT window.
func captureCmd(id, target string, n int) tea.Cmd {
	return func() tea.Msg {
		lines, err := tmuxCapture(target, n)
		out := make([]TailLine, 0, len(lines))
		for _, l := range lines {
			out = append(out, TailLine{Kind: "screen", Text: l})
		}
		return tailFetched{id: id, lines: out, err: err}
	}
}

// selected is the id under the cursor, or empty when there is nothing there.
func (p *agentPane) selected() string {
	v := p.visible()
	if p.cursor < 0 || p.cursor >= len(v) {
		return ""
	}
	return v[p.cursor].ID
}

// View draws the table and, under it, the tail of whatever the cursor is
// on. Columns are padded to fixed widths so the eye can scan down one.
func (p *agentPane) View(s Styles, width, height int) string {
	var b strings.Builder
	for _, w := range p.v.Warnings {
		b.WriteString(s.Caution.Render("! "+w) + "\n")
	}
	rows := p.visible()
	if len(p.v.Sessions) == 0 {
		b.WriteString(s.Dim.Render("no sessions wrote in the last "+
			p.v.Window.String()+" under "+p.v.Root) + "\n")
		return b.String()
	}
	var in, out, cread, cmade int64
	byModel := map[string]int{}
	for _, sess := range p.v.Sessions {
		in += sess.InputTok
		out += sess.OutputTok
		cread += sess.CacheRead
		cmade += sess.CacheMade
		byModel[shortModel(sess.Model)]++
	}
	models := make([]string, 0, len(byModel))
	for m := range byModel {
		models = append(models, m)
	}
	sort.Strings(models)
	// a self-refreshing screen has to say when it last looked, or a frozen
	// pane and a quiet estate render identically
	read := ""
	if !p.v.Read.IsZero() {
		read = " · read " + humanSince(time.Since(p.v.Read))
	}
	// the model chips are the filter control: clicking one shows just that
	// model's sessions. Their positions are recorded as they are drawn, so
	// a click lands on what was rendered rather than on a second guess at
	// where it should have been.
	lead := orDash(p.v.Host) + " · last " + p.v.Window.String() + " · "
	count := fmt.Sprintf("%d sessions", len(p.v.Sessions))
	b.WriteString(
		s.Dim.Render(
			lead,
		) + s.Accent.Render(
			count,
		) + s.Dim.Render(
			"  ",
		),
	)
	col := len(lead) + len(count) + 2
	chipY := strings.Count(b.String(), "\n")
	p.hitChips = p.hitChips[:0]
	for i, m := range models {
		chip := fmt.Sprintf("%s x%d", m, byModel[m])
		// the chips are the only line here that grows with the estate,
		// and in a split there is half the room.
		//
		// Stop while there is still margin rather than running past the
		// column: an unfinished list with a marker is readable, a torn
		// layout is not.
		if width > 0 && col+len(chip)+4 > width {
			b.WriteString(s.Dim.Render("\u2026"))
			break
		}
		style := s.Dim
		if p.filter == m {
			style = s.AccentAlt
		}
		b.WriteString(style.Render(chip))
		p.hitChips = append(
			p.hitChips,
			hitBox{x: col, w: len(chip), y: chipY, name: m},
		)
		col += len(chip)
		if i < len(models)-1 {
			b.WriteString(s.Dim.Render(" · "))
			col += 3
		}
	}
	b.WriteString(s.Dim.Render(read))
	// whether puffin will tell you about this without the screen open. Said
	// on the screen rather than hidden in a config: a tool that can send
	// desktop notifications should say so where you can see it.
	if ws := watchesFor("agents"); len(ws) > 0 {
		labels := make([]string, 0, len(ws))
		for _, w := range ws {
			labels = append(labels, w.Label())
		}
		b.WriteString(
			s.Accent.Render(
				"  · watching: " + strings.Join(labels, ", "),
			),
		)
	}
	if notifyEnabled() {
		b.WriteString(s.Ok.Render("  · notifying"))
	} else {
		b.WriteString(s.Dim.Render("  · ! to notify when an agent " +
			"starts waiting"))
	}
	b.WriteString("\n")
	// cache read is separated from fresh input on purpose: they are not the
	// same spend, and a total that hides the split flatters the number
	if p.filter != "" {
		b.WriteString(s.AccentAlt.Render("showing "+p.filter+" only") +
			s.Dim.Render(
				fmt.Sprintf(
					"  (%d of %d) · click the chip "+
						"again, or esc, to clear",
					len(rows),
					len(p.v.Sessions),
				),
			) + "\n")
	}
	b.WriteString(
		s.Dim.Render("tokens: ") + s.Row.Render(humanCount(in)+" in") +
			s.Dim.Render(
				" · ",
			) + s.Row.Render(humanCount(out)+" out") +
			s.Dim.Render(
				" · "+humanCount(
					cread,
				)+" cache read · "+humanCount(
					cmade,
				)+" cache written",
			) + "\n\n",
	)

	// A record is TWO lines, and that is what stopped columns disappearing.
	//
	// One line meant the row got wider every time a column was added, so
	// something had to be dropped to fit -- which is how "tokens" quietly
	// vanished when context, lines and cost arrived.
	//
	// Dropping a column is a silent answer to a question the operator
	// asked, and on this screen the next keystroke may be a kill.
	//
	// Split by what the columns ARE rather than by width. The first line is
	// identity and location: which checkout, what it is called, which
	// branch, which model, what it is doing, which pane, when it last
	// spoke.
	//
	// The second is the meter: turns, tokens, context, peak, lines, cost.
	// You read the first to find a session and the second to judge it, and
	// they are never needed in the same glance.
	//
	// Both lines carry the cursor's highlight, because a record is one
	// thing and a half-lit row reads as two.
	cols := p.columns(width)
	h := func(
		name string,
		w int,
	) string {
		return pad(name+p.sortMark(name), w)
	}

	head := h("project", slotProject)
	if cols.name {
		head += h("name", slotName)
	}
	if cols.branch {
		head += h("branch", slotBranch)
	}
	head += pad("model", slotModel) + pad("state", slotState) +
		pad("tmux", slotTmux) + h("last log", slotAge)
	// the meter always carries all six. When an identity column is dropped
	// from the first line its slot is gone, so the meter cell falls back to
	// its own width: alignment degrades at narrow widths, data never does.
	// Losing a column was the whole disease.
	slots := gridSlots(cols)
	meterCells := []struct {
		name    string
		natural int
	}{{"turns", 6}, {
		"tokens",
		9,
	}, {"context", 13}, {"peak", 6}, {"cost", 9}, {"lines", 11}}
	meterHead := "  "
	for i, mc := range meterCells {
		meterHead += h(mc.name, slotFor(slots, i, mc.natural))
	}
	if rest := ansi.StringWidth(
		ansi.Strip(head),
	) - ansi.StringWidth(ansi.Strip(meterHead)); rest > 0 {
		meterHead += strings.Repeat(" ", rest)
	}
	b.WriteString(s.Header.Render(head) + "\n")
	b.WriteString(s.Header.Render(meterHead) + "\n")

	// the header rows' y in pane coordinates, measured rather than counted:
	// warnings, the filter line and the summary all come and go above them,
	// and a click offset that is wrong by one sorts the wrong column. The
	// rendered text is the only thing that knows where it put itself.
	meterY := strings.Count(b.String(), "\n") - 1
	headerY := meterY - 1
	p.hitHeader = p.hitHeader[:0]
	col = 2
	add := func(name string, w int, clickable bool, y int) {
		if clickable {
			p.hitHeader = append(
				p.hitHeader,
				hitBox{x: col, w: w, y: y, name: name},
			)
		}
		col += w
	}
	add("project", slotProject, true, headerY)
	if cols.name {
		add("name", slotName, true, headerY)
	}
	if cols.branch {
		add("branch", slotBranch, true, headerY)
	}
	add("model", slotModel, false, headerY)
	add("state", slotState, false, headerY)
	add("tmux", slotTmux, false, headerY)
	add("last log", slotAge, true, headerY)
	col = 2
	for i, mc := range meterCells {
		add(mc.name, slotFor(slots, i, mc.natural), true, meterY)
	}
	// the tail window owns the bottom, and a record costs two lines now, so
	// the count of records that fit is half what it was
	size := (height - 14 - (tailLines + 4)) / linesPerRecord
	if height <= 0 {
		size = len(rows)
	} else if size < 3 {
		size = 3
	}
	off := windowOffset(len(rows), p.cursor, size, p.offset)
	p.offset = off
	last := off + size
	if last > len(rows) {
		last = len(rows)
	}
	if off > 0 {
		b.WriteString(
			s.Dim.Render(
				fmt.Sprintf("  %d more above", off),
			) + "\n",
		)
	}
	p.hitTop = strings.Count(b.String(), "\n")
	p.hitRows = (last - off) * linesPerRecord
	now := time.Now()
	for i := off; i < last; i++ {
		sess := rows[i]
		on := i == p.cursor
		// the tag and the cursor are different facts and both have to
		// be visible at once: the cursor is where you are, a tag is
		// what will be acted on, and on the screen whose next keystroke
		// may be a kill, confusing them is the failure that matters.
		marker := s.On(s.Row, on).Render("  ")
		switch {
		case p.tagged[sess.ID] && on:
			marker = s.On(s.RowSel, on).Render("▸\u2022")
		case p.tagged[sess.ID]:
			marker = s.Accent.Render(" \u2022")
		case on:
			marker = s.On(s.RowSel, on).Render("▸ ")
		}
		age := humanSince(now.Sub(sess.LastWrite))
		ageStyle := s.Dim
		if now.Sub(sess.LastWrite) < 2*time.Minute {
			ageStyle = s.Ok
		}
		// the age is padded out so the cursor's ground runs the whole
		// row: this is the screen where a stray keystroke interrupts
		// somebody's work, and the row you are about to act on has to
		// be unmistakable
		ageCell := s.On(ageStyle, on).Render(padTo(age, 12))
		// the tmux target is the address you can TALK to. Ambiguity is
		// shown rather than hidden: two panes in one directory means
		// the target was a pick, and a message sent to the wrong one of
		// two agents is worse than being asked which.
		tmuxCell := s.On(s.Dim, on).Render(pad("-", 14))
		if sess.TmuxShared != "" {
			tmuxCell = s.On(s.Dim, on).
				Render(pad("("+sess.TmuxShared+")", 14))
		}
		if sess.Tmux.Target != "" {
			label := sess.Tmux.Target
			if sess.TmuxCount > 1 {
				label += fmt.Sprintf(" +%d", sess.TmuxCount-1)
			}
			tmuxCell = s.On(s.AccentAlt, on).Render(pad(label, 14))
		}
		// fill pads a cell out to its slot in the grid, in the ROW's
		// style so the cursor highlight covers the gap. Without it the
		// second line highlighted only as far as its own content and
		// the selected record read as a wide line above a short one.
		fill := func(cell string, natural, slot int) string {
			if slot <= natural {
				return cell
			}
			return cell + s.On(s.Row, on).
				Render(strings.Repeat(" ", slot-natural))
		}

		// line one: who and where
		row := marker + s.On(s.Row, on).
			Render(pad(orDash(sess.Project), slotProject))
		if cols.name {
			row += s.On(s.AccentAlt, on).
				Render(pad(orDash(sess.Name), slotName))
		}
		if cols.branch {
			row += s.On(s.Dim, on).
				Render(pad(orDash(sess.Branch), slotBranch))
		}
		row += s.On(s.Accent, on).
			Render(pad(orDash(modelCell(sess)), slotModel)) +
			stateCell(
				s,
				sess,
				on,
				now,
			) + tmuxCell + ageCell

		// line two: the meter, on the same slots so the columns line up
		// and the highlight is a rectangle
		rendered := []string{
			s.On(s.Dim, on).
				Render(pad(fmt.Sprintf("%d", sess.Turns), 6)),
			s.On(s.Row, on).
				Render(pad(humanCount(sess.Tokens()), 9)),
			contextCell(s, sess, on),
			peakCell(s, sess, on),
			costCell(s, sess, on),
			diffCell(s, sess, on),
		}
		meter := s.On(s.Row, on).Render("  ")
		for i, mc := range meterCells {
			meter += fill(
				rendered[i],
				mc.natural,
				slotFor(slots, i, mc.natural),
			)
		}
		// Pad to the measured width of the line above, not to a
		// computed one.
		//
		// The computed version was two columns short, because a cell
		// renderer somewhere pads to a width the constants here do not
		// know about -- and a highlight that is nearly a rectangle
		// looks broken in exactly the way a wrong number always does.
		//
		// The rendered line is the only thing that knows how wide it
		// is.
		if rest := ansi.StringWidth(
			ansi.Strip(row),
		) - ansi.StringWidth(ansi.Strip(meter)); rest > 0 {
			meter += s.On(s.Row, on).
				Render(strings.Repeat(" ", rest))
		}

		b.WriteString(row + "\n" + meter + "\n")
	}
	if last < len(rows) {
		b.WriteString(
			s.Dim.Render(
				fmt.Sprintf("  %d more below", len(rows)-last),
			) + "\n",
		)
	}
	switch {
	case p.writing:
		b.WriteString(
			"\n" + s.Header.Render(
				"say something",
			) + "\n" + p.compose.View() + "\n",
		)
		b.WriteString(helpLine(s, "enter: send · esc: don't") + "\n")
	case p.pending != nil:
		b.WriteString("\n" + confirmView(s, p.pending, width) + "\n")
	case p.ask != nil:
		b.WriteString(
			"\n" + s.Caution.Render(fmt.Sprintf("%s to %s in %s?",
				p.ask.label, p.ask.sess, p.ask.target)) + "\n",
		)
		b.WriteString(helpLine(s, "y: yes · any other key: no") + "\n")
	case p.note != "":
		b.WriteString("\n" + s.Ok.Render(p.note) + "\n")
	}
	if p.detail {
		b.WriteString("\n" + p.detailView(s, width))
	} else {
		b.WriteString("\n" + p.tailView(s, width))
	}
	return b.String()
}

// tailView is the little screen under the list: the last ten things the session
// under the cursor actually did.
//
// A transcript is JSON Lines, so this is not a literal tail -- a wall of
// escaped JSON tells you nothing at a glance -- it is one line per record
// saying what happened, which is what anyone wants from tail -f in the first
// place.
//
// The frame is the pane's, painted on the theme's own raised surface rather
// than a hardcoded blue: the whole point of pulling themes from dodo is that
// one screen cannot go opting out of the estate's palette.
func (p *agentPane) tailView(s Styles, width int) string {
	name := "nothing selected"
	if sess, ok := p.session(); ok {
		name = orDash(sess.Project)
		if sess.Branch != "" {
			name += " · " + sess.Branch
		}
	}
	channel := "transcript"
	if p.live {
		channel = "live pane"
	}

	// the screen's inner width: the frame takes a border and a pad on each
	// side, and the scrollbar owns the last column
	inner := width - 10
	if inner < 24 {
		inner = 24
	}
	text := inner - 1

	lines := p.tail
	off := p.tailOff
	if off > maxInt(0, len(lines)-tailLines) {
		off = maxInt(0, len(lines)-tailLines)
	}
	if off < 0 {
		off = 0
	}

	var b strings.Builder
	head := name + " · " + channel
	if len(lines) > tailLines {
		head += fmt.Sprintf(
			"   %d-%d of %d",
			off+1,
			minInt(off+tailLines, len(lines)),
			len(lines),
		)
		if !p.tailFollow {
			head += " (scrolled)"
		}
	}
	b.WriteString(s.CRTDim.Render(padTo(head, inner)) + "\n")

	for row := 0; row < tailLines; row++ {
		i := off + row
		var body string
		var style lipgloss.Style
		switch {
		case p.tailErr != "" && row == 0:
			body, style = p.tailErr, s.Warn.Background(s.Screen)
		case i >= len(lines):
			// an empty row is still lit screen
			body, style = "", s.CRT
		default:
			l := lines[i]
			body = l.Text
			switch l.Kind {
			case "tool":
				style = s.AccentAlt.Background(s.Screen)
			case "result", "screen":
				style = s.CRTDim
			case "user":
				style = s.Accent.Background(s.Screen)
			default:
				style = s.CRT
			}
		}
		if len(lines) == 0 && row == 0 && p.tailErr == "" {
			body, style = "nothing in the tail yet", s.CRTDim
		}
		b.WriteString(style.Render(padTo(body, text)))
		b.WriteString(
			s.CRTDim.Render(
				scrollCell(row, tailLines, off, len(lines)),
			),
		)
		if row < tailLines-1 {
			b.WriteString("\n")
		}
	}
	return s.CRTFrame.Render(b.String())
}

// scrollCell paints one cell of the scrollbar down the screen's right edge.
// A screen with nothing off the top or bottom shows no bar at all: a track
// that never moves is furniture.
func scrollCell(row, shown, off, total int) string {
	if total <= shown {
		return " "
	}
	span := float64(shown) / float64(total)
	thumb := maxInt(1, int(span*float64(shown)+0.5))
	pos := 0
	if total > shown {
		pos = int(
			float64(
				off,
			) / float64(
				total-shown,
			) * float64(
				shown-thumb,
			),
		)
	}
	if row >= pos && row < pos+thumb {
		return "\u2588"
	}
	return "\u2502"
}

// padTo pads or clips to an exact column count, so every row of the screen
// paints the full width -- a background that stops at the end of the text
// is a smudge, not a screen.
func padTo(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		if n <= 1 {
			return string(r[:n])
		}
		return string(r[:n-1]) + "\u2026"
	}
	return s + strings.Repeat(" ", n-len(r))
}

// minInt keeps the row arithmetic readable at the call sites, where the
// clamping is the point and the comparison is not.
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// humanSince renders an age the way the cluster screen does.
func humanSince(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// The tail window: what the session under the cursor is actually doing.
//
// A transcript is JSON Lines, so a literal tail is a wall of escaped JSON --
// useless at a glance and unreadable at 03:20. What an operator wants from
// `tail -f` here is the same thing they want from it anywhere: is this thing
// moving, and on what.
//
// So each record renders as one line of what happened -- a tool with its
// target, a sentence of prose, a result with its size.
//
// The read is bounded to the LAST 64KB of the file rather than the file. A
// transcript reaches tens of megabytes, and re-reading one every five
// seconds to show ten lines is the kind of thing that makes a terminal fan
// audible.

// tailBytes is how much of the end of a transcript is read for the window,
// and tailLines is how many rendered lines the window shows.
const (
	tailBytes = 64 << 10
	// tailLines is what the little screen shows; tailBuffer is what it
	// holds. A screen you cannot scroll back in is a screen that loses the
	// thing you just saw go past, which is the whole complaint people have
	// with a bare tail -f.
	tailLines  = 10
	tailBuffer = 300
)

// TailLine is one rendered line of session activity.
type TailLine struct {
	Kind string // tool | text | result | user | meta
	Text string
}

// tailFetched carries a tail back into the loop.
type tailFetched struct {
	id    string
	lines []TailLine
	err   error
}

// tailCmd reads one session's tail off the main loop.
func tailCmd(id, path string, n int) tea.Cmd {
	return func() tea.Msg {
		lines, err := tailTranscript(path, n)
		return tailFetched{id: id, lines: lines, err: err}
	}
}

// tailTranscript renders the last n interesting records of a transcript.
func tailTranscript(path string, n int) ([]TailLine, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	off := int64(0)
	if fi.Size() > tailBytes {
		off = fi.Size() - tailBytes
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	if off > 0 {
		// the first line is a fragment: a seek lands mid-record
		sc.Scan()
	}
	var out []TailLine
	for sc.Scan() {
		if l, ok := renderRecord(sc.Bytes()); ok {
			out = append(out, l)
			if len(out) > n*4 {
				out = out[len(out)-n*2:]
			}
		}
	}
	if err := sc.Err(); err != nil && len(out) == 0 {
		return nil, err
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out, nil
}

// renderRecord turns one transcript record into one readable line, or says
// it is not worth a line. Snapshots, attachments and the harness's own
// bookkeeping are dropped: they are not what the agent is doing.
func renderRecord(raw []byte) (TailLine, bool) {
	var rec struct {
		Type    string `json:"type"`
		Message struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &rec) != nil {
		return TailLine{}, false
	}
	if rec.Type != "assistant" && rec.Type != "user" {
		return TailLine{}, false
	}
	var blocks []struct {
		Type    string          `json:"type"`
		Text    string          `json:"text"`
		Name    string          `json:"name"`
		Input   json.RawMessage `json:"input"`
		Content json.RawMessage `json:"content"`
		IsError bool            `json:"is_error"`
	}
	if json.Unmarshal(rec.Message.Content, &blocks) != nil {
		// a plain string content is a user turn typed by a human
		var text string
		if json.Unmarshal(rec.Message.Content, &text) == nil &&
			strings.TrimSpace(text) != "" {
			return TailLine{
				Kind: "user",
				Text: "you: " + oneLine(text, 90),
			}, true
		}
		return TailLine{}, false
	}
	for _, b := range blocks {
		switch b.Type {
		case "tool_use":
			return TailLine{
				Kind: "tool",
				Text: b.Name + "(" + toolTarget(b.Input) + ")",
			}, true
		case "text":
			if strings.TrimSpace(b.Text) == "" {
				continue
			}
			who := "you: "
			if rec.Type == "assistant" {
				who = ""
			}
			return TailLine{
				Kind: "text",
				Text: who + oneLine(b.Text, 90),
			}, true
		case "tool_result":
			n := len(b.Content)
			if b.IsError {
				return TailLine{
					Kind: "result",
					Text: fmt.Sprintf(
						"error, %d bytes back",
						n,
					),
				}, true
			}
			return TailLine{
				Kind: "result",
				Text: fmt.Sprintf("%d bytes back", n),
			}, true
		}
	}
	return TailLine{}, false
}

// toolTarget is the one field of a tool call worth reading at a glance --
// the command, the path, the pattern. The whole input is a paragraph.
func toolTarget(in json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(in, &m) != nil {
		return ""
	}
	for _, k := range []string{
		"command",
		"file_path",
		"path",
		"pattern",
		"query",
		"prompt",
		"url",
	} {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return oneLine(v, 64)
		}
	}
	return ""
}

// oneLine flattens and clips: the window is ten lines, not ten paragraphs.
func oneLine(s string, n int) string {
	s = strings.TrimSpace(
		strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\t", " "),
	)
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "\u2026"
	}
	return s
}

// tmuxWhyNot says why a session cannot be talked to, because "nothing
// happened" is the least useful thing a key can do.
func tmuxWhyNot(s AgentSession) string {
	if s.TmuxShared != "" {
		return "another session in " +
			"" + s.Project + " holds " + s.TmuxShared +
			" -- this one is not what is typing in there"
	}
	return "no tmux pane is running in " + orDash(s.Cwd)
}

// maxInt because the generic max is not available for the shapes used here
// without dragging a constraint in for two call sites.
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// contextLimit is the window a model works in, or 0 when puffin does not
// know. Zero matters: an unknown model gets a bare number and NO percentage,
// because a percentage of a denominator nobody checked is exactly the kind
// of number people make decisions on.
//
// The 1M figures are not read off a vendor table -- they are what these
// transcripts actually reach on this machine: opus-5 to 999,559, fable-5 to
// 998,018, sonnet-5 to 966,703. Haiku's 200k is documented rather than
// observed here; the sessions were too short to press it.
func contextLimit(model string) int64 {
	m := strings.TrimSuffix(strings.TrimPrefix(model, "claude-"), "[1m]")
	switch {
	case strings.HasPrefix(m, "haiku"):
		return 200_000
	case strings.HasPrefix(m, "opus"), strings.HasPrefix(m, "sonnet"),
		strings.HasPrefix(m, "fable"), strings.HasPrefix(m, "mythos"):
		return 1_000_000
	}
	return 0
}

// contextCell renders how full the window is, and how full it has been.
func contextCell(s Styles, sess AgentSession, on bool) string {
	if sess.Context == 0 {
		return s.On(s.Dim, on).Render(pad("-", 13))
	}
	limit := contextLimit(sess.Model)
	if limit == 0 {
		return s.On(s.Dim, on).Render(pad(humanCount(sess.Context), 13))
	}
	pct := float64(sess.Context) / float64(limit) * 100
	return s.On(contextStyle(s, pct), on).Render(
		pad(
			fmt.Sprintf("%s %.0f%%", humanCount(sess.Context), pct),
			13,
		))
}

// contextStyle colours by how full the window is.
func contextStyle(s Styles, pct float64) lipgloss.Style {
	switch {
	case pct >= 90:
		return s.Warn
	case pct >= 70:
		return s.Caution
	}
	return s.Dim
}

// peakCell is the fullest this session has been. It gets its own column
// rather than a caret glued to the context cell, for two reasons: the glued
// version overflowed the column and clipped to "^1…", and a bare "^97" is a
// symbol nobody can look up. A header is the legend a number needs.
//
// It shows only when the session has been meaningfully fuller than it is
// now, which is compaction: the session has already dropped things it once
// had in front of it, and that is the thing worth seeing.
func peakCell(s Styles, sess AgentSession, on bool) string {
	limit := contextLimit(sess.Model)
	if limit == 0 || sess.Peak <= sess.Context+sess.Context/5 {
		return s.On(s.Dim, on).Render(pad("-", 6))
	}
	pct := float64(sess.Peak) / float64(limit) * 100
	return s.On(contextStyle(s, pct), on).
		Render(pad(fmt.Sprintf("%.0f%%", pct), 6))
}

// diffCell is what the session actually changed on disk.
func diffCell(s Styles, sess AgentSession, on bool) string {
	if !sess.CostKnown || (sess.LinesAdded == 0 && sess.LinesRemoved == 0) {
		return s.On(s.Dim, on).Render(pad("-", 11))
	}
	added := fmt.Sprintf("%+d", sess.LinesAdded)
	removed := pad(fmt.Sprintf("-%d", sess.LinesRemoved), 11-len(added)-1)
	return s.On(s.Ok, on).Render(added) +
		s.On(s.Dim, on).Render(" ") +
		s.On(s.Warn, on).Render(removed)
}

// costCell is Claude Code's own figure, not puffin's arithmetic. Nothing
// here computes a price: the harness writes the dollars into the transcript
// and puffin reads them, which is the difference between reporting a number
// and inventing one.
func costCell(s Styles, sess AgentSession, on bool) string {
	if !costEnabled("test") {
		// the source stopped emitting; the column says nothing rather
		// than repeating a frozen number. See telemetry.go.
		return s.On(s.Dim, on).Render(pad("-", 9))
	}
	if !sess.CostKnown {
		return s.On(s.Dim, on).Render(pad("-", 9))
	}
	// a stale figure is dimmed and carries a tilde. It is not wrong, it is
	// old, and those are different -- so it is not hidden and not styled
	// like a current number either.
	if sess.CostStale() {
		return s.On(s.Dim, on).
			Render(pad(fmt.Sprintf("~$%.2f", sess.CostUSD), 9))
	}
	return s.On(s.Row, on).
		Render(pad(fmt.Sprintf("$%.2f", sess.CostUSD), 9))
}

// costStaleAfter is how far a cost figure may lag the session's own last
// line before it stops being reported as current.
//
// The harness writes a cost-state at checkpoints and at exit, never per turn,
// so every live session's cost lags a little and that is normal.
//
// What is not normal is lagging by hours: one seen here had gone thirteen hours
// on a session that had done half its work since, and puffin drew it in the
// same ink as a number from a second ago.
//
// Fifteen minutes is loose enough that an ordinary checkpoint gap never
// trips it and tight enough that "the client stopped reporting" shows up
// the same evening.
const costStaleAfter = 15 * time.Minute

// CostStale says the cost figure is older than the work it claims to price.
func (s AgentSession) CostStale() bool {
	if !s.CostKnown || s.CostAt.IsZero() {
		return false
	}
	return s.LastWrite.Sub(s.CostAt) > costStaleAfter
}

// CostAge is how far behind the cost figure is, for the surfaces that have
// room to say so.
func (s AgentSession) CostAge() time.Duration {
	if s.CostAt.IsZero() {
		return 0
	}
	return s.LastWrite.Sub(s.CostAt)
}

// agentSorts are the columns the list can be ordered by. "last log" is first
// because it is the default and the one that answers "what is moving right
// now"; the rest answer "what is expensive", "what is nearly full", "what
// actually changed the tree".
//
// agentSorts is order-sensitive and the order is persisted.
//
// ui.json stores agentSort as an index into this slice, so inserting a sorter
// anywhere but the end silently changes what every saved preference means: a
// remembered "5" stops being the column that was chosen.
//
// Adding "driven" in the middle did exactly that and a test caught it, which is
// the only reason this comment exists rather than a confused bug report in a
// week.
//
// Append only. TestAgentSortOrderIsFrozen pins it.
var agentSorts = []struct {
	name string
	less func(a, b AgentSession) bool
}{
	{
		"last log",
		func(
			a,
			b AgentSession,
		) bool {
			return a.LastWrite.After(b.LastWrite)
		},
	},
	{
		"context",
		func(a, b AgentSession) bool { return a.Context > b.Context },
	},
	{"peak", func(a, b AgentSession) bool { return a.Peak > b.Peak }},
	{"cost", func(a, b AgentSession) bool { return a.CostUSD > b.CostUSD }},
	{"lines", func(a, b AgentSession) bool {
		return a.LinesAdded+a.LinesRemoved > b.LinesAdded+b.LinesRemoved
	}},
	{"turns", func(a, b AgentSession) bool { return a.Turns > b.Turns }},
	{
		"project",
		func(a, b AgentSession) bool { return a.Project < b.Project },
	},
	{"name", func(a, b AgentSession) bool { return a.Name < b.Name }},
	// Appended. ui.json persists the chosen order by index, so putting this
	// next to "turns" where it belongs by meaning would silently redefine
	// every remembered preference.
	//
	// TestAgentSortOrderIsFrozen catches it, and caught it here -- the
	// comment about appending was written in the same edit that inserted.
	{"steps", func(a, b AgentSession) bool { return a.Steps > b.Steps }},
}

// sortSessions orders the list and puts the cursor back on the session it
// was on. Every comparison falls back to last-log so the order is total:
// two sessions with no cost-state are otherwise free to swap places on
// every refresh, which makes a self-refreshing list impossible to read.
func (p *agentPane) sortSessions() {
	sorter := agentSorts[p.sortBy%len(agentSorts)]
	sort.SliceStable(p.v.Sessions, func(i, j int) bool {
		a, b := p.v.Sessions[i], p.v.Sessions[j]
		if sorter.less(a, b) != sorter.less(b, a) {
			if p.sortDesc {
				return sorter.less(b, a)
			}
			return sorter.less(a, b)
		}
		return a.LastWrite.After(b.LastWrite)
	})
	if p.sel != "" {
		for i, sess := range p.visible() {
			if sess.ID == p.sel {
				p.cursor = i
				return
			}
		}
	}
	if n := len(p.visible()); p.cursor >= n {
		p.cursor = maxInt(0, n-1)
	}
}

// sortMark points at the column the list is ordered by.
func (p *agentPane) sortMark(col string) string {
	if agentSorts[p.sortBy%len(agentSorts)].name != col {
		return ""
	}
	if p.sortDesc {
		return "\u25b4"
	}
	return "\u25be"
}

// agentCols says which optional columns fit on the record's first line.
//
// The second line -- the meter -- is 56 columns and always fits, so turns,
// tokens, context, peak, lines and cost are no longer droppable. That is
// the point of the two-line record: the columns that used to disappear were
// exactly the ones somebody had gone looking for.
type agentCols struct{ name, branch bool }

// agentSlot is the column grid, and BOTH lines of a record stand on it.
//
// The first version gave each line its own widths, which is what "equally
// justified" was asking about: the meter's columns landed wherever their own
// padding put them, so the two lines of one record did not line up and the
// record read as two unrelated rows.
//
// The slots are named once here and every cell on either line is padded to one
// of them.
//
// It also makes the cursor highlight a rectangle. Both lines total the same
// width, so the selected record is one block instead of a wide line above a
// short one.
//
// The meter's cells are narrower than the slots they sit in, and cost is
// placed in the narrow state slot because it is the only one that fits
// there exactly. That is why the meter reads turns, tokens, context, peak,
// cost, lines rather than in the order the header used to list them.
const (
	slotProject = 18
	slotName    = 18
	slotBranch  = 14
	slotModel   = 16
	slotState   = 9
	slotTmux    = 14
	slotAge     = 10
	slotMarker  = 2
)

// gridSlots is the first line's column widths, in order, for the columns
// that are actually being drawn.
func gridSlots(c agentCols) []int {
	out := []int{slotProject}
	if c.name {
		out = append(out, slotName)
	}
	if c.branch {
		out = append(out, slotBranch)
	}
	return append(out, slotModel, slotState, slotTmux, slotAge)
}

// slotFor is the grid slot a meter cell stands in, or its own width when
// the grid has run out of slots.
func slotFor(slots []int, i, natural int) int {
	if i < len(slots) && slots[i] > natural {
		return slots[i]
	}
	return natural
}

// linesPerRecord is how many terminal lines one session occupies.
//
// Every piece of arithmetic on this screen that converts between a row index
// and a y coordinate goes through it -- the window size, the click target, the
// hit height -- because they were three separate calculations that all had to
// agree, and three copies of a number is how they stop agreeing.
const linesPerRecord = 2

// columns decides what the first line can carry at this width. The fixed
// part -- marker, project, model, state, tmux, last log -- is 69 wide.
func (p *agentPane) columns(width int) agentCols {
	const fixed = 2 + 18 + 16 + 9 + 14 + 10
	if width <= 0 {
		return agentCols{name: true, branch: true}
	}
	room := width - 6 - fixed // the pane frame takes a border and padding
	c := agentCols{}
	// the name goes first: it is the address another session messages this
	// one at, and a row you cannot name is a row you cannot reach
	for _, add := range []struct {
		w  int
		on *bool
	}{{18, &c.name}, {14, &c.branch}} {
		if room >= add.w {
			*add.on = true
			room -= add.w
		}
	}
	return c
}

// modelCell is the model and the effort it is being run at, together:
// "opus-5/xhigh". One fact about how a session is being driven, so it reads
// as one cell -- a session at low effort and one at max are not the same
// thing wearing the same name.
func modelCell(sess AgentSession) string {
	m := shortModel(sess.Model)
	if m == "" {
		return ""
	}
	if sess.Effort == "" {
		return m
	}
	return m + "/" + sess.Effort
}

// hitBox is a region the mouse can land on, in pane coordinates.
type hitBox struct {
	x, w int
	y    int
	name string
}

// hit is whether a click landed in this box, in the pane's own
// coordinates rather than the terminal's.
func (h hitBox) hit(
	x, y int,
) bool {
	return y == h.y && x >= h.x && x < h.x+h.w
}

// Click resolves a click against what the last render actually drew.
func (p *agentPane) Click(x, y, wheel int) tea.Cmd {
	if wheel != 0 {
		// the wheel moves the cursor through the list, which is what
		// the list is for; the little screen has its own keys
		if wheel < 0 && p.cursor > 0 {
			p.cursor--
		}
		if wheel > 0 && p.cursor < len(p.visible())-1 {
			p.cursor++
		}
		p.sel = p.selected()
		return p.watch()
	}
	// a column header sorts by that column, and clicking the one already
	// sorted reverses it -- the same bargain every table in the world makes
	for _, h := range p.hitHeader {
		if !h.hit(x, y) {
			continue
		}
		for i, srt := range agentSorts {
			if srt.name != h.name {
				continue
			}
			if p.sortBy == i {
				p.sortDesc = !p.sortDesc
			} else {
				p.sortBy, p.sortDesc = i, false
			}
			p.sortSessions()
			rememberAgentSort(p.sortBy, p.sortDesc)
			return nil
		}
		return nil
	}
	// a model chip filters to that model, and clicking it again clears
	for _, h := range p.hitChips {
		if h.hit(x, y) {
			if p.filter == h.name {
				p.filter = ""
			} else {
				p.filter = h.name
			}
			p.cursor = 0
			p.sel = p.selected()
			return p.watch()
		}
	}
	// and a row selects it
	if y >= p.hitTop && y < p.hitTop+p.hitRows {
		// either line of a record selects it: they are one thing
		p.cursor = p.offset + (y-p.hitTop)/linesPerRecord
		if n := len(p.visible()); p.cursor >= n {
			p.cursor = maxInt(0, n-1)
		}
		p.sel = p.selected()
		return p.watch()
	}
	return nil
}

// visible is the session list after filtering. Everything that indexes the
// list -- the cursor, the window, a click -- goes through this, so a filter
// cannot leave the cursor pointing at a row nobody can see.
func (p *agentPane) visible() []AgentSession {
	if p.filter == "" {
		return p.v.Sessions
	}
	out := make([]AgentSession, 0, len(p.v.Sessions))
	for _, s := range p.v.Sessions {
		if shortModel(s.Model) == p.filter {
			out = append(out, s)
		}
	}
	return out
}

// HandlesEsc says esc is the pane's while a filter is on: it clears the
// filter rather than leaving the screen. Without this the pane never sees
// esc at all -- the frame closes on it first -- and a key documented in the
// help line would do something else entirely.
func (p *agentPane) HandlesEsc() bool { return p.filter != "" }

// lastBlockKind says what an assistant turn ended with. A turn that ends in a
// tool call is mid-loop; one that ends in prose has finished and handed back.
//
// That distinction is the whole of "does this agent need me", and it is in the
// transcript already -- no Stop hook, no Notification matcher, neither of which
// is reliable anyway.
func lastBlockKind(content json.RawMessage) string {
	var blocks []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return ""
	}
	kind := ""
	for _, b := range blocks {
		switch b.Type {
		case "tool_use":
			kind = "tool"
		case "text":
			kind = "text"
		}
	}
	return kind
}

// State is what the session is doing, as far as a file on disk can say.
//
//	working   it wrote recently, or its last act was a tool call
//	waiting   it finished a turn with prose and has been quiet since
//	idle      it has not written for a while
//
// "waiting" is the one worth a notification: the agent is done and the next
// move is yours. The quiet threshold is deliberately not one second -- a
// model streaming a long answer pauses -- and deliberately not a minute,
// which is long enough to have wandered off.
func (s AgentSession) State(now time.Time) string {
	quiet := now.Sub(s.LastWrite)
	switch {
	case quiet < 20*time.Second:
		return "working"
	case s.LastKind == "text" && quiet < 30*time.Minute:
		return "waiting"
	case quiet < 2*time.Minute:
		return "working"
	}
	return "idle"
}

// Autonomy is model steps per typed prompt: how far this session runs on
// its own between one person saying something and the next.
//
// It separates two things that look identical in every other column. One
// session measured here had 1769 steps against 11 typed prompts -- 161 to one,
// running essentially unattended. Another over the same window had 11702
// against 704, which is 17 to one and reads as a conversation.
//
// The first is getting nothing from anybody, and that is the fact worth having
// on the screen.
//
// Zero turns means nobody has typed yet, which is not infinite autonomy,
// so it reports nothing rather than a number.
func (s AgentSession) Autonomy() (float64, bool) {
	if s.Turns == 0 || s.Steps == 0 {
		return 0, false
	}
	return float64(s.Steps) / float64(s.Turns), true
}

// announce fires a notification for each session that has JUST started waiting.
//
// Transitions only: "this agent is waiting" is true every five seconds and
// "this agent has just started waiting" is true once, and a notifier that
// re-sends a condition is one people mute -- which is worse than none, because
// a muted notifier is also a trusted one.
func (p *agentPane) announce() {
	if p.was == nil {
		// the first pass records where everything already is and
		// announces nothing: opening the pane should not fire six
		// notifications for six agents that were already waiting before
		// you looked
		p.was = map[string]string{}
		for _, s := range p.v.Sessions {
			p.was[s.ID] = s.State(time.Now())
		}
		return
	}
	now := time.Now()
	ws := watchesFor("agents")
	for _, sess := range p.v.Sessions {
		state := sess.State(now)
		// a watch on the agents pane matches what a session IS -- its
		// project, branch, model, or the state it just entered -- so
		// "albatross" tells you when that one stops, and "waiting"
		// tells you when any of them does
		if p.was[sess.ID] != state {
			line := strings.Join([]string{sess.Project, sess.Branch,
				shortModel(sess.Model), state}, " ")
			for _, w := range ws {
				if w.Matches(line) {
					Emit(
						Event{
							Kind: WatchFired,
							Subject: "agents · " +
								"" + w.Pattern,
							Detail: orDash(
								sess.Project,
							) + " is " + state,
							Emote: w.Emote,
						},
					)
					break
				}
			}
		}
		if p.was[sess.ID] != state {
			where := orDash(sess.Project)
			if sess.Branch != "" {
				where += " · " + sess.Branch
			}
			switch state {
			case "waiting":
				if p.was[sess.ID] == "working" {
					detail := shortModel(sess.Model) +
						" finished its turn after " +
						plural(sess.Turns, "turn")
					Emit(Event{
						Kind:    AgentWaiting,
						Subject: where,
						Detail:  detail,
					})
				}
			case "working":
				Emit(Event{Kind: AgentWorking, Subject: where,
					Detail: shortModel(
						sess.Model,
					) + " started again"})
			}
		}
		p.was[sess.ID] = state
	}
	// sessions that fell out of the window stop being tracked
	live := map[string]bool{}
	for _, s := range p.v.Sessions {
		live[s.ID] = true
	}
	for id := range p.was {
		if !live[id] {
			delete(p.was, id)
		}
	}
}

// stateCell paints what a session is doing. Waiting is the one that earns a
// colour: it means the next move is yours.
func stateCell(s Styles, sess AgentSession, on bool, now time.Time) string {
	state := sess.State(now)
	style := s.Dim
	switch state {
	case "working":
		style = s.Ok
	case "waiting":
		style = s.Accent
	}
	return s.On(style, on).Render(pad(state, 9))
}

// targets is what a verb will act on: every tagged session, or the row under
// the cursor when nothing is tagged.
//
// Tagging is not a mode. With nothing tagged the keys do what they always
// did to the row you are on, so the feature costs nothing to anyone who
// never presses space -- and with three tagged, the same key acts on three.
func (p *agentPane) targets() []AgentSession {
	if len(p.tagged) > 0 {
		var out []AgentSession
		for _, s := range p.visible() {
			if p.tagged[s.ID] {
				out = append(out, s)
			}
		}
		return out
	}
	if s, ok := p.session(); ok {
		return []AgentSession{s}
	}
	return nil
}

// armSignal builds the confirmation for TERM or KILL over every target.
//
// The intent carries the actual kill commands, one per session, pids resolved
// now rather than at the moment of pressing. That is deliberate: what is on the
// screen is what will run, and a pid that has since gone away fails loudly
// instead of a different process inheriting the number and the signal.
func (p *agentPane) armSignal(sig, name string) (pane, tea.Cmd) {
	ts := p.targets()
	if len(ts) == 0 {
		p.note = "nothing to signal"
		return p, nil
	}
	var lines []string
	var acting []AgentSession
	for _, s := range ts {
		if s.Tmux.Target == "" {
			lines = append(lines, "# "+s.Project+": "+tmuxWhyNot(s))
			continue
		}
		kids, err := paneChildren(s.Tmux.Target)
		if err != nil {
			lines = append(lines, "# "+s.Project+": "+err.Error())
			continue
		}
		lines = append(lines, signalCommand(sig, kids)+
			"   # "+s.Project+" in "+s.Tmux.Target)
		acting = append(acting, s)
	}
	if len(acting) == 0 {
		p.note = "nothing signalable: " + strings.Join(lines, " · ")
		return p, nil
	}
	target := acting[0].Project
	if len(acting) > 1 {
		target = itoa(len(acting)) + " sessions"
	}
	p.pending = arm(Intent{
		Verb:     name,
		Target:   target,
		Wire:     strings.Join(lines, "\n"),
		Kind:     "signal",
		Endpoint: "tmux panes on this host",
		// TERM lets a session close its transcript and be resumed. KILL
		// does not, and that is the whole difference between the rungs.
		Reversible: sig == "TERM",
	}, func() tea.Cmd {
		return func() tea.Msg {
			var bad []string
			for _, s := range acting {
				if err := signalPane(
					s.Tmux.Target,
					sig,
				); err != nil {
					bad = append(
						bad,
						s.Project+": "+err.Error(),
					)
				}
			}
			return agentActed{
				note: signalNote(name, len(acting), bad),
			}
		}
	})
	return p, nil
}

// armQuit is the gentlest rung: type /quit at the session and let it end
// itself. No signal, no lost state, and the transcript closes the way the
// harness meant it to.
func (p *agentPane) armQuit() (pane, tea.Cmd) {
	ts := p.targets()
	var acting []AgentSession
	var lines []string
	for _, s := range ts {
		if s.Tmux.Target == "" {
			lines = append(lines, "# "+s.Project+": "+tmuxWhyNot(s))
			continue
		}
		lines = append(
			lines,
			"tmux send-keys -t "+s.Tmux.Target+" -l -- /quit ; "+
				"Enter"+
				"   # "+s.Project,
		)
		acting = append(acting, s)
	}
	if len(acting) == 0 {
		p.note = "nothing to quit"
		return p, nil
	}
	target := acting[0].Project
	if len(acting) > 1 {
		target = itoa(len(acting)) + " sessions"
	}
	p.pending = arm(Intent{
		Verb:     "/quit",
		Target:   target,
		Wire:     strings.Join(lines, "\n"),
		Kind:     "tmux send-keys",
		Endpoint: "tmux panes on this host",
		// it ends a session, but the session saved itself on the way
		// out
		Reversible: true,
	}, func() tea.Cmd {
		return func() tea.Msg {
			var bad []string
			for _, s := range acting {
				if err := tmuxSend(
					s.Tmux.Target,
					"/quit",
				); err != nil {
					bad = append(
						bad,
						s.Project+": "+err.Error(),
					)
				}
			}
			return agentActed{
				note: signalNote("/quit", len(acting), bad),
			}
		}
	})
	return p, nil
}

// signalNote says what happened, including what did not.
func signalNote(name string, n int, bad []string) string {
	if len(bad) == 0 {
		return name + " sent to " + itoa(n) + " session(s)"
	}
	return name + ": " + itoa(n-len(bad)) + " sent, " +
		itoa(len(bad)) + " failed · " + strings.Join(bad, " · ")
}

// agentActed carries the outcome back to the pane.
type agentActed struct{ note string }
