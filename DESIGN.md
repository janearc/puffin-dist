# puffin — design

## The premise

The definition sets the scope: the enclave is every service that publishes an
API. Which is to say, all of them. Membership is answering `/api`, not being on
a list, so puffin holds no roster and never will.

Discovery asks the mesh who exists: hm, the network's own health monitor, keeps
a truth table of who heartbeats; flipr, the flag store, knows who has flags; and
every candidate is then probed by name. Source failures render as warnings,
never as silent absences — "hm is down" and "no citizens" are different answers.

## The five states

Puffin once called a healthy daemon dead, and the fix became the status
model. The `.test` wildcard resolves every name, traefik answers 404 for the
unrouted ones, and "no route", "not deployed" and "down" are three different
truths that used to share one red word.

| state | meaning | color |
|---|---|---|
| healthy | answers /health, says so | green |
| daemon | no route by design, alive by heartbeat | green |
| unhealthy | answers /health with a refusal and a reason | yellow |
| absent | known to the mesh, not deployed | dim |
| down | expected alive, is not | red — the only red |

The probe classifies what the wire actually said — a health-shaped answer, a
traefik page, or nothing — and hm's citizenship decides the unrouted cases.

A 200 is only an answer when the body parses as health; absent-but-routed is
traefik talking about itself, and it has been mistaken for an answer enough
times here to earn its own code path.

## The universal client

Every member serves a FileDescriptorSet at `/api`, built by buf from the same
bytes its wire types came from. Puffin parses it and renders the contract
generically: methods, message shapes, and — because buf keeps SourceCodeInfo —
the author's own comments beside each field.

The composer builds a valid protojson skeleton from the schema (one arm per
oneof, 64-bit ints as strings, cycles capped), shows the contract next to the
editor, and fires the request. Zero per-service code. A service deployed
tomorrow is browsable and callable tomorrow.

Membership is parsing, not sniffing: bytes that do not decode to a
descriptor naming at least one service are not an API, whatever the status
code claimed.

## The operator screens

One keystroke each from the roster, one visual grammar across all three.

**f — flags.** Every flag in the store, newest namespace per service, the
expensive set wearing `$`. Enter opens an editor directly under the flag: bools
arrive pre-toggled, the reason field is mandatory because flipr will refuse
without it, and refusals pass through in flipr's own words.

A successful flip refetches — the store is the truth, the screen borrows it.

**m — maps.** Browse, not display. kingfisher's mount table, its JSON directory
indexes with sizes, and on enter the assessment: one deliberate HEAD — available
or not available, with the status, size, content-type, cache posture and
range support.

One HEAD on request, never a sweep: hammering every file to decorate a listing
is a scan, not a browse.

**c — cluster.** Pods namespace-grouped with a running/waiting/broken headline.

Colors by condition; a Succeeded pod renders finished, not failing. kubectl
underneath with the context explicit always — the context is the boundary
between one cluster and another, and the discipline that binds humans at the
shell binds the tool.

Default `k3d-local`; anything else is a deliberate `PUFFIN_KUBE_CONTEXT` away.

`l` reads the pod under the cursor into the pager. `s`, `x` and `R` start,
stop and restart it.

## Starting and stopping

A pod has no start and no stop. Deleting one is not stopping it — the controller
has a replacement scheduled before the terminal redraws — so the verbs here mean
what an operator means by them: `x` takes the workload behind the pod to zero
replicas and leaves it there, `s` puts it back, `R` rolls it.

Anything else would be a stop that does not stop, and the dev network's third
condition ("easy to start and stop them and it's not mysterious") is not
satisfied by a button that appears to work.

The walk from pod to workload goes through the controller references and
does not halt at the ReplicaSet. Scaling a ReplicaSet is undone by its
Deployment within seconds, which looks exactly like the tool being broken.

The replica count a workload had before the stop rides on the workload as an
annotation, so start restores three replicas rather than guessing one, and
puffin holds no state between invocations — the same reason it holds no roster.

No annotation means one: a guess, but a visible one, because the roster shows
the count it came back as. The annotation is puffin's own and a redeploy
overwrites it; nothing reads it but start.

Confirmation is not decoration. `x` resolves the workload first and then
asks, so the question names `deployment/athlete` and not the pod, which is
the difference between an operator agreeing to what they meant and
agreeing to what they typed. Anything that is not `y` is no.

Reads are never guarded.

Changes are: the mutation path refuses any context but the home one before it
reaches kubectl, because a machine's own current context is often production's
and one absent flag is the entire distance between two clusters.

`PUFFIN_HOME_CONTEXT` names the one that may be changed;
`PUFFIN_ALLOW_FOREIGN_CONTEXT` exists to be typed deliberately.

## The shell door

Subcommands for the shell, because JSON in the shell is a punishment nobody
ordered: `status`, `truth`, `flags`, `get`, `set`, `exec`, `sh`.

Puffin builds the protojson itself and resolves each service's newest deploy
namespace by the flags' own timestamps — nobody types a commit hash. `set` types
the value by the flag's declared kind, so a string flag spelling "true" stays a
string. `get` prints the bare value for `$(...)`.

`exec` and `sh` run commands in pods by delegating to kubectl with stdio handed
over whole, so interactive tools behave.

The credential story for a work-server — a pod to shell into and work from — is
a declared seam, not yet built: when SSO lands at the edge, puffin grows a
bearer it holds on the operator's behalf, and the mechanism gets designed
against the real oauth2-proxy config rather than guessed at now.

## The look

The palette is dodo's tokens, so the terminal and the web read as one thing.

The bird is painted per-rune in its actual colors — black plumage in charcoal,
white face and belly in snow, the beak in the orange that separates a puffin
from a guillemot — because the first render painted the whole bird in ink and
produced an inverted puffin.

The splash animates the way the 90s animated: the name types itself out, the
prompt blinks, and nothing else moves. Two effects is tasteful; three is a
screensaver.

When interface's theme endpoint lands, puffin consumes it live and the
baked palette becomes the fallback. Themes are the one place
last-known-good is legal: a stale colour misleads nobody, a stale flag
does.

## Verification

Unit tests for the parser, against real descriptor bytes, and for the skeleton
builder, on oneof discipline and type fidelity.

Then the highlighter, whose output stripped of colour is byte-identical to its
input; the five-state matrix, cell by cell; the newest-namespace resolution;
and the kubectl reducer against kubectl's real JSON.

The update loop is driven like an operator: keys in, screens out, including the
editor's refusal of reasonless flips. Discovery runs against a Host-routed fake
shaped like traefik. A `-tags live` suite verifies against the real enclave. The
splash is tested for the bird's presence, its majesty, and the correct eye.

The lifecycle layer runs kubectl through one swappable function, so the tests
assert the order things happen in and not merely their arguments: the replica
count is recorded before the workload goes to zero.

Reversed, the annotation records zero, every stopped service comes back as one
replica forever, and nothing anywhere says so.

The pod-to-workload walk is tested past the ReplicaSet, the guard is tested for
refusing before it reaches kubectl, and the confirmation is driven from the
keyboard — x asks, n cancels, y acts.

The lifecycle integration suite (`-tags live`) builds its own Deployment, does
everything to it, and takes it away again in a cleanup that runs whether the
test passed, failed or panicked.

It runs TWO replicas on purpose: a workload that happens to run one cannot tell
"restored the recorded count" apart from "fell back to one", so a single-replica
check proves nothing about the annotation it was written to verify.

It also holds the workload down for eight seconds after a stop, because a scale
applied to the wrong object comes back on its own and that is precisely the
failure the walk exists to prevent.

Testing start and stop by bouncing a service somebody is using is not a
test. It is an outage with a good intention behind it, it is not
repeatable, and it lives in a terminal instead of the repository.

Coverage: 75.0% of statements, against a floor of 90%, so this repository is
under it.

The lifecycle layer and both its doors are at 81-100%; the shortfall is older
than this work and sits in the http fetchers (fetchFlags, FetchAPI, fetchMounts,
fetchListing, headFile), the invoke skeleton builder, and the views — real gaps
with real tests owed, not un-fakeable execs.

Naming them here rather than rounding up: the floor is a floor, and this is a
debt with an address.

## The gaps discovery finds, as classes

Name-keyed discovery turns up the same three shapes on every network, and
each is a finding rather than a fault in the tool. Puffin reports them
instead of papering over them, which is most of what it is for.

- **A service deployed under a name its configuration does not use.** The
  route says one thing and the flag namespace another, so nothing keyed by
  name can see both halves. It is invisible until something goes looking
  for it by name, which is exactly what discovery does.
- **A service that serves `/api` with no service block in its
  descriptor** — message types only. Puffin refuses it as an API, which is
  correct and is also the whole finding: something answered, and what it
  answered with is not a contract.
- **A daemon with no route by design**, which therefore cannot serve
  anything through the front door. Alive by heartbeat, unreachable by
  intent. It needs a ruling rather than a fix: a route, a side channel, or
  a stated exemption. Until then it sits in the antechamber and says so.

The theme endpoint, macros, and grafana panels remain future work.
