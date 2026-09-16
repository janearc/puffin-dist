//go:build live

package main

import (
	"strings"
	"testing"
)

// TestLiveEveryTopic is the claim under test: with contracts, puffin can
// read anything on the bus. It is true where a contract exists and honestly
// bounded where one does not -- logs.events holds the log collector's output
// as plain json, and a collector is not a member of the mesh.
func TestLiveEveryTopic(t *testing.T) {
	topics, err := busTopics(homeContext())
	if err != nil {
		t.Skip("no kafka: ", err)
	}
	for _, topic := range topics {
		msgs, err := busRead(homeContext(), topic, 3)
		if err != nil {
			t.Errorf("%s: %v", topic, err)
			continue
		}
		if len(msgs) == 0 {
			t.Logf("%s: quiet", topic)
			continue
		}
		for i := range msgs {
			if id := msgs[i].SchemaID; id != 0 {
				subj, names, err := fetchSchema(
					homeContext(),
					id,
					msgs[i].MsgIndex,
				)
				if err == nil {
					msgs[i].Schema = subj
					nameFields(&msgs[i], names)
				}
			}
		}
		m := msgs[len(msgs)-1]
		var parts []string
		for _, k := range m.Order {
			parts = append(parts, k+"="+m.Fields[k])
		}
		line := strings.Join(parts, " ")
		if len(line) > 130 {
			line = line[:130] + "…"
		}
		t.Logf("%-22s [%s] %s", topic, shortSchema(m.Schema), line)
		// every record must render as something: a contract, json, or
		// an honest statement that it is neither
		if len(m.Order) == 0 && m.Err == "" {
			t.Errorf(
				"%s: a record rendered as nothing at all",
				topic,
			)
		}
	}
}
