package main

import (
	"testing"
	"time"
)

// The collector's records, verbatim from the pod. A parser test written
// against a record someone typed from memory tests the memory; these were
// tailed out of /fleet-logs on the day of the cutover.
const (
	vectorLine = `{"container":"ingestd","cri_flag":"F",` +
		`"cri_stream":"stdout",` +
		`"cri_ts":"2026-08-30T23:54:38.864053211Z",` +
		`"log":{"file":{"path":"/var/log/containers/kingfisher-ingest` +
		`d-79f7994cd7-f9pxr_local_ingestd-929c310395650b41f050ecf5253` +
		`67e2cba364f8224c72be628353f08e59b3b7f.log"}},` +
		`"message":"2026-08-30T23:54:38.864053211Z stdout F {\"level\` +
		`": \"info\", \"event\": \"no_pipeline_yet\"}",` +
		`"namespace":"local","payload":"{\"level\": \"info\",` +
		` \"event\": \"no_pipeline_yet\"}",` +
		`"pod":"kingfisher-ingestd-79f7994cd7-f9pxr",` +
		`"svc":{"event":"no_pipeline_yet","level":"info"},` +
		`"timestamp":"2026-08-30T23:54:38.923172139Z"}`

	// what logstash wrote, which retention keeps on the same mount for
	// seven days after the swap
	logstashLine = `{"@timestamp":"2026-08-29T10:11:12.130000000Z",` +
		`"cri_stream":"stderr",` +
		`"message":"2026-08-29T10:11:12.13Z stderr F boom",` +
		`"log":{"file":{"path":"/var/log/containers/athlete-59c5b49dc` +
		`7-wv6f5_local_athlete-abc123.log"}}}`
)

// TestParseVectorLine is the port's actual claim: the collector changed and
// the pane still reads a line the same way.
func TestParseVectorLine(t *testing.T) {
	l, ok := parseLogLine(vectorLine)
	if !ok {
		t.Fatal("a live vector record did not parse")
	}
	if l.Service != "kingfisher-ingestd" {
		t.Errorf("service: got %q want kingfisher-ingestd", l.Service)
	}
	if l.Pod != "kingfisher-ingestd-79f7994cd7-f9pxr" || l.NS != "local" {
		t.Errorf("pod/ns: got %q %q", l.Pod, l.NS)
	}
	if l.Stream != "stdout" {
		t.Errorf("stream: got %q", l.Stream)
	}
	// the CRI framing is the collector's, not the service's, and vector has
	// already taken it off in payload
	if want := `{"level": "info",` +
		` "event": "no_pipeline_yet"}`; l.Text != want {
		t.Errorf("text: got %q want %q", l.Text, want)
	}
}

// TestParseUsesContainerTime guards the failure that made this a port rather
// than a label change.
//
// vector writes no @timestamp, so the old parser gave every line the zero time:
// the pane sorted on nothing, rendered 00:00:00, and no watch could ever fire
// again -- four wrong behaviours, none of them an error.
//
// And cri_ts rather than vector's `timestamp`, because the latter is when the
// collector read the line: after a restart it re-reads history and stamps a
// thousand old lines with one second.
func TestParseUsesContainerTime(t *testing.T) {
	l, ok := parseLogLine(vectorLine)
	if !ok {
		t.Fatal("did not parse")
	}
	if l.When.IsZero() {
		t.Fatal(
			"no timestamp: the pane cannot sort, stamp or watch " +
				"this line",
		)
	}
	want, _ := time.Parse(
		time.RFC3339Nano,
		"2026-08-30T23:54:38.864053211Z",
	)
	if !l.When.Equal(want) {
		t.Errorf(
			"when: got %s want %s (cri_ts, not vector's read time)",
			l.When,
			want,
		)
	}
}

// TestParseLogstashLine is the seven days of files already on the mount.
func TestParseLogstashLine(t *testing.T) {
	l, ok := parseLogLine(logstashLine)
	if !ok {
		t.Fatal("a logstash-era record did not parse")
	}
	if l.Service != "athlete" || l.Pod != "athlete-59c5b49dc7-wv6f5" ||
		l.NS != "local" {
		t.Errorf("path fallback: got %q %q %q", l.Service, l.Pod, l.NS)
	}
	if l.Stream != "stderr" || l.Text != "boom" {
		t.Errorf("got stream %q text %q", l.Stream, l.Text)
	}
	if l.When.IsZero() {
		t.Error("@timestamp is still a timestamp")
	}
}

// TestParseDissectFailure is the collector keeping what it could not parse.
// The manifest is deliberate about this -- a collector that drops what it
// cannot read is a collector with an opinion -- so the pane must show the
// line rather than quietly agreeing to lose it.
func TestParseDissectFailure(t *testing.T) {
	raw := `{"message":"a line with no CRI envelope",` +
		`"timestamp":"2026-08-30T23:54:38.923172139Z",` +
		`"tags":["_cri_dissect_failure"],` +
		`"log":{"file":{"path":"/var/log/containers/hm-5bf58fd9f8-7h2` +
		`tm_local_hm-deadbeef.log"}}}`
	l, ok := parseLogLine(raw)
	if !ok {
		t.Fatal(
			"an unparsed line was dropped; the collector kept it " +
				"and so must the pane",
		)
	}
	if l.Text != "a line with no CRI envelope" {
		t.Errorf("text: got %q", l.Text)
	}
	if l.Service != "hm" {
		t.Errorf("service: got %q want hm", l.Service)
	}
	if l.When.IsZero() {
		t.Error(
			"with no cri_ts the read time is the only time there " +
				"is",
		)
	}
}

// Anything that is not a record is rejected rather than rendered as an
// empty line, which would read as a log that said nothing.
func TestParseRejectsNonRecords(t *testing.T) {
	for _, raw := range []string{"", "not json at all", `{"message":""}`} {
		if _, ok := parseLogLine(raw); ok {
			t.Errorf("parsed %q as a log line", raw)
		}
	}
}
