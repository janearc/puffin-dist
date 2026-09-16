package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Notifications.
//
// The useful case is knowing an agent needs you without watching for it. Puffin
// is already reading transcripts every five seconds, so it knows when a session
// stops working and starts waiting -- and a notification is that knowledge
// arriving without a screen being open.
//
// osascript rather than a library. terminal-notifier is nicer (its own icon,
// click actions) and is not installed here, and a cgo binding to
// UNUserNotificationCenter needs a bundled .app, which a single binary on
// PATH is not. osascript ships with the OS and works today.
//
// Two rules keep this from becoming the thing you turn off.
//
// It is OPT-IN. A tool that starts sending desktop notifications because you
// opened it is a tool you close.
//
// And it notifies on transitions, never on states. "This agent is waiting"
// is true every five seconds; "this agent has just started waiting" is true
// once. A notifier that re-sends a condition is a notifier people mute, and
// a muted notifier is worse than none because it is also trusted.

// notifier sends desktop notifications, at most one per subject per window.
type notifier struct {
	mu    sync.Mutex
	on    bool
	last  map[string]time.Time
	every time.Duration
}

// notifyWindow is how long the same subject stays quiet after firing.
const notifyWindow = 10 * time.Minute

var notifications = &notifier{last: map[string]time.Time{}, every: notifyWindow}

// enableNotify turns them on. PUFFIN_NOTIFY is the way to have them on from
// a cold start; the pane can toggle within a session.
func enableNotify(on bool) {
	notifications.mu.Lock()
	defer notifications.mu.Unlock()
	notifications.on = on
}

// notifyEnabled is opt-in on purpose: a tool that starts sending desktop
// notifications because you opened it is a tool you close.
func notifyEnabled() bool {
	notifications.mu.Lock()
	defer notifications.mu.Unlock()
	return notifications.on
}

// notify posts one notification. The subject is what gets rate-limited, so
// two different agents going quiet both get through and one agent going
// quiet twice in a minute does not.
func notify(subject, title, body string) {
	notifications.mu.Lock()
	if !notifications.on {
		notifications.mu.Unlock()
		return
	}
	if at, ok := notifications.last[subject]; ok &&
		time.Since(at) < notifications.every {
		notifications.mu.Unlock()
		return
	}
	notifications.mu.Unlock()
	// and once across processes. This tool is good enough to run in several
	// windows at once -- a daemon watching /OOM/ and a tui in tmux -- and
	// two instances matching the same line both fire.
	//
	// Two notifications for one event is how a notifier gets muted, and a
	// muted notifier is worse than none because it is still half-trusted.
	//
	// A shared file rather than a lock or a leader: they do not need to
	// coordinate, they need to agree on what has already been said. Any
	// instance can be first and the rest fall silent, whichever one it is.
	if !claimNotification(subject) {
		return
	}
	notifications.mu.Lock()
	notifications.last[subject] = time.Now()
	notifications.mu.Unlock()
	go post(title, body)
}

// post hands the text to the terminal first, and to the OS only if the
// terminal cannot do it.
//
// Asking the terminal is the right route for a TUI and the first cut got it
// wrong. osascript posts as Script Editor, which is a different app with its
// own notification permission -- one that is off by default on a machine that
// has never run a script that asked.
//
// It exits 0 either way, so the failure is silent, which is exactly what
// happened here: the command succeeded and no notification existed.
//
// A terminal that supports OSC 777 posts under its OWN identity, which is
// already permitted because it is an app you use. It also works over ssh,
// which osascript does not, and needs no permission dance.
//
// Fire and forget either way: a notification that fails is not worth an
// error on a screen, and a notifier that can block the update loop is a
// worse bug than a missing notification.
func post(title, body string) {
	// terminal-notifier first when it is installed, and the reason is
	// filtering rather than looks.
	//
	// The OSC route posts under the terminal's identity, so every
	// notification puffin sends is indistinguishable from every other thing
	// Ghostty says -- and macOS Focus filters by app, which means you
	// cannot let puffin through without letting the whole terminal through.
	//
	// terminal-notifier is a signed bundle with its own identifier, so it
	// gets its own line in Notification Settings and its own Focus
	// allowance. It is the only route here that can be filtered on its own.
	//
	// It does not work over ssh, which the OSC route does -- so the order
	// is: a filterable local notifier, then the terminal, then osascript.
	if notifierBinary != "" && notifierNotify(title, body) {
		return
	}
	if termNotify(title, body) {
		return
	}
	if runtime.GOOS != "darwin" {
		return
	}
	script := "display notification " + osaQuote(body) +
		" with title " + osaQuote("puffin") +
		" subtitle " + osaQuote(title)
	cmd := exec.Command("osascript", "-e", script)
	cmd.Stdout, cmd.Stderr = nil, nil
	_ = cmd.Run()
}

// termNotify writes the OSC 777 desktop-notification sequence, which ghostty,
// kitty, wezterm and rxvt-unicode all understand.
//
// It is written to the terminal rather than rendered: an OSC is a command, not
// text, so it paints no cells and moves no cursor -- safe to emit from inside a
// full screen application between frames.
func termNotify(title, body string) bool {
	if !termCanNotify() {
		return false
	}
	// the payload is semicolon-delimited, so semicolons and control bytes
	// are stripped rather than escaped: there is no escape in this protocol
	clean := func(s string) string {
		s = strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f || r == ';' {
				return ' '
			}
			return r
		}, s)
		if len(s) > 180 {
			s = s[:179] + "…"
		}
		return s
	}
	seq := "\x1b]777;notify;" + clean(
		"puffin · "+title,
	) + ";" + clean(
		body,
	) + "\x07"
	_, err := os.Stdout.WriteString(wrapForTmux(seq))
	return err == nil
}

// wrapForTmux puts an escape sequence inside tmux's passthrough so it
// reaches the terminal outside tmux rather than being eaten by it.
//
// This matters for the case the watch command exists for. tmux does not forward
// OSC 777 on its own -- it is not a sequence tmux understands, and an
// unrecognised OSC dies in the pane.
//
// So `puffin watch` running in a tmux pane, which is exactly where it is meant
// to run, would notify nobody and report no error while doing it.
//
// The wrapper is DCS tmux; <payload> ST, with every ESC in the payload doubled,
// which is how tmux knows where the payload ends.
//
// It also requires allow-passthrough to be on in tmux; when it is off this is
// inert rather than harmful -- the sequence is swallowed either way, and it was
// already being swallowed.
func wrapForTmux(seq string) string {
	if os.Getenv("TMUX") == "" {
		return seq
	}
	return "\x1bPtmux;" + strings.ReplaceAll(
		seq,
		"\x1b",
		"\x1b\x1b",
	) + "\x1b\\"
}

// termCanNotify says whether this terminal understands OSC 777. Detected rather
// than assumed, and overridable: a terminal that does not understand it prints
// nothing and swallows the sequence, so a wrong guess is quiet rather than
// garbled
//
// -- but a wrong guess also means no notification, so the osascript fallback
// needs to know.
func termCanNotify() bool {
	if v := os.Getenv("PUFFIN_TERM_NOTIFY"); v != "" {
		return v != "0"
	}
	// stdout must actually BE a terminal.
	//
	// TERM_PROGRAM is inherited by every child process, including ones
	// whose output is a pipe, so a shell running under a terminal is not
	// the same thing as a terminal -- and writing an OSC to a pipe does not
	// notify anybody, it prints ]777;notify;...
	//
	// as text where the answer should have been.
	//
	// That is exactly what happened when this was tested from a non-tty
	// shell.
	if !stdoutIsTerminal() {
		return false
	}
	switch os.Getenv("TERM_PROGRAM") {
	case "ghostty", "WezTerm", "iTerm.app":
		return true
	}
	term := os.Getenv("TERM")
	return strings.Contains(term, "ghostty") ||
		strings.Contains(term, "kitty") ||
		strings.Contains(term, "wezterm")
}

// osaQuote makes a Go string safe inside an AppleScript literal. Backslash
// first, then quote -- the other order escapes the backslashes it just
// added. Text here comes from transcripts and pod names, which is to say
// from outside, and it is being handed to an interpreter.
func osaQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 200 {
		s = s[:199] + "…"
	}
	return `"` + s + `"`
}

// notifyFromEnv reads the opening setting.
func notifyFromEnv() bool { return os.Getenv("PUFFIN_NOTIFY") != "" }

// stdoutIsTerminal says whether the escape has anywhere to go. A character
// device is a terminal; a pipe or a file is not.
func stdoutIsTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// notifierBinary is terminal-notifier's path, found once at start.
var notifierBinary = findNotifier()

// findNotifier looks for terminal-notifier once at start, because a
// LookPath per notification is a syscall per event for an answer that
// does not change while the process runs.
func findNotifier() string {
	if os.Getenv("PUFFIN_TERM_NOTIFY") == "1" {
		return "" // the terminal route was asked for explicitly
	}
	p, err := exec.LookPath("terminal-notifier")
	if err != nil {
		return ""
	}
	return p
}

// notifierNotify posts through terminal-notifier. -group means a second
// notification about the same subject replaces the first rather than
// stacking: a watch that fires twice should leave one notification saying
// the newer thing, not two saying both.
func notifierNotify(title, body string) bool {
	cmd := exec.Command(notifierBinary,
		"-title", "puffin",
		"-subtitle", title,
		"-message", body,
		"-group", "puffin",
		"-sender", "com.mitchellh.ghostty")
	cmd.Stdout, cmd.Stderr = nil, nil
	return cmd.Run() == nil
}

// notifyRoute names how a notification will be sent, for `puffin notify` to
// report. "Nothing appeared" has several causes and they are fixed in
// different places, so the tool says which one it is using.
func notifyRoute() string {
	switch {
	case notifierBinary != "":
		return "terminal-notifier (" + notifierBinary + ") -- its " +
			"own app identity, so it can be filtered on its own"
	case termCanNotify():
		return "the terminal, via OSC 777 (" + os.Getenv(
			"TERM_PROGRAM",
		) +
			") -- arrives AS the terminal, so Focus cannot tell " +
			"it from anything else in a terminal"
	case !stdoutIsTerminal():
		return "osascript, as Script Editor (stdout is not a " +
			"terminal here, so the OSC route is unavailable)"
	}
	return "osascript, as Script Editor -- a different app, with its own " +
		"notification permission"
}

// claimNotification says whether THIS process should send it, or whether
// another puffin already did within the window.
//
// The file is a small table of subject -> unix time, rewritten on each
// claim. It is best-effort by design: if it cannot be read or written the
// answer is yes, because a missed notification is worse than a duplicated
// one and neither is worth an error on a screen.
func claimNotification(subject string) bool {
	return claimWrite("notify:"+subject, notifyWindow)
}

// claimWrite is the general form: has anything on this machine already done
// this, under this name, inside this window? Used for notifications and for
// the one estate write that is not idempotent -- typing into an agent's
// prompt.
//
// Best-effort by design. If the store cannot be read or written the answer is
// YES: a missed notification is worse than a duplicate, and a refused message
// is worse than a repeated one.
//
// Failing open is the right direction here precisely because the thing being
// prevented is a nuisance rather than a hazard.
func claimWrite(subject string, window time.Duration) bool {
	path := filepath.Join(themeCacheDir(), "claims.json")
	now := time.Now()
	seen := map[string]int64{}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &seen)
	}
	if at, ok := seen[subject]; ok && now.Sub(time.Unix(at, 0)) < window {
		return false
	}
	seen[subject] = now.Unix()
	// entries older than the window are dropped: a laptop left running for
	// a week should not accumulate a subject per event forever
	for k, at := range seen {
		if now.Sub(time.Unix(at, 0)) > notifyWindow*4 {
			delete(seen, k)
		}
	}
	if b, err := json.Marshal(seen); err == nil {
		if os.MkdirAll(filepath.Dir(path), 0o755) == nil {
			tmp := path + ".tmp"
			if os.WriteFile(tmp, b, 0o644) == nil {
				_ = os.Rename(tmp, path)
			}
		}
	}
	return true
}
