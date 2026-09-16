package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// The kubernetes screen: what is running, in one picture.
//
// puffin shells out to kubectl rather than speaking the API: kubectl holds the
// kubeconfig, whatever auth the cluster wants, and one battle-tested JSON
// shape. The context is always explicit.
//
// On a machine with a development cluster and a production one in the same
// kubeconfig, the context is the whole boundary between them, and an implicit
// context is how development work lands in production. The default is the local
// cluster; anything else is a deliberate PUFFIN_KUBE_CONTEXT away.

// KubePod is one pod, reduced to what an operator reads at a glance.
type KubePod struct {
	Namespace string
	Name      string
	Phase     string
	Ready     string // "1/1"
	AllReady  bool
	Restarts  int
	Age       string
	Terminal  bool // Succeeded/Failed: finished, not failing
	// MemUsed is the pod's memory right now, summed across containers, and
	// MemLimit is what it is allowed. Zero limit means none was declared,
	// which is a different fact from "using nothing" and is drawn as such.
	//
	// The question being asked is whether a service has enough memory. A
	// phase of Running says a pod has not been killed yet. It does not say
	// it is nowhere near being killed, and those are the two different
	// things an operator is asking about.
	MemUsed  int64
	MemLimit int64
}

// KubeView is one cluster's pods, grouped for the screen.
type KubeView struct {
	Context string
	Pods    []KubePod
	Err     string
}

// kubeContext resolves which cluster puffin looks at. Explicit, always.
func kubeContext() string {
	if c := os.Getenv("PUFFIN_KUBE_CONTEXT"); c != "" {
		return c
	}
	return "k3d-local"
}

// fetchKube reads the cluster through kubectl with an explicit context.
func fetchKube(ctx string) KubeView {
	v := KubeView{Context: ctx}
	out, err := exec.Command("kubectl", "--context", ctx,
		"get", "pods", "-A", "-o", "json").Output()
	if err != nil {
		// kubectl's stderr is the useful part of its failures
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			v.Err = strings.TrimSpace(string(ee.Stderr))
		} else {
			v.Err = err.Error()
		}
		return v
	}
	v2, perr := parsePods(out, ctx)
	if perr != "" {
		v.Err = perr
		return v
	}
	// Usage comes from a second source and is allowed to be missing.
	// metrics-server is a separate deployment that can be absent, starting,
	// or broken, and none of those should cost the operator the pod list --
	// so a failure here leaves MemUsed at zero and the column reads "-".
	if used := fetchPodMemory(ctx); used != nil {
		for i := range v2 {
			if n, ok := used[v2[i].Namespace+"/"+v2[i].Name]; ok {
				v2[i].MemUsed = n
			}
		}
	}
	v.Pods = v2
	return v
}

// fetchPodMemory reads live usage from the metrics API.
//
// Through `kubectl get --raw` rather than `kubectl top`, because top prints a
// table meant for a person -- rounded, localised, and reshaped whenever kubectl
// feels like it -- while the raw endpoint returns the numbers with their units
// attached.
//
// nil means the metrics API did not answer, which is a normal state and not an
// error worth showing.
func fetchPodMemory(ctx string) map[string]int64 {
	out, err := exec.Command("kubectl", "--context", ctx,
		"get", "--raw", "/apis/metrics.k8s.io/v1beta1/pods").Output()
	if err != nil {
		return nil
	}
	return parsePodMemory(out)
}

// parsePodMemory sums each pod's containers. Split out so the arithmetic
// is testable without a cluster.
func parsePodMemory(out []byte) map[string]int64 {
	var raw struct {
		Items []struct {
			Metadata struct {
				Namespace string `json:"namespace"`
				Name      string `json:"name"`
			} `json:"metadata"`
			Containers []struct {
				Usage struct {
					Memory string `json:"memory"`
				} `json:"usage"`
			} `json:"containers"`
		} `json:"items"`
	}
	if json.Unmarshal(out, &raw) != nil {
		return nil
	}
	used := make(map[string]int64, len(raw.Items))
	for _, it := range raw.Items {
		var total int64
		for _, c := range it.Containers {
			if n, ok := parseQuantity(c.Usage.Memory); ok {
				total += n
			}
		}
		used[it.Metadata.Namespace+"/"+it.Metadata.Name] = total
	}
	return used
}

// parsePods reduces kubectl's JSON to the rows an operator reads. Split from
// fetchKube so the reduction is testable without an exec in the loop.
func parsePods(out []byte, ctx string) ([]KubePod, string) {
	var raw struct {
		Items []struct {
			Metadata struct {
				Namespace         string    `json:"namespace"`
				Name              string    `json:"name"`
				CreationTimestamp time.Time `json:"creationTimestamp"`
			} `json:"metadata"`
			Spec struct {
				Containers []struct {
					Resources struct {
						Limits struct {
							Memory string `json:"memory"`
						} `json:"limits"`
					} `json:"resources"`
				} `json:"containers"`
			} `json:"spec"`
			Status struct {
				Phase             string `json:"phase"`
				ContainerStatuses []struct {
					Ready        bool `json:"ready"`
					RestartCount int  `json:"restartCount"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, "kubectl answered, but not with pods: " +
			"" + err.Error()
	}
	var pods []KubePod
	for _, it := range raw.Items {
		ready, total, restarts := 0, 0, 0
		for _, c := range it.Status.ContainerStatuses {
			total++
			if c.Ready {
				ready++
			}
			restarts += c.RestartCount
		}
		// the pod's ceiling is the sum of its containers' ceilings. A
		// container with no limit makes the POD unlimited, because one
		// unbounded container can take the node down regardless of what
		// its neighbours declared.
		var limit int64
		bounded := len(it.Spec.Containers) > 0
		for _, c := range it.Spec.Containers {
			n, ok := parseQuantity(c.Resources.Limits.Memory)
			if !ok {
				bounded = false
				break
			}
			limit += n
		}
		if !bounded {
			limit = 0
		}
		pods = append(pods, KubePod{
			Namespace: it.Metadata.Namespace,
			Name:      it.Metadata.Name,
			Phase:     it.Status.Phase,
			Ready:     fmt.Sprintf("%d/%d", ready, total),
			AllReady:  total > 0 && ready == total,
			Restarts:  restarts,
			Age: humanAge(
				time.Since(it.Metadata.CreationTimestamp),
			),
			Terminal: it.Status.Phase == "Succeeded" ||
				it.Status.Phase == "Failed",
			MemLimit: limit,
		})
	}
	sort.Slice(pods, func(i, j int) bool {
		if pods[i].Namespace != pods[j].Namespace {
			return pods[i].Namespace < pods[j].Namespace
		}
		return pods[i].Name < pods[j].Name
	})
	return pods, ""
}

// humanAge renders durations the way kubectl does: the largest unit.
func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// otherClusters names the clusters this screen is NOT showing. Cheap: the
// host pane already asked k3d, and the answer is cached on the model for
// exactly this line.
func (m model) otherClusters() string {
	var out []string
	for _, c := range m.clusters {
		if "k3d-"+c.Name != m.kube.Context {
			state := "up"
			if c.ServersRunning < c.ServersCount {
				state = "down"
			}
			out = append(out, c.Name+" ("+state+")")
		}
	}
	return strings.Join(out, ", ")
}
