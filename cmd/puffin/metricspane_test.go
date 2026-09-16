package main

import (
	"strings"
	"testing"
	"time"
)

// The metrics pane's job is to be believed. Every test here is about a
// number the screen is not entitled to state: a zero it cannot stand behind,
// an empty series read as nought, a target that is only an address.

// metricsFixture is a pane already holding values, so the tests exercise
// the drawing rather than prometheus.
func metricsFixture() *metricsPane {
	now := time.Now()
	return &metricsPane{
		window: time.Hour,
		v: MetricsView{
			Base:  "http://prometheus.test",
			Rules: 4,
			Targets: []Target{
				{
					Job:        "kubernetes-pods",
					Instance:   "10.42.0.9:9800",
					Name:       "flipr",
					Namespace:  "flipr",
					Health:     "up",
					LastScrape: now.Add(-12 * time.Second),
				},
				{
					Job:        "kubernetes-pods",
					Instance:   "10.42.0.7:9100",
					Name:       "",
					Health:     "up",
					LastScrape: now.Add(-30 * time.Second),
				},
				{
					Job:        "kafka",
					Instance:   "10.42.0.4:9092",
					Name:       "kafka-0",
					Namespace:  "local",
					Health:     "down",
					LastError:  "connection refused",
					LastScrape: now.Add(-time.Minute),
				},
			},
		},
		series: []Series{
			{
				Query: Query{
					Name: "pods restarting",
					Unit: "count",
				},
				Points: []float64{0, 0, 2},
			},
			{
				Query:  Query{Name: "cpu", Unit: "pct"},
				Points: []float64{0.2, 0.5, 0.91},
			},
			{
				Query:  Query{Name: "disk free", Unit: "bytes"},
				Points: []float64{4e9, 3e9},
			},
			{Query: Query{Name: "bus lag", Unit: "count"}},
			{
				Query: Query{Name: "broken", Unit: "count"},
				Err:   "bad query",
			},
		},
	}
}

// The pane before anything came back says what it is asking, not "0 up".
func TestMetricsPaneEmptyStates(t *testing.T) {
	p := &metricsPane{v: MetricsView{Base: "http://prometheus.test"}}
	out := p.View(Compile(DodoDark()), 120, 40)
	if !strings.Contains(out, "asking http://prometheus.test") {
		t.Fatalf(
			"a pane with nothing yet did not say what it was "+
				"asking: %q",
			out,
		)
	}
	if strings.Contains(out, "0 up") {
		t.Error("the pane stated a count it does not have")
	}
	// and once there is a reason, the reason stays on screen
	p.v.Warnings = []string{"dial tcp: connection refused"}
	out = p.View(Compile(DodoDark()), 120, 40)
	if !strings.Contains(out, "no targets: dial tcp") {
		t.Fatal("the pane hid why it has nothing")
	}
}

// The numbers come first. What prometheus scrapes is plumbing; what the
// estate is doing is why the screen is open.
func TestMetricsPaneDrawsSeries(t *testing.T) {
	out := metricsFixture().View(Compile(DodoDark()), 160, 44)
	if !strings.Contains(out, "over the last 1h") {
		t.Error("the pane did not say what span the numbers cover")
	}
	for _, want := range []string{"pods restarting", "cpu", "disk free"} {
		if !strings.Contains(out, want) {
			t.Errorf("series %q is missing", want)
		}
	}
	// an empty series is a real answer and must not read as zero
	if !strings.Contains(out, "no data · is the exporter scraped?") {
		t.Error("an empty series was rendered as a value")
	}
	// a failed query says so rather than disappearing
	if !strings.Contains(out, "bad query") {
		t.Error("a query error was swallowed")
	}
}

// A target that is only an address is the finding the pane exists to
// surface, and it is stated in words rather than left to be noticed.
func TestMetricsPaneCallsOutAddressedTargets(t *testing.T) {
	out := metricsFixture().View(Compile(DodoDark()), 160, 44)
	if !strings.Contains(out, "1 targets are only an address") {
		t.Fatal("an unclaimed address was not called out")
	}
	if !strings.Contains(out, "10.42.0.7:9100") {
		t.Error(
			"the address itself is not shown, so nobody can " +
				"chase it",
		)
	}
	if !strings.Contains(out, "2 up") || !strings.Contains(out, "1 down") {
		t.Error("the up/down tally is wrong or missing")
	}
	// the selected row's error is spelled out under the table
	p := metricsFixture()
	p.cursor = 2
	if !strings.Contains(
		p.View(Compile(DodoDark()), 160, 44),
		"connection refused",
	) {
		t.Error("the selected target's last error is not shown")
	}
}

// alertingLine is the pane's conscience: six postures, and none of them is
// a zero the screen cannot stand behind.
func TestAlertingLineNeverOverstates(t *testing.T) {
	s := Compile(DodoDark())
	for _, tc := range []struct {
		name              string
		rules, firing     int
		anonymous         bool
		want, mustNotWant string
	}{
		{"unauthenticated and " +
			"empty", 0, 0, true, "not " +
			"authenticated", "NO ALERT RULES"},
		{"count unknown", -1, 0, false, "rule count unknown", ""},
		{"genuinely none", 0, 0, false, "NO ALERT RULES", ""},
		{"quiet but partial", 3, 0, true, "may be partial", ""},
		{"quiet and complete", 3, 0, false, "3 rules, none " +
			"firing", "partial"},
		{"firing", 3, 2, false, "2 FIRING of 3 rules", ""},
	} {
		got := alertingLine(
			s,
			"grafana",
			tc.rules,
			tc.firing,
			tc.anonymous,
		)
		if !strings.Contains(got, tc.want) {
			t.Errorf(
				"%s: %q does not say %q",
				tc.name,
				got,
				tc.want,
			)
		}
		if tc.mustNotWant != "" &&
			strings.Contains(got, tc.mustNotWant) {
			t.Errorf(
				"%s: %q wrongly says %q",
				tc.name,
				got,
				tc.mustNotWant,
			)
		}
	}
}

// Alerting is reported per system because they fail differently, and a
// grafana puffin cannot reach is not a grafana with no alerts.
func TestMetricsPaneReportsBothAlertingSystems(t *testing.T) {
	p := metricsFixture()
	p.v.Alerts = []Alert{
		{Name: "KafkaLagGrowing", Summary: "consumer group is behind"},
	}
	p.v.Grafana = GrafanaAlerting{
		Base:      "http://grafana.test",
		Reachable: false,
		Err:       "no route to host",
	}
	out := p.View(Compile(DodoDark()), 160, 44)
	if !strings.Contains(out, "prometheus: 1 FIRING of 4 rules") {
		t.Error("prometheus alerting not reported")
	}
	if !strings.Contains(out, "grafana: unreachable -- no route to host") {
		t.Error("an unreachable grafana was reported as quiet")
	}
	if !strings.Contains(out, "KafkaLagGrowing") ||
		!strings.Contains(out, "consumer group is behind") {
		t.Error("a firing alert is not on screen")
	}
	// a reachable grafana gets its own posture line
	p.v.Grafana = GrafanaAlerting{
		Base:      "http://grafana.test",
		Reachable: true,
		Anonymous: true,
		Rules:     0,
	}
	if out := p.View(Compile(DodoDark()), 160, 44); !strings.Contains(
		out,
		"grafana: puffin is not authenticated",
	) {
		t.Error("an unauthenticated grafana claimed to have no rules")
	}
}

// The table scrolls, and says how much is off screen in each direction --
// otherwise a target that is down can be invisible with no sign of it.
func TestMetricsPaneSaysWhatIsOffScreen(t *testing.T) {
	p := metricsFixture()
	for i := 0; i < 40; i++ {
		p.v.Targets = append(
			p.v.Targets,
			Target{Job: "j", Instance: "10.0.0.1:1", Name: "svc",
				Health: "up", LastScrape: time.Now()},
		)
	}
	p.cursor = len(p.v.Targets) - 1
	out := p.View(Compile(DodoDark()), 160, 30)
	if !strings.Contains(out, "more above") {
		t.Error("the pane did not say rows were scrolled off the top")
	}
	p.cursor = 0
	p.offset = 0
	if out := p.View(Compile(DodoDark()), 160, 30); !strings.Contains(
		out,
		"more below",
	) {
		t.Error("the pane did not say rows were below the fold")
	}
}

// Moving between queries and widening the window are the only keys, and
// neither may refetch what is already in hand.
func TestMetricsPaneKeys(t *testing.T) {
	p := metricsFixture()
	p.Update(key2('j'))
	if p.cursor != 1 {
		t.Fatalf("j: %d", p.cursor)
	}
	for i := 0; i < 5; i++ {
		p.Update(key2('j'))
	}
	if p.cursor != len(p.v.Targets)-1 {
		t.Fatalf("j past the end: %d", p.cursor)
	}
	for i := 0; i < 5; i++ {
		p.Update(key2('k'))
	}
	if p.cursor != 0 {
		t.Fatalf("k past the start: %d", p.cursor)
	}

	// w cycles the span and re-asks: the same series over fifteen minutes
	// and over a day are different questions
	p.domain = "test"
	seen := map[time.Duration]bool{}
	for range metricWindows {
		_, cmd := p.Update(key2('w'))
		if cmd == nil {
			t.Fatal("w did not re-ask")
		}
		seen[p.window] = true
	}
	if len(seen) != len(metricWindows) {
		t.Fatalf("w visited %v, want all of %v", seen, metricWindows)
	}
	// an unrecognised window lands back on the first step rather than
	// sticking
	p.window = 7 * time.Minute
	p.Update(key2('w'))
	if p.window != metricWindows[0] {
		t.Fatalf("window %v", p.window)
	}
}

// A read that comes back shorter must not leave the cursor past the end, and
// a clean refresh takes the last failure down with it.
func TestMetricsPaneFetchClampsAndClearsWarnings(t *testing.T) {
	p := metricsFixture()
	p.cursor = 2
	p.Update(
		metricsFetched{
			v: MetricsView{Warnings: []string{"grafana timed out"}},
		},
	)
	if p.cursor != 0 {
		t.Fatalf("cursor %d after an empty read", p.cursor)
	}
	if !strings.Contains(
		p.View(Compile(DodoDark()), 160, 44),
		"grafana timed out",
	) {
		t.Error("a fetch warning did not reach the screen")
	}
	// and the series survive a read that carries none, rather than the
	// numbers blanking on a refresh that only re-read the targets
	p = metricsFixture()
	before := len(p.series)
	p.Update(metricsFetched{v: p.v})
	if len(p.series) != before {
		t.Fatalf(
			"series went from %d to %d on a read that carried none",
			before,
			len(p.series),
		)
	}
	p.Update(toastExpired{})
}

// A refresh has to go back to the same prometheus. A pane that forgets
// its domain answers from a different cluster on the second read.
func TestMetricsPaneLoadRemembersItsDomain(t *testing.T) {
	p := &metricsPane{}
	if cmd := p.Load("test"); cmd == nil {
		t.Fatal("Load fetched nothing")
	}
	if p.domain != "test" {
		t.Fatalf("domain %q", p.domain)
	}
	// a key that re-asks does not have to be handed the domain again
	p.Load("")
	if p.domain != "test" {
		t.Fatalf("domain %q after a re-ask", p.domain)
	}
}

// An empty series must never read as zero, and the range is what makes a
// number mean something: 40% of what, since when.
func TestSeriesLastAndRange(t *testing.T) {
	var empty Series
	if v, ok := empty.Last(); ok || v != 0 {
		t.Errorf("an empty series answered %v, %v", v, ok)
	}
	if lo, hi := empty.Range(); lo != 0 || hi != 0 {
		t.Errorf("an empty range: %v %v", lo, hi)
	}
	s := Series{Points: []float64{0.4, 0.1, 0.9, 0.5}}
	if v, ok := s.Last(); !ok || v != 0.5 {
		t.Errorf("last %v %v", v, ok)
	}
	if lo, hi := s.Range(); lo != 0.1 || hi != 0.9 {
		t.Errorf("range %v-%v", lo, hi)
	}
}

// The units decide the formatting, not the maths. An unknown unit prints the
// number, which is never wrong, only unhelpful.
func TestFormatValue(t *testing.T) {
	for _, tc := range []struct {
		v    float64
		unit string
		want string
	}{
		{0.25, "pct", "25.0%"},
		{3, "count", "3"},
		{3.5, "count", "3.5"},
		{1536, "bytes", "1.5 KiB"},
		{90, "s", "1m30s"},
		{2.5, "", "2.5"},
	} {
		if got := formatValue(tc.v, tc.unit); got != tc.want {
			t.Errorf(
				"formatValue(%v, %q) = %q, want %q",
				tc.v,
				tc.unit,
				got,
				tc.want,
			)
		}
	}
}

// A sparkline answers "how much" and "since when" in the same width, and it
// must not draw a shape that is not there.
func TestSparkline(t *testing.T) {
	// no data draws a placeholder of the right width rather than nothing:
	// an empty column and a missing column are different facts
	if got := sparkline(nil, 10); got != strings.Repeat("·", 10) {
		t.Errorf("an empty series drew %q", got)
	}
	if got := sparkline([]float64{1, 2}, 0); got != "" {
		t.Errorf("a zero-width sparkline drew %q", got)
	}
	// only the recent past: averaging into buckets hides the spike that was
	// the reason to look
	if got := []rune(sparkline([]float64{9, 9, 9, 0, 1, 2}, 3)); len(
		got,
	) != 3 {
		t.Errorf("width: %q", string(got))
	}
	// a flat series is flat: no invented peaks from dividing by a zero
	// range
	flat := sparkline([]float64{5, 5, 5, 5}, 4)
	if len([]rune(flat)) != 4 {
		t.Fatalf("width: %q", flat)
	}
	if strings.Count(flat, string([]rune(flat)[0])) != 4 {
		t.Errorf("a flat series drew a shape: %q", flat)
	}
	// a rising series ends higher than it starts
	rise := []rune(sparkline([]float64{0, 1, 2, 3, 4, 5, 6, 7}, 8))
	if rise[0] >= rise[len(rise)-1] {
		t.Errorf("a rising series did not rise: %q", string(rise))
	}
}

// A monitor that manufactures load is a monitor nobody keeps open, so the
// default set stays small and every query is named.
func TestDefaultQueriesAreFewAndNamed(t *testing.T) {
	qs := defaultQueries()
	if len(qs) == 0 {
		t.Fatal("no default queries")
	}
	// a dashboard with forty panels is one nobody reads
	if len(qs) > 12 {
		t.Errorf(
			"%d default queries: this is a dashboard nobody reads",
			len(qs),
		)
	}
	for _, q := range qs {
		if q.Name == "" || q.Expr == "" {
			t.Errorf(
				"query %+v is missing a name or an expression",
				q,
			)
		}
	}
}

// Nothing may render outside the terminal, at any size.
func TestMetricsPaneDrawsInATinyWindow(t *testing.T) {
	p := metricsFixture()
	for _, wh := range [][2]int{{40, 6}, {20, 0}, {200, 60}} {
		if out := p.View(Compile(DodoDark()), wh[0], wh[1]); out == "" {
			t.Errorf("%dx%d drew nothing", wh[0], wh[1])
		}
	}
}
