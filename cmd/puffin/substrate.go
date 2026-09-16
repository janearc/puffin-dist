package main

import (
	"bufio"
	"fmt"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Starting the substrate.
//
// After a reboot the substrate is down and every pane is empty. Bringing
// it back is the first thing anybody does, so it is on the screen, and it
// takes two keystrokes: one to say you want to, one to say you mean it.
//
// Two things this deliberately does NOT do.
//
// It never stops the virtual machine. One profile hosts every k3d cluster
// on the machine, so there is no stop that affects only the development
// one -- every one of them takes production down with it. A verb that
// cannot be offered honestly is not offered.
//
// And it never starts the production cluster. Starting a development cluster is
// development work; starting production is a production action and needs an
// approval puffin does not have.
//
// The restart policy makes this mostly moot -- both k3d nodes run
// unless-stopped, so docker brings them back on its own once colima is up --
// but a button that exists can be pressed, and this one refuses.

// substrateOut is one line of output from a long-running start.
type substrateOut struct {
	line string
	done bool
	err  error
}

// startColima runs `colima start` with the profile named explicitly, always,
// and streams its output back a line at a time so the screen shows progress
// rather than freezing for two minutes on a spinner.
func startColima(profile string) (chan substrateOut, error) {
	if profile == "" {
		return nil, fmt.Errorf(
			"no profile named: puffin will not run a colima " +
				"command without one",
		)
	}
	return streamCmd("colima", "start", "--profile", profile)
}

// startCluster brings a k3d cluster up. It refuses anything but the dev one.
func startCluster(name string) (chan substrateOut, error) {
	if "k3d-"+name != homeContext() {
		return nil, fmt.Errorf(
			"refusing to start %q: puffin only starts %s, and "+
				"starting another cluster is a decision this "+
				"tool does not get to make",
			name,
			strings.TrimPrefix(homeContext(), "k3d-"),
		)
	}
	return streamCmd("k3d", "cluster", "start", name)
}

// streamCmd runs a command and delivers its output line by line. Both
// streams are read, because the interesting half of a failing start is
// usually on stderr and a screen that shows only stdout reports a silent
// failure.
func streamCmd(name string, args ...string) (chan substrateOut, error) {
	cmd := exec.Command(name, args...)
	// stderr is folded into stdout rather than read separately: the
	// interesting half of a failing start is usually on stderr, and colima
	// writes its progress there, so a screen reading only stdout would show
	// a two-minute silence and then a result.
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = cmd.Stdout
	ch := make(chan substrateOut, 64)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 8192), 1<<20)
		for sc.Scan() {
			ch <- substrateOut{line: strings.TrimRight(
				sc.Text(),
				"\r",
			)}
		}
		err := cmd.Wait()
		ch <- substrateOut{done: true, err: err}
		close(ch)
	}()
	return ch, nil
}

// nextLine waits for one more line. Re-issued on every receipt, which is how
// a channel becomes a stream of messages in this loop.
func nextLine(ch chan substrateOut) tea.Cmd {
	return func() tea.Msg {
		out, ok := <-ch
		if !ok {
			return substrateOut{done: true}
		}
		return out
	}
}

// armed is a two-keystroke gate: the first press says what will happen,
// the second means it. It is not the same as a y/n prompt -- the same key
// twice keeps your hand where it was, and the armed state names the thing
// on screen while you decide.
type armed struct {
	verb   string // what the second press will do
	target string
	at     time.Time
}

// armWindow is how long an armed action stays armed. A gate that never
// expires is a keystroke waiting to be pressed by accident an hour later.
const armWindow = 10 * time.Second

// live is whether an arm is still good. It expires so a key pressed a
// minute ago cannot be completed by an unrelated second press.
func (a *armed) live() bool { return a != nil && time.Since(a.at) < armWindow }
