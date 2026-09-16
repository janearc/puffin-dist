package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Starting, stopping and reading pods.
//
// A pod has no start or stop. Deleting one is not stopping it -- the controller
// has a replacement scheduled before the terminal redraws -- so "stop" here
// means what an operator means: take the workload behind this pod down to zero
// and leave it down. Start puts it back.
//
// That is the only reading under which starting and stopping a service is not
// mysterious: a stop that does not stop is the mystery.
//
// The replica count a workload had before the stop rides on the workload
// itself as an annotation, so puffin holds no state between invocations --
// the same reason it holds no roster. Start reads it back; absent, one.

// prevReplicas records what a workload was running before puffin stopped it.
const prevReplicas = "puffin.janearc.dev/previous-replicas"

// mutationEnvVar names the deliberate override for the context guard.
const mutationEnvVar = "PUFFIN_ALLOW_FOREIGN_CONTEXT"

// homeContext is the only context puffin will change on its own
// authority. PUFFIN_KUBE_CONTEXT alone changes what puffin looks at;
// naming a context here is the separate and deliberate act of saying
// which cluster it may touch.
func homeContext() string {
	if c := os.Getenv("PUFFIN_HOME_CONTEXT"); c != "" {
		return c
	}
	return "k3d-local"
}

// defaultNamespace is the namespace the verbs assume when none is given.
// Kubernetes calls it "default"; a cluster that keeps its services
// somewhere else says so once, here, rather than at every call site.
func defaultNamespace() string {
	if ns := os.Getenv("PUFFIN_NAMESPACE"); ns != "" {
		return ns
	}
	return "default"
}

// KubeWorkload is the controller behind a pod -- the thing that actually
// has a running/stopped state.
type KubeWorkload struct {
	Kind      string // Deployment, StatefulSet, DaemonSet, Job
	Name      string
	Namespace string
	Replicas  int
	Scalable  bool
}

// Target renders the workload the way kubectl addresses it.
func (w KubeWorkload) Target() string {
	return strings.ToLower(w.Kind) + "/" + w.Name
}

// guardContext refuses to mutate a cluster that is not the dev network.
//
// The context is the dev/prod boundary on this machine and this is the one
// code path in puffin that can take a service down, so the boundary is
// checked here rather than trusted. Reads are never guarded; only changes.
func guardContext(ctx string) error {
	if ctx == homeContext() || os.Getenv(mutationEnvVar) != "" {
		return nil
	}
	return fmt.Errorf(
		"refusing to change %q: puffin only starts and stops in "+
			"%s; set %s to mean it",
		ctx,
		homeContext(),
		mutationEnvVar,
	)
}

// kubectlRunner runs kubectl and hands back its stdout.
//
// It is a variable so the parts that matter -- record the replica count before
// scaling to zero, read it back on the way up -- can be tested for the order
// they happen in rather than only for their arguments.
//
// An annotation written after the scale would record zero and the service would
// come back as one replica forever, which is the kind of thing that is obvious
// in a test and invisible in a terminal. Only tests reassign it.
var kubectlRunner = func(args ...string) ([]byte, error) {
	out, err := exec.Command("kubectl", args...).Output()
	if err != nil {
		return nil, kubectlErr(err)
	}
	return out, nil
}

// kubectlJSON runs kubectl with the context explicit, always.
func kubectlJSON(ctx string, args ...string) ([]byte, error) {
	return kubectlRunner(append([]string{"--context", ctx}, args...)...)
}

// kubectlErr surfaces kubectl's stderr, which is the useful half of its
// failures -- the exit status alone says nothing an operator can act on.
func kubectlErr(err error) error {
	if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
		return fmt.Errorf("%s", strings.TrimSpace(string(ee.Stderr)))
	}
	return err
}

// ownerRef is the shape of the one field that resolves a pod to its
// controller. Only the controller reference counts; a pod can carry others.
type ownerRef struct {
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Controller *bool  `json:"controller"`
}

// controllerOf picks the controlling reference out of a set.
func controllerOf(refs []ownerRef) (ownerRef, bool) {
	for _, r := range refs {
		if r.Controller != nil && *r.Controller {
			return r, true
		}
	}
	return ownerRef{}, false
}

// resolveWorkload walks a pod up to the thing that controls it.
//
// Pod -> ReplicaSet -> Deployment is the common case and the one that
// matters: scaling the ReplicaSet is undone by the Deployment within
// seconds, so stopping the ReplicaSet is a stop that does not stop.
func resolveWorkload(ctx, ns, pod string) (KubeWorkload, error) {
	out, err := kubectlJSON(ctx, "-n", ns, "get", "pod", pod, "-o", "json")
	if err != nil {
		return KubeWorkload{}, err
	}
	w, err := workloadFromPod(out, ns)
	if err != nil {
		return KubeWorkload{}, err
	}
	// a ReplicaSet is never the answer; its Deployment is
	if w.Kind == "ReplicaSet" {
		rs, err := kubectlJSON(
			ctx,
			"-n",
			ns,
			"get",
			"replicaset",
			w.Name,
			"-o",
			"json",
		)
		if err != nil {
			return KubeWorkload{}, err
		}
		up, err := ownerFromObject(rs)
		if err != nil {
			return KubeWorkload{}, fmt.Errorf(
				"replicaset %s has no controller; nothing to "+
					"start or stop",
				w.Name,
			)
		}
		w.Kind, w.Name = up.Kind, up.Name
	}
	w.Scalable = w.Kind == "Deployment" || w.Kind == "StatefulSet"
	if !w.Scalable {
		return w, nil
	}
	n, err := currentReplicas(ctx, w)
	if err != nil {
		return KubeWorkload{}, err
	}
	w.Replicas = n
	return w, nil
}

// workloadFromPod reduces a pod's JSON to its controller. Split out so the
// walk is testable without a cluster.
func workloadFromPod(out []byte, ns string) (KubeWorkload, error) {
	var raw struct {
		Metadata struct {
			Namespace       string     `json:"namespace"`
			OwnerReferences []ownerRef `json:"ownerReferences"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return KubeWorkload{}, fmt.Errorf(
			"kubectl answered, but not with a pod: %w",
			err,
		)
	}
	if raw.Metadata.Namespace != "" {
		ns = raw.Metadata.Namespace
	}
	ref, ok := controllerOf(raw.Metadata.OwnerReferences)
	if !ok {
		return KubeWorkload{}, fmt.Errorf(
			"this pod has no controller -- it was created " +
				"directly, so there is nothing to scale; " +
				"delete it if you want it gone",
		)
	}
	return KubeWorkload{Kind: ref.Kind, Name: ref.Name, Namespace: ns}, nil
}

// ownerFromObject pulls the controlling reference off any object's JSON.
func ownerFromObject(out []byte) (ownerRef, error) {
	var raw struct {
		Metadata struct {
			OwnerReferences []ownerRef `json:"ownerReferences"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return ownerRef{}, err
	}
	ref, ok := controllerOf(raw.Metadata.OwnerReferences)
	if !ok {
		return ownerRef{}, fmt.Errorf("no controller")
	}
	return ref, nil
}

// currentReplicas reads a workload's spec replica count.
func currentReplicas(ctx string, w KubeWorkload) (int, error) {
	out, err := kubectlJSON(ctx, "-n", w.Namespace, "get", w.Target(),
		"-o", "jsonpath={.spec.replicas}")
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return 0, nil
	}
	return strconv.Atoi(s)
}

// storedReplicas reads back what a workload was running before it was
// stopped. Absent or unreadable means one -- the only safe guess, and a
// visible one, since a workload that wanted three comes back as one and
// says so on the roster.
func storedReplicas(ctx string, w KubeWorkload) int {
	out, err := kubectlJSON(
		ctx,
		"-n",
		w.Namespace,
		"get",
		w.Target(),
		"-o",
		"jsonpath={.metadata.annotations."+escapeJSONPath(
			prevReplicas,
		)+"}",
	)
	if err != nil {
		return 1
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// escapeJSONPath escapes the dots and slashes in an annotation key so
// jsonpath reads it as one field name rather than a path.
func escapeJSONPath(k string) string {
	return strings.NewReplacer(".", `\.`, "/", `\/`).Replace(k)
}

// StopWorkload takes the workload behind a pod to zero replicas, recording
// what it was running so Start can put it back.
func StopWorkload(ctx string, w KubeWorkload) error {
	if err := guardContext(ctx); err != nil {
		return err
	}
	if !w.Scalable {
		return fmt.Errorf(
			"a %s does not scale; puffin will not guess what "+
				"stopping one should mean",
			strings.ToLower(w.Kind),
		)
	}
	if w.Replicas == 0 {
		return fmt.Errorf("%s is already stopped", w.Target())
	}
	if err := annotate(
		ctx,
		w,
		prevReplicas,
		strconv.Itoa(w.Replicas),
	); err != nil {
		return err
	}
	return scale(ctx, w, 0)
}

// StartWorkload restores a stopped workload to the replica count it had.
func StartWorkload(ctx string, w KubeWorkload) error {
	if err := guardContext(ctx); err != nil {
		return err
	}
	if !w.Scalable {
		return fmt.Errorf(
			"a %s does not scale; puffin will not guess what "+
				"starting one should mean",
			strings.ToLower(w.Kind),
		)
	}
	if w.Replicas > 0 {
		return fmt.Errorf(
			"%s is already running %d",
			w.Target(),
			w.Replicas,
		)
	}
	return scale(ctx, w, storedReplicas(ctx, w))
}

// RestartWorkload rolls the workload -- new pods, same replica count.
// This is the one an Error pod usually wants.
func RestartWorkload(ctx string, w KubeWorkload) error {
	if err := guardContext(ctx); err != nil {
		return err
	}
	_, err := kubectlJSON(
		ctx,
		"-n",
		w.Namespace,
		"rollout",
		"restart",
		w.Target(),
	)
	return err
}

// scale sets a workload's replica count.
func scale(ctx string, w KubeWorkload, n int) error {
	_, err := kubectlJSON(ctx, "-n", w.Namespace, "scale", w.Target(),
		"--replicas="+strconv.Itoa(n))
	return err
}

// annotate writes one annotation onto a workload.
func annotate(ctx string, w KubeWorkload, k, v string) error {
	_, err := kubectlJSON(ctx, "-n", w.Namespace, "annotate", "--overwrite",
		w.Target(), k+"="+v)
	return err
}

// PodLogs reads a pod's logs. previous asks for the dead container's -- the
// one that explains why a pod is restarting, which the live log never does.
func PodLogs(ctx, ns, pod string, tail int, previous bool) (string, error) {
	args := []string{"-n", ns, "logs", pod, "--tail=" + strconv.Itoa(tail)}
	if previous {
		args = append(args, "--previous")
	}
	out, err := kubectlJSON(ctx, args...)
	if err != nil {
		return "", err
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		return "(no output)", nil
	}
	return string(out), nil
}

// ResolveTarget finds the workload a name refers to.
//
// The TUI always has a pod under the cursor, but the shell does not: a stopped
// service has no pods at all, so `puffin start athlete` must work with nothing
// running to point at. A name is tried as a pod first, then as a workload
// directly.
//
// Boring on purpose -- an operator should not have to know which of the two
// they are holding.
func ResolveTarget(ctx, ns, name string) (KubeWorkload, error) {
	w, err := resolveWorkload(ctx, ns, name)
	if err == nil {
		return w, nil
	}
	kinds := []struct{ arg, display string }{
		{"deployment", "Deployment"},
		{"statefulset", "StatefulSet"},
	}
	for _, k := range kinds {
		kind, display := k.arg, k.display
		out, gerr := kubectlJSON(ctx, "-n", ns, "get", kind, name,
			"-o", "jsonpath={.spec.replicas}")
		if gerr != nil {
			continue
		}
		n, cerr := strconv.Atoi(strings.TrimSpace(string(out)))
		if cerr != nil {
			n = 0
		}
		return KubeWorkload{
			Kind:      display,
			Name:      name,
			Namespace: ns,
			Replicas:  n,
			Scalable:  true,
		}, nil
	}
	return KubeWorkload{}, fmt.Errorf(
		"no pod, deployment or statefulset named %q in %s",
		name,
		ns,
	)
}
