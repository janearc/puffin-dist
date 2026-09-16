package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The logs pane: the whole estate's stdout, by name.
//
// vector-collector tails every container on the node and writes
// /fleet-logs/containers-YYYY.MM.dd.jsonl inside its own pod. Today's file is
// already 92MB and yesterday's had a hundred thousand lines, which decides the
// entire design: puffin never streams that out.
//
// Every read is `tail -n` or `grep -m` executed inside the pod, so the
// filtering happens where the bytes already are and only the answer crosses the
// wire.
//
// The collector was logstash until 2026-08-30. The mount, the filename and the
// dated-file convention survived the swap by design, so what changed for puffin
// is only the shape of a record and the label the pod answers to
//
// -- but the record shape changed in a way that fails silently, which is why it
// is spelled out at parseLogLine rather than left to be discovered.
//
// The service name comes from the pod, which the collector now names outright
// and which is also recoverable from the log file path --
// /var/log/containers/<pod>_<namespace>_<container>-<id>.log -- with the
// replica suffix stripped off.
//
// That is a name, which is the only identifier this estate permits; the pod is
// kept beside it because when a service has two replicas and one is
// misbehaving, "which one" is the question you are about to ask.

// logFile is where the collector writes, and the pod it writes in.
const (
	logDir      = "/fleet-logs"
	logSelector = "app.kubernetes.io/name=vector-collector"
	// how many lines to pull. The steps are what an operator actually
	// wants: a screenful, a working set, and everything today.
	logTailDefault = 1000
)

// logTailSteps are the buffer sizes L cycles through.
var logTailSteps = []int{200, 1000, 5000, 20000}

// LogLine is one collected line.
type LogLine struct {
	When    time.Time
	Service string
	Pod     string
	NS      string
	Stream  string // stdout | stderr
	Text    string
}

// LogView is the pane's data.
type LogView struct {
	Lines    []LogLine
	File     string
	Query    string
	Warnings []string
}

// serviceOf turns a pod name into the service's name by dropping what the
// controller appended: a replicaset hash and a pod id for a Deployment, or
// an ordinal for a StatefulSet.
//
//	athlete-59c5b49dc7-wv6f5 -> athlete
//	kafka-0                  -> kafka
//	vector-collector-c594f5884-qc46v -> vector-collector
//
// Written as a scanner because the rule is two sentences and a pattern makes
// it one unreadable one.
func serviceOf(pod string) string {
	if pod == "" {
		return ""
	}
	parts := strings.Split(pod, "-")
	// a statefulset ordinal: the last segment is all digits
	if n := len(parts); n > 1 && isDigits(parts[n-1]) {
		return strings.Join(parts[:n-1], "-")
	}
	// a deployment pod: <name>-<replicaset hash>-<pod id>
	if n := len(parts); n > 2 && isPodID(parts[n-1]) &&
		isReplicaHash(parts[n-2]) {
		return strings.Join(parts[:n-2], "-")
	}
	return pod
}

// isPodID is the five-character suffix kubernetes gives a pod.
func isPodID(s string) bool {
	if len(s) != 5 {
		return false
	}
	return isLowerAlnum(s)
}

// isReplicaHash is the replicaset's own suffix: 6 to 10 lower alphanumerics.
func isReplicaHash(s string) bool {
	if len(s) < 6 || len(s) > 10 {
		return false
	}
	return isLowerAlnum(s)
}

// isLowerAlnum tells a kubernetes name from anything else, which is how a
// pod name is picked out of a line without a pattern.
func isLowerAlnum(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return s != ""
}

// fleetLogsFetched carries a read back into the loop.
//
// Named for the fleet rather than for logs because the loop already carries
// logsFetched for a single pod's kubectl logs -- two different things that both
// deserve the obvious name, and the wrong one silently routing to the wrong
// screen is a bug nobody would find quickly.
type fleetLogsFetched struct{ v LogView }

// fetchLogs pulls the last n lines. The read is bounded IN THE POD -- tail
// runs where the bytes are and only the tail crosses the wire -- and n is
// the operator's to choose, because "how much history do I want in front of
// me" is a question only they can answer.
//
// Searching happens locally, over what was pulled, the way it does in a pager:
// type a query and jump between matches rather than making a round trip per
// keystroke.
//
// A remote grep is still there for the case the buffer cannot answer -- the
// string is older than the lines you pulled -- and it is a deliberate second
// key rather than the default.
func fetchLogs(kubeCtx, query string, n int) LogView {
	v := LogView{Query: query}
	if n <= 0 {
		n = logTailDefault
	}
	pod, ns, err := collectorPod(kubeCtx)
	if err != nil {
		v.Warnings = append(v.Warnings, err.Error())
		return v
	}
	// The file is found, not computed. It used to be
	// "containers-"+time.Now().Format(...) -- the local date -- while the
	// collector names its files in UTC.
	//
	// Between the two midnights puffin read yesterday's file and showed a
	// frozen screen with no sign of it: at 19:23 PDT the newest line on
	// screen was 23:59:59Z, two and a half hours stale, and would have
	// stayed that way until local midnight.
	//
	// A timezone-correct computation would fix today's symptom and keep the
	// assumption.
	//
	// Asking the pod which file is newest has no assumption in it, costs
	// nothing extra -- it rides in the read that was happening anyway --
	// and is the same rule the collector and the broker are found by:
	//
	// a tool that works out where a thing should be works until it is
	// somewhere else.
	sh := logReadScript(logDir, query, n)
	out,
		err := exec.Command(
		"kubectl",
		"--context",
		kubeCtx,
		"exec",
		"-n",
		ns,
		pod,
		"--", "sh", "-c", sh).
		Output()
	if err != nil && len(out) == 0 {
		v.Warnings = append(
			v.Warnings,
			"reading "+logDir+": "+err.Error(),
		)
		return v
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	// the first line names the file the rest came out of, so the header can
	// say where it is reading rather than where it assumed
	if len(lines) > 0 && strings.HasPrefix(lines[0], "FILE:") {
		v.File = strings.TrimPrefix(lines[0], "FILE:")
		lines = lines[1:]
	} else if len(lines) > 0 && lines[0] == "NOFILES" {
		v.Warnings = append(v.Warnings,
			"no containers-*.jsonl in "+logDir+" -- is the "+
				"collector writing?")
		return v
	}
	for _, line := range lines {
		if l, ok := parseLogLine(line); ok {
			v.Lines = append(v.Lines, l)
		}
	}
	sort.SliceStable(
		v.Lines,
		func(
			i,
			j int,
		) bool {
			return v.Lines[i].When.Before(v.Lines[j].When)
		},
	)
	return v
}

// logReadScript is what runs inside the pod: find the newest file, name it,
// then read the tail of it -- or grep it, when there is a filter.
//
// Split out so the quoting is testable without a cluster. Everything in it
// is bounded where the bytes are: today's file is 121MB and nothing but the
// answer crosses the wire.
func logReadScript(dir, query string, n int) string {
	read := fmt.Sprintf(`tail -n %d "$f"`, n)
	if q := strings.TrimSpace(query); q != "" {
		// the deliberate remote search: fixed-string, because an
		// operator typing a service name or an error string is not
		// writing a regex and a stray bracket should not become a
		// syntax error on somebody else's machine
		read = fmt.Sprintf(
			`grep -F -- %s "$f" | tail -n %d`,
			shellQuote(query),
			n,
		)
	}
	// sort before tail because ls -1 of a glob is not guaranteed ordered,
	// and the dated names sort lexically into date order by construction
	return fmt.Sprintf(
		`f=$(ls -1 %s/containers-*.jsonl 2>/dev/null | sort | tail -1`+
			`); `+
			`[ -n "$f" ] || { echo "NOFILES"; exit 0; }; echo "FI`+
			`LE:$f"; %s`,
		dir,
		read,
	)
}

// collectorPod finds the collector by label rather than by a name with a
// replicaset hash in it: the hash changes on every redeploy, and a tool that
// hardcodes one works until the next one.
//
// The label is also what survived the logstash-to-vector swap intact in shape
// while changing in value, so the one line below is the whole of the discovery
// change.
func collectorPod(kubeCtx string) (string, string, error) {
	out,
		err := exec.Command(
		"kubectl", "--context", kubeCtx,
		"get", "pods", "-A",
		"-l", logSelector, "-o",
		"jsonpath={.items[0].metadata.name} "+
			"{.items[0].metadata.namespace}").
		Output()
	if err != nil {
		return "", "", fmt.Errorf(
			"cannot find the log collector: %w",
			err,
		)
	}
	f := strings.Fields(string(out))
	if len(f) < 2 {
		return "", "", fmt.Errorf(
			"no pod matches %s -- is vector-collector deployed?",
			logSelector,
		)
	}
	return f[0], f[1], nil
}

// parseLogLine reduces one collector record.
//
// vector's shape is the one the collector's manifest builds on purpose, and
// every key read here is named there: the CRI envelope split into cri_ts /
// cri_stream / payload, the pod and namespace recovered from the kubernetes
// filename convention, and the raw path kept under log.file.path.
//
// The logstash keys are still read as a fallback, and that is not politeness
// to a dead component. Retention keeps seven days of files on the same mount,
// so for a week after the cutover the buffer contains both shapes, and a
// reader that understands only today's goes blind halfway down the screen.
//
// The timestamp is the container's, not the collector's.
//
// logstash wrote @timestamp; vector writes `timestamp`, and it means when
// vector READ the line -- on a restart it re-reads history and stamps thousands
// of old lines with the same second, which would sort the pane into nonsense
// and make the watch high-water mark fire on ancient text.
//
// cri_ts is when the service actually spoke.
//
// It is also why the swap could not be a label change alone: with no @timestamp
// present every line parsed to the zero time, which sorts, renders 00:00:00,
// and never trips a watch -- wrong in four places and loud in none of them.
func parseLogLine(raw string) (LogLine, bool) {
	var rec struct {
		CRITime   string `json:"cri_ts"`     // when the container spoke
		Read      string `json:"timestamp"`  // when vector read it
		Shipped   string `json:"@timestamp"` // what logstash wrote
		Message   string `json:"message"`
		Payload   string `json:"payload"`
		Stream    string `json:"cri_stream"`
		Pod       string `json:"pod"`
		Namespace string `json:"namespace"`
		Log       struct {
			File struct {
				Path string `json:"path"`
			} `json:"file"`
		} `json:"log"`
	}
	if json.Unmarshal([]byte(raw), &rec) != nil {
		return LogLine{}, false
	}
	l := LogLine{Stream: rec.Stream, Pod: rec.Pod, NS: rec.Namespace}
	// payload is the message with the CRI framing already taken off. When
	// the dissect failed the collector keeps the raw line rather than
	// dropping it -- a collector with an opinion is a collector with a hole
	// -- and then the framing is still on the front, so strip it here.
	if rec.Payload != "" {
		l.Text = strings.TrimSpace(rec.Payload)
	} else {
		l.Text = stripCRI(rec.Message)
	}
	for _, ts := range []string{rec.CRITime, rec.Read, rec.Shipped} {
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			l.When = t
			break
		}
	}
	// the named fields are the collector's own parse of the path. The path
	// is the fallback: for a record whose filename the regex could not
	// read, and for the logstash-era files that never carried the fields at
	// all. /var/log/containers/<pod>_<namespace>_<container>-<id>.log
	if l.Pod == "" {
		base := strings.TrimSuffix(path.Base(rec.Log.File.Path), ".log")
		if parts := strings.Split(base, "_"); len(parts) >= 2 {
			l.Pod, l.NS = parts[0], parts[1]
		}
	}
	l.Service = serviceOf(l.Pod)
	if l.Text == "" {
		return LogLine{}, false
	}
	return l, true
}

// stripCRI removes the container runtime's framing from the front of a line.
func stripCRI(msg string) string {
	f := strings.SplitN(msg, " ", 4)
	if len(f) == 4 && (f[1] == "stdout" || f[1] == "stderr") {
		return strings.TrimSpace(f[3])
	}
	return strings.TrimSpace(msg)
}

// shellQuote makes a query safe to hand to sh -c. The query is typed by an
// operator and goes through a shell inside a pod, so it is quoted rather
// than trusted -- a search box that can run commands is not a search box.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// logsPane is the cluster's stdout, with a vim-shaped search: pull a
// buffer, then / to find and n/N to walk the matches. A round trip per
// keystroke is a worse search box than a pager that already has the
// thousand lines in hand.
type logsPane struct {
	v       LogView
	lines   int // how many to pull
	cursor  int
	offset  int
	query   string
	typing  bool
	matches []int
	matcher *Watch
	window  int // rows the last render showed, for the page keys
	vis     visual
	yanked  string // what the last yank did, shown until the next key
	atMatch int
	// filter is a grep that runs IN THE POD, before anything is shipped or
	// drawn.
	//
	// It is a different thing from query, which searches what was already
	// pulled, and the difference is the whole point:
	//
	// a buffer of 1000 lines is two minutes of this estate, so an in-buffer
	// search for a quiet service answers "no matches" when the truth is
	// "not in the last two minutes".
	//
	// With a filter set, the same 1000 lines are 1000 lines of postgres,
	// which is hours.
	filter    string
	filtering bool // the filter box is open
	// anchor is the line the cursor was standing on, kept so a refresh can
	// restore the position rather than the index.
	//
	// The buffer is a fresh tail every ten seconds: every line that arrived
	// shifts every older line down, so holding the index slides you quietly
	// down the screen, which looks exactly like holding still.
	anchor  lineKey
	drifted bool // the anchored line aged out of the buffer, and it is said
	// the newest line already checked against the watches, so a refresh
	// notifies about what arrived rather than about the whole buffer again
	seen time.Time
}

// lineKey identifies one line across two reads of the same file. There is no
// id in a log line, so it is the three things that together do not repeat:
// when it was written, who wrote it, and what it said.
type lineKey struct {
	when time.Time
	pod  string
	text string
}

// keyOf identifies a line across refetches, so the cursor stays on the
// line it was on when new ones arrive above it.
func keyOf(
	l LogLine,
) lineKey {
	return lineKey{when: l.When, pod: l.Pod, text: l.Text}
}

// zero is "no line is marked", which is a different state from a line
// that happens to be empty.
func (k lineKey) zero() bool {
	return k.when.IsZero() && k.pod == "" && k.text == ""
}

// Tick makes the pane refresh itself, which is what makes a watch a watch:
// the point is not to be looking. Ten seconds rather than the agents pane's
// five -- this reads a file inside a pod, and the thing being waited for is
// rarely urgent to the second.
func (p *logsPane) Tick() time.Duration { return 10 * time.Second }

// Key opens the logs pane from the roster.
func (p *logsPane) Key() string { return "l" }

// Title names the pane, and keys its refresh gate.
func (p *logsPane) Title() string { return "logs" }

// Help is the footer: the search keys, which are vim's, so nobody has to
// learn a second set.
func (p *logsPane) Help() string {
	return fmt.Sprintf(
		"f: filter IN THE POD (greps the whole file) · / search what "+
			"was pulled · n/N match · q: back\n"+
			"W: watch the search · E: what the companion does "+
			"about it · C: who the companion is · e: "+
			"poke it\n"+
			"ctrl+f/b page · ctrl+d/u half · H/M/L screen · g/G "+
			"ends · v: visual · y: yank · Y: yank all\n"+
			"B: pull more lines (now %d) · esc drops the search, "+
			"then the filter · /slashes/ make a pattern "+
			"a regex",
		p.lines,
	)
}

// Capturing says the pane owns the keyboard while a pattern is being
// typed, so q does not close it mid-word.
func (p *logsPane) Capturing() bool { return p.typing || p.filtering }

// HandlesEsc claims esc while there is something to put down: a box that is
// open, a search, or a filter. Anything else and esc belongs to the frame.
func (p *logsPane) HandlesEsc() bool {
	return p.typing || p.filtering || p.query != "" || p.filter != ""
}

// Load pulls a buffer rather than following, because a search wants the
// text already in hand: a round trip per keystroke is a worse search box.
func (p *logsPane) Load(string) tea.Cmd {
	if p.lines == 0 {
		p.lines = logTailDefault
	}
	ctx, n, q := kubeContext(), p.lines, p.filter
	return func() tea.Msg { return fleetLogsFetched{fetchLogs(ctx, q, n)} }
}

// Update folds in lines or a keystroke, and holds whether the pane is
// following the tail or scrolled back, which the header states.
func (p *logsPane) Update(msg tea.Msg) (pane, tea.Cmd) {
	switch msg := msg.(type) {
	case fleetLogsFetched:
		wasAtBottom := p.cursor >= len(p.v.Lines)-1
		p.v = msg.v
		p.check()
		p.find()
		// Follow the bottom only if you were already there.
		//
		// A log screen that refreshes itself every ten seconds and
		// yanks the cursor to the newest line is unreadable the moment
		// you scroll back to look at something -- which is the entire
		// reason to scroll back.
		//
		// This is the same bargain tail -f makes, and the same one the
		// little CRT window already made; the logs pane simply never
		// got it.
		//
		// Scrolled back, the cursor goes back on the LINE it was on,
		// not on the index it had.
		//
		// The buffer is a fresh tail: every line that arrived in those
		// ten seconds shifted every older line down, so keeping the
		// index moved the reader a few lines down the screen each
		// refresh
		//
		// -- quietly, which is worse than being yanked, because a slide
		// looks exactly like holding still.
		switch {
		case wasAtBottom:
			p.cursor = maxInt(0, len(p.v.Lines)-1)
			p.anchor, p.drifted = p.cursorLine(), false
		default:
			p.restoreAnchor()
		}
		return p, nil
	case tea.KeyMsg:
		// the filter box: what is typed here goes to the POD, not to
		// the buffer, so it is a separate box from / and says so
		if p.filtering {
			switch msg.String() {
			case "esc":
				p.filtering = false
				return p, nil
			case "enter":
				p.filtering = false
				// re-read with the filter applied, from the
				// top: the buffer about to arrive has nothing
				// to do with the one on screen
				p.cursor, p.offset, p.anchor, p.drifted =
					0, 0, lineKey{}, false
				return p, p.Load("")
			case "backspace":
				if p.filter != "" {
					p.filter = p.filter[:len(p.filter)-1]
				}
				return p, nil
			}
			if r := msg.String(); len([]rune(r)) == 1 {
				p.filter += r
			}
			return p, nil
		}
		if p.typing {
			switch msg.String() {
			case "esc":
				p.typing, p.query = false, ""
				p.find()
				return p, nil
			case "enter":
				p.typing = false
				p.find()
				p.jump(0)
				return p, nil
			case "backspace":
				if p.query != "" {
					p.query = p.query[:len(p.query)-1]
				}
				return p, nil
			}
			if r := msg.String(); len([]rune(r)) == 1 {
				p.query += r
			}
			return p, nil
		}
		// any key clears the last yank's message: it is a receipt, not
		// a status, and one that lingers starts lying
		p.yanked = ""
		switch msg.String() {
		case "v":
			// visual LINE mode. Pressing v again leaves it, the way
			// it does in vim, rather than needing esc for a thing
			// that is not modal in any other sense.
			p.vis.on, p.vis.anchor = !p.vis.on, p.cursor
			return p, nil
		case "y":
			lo, hi := p.vis.span(p.cursor)
			p.yanked = yank(p.rawLines(), lo, hi)
			p.vis.on = false
			return p, nil
		case "Y":
			// the whole buffer, for the times the thing you want to
			// paste is "all of it"
			p.yanked = yank(p.rawLines(), 0, len(p.v.Lines)-1)
			p.vis.on = false
			return p, nil
		case "/":
			p.typing, p.query = true, ""
			return p, nil
		case "f":
			// filter IN THE POD.
			//
			// The read that follows greps the whole of the newest
			// file and brings back n matching lines, so the buffer
			// stops being "the last two minutes of everything" and
			// becomes "the last n lines of this", which is hours.
			p.filtering = true
			return p, nil
		case "E":
			// what the companion does about the watch on the
			// current search. W sets the watch; E says what it
			// means.
			if p.query != "" {
				cycleWatchEmote(p.query, "logs")
			}
			return p, nil
		case "W":
			// watch what is currently being searched for. The
			// search box is already the place you typed the thing
			// you care about, so watching it is one key rather than
			// a second prompt.
			if p.query == "" {
				return p, nil
			}
			for _, w := range watchesFor("logs") {
				if w.Pattern == p.query {
					dropWatch(p.query, "logs")
					return p, nil
				}
			}
			addWatch(p.query, "logs")
			enableNotify(true)
			rememberNotify(true)
			return p, nil
		case "esc":
			if p.vis.on {
				p.vis.on = false
				return p, nil
			}
			if p.query != "" {
				p.query, p.matches = "", nil
				return p, nil
			}
			// nothing left to put down but the filter, so put that
			// down and go back to the whole estate
			if p.filter != "" {
				p.filter = ""
				p.cursor, p.offset, p.anchor, p.drifted =
					0, 0, lineKey{}, false
				return p, p.Load("")
			}
			return p, nil
		case "n":
			p.jump(1)
		case "N":
			p.jump(-1)
		case "down", "j":
			if p.cursor < len(p.v.Lines)-1 {
				p.cursor++
			}
		case "up", "k":
			if p.cursor > 0 {
				p.cursor--
			}
		case "g":
			p.cursor = 0
		case "G":
			p.cursor = maxInt(0, len(p.v.Lines)-1)
		// vim's motions, because this is a pager and everyone's hands
		// already know them. L was the buffer-size key, which is a bad
		// choice in a pager: in vim L means the bottom of the screen.
		case "ctrl+f", "pgdown":
			p.cursor = clampInt(
				p.cursor+p.page(),
				0,
				len(p.v.Lines)-1,
			)
		case "ctrl+b", "pgup":
			p.cursor = clampInt(
				p.cursor-p.page(),
				0,
				len(p.v.Lines)-1,
			)
		case "ctrl+d":
			p.cursor = clampInt(
				p.cursor+p.page()/2,
				0,
				len(p.v.Lines)-1,
			)
		case "ctrl+u":
			p.cursor = clampInt(
				p.cursor-p.page()/2,
				0,
				len(p.v.Lines)-1,
			)
		case "H":
			p.cursor = clampInt(p.offset, 0, len(p.v.Lines)-1)
		case "M":
			p.cursor = clampInt(
				p.offset+p.page()/2,
				0,
				len(p.v.Lines)-1,
			)
		case "L":
			p.cursor = clampInt(
				p.offset+p.page()-1,
				0,
				len(p.v.Lines)-1,
			)
		case "B":
			// cycle the buffer size and re-pull: how much history
			// you want in front of you is a question only the
			// operator can answer
			for i, n := range logTailSteps {
				if n == p.lines {
					p.lines = logTailSteps[(i+1)%len(
						logTailSteps,
					)]
					return p, p.Load("")
				}
			}
			p.lines = logTailSteps[0]
			return p, p.Load("")
		}
		// whatever the key just did to the cursor, THAT line is now the
		// one to come back to. Without this a refresh would restore the
		// place they were before they moved, which is its own kind of
		// slide.
		p.anchor, p.drifted = p.cursorLine(), false
	}
	return p, nil
}

// find records which lines match, so n and N are a walk rather than a scan.
// The search uses the SAME matcher a watch does, so /slashes/ mean a regex
// in the search box exactly as they do in a watch -- one rule to learn,
// applied in both places you would type a pattern.
func (p *logsPane) find() {
	p.matches, p.matcher = nil, nil
	if p.query == "" {
		return
	}
	w := &Watch{Pattern: p.query}
	w.compile()
	p.matcher = w
	for i, l := range p.v.Lines {
		if w.Matches(l.Text) || w.Matches(l.Service) {
			p.matches = append(p.matches, i)
		}
	}
}

// matchSpans finds where in a line the term actually is. Highlighting the
// LINE says "it is in here somewhere" and leaves you reading; highlighting
// the term is the answer to the question you typed.
func (w *Watch) matchSpans(line string) [][2]int {
	if w == nil || w.bad != "" || line == "" {
		return nil
	}
	if w.re != nil {
		var out [][2]int
		for _, m := range w.re.FindAllStringIndex(line, -1) {
			// a zero-width match (a pattern like /^/ or /x*/) would
			// paint nothing forever and loop the renderer, so it is
			// skipped
			if m[1] > m[0] {
				out = append(out, [2]int{m[0], m[1]})
			}
		}
		return out
	}
	var out [][2]int
	low, q := strings.ToLower(line), strings.ToLower(w.Pattern)
	if q == "" {
		return nil
	}
	for at := 0; ; {
		i := strings.Index(low[at:], q)
		if i < 0 {
			return out
		}
		out = append(out, [2]int{at + i, at + i + len(q)})
		at += i + len(q)
	}
}

// paintMatches renders a line with the matched term picked out. The
// unmatched parts keep the ordinary log highlighting when the line is not
// the cursor's, so a search does not flatten everything else it touches.
func paintMatches(
	s Styles,
	line string,
	spans [][2]int,
	ground lipgloss.Style,
	syntax bool,
) string {
	if len(spans) == 0 {
		if syntax {
			return HighlightLog(line, s)
		}
		return ground.Render(line)
	}
	var b strings.Builder
	at := 0
	for _, sp := range spans {
		if sp[0] > at {
			seg := line[at:sp[0]]
			if syntax {
				b.WriteString(HighlightLog(seg, s))
			} else {
				b.WriteString(ground.Render(seg))
			}
		}
		b.WriteString(s.MatchTerm.Render(line[sp[0]:sp[1]]))
		at = sp[1]
	}
	if at < len(line) {
		seg := line[at:]
		if syntax {
			b.WriteString(HighlightLog(seg, s))
		} else {
			b.WriteString(ground.Render(seg))
		}
	}
	return b.String()
}

// jump moves to the next or previous match, wrapping. Wrapping is the
// behaviour every pager has and the absence of it is the thing people
// notice.
func (p *logsPane) jump(dir int) {
	if len(p.matches) == 0 {
		return
	}
	if dir == 0 {
		for i, at := range p.matches {
			if at >= p.cursor {
				p.atMatch, p.cursor = i, at
				return
			}
		}
		p.atMatch, p.cursor = 0, p.matches[0]
		return
	}
	p.atMatch = (p.atMatch + dir + len(p.matches)) % len(p.matches)
	p.cursor = p.matches[p.atMatch]
}

// cursorLine is the line the cursor is standing on, as a key that survives
// the buffer being replaced.
func (p *logsPane) cursorLine() lineKey {
	if p.cursor < 0 || p.cursor >= len(p.v.Lines) {
		return lineKey{}
	}
	return keyOf(p.v.Lines[p.cursor])
}

// restoreAnchor puts the cursor back on the line it was on. When that line
// has aged out of the buffer -- the tail moved past it -- the pane SAYS so
// rather than landing the cursor somewhere near and letting the reader
// believe they never moved.
func (p *logsPane) restoreAnchor() {
	if p.anchor.zero() {
		p.anchor = p.cursorLine()
	}
	for i, l := range p.v.Lines {
		if keyOf(l) == p.anchor {
			p.cursor, p.drifted = i, false
			return
		}
	}
	// gone: hold the top of the buffer, which is the nearest thing to where
	// they were, and mark it so the header can say what happened
	if p.cursor >= len(p.v.Lines) {
		p.cursor = maxInt(0, len(p.v.Lines)-1)
	}
	p.drifted = !p.anchor.zero() && len(p.v.Lines) > 0
}

// rawLines is what the service actually logged, in order: no highlighting,
// no truncation, no escape sequences. It is what goes on the clipboard.
func (p *logsPane) rawLines() []string {
	out := make([]string, len(p.v.Lines))
	for i, l := range p.v.Lines {
		// reassembled rather than rendered: timestamp, where it came
		// from, and what it said. This is the line as a person would
		// want it in a ticket, which is not the same as the line as the
		// pane draws it.
		out[i] = fmt.Sprintf(
			"%s %s/%s %s %s",
			l.When.Format(
				time.RFC3339,
			),
			l.NS,
			l.Pod,
			l.Stream,
			l.Text,
		)
	}
	return out
}

// View draws the buffer highlighted by the theme, and says which of
// following or scrolled back it is doing.
func (p *logsPane) View(s Styles, width, height int) string {
	var b strings.Builder
	for _, w := range p.v.Warnings {
		b.WriteString(s.Caution.Render("! "+w) + "\n")
	}
	if len(p.v.Lines) == 0 {
		// an empty buffer with a filter set is a REAL answer -- the
		// grep ran over the whole of today's file, in the pod -- and it
		// is a different answer from not having looked yet
		if p.filter != "" {
			b.WriteString(
				s.Caution.Render(
					"nothing in "+path.Base(p.v.File)+
						" matches "+p.filter,
				) + "\n",
			)
			b.WriteString(
				s.Dim.Render(
					"the whole file was searched, not " +
						"just the buffer · " +
						"f changes it · esc clears it",
				),
			)
			return b.String()
		}
		b.WriteString(
			s.Dim.Render("no lines yet · r to read " + p.v.File),
		)
		return b.String()
	}
	// how much log is in front of you, over what span, and from whom.
	//
	// "1000 lines" alone is unanswerable: 1000 lines of this estate is two
	// minutes on a quiet day and twenty seconds when valhalla is being
	// probed, and which of those you are holding decides whether a search
	// that found nothing means anything at all.
	span, from, to := p.span()
	head := fmt.Sprintf("%d lines · %s–%s (%s) · %s", len(p.v.Lines),
		from.Format("15:04:05"), to.Format("15:04:05"), humanSpan(span),
		path.Base(p.v.File))
	b.WriteString(s.Dim.Render(head))
	if p.filter != "" {
		b.WriteString(
			s.Accent.Render("   filtered in the pod: "+p.filter) +
				s.Dim.Render(" · esc clears"),
		)
	}
	// say which mode you are in, because "it stopped following" and "it is
	// following" look identical until the next refresh arrives
	if p.yanked != "" {
		b.WriteString(s.Accent.Render("  " + p.yanked))
	}
	if p.vis.on {
		lo, hi := p.vis.span(p.cursor)
		b.WriteString(
			s.AccentAlt.Render(
				fmt.Sprintf("  visual %d lines", hi-lo+1),
			),
		)
	}
	if p.cursor >= len(p.v.Lines)-1 {
		b.WriteString(s.Ok.Render("  following"))
	} else {
		b.WriteString(s.AccentAlt.Render("  scrolled back") +
			s.Dim.Render(" · G to follow again"))
	}
	// the line you were reading fell off the top of the buffer. Saying so
	// is the whole point: silently landing the cursor somewhere near looks
	// exactly like not having moved.
	if p.drifted {
		b.WriteString(
			s.Caution.Render(
				"  the line you were on has scrolled out of "+
					"the buffer",
			) +
				s.Dim.Render(
					" · B pulls more",
				),
		)
	}
	// what is being waited for, on the screen. A standing instruction that
	// is invisible is one you forget you gave, and then cannot explain.
	if ws := watchesFor("logs"); len(ws) > 0 {
		labels := make([]string, 0, len(ws))
		for _, w := range ws {
			labels = append(labels, w.Label())
		}
		b.WriteString(
			s.Accent.Render(
				"   watching: " + strings.Join(labels, ", "),
			),
		)
	}
	if p.query != "" {
		b.WriteString(
			s.AccentAlt.Render(fmt.Sprintf("   /%s", p.query)) +
				s.Dim.Render(
					fmt.Sprintf(
						"  %d matches",
						len(p.matches),
					),
				),
		)
	}
	if p.typing {
		b.WriteString(s.Accent.Render("   /" + p.query + "_"))
	}
	if p.filtering {
		b.WriteString(s.Accent.Render("   filter: "+p.filter+"_") +
			s.Dim.Render(
				" · runs grep in the pod over the whole file "+
					"· enter",
			))
	}
	b.WriteString("\n")
	// who is actually in this buffer. The noisiest service is usually not
	// the one being looked for, and knowing that it is 600 of the 1000
	// lines is the difference between filtering and scrolling.
	b.WriteString(s.Dim.Render(p.sources(width)) + "\n")

	size := height - 10
	if height <= 0 {
		size = len(p.v.Lines)
	} else if size < 3 {
		size = 3
	}
	// remembered so the page keys know how far a page is: the view knows
	// the height and the update loop does not
	p.window = size
	off := windowOffset(len(p.v.Lines), p.cursor, size, p.offset)
	p.offset = off
	last := minInt(off+size, len(p.v.Lines))
	matched := map[int]bool{}
	for _, i := range p.matches {
		matched[i] = true
	}
	for i := off; i < last; i++ {
		l := p.v.Lines[i]
		on := i == p.cursor
		// a selected line is not the cursor line: the cursor is where
		// you are and the selection is what you would take, and while
		// extending a visual span you need to see both at once
		sel := p.vis.covers(i, p.cursor)
		marker := s.On(s.Row, on).Render("  ")
		switch {
		case on:
			marker = s.On(s.RowSel, on).Render("▸ ")
		case sel:
			marker = s.Accent.Render("│ ")
		}
		stamp := l.When.Format("15:04:05")
		name := l.Service
		if name == "" {
			name = "?"
		}
		style := s.AccentAlt
		if l.Stream == "stderr" {
			style = s.Warn
		}
		// a matched line gets a ground of its own, under the cursor's,
		// so you can see which lines matched while still knowing which
		// one you are standing on
		ground := s.Row
		switch {
		case on:
			ground = s.On(s.Row, true)
		case sel:
			ground = s.Row.Background(s.MatchBg)
		case matched[i]:
			ground = s.Row.Background(s.MatchBg)
		}
		cell := func(st lipgloss.Style, text string) string {
			if on {
				return s.On(st, true).Render(text)
			}
			if sel || matched[i] {
				return st.Background(s.MatchBg).Render(text)
			}
			return st.Render(text)
		}
		row := marker + cell(
			s.Dim,
			pad(stamp, 10),
		) + cell(
			style,
			pad(name, 20),
		)
		// structured lines are reordered so the message survives the
		// terminal's width: json keys arrive alphabetically, which puts
		// "addr" first and "msg" in the middle, and truncation then
		// eats the only part that says what happened
		text := l.Text
		if lvl, flat, ok := structuredLine(text); ok {
			text = flat
			if lvl != "" {
				row += cell(levelStyle(s, lvl), pad(lvl, 6))
			} else {
				row += cell(s.Dim, pad("", 6))
			}
		} else {
			row += cell(s.Dim, pad("", 6))
		}
		text = padTo(text, maxInt(20, width-46))
		// the term itself is picked out inside the line. Syntax
		// highlighting is kept for lines that are neither matched nor
		// under the cursor: a search should not flatten everything it
		// touches.
		row += paintMatches(
			s,
			text,
			p.matcher.matchSpans(text),
			ground,
			!on && !matched[i],
		)
		b.WriteString(row + "\n")
	}
	return b.String()
}

// span is what the buffer actually covers: oldest, newest, and the distance
// between them.
func (p *logsPane) span() (time.Duration, time.Time, time.Time) {
	var from, to time.Time
	for _, l := range p.v.Lines {
		if l.When.IsZero() {
			continue
		}
		if from.IsZero() || l.When.Before(from) {
			from = l.When
		}
		if l.When.After(to) {
			to = l.When
		}
	}
	return to.Sub(from), from, to
}

// humanSpan is a duration, not an age. humanSince says "10m ago", which is
// the right words for a timestamp and the wrong ones for the distance
// between two of them -- a buffer covering ten minutes is not ten minutes
// old, and the two are read differently at speed.
func humanSpan(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf(
			"%dm%ds",
			int(d.Minutes()),
			int(d.Seconds())%60,
		)
	case d < 24*time.Hour:
		return fmt.Sprintf(
			"%dh%dm",
			int(d.Hours()),
			int(d.Minutes())%60,
		)
	}
	return fmt.Sprintf("%dd%dh", int(d.Hours()/24), int(d.Hours())%24)
}

// sources is who wrote the buffer, loudest first. It answers "from whom",
// and it is the argument for pressing f: when one service is two thirds of
// the screen, scrolling is not going to find the other third.
func (p *logsPane) sources(width int) string {
	if len(p.v.Lines) == 0 {
		return ""
	}
	n := map[string]int{}
	for _, l := range p.v.Lines {
		name := l.Service
		if name == "" {
			name = "?"
		}
		n[name]++
	}
	names := make([]string, 0, len(n))
	for name := range n {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if n[names[i]] != n[names[j]] {
			return n[names[i]] > n[names[j]]
		}
		return names[i] < names[j]
	})
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s %d", name, n[name]))
	}
	line := fmt.Sprintf(
		"%d services · ",
		len(names),
	) + strings.Join(
		parts,
		" · ",
	)
	if width > 20 && len([]rune(line)) > width-2 {
		line = string([]rune(line)[:width-3]) + "\u2026"
	}
	return line
}

// check tells the watches about lines that have arrived since the last look.
// Only new ones: a watch that fires again for every line already on the
// screen is a watch you turn off within a minute.
func (p *logsPane) check() {
	ws := watchesFor("logs")
	if len(ws) == 0 {
		p.seen = newestLine(p.v.Lines)
		return
	}
	newest := p.seen
	for _, l := range p.v.Lines {
		if !l.When.After(p.seen) {
			continue
		}
		if l.When.After(newest) {
			newest = l.When
		}
		for _, w := range ws {
			if w.Matches(l.Text) || w.Matches(l.Service) {
				// the subject is the watch, so a burst of
				// matching lines is one notification rather
				// than forty
				Emit(
					Event{
						Kind:    WatchFired,
						Subject: "logs · " + w.Pattern,
						Detail: l.Service + ": " +
							"" + oneLine(
							l.Text,
							140,
						),
						Emote: w.Emote,
					},
				)
				break
			}
		}
	}
	p.seen = newest
}

// newestLine is the high-water mark: on the first read nothing is "new", or
// opening the pane would announce the entire buffer.
func newestLine(lines []LogLine) time.Time {
	var t time.Time
	for _, l := range lines {
		if l.When.After(t) {
			t = l.When
		}
	}
	return t
}

// page is how many rows a page is: what the window shows, less one so a
// line stays on screen across the jump. Vim keeps that line for a reason --
// it is how you know where you landed.
func (p *logsPane) page() int {
	n := p.window - 1
	if n < 1 {
		return 1
	}
	return n
}
