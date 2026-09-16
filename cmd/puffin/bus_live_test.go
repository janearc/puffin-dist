//go:build live

package main

import (
	"strings"
	"testing"
)

// TestLiveBus reads the real bus and decodes it against the real registry.
// It asserts the pane learned the shape -- that records name a schema and
// the schema names their fields -- not what any service happened to say.
func TestLiveBus(t *testing.T) {
	topics, err := busTopics(homeContext())
	if err != nil {
		t.Skip("no kafka: ", err)
	}
	t.Logf("topics: %v", topics)
	msgs, err := busRead(homeContext(), "observability.events", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) == 0 {
		t.Skip("nothing on the bus right now")
	}
	// mirror what the pane does: fetch each schema once, then name every
	// record that carries it. The first cut named only the first record of
	// each schema and the rest rendered as "(unframed)", which is a test
	// bug that looks exactly like a decoder bug.
	type info struct {
		subject string
		names   map[int]string
	}
	seen := map[int]info{}
	for i := range msgs {
		id := msgs[i].SchemaID
		if id == 0 {
			continue
		}
		got, ok := seen[id]
		if !ok {
			subject, names, err := fetchSchema(
				homeContext(),
				id,
				msgs[i].MsgIndex,
			)
			if err != nil {
				t.Fatalf("schema %d: %v", id, err)
			}
			t.Logf(
				"schema %d is %s with %d named fields",
				id,
				subject,
				len(names),
			)
			if len(names) == 0 {
				t.Errorf("schema %d named no fields", id)
			}
			got = info{subject, names}
			seen[id] = got
		}
		msgs[i].Schema = got.subject
		nameFields(&msgs[i], got.names)
	}
	for _, m := range msgs {
		if m.SchemaID != 0 && m.Schema == "" {
			t.Errorf(
				"a framed record was left unnamed: %+v",
				m.Order,
			)
		}
	}
	for _, m := range msgs[:minInt(3, len(msgs))] {
		var parts []string
		for _, k := range m.Order {
			parts = append(parts, k+"="+m.Fields[k])
		}
		t.Logf(
			"  %s: %s",
			shortSchema(m.Schema),
			strings.Join(parts, " "),
		)
	}
}
