package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Safe mode: show the request, then ask.
//
// A confirmation that does not show what is about to be sent is asking you
// to agree to a sentence rather than to a request.
//
// Puffin had three different confirmations before this -- flipr's reason
// prompt, the cluster screen's y/n, colima's two-keystroke arm -- and not one
// of them showed what was actually going to be sent.
//
// Each said the verb and the target in English, which is a summary written by
// the same code that is about to act. A summary cannot be wrong in an
// interesting way; the request can.
//
// So an Intent carries the request, not a description of it. Everything here is
// contract-first, which means every mutation puffin makes is already a message
// with a shape somebody agreed to -- a SetFlag body, a scale patch, a command
// line.
//
// Rendering that is free and it is the only thing that cannot drift from what
// happens next.
type Intent struct {
	// Verb and Target are the headline: "set flag", "kestrel/v1
	// fetch.terrain".
	Verb   string
	Target string
	// Wire is what will actually be sent, ready to read. JSON for anything
	// that goes over the wire as JSON; a command line for anything that
	// shells out. Never a paraphrase.
	Wire string
	// Kind names the transport, so the screen can say "POST to flipr"
	// rather than leaving you to infer it: rpc, kubectl or shell.
	Kind string
	// Where the request is going: a service name, a context, a host.
	Endpoint string
	// Reversible says whether this can be undone by doing the opposite.
	// Stopping a deployment is; a flag flip is; deleting is not. It decides
	// how loud the confirmation is, and nothing else.
	Reversible bool
}

// pretty formats JSON for reading. A one-line protojson body is correct and
// unreadable, and the whole point here is that somebody reads it before
// pressing the second key.
func pretty(wire string) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(wire), "", "  "); err != nil {
		return wire // not JSON: a command line, shown as written
	}
	return buf.String()
}

// Summary is the one-line version, for a footer or a toast.
func (i Intent) Summary() string {
	return i.Verb + " " + i.Target + " · " + i.Kind + " → " + i.Endpoint
}

// safeMode says whether every mutation is confirmed, or only the ones that
// cannot be undone.
//
// On by default. puffin acts on one cluster and looks at any of them, and the
// cost of being asked one extra time is a keystroke, while the cost of not
// being asked is a flag flipped on the wrong service at three in the morning.
// Somebody who wants it off can say so and it is remembered.
func safeMode() bool { return rememberedSafeMode() }

// needsConfirm decides whether this intent stops and asks.
func (i Intent) needsConfirm() bool {
	if safeMode() {
		return true
	}
	return !i.Reversible
}

// fliprIntent describes a flag flip in flipr's own terms.
func fliprIntent(row FlagRow, newValue, reason, domain string) Intent {
	return Intent{
		Verb:     "set flag",
		Target:   row.Service + "/" + row.Version + " " + row.Key,
		Wire:     pretty(flipBody(row, newValue, reason)),
		Kind:     "rpc flipr.v1.FliprService/SetFlag",
		Endpoint: "flipr." + domain,
		// a flag flips back, and flipr keeps the oplog
		Reversible: true,
	}
}

// lifecycleIntent describes a start, stop or restart as the kubectl that
// will run. The pod is deliberately absent from the target: stopping a pod
// is not a thing, the workload behind it is what moves.
func lifecycleIntent(verb string, w KubeWorkload, ctx string) Intent {
	var wire string
	switch verb {
	case "stop":
		wire = strings.Join([]string{"kubectl", "--context", ctx,
			"-n",
			w.Namespace,
			"scale",
			strings.ToLower(w.Kind) + "/" + w.Name,
			"--replicas=0"}, " ")
	case "start":
		wire = strings.Join([]string{"kubectl", "--context", ctx,
			"-n",
			w.Namespace,
			"scale",
			strings.ToLower(w.Kind) + "/" + w.Name,
			"--replicas=" + itoa(maxInt(1, w.Replicas))}, " ")
	default:
		wire = strings.Join([]string{"kubectl", "--context", ctx,
			"-n", w.Namespace, "rollout", "restart",
			strings.ToLower(w.Kind) + "/" + w.Name}, " ")
	}
	return Intent{
		Verb:     verb,
		Target:   w.Kind + "/" + w.Name + " in " + w.Namespace,
		Wire:     wire,
		Kind:     "kubectl",
		Endpoint: ctx,
		// a stop remembers the replica count it stopped, so it comes
		// back
		Reversible: true,
	}
}

// The confirm key changes every time.
//
// Getting used to hitting return is the entire failure mode of a y/n prompt,
// and it is not a hypothetical:
//
// a prompt in the same place with the same answer every time trains your hands
// to answer it before your eyes reach it, and then the confirmation is not a
// check, it is a speed bump you have learned to ignore.
//
// A key you cannot predict cannot be answered in advance. You have to read the
// screen to know what to press, and reading the screen is the entire thing
// being bought.
//
// The alphabet excludes the keys that already mean something everywhere in
// puffin -- q and esc cancel, r reloads -- and the ones that are easy to hit
// by accident. It excludes j and k hardest of all: those are the motions
// your hands are already holding down when the prompt appears.
const confirmAlphabet = "abcdefghmnopstuvwxyz"

// confirmKeyFor picks the key for one confirmation. Seeded from the clock,
// because the only property required is that the operator cannot know it
// before the prompt is drawn.
func confirmKeyFor(seed int64) string {
	if seed < 0 {
		seed = -seed
	}
	return string(confirmAlphabet[seed%int64(len(confirmAlphabet))])
}

// pending is a confirmation waiting for its key.
type pending struct {
	intent Intent
	key    string
	// what to do if the key arrives. Held rather than reconstructed, so the
	// thing that runs is the thing that was described -- rebuilding the
	// request after the confirmation is how a screen ends up honestly
	// showing one payload and sending another.
	do func() tea.Cmd
}

// arm builds a pending confirmation for an intent.
func arm(i Intent, do func() tea.Cmd) *pending {
	return &pending{
		intent: i,
		key:    confirmKeyFor(time.Now().UnixNano()),
		do:     do,
	}
}

// confirmView draws the request and asks for the key.
func confirmView(s Styles, p *pending, width int) string {
	var b strings.Builder
	b.WriteString(
		s.Header.Render(" "+strings.ToUpper(p.intent.Verb)+" ") + "  " +
			s.Accent.Render(p.intent.Target) + "\n",
	)
	b.WriteString(
		s.Dim.Render(
			p.intent.Kind+"  →  ",
		) + s.AccentAlt.Render(
			p.intent.Endpoint,
		) + "\n",
	)
	if !p.intent.Reversible {
		b.WriteString(s.Warn.Render("this one does not undo") + "\n")
	}
	b.WriteString("\n")
	// the request itself, highlighted if it is JSON. This is the part worth
	// the screen space: the verb and target are puffin's words about what
	// it is doing, and the wire is what it will do.
	wire := p.intent.Wire
	if strings.HasPrefix(strings.TrimSpace(wire), "{") {
		wire = HighlightJSON(wire, s)
	} else {
		wire = s.AccentAlt.Render(wire)
	}
	for _, line := range strings.Split(wire, "\n") {
		b.WriteString("  " + line + "\n")
	}
	b.WriteString("\n")
	b.WriteString(
		s.Caution.Render("press "+strings.ToUpper(p.key)+" to send") +
			s.Dim.Render("  ·  anything else cancels"),
	)
	return b.String()
}
