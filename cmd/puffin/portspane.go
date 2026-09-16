package main

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// portsPane is the audit on screen, and the place to end what should not be
// running.
//
// Editing is deliberately asymmetric. Stopping a forward is one key and a
// confirmation, because that is the operation the audit exists to enable -- you
// look at the list to find what should not be there.
//
// Starting one asks for the service by name, and says out loud that a forward
// is host state that no repository knows about, because the dev network's rule
// is that nothing exists which was not created by code.
type portsPane struct {
	v       PortView
	cursor  int
	pending *pending
	note    string
	// typing a new forward: "svc/flipr 8080:80", or "flipr 8080" and let
	// the defaults do the rest
	typing bool
	entry  string
}

// Key opens the ports pane from the roster.
func (p *portsPane) Key() string { return "P" }

// Title names the pane, and keys its refresh gate.
func (p *portsPane) Title() string { return "ports" }

// Tick is how often the list re-reads, which is often enough that a
// forward dying leaves the screen on its own.
func (p *portsPane) Tick() time.Duration {
	// a forward that died should leave the list on its own, and the whole
	// read is three cheap commands
	return 15 * time.Second
}

// Capturing says the composer owns the keyboard while a forward is being
// typed, so q does not close the pane mid-word.
func (p *portsPane) Capturing() bool { return p.typing }

// Help is the footer: how a forward is typed, and how one is stopped.
func (p *portsPane) Help() string {
	return "j/k: move · n: new forward · x: stop one · r: re-read\n" +
		"forwards are host processes: nothing in the cluster knows " +
		"they exist, and they die with the terminal that " +
		"started them"
}

type portsFetched struct{ v PortView }

// Load reads all four places a port reaches the host, because only one of
// them is written down anywhere.
func (p *portsPane) Load(domain string) tea.Cmd {
	ctx := kubeContext()
	return func() tea.Msg { return portsFetched{v: fetchPorts(ctx)} }
}

// Update folds in the four sources, and holds the composer while a
// forward is being typed.
func (p *portsPane) Update(msg tea.Msg) (pane, tea.Cmd) {
	switch msg := msg.(type) {
	case portsFetched:
		p.v = msg.v
		if p.cursor >= len(p.v.Forwards) {
			p.cursor = maxInt(0, len(p.v.Forwards)-1)
		}
		return p, nil
	case portsActed:
		p.note = msg.note
		return p, p.Load("")
	case tea.KeyMsg:
		if p.pending != nil {
			pend := p.pending
			p.pending = nil
			if msg.String() == pend.key {
				return p, pend.do()
			}
			p.note = "cancelled"
			return p, nil
		}
		if p.typing {
			switch msg.String() {
			case "esc":
				p.typing, p.entry = false, ""
				return p, nil
			case "enter":
				p.typing = false
				return p.armStart(p.entry)
			case "backspace":
				if p.entry != "" {
					p.entry = p.entry[:len(p.entry)-1]
				}
				return p, nil
			}
			if r := msg.String(); len([]rune(r)) == 1 {
				p.entry += r
			}
			return p, nil
		}
		switch msg.String() {
		case "n":
			p.typing, p.entry, p.note = true, "", ""
			return p, nil
		case "j", "down":
			if p.cursor < len(p.v.Forwards)-1 {
				p.cursor++
			}
		case "k", "up":
			if p.cursor > 0 {
				p.cursor--
			}
		case "x":
			return p.armStop()
		}
	}
	return p, nil
}

// armStop confirms ending one forward, showing the process it will signal.
func (p *portsPane) armStop() (pane, tea.Cmd) {
	if p.cursor >= len(p.v.Forwards) {
		p.note = "no forwards to stop"
		return p, nil
	}
	f := p.v.Forwards[p.cursor]
	p.pending = arm(Intent{
		Verb:     "stop forward",
		Target:   f.Remote + " on :" + f.Local,
		Wire:     "kill -TERM " + itoa(f.PID) + "\n# " + f.Raw,
		Kind:     "signal",
		Endpoint: "this host",
		// it can be started again, and the audit will show it gone
		Reversible: true,
	}, func() tea.Cmd {
		return func() tea.Msg {
			out, err := exec.Command("kill", "-TERM", itoa(f.PID)).
				CombinedOutput()
			if err != nil {
				return portsActed{
					note: "kill " + itoa(f.PID) + ": " +
						strings.TrimSpace(string(out)),
				}
			}
			return portsActed{
				note: fmt.Sprintf(
					"stopped %d (%s)",
					f.PID,
					f.Remote,
				),
			}
		}
	})
	return p, nil
}

type portsActed struct{ note string }

// View draws the ad-hoc forwards beside the declared ones, so the
// difference between them is visible rather than inferred.
func (p *portsPane) View(s Styles, width, height int) string {
	var b strings.Builder
	for _, w := range p.v.Warnings {
		b.WriteString(s.Caution.Render("! "+w) + "\n")
	}
	// who this audit is about, before what it found.
	//
	// A forward is a process on ONE machine and the list is only true for
	// the machine puffin is running on -- reading "none" without knowing
	// whose "none" is how you conclude the estate is clean while somebody
	// else's laptop holds a tunnel open into the cluster.
	b.WriteString(s.Accent.Render(p.v.Host.Host) +
		s.Dim.Render(" · "+p.v.Host.User) +
		s.Dim.Render(
			"  ·  cluster ",
		) + s.AccentAlt.Render(orDash(p.v.Context)))
	if p.v.Host.Uptime != "" {
		b.WriteString(s.Dim.Render("  ·  " + p.v.Host.Uptime))
	}
	b.WriteString(
		"\n" + s.Dim.Render(
			"forwards below are processes on THIS machine only",
		) + "\n\n",
	)

	// the ad-hoc ones first: they are the reason this screen exists
	b.WriteString(s.Header.Render("port-forwards on "+p.v.Host.Host) + "\n")
	if len(p.v.Forwards) == 0 {
		b.WriteString(
			s.Ok.Render(
				"  none · nothing is standing in for a name",
			) + "\n",
		)
	}
	for i, f := range p.v.Forwards {
		on := i == p.cursor
		marker := s.On(s.Row, on).Render("  ")
		if on {
			marker = s.On(s.RowSel, on).Render("▸ ")
		}
		b.WriteString(marker +
			s.On(s.Accent, on).Render(pad(":"+f.Local, 8)) +
			s.On(s.Row, on).Render(pad(f.Remote, 34)) +
			s.On(s.Dim, on).
				Render(pad(orDash(f.NS), 14)+pad("pid "+
					itoa(f.PID), 12)) +
			"\n")
	}

	b.WriteString(
		"\n" + s.Header.Render(
			"services that reach past the cluster",
		) + "\n",
	)
	if len(p.v.Services) == 0 {
		b.WriteString(
			s.Dim.Render(
				"  none · everything is ClusterIP, reachable "+
					"by name",
			) + "\n",
		)
	}
	for _, sv := range p.v.Services {
		b.WriteString("  " + s.Accent.Render(pad(sv.Name, 24)) +
			s.Dim.Render(
				pad(
					sv.NS,
					14,
				)+pad(
					sv.Type,
					14,
				)+strings.Join(
					sv.Ports,
					" ",
				),
			) + "\n")
	}

	b.WriteString(
		"\n" + s.Header.Render(
			"published by k3d when the cluster was made",
		) + "\n",
	)
	for _, m := range p.v.Mappings {
		b.WriteString(
			"  " + s.Dim.Render(
				pad(m.Container, 24)+m.Published,
			) + "\n",
		)
	}

	switch {
	case p.typing:
		b.WriteString("\n" + s.Accent.Render("forward: ") +
			s.Row.Render(p.entry+"\u2588") + "\n")
		b.WriteString(
			helpLine(s, "enter: confirm · esc: cancel") + "\n",
		)
		b.WriteString(
			s.Dim.Render(
				`  "flipr 8080"  ·  "svc/flipr 8080:80"  ·  "`+
					`kafka/schema-registry 8081"`,
			) + "\n",
		)
	case p.pending != nil:
		b.WriteString("\n" + confirmView(s, p.pending, width) + "\n")
	case p.note != "":
		b.WriteString("\n" + s.Ok.Render(p.note) + "\n")
	}
	return b.String()
}

// parseForwardEntry reads what was typed into the pieces kubectl wants.
//
// Deliberately forgiving about everything except the port, because the port
// is the only part that cannot be guessed:
//
//	flipr 8080          -> svc/flipr, 8080 both sides, default namespace
//	svc/flipr 8080:80   -> exactly that
//	kafka/flipr 8080:80 -> namespace kafka
//
// A bare name becomes a service and not a pod: a pod name is a moving target
// that will be wrong by tomorrow, and forwarding to one is how you end up
// debugging a pod that was replaced an hour ago.
func parseForwardEntry(
	entry, defaultNS string,
) (ns, target, local, remote string, err error) {
	f := strings.Fields(strings.TrimSpace(entry))
	if len(f) != 2 {
		return "", "", "", "", fmt.Errorf(
			`say it as "<service> <local>[:<remote>]"`,
		)
	}
	name, ports := f[0], f[1]
	ns = defaultNS
	if i := strings.Index(name, "/"); i > 0 &&
		!strings.HasPrefix(name, "svc/") &&
		!strings.HasPrefix(name, "pod/") &&
		!strings.HasPrefix(name, "deployment/") {
		ns, name = name[:i], name[i+1:]
	}
	if !strings.Contains(name, "/") {
		name = "svc/" + name
	}
	local, remote, _ = strings.Cut(ports, ":")
	if remote == "" {
		remote = local
	}
	for _, port := range []string{local, remote} {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 {
			return "", "", "", "", fmt.Errorf(
				"%q is not a port",
				port,
			)
		}
	}
	return ns, name, local, remote, nil
}

// armStart confirms a new forward, showing the exact kubectl.
func (p *portsPane) armStart(entry string) (pane, tea.Cmd) {
	ns, target, local, remote, err := parseForwardEntry(
		entry,
		defaultNamespace(),
	)
	if err != nil {
		p.note = err.Error()
		return p, nil
	}
	ctx := p.v.Context
	cmdline := forwardCommand(ctx, ns, target, local, remote)
	p.pending = arm(Intent{
		Verb:     "start forward",
		Target:   target + " on :" + local,
		Wire:     cmdline,
		Kind:     "kubectl",
		Endpoint: ctx,
		// it can be stopped from this screen, and it dies with puffin
		Reversible: true,
	}, func() tea.Cmd {
		return func() tea.Msg {
			c := exec.Command("kubectl", "--context", ctx, "-n", ns,
				"port-forward", target, local+":"+remote)
			if err := c.Start(); err != nil {
				return portsActed{
					note: "could not start: " + err.Error(),
				}
			}
			// reaped rather than left as a zombie when it exits on
			// its own
			go func() { _ = c.Wait() }()
			return portsActed{note: fmt.Sprintf(
				"forwarding %s to :%s (pid %d) · it dies "+
					"when puffin does",
				target,
				local,
				c.Process.Pid,
			)}
		}
	})
	return p, nil
}
