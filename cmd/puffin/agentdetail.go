package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The detail view: everything about one session that does not fit a column.
//
// Stats, the log, where the scratch directory is, and whether there is
// anything in it.
//
// The scratch directory is the part worth having. It lives under
// /private/tmp, which means it is invisible, unbacked, and gone when the
// machine is rebooted or the directory is reaped. On 2026-09-02 one session
// on this host was holding 7,417 files and 221MB there and nothing said so.

// scratchDir is where a session's scratchpad lives, derived from the
// transcript's own path rather than guessed.
//
// A transcript is ~/.claude/projects/<sanitised-cwd>/<session-id>.jsonl and the
// scratchpad is /private/tmp/claude-<uid>/<sanitised-cwd>/<session-id>/
// scratchpad -- the same two components in a different root.
//
// Deriving it from the path puffin already has means it cannot drift from the
// session it claims to belong to.
func scratchDir(transcriptPath string) string {
	if transcriptPath == "" {
		return ""
	}
	id := strings.TrimSuffix(filepath.Base(transcriptPath), ".jsonl")
	proj := filepath.Base(filepath.Dir(transcriptPath))
	if id == "" || proj == "" {
		return ""
	}
	return filepath.Join(scratchRoot(), proj, id, "scratchpad")
}

// scratchRoot is /private/tmp/claude-<uid>. The uid is read rather than
// assumed: 501 is the first human account on a mac and is not a constant.
func scratchRoot() string {
	return filepath.Join(
		"/private/tmp",
		fmt.Sprintf("claude-%d", os.Getuid()),
	)
}

// ScratchInfo is what is sitting in a session's scratchpad.
type ScratchInfo struct {
	Path    string
	Exists  bool
	Files   int
	Bytes   int64
	Newest  time.Time
	Sampled bool // the walk stopped early: Files and Bytes are a floor
}

// scratchCache memoises the walk. A session with thousands of files costs
// real time to measure and the answer does not change between keystrokes.
var scratchCache = struct {
	sync.Mutex
	at map[string]ScratchInfo
}{at: map[string]ScratchInfo{}}

// scratchWalkCap is a guard against a pathological directory, not a budget.
//
// It was 4,000 on the assumption that walking a large scratchpad would make
// the view feel slow. Measured instead: a scratchpad of 7,417 files and
// 216MB takes 0.17 seconds to walk in full, cached thereafter. The cap was
// answering a question nobody had asked.
//
// 200,000 is roughly five seconds at the measured rate, which is the point
// where something has gone wrong rather than the point where a session got
// busy. Past it the count is reported as a floor and says so, because a
// number silently rounded off is the failure this repository keeps finding.
const scratchWalkCap = 200000

// scratchMeasured carries a finished walk back into the loop.
type scratchMeasured struct {
	dir  string
	info ScratchInfo
}

// scratchCached is the answer if it is already known, and nothing if it is
// not. It never touches the filesystem, so the render path cannot block on
// it.
func scratchCached(transcriptPath string) (ScratchInfo, bool) {
	dir := scratchDir(transcriptPath)
	if dir == "" {
		return ScratchInfo{}, false
	}
	scratchCache.Lock()
	defer scratchCache.Unlock()
	v, ok := scratchCache.at[dir]
	return v, ok
}

// scratchCmd measures a scratchpad OFF the render path.
//
// It was called from the view, which put a filesystem walk between a keystroke
// and a frame. A scratchpad holding a working set of real data is large enough
// to be worth worrying about, and the measurement that said 0.17 seconds was a
// measurement of one directory on one day -- it is not a bound on anything.
//
// A tea.Cmd runs in its own goroutine and delivers a message, so the view
// draws "measuring" immediately and redraws when the answer arrives. It is
// the same reasoning as the companion's clock: the host owns the frame, and
// work that is not instant does not get to happen inside one.
//
// nil when the answer is already cached: a command that would deliver a
// message nobody needs is a redraw nobody asked for.
func scratchCmd(transcriptPath string) tea.Cmd {
	dir := scratchDir(transcriptPath)
	if dir == "" {
		return nil
	}
	if _, done := scratchCached(transcriptPath); done {
		return nil
	}
	return func() tea.Msg {
		info := walkScratch(dir)
		scratchCache.Lock()
		scratchCache.at[dir] = info
		scratchCache.Unlock()
		return scratchMeasured{dir: dir, info: info}
	}
}

// walkScratch is the measurement, without the cache, so it is testable
// against a directory a test just built.
func walkScratch(dir string) ScratchInfo {
	info := ScratchInfo{Path: dir}
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		info.Exists = true
		filepath.WalkDir(
			dir,
			func(p string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				if info.Files >= scratchWalkCap {
					info.Sampled = true
					return filepath.SkipAll
				}
				info.Files++
				if fi, err := d.Info(); err == nil {
					info.Bytes += fi.Size()
					if fi.ModTime().After(info.Newest) {
						info.Newest = fi.ModTime()
					}
				}
				return nil
			},
		)
	}
	return info
}

// Summary is the one line the detail view prints about the scratchpad.
func (s ScratchInfo) Summary() string {
	switch {
	case !s.Exists:
		return "none"
	case s.Files == 0:
		return "empty"
	}
	n := fmt.Sprintf("%d files", s.Files)
	if s.Sampled {
		n = fmt.Sprintf("%d+ files", s.Files)
	}
	return fmt.Sprintf("%s · %s · newest %s", n, humanBytes(s.Bytes),
		humanSince(time.Since(s.Newest)))
}

// detailView is the whole session, for the one under the cursor.
//
// It replaces the tail rather than sitting beside it: the pane is already
// two lines per record plus a tail window, and a third region would push
// the list off a short terminal. tab still switches transcript and live;
// d closes this and gives the tail back.
func (p *agentPane) detailView(s Styles, width int) string {
	sess, ok := p.session()
	if !ok {
		return s.Dim.Render("  nothing selected")
	}
	now := time.Now()
	var b strings.Builder

	title := orDash(sess.Name)
	if sess.Name == "" {
		title = "unnamed session"
	}
	b.WriteString(s.Header.Render("  "+title) +
		s.Dim.Render("  ·  "+sess.ID) + "\n\n")

	row := func(k, v string) {
		b.WriteString(
			"  " + s.Dim.Render(
				pad(k, 12),
			) + s.Row.Render(
				v,
			) + "\n",
		)
	}

	row("project", orDash(sess.Project))
	row("cwd", orDash(sess.Cwd))
	if sess.Branch != "" {
		row("branch", sess.Branch)
	}
	row("model", orDash(modelCell(sess)))
	row("state", sess.State(now))

	// where it is, and whether it can be reached. A session with no pane
	// cannot be typed at, and that is a different fact from being idle.
	tmux := "not in tmux"
	if sess.Tmux.Target != "" {
		tmux = sess.Tmux.Target
		if sess.TmuxCount > 1 {
			tmux += fmt.Sprintf(
				"  (+%d more in this directory)",
				sess.TmuxCount-1,
			)
		}
	} else if sess.TmuxShared != "" {
		tmux = "(" + sess.TmuxShared + ") — a newer session holds " +
			"this pane"
	}
	row("tmux", tmux)

	b.WriteString("\n")
	// two counts and the ratio between them. One number called "turns"
	// was answering a question nobody asked: an agentic loop's step count
	// is not a conversation length, and the two differ by up to two
	// orders of magnitude on this laptop.
	row("turns", fmt.Sprintf("%d typed", sess.Turns))
	steps := fmt.Sprintf("%d model replies", sess.Steps)
	if r, ok := sess.Autonomy(); ok {
		steps += fmt.Sprintf("  ·  %.0f per turn", r)
	}
	row("steps", steps)
	if sess.Synthetic > 0 {
		// harness-authored records: interrupts, errors, login prompts.
		// Counted, never shown until now, and a high count is a session
		// that has been failing rather than working.
		row(
			"synthetic",
			fmt.Sprintf(
				"%d  (interrupts, errors — not model turns)",
				sess.Synthetic,
			),
		)
	}
	row("tokens", humanCount(sess.Tokens()))
	if lim := contextLimit(sess.Model); lim > 0 && sess.Context > 0 {
		row(
			"context",
			fmt.Sprintf(
				"%s of %s  %.0f%%",
				humanCount(sess.Context),
				humanCount(
					lim,
				),
				float64(sess.Context)/float64(lim)*100,
			),
		)
	}
	// the two clocks, side by side, because the ratio is the fact.
	//
	// A session three days old that spent three hours on the model was
	// mostly a person thinking; one with the same wall figure and thirty
	// hours of API was doing something else entirely, and a single number
	// cannot tell those apart.
	if sess.Wall > 0 {
		elapsed := humanDur(sess.Wall) + " wall"
		if sess.API > 0 {
			elapsed += fmt.Sprintf(
				"  ·  %s on the model (%.0f%%)",
				humanDur(
					sess.API,
				),
				float64(sess.API)/float64(sess.Wall)*100,
			)
		}
		// same record as the cost, so the same lag: measured on
		// 2026-09-03 the on-disk figure trailed a live /cost by
		// thirteen hours. A duration that is quietly stale reads as a
		// session that stopped.
		if sess.CostStale() {
			elapsed += fmt.Sprintf(
				"  · as of %s",
				humanSince(sess.CostAge()),
			)
		}
		row("elapsed", elapsed)
	}
	if sess.CostKnown && costEnabled("test") {
		cost := fmt.Sprintf("$%.2f", sess.CostUSD)
		if sess.CostRuns > 1 {
			cost += fmt.Sprintf("  across %d runs", sess.CostRuns)
		}
		if sess.CostStale() {
			cost += fmt.Sprintf(
				"  · STALE, last reported %s",
				humanSince(sess.CostAge()),
			)
		}
		row("cost", cost)
	}
	if sess.LinesAdded > 0 || sess.LinesRemoved > 0 {
		row(
			"lines",
			fmt.Sprintf(
				"+%d -%d",
				sess.LinesAdded,
				sess.LinesRemoved,
			),
		)
	}

	b.WriteString("\n")
	row("last log", humanSince(now.Sub(sess.LastWrite)))
	driven := "never — nothing has ever typed into this session"
	if !sess.LastHuman.IsZero() {
		driven = humanSince(now.Sub(sess.LastHuman))
	}
	row("driven", driven)

	// The scratchpad. Under /private/tmp, so it is unbacked and reaped,
	// and nothing else in puffin mentions it exists.
	//
	// Measured off the render path: if the walk has not finished this says
	// so rather than blocking the frame, and the row is replaced when the
	// message lands.
	b.WriteString("\n")
	if sc, done := scratchCached(sess.Path); done {
		row("scratch", sc.Summary())
		if sc.Path != "" {
			b.WriteString(
				"  " + s.Dim.Render(pad("", 12)+sc.Path) + "\n",
			)
		}
	} else {
		row("scratch", "measuring...")
		if d := scratchDir(sess.Path); d != "" {
			b.WriteString("  " + s.Dim.Render(pad("", 12)+d) + "\n")
		}
	}

	b.WriteString("\n  " + s.Dim.Render("resume:  ") +
		s.Row.Render(
			fmt.Sprintf(
				"cd %s && claude --resume %s",
				orDash(sess.Cwd),
				sess.ID,
			),
		) + "\n")
	return b.String()
}

// humanDur renders a duration the way a person says it: the two largest
// units and no more. "3d 10h" rather than "82h48m36s", which is accurate,
// unreadable, and the reason nobody looks twice at a Go duration.
func humanDur(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf(
			"%dd %dh",
			int(d.Hours())/24,
			int(d.Hours())%24,
		)
	case d >= time.Hour:
		return fmt.Sprintf(
			"%dh %dm",
			int(d.Hours()),
			int(d.Minutes())%60,
		)
	case d >= time.Minute:
		return fmt.Sprintf(
			"%dm %ds",
			int(d.Minutes()),
			int(d.Seconds())%60,
		)
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}
