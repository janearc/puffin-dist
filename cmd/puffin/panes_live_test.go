//go:build live

package main

import (
	"strings"
	"testing"
	"time"
)

// The live checks for the panes. Both are READ-ONLY -- they run kubectl get,
// colima list and k3d list and nothing else -- so unlike the lifecycle suite
// these do not build a workload to play with and cannot disturb one.

// TestLiveHost reads this machine and asserts the shape of the answer, not
// its values: the numbers are whatever they are today.
func TestLiveHost(t *testing.T) {
	h := fetchHost()
	if h.Hostname == "" {
		t.Fatal("no hostname")
	}
	if len(h.Disks) == 0 {
		t.Fatal("no filesystems read")
	}
	for _, d := range h.Disks {
		if d.TotalBytes == 0 {
			t.Fatalf("%s has no size", d.Path)
		}
		t.Logf(
			"%s: %s free of %s",
			d.Path,
			humanBytes(int64(d.FreeBytes)),
			humanBytes(int64(d.TotalBytes)),
		)
	}
	for _, v := range h.VMs {
		t.Logf(
			"colima %s: %s, %d cpu, %s",
			v.Name,
			v.Status,
			v.CPUs,
			humanBytes(int64(v.MemoryBytes)),
		)
		if strings.EqualFold(v.Status, "running") {
			if v.Uptime == "" {
				t.Errorf(
					"  the running VM said nothing about "+
						"itself: %q",
					v.InsideError,
				)
			}
			t.Logf(
				"  inside: up %s, load %.2f, mem %d/%d MB, "+
					"%d blocked",
				v.Uptime,
				v.Load[0],
				v.MemUsedMB,
				v.MemTotalMB,
				v.Blocked,
			)
			t.Logf(
				"  disk:   %.1f/%.1fG used · images %.1fG · "+
					"volumes %.1fG · cache %.1fG · "+
					"reclaimable %.1fG",
				v.DiskUsedGB,
				v.DiskTotalGB,
				v.ImagesGB,
				v.VolumesGB,
				v.BuildCacheGB,
				v.ReclaimableGB,
			)
			if v.DiskTotalGB == 0 {
				t.Error(
					"  the VM's own disk was not read -- " +
						"the host's free space is " +
						"not the disk the clusters " +
						"run on",
				)
			}
			if note, tight := diskNote(v); tight {
				t.Logf("  WARN:   %s", note)
			}
		}
	}
	var found bool
	for _, c := range h.Clusters {
		t.Logf(
			"k3d %s: servers %d/%d",
			c.Name,
			c.ServersRunning,
			c.ServersCount,
		)
		if "k3d-"+c.Name == kubeContext() {
			found = true
		}
	}
	if len(h.Clusters) > 0 && !found {
		t.Fatalf(
			"puffin's context %q is not among the clusters on "+
				"this host",
			kubeContext(),
		)
	}
	for _, w := range h.Warnings {
		t.Logf("warning: %s", w)
	}
}

// TestLiveDeploys reconciles the real cluster against the real flipr. It
// asserts that the reconciliation RAN, not that everything agrees: a
// disagreement is a finding about the estate, not a broken test.
func TestLiveDeploys(t *testing.T) {
	v := fetchDeploys(homeContext(), "test")
	if len(v.Rows) == 0 {
		t.Fatal("no deploys read from the cluster")
	}
	var agree, disagree, cannot int
	for _, d := range v.Rows {
		switch d.Agree {
		case AgreeYes:
			agree++
		case AgreeNo:
			disagree++
			t.Logf(
				"MISMATCH: %s/%s image %s (%s) but it "+
					"reports %s",
				d.Namespace,
				d.Service,
				d.Tag,
				d.Digest,
				d.Reported,
			)
		default:
			cannot++
		}
		if d.Pods == 0 {
			t.Fatalf("%s reduced to zero pods", d.Service)
		}
	}
	t.Logf(
		"%d services: %d confirmed, %d mismatched, %d uncheckable",
		len(v.Rows),
		agree,
		disagree,
		cannot,
	)
	for _, d := range v.Rows {
		if true {
			t.Logf(
				"  %-20s ns=%-14s %-11s uses-flipr=%v",
				d.Service,
				orDash(d.FliprNS),
				orDash(d.NSState),
				d.UsesFlipr,
			)
		}
	}
	for _, w := range v.Warnings {
		t.Logf("warning: %s", w)
	}
	// every row must carry a digest: the digest is the only hard fact on
	// the screen, and a row without one is a row that cannot be checked
	for _, d := range v.Rows {
		if d.Digest == "" && !strings.HasPrefix(d.Namespace, "kube-") {
			t.Errorf(
				"%s/%s has no image digest",
				d.Namespace,
				d.Service,
			)
		}
	}
}

// TestLiveMetrics reads the real prometheus and the real grafana. It asserts
// that the pane learned the shape of the answer -- including whether it was
// allowed to see grafana's rules -- and never that the estate is quiet.
func TestLiveMetrics(t *testing.T) {
	v := fetchMetricsFrom(
		"http://prometheus.test",
		"http://grafana.test",
		homeContext(),
	)
	if len(v.Targets) == 0 {
		t.Fatal("no scrape targets read")
	}
	named, addressed := 0, 0
	for _, tg := range v.Targets {
		if tg.Addressed() {
			addressed++
			t.Logf(
				"still an address: %s (job %s)",
				tg.Instance,
				tg.Job,
			)
		} else {
			named++
		}
	}
	t.Logf(
		"%d targets: %d named, %d still addresses",
		len(v.Targets),
		named,
		addressed,
	)
	t.Logf("prometheus: %d rules, %d firing", v.Rules, len(v.Alerts))
	t.Logf(
		"grafana: reachable=%v anonymous=%v rules=%d firing=%d err=%q",
		v.Grafana.Reachable,
		v.Grafana.Anonymous,
		v.Grafana.Rules,
		len(v.Grafana.Alerts),
		v.Grafana.Err,
	)
	for _, w := range v.Warnings {
		t.Logf("warning: %s", w)
	}
}

// TestLiveAgents reads this machine's transcripts.
func TestLiveAgents(t *testing.T) {
	v := fetchAgents(agentWindow)
	t.Logf(
		"%s: %d transcripts in the window, %d sessions read",
		v.Host,
		v.Files,
		len(v.Sessions),
	)
	var tok int64
	for _, s := range v.Sessions {
		tok += s.Tokens()
	}
	t.Logf("%s tokens across them", humanCount(tok))
	for i, s := range v.Sessions {
		if i >= 5 {
			break
		}
		lim := contextLimit(s.Model)
		pct := 0.0
		if lim > 0 {
			pct = float64(s.Context) / float64(lim) * 100
		}
		t.Logf(
			"  %-16s %-10s turns=%-4d ctx=%7s (%3.0f%%) peak=%7s "+
				" +%d/-%d  $%.2f  %s",
			s.Project,
			shortModel(s.Model),
			s.Turns,
			humanCount(s.Context),
			pct,
			humanCount(
				s.Peak,
			),
			s.LinesAdded,
			s.LinesRemoved,
			s.CostUSD,
			humanSince(time.Since(s.LastWrite)),
		)
	}
	for _, w := range v.Warnings {
		t.Logf("warning: %s", w)
	}
}

// TestLiveThemes pulls the real dodo. It asserts the shape -- that the
// tokens parse into paintable colours -- not the hexes, which are dodo's to
// change and the entire reason this is pulled rather than transcribed.
func TestLiveThemes(t *testing.T) {
	themes, src, err := loadThemes("http://dodo.test")
	if err != nil {
		t.Logf("dodo: %v", err)
	}
	if len(themes) == 0 {
		t.Fatalf("no themes from dodo (source %q)", src)
	}
	t.Logf("%d themes from %s", len(themes), src)
	for _, th := range themes {
		t.Logf("  %-12s ground=%s ink=%s accent=%s light=%v",
			th.Name, th.Bg, th.Ink, th.Accent, th.Light)
		for label, c := range map[string]string{
			"Bg":     string(th.Bg),
			"Panel":  string(th.Panel),
			"Raised": string(th.Raised),
			"Ink":    string(th.Ink),
			"Dim":    string(th.Dim),
			"Accent": string(th.Accent),
		} {
			if !isHex(c) {
				t.Errorf(
					"%s.%s is %q, which is not paintable",
					th.Name,
					label,
					c,
				)
			}
		}
	}
	// the ones dodo actually serves today
	want := map[string]bool{
		"corvid":      false,
		"vaporwave":   false,
		"bladerunner": false,
		"stardew":     false,
	}
	for _, th := range themes {
		if _, ok := want[th.Name]; ok {
			want[th.Name] = true
		}
	}
	for n, found := range want {
		if !found {
			t.Errorf(
				"dodo serves no theme called %q any more -- "+
					"if that is deliberate, this test is "+
					"the notice",
				n,
			)
		}
	}
}

// TestLiveTail renders the CRT window against the newest real transcript.
// It asserts the window shows activity -- no raw json, nothing longer than a
// line -- because the whole point is a glance.
func TestLiveTail(t *testing.T) {
	v := fetchAgents(agentWindow)
	if len(v.Sessions) == 0 {
		t.Skip("no sessions in the window")
	}
	s := v.Sessions[0]
	lines, err := tailTranscript(s.Path, tailLines)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s (%s) %s:", s.Project, shortModel(s.Model), s.ID[:8])
	for _, l := range lines {
		t.Logf("  [%-6s] %s", l.Kind, l.Text)
		if strings.Contains(l.Text, `{"type"`) {
			t.Errorf("raw json in the window: %q", l.Text)
		}
		if strings.Contains(l.Text, "\n") {
			t.Errorf("a window line is not one line: %q", l.Text)
		}
	}
	if v.Tmux {
		t.Logf("tmux: %d sessions matched a pane", countMatched(v))
		for _, sess := range v.Sessions {
			if sess.Tmux.Target != "" {
				t.Logf(
					"  addressable: %s (%s) -> %s "+
						"running %s",
					sess.Project,
					shortModel(sess.Model),
					sess.Tmux.Target,
					sess.Tmux.Command,
				)
			}
			if sess.TmuxShared != "" {
				t.Logf(
					"  shares %s but is not typing in "+
						"it: %s (%s)",
					sess.TmuxShared,
					sess.Project,
					shortModel(sess.Model),
				)
			}
		}
	} else {
		t.Log("tmux: no server answered -- w and i will say so " +
			"rather than acting")
	}
}

// countMatched is how many sessions were joined to a tmux pane, which is
// the number that says whether the join worked at all.
func countMatched(v AgentView) int {
	n := 0
	for _, s := range v.Sessions {
		if s.Tmux.Target != "" {
			n++
		}
	}
	return n
}
