# using puffin

`puffin` with no arguments opens the console. `puffin --help` prints every
subcommand and then explains addressing, which is the one part worth
reading before you start.

## Addressing: two settings that move independently

`--context <name>` decides which cluster kubectl reads: pods, logs, deploys,
ports. `PUFFIN_DOMAIN=<suffix>` decides which names the roster probes over http.
Setting one without the other shows one cluster's pods beside another cluster's
services, and nothing on the screen says which half you have.

A namespace is not a context. A context is which cluster; a namespace is a
drawer inside it.

puffin reads any context. It changes things only in the one named by
`PUFFIN_HOME_CONTEXT`, which defaults to `k3d-local`.
`PUFFIN_ALLOW_FOREIGN_CONTEXT=1` is how you say you mean otherwise, and it
is there to be typed on purpose and never by habit.

`PUFFIN_NAMESPACE` is the namespace the verbs assume when no `-n` is given;
it defaults to `default`. `PUFFIN_CODE_ROOT` is where checkouts live, for
the agents pane's project column; it defaults to `~/src`.

## The shell door

Plain words, no JSON anywhere near a shell prompt. puffin resolves each
service's newest deploy namespace itself, so nobody types a commit hash.

    puffin status                              the enclave roster
    puffin truth                               the citizenship table
    puffin flags [service]                     flags, all or one service's
    puffin get <service> <key>                 one value, bare, for $(...)
    puffin set <service> <key> <value> --reason "why"
    puffin exec <pod> [-n ns] -- <cmd...>      run one command in a pod
    puffin sh <pod> [-n ns]                    an interactive shell in a pod
    puffin logs <pod> [-n ns] [--tail N] [-p]  a pod's logs
    puffin stop <name> [-n ns]                 take it down, leave it down
    puffin start <name> [-n ns]                put it back to what it was
    puffin restart <name> [-n ns]              new pods, same count
    puffin repos [path]                        which checkouts are safe to
                                               delete, and what the rest hold

`logs -p` reads the DEAD container's log. That is the one that says why a
pod is restarting; the live log of a fresh container never does.

`stop` and `start` take a pod name or a workload name, whichever you happen
to be holding. A stopped service has no pods to point at, so `puffin start
athlete` has to work with nothing running, and does.

`set` types the value by the flag's declared kind, so a boolean flag takes
true or false and nothing else, and a string flag spelling "true" stays a
string. The reason is mandatory because flipr refuses flips without one;
the CLI says so before the wire does.

## The panes

One keystroke each from the roster. A pane owns its own state and fetches
through one function that returns a plain struct, which is the seam a
backend arrives through rather than decoration.

| key | pane | what it is for |
|---|---|---|
| c | cluster | pods, namespace-grouped, coloured by condition |
| v | host | disk against its floor, the VM, the clusters, and which one puffin is pointed at |
| d | deploys | what the flags say is deployed against what the pods are running |
| a | agents | this host's Claude sessions: project, branch, model, turns, tokens |
| p | metrics | prometheus targets by NAME, and what can fire in prometheus and grafana |
| l | logs | a buffer, with `/` to search and `n`/`N` to walk the matches |

The deploys pane is three-valued on purpose. An image tagged `:dev` carries
no build id, and a flag namespace is sometimes a placeholder; neither can
agree or disagree with anything, and calling that agreement is a green
light bolted to a cut wire. "cannot say" is its own answer.

The agents pane reads Claude Code's transcripts under `~/.claude/projects`,
so it is a view of THIS HOST and says so. The column is "last wrote", never
"running": a file time is not a heartbeat, and a session that died mid-turn
looks exactly like one that is thinking.

The metrics pane resolves prometheus's numeric targets to pod names before
showing them, because an address is not an identifier. It reports prometheus and
grafana separately: they fail differently, and a grafana puffin cannot
authenticate to returns an empty list whether or not it has rules.

An unauthenticated empty answer is reported as unauthenticated, never as zero.

## Keys

| key | does |
|---|---|
| any | dismisses the bird |
| j / k, arrows | move |
| enter | read a service's contract |
| r | rediscover the enclave |
| t | cycle the theme |
| q | back, then quit |

On the cluster screen, acting on the pod under the cursor:

| key | does |
|---|---|
| l | its logs, in the pager |
| s | start it |
| x | stop it |
| R | restart it |
| space | fold or unfold what the cursor is on |
| h | collapse into the namespace above |

The cluster screen folds by namespace and by shared pod prefix -- more than two
of a `word-` fold into `word-` -- and windows what is left to the terminal. A
folded header carries the count and condition of what it hid: a fold must never
hide a broken pod without saying a pod is broken.

The cursor indexes rows, so the lifecycle keys refuse a header rather than
acting on whatever pod happens to be nearby.

`s`, `x` and `R` ask before they act, and the question names the workload
rather than the pod: a deployment is what moves, and a pod nobody scaled is
back before the screen redraws. Anything that is not `y` is no.

## Themes

    PUFFIN_THEME=vaporwave puffin

`t` cycles at runtime and the roster names what you are wearing. The choice
is not persisted, the same reason puffin holds no roster, so `PUFFIN_THEME`
is how a choice outlives a session.

| name | is |
|---|---|
| corvid | the default: midnight in theme form |
| vaporwave | sun low on the horizon, scanlines over it, cyan haze below |
| bladerunner | smog lit from below by something orange, cold light above |
| stardew | pale sky over a green field; the light one |
| puffin | the palette puffin carried before the themes landed |

Two things deliberately do NOT follow the theme. The semantic colours -- ok,
warn, caution -- are shared by every dark theme, because a warning that changes
colour with the theme is not a warning; only the light theme overrides them, and
only for contrast.

And the beak stays orange in all five, because a puffin without an orange beak
is a guillemot whatever the terminal is wearing.

## Tests

    go test ./...                           the unit suite
    go test -tags live -run TestLive ./...  against a real cluster

The live suite builds its own two-replica Deployment, exercises stop, start,
restart, logs and the pod-name walk against it, and deletes it again in a
cleanup that runs even when a test panics. It refuses to run against any context
but the home one, and it does not touch a service you are using.

## Building without starling

Talking back to an agent needs starling. Reading them does not, and nobody
should be made to run a second service to use the first.

    go build -tags nostarling -o bin/puffin ./cmd/puffin

produces a binary that does not link it at all.
