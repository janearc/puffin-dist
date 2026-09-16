package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Actually asking prometheus what the numbers are.
//
// The metrics pane read /targets and /rules, so it could tell you which
// exporters prometheus was scraping and whether anything was alerting. It never
// asked for a value.
//
// It was a monitoring-system status page, not a monitoring screen, and those
// are different things: "nine targets up" is a statement about prometheus, not
// about the cluster.
//
// The unit here is a Query: a name, an expression, and how to read the
// result. Range queries rather than instant ones, because a number with no
// history is the thing that makes a dashboard useless -- 40% of what? Since
// when? A sparkline answers both in the same width.

// Query is one line on the metrics screen.
type Query struct {
	Name string
	Expr string
	// Unit decides the formatting, not the maths: "pct" for a 0-1 ratio,
	// "bytes", "count", "s". An unknown unit prints the number, which is
	// never wrong, only unhelpful.
	Unit string
}

// Series is what came back: values in time order, oldest first.
type Series struct {
	Query  Query
	Points []float64
	Err    string
}

// Last is the current value, and the second return says whether there is one
// -- an empty series is a real answer and must not read as zero.
func (s Series) Last() (float64, bool) {
	if len(s.Points) == 0 {
		return 0, false
	}
	return s.Points[len(s.Points)-1], true
}

// Range says how far the series moved, for the screens that care more about
// the shape than the value.
func (s Series) Range() (lo, hi float64) {
	if len(s.Points) == 0 {
		return 0, 0
	}
	lo, hi = s.Points[0], s.Points[0]
	for _, v := range s.Points {
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	return lo, hi
}

// defaultQueries are what the dev network is actually asked about.
//
// Deliberately few. A dashboard with forty panels is one nobody reads, and
// the estate's own rule applies here harder than anywhere: a metric is a
// floor or a tripwire, never a goal. These are the tripwires -- is anything
// restarting, is anything wedged, is the disk going, is the bus backing up.
func defaultQueries() []Query {
	return []Query{
		{Name: "pods restarting", Unit: "count",
			Expr: `sum(increase(kube_pod_container_status_restart` +
				`s_total[15m]))`},
		{Name: "pods not ready", Unit: "count",
			Expr: `sum(kube_pod_status_ready{condition="false"})`},
		{Name: "node cpu", Unit: "pct",
			Expr: `1 - avg(rate(node_cpu_seconds_total{mode="idle` +
				`"}[5m]))`},
		{Name: "node memory used", Unit: "pct",
			Expr: `1 - (node_memory_MemAvailable_bytes / node_mem` +
				`ory_MemTotal_bytes)`},
		{Name: "root filesystem used", Unit: "pct",
			Expr: `1 - (node_filesystem_avail_bytes{mountpoint="/` +
				`"} / node_filesystem_size_bytes{mountpoint="` +
				`/"})`},
	}
}

// promRange runs one range query over the last window.
func promRange(base string, q Query, window time.Duration, points int) Series {
	s := Series{Query: q}
	if points < 2 {
		points = 2
	}
	end := time.Now()
	start := end.Add(-window)
	step := window / time.Duration(points)
	if step < time.Second {
		step = time.Second
	}
	u := fmt.Sprintf(
		"%s/api/v1/query_range?query=%s&start=%d&end=%d&step=%d",
		strings.TrimRight(base, "/"),
		url.QueryEscape(q.Expr),
		start.Unix(),
		end.Unix(),
		int(step.Seconds()),
	)

	resp, err := client.Get(u)
	if err != nil {
		s.Err = err.Error()
		return s
	}
	body, _ := readCapped(resp)
	var doc struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Result []struct {
				Values [][]any `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		s.Err = "prometheus did not answer as prometheus: " +
			"" + err.Error()
		return s
	}
	if doc.Status != "success" {
		// prometheus says WHY a query failed and the message is the
		// useful part -- a parse error names the character it stopped
		// at
		s.Err = orDash(doc.Error)
		return s
	}
	if len(doc.Data.Result) == 0 {
		// no series is a real answer: the metric does not exist here
		return s
	}
	for _, v := range doc.Data.Result[0].Values {
		if len(v) != 2 {
			continue
		}
		str, ok := v[1].(string)
		if !ok {
			continue
		}
		f, err := strconv.ParseFloat(str, 64)
		if err != nil || math.IsNaN(f) {
			continue
		}
		s.Points = append(s.Points, f)
	}
	return s
}

// sparkline draws a series in one row of text.
//
// Scaled to the series' OWN range rather than to zero, which is the choice
// that matters: a memory graph pinned between 61% and 63% is a flat line
// against a zero baseline and a visible drift against its own. What the
// screen is for is noticing change.
//
// A flat series draws as a flat line at mid height rather than at the
// bottom, so "steady" and "zero" do not look the same.
func sparkline(points []float64, width int) string {
	const ramp = " ▁▂▃▄▅▆▇█"
	if width < 1 {
		return ""
	}
	if len(points) == 0 {
		return strings.Repeat("·", width)
	}
	// take the last `width` points: the recent past is what a one-line
	// graph is for, and averaging them into buckets hides the spike that
	// was the reason to look
	if len(points) > width {
		points = points[len(points)-width:]
	}
	lo, hi := points[0], points[0]
	for _, v := range points {
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	glyphs := []rune(ramp)
	var b strings.Builder
	for _, v := range points {
		if hi == lo {
			b.WriteRune(glyphs[len(glyphs)/2])
			continue
		}
		i := int((v - lo) / (hi - lo) * float64(len(glyphs)-1))
		b.WriteRune(glyphs[clampInt(i, 0, len(glyphs)-1)])
	}
	return b.String()
}

// formatValue renders one number the way its unit wants to be read.
func formatValue(v float64, unit string) string {
	switch unit {
	case "pct":
		return fmt.Sprintf("%.1f%%", v*100)
	case "bytes":
		return humanBytes(int64(v))
	case "s":
		return (time.Duration(v * float64(time.Second))).Round(
			time.Millisecond,
		).
			String()
	case "count":
		if v == math.Trunc(v) {
			return strconv.FormatFloat(v, 'f', 0, 64)
		}
		return fmt.Sprintf("%.1f", v)
	default:
		return strconv.FormatFloat(v, 'g', 4, 64)
	}
}
