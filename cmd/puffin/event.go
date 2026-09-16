package main

import (
	"sync"
	"time"
)

// What happened, said once, to whoever is listening.
//
// Puffin's transitions used to call notify() directly, which worked while
// notification was the only thing that wanted to know. It is not any more: a
// sprite that emotes at the screen wants the same events, and so would a log
// file, a webhook, or a status line.
//
// A producer that names its consumer can only ever have one.
//
// So a transition emits an Event and the consumers subscribe. The notifier
// is now one of them rather than the point.
//
// Everything here is a transition, never a state. "An agent is waiting" is true
// every five seconds; "an agent has just started waiting" is true once.
//
// That rule was learned the expensive way with notifications -- a notifier that
// re-sends a condition is one people mute -- and it is the same rule for a
// bird: one that emotes on states is permanently agitated.

// Kind says what sort of thing happened, so a consumer can react without
// parsing prose.
type Kind int

const (
	// AgentWaiting: a session finished its turn and the next move is yours.
	AgentWaiting Kind = iota
	// AgentWorking: a session that was idle or waiting has started again.
	AgentWorking
	// WatchFired: something an operator asked to be told about appeared.
	WatchFired
	// Broke: something entered a bad state -- a pod, a deploy, a target.
	Broke
	// Recovered: it stopped being in that state.
	Recovered
	// Acted: puffin changed something and it worked.
	Acted
	// Quiet: nothing has happened for a while. Emitted by the idle timer,
	// so a consumer can wind down rather than having to guess.
	Quiet
)

// Event is one thing that happened.
type Event struct {
	Kind    Kind
	Subject string // what it happened to: a session, a pod, a service
	Detail  string // one line, for a human
	At      time.Time
	// Emote is what the companion should do about it, when the thing that
	// emitted the event has an opinion.
	//
	// Empty means no opinion and the corner falls back to what it does for
	// the Kind -- which is what every event did before watches could carry
	// one, so nothing that does not set this changed.
	Emote string
}

// listeners are the things that want to know.
var listeners = struct {
	mu sync.Mutex
	fs []func(Event)
}{}

// Listen subscribes. There is no unsubscribe: everything that listens here
// lives as long as the process, and an unsubscribe nobody calls is a method
// that only exists to be got wrong.
func Listen(f func(Event)) {
	listeners.mu.Lock()
	listeners.fs = append(listeners.fs, f)
	listeners.mu.Unlock()
}

// Emit tells everyone. Listeners are called on the caller's goroutine and
// must not block: this is called from the update loop, and a listener that
// waits on a network is a listener that freezes the interface.
func Emit(e Event) {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	listeners.mu.Lock()
	fs := make([]func(Event), len(listeners.fs))
	copy(fs, listeners.fs)
	listeners.mu.Unlock()
	for _, f := range fs {
		f(e)
	}
}

// listenNotify makes the notifier a consumer of events rather than the
// place they are sent to. Called once at start.
func listenNotify() {
	Listen(func(e Event) {
		switch e.Kind {
		case AgentWaiting:
			notify(
				"agent:"+e.Subject,
				e.Subject+" is waiting",
				e.Detail,
			)
		case WatchFired:
			notify("watch:"+e.Subject, e.Subject, e.Detail)
		case Broke:
			notify(
				"broke:"+e.Subject,
				e.Subject+" is broken",
				e.Detail,
			)
		}
	})
}
