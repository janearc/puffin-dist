package main

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The command line, for the times the answer is one fact and opening a
// screen to read it is absurd. JSON is miserable to handle at a shell
// prompt, so none of it goes near one.
//
// So: subcommands, plain words, no JSON anywhere near the shell. puffin
// builds the protojson itself, resolves the newest deploy namespace itself
// (nobody types commit hashes), and prints text a pipe can eat. No
// arguments still opens the TUI.
//
//	puffin status                     the enclave roster
//	puffin truth                      hm's citizenship table
//	puffin flags [service]            every flag, or one service's
//	puffin get <service> <key>        one value
//	puffin set <service> <key> <value> --reason "why"
//	puffin logs <pod> [-n ns] [--tail N] [-p]
//	puffin start|stop|restart <name> [-n ns]
//
// set infers the value's type from the flag it is flipping -- the declared
// kind wins, so a string flag spelling "true" stays a string.

// runCLI dispatches a subcommand and returns the process exit code.
func runCLI(args []string) int {
	domain := os.Getenv("PUFFIN_DOMAIN")
	if domain == "" {
		domain = "test"
	}
	// global flags are stripped before dispatch, so they work in front of
	// any subcommand and in front of none. --context with nothing after it
	// opens the TUI against that cluster, which is the common case.
	args, tui, code := takeGlobals(args)
	if code >= 0 {
		return code
	}
	if tui {
		runTUI(domain)
		return 0
	}
	switch args[0] {
	case "selftest", "--selftest":
		return cliSelftest(args[1:])
	case "agents", "--list-agents":
		return cliAgents()
	case "pet":
		return cliPet(args[1:])
	case "notify", "--notify":
		// a way to test notifications without waiting for an agent to
		// go quiet.
		//
		// It reports which route it used, because "nothing appeared"
		// has two very different causes -- a terminal that ignored the
		// escape, or an osascript whose notification is disabled for
		// Script Editor -- and they are fixed in different places.
		msg := "puffin can talk"
		if len(args) > 1 {
			msg = strings.Join(args[1:], " ")
		}
		enableNotify(true)
		fmt.Println("sending through " + notifyRoute())
		post("notification test", msg)
		fmt.Println()
		fmt.Println("if nothing appeared:")
		fmt.Println(
			"  PUFFIN_TERM_NOTIFY=1 puffin notify   forces the " +
				"terminal route",
		)
		fmt.Println(
			"  PUFFIN_TERM_NOTIFY=0 puffin notify   forces " +
				"osascript",
		)
		if notifierBinary == "" {
			fmt.Println()
			fmt.Println(
				"to filter puffin's notifications separately " +
					"from everything else in a terminal:",
			)
			fmt.Println("  brew install terminal-notifier")
			fmt.Println(
				"puffin will then post under its own app " +
					"identity, which macOS Focus can " +
					"allow or",
			)
			fmt.Println(
				"silence on its own. Without it, every " +
					"notification arrives as your " +
					"terminal.",
			)
		}
		return 0
	case "watch":
		return cliWatch(domain, args[1:])
	case "diff":
		return cliDiff(args[1:])
	case "repos":
		return cliRepos(args[1:])
	case "--age", "age":
		// "how old is this binary" is the question you ask when a fix
		// you were told about is not in front of you.
		//
		// Two ages, because they answer different halves: the commit's
		// age says how old the source is, and the FILE's age says when
		// this copy was installed. They disagree when a build did not
		// land, which is the case worth catching.
		b := thisBuild()
		if !b.Time.IsZero() {
			fmt.Printf(
				"commit    %s, %s\n",
				b.Short(),
				humanSince(time.Since(b.Time)),
			)
		} else {
			fmt.Println("commit    unstamped")
		}
		if b.Subject != "" {
			fmt.Printf("          %s\n", b.Subject)
		}
		if exe, err := os.Executable(); err == nil {
			if fi, err := os.Stat(exe); err == nil {
				fmt.Printf(
					"installed %s, %s\n",
					fi.ModTime().
						Local().
						Format("15:04:05"),
					humanSince(time.Since(fi.ModTime())),
				)
				fmt.Printf("path      %s\n", exe)
			}
		}
		if b.Dirty {
			fmt.Println(
				"\nbuilt from a modified tree: this binary " +
					"is not exactly the commit it names",
			)
		}
		return 0
	case "version", "--version", "-v":
		// the shell answer to "what is this binary", so a report from a
		// terminal can name the puffin that produced it
		fmt.Println(thisBuild())
		// the full message when the repository is at hand. It usually
		// is not -- the binary lives in ~/.local/bin -- so this is a
		// bonus rather than the answer, and its absence is not worth
		// mentioning.
		if body := commitBody(thisBuild().Revision); body != "" {
			fmt.Println()
			fmt.Println(body)
		}
		return 0
	case "status":
		return cliStatus(domain)
	case "truth":
		return cliTruth(domain)
	case "flags":
		svc := ""
		if len(args) > 1 {
			svc = args[1]
		}
		return cliFlags(domain, svc)
	case "get":
		if len(args) != 3 {
			return usage("get <service> <key>")
		}
		return cliGet(domain, args[1], args[2])
	case "set":
		return cliSet(domain, args[1:])
	case "exec":
		return cliExec(args[1:], false)
	case "sh":
		return cliExec(args[1:], true)
	case "logs":
		return cliLogs(args[1:])
	case "start", "stop", "restart":
		return cliLifecycle(args[0], args[1:])
	case "-h", "--help", "help":
		printHelp()
		return 0
	default:
		// an unknown command is an error, not a hint. This returned 0,
		// which means a script could not tell a typo from success --
		// the one thing a shell tool has to get right.
		fmt.Fprintf(
			os.Stderr,
			"puffin: no command called %q\n\n",
			args[0],
		)
		printHelp()
		return 2
	}
}

// usage prints how a command is held and returns the error exit code.
func usage(u string) int {
	fmt.Fprintln(os.Stderr, "usage: puffin "+u)
	return 2
}

// cliStatus prints the roster the TUI shows, one line per service.
func cliStatus(domain string) int {
	e := Discover(domain)
	for _, w := range e.Warnings {
		fmt.Fprintln(os.Stderr, "warning: "+w)
	}
	for _, s := range e.Services {
		lease := "-"
		if s.Lease != nil {
			lease = s.Lease.State
		}
		api := "-"
		if s.HasAPI {
			api = strings.Join(s.APIVersions, ",")
			if api == "" {
				api = "yes"
			}
		}
		flags := "-"
		if s.Flags >= 0 {
			flags = strconv.Itoa(s.Flags)
			if s.Expensive > 0 {
				flags += fmt.Sprintf(" ($%d)", s.Expensive)
			}
		}
		fmt.Printf("%-18s %-10s %-12s %-10s %-8s %s\n",
			s.Name, s.State, lease, orDash(s.Version), api, flags)
	}
	if len(e.Services) == 0 {
		fmt.Fprintln(os.Stderr, "nobody home")
		return 1
	}
	return 0
}

// cliTruth prints hm's table: who is authorized, their cadence, their TTL.
func cliTruth(domain string) int {
	leases, err := hmTruth(fmt.Sprintf("http://hm.%s", domain))
	if err != nil {
		fmt.Fprintln(os.Stderr, "hm: "+err.Error())
		return 1
	}
	names := make([]string, 0, len(leases))
	for n := range leases {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		l := leases[n]
		fmt.Printf(
			"%-18s %-11s beats=%-5d cadence=%-6s expires-in=%s\n",
			n,
			l.State,
			l.Beats,
			fmt.Sprintf("%ds", l.CadenceMS/1000),
			fmt.Sprintf("%ds", int(l.TTL(time.Now()).Seconds())),
		)
	}
	return 0
}

// cliFlags prints flags, newest namespace per service, one line each.
func cliFlags(domain, only string) int {
	rows, err := latestFlags(domain)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}
	shown := 0
	for _, r := range rows {
		if only != "" && r.Service != only {
			continue
		}
		exp := " "
		if r.Expensive {
			exp = "$"
		}
		fmt.Printf(
			"%s %-14s %-26s = %-12s %s\n",
			exp,
			r.Service,
			r.Key,
			r.Value,
			r.Desc,
		)
		shown++
	}
	if only != "" && shown == 0 {
		fmt.Fprintf(
			os.Stderr,
			"no flags for %q in the newest namespace\n",
			only,
		)
		return 1
	}
	return 0
}

// cliGet prints one value, nothing else, so $(puffin get svc key) works.
func cliGet(domain, service, key string) int {
	rows, err := latestFlags(domain)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}
	for _, r := range rows {
		if r.Service == service && r.Key == key {
			fmt.Println(r.Value)
			return 0
		}
	}
	fmt.Fprintf(os.Stderr, "no flag %s/%s\n", service, key)
	return 1
}

// cliSet flips one flag: value typed by the declared kind, version resolved
// to the newest namespace, reason mandatory because flipr will refuse
// without one anyway -- the CLI just says so before the wire does.
func cliSet(domain string, args []string) int {
	var pos []string
	reason := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--reason" && i+1 < len(args) {
			reason = args[i+1]
			i++
			continue
		}
		pos = append(pos, args[i])
	}
	if len(pos) != 3 {
		return usage(`set <service> <key> <value> --reason "why"`)
	}
	if reason == "" {
		fmt.Fprintln(
			os.Stderr,
			`a reason is required: flipr refuses flips with no st`+
				`ated reason. add --reason "why"`,
		)
		return 2
	}
	service, key, value := pos[0], pos[1], pos[2]

	rows, err := latestFlags(domain)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}
	var row *FlagRow
	for i := range rows {
		if rows[i].Service == service && rows[i].Key == key {
			row = &rows[i]
			break
		}
	}
	if row == nil {
		fmt.Fprintf(
			os.Stderr,
			"no flag %s/%s -- set only flips declared flags; "+
				"services declare at deploy\n",
			service,
			key,
		)
		return 1
	}
	// the declared kind types the value: a string flag spelling "true"
	// stays a string
	if row.Kind == "bool" && value != "true" && value != "false" {
		fmt.Fprintf(
			os.Stderr,
			"%s/%s is a boolean; the value must be true or false\n",
			service,
			key,
		)
		return 2
	}
	if row.Kind == "int" {
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			fmt.Fprintf(
				os.Stderr,
				"%s/%s is an integer; %q is not\n",
				service,
				key,
				value,
			)
			return 2
		}
	}
	res, err := flip(
		fmt.Sprintf("http://flipr.%s", domain),
		*row,
		value,
		reason,
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}
	if res.Status != 200 {
		fmt.Fprintln(os.Stderr, strings.TrimSpace(res.Body))
		return 1
	}
	fmt.Printf(
		"%s/%s = %s (%s@%s)\n",
		service,
		key,
		value,
		row.Service,
		row.Version,
	)
	return 0
}

// cliExec runs a command in a pod. kubectl does the transport: it holds the
// kubeconfig and the auth posture, and the context is always explicit, because
// the context is the boundary between clusters and an implicit one is how work
// lands in the wrong place.
//
// The terminal is handed over whole, so vim and top behave.
func cliExec(args []string, interactive bool) int {
	ns := defaultNamespace()
	var pod string
	var cmd []string
	i := 0
	for i < len(args) {
		switch {
		case args[i] == "-n" && i+1 < len(args):
			ns = args[i+1]
			i += 2
		case args[i] == "--":
			cmd = args[i+1:]
			i = len(args)
		case pod == "":
			pod = args[i]
			i++
		default:
			cmd = args[i:]
			i = len(args)
		}
	}
	if pod == "" {
		if interactive {
			return usage("sh <pod> [-n ns]")
		}
		return usage("exec <pod> [-n ns] -- <cmd...>")
	}
	kargs := []string{"--context", kubeContext(), "-n", ns, "exec"}
	if interactive {
		kargs = append(kargs, "-it", pod, "--", "sh", "-c",
			"command -v bash >/dev/null && exec bash || exec sh")
	} else {
		if len(cmd) == 0 {
			return usage("exec <pod> [-n ns] -- <cmd...>")
		}
		kargs = append(kargs, pod, "--")
		kargs = append(kargs, cmd...)
	}
	c := exec.Command("kubectl", kargs...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}
	return 0
}

// latestFlags returns rows from each service's newest namespace only,
// resolved by the flags' own updatedAt stamps -- nobody types commit hashes.
func latestFlags(domain string) ([]FlagRow, error) {
	rows, err := fetchFlags(fmt.Sprintf("http://flipr.%s", domain))
	if err != nil {
		return nil, err
	}
	return newestOnly(rows), nil
}

// nsFlag pulls an optional -n out of an argument list, defaulting to the
// configured namespace. Shared by logs and the lifecycle verbs so they
// agree on how a namespace is spelled.
func nsFlag(args []string) (string, []string) {
	ns, rest := defaultNamespace(), []string{}
	for i := 0; i < len(args); i++ {
		if args[i] == "-n" && i+1 < len(args) {
			ns = args[i+1]
			i++
			continue
		}
		rest = append(rest, args[i])
	}
	return ns, rest
}

// cliLogs prints a pod's logs.
//
// -p reads the DEAD container's log, which is the one that says why a pod
// is restarting; the live log of a fresh container never does.
func cliLogs(args []string) int {
	ns, rest := nsFlag(args)
	tail, previous, pod := 500, false, ""
	for i := 0; i < len(rest); i++ {
		switch {
		case rest[i] == "--tail" && i+1 < len(rest):
			n, err := strconv.Atoi(rest[i+1])
			if err != nil {
				fmt.Fprintln(
					os.Stderr,
					"--tail wants a number, got "+rest[i+1],
				)
				return 2
			}
			tail = n
			i++
		case rest[i] == "-p" || rest[i] == "--previous":
			previous = true
		case pod == "":
			pod = rest[i]
		}
	}
	if pod == "" {
		return usage("logs <pod> [-n ns] [--tail N] [-p]")
	}
	body, err := PodLogs(kubeContext(), ns, pod, tail, previous)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}
	fmt.Print(body)
	if !strings.HasSuffix(body, "\n") {
		fmt.Println()
	}
	return 0
}

// cliLifecycle starts, stops or restarts the workload behind a name.
//
// It reports the workload it resolved to before it acts, because "stopped
// athlete" and "stopped deployment/athlete in payments" are different
// amounts of information at 03:20.
func cliLifecycle(verb string, args []string) int {
	ns, rest := nsFlag(args)
	if len(rest) != 1 {
		return usage(verb + " <name> [-n ns]")
	}
	ctx := kubeContext()
	// the guard comes first: an operator pointed at the wrong cluster
	// should be told that, not told the deployment does not exist there
	if err := guardContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}
	w, err := ResolveTarget(ctx, ns, rest[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}
	switch verb {
	case "stop":
		err = StopWorkload(ctx, w)
	case "start":
		err = StartWorkload(ctx, w)
	case "restart":
		err = RestartWorkload(ctx, w)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}
	fmt.Printf("%s %s in %s\n", pastTense(verb), w.Target(), w.Namespace)
	return 0
}

// commands is the ONE list.
//
// Help is printed from it and the unknown-command message is printed from it,
// so a command that exists cannot be missing from the help -- which is what
// happened: watch, notify, version and --age were all added tonight and the
// usage line still listed nine commands from this morning.
//
// A hand-rolled cli drifts exactly here, every time, and the fix is not a
// framework, it is having one list instead of three.
var commands = []struct{ use, does string }{
	{"status", "the enclave roster"},
	{"truth", "hm's citizenship table"},
	{"flags [service]", "flags, all or one service's"},
	{"get <service> <key>", "one value, bare, for $(...)"},
	{"set <service> <key> <value> --reason \"why\"", "flip a flag"},
	{"exec <pod> [-n ns] -- <cmd...>", "run one command in a pod"},
	{"sh <pod> [-n ns]", "an interactive shell in a pod"},
	{
		"logs <pod> [-n ns] [--tail N] [-p]",
		"a pod's logs; -p for the dead container's",
	},
	{"stop <name> [-n ns]", "take the workload behind it to zero"},
	{"start <name> [-n ns]", "put it back to what it was running"},
	{"restart <name> [-n ns]", "roll it: new pods, same count"},
	{"watch <logs|bus|agents> <pattern>", "wait for something and say so"},
	{"agents", "the claude sessions on this host, with ids to resume them"},
	{
		"notify [message]",
		"send a test notification, and say by which route",
	},
	{"selftest [--quiet]", "run puffin's own tests"},
	{"version", "the commit this binary was built from"},
	{"--age", "how old the binary is, and how old its commit is"},
	{
		"--context <name>",
		"look at another cluster; -c works too, and --contexts lists " +
			"them",
	},
	{
		"diff <path>",
		"what is on disk vs what is running, via kubectl's " +
			"server-side dry run",
	},
	{
		"selftest [--cover]",
		"run puffin's tests, and say what fraction of it they reach",
	},
	{
		"pet",
		"the companion, and what it can see: move the mouse and it " +
			"follows",
	},
	{
		"repos [path]",
		"which checkouts are safe to delete, and what the rest are " +
			"holding",
	},
}

// printHelp writes the whole surface, generated from the list above.
func printHelp() {
	fmt.Println(
		"puffin: a tui for the enclave. no arguments opens the tui.",
	)
	fmt.Println()
	width := 0
	for _, c := range commands {
		if n := len(c.use); n > width {
			width = n
		}
	}
	for _, c := range commands {
		fmt.Printf("  puffin %-*s  %s\n", width, c.use, c.does)
	}
	fmt.Println()
	fmt.Println("  " + strings.ReplaceAll(watchUsage, "\n", "\n  "))
	fmt.Println()
	printAddressing()
}

// printAddressing explains the two axes, because they are independent and
// nothing else says so.
//
// The two are independent, and setting one without the other gives a
// half-and-half view: one cluster's pods beside another cluster's
// services, with nothing on the screen saying which half you have.
//
// The boundary is stated here rather than left to be discovered: reading
// another cluster is a flag, and changing one is deliberately not.
func printAddressing() {
	fmt.Println("two axes, and they move independently:")
	fmt.Println()
	fmt.Println(
		"  --context <name>        which CLUSTER kubectl reads: " +
			"pods, logs, deploys, ports",
	)
	fmt.Println(
		"  PUFFIN_DOMAIN=<suffix>  which NAMES the roster probes " +
			"over http",
	)
	fmt.Println()
	fmt.Println(
		"  local answers on .test        puffin --context k3d-local",
	)
	fmt.Println(
		"  fleet answers on .mesh        PUFFIN_DOMAIN=mesh puffin " +
			"--context k3d-fleet",
	)
	fmt.Println()
	fmt.Println(
		"  setting one without the other shows one cluster's pods " +
			"beside",
	)
	fmt.Println(
		"  another cluster's services, and nothing on screen says so.",
	)
	fmt.Println()
	fmt.Println(
		"a namespace is not a context: a context is which cluster, a " +
			"namespace is a",
	)
	fmt.Println(
		"drawer inside it. k3d-fleet is the context; fleet is a " +
			"namespace within it.",
	)
	fmt.Println()
	fmt.Printf(
		"puffin READS any context and only CHANGES things in %s.\n",
		homeContext(),
	)
	fmt.Println("PUFFIN_HOME_CONTEXT names a different one.")
	fmt.Println(
		"PUFFIN_ALLOW_FOREIGN_CONTEXT=1 is how you say you mean " +
			"otherwise.",
	)
}

// commitBody returns the full commit message IF this shell is standing in a
// repository that has that commit. Stamping a whole message into a binary is
// a bad trade -- these messages run to forty lines -- so the subject is
// stamped and the body is looked up when it happens to be reachable.
func commitBody(rev string) string {
	if rev == "" {
		return ""
	}
	out, err := exec.Command("git", "log", "-1", "--pretty=%B", rev).
		Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// cliAgents lists the sessions on this host with the id needed to get back
// to one.
//
// The point of the list is the session id, so a session can be rejoined after
// it and its terminal are separated. Separation is the normal case rather than
// an accident -- a terminal closes, a tmux dies, a laptop sleeps -- and the
// session is still there on disk with its whole context.
//
// The id is the only way back to it, and it is otherwise only visible as a
// filename.
func cliAgents() int {
	v := fetchAgents(agentWindow)
	for _, w := range v.Warnings {
		fmt.Fprintln(os.Stderr, "warning: "+w)
	}
	if len(v.Sessions) == 0 {
		fmt.Fprintf(
			os.Stderr,
			"no sessions wrote in the last %s\n",
			agentWindow,
		)
		return 1
	}
	// The same numbers the pane carries, because the question you ask from
	// a shell is the question you ask from the screen: is this session all
	// right. A row that says only "waiting" cannot answer it -- a session
	// at 99% of its context is waiting too, and it is waiting for a wall.
	now := time.Now()
	// pad() rather than %-16s, because %-16s pads and never truncates: one
	// session called something long shifts every column after it on that
	// row only, which is the specific way a table stops being a table.
	fmt.Printf(
		"%s%s%s%s%6s %7s %13s %8s  %-10s %s\n",
		pad(
			"name",
			17,
		),
		pad("project", 19),
		pad("model", 17),
		pad("state", 10),
		"turns",
		"tokens",
		"context",
		"cost",
		"last log",
		"driven",
	)
	for _, s := range v.Sessions {
		ctx := "-"
		if s.Context > 0 {
			if limit := contextLimit(s.Model); limit > 0 {
				ctx = fmt.Sprintf(
					"%s %.0f%%",
					humanCount(s.Context),
					float64(s.Context)/float64(limit)*100,
				)
			} else {
				ctx = humanCount(s.Context)
			}
		}
		// a stale figure says so rather than passing as current: the
		// harness stopped reporting and the number stopped moving, and
		// those look identical without this
		cost := "-"
		if s.CostKnown {
			cost = fmt.Sprintf("$%.2f", s.CostUSD)
			if s.CostStale() {
				cost = fmt.Sprintf("~$%.2f", s.CostUSD)
			}
		}
		// "driven" is when a person last typed. A session whose last
		// log is seconds ago and whose last human turn is days ago is
		// running unattended, and neither "working" nor "idle" says so.
		driven := "never"
		if !s.LastHuman.IsZero() {
			driven = humanSince(now.Sub(s.LastHuman))
		}
		fmt.Printf("%s%s%s%s%6d %7s %13s %8s  %-10s %s\n",
			pad(orDash(s.Name), 17), pad(orDash(s.Project), 19),
			pad(orDash(modelCell(s)), 17), pad(s.State(now), 10),
			s.Turns, humanCount(s.Tokens()), ctx, cost,
			humanSince(now.Sub(s.LastWrite)), driven)
	}
	// the way back, spelled out. A uuid on its own is a fact; a command is
	// an answer, and the working directory is part of it -- resuming from
	// the wrong one gives you the session with the wrong tools around it.
	if len(v.Sessions) > 0 {
		s := v.Sessions[0]
		fmt.Println()
		fmt.Printf(
			"to rejoin the most recent:  cd %s && claude "+
				"--resume %s\n",
			orDash(s.Cwd),
			s.ID,
		)
	}
	return 0
}

// takeGlobals pulls the flags that apply to everything puffin does.
//
// There is one today and it is the one that was missing: --context. It
// exists because PUFFIN_KUBE_CONTEXT already worked and nobody could find
// it -- an environment variable is documentation you have to have read,
// while a flag is documentation you can discover by being wrong once.
//
// It sets the environment variable rather than threading a parameter
// through: kubeContext() is read from a dozen places including inside panes
// that have no argv, and one door into that decision is worth more than
// tidiness about globals.
//
// Returns the remaining args, whether the TUI should open, and an exit code
// of -1 for "carry on".
func takeGlobals(args []string) ([]string, bool, int) {
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--context" || a == "-c":
			if i+1 >= len(args) {
				fmt.Fprintln(
					os.Stderr,
					"--context wants a kube context, "+
						"e.g. k3d-local",
				)
				return nil, false, 2
			}
			i++
			if code := setContext(args[i]); code != 0 {
				return nil, false, code
			}
		case strings.HasPrefix(a, "--context="):
			if code := setContext(
				strings.TrimPrefix(a, "--context="),
			); code != 0 {
				return nil, false, code
			}
		case a == "--contexts":
			// what can I pass? asked at exactly the moment you get
			// it wrong
			for _, c := range kubeContexts() {
				mark := "  "
				if c == kubeContext() {
					mark = "* "
				}
				fmt.Println(mark + c)
			}
			return nil, false, 0
		default:
			rest = append(rest, a)
		}
	}
	return rest, len(rest) == 0, -1
}

// setContext checks the name against what kubectl actually has before
// accepting it.
//
// A typo'd context otherwise fails later, once per read, as a different
// error on every pane -- and "connection refused" on six screens does not
// spell "you misspelled the cluster". The check is one kubectl call and it
// turns six confusing failures into one clear one.
func setContext(name string) int {
	known := kubeContexts()
	for _, c := range known {
		if c == name {
			os.Setenv("PUFFIN_KUBE_CONTEXT", name)
			return 0
		}
	}
	fmt.Fprintf(os.Stderr, "no kube context called %q\n", name)
	if len(known) > 0 {
		fmt.Fprintln(
			os.Stderr,
			"this machine has: "+strings.Join(known, ", "),
		)
	}
	return 2
}

// kubeContexts asks kubectl what exists. An empty list is not an error here:
// it means kubectl could not be run, and the caller says so in its own words.
func kubeContexts() []string {
	out, err := exec.Command("kubectl", "config", "get-contexts",
		"-o", "name").Output()
	if err != nil {
		return nil
	}
	var cs []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			cs = append(cs, l)
		}
	}
	sort.Strings(cs)
	return cs
}
