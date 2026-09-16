package main

import (
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// tmux, both directions.
//
// A tmux session can be read from and written to, which is what makes
// saying something back to a running agent possible at all.
//
// This is the other half of the agents pane. The transcript says what a session
// HAS DONE; tmux is where it is running and the only way to say something back
// to it.
//
// It also turns "kill these when they are unruly" into something far better
// than a signal: an agent that is mid-thought answers Escape, finishes cleanly,
// and keeps its context. kill -9 is what you reach for when you have no other
// channel, and this is that other channel.
//
// The join between the two worlds is the working directory. A Claude transcript
// records the cwd of the session that wrote it, and a tmux pane reports the cwd
// of what is running in it. Matching them is how a row on the agents pane
// learns which pane to talk to.
//
// It is not a perfect key -- two panes in one directory are indistinguishable
// -- so the pane says which target it picked rather than acting on an
// assumption silently.

// TmuxPane is one pane, addressed the way tmux addresses it.
type TmuxPane struct {
	Target  string // session:window.pane -- what -t takes
	Session string
	Path    string // the pane's working directory: the join key
	Command string // what is running in it right now
	Title   string
	Active  bool
}

// tmuxFormat asks tmux for exactly the fields above, one pane per line.
//
// TAB is the separator, and the choice matters. The obvious pick is the ASCII
// unit separator, 0x1f, which is what it exists for -- and tmux escapes it on
// the way out, so the byte arrives as the four characters \037 and every line
// parses as one field.
//
// It fails silently: no error, no server complaint, just an empty pane list
// that reads exactly like a machine with no tmux running. Tab passes through
// untouched, and neither a path nor a command can contain one.
//
// The pane title is last for the same reason. It is the one field a human
// can put anything in, so if a tab ever does turn up in one it runs off the
// end of the line rather than shifting every column after it.
const tmuxSep = "\t"
const tmuxFormat = "#{session_name}:#{window_index}.#{pane_index}" + tmuxSep +
	"#{session_name}" + tmuxSep + "#{pane_current_path}" + tmuxSep +
	"#{pane_current_command}" + tmuxSep + "#{pane_active}" + tmuxSep +
	"#{pane_title}"

// tmuxPanes lists every pane on the server. No server is a normal state and
// not an error: puffin runs fine outside tmux, and saying "tmux is broken"
// when tmux is merely not running is the kind of false alarm that teaches
// people to ignore a screen.
func tmuxPanes() ([]TmuxPane, error) {
	cmd := exec.Command("tmux", "list-panes", "-a", "-F", tmuxFormat)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	out, err := cmd.Output()
	if err != nil {
		// tmux exits 1 for everything, so the exit code says nothing
		// and its stderr says everything.
		//
		// Swallowing every exit-1 as "no server" hides a socket puffin
		// cannot reach behind a message that reads like an empty desk
		// -- which is how a real failure gets mistaken for an idle
		// machine.
		msg := strings.TrimSpace(errb.String())
		switch {
		case msg == "":
			return nil, err
		case strings.Contains(msg, "no server running"),
			strings.Contains(msg, "no current session"):
			// genuinely nothing running: a normal state
			return nil, nil
		default:
			return nil, fmt.Errorf("%s", msg)
		}
	}
	var panes []TmuxPane
	for _, line := range strings.Split(
		strings.TrimSpace(string(out)),
		"\n",
	) {
		// SplitN, so a tab inside a pane title lands in the title
		// rather than shifting the fields that come after it
		f := strings.SplitN(line, tmuxSep, 6)
		if len(f) < 6 {
			continue
		}
		panes = append(panes, TmuxPane{
			Target: f[0], Session: f[1], Path: f[2],
			Command: f[3], Active: f[4] == "1", Title: f[5],
		})
	}
	return panes, nil
}

// tmuxByPath indexes panes by working directory. When two panes share one,
// the active one wins and the count is kept, so the screen can say that the
// address it picked was a choice rather than the only answer.
func tmuxByPath(panes []TmuxPane) (map[string]TmuxPane, map[string]int) {
	best := map[string]TmuxPane{}
	n := map[string]int{}
	for _, p := range panes {
		n[p.Path]++
		if cur, ok := best[p.Path]; !ok || (p.Active && !cur.Active) {
			best[p.Path] = p
		}
	}
	return best, n
}

// tmuxSend types a line into a pane and presses enter.
//
// The text goes with -l, literally, and the newline is a separate call.
//
// Send text and keys together and tmux reads words like "Enter", "Escape" and
// "C-c" inside your sentence as keystrokes -- so "press Enter when the build is
// done" would submit itself partway through, which is a very silly way to find
// out about an escaping bug.
func tmuxSend(target, text string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("nothing to send")
	}
	// Typing into somebody's prompt is the one write puffin makes that is
	// NOT idempotent. Everything else is: kubectl serializes the cluster
	// writes and stopping a stopped workload is refused; a flag flip is
	// last-writer-wins on a value, which is what a flag means.
	//
	// But two puffins with the same session selected, both sending "please
	// finish the build", puts the sentence in twice -- and the agent reads
	// it as two instructions from someone who repeats themselves.
	//
	// So the send is claimed first, keyed by target AND text, with a short
	// window. The same message twice inside a few seconds is one message;
	// the same message a minute later is two, because by then you meant it.
	if !claimWrite("tmux:"+target+":"+text, tmuxSendWindow) {
		return fmt.Errorf(
			"an identical message went to %s a moment ago",
			target,
		)
	}
	if err := exec.Command(
		"tmux",
		"send-keys",
		"-t",
		target,
		"-l",
		"--",
		text,
	).Run(); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	return exec.Command("tmux", "send-keys", "-t", target, "Enter").Run()
}

// tmuxKey sends one named key: Escape to stop an agent mid-thought, C-c for
// the harder interrupt. Named keys are exactly what -l exists to prevent, so
// this is the deliberate other path and takes a key name, never prose.
func tmuxKey(target, key string) error {
	// an interrupt is idempotent in effect for a local agent -- a second
	// Escape to one that already stopped does nothing -- but that stops
	// being true the moment the pane is holding something remote.
	//
	// C-c interrupts the foreground process, and if the first one dropped a
	// client out of a request and back to a shell, the second lands on the
	// shell. Against ssh it can close the connection; against a repl or a
	// kubectl exec it kills the session rather than the command.
	//
	// So it is claimed on the same terms as a message: the same key to the
	// same pane twice inside a few seconds is one keystroke.
	if !claimWrite("tmuxkey:"+target+":"+key, tmuxSendWindow) {
		return fmt.Errorf(
			"%s already went to %s a moment ago",
			key,
			target,
		)
	}
	return exec.Command("tmux", "send-keys", "-t", target, key).Run()
}

// tmuxSendWindow is how long an identical message is considered the same
// message. Short: long enough to cover two puffins reacting to one
// keystroke, short enough that saying the same thing twice on purpose works.
const tmuxSendWindow = 5 * time.Second

// tmuxCapture reads the last n lines a pane has on screen -- the other
// direction, and the one that answers "what is it actually showing".
func tmuxCapture(target string, n int) ([]string, error) {
	out, err := exec.Command("tmux", "capture-pane", "-p", "-t", target,
		"-S", fmt.Sprintf("-%d", n)).Output()
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	// capture-pane pads to the pane height; trailing blanks are not content
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

// The kill ladder.
//
// Graceful, less graceful, and -9.
//
// Four rungs, and the first two are not kills at all, which is the point of
// having a ladder rather than a button:
//
//	Escape   stop generating. The agent finishes cleanly and keeps its
//	         context. Nothing dies. This is what you want almost always.
//	C-c      interrupt the foreground process. The harness survives; the
//	         request in flight does not.
//	TERM     signal the process. It can clean up, flush, and exit. The
//	         transcript is closed properly and a resume will work.
//	KILL     -9. Nothing runs, nothing flushes, nothing is written. The last
//	         thing the transcript will ever say is whatever had already
//	         reached the disk.
//
// The signals go to the pane's children, not to the pane's own process. A
// pane's pid is its shell; killing that takes the pane with it and you lose the
// scrollback, the working directory and the ability to just start another agent
// in the same place. What you want dead is the thing the shell started.
func paneChildren(target string) ([]int, error) {
	out, err := exec.Command("tmux", "display-message", "-p", "-t", target,
		"#{pane_pid}").Output()
	if err != nil {
		return nil, fmt.Errorf("no pane %s: %w", target, err)
	}
	shell, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return nil, fmt.Errorf(
			"tmux gave a pane pid puffin cannot read: %q",
			out,
		)
	}
	ps, err := exec.Command("ps", "-o", "pid=,ppid=").Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	var kids []int
	for _, line := range strings.Split(string(ps), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])
		if e1 != nil || e2 != nil || ppid != shell {
			continue
		}
		kids = append(kids, pid)
	}
	if len(kids) == 0 {
		return nil, fmt.Errorf(
			"nothing is running in %s -- only its shell",
			target,
		)
	}
	return kids, nil
}

// signalCommand is the command that will be run, built so the confirmation
// can SHOW it. Nothing here runs anything: the string the operator reads and
// the thing that executes are built from one call, which is the property
// that makes a confirmation worth reading.
func signalCommand(sig string, pids []int) string {
	parts := make([]string, 0, len(pids)+2)
	parts = append(parts, "kill", "-"+sig)
	for _, p := range pids {
		parts = append(parts, itoa(p))
	}
	return strings.Join(parts, " ")
}

// signalPane sends one signal to whatever a pane is running.
func signalPane(target, sig string) error {
	kids, err := paneChildren(target)
	if err != nil {
		return err
	}
	args := []string{"-" + sig}
	for _, p := range kids {
		args = append(args, itoa(p))
	}
	if out, err := exec.Command(
		"kill",
		args...,
	).CombinedOutput(); err != nil {
		return fmt.Errorf(
			"kill -%s: %s",
			sig,
			strings.TrimSpace(string(out)),
		)
	}
	return nil
}
