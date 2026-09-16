package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The metrics pane: prometheus, read by name, and made to speak names back.
//
// prometheus's own targets list identifies most of this estate as IP:port --
// `10.42.0.57:9100` -- which is precisely the identifier the house rule
// forbids. An address is not a name: it changes when the pod reschedules, it
// says nothing about who is behind it, and nobody can act on it.
//
// So the pane resolves pod IPs to pod names through kubectl before rendering,
// and shows the address only when nothing in the cluster claims it.
//
// What is NOT here on purpose: an arbitrary PromQL box. That is a real
// screen and it deserves its own tranche with a query history and a result
// table; a half of it bolted on here would be a worse version of `curl`.

// Target is one prometheus scrape target.
type Target struct {
	Job      string
	Instance string // as prometheus knows it: usually address:port
	// the resolved pod name, empty when nothing claims the address
	Name       string
	Namespace  string
	Health     string // up | down | unknown
	LastError  string
	LastScrape time.Time
	Duration   float64
}

// Addressed says whether this target is still only an address, which is the
// finding the pane exists to surface.
func (t Target) Addressed() bool { return t.Name == "" }

// MetricsView is the pane's data.
type MetricsView struct {
	Base     string
	Targets  []Target
	Alerts   []Alert
	Rules    int
	Grafana  GrafanaAlerting
	Warnings []string
}

// GrafanaAlerting is the other half of the alerting story, and the reason
// this struct exists at all: alerting in this estate lives in grafana, not
// in prometheus rules, so a pane that reads prometheus alone reports a
// confident zero about a system it never looked at.
//
// Anonymous is the field that matters. grafana here answers its alerting API
// without credentials but refuses /api/user, so an empty rule list has two
// readings -- there are none, or puffin is not allowed to see them -- and those
// are not the same fact.
//
// Reporting "0 rules" for the second one is how a screen earns distrust.
type GrafanaAlerting struct {
	Base      string
	Reachable bool
	Anonymous bool // puffin is unauthenticated: the list may be partial
	Rules     int
	Alerts    []Alert
	Err       string
}

// Alert is one firing or pending alert.
type Alert struct {
	Name     string
	State    string
	Severity string
	Summary  string
	Since    time.Time
}

// fetchMetricsFrom reads both alerting systems. grafanaBase empty means the
// grafana half is skipped, which is what the unit tests want.
func fetchMetricsFrom(base, grafanaBase, kubeCtx string) MetricsView {
	v := MetricsView{Base: base}
	if grafanaBase != "" {
		v.Grafana = fetchGrafanaAlerting(grafanaBase)
	}
	targets, err := promTargets(base)
	if err != nil {
		v.Warnings = append(
			v.Warnings,
			"prometheus targets: "+err.Error(),
		)
	}
	alerts, rules, err := promAlerts(base)
	if err != nil {
		v.Warnings = append(
			v.Warnings,
			"prometheus alerts: "+err.Error(),
		)
	}
	v.Alerts, v.Rules = alerts, rules
	// the naming pass: an address is not an identifier, so ask the cluster
	// who holds it before showing one to a human
	names, nerr := podsByIP(kubeCtx)
	if nerr != nil {
		v.Warnings = append(
			v.Warnings,
			"cannot resolve target addresses to names: "+
				nerr.Error(),
		)
	}
	for i := range targets {
		host := targets[i].Instance
		if c := strings.LastIndex(host, ":"); c > 0 {
			host = host[:c]
		}
		if p, ok := names[host]; ok {
			targets[i].Name, targets[i].Namespace = p.name, p.ns
		} else if !strings.Contains(host, ".") && !isIPish(host) {
			// already a name: a node, or localhost
			targets[i].Name = host
		} else if strings.HasSuffix(host, ".svc.cluster.local") {
			targets[i].Name = strings.SplitN(host, ".", 2)[0]
		}
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].Health != targets[j].Health {
			// the broken ones first
			return targets[i].Health == "down"
		}
		if targets[i].Job != targets[j].Job {
			return targets[i].Job < targets[j].Job
		}
		return targets[i].Instance < targets[j].Instance
	})
	v.Targets = targets
	return v
}

// promTarget is one scrape target as prometheus reports it. Named rather
// than written inline, because an anonymous struct three levels down costs
// three levels of indent for the field names that carry the meaning.
type promTarget struct {
	Labels     map[string]string `json:"labels"`
	Health     string            `json:"health"`
	LastError  string            `json:"lastError"`
	LastScrape time.Time         `json:"lastScrape"`
	Duration   float64           `json:"lastScrapeDuration"`
}

// promAlert is one firing or pending alert, as prometheus reports it.
type promAlert struct {
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	State       string            `json:"state"`
	ActiveAt    time.Time         `json:"activeAt"`
}

// promTargets reads the active scrape targets.
func promTargets(base string) ([]Target, error) {
	resp, err := client.Get(base + "/api/v1/targets?state=active")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var doc struct {
		Data struct {
			ActiveTargets []promTarget `json:"activeTargets"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, err
	}
	out := make([]Target, 0, len(doc.Data.ActiveTargets))
	for _, t := range doc.Data.ActiveTargets {
		out = append(out, Target{
			Job: t.Labels["job"], Instance: t.Labels["instance"],
			Health: t.Health, LastError: t.LastError,
			LastScrape: t.LastScrape, Duration: t.Duration,
		})
	}
	return out, nil
}

// promAlerts reads what is firing, and how many rules exist to fire at all.
// A prometheus with no rules alerts on nothing, which is worth saying out
// loud on the screen rather than rendering as a reassuring empty list.
func promAlerts(base string) ([]Alert, int, error) {
	resp, err := client.Get(base + "/api/v1/alerts")
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	var doc struct {
		Data struct {
			Alerts []promAlert `json:"alerts"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, 0, err
	}
	out := make([]Alert, 0, len(doc.Data.Alerts))
	for _, a := range doc.Data.Alerts {
		out = append(out, Alert{
			Name: a.Labels["alertname"], State: a.State,
			Severity: a.Labels["severity"],
			Summary:  a.Annotations["summary"],
			Since:    a.ActiveAt,
		})
	}
	return out, promRuleCount(base), nil
}

// promRuleCount counts loaded rules. A failure here is not worth a warning
// of its own -- it renders as "unknown" and the targets still show.
func promRuleCount(base string) int {
	resp, err := client.Get(base + "/api/v1/rules")
	if err != nil {
		return -1
	}
	defer resp.Body.Close()
	var doc struct {
		Data struct {
			Groups []struct {
				Rules []json.RawMessage `json:"rules"`
			} `json:"groups"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return -1
	}
	n := 0
	for _, g := range doc.Data.Groups {
		n += len(g.Rules)
	}
	return n
}

type podID struct{ name, ns string }

// podsByIP asks the cluster who holds each address.
func podsByIP(kubeCtx string) (map[string]podID, error) {
	out, err := exec.Command("kubectl", "--context", kubeCtx,
		"get", "pods", "-A", "-o", "json").Output()
	if err != nil {
		return nil, err
	}
	var doc struct {
		Items []struct {
			Metadata struct {
				Name      string            `json:"name"`
				Namespace string            `json:"namespace"`
				Labels    map[string]string `json:"labels"`
			} `json:"metadata"`
			Status struct {
				PodIP string `json:"podIP"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, err
	}
	m := map[string]podID{}
	for _, it := range doc.Items {
		if it.Status.PodIP == "" {
			continue
		}
		// the service name is the useful identifier, not the replica
		// hash
		name := it.Metadata.Labels["app.kubernetes.io/name"]
		if name == "" {
			name = it.Metadata.Name
		}
		m[it.Status.PodIP] = podID{
			name: name,
			ns:   it.Metadata.Namespace,
		}
	}
	return m, nil
}

// metricsFetched carries the read back into the loop.
type metricsFetched struct {
	v      MetricsView
	series []Series
	window time.Duration
}

type metricsPane struct {
	v      MetricsView
	cursor int
	offset int
	// fetch warnings are transient and say so by leaving. What is actually
	// wrong -- a target down, no alert rules, an unauthenticated grafana --
	// stays in the body where it cannot be missed.
	toast toast
	// the numbers themselves, which this pane did not have until now: it
	// read prometheus's own status and never asked it a question
	series []Series
	window time.Duration
	// the domain the pane was loaded with, so a key that re-asks does not
	// have to be handed one. A pane's Load is its only door to the world
	// and this is the doorknob.
	domain string
}

// Key opens the metrics pane from the roster.
func (p *metricsPane) Key() string { return "p" }

// Title names the pane, and keys its refresh gate.
func (p *metricsPane) Title() string { return "metrics" }

// window cycling: an hour is the default because it is the span in which a
// dev cluster's problems appear and go away again.
var metricWindows = []time.Duration{
	15 * time.Minute,
	time.Hour,
	6 * time.Hour,
	24 * time.Hour,
}

// Help is the footer: the queries, and how to widen the window.
func (p *metricsPane) Help() string {
	return "j/k: move · r: refresh · q: back   ·   prometheus, read by name"
}

// Load asks prometheus for values, not only for its own status. The
// domain decides which prometheus is asked.
func (p *metricsPane) Load(domain string) tea.Cmd {
	if domain != "" {
		p.domain = domain
	}
	domain = p.domain
	base := fmt.Sprintf("http://prometheus.%s", domain)
	graf := fmt.Sprintf("http://grafana.%s", domain)
	ctx := kubeContext()
	win := p.window
	if win == 0 {
		win = time.Hour
	}
	return func() tea.Msg {
		v := fetchMetricsFrom(base, graf, ctx)
		// the queries run against the same prometheus, in one pass, off
		// the main loop like every other fetch here
		var ss []Series
		for _, q := range defaultQueries() {
			ss = append(ss, promRange(base, q, win, 120))
		}
		return metricsFetched{v: v, series: ss, window: win}
	}
}

// Update folds in an answer, and keeps prometheus and grafana apart
// because they fail differently and one being down says nothing about
// the other.
func (p *metricsPane) Update(msg tea.Msg) (pane, tea.Cmd) {
	switch msg := msg.(type) {
	case metricsFetched:
		p.v = msg.v
		if msg.series != nil {
			p.series, p.window = msg.series, msg.window
		}
		if p.cursor >= len(p.v.Targets) {
			p.cursor = max(0, len(p.v.Targets)-1)
		}
		// a clean refresh takes the last failure down with it rather
		// than leaving it to time out
		return p, p.toast.show(strings.Join(p.v.Warnings, " · "))
	case toastExpired:
		return p, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "down", "j":
			if p.cursor < len(p.v.Targets)-1 {
				p.cursor++
			}
		case "up", "k":
			if p.cursor > 0 {
				p.cursor--
			}
		case "w":
			// cycle the window and re-ask. The same series over
			// fifteen minutes and over a day are different
			// questions -- one says what is happening, the other
			// says whether it always does.
			cur := p.window
			next := metricWindows[0]
			for i, w := range metricWindows {
				if w == cur {
					next = metricWindows[(i+1)%len(
						metricWindows,
					)]
				}
			}
			p.window = next
			return p, p.Load("")
		}
	}
	return p, nil
}

// View draws a value with a sparkline beside it, because a number with no
// history answers neither "of what" nor "since when".
func (p *metricsPane) View(s Styles, width, height int) string {
	var b strings.Builder
	if len(p.v.Targets) == 0 {
		// nothing came back at all: that is state, not a hiccup, and it
		// stays on the screen until it is not true any more
		if len(p.v.Warnings) > 0 {
			b.WriteString(
				s.Warn.Render(
					"no targets: "+strings.Join(
						p.v.Warnings,
						" · ",
					),
				) + "\n",
			)
		} else {
			b.WriteString(s.Dim.Render("asking "+
				p.v.Base+"...") + "\n")
		}
		return b.String()
	}
	// the numbers first. What prometheus is scraping is plumbing; what the
	// estate is doing is the reason the screen is open.
	if len(p.series) > 0 {
		b.WriteString(
			s.Header.Render(
				"over the last "+humanSince(p.window),
			) + "\n",
		)
		for _, ser := range p.series {
			name := s.Row.Render(pad(ser.Query.Name, 24))
			switch {
			case ser.Err != "":
				b.WriteString(
					"  " + name + s.Caution.Render(
						ser.Err,
					) + "\n",
				)
				continue
			case len(ser.Points) == 0:
				// not an error and not a zero: prometheus has
				// never heard of this metric here, which is
				// usually a missing exporter
				b.WriteString(
					"  " + name + s.Dim.Render(
						"no data · is the exporter "+
							"scraped?",
					) + "\n",
				)
				continue
			}
			v, _ := ser.Last()
			lo, hi := ser.Range()
			val := formatValue(v, ser.Query.Unit)
			style := s.Ok
			if ser.Query.Unit == "pct" && v > 0.85 {
				style = s.Warn
			} else if ser.Query.Unit == "count" && v > 0 {
				style = s.Caution
			}
			b.WriteString("  " + name +
				s.AccentAlt.Render(
					sparkline(ser.Points, 40),
				) + "  " +
				style.Render(pad(val, 10)) +
				s.Dim.Render(
					formatValue(lo, ser.Query.Unit)+" – "+
						formatValue(hi, ser.Query.Unit),
				) + "\n")
		}
		b.WriteString("\n")
	}

	up, down, unnamed := 0, 0, 0
	for _, t := range p.v.Targets {
		switch t.Health {
		case "up":
			up++
		case "down":
			down++
		}
		if t.Addressed() {
			unnamed++
		}
	}
	b.WriteString(
		s.Ok.Render(fmt.Sprintf("%d up", up)) + s.Dim.Render(" · ") +
			s.Warn.Render(
				fmt.Sprintf("%d down", down),
			) + s.Dim.Render(" · ") +
			s.Dim.Render(
				fmt.Sprintf("%d targets", len(p.v.Targets)),
			) + "\n",
	)

	// alerting is reported per system, because they fail differently: a
	// prometheus with no rules alerts on nothing, and a grafana puffin
	// cannot authenticate to reports nothing whether or not it has rules
	b.WriteString(
		alertingLine(
			s,
			"prometheus",
			p.v.Rules,
			len(p.v.Alerts),
			false,
		),
	)
	if p.v.Grafana.Base != "" {
		g := p.v.Grafana
		switch {
		case !g.Reachable:
			b.WriteString(
				s.Caution.Render(
					"grafana: unreachable -- "+g.Err,
				) + "\n",
			)
		default:
			b.WriteString(
				alertingLine(
					s,
					"grafana",
					g.Rules,
					len(g.Alerts),
					g.Anonymous,
				),
			)
		}
	}
	for _, a := range append(
		append([]Alert{}, p.v.Alerts...),
		p.v.Grafana.Alerts...,
	) {
		b.WriteString(
			"  " + s.Warn.Render(
				pad(a.Name, 24),
			) + s.Dim.Render(
				a.Summary,
			) + "\n",
		)
	}
	if unnamed > 0 {
		b.WriteString(s.Caution.Render(fmt.Sprintf(
			"%d targets are only an address: prometheus scrapes "+
				"them by IP and nothing in the cluster "+
				"claims it",
			unnamed,
		)) + "\n")
	}
	b.WriteString(
		"\n" + s.Header.Render(
			pad("target", 26)+pad("job", 18)+pad("ns", 12)+
				pad("health", 8)+"last scrape",
		) + "\n",
	)

	size := height - 16
	if height <= 0 {
		size = len(p.v.Targets)
	} else if size < 3 {
		size = 3
	}
	off := windowOffset(len(p.v.Targets), p.cursor, size, p.offset)
	p.offset = off
	last := off + size
	if last > len(p.v.Targets) {
		last = len(p.v.Targets)
	}
	if off > 0 {
		b.WriteString(
			s.Dim.Render(
				fmt.Sprintf("  %d more above", off),
			) + "\n",
		)
	}
	now := time.Now()
	for i := off; i < last; i++ {
		t := p.v.Targets[i]
		on := i == p.cursor
		marker := s.On(s.Row, on).Render("  ")
		if on {
			marker = s.On(s.RowSel, on).Render("▸ ")
		}
		name := t.Name
		style := s.Row
		if t.Addressed() {
			name = t.Instance
			// an address is shown dimmed: it is not an identifier
			style = s.Dim
		}
		healthStyle := s.Ok
		if t.Health != "up" {
			healthStyle = s.Warn
		}
		scrape := "-"
		if !t.LastScrape.IsZero() {
			scrape = humanSince(now.Sub(t.LastScrape))
		}
		b.WriteString(marker + s.On(style, on).Render(pad(name, 26)) +
			s.On(s.Dim, on).Render(pad(t.Job, 18)) +
			s.On(s.Dim, on).Render(pad(orDash(t.Namespace), 12)) +
			s.On(healthStyle, on).Render(pad(t.Health, 8)) +
			s.On(s.Dim, on).Render(padTo(scrape, 12)) + "\n")
	}
	if last < len(p.v.Targets) {
		b.WriteString(
			s.Dim.Render(
				fmt.Sprintf(
					"  %d more below",
					len(p.v.Targets)-last,
				),
			) + "\n",
		)
	}
	if t := p.v.Targets[p.cursor]; t.LastError != "" {
		b.WriteString("\n  " + s.Warn.Render(t.LastError) + "\n")
	}
	b.WriteString(p.toast.view(s))
	return b.String()
}

// fetchGrafanaAlerting reads grafana's unified alerting through its
// prometheus-compatible endpoints, and finds out whether it is being trusted
// with the whole answer.
func fetchGrafanaAlerting(base string) GrafanaAlerting {
	g := GrafanaAlerting{Base: base}
	resp, err := client.Get(base + "/api/prometheus/grafana/api/v1/rules")
	if err != nil {
		g.Err = err.Error()
		return g
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		g.Reachable, g.Anonymous, g.Rules = true, true, -1
		return g
	}
	var doc struct {
		Data struct {
			Groups []struct {
				Rules []struct {
					Name        string            `json:"name"`
					State       string            `json:"state"`
					Labels      map[string]string `json:"labels"`
					Annotations map[string]string `json:"annotations"`
					Alerts      []struct {
						State    string            `json:"state"`
						ActiveAt time.Time         `json:"activeAt"`
						Labels   map[string]string `json:"labels"`
					} `json:"alerts"`
				} `json:"rules"`
			} `json:"groups"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		g.Err = err.Error()
		return g
	}
	g.Reachable = true
	for _, grp := range doc.Data.Groups {
		for _, r := range grp.Rules {
			g.Rules++
			ann := r.Annotations
			for _, a := range r.Alerts {
				if a.State == "firing" ||
					a.State == "alerting" ||
					a.State == "pending" {
					g.Alerts = append(g.Alerts, Alert{
						Name: r.Name, State: a.State,
						Severity: r.Labels["severity"],
						Summary:  ann["summary"],
						Since:    a.ActiveAt,
					})
				}
			}
		}
	}
	// grafana here serves alerting anonymously but refuses /api/user. An
	// empty list from an unauthenticated caller is not evidence of an empty
	// system, so the pane finds out which one it is instead of assuming.
	g.Anonymous = !grafanaAuthenticated(base)
	return g
}

// grafanaAuthenticated asks grafana whether it knows who puffin is.
func grafanaAuthenticated(base string) bool {
	resp, err := client.Get(base + "/api/user")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

// alertingLine states one alerting system's posture in one line, and never
// reports a zero it cannot stand behind.
func alertingLine(
	s Styles,
	name string,
	rules, firing int,
	anonymous bool,
) string {
	switch {
	case anonymous && rules <= 0:
		return s.Caution.Render(
			name+": puffin is not authenticated -- rules it "+
				"cannot see are not rules it can say are "+
				"absent",
		) + "\n"
	case rules < 0:
		return s.Caution.Render(name+": rule count unknown") + "\n"
	case rules == 0:
		return s.Caution.Render(
			name+": NO ALERT RULES -- nothing here can fire",
		) + "\n"
	case firing == 0 && anonymous:
		return s.Dim.Render(
			fmt.Sprintf(
				"%s: %d rules visible, none firing "+
					"(unauthenticated: may be partial)",
				name,
				rules,
			),
		) + "\n"
	case firing == 0:
		return s.Ok.Render(
			fmt.Sprintf("%s: %d rules, none firing", name, rules),
		) + "\n"
	default:
		return s.Warn.Render(
			fmt.Sprintf(
				"%s: %d FIRING of %d rules",
				name,
				firing,
				rules,
			),
		) + "\n"
	}
}
