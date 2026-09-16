package main

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// Headless puffin: watch a thing, say something when it happens.
//
//	puffin watch logs "migration failed"
//	puffin watch bus flipr.oplog /restart|panic/
//	puffin watch agents waiting
//
// Plain words rather than a json argument, per the estate's own rule -- json in
// the shell is a punishment nobody ordered, and it is puffin's readme that says
// so.
//
// The source comes first because it decides what the rest means, which is the
// same shape kubectl and git use and the reason nobody has to look those up.
//
// This is the same watch layer the panes use, running without a screen. It
// exists because the panes can only notice things while they are open, and
// the whole point of being told about something is not having to be looking.

// watchUsage is what a wrong invocation gets. Examples, not a grammar: a
// usage message people can copy is one they do not have to parse.
const watchUsage = `usage:
  puffin watch logs <pattern>              every service's stdout
  puffin watch bus [topic] <pattern>       the kafka topics, decoded
  puffin watch agents <pattern>            claude sessions on this host

  a pattern is plain text; /slashes/ make it a regex

  --once        exit on the first match, for scripting
  --interval D  how often to look (default 10s for logs, 15s for the bus,
                5s for agents)
  --quiet       print matches, do not notify

examples:
  puffin watch logs "connection refused"
  puffin watch bus flipr.oplog /SetFlag/
  puffin watch agents waiting --once && say "your agent needs you"`

// cliWatch runs a watch without a screen.
func cliWatch(domain string, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, watchUsage)
		return 2
	}
	source := args[0]
	rest, once, quiet, interval := parseWatchFlags(args[1:])

	topic := ""
	if source == "bus" && len(rest) > 1 {
		topic, rest = rest[0], rest[1:]
	}
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, watchUsage)
		return 2
	}
	pattern := strings.Join(rest, " ")

	w := &Watch{Pattern: pattern, Where: source}
	w.compile()
	if w.bad != "" {
		fmt.Fprintf(
			os.Stderr,
			"that pattern is not a valid regex: %s\n",
			w.bad,
		)
		return 2
	}
	if interval == 0 {
		interval = defaultInterval(source)
	}
	if !quiet {
		enableNotify(true)
		listenNotify()
	}

	// ctrl-c ends it cleanly rather than leaving a half-written line: this
	// is meant to run in a pane for hours
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	fmt.Fprintf(
		os.Stderr,
		"watching %s for %s, every %s%s\n",
		source,
		w.Label(),
		interval,
		map[bool]string{true: " (until the first match)"}[once],
	)

	seen := newSeenSet()
	kubeCtx := kubeContext()
	for {
		hits, err := watchOnce(source, topic, domain, kubeCtx, w, seen)
		if err != nil {
			// a source that is briefly unreachable is not a reason
			// to stop: this runs for hours and clusters get
			// restarted under it
			fmt.Fprintf(
				os.Stderr,
				"%s: %v\n",
				time.Now().Format("15:04:05"),
				err,
			)
		}
		for _, h := range hits {
			fmt.Println(h)
			if !quiet {
				Emit(
					Event{
						Kind: WatchFired,
						Subject: source + " · " +
							"" + pattern,
						Detail: h,
					},
				)
			}
		}
		if once && len(hits) > 0 {
			return 0
		}
		select {
		case <-stop:
			fmt.Fprintln(os.Stderr, "stopped")
			return 0
		case <-time.After(interval):
		}
	}
}

// defaultInterval matches what the panes use: the cadence belongs to what is
// being watched, not to the watcher.
func defaultInterval(source string) time.Duration {
	switch source {
	case "agents":
		return 5 * time.Second
	case "bus":
		return 15 * time.Second
	}
	return 10 * time.Second
}

// parseWatchFlags pulls the options out of the arguments. Hand-rolled rather
// than through the flag package because the pattern is positional and may
// itself start with a dash -- /-\d+/ is a perfectly good thing to watch for.
func parseWatchFlags(
	args []string,
) (rest []string, once, quiet bool, interval time.Duration) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--once":
			once = true
		case "--quiet":
			quiet = true
		case "--interval":
			if i+1 < len(args) {
				i++
				if d, err := time.ParseDuration(
					args[i],
				); err == nil {
					interval = d
				}
			}
		default:
			rest = append(rest, args[i])
		}
	}
	return rest, once, quiet, interval
}

// seenSet remembers what has already been reported, so a watch reports
// arrivals rather than the contents of whatever window it happens to read.
type seenSet struct {
	first bool
	mark  time.Time
	keys  map[string]bool
	state map[string]string
}

// newSeenSet remembers what has already matched, so a watch that fires
// reports a line once rather than on every poll that still sees it.
func newSeenSet() *seenSet {
	return &seenSet{
		first: true,
		keys:  map[string]bool{},
		state: map[string]string{},
	}
}

// watchOnce looks once and returns what is new AND matching. The first look
// is a baseline that reports nothing: starting a watch must not immediately
// report the thousand lines that were already there, which would make
// --once useless and the notification meaningless.
func watchOnce(
	source, topic, domain, kubeCtx string,
	w *Watch,
	seen *seenSet,
) ([]string, error) {
	var hits []string
	switch source {
	case "logs":
		v := fetchLogs(kubeCtx, "", logTailDefault)
		if len(v.Warnings) > 0 && len(v.Lines) == 0 {
			return nil, fmt.Errorf(
				"%s",
				strings.Join(v.Warnings, "; "),
			)
		}
		hits = newLogHits(v.Lines, w, seen)

	case "bus":
		topics := []string{topic}
		if topic == "" {
			t, err := busTopics(kubeCtx)
			if err != nil {
				return nil, err
			}
			topics = t
		}
		for _, tp := range topics {
			msgs, err := busRead(kubeCtx, tp, busMaxMessages)
			if err != nil {
				return nil, err
			}
			for i := range msgs {
				if id := msgs[i].SchemaID; id != 0 {
					if subj, names, err := fetchSchema(
						kubeCtx,
						id,
						msgs[i].MsgIndex,
					); err == nil {
						msgs[i].Schema = subj
						nameFields(&msgs[i], names)
					}
				}
			}
			hits = append(hits, newBusHits(tp, msgs, w, seen)...)
		}

	case "agents":
		hits = newAgentHits(
			fetchAgents(agentWindow).Sessions,
			w,
			seen,
			time.Now(),
		)

	default:
		return nil, fmt.Errorf(
			"no source called %q: logs, bus or agents",
			source,
		)
	}
	seen.first = false
	return hits, nil
}

// The newness rules, lifted out of the I/O so they can be tested.
//
// Each source has a different idea of what "new" means and getting it wrong is
// how a watch becomes a thing you turn off -- but all three were buried inside
// a function that shells out to kubectl, which meant none of them could be
// tested at all.
//
// The headless path measured 0% covered, which was not an oversight so much as
// a consequence: code that mixes I/O with a decision cannot be checked without
// the I/O.

// newLogHits reports lines that arrived after the last look and match.
func newLogHits(lines []LogLine, w *Watch, seen *seenSet) []string {
	var hits []string
	newest := seen.mark
	for _, l := range lines {
		if !l.When.After(seen.mark) {
			continue
		}
		if l.When.After(newest) {
			newest = l.When
		}
		if !seen.first && (w.Matches(l.Text) || w.Matches(l.Service)) {
			hits = append(hits, fmt.Sprintf(
				"%s  %-20s %s",
				l.When.Format(
					"15:04:05",
				),
				l.Service,
				oneLine(l.Text, 160),
			))
		}
	}
	seen.mark = newest
	return hits
}

// newBusHits reports records not seen before. The bus has no cursor, so a
// record is identified by the idempotency key it carries -- the tail gets
// re-read and a heartbeat must not fire twice for that.
func newBusHits(
	topic string,
	msgs []BusMessage,
	w *Watch,
	seen *seenSet,
) []string {
	var hits []string
	for _, m := range msgs {
		k := topic + "/" + recordKey(m)
		if seen.keys[k] {
			continue
		}
		seen.keys[k] = true
		line := topic + " " + shortSchema(
			m.Schema,
		) + " " + recordLine(
			m,
		)
		if !seen.first && w.Matches(line) {
			hits = append(
				hits,
				time.Now().
					Format("15:04:05")+
					"  "+oneLine(
					line,
					200,
				),
			)
		}
	}
	return hits
}

// newAgentHits reports sessions whose state changed and now matches.
func newAgentHits(
	sessions []AgentSession,
	w *Watch,
	seen *seenSet,
	now time.Time,
) []string {
	var hits []string
	for _, s := range sessions {
		state := s.State(now)
		was := seen.state[s.ID]
		seen.state[s.ID] = state
		if seen.first || was == state {
			continue
		}
		line := strings.Join(
			[]string{
				s.Project,
				s.Branch,
				shortModel(s.Model),
				state,
			},
			" ",
		)
		if w.Matches(line) {
			hits = append(hits, fmt.Sprintf(
				"%s  %-18s %-12s %s",
				now.Format(
					"15:04:05",
				),
				orDash(s.Project),
				shortModel(s.Model),
				state,
			))
		}
	}
	return hits
}
