package main

import (
	"encoding/base64"
	"encoding/binary"
	"math"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// The bus pane decodes without generated types: the wire format gives field
// numbers and values, and the registry's schema text gives the names.
//
// That is the claim these tests hold it to, because a decoder that is subtly
// wrong renders confidently wrong records -- which is worse than an error, and
// is exactly the bug the schemaMessages comment records.

// wire builders. Constructing bytes by hand rather than importing a proto
// library keeps the test at the same altitude as the scanner: both work from
// the wire format alone.

// The wire types, named. A tag is the field number shifted three left with
// one of these in the low bits, and naming them is the difference between
// reading the shift and reading the format.
const (
	wireVarint  = 0
	wireFixed64 = 1
	wireBytes   = 2
	wireFixed32 = 5
)

func varintField(num int, v uint64) []byte {
	b := binary.AppendUvarint(nil, uint64(num)<<3|wireVarint)
	return binary.AppendUvarint(b, v)
}

// bytesField builds one length-delimited protobuf field, so the scanner
// is tested against real wire bytes rather than against a mock of them.
func bytesField(num int, data []byte) []byte {
	b := binary.AppendUvarint(nil, uint64(num)<<3|2)
	b = binary.AppendUvarint(b, uint64(len(data)))
	return append(b, data...)
}

// doubleField builds one 64-bit field, the wire type a scanner is most
// likely to mis-length.
func doubleField(num int, f float64) []byte {
	b := binary.AppendUvarint(nil, uint64(num)<<3|1)
	return binary.LittleEndian.AppendUint64(b, math.Float64bits(f))
}

// fixed32Field builds one 32-bit field, for the same reason.
func fixed32Field(num int, v uint32) []byte {
	b := binary.AppendUvarint(nil, uint64(num)<<3|5)
	return binary.LittleEndian.AppendUint32(b, v)
}

// a heartbeat as it actually appears: confluent framing, a zero message
// index, then the message
func confluentFramed(id int, body []byte) []byte {
	out := []byte{0}
	out = binary.BigEndian.AppendUint32(out, uint32(id))
	out = append(out, 0) // message index 0: the first message in the file
	return append(out, body...)
}

// heartbeatBody is a whole message in wire form, since decoding one field
// correctly says nothing about decoding a record.
func heartbeatBody() []byte {
	var b []byte
	b = append(b, bytesField(1, []byte("kingfisher"))...)
	b = append(b, varintField(2, 42)...)
	b = append(b, doubleField(3, 1.5)...)
	b = append(b, fixed32Field(4, 7)...)
	return b
}

const heartbeatProto = `syntax = "proto3";
package observability.v1;

message Heartbeat {
  string service_name = 1;
  uint64 uptime_seconds = 2;
  double load = 3;
  fixed32 replicas = 4;
}

message RunwayState {
  string runway = 1;
}
`

// A scanner that handles three of the wire types silently drops fields of
// the fourth, which reads on screen as a message that did not carry them.
func TestScanProtoReadsEveryWireType(t *testing.T) {
	fields, ok := scanProto(heartbeatBody())
	if !ok {
		t.Fatal("a well-formed message did not scan")
	}
	if len(fields) != 4 {
		t.Fatalf("scanned %d fields, want 4", len(fields))
	}
	for i, want := range []string{"kingfisher", "42", "1.5", "7"} {
		if got := renderField(fields[i]); got != want {
			t.Errorf(
				"field %d rendered %q, want %q",
				i+1,
				got,
				want,
			)
		}
	}
}

// The scanner must FAIL rather than invent: a truncated record and a
// reserved wire type both mean "these are not the bytes you think".
func TestScanProtoRefusesNonsense(t *testing.T) {
	body := heartbeatBody()
	if _, ok := scanProto(body[:len(body)-3]); ok {
		t.Error("a truncated message scanned clean")
	}
	// wire type 3 and 4 are the deprecated groups; 6 and 7 are reserved
	if _, ok := scanProto([]byte{byte(1<<3 | 6), 0x01}); ok {
		t.Error("a reserved wire type scanned clean")
	}
	// field number zero does not exist
	if _, ok := scanProto([]byte{0x00, 0x01}); ok {
		t.Error("field number zero scanned clean")
	}
	// a length that runs past the end of the buffer
	if _, ok := scanProto([]byte{0x0a, 0x40, 'a'}); ok {
		t.Error("a length past the end scanned clean")
	}
	// 64-bit and 32-bit fields cut short
	if _, ok := scanProto([]byte{0x09, 0x01, 0x02}); ok {
		t.Error("a short 64-bit field scanned clean")
	}
	if _, ok := scanProto([]byte{0x0d, 0x01}); ok {
		t.Error("a short 32-bit field scanned clean")
	}
}

// A nested message renders as its fields rather than as a byte count --
// which is how a google.protobuf.Timestamp shows as seconds instead of as
// eleven bytes of nothing.
func TestRenderFieldNestsAndFallsBack(t *testing.T) {
	inner := append(varintField(1, 1756598078), varintField(2, 500)...)
	f := wireField{num: 9, kind: 2, data: inner}
	if got := renderField(f); got != "{1=1756598078 2=500}" {
		t.Errorf("nested message rendered %q", got)
	}
	// bytes that are neither text nor a message say how many they are,
	// which is the honest answer
	f = wireField{num: 9, kind: 2, data: []byte{0x01, 0x02, 0x03}}
	if got := renderField(f); got != "3 bytes" {
		t.Errorf("opaque bytes rendered %q", got)
	}
	// high bytes stay text on purpose: utf-8 is how service names with
	// anything but ascii in them survive the wire
	f = wireField{num: 9, kind: 2, data: []byte("café")}
	if got := renderField(f); got != "café" {
		t.Errorf("utf-8 rendered %q", got)
	}
}

// Binary handed to a terminal stops being a terminal, so the test is what
// counts as showable text.
func TestIsPrintable(t *testing.T) {
	for _, tc := range []struct {
		in   []byte
		want bool
	}{
		{[]byte("kingfisher"), true},
		{[]byte("two\nlines\there"), true},
		{nil, false},
		{[]byte{0x00}, false},
		{[]byte{0x7f}, false},
		{[]byte("text\x01more"), false},
	} {
		if got := isPrintable(tc.in); got != tc.want {
			t.Errorf("isPrintable(%q) = %v", tc.in, got)
		}
	}
}

// The framing is the whole reason this pane needs no per-topic code: the
// bytes say which contract governs them.
func TestDecodeOneReadsTheFraming(t *testing.T) {
	m := decodeOne(
		confluentFramed(7, heartbeatBody()),
		"observability.events",
	)
	if m.SchemaID != 7 {
		t.Fatalf("schema id %d, want 7", m.SchemaID)
	}
	if m.Err != "" {
		t.Fatalf("error decoding a good record: %s", m.Err)
	}
	if m.Fields["1"] != "kingfisher" {
		t.Fatalf("field 1 = %q", m.Fields["1"])
	}
	if len(m.Order) != 4 {
		t.Fatalf("wire order lost: %v", m.Order)
	}
}

// A producer that skipped the registry still wrote something, and a record
// with no contract is still bytes puffin can read.
func TestDecodeOneUnframed(t *testing.T) {
	m := decodeOne(heartbeatBody(), "logs.events")
	if m.Schema != "unframed" {
		t.Fatalf("schema %q", m.Schema)
	}
	if m.Fields["1"] != "kingfisher" {
		t.Fatalf(
			"an unframed protobuf record did not decode: %v",
			m.Fields,
		)
	}
	// plain json, which is what the log collector wrote to logs.events --
	// a pane that only reads contracts would render this as an error
	m = decodeOne(
		[]byte(`{"level":"info","svc":"kingfisher"}`),
		"logs.events",
	)
	if m.Schema != "json" {
		t.Fatalf("json record decoded as %q", m.Schema)
	}
	if m.Fields["svc"] != "kingfisher" {
		t.Fatalf("json fields: %v", m.Fields)
	}
	// and bytes that are neither say so rather than pretending
	m = decodeOne([]byte{0xff, 0xff, 0xff}, "logs.events")
	if m.Err == "" {
		t.Error("undecodable bytes decoded silently")
	}
}

// The whole stream is one base64 blob, split after decoding: 0x0a is field 1
// with a length-delimited type, not a delimiter, and splitting the encoded
// text turned every heartbeat into plausible nonsense.
func TestDecodeBatchSplitsAfterDecoding(t *testing.T) {
	rec := confluentFramed(7, heartbeatBody())
	blob := append(
		append(append([]byte{}, rec...), []byte(busSep)...),
		rec...)
	msgs := decodeBatch(
		base64.StdEncoding.EncodeToString(blob),
		"observability.events",
	)
	if len(msgs) != 2 {
		t.Fatalf("got %d records, want 2", len(msgs))
	}
	for i, m := range msgs {
		if m.Fields["1"] != "kingfisher" {
			t.Errorf("record %d: %v", i, m.Fields)
		}
	}
	// a batch that is not base64 at all is one honest error, not a crash
	bad := decodeBatch("this is not base64!!", "observability.events")
	if len(bad) != 1 || bad[0].Err == "" {
		t.Fatalf("unreadable batch: %+v", bad)
	}
}

// Names come from the schema in file order. The first cut collected fields
// from the whole file into one map and the last message won, so a
// heartbeat's service_name rendered as runway_state: names from a different
// message applied confidently to the right bytes.
func TestSchemaMessagesKeepsFileOrder(t *testing.T) {
	msgs := schemaMessages(heartbeatProto)
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}
	if msgs[0].name != "Heartbeat" || msgs[1].name != "RunwayState" {
		t.Fatalf("order: %q, %q", msgs[0].name, msgs[1].name)
	}
	if msgs[0].fields[1] != "service_name" {
		t.Fatalf("Heartbeat field 1 = %q", msgs[0].fields[1])
	}
	// the second message's field 1 must not have overwritten the first's
	if msgs[1].fields[1] != "runway" {
		t.Fatalf("RunwayState field 1 = %q", msgs[1].fields[1])
	}
}

// A nested type's field 1 is not the enclosing message's field 1.
func TestSchemaMessagesIgnoresNestedTypes(t *testing.T) {
	msgs := schemaMessages(`package a.v1;
message Outer {
  string outer_name = 1;
  message Inner {
    string inner_name = 1;
  }
  int32 count = 2;
}
`)
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	if msgs[0].fields[1] != "outer_name" {
		t.Fatalf(
			"field 1 = %q; a nested type's field won",
			msgs[0].fields[1],
		)
	}
	if msgs[0].fields[2] != "count" {
		t.Fatalf("field 2 = %q", msgs[0].fields[2])
	}
}

// Field names come from the registered schema, so reading a name out of
// schema text is the step that turns numbers into a record.
func TestSchemaPackageAndMessageName(t *testing.T) {
	if got := schemaPackage(heartbeatProto); got != "observability.v1" {
		t.Errorf("package %q", got)
	}
	if got := schemaMessageName(
		heartbeatProto,
	); got != "observability.v1.Heartbeat" {
		t.Errorf("name %q", got)
	}
	// a schema with no package is still a message
	if got := schemaMessageName("message Bare {\n string x = " +
		"1;\n}"); got != "Bare" {
		t.Errorf("name %q", got)
	}
	if got := schemaPackage("message Bare {}"); got != "" {
		t.Errorf("package %q", got)
	}
}

// When the registry answers, numbers become names. When it does not, the
// numbers stand on their own rather than the record becoming unreadable.
func TestNameFields(t *testing.T) {
	m := decodeOne(
		confluentFramed(7, heartbeatBody()),
		"observability.events",
	)
	nameFields(&m, schemaMessages(heartbeatProto)[0].fields)
	if m.Fields["service_name"] != "kingfisher" {
		t.Fatalf("fields: %v", m.Fields)
	}
	if m.Order[0] != "service_name" || m.Order[1] != "uptime_seconds" {
		t.Fatalf("order: %v", m.Order)
	}
	// a field the schema does not mention keeps a legible label
	m2 := decodeOne(
		confluentFramed(7, heartbeatBody()),
		"observability.events",
	)
	nameFields(&m2, map[int]string{1: "service_name"})
	if m2.Fields["field 2"] != "42" {
		t.Fatalf("an unnamed field was lost: %v", m2.Fields)
	}
	// no schema at all leaves the record exactly as it was
	m3 := decodeOne(
		confluentFramed(7, heartbeatBody()),
		"observability.events",
	)
	before := len(m3.Order)
	nameFields(&m3, nil)
	if len(m3.Order) != before || m3.Fields["1"] != "kingfisher" {
		t.Fatalf("an empty schema changed the record: %v", m3.Fields)
	}
}

// A value has to render as itself: a timestamp as a time, bytes as bytes,
// and nothing as a plausible-looking guess.
func TestDecodeJSONAndRenderValues(t *testing.T) {
	obj, ok := decodeJSON(
		[]byte(
			`{"b":"text","a":1,"c":1.5,"d":true,"e":null,` +
				`"f":{"y":1,"x":2},"g":[1,2,3]}`,
		),
	)
	if !ok {
		t.Fatal("a json record did not decode")
	}
	// keys sorted, so a screen that refreshes does not reshuffle its
	// columns
	if strings.Join(obj.order, ",") != "a,b,c,d,e,f,g" {
		t.Fatalf("order: %v", obj.order)
	}
	for k, want := range map[string]string{
		"a": "1", "b": "text", "c": "1.5", "d": "true", "e": "null",
		"f": "{x y}", "g": "[3]",
	} {
		if obj.fields[k] != want {
			t.Errorf("%s = %q, want %q", k, obj.fields[k], want)
		}
	}
	if _, ok := decodeJSON([]byte(`{}`)); ok {
		t.Error("an empty object decoded as a record")
	}
	if _, ok := decodeJSON([]byte(`not json`)); ok {
		t.Error("non-json decoded as a record")
	}
}

// The schema line is trimmed to what fits a row without losing the part
// that identifies which schema it is.
func TestShortSchema(t *testing.T) {
	if got := shortSchema(
		"observability.v1.Heartbeat",
	); got != "Heartbeat" {
		t.Errorf("got %q", got)
	}
	if got := shortSchema(""); got != "(unframed)" {
		t.Errorf("got %q", got)
	}
	if got := shortSchema("Bare"); got != "Bare" {
		t.Errorf("got %q", got)
	}
}

// busFixture is a pane holding two decoded records on a named topic.
func busFixture() *busPane {
	p := &busPane{}
	m1 := decodeOne(
		confluentFramed(7, heartbeatBody()),
		"observability.events",
	)
	nameFields(&m1, schemaMessages(heartbeatProto)[0].fields)
	m1.Schema = "observability.v1.Heartbeat"
	m2 := decodeOne([]byte(`{"level":"info","svc":"dodo"}`), "logs.events")
	p.v = BusView{
		Topics: []string{
			"observability.events",
			"flipr.oplog",
			"logs.events",
		},
		Topic:    "observability.events",
		Messages: []BusMessage{m1, m2},
	}
	return p
}

// A spinner that says nothing is indistinguishable from a hang, and this
// read is genuinely slow: a consumer inside the cluster with an eight second
// window, then a schema fetch per id.
func TestBusPaneSaysWhyItIsSlow(t *testing.T) {
	p := busFixture()
	p.loading = true
	out := p.View(Compile(DodoDark()), 120, 40)
	if !strings.Contains(out, "reading the bus") ||
		!strings.Contains(out, "8s") {
		t.Fatal("a loading pane did not say what was slow")
	}
	// and the topics stay on screen while it loads, so the operator can see
	// which one they are waiting for
	if !strings.Contains(out, "observability.events") {
		t.Error("the topics vanished during the read")
	}
}

// The pane draws decoded records, which is the whole reason it is not a
// hexdump.
func TestBusPaneDrawsRecords(t *testing.T) {
	out := busFixture().View(Compile(DodoDark()), 160, 40)
	if !strings.Contains(out, "2 records") {
		t.Error("the pane did not say how many records it has")
	}
	if !strings.Contains(out, "Heartbeat") {
		t.Error("the record's type is not shown")
	}
	if !strings.Contains(out, "service_name=kingfisher") {
		t.Error("a named field did not reach the screen")
	}
	if !strings.Contains(out, "svc=dodo") {
		t.Error("the contract-less record was not rendered")
	}
}

// An empty topic is a fact about the topic, not a failure, and the pane says
// what to do next.
func TestBusPaneEmptyAndWarnings(t *testing.T) {
	p := &busPane{
		v: BusView{
			Topics: []string{"flipr.oplog"},
			Topic:  "flipr.oplog",
		},
	}
	out := p.View(Compile(DodoDark()), 120, 40)
	if !strings.Contains(out, "nothing arrived on flipr.oplog") {
		t.Fatal("an empty topic did not say so")
	}
	if !strings.Contains(out, "tab for another topic") {
		t.Error("the pane did not say how to move on")
	}
	// with no topic at all the pane still renders rather than lying about a
	// topic it does not have
	p = &busPane{}
	if out := p.View(Compile(DodoDark()), 120, 40); !strings.Contains(
		out,
		"nothing arrived on -",
	) {
		t.Errorf("no-topic render: %q", out)
	}
	p.v.Warnings = []string{"cannot reach the registry"}
	if out := p.View(Compile(DodoDark()), 120, 40); !strings.Contains(
		out,
		"cannot reach the registry",
	) {
		t.Error("a warning was hidden")
	}
}

// A record that could not be decoded shows its error in place, so one bad
// record does not take the screen with it.
func TestBusPaneShowsPerRecordErrors(t *testing.T) {
	p := busFixture()
	p.v.Messages = append(p.v.Messages, BusMessage{
		Topic: "observability.events", Err: "neither protobuf nor json",
		Fields: map[string]string{}})
	out := p.View(Compile(DodoDark()), 160, 40)
	if !strings.Contains(out, "neither protobuf nor json") {
		t.Error("a record's error was swallowed")
	}
	if !strings.Contains(out, "service_name=kingfisher") {
		t.Error("one bad record took the good ones with it")
	}
}

// Changing topic reloads and moving does not, because the two topics
// share nothing worth keeping and the rows in hand have not changed.
func TestBusPaneKeys(t *testing.T) {
	t.Cleanup(func() { restoreWatches(nil); enableNotify(false) })
	restoreWatches(nil)
	p := busFixture()

	// tab moves to the next topic and re-reads, wrapping at the end
	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyTab})
	if p.topic != 1 || cmd == nil {
		t.Fatalf("tab: topic %d cmd %v", p.topic, cmd)
	}
	p.topic = len(p.v.Topics) - 1
	p.Update(tea.KeyMsg{Type: tea.KeyTab})
	if p.topic != 0 {
		t.Fatalf("tab did not wrap: %d", p.topic)
	}

	// j and k walk the records and stop at the ends
	p.cursor = 0
	p.Update(key2('j'))
	if p.cursor != 1 {
		t.Fatalf("j: %d", p.cursor)
	}
	p.Update(key2('j'))
	if p.cursor != 1 {
		t.Fatalf("j past the end: %d", p.cursor)
	}
	p.Update(key2('k'))
	p.Update(key2('k'))
	if p.cursor != 0 {
		t.Fatalf("k past the start: %d", p.cursor)
	}

	// tab left the pane loading, and a loading pane deliberately draws why
	// it is slow instead of its body
	p.loading = false

	// the search box captures its own keystrokes
	p.Update(key2('/'))
	if !p.Capturing() {
		t.Fatal("/ did not open the search box")
	}
	for _, r := range "dodox" {
		p.Update(key2(r))
	}
	p.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if p.query != "dodo" {
		t.Fatalf("query %q", p.query)
	}
	if !strings.Contains(p.View(Compile(DodoDark()), 160, 40), "/dodo") {
		t.Error("what is being typed is not on screen")
	}
	p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if p.Capturing() {
		t.Fatal("enter did not close the search box")
	}
	if !strings.Contains(p.View(Compile(DodoDark()), 160, 40), "/dodo") {
		t.Error(
			"a closed search is still a live search and must " +
				"stay on screen",
		)
	}

	// W watches what is in the box, and says so
	p.Update(key2('W'))
	if len(watchesFor("bus")) != 1 {
		t.Fatalf("W did not watch: %v", watchesFor("bus"))
	}
	if !strings.Contains(
		p.View(Compile(DodoDark()), 160, 40),
		"watching: dodo",
	) {
		t.Error("the pane does not show what it is waiting for")
	}
	p.Update(key2('W'))
	if len(watchesFor("bus")) != 0 {
		t.Fatal("W did not toggle the watch off")
	}
	// and W with an empty box watches nothing rather than everything
	p.query = ""
	p.Update(key2('W'))
	if len(watchesFor("bus")) != 0 {
		t.Fatal("W with an empty box created a watch")
	}
	// esc abandons the box
	p.Update(key2('/'))
	p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if p.typing || p.query != "" {
		t.Fatal("esc did not abandon the search")
	}
}

// A read that comes back shorter must not leave the cursor past the end.
func TestBusPaneFetchClampsCursor(t *testing.T) {
	p := busFixture()
	p.cursor, p.loading = 1, true
	p.Update(busFetched{BusView{Topics: p.v.Topics, Topic: "flipr.oplog"}})
	if p.cursor != 0 {
		t.Fatalf("cursor %d after an empty read", p.cursor)
	}
	if p.loading {
		t.Error(
			"the pane still claims to be loading after the read " +
				"landed",
		)
	}
}

// The bus has no cursor to remember, so a record is identified by its
// idempotency key -- which is what an idempotency key is for. A watch must
// not fire twice for one heartbeat just because the tail was read twice.
func TestBusWatchesFireOncePerRecord(t *testing.T) {
	t.Cleanup(func() { restoreWatches(nil); enableNotify(false) })
	restoreWatches([]Watch{{Pattern: "kingfisher", Where: "bus"}})

	rec := func(key, svc string) BusMessage {
		return BusMessage{
			Schema: "observability.v1.Heartbeat",
			Fields: map[string]string{
				"idempotency_key": key,
				"service_name":    svc,
			},
			Order: []string{"idempotency_key", "service_name"},
		}
	}
	p := &busPane{}
	p.v.Messages = []BusMessage{rec("k1", "kingfisher")}
	// the first read is the baseline: opening the pane must not announce
	// what was already on the bus
	p.check()
	if len(p.seen) != 1 {
		t.Fatalf("baseline: %v", p.seen)
	}

	// Listen has no unsubscribe by design, so the counter is gated on a
	// flag this test owns rather than on a listener being removed
	counting, fired := true, 0
	Listen(func(e Event) {
		if counting && e.Kind == WatchFired {
			fired++
		}
	})
	t.Cleanup(func() { counting = false })

	// the same record again is the same record
	p.check()
	// and a new one fires exactly once, however often the tail is re-read
	p.v.Messages = append(p.v.Messages, rec("k2", "kingfisher"))
	p.check()
	p.check()
	if fired != 1 {
		t.Fatalf("%d notifications for one new record", fired)
	}
}

// recordKey prefers the identifier the record actually carries, and falls
// back to the whole line when it carries none.
func TestRecordKeyAndLine(t *testing.T) {
	m := BusMessage{
		Fields: map[string]string{
			"idempotency_key": "abc",
			"id":              "1",
			"ts":              "2",
		},
		Order: []string{"idempotency_key", "id", "ts"},
	}
	if got := recordKey(m); got != "abc" {
		t.Errorf("key %q", got)
	}
	m.Fields["idempotency_key"] = ""
	if got := recordKey(m); got != "1" {
		t.Errorf("key %q", got)
	}
	bare := BusMessage{
		Fields: map[string]string{"a": "1", "b": "2"},
		Order:  []string{"a", "b"},
	}
	if got := recordKey(bare); got != "a=1 b=2" {
		t.Errorf("fallback key %q", got)
	}
	if got := recordLine(bare); got != "a=1 b=2" {
		t.Errorf("line %q", got)
	}
}

// Nothing may render outside the terminal, at any size.
func TestBusPaneDrawsInATinyWindow(t *testing.T) {
	p := busFixture()
	for _, wh := range [][2]int{{40, 6}, {20, 0}, {200, 60}} {
		if out := p.View(Compile(DodoDark()), wh[0], wh[1]); out == "" {
			t.Errorf("%dx%d drew nothing", wh[0], wh[1])
		}
	}
}

// A schema whose framing points at a message other than the first carries an
// index array, and the bytes after it are the message. Skipping the array
// wrongly decodes the right bytes against the wrong contract, which is the
// failure that looks decoded.
func TestDecodeOneSkipsTheMessageIndexArray(t *testing.T) {
	body := heartbeatBody()
	raw := []byte{0}
	raw = binary.BigEndian.AppendUint32(raw, 11)
	raw = append(raw, 0x02, 0x00, 0x01) // two indices: this is message 2
	raw = append(raw, body...)

	m := decodeOne(raw, "observability.events")
	if m.MsgIndex != 2 {
		t.Fatalf("message index %d, want 2", m.MsgIndex)
	}
	if m.SchemaID != 11 {
		t.Fatalf("schema id %d", m.SchemaID)
	}
	if m.Fields["1"] != "kingfisher" {
		t.Fatalf("the array was not skipped: %v", m.Fields)
	}
}

// A record shorter than the framing is not a framed record.
func TestDecodeOneShortRecord(t *testing.T) {
	m := decodeOne([]byte{0, 1, 2}, "observability.events")
	if m.Schema != "unframed" {
		t.Fatalf("schema %q", m.Schema)
	}
}

// With nothing being watched the pane still tracks what it has seen, so
// turning a watch ON does not then announce the whole backlog.
func TestBusCheckTracksSeenWithNoWatches(t *testing.T) {
	t.Cleanup(func() { restoreWatches(nil) })
	restoreWatches(nil)
	p := &busPane{}
	p.v.Messages = []BusMessage{
		{Fields: map[string]string{"id": "1"}, Order: []string{"id"}},
	}
	p.check()
	p.v.Messages = append(p.v.Messages, BusMessage{
		Fields: map[string]string{"id": "2"}, Order: []string{"id"}})
	p.check()
	if !p.seen["2"] {
		t.Fatal(
			"a record arriving with no watch set was not " +
				"remembered",
		)
	}
}

// A watch that does not match stays quiet.
func TestBusWatchIgnoresRecordsThatDoNotMatch(t *testing.T) {
	t.Cleanup(func() { restoreWatches(nil); enableNotify(false) })
	restoreWatches([]Watch{{Pattern: "kestrel", Where: "bus"}})
	counting, fired := true, 0
	Listen(func(e Event) {
		if counting && e.Kind == WatchFired {
			fired++
		}
	})
	t.Cleanup(func() { counting = false })

	p := &busPane{seen: map[string]bool{}}
	p.v.Messages = []BusMessage{{Schema: "observability.v1.Heartbeat",
		Fields: map[string]string{"id": "9", "service_name": "dodo"},
		Order:  []string{"id", "service_name"}}}
	p.check()
	if fired != 0 {
		t.Fatalf(
			"%d notifications for a record nobody asked about",
			fired,
		)
	}
}
