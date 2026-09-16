package main

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The bus pane: what services are saying to each other.
//
// Three topics carry the traffic worth watching: every service's
// heartbeat, every flag flip, and the log stream. Membership on the
// network is decided from those heartbeats, so watching the bus is
// watching that decision being made.
//
// Everything here is generic, and it can be because the contracts did the
// work already: the topics are protobuf and their schemas are registered,
// so puffin fetches the schema by id and decodes against it. No per-service
// code, the same rule the contract screen follows.
//
// Two hard constraints shape it. Kafka is headless -- ClusterIP None -- so
// there is no name to dial from a laptop, and every read runs inside the
// cluster through kubectl exec, bounded by --max-messages and a timeout so a
// busy topic cannot turn a keystroke into a firehose.
//
// And the decoding is a wire scanner rather than generated types: protobuf's
// wire format gives field numbers and values without any code for the message,
// and the registry's schema text supplies the names.

// kafkaPod is where the reads run; the bus has no route by design.
const (
	kafkaPod       = "kafka-0"
	kafkaBootstrap = "localhost:9092"
	schemaRegistry = "http://schema-registry:8081"
	busMaxMessages = 50
	busTimeout     = 8 * time.Second
	// busSep separates records in the consumer's output. Any byte can
	// appear inside a protobuf message, so no separator is safe by
	// construction -- this one is safe by being improbable.
	busSep = "@@SEP@@"
)

// kafkaNS finds which namespace holds the broker, because the namespace is
// NOT a constant even though it was written as one.
//
// This pane ran kubectl exec with the context as a variable and the namespace
// hardcoded, so it worked against one cluster and only against that one.
//
// Pointed at another, where the broker lives in a namespace of its own, kubectl
// said "pods kafka-0 not found" and the pane said "listing topics: exit status
// 1", which names neither the cluster it looked in nor the thing it could not
// find.
//
// Found by NAME across namespaces rather than by a second hardcoded guess,
// which is the same rule the log collector is found by, for the same reason:
// a tool that hardcodes where a thing lives works until it moves.
//
// The errors go through kubectlErr, which lifecycle.go has had since it was
// written and this file never called -- so the useful half of every failure,
// kubectl's own stderr, was being dropped on the floor three call sites at a
// time.
func kafkaNS(kubeCtx string) (string, error) {
	nsCache.mu.Lock()
	ns, ok := nsCache.byContext[kubeCtx]
	nsCache.mu.Unlock()
	if ok {
		return ns, nil
	}
	out,
		err := exec.Command(
		"kubectl",
		"--context",
		kubeCtx,
		"get",
		"pods",
		"-A",
		"--field-selector", "metadata.name="+kafkaPod,
		"-o", "jsonpath={.items[0].metadata.namespace}").
		Output()
	if err != nil {
		return "", fmt.Errorf(
			"finding %s in %s: %w",
			kafkaPod,
			kubeCtx,
			kubectlErr(err),
		)
	}
	ns = strings.TrimSpace(string(out))
	if ns == "" {
		return "", fmt.Errorf(
			"no pod called %s in %s -- is the bus running there?",
			kafkaPod,
			kubeCtx,
		)
	}
	nsCache.mu.Lock()
	if nsCache.byContext == nil {
		nsCache.byContext = map[string]string{}
	}
	nsCache.byContext[kubeCtx] = ns
	nsCache.mu.Unlock()
	return ns, nil
}

// nsCache remembers where the broker was found, per cluster. A pod does not
// change namespace, and this is asked before every read.
var nsCache = struct {
	mu        sync.Mutex
	byContext map[string]string
}{}

// BusMessage is one decoded record.
type BusMessage struct {
	Topic    string
	Schema   string            // the subject the framing pointed at
	Fields   map[string]string // field name (or number) to rendered value
	Order    []string          // field order as it appeared on the wire
	SchemaID int               // the registry id the framing pointed at
	// which message in that schema these bytes are
	MsgIndex int
	// payload bytes, when nothing could be decoded
	Raw int
	Err string
}

// BusView is the pane's data.
type BusView struct {
	Topics   []string
	Topic    string
	Messages []BusMessage
	Warnings []string
	Read     time.Time
}

// busTopics lists what the bus carries, minus kafka's own bookkeeping.
func busTopics(kubeCtx string) ([]string, error) {
	ns, err := kafkaNS(kubeCtx)
	if err != nil {
		return nil, err
	}
	out,
		err := exec.Command(
		"kubectl", "--context", kubeCtx,
		"exec", "-n", ns,
		kafkaPod, "--", "bash", "-c",
		"kafka-topics --bootstrap-server "+kafkaBootstrap+" --list").
		Output()
	if err != nil {
		return nil, fmt.Errorf(
			"listing topics in %s: %w",
			kubeCtx,
			kubectlErr(err),
		)
	}
	var topics []string
	for _, t := range strings.Fields(string(out)) {
		// __consumer_offsets and _schemas are kafka talking to itself
		if strings.HasPrefix(t, "_") {
			continue
		}
		topics = append(topics, t)
	}
	return topics, nil
}

// busRead consumes the tail of a topic. --max-messages bounds it and the
// timeout bounds the wait, so a quiet topic returns quickly with nothing
// rather than hanging the pane on a bus that has nothing to say.
func busRead(kubeCtx, topic string, n int) ([]BusMessage, error) {
	// A console consumer with no offset reads only what arrives while it is
	// attached, so an eight-second window on a topic that heartbeats every
	// ten seconds returns nothing and looks like a dead bus.
	//
	// The tail is taken from real offsets instead: ask where the topic
	// ends, subtract, and read forward from there. Bounded, and it returns
	// immediately with history rather than waiting for the future to
	// happen.
	//
	// Records are separated by an improbable string rather than by a
	// newline, and the whole stream is base64'd once: 0x0a is not a
	// delimiter in protobuf, it is field 1 with a length-delimited type,
	// which makes it one of the most common bytes in these messages.
	sh := fmt.Sprintf(``+"\n"+
		`end=$(kafka-run-class kafka.tools.GetOffsetShell --bootstrap`+
		`-server %[1]s --topic %[2]s --time -1 2>/dev/null | `+
		`awk -F: '{s+=$3} END {print s+0}')`+"\n"+
		`start=$(( end - %[3]d )); [ "$start" -lt 0 ] && start=0`+"\n"+
		`timeout %[4]d kafka-console-consumer --bootstrap-server %[1]`+
		`s --topic %[2]s --offset "$start" --partition 0 --ma`+
		`x-messages %[3]d --timeout-ms %[5]d --property line.`+
		`separator='%[6]s' 2>/dev/null | base64 -w0`+"\n"+
		``, kafkaBootstrap, topic, n, int(
		busTimeout.Seconds(),
	), int(busTimeout.Milliseconds()), busSep)
	ns, err := kafkaNS(kubeCtx)
	if err != nil {
		return nil, err
	}
	out,
		err := exec.Command(
		"kubectl", "--context", kubeCtx,
		"exec", "-n", ns,
		kafkaPod, "--", "bash", "-c", sh).
		Output()
	if err != nil && len(out) == 0 {
		return nil, fmt.Errorf("reading %s: %w", topic, kubectlErr(err))
	}
	return decodeBatch(string(out), topic), nil
}

// wireField is one field as it appears on the protobuf wire.
type wireField struct {
	num  int
	kind int
	val  uint64
	data []byte
}

// scanProto walks a protobuf message without knowing what it is. The wire
// format carries a field number and a type for every field, which is enough
// to render values -- names come from the registry separately, and when the
// registry cannot be reached the numbers still say something.
func scanProto(b []byte) ([]wireField, bool) {
	var out []wireField
	i := 0
	for i < len(b) {
		key, n := binary.Uvarint(b[i:])
		if n <= 0 {
			return out, false
		}
		i += n
		f := wireField{num: int(key >> 3), kind: int(key & 7)}
		if f.num == 0 {
			return out, false
		}
		switch f.kind {
		case 0: // varint
			v, n := binary.Uvarint(b[i:])
			if n <= 0 {
				return out, false
			}
			f.val, i = v, i+n
		case 1: // 64-bit
			if i+8 > len(b) {
				return out, false
			}
			f.val, i = binary.LittleEndian.Uint64(b[i:]), i+8
		case 2: // length-delimited
			l, n := binary.Uvarint(b[i:])
			if n <= 0 || i+n+int(l) > len(b) {
				return out, false
			}
			i += n
			f.data, i = b[i:i+int(l)], i+int(l)
		case 5: // 32-bit
			if i+4 > len(b) {
				return out, false
			}
			f.val, i = uint64(
				binary.LittleEndian.Uint32(b[i:]),
			), i+4
		default:
			return out, false
		}
		out = append(out, f)
	}
	return out, true
}

// renderField turns one wire field into something readable. A
// length-delimited field is shown as text when it IS text, because most of
// them are, and as a nested message when it scans as one -- which is how a
// google.protobuf.Timestamp shows up as seconds rather than as bytes.
func renderField(f wireField) string {
	switch f.kind {
	case 0:
		return strconv.FormatUint(f.val, 10)
	case 1:
		return strconv.FormatFloat(
			math.Float64frombits(f.val),
			'g',
			-1,
			64,
		)
	case 5:
		return strconv.FormatUint(f.val, 10)
	}
	if isPrintable(f.data) {
		return string(f.data)
	}
	if sub, ok := scanProto(f.data); ok && len(sub) > 0 {
		parts := make([]string, 0, len(sub))
		for _, s := range sub {
			parts = append(
				parts,
				fmt.Sprintf("%d=%s", s.num, renderField(s)),
			)
		}
		return "{" + strings.Join(parts, " ") + "}"
	}
	return fmt.Sprintf("%d bytes", len(f.data))
}

// isPrintable decides whether a decoded field can be shown as text or has
// to be rendered as bytes. A terminal handed raw binary stops being a
// terminal.
func isPrintable(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	for _, c := range b {
		if c < 0x20 && c != '\t' && c != '\n' || c == 0x7f {
			return false
		}
	}
	return true
}

// The confluent framing: a zero byte, a four-byte big-endian schema id, then
// for protobuf a varint message-index array, then the message itself.
//
// Puffin reads the id and asks the registry what it is, which is the whole
// reason this pane needs no per-topic code: the bytes on the wire say which
// contract governs them, and the contract is a fetch away. That is the same
// bargain the contract screen makes with /api, arriving from the other side.
func decodeBatch(raw, topic string) []BusMessage {
	// the whole stream is one base64 blob and the records are split after
	// decoding.
	//
	// Splitting the encoded text on newlines was the first cut and it
	// turned every heartbeat into fragments that decoded to plausible
	// nonsense -- 0x0a is field 1 with a length-delimited type, not a
	// delimiter.
	all, err := base64Decode(raw)
	if err != nil {
		return []BusMessage{{Topic: topic, Err: "unreadable batch"}}
	}
	var out []BusMessage
	for _, rec := range strings.Split(string(all), busSep) {
		if rec == "" {
			continue
		}
		out = append(out, decodeOne([]byte(rec), topic))
	}
	return out
}

// decodeOne decodes a single record.
func decodeOne(b []byte, topic string) BusMessage {
	m := BusMessage{Topic: topic, Fields: map[string]string{}}
	if len(b) < 5 || b[0] != 0 {
		// not confluent-framed: try it as a bare protobuf message,
		// since a producer that skipped the registry still wrote
		// something
		m.Schema = "unframed"
		m.decode(b)
		return m
	}
	id := int(binary.BigEndian.Uint32(b[1:5]))
	body := b[5:]
	// protobuf framing carries a message-index array first: a single zero
	// means "the first message in the file", which is the common case
	if idx, n := binary.Uvarint(body); n > 0 {
		if idx == 0 {
			body = body[n:]
		} else {
			m.MsgIndex = int(idx)
			// skip the array: a count followed by that many indices
			skip := n
			for i := uint64(0); i < idx && skip < len(body); i++ {
				_, k := binary.Uvarint(body[skip:])
				if k <= 0 {
					break
				}
				skip += k
			}
			body = body[skip:]
		}
	}
	m.SchemaID = id
	m.decode(body)
	return m
}

// decode scans the payload and records the fields in wire order.
//
// Protobuf first, then json. Not everything on this bus carries a contract:
// logs.events holds the log collector's own output, which is plain json,
// because a collector is not a member of the mesh and has no descriptor to
// publish.
//
// Nothing writes to it any more -- logstash did, hm refused every record as
// off-contract, the refusal was collected and refused again, and the loop wrote
// 164MB in six minutes, so the collector that replaced it ships to files only
// -- but the topic is still there and still full.
//
// A pane that only reads contracts would render it as an error, which is a tool
// refusing to show bytes it can perfectly well read.
func (m *BusMessage) decode(b []byte) {
	fields, ok := scanProto(b)
	if !ok || len(fields) == 0 {
		if obj, ok := decodeJSON(b); ok {
			m.Schema, m.Fields, m.Order =
				"json", obj.fields, obj.order
			return
		}
		m.Raw = len(b)
		if m.Err == "" && len(b) > 0 {
			m.Err = "neither protobuf nor json"
		}
		return
	}
	for _, f := range fields {
		name := strconv.Itoa(f.num)
		m.Fields[name] = renderField(f)
		m.Order = append(m.Order, name)
	}
}

// base64Decode: the consumer's output crosses kubectl, which is not a
// binary-safe channel, so the bytes travel encoded and are unwrapped here.
func base64Decode(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(strings.TrimSpace(s))
}

// protoMessage is one message declared in a schema, in file order.
type protoMessage struct {
	name   string
	fields map[int]string
}

// schemaMessages pulls the messages and their field names out of a .proto
// source, in file order, which is what the framing's message index refers to.
//
// The first cut collected fields from the whole file into one map and the last
// message's field 1 won. observability.v1 declares six messages, so a
// heartbeat's service_name rendered as runway_state -- names from a different
// message applied confidently to the right bytes.
//
// Wrong in the most expensive way: it looked decoded.
//
// A scanner, not a parser: a line ending in "= <number>;" inside a message
// block is a field, and the identifier before the equals is its name. It
// fails by returning nothing rather than by being subtly wrong.
func schemaMessages(proto string) []protoMessage {
	var out []protoMessage
	depth := 0
	for _, line := range strings.Split(proto, "\n") {
		l := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(l, "message ") && depth == 0:
			name := strings.TrimSpace(
				strings.TrimSuffix(
					strings.TrimPrefix(l, "message "),
					"{",
				),
			)
			out = append(
				out,
				protoMessage{
					name:   name,
					fields: map[int]string{},
				},
			)
			depth++
			continue
		case strings.HasSuffix(l, "{"):
			depth++
			continue
		case l == "}":
			depth--
			continue
		}
		// depth 1 is the current message's own fields; deeper is a
		// nested type, whose field 1 is not this message's field 1
		if depth != 1 || !strings.HasSuffix(l, ";") || len(out) == 0 {
			continue
		}
		eq := strings.LastIndex(l, "=")
		if eq < 0 {
			continue
		}
		num, err := strconv.Atoi(
			strings.TrimSpace(strings.TrimSuffix(l[eq+1:], ";")),
		)
		if err != nil {
			continue
		}
		head := strings.Fields(strings.TrimSpace(l[:eq]))
		if len(head) < 2 {
			continue
		}
		out[len(out)-1].fields[num] = head[len(head)-1]
	}
	return out
}

// nameFields replaces field numbers with the names the registry's schema gives
// them. The schema arrives as .proto source, so the names are read out of the
// text: "string service_name = 1;" is a field number and a name, and that is
// all this needs.
//
// It does not need to understand proto, only to find the pairs -- and when the
// registry cannot be reached the numbers stand on their own.
func nameFields(m *BusMessage, names map[int]string) {
	if len(names) == 0 {
		return
	}
	fields := make(map[string]string, len(m.Fields))
	order := make([]string, 0, len(m.Order))
	for _, k := range m.Order {
		n, err := strconv.Atoi(k)
		if err != nil {
			fields[k], order = m.Fields[k], append(order, k)
			continue
		}
		name := names[n]
		if name == "" {
			name = "field " + k
		}
		fields[name] = m.Fields[k]
		order = append(order, name)
	}
	m.Fields, m.Order = fields, order
}

// fetchSchema asks the registry what a schema id is. Cached for the life of
// the pane: schema ids are immutable by definition, so re-asking is pure
// round trip.
func fetchSchema(
	kubeCtx string,
	id, index int,
) (subject string, names map[int]string, err error) {
	ns, err := kafkaNS(kubeCtx)
	if err != nil {
		return "", nil, err
	}
	out,
		err := exec.Command(
		"kubectl", "--context", kubeCtx,
		"exec", "-n", ns,
		kafkaPod, "--", "bash", "-c",
		fmt.Sprintf("curl -s %s/schemas/ids/%d", schemaRegistry, id),
	).
		Output()
	if err != nil {
		return "", nil, fmt.Errorf(
			"asking the registry about schema %d: %w",
			id,
			kubectlErr(err),
		)
	}
	var doc struct {
		Schema     string `json:"schema"`
		SchemaType string `json:"schemaType"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return "", nil, err
	}
	msgs := schemaMessages(doc.Schema)
	if len(msgs) == 0 {
		return "", nil, fmt.Errorf("schema %d declares no messages", id)
	}
	// the framing's message index picks which message these bytes are; zero
	// -- the common case -- is the first message in the file
	if index < 0 || index >= len(msgs) {
		index = 0
	}
	subject = msgs[index].name
	if pkg := schemaPackage(doc.Schema); pkg != "" {
		subject = pkg + "." + subject
	}
	return subject, msgs[index].fields, nil
}

// schemaPackage is the proto package the schema declares.
func schemaPackage(proto string) string {
	for _, line := range strings.Split(proto, "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "package ") {
			return strings.TrimSuffix(
				strings.TrimPrefix(l, "package "),
				";",
			)
		}
	}
	return ""
}

// schemaMessageName is the package and message the schema declares, which is
// what the pane shows as the record's type.
func schemaMessageName(proto string) string {
	pkg, msg := "", ""
	for _, line := range strings.Split(proto, "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "package ") {
			pkg = strings.TrimSuffix(
				strings.TrimPrefix(l, "package "),
				";",
			)
		}
		if strings.HasPrefix(l, "message ") && msg == "" {
			msg = strings.TrimSpace(
				strings.TrimSuffix(
					strings.TrimPrefix(l, "message "),
					"{",
				),
			)
		}
	}
	if pkg != "" && msg != "" {
		return pkg + "." + msg
	}
	return msg
}

// busFetched carries a read back into the loop.
type busFetched struct{ v BusView }

// busPane watches the bus. Membership on the network is authorized by
// reading heartbeats off observability.events, so this is the screen where
// that decision is visible as it happens.
type busPane struct {
	v       BusView
	topic   int // index into v.Topics
	cursor  int
	offset  int
	loading bool
	query   string
	typing  bool
	// the idempotency keys already checked, so a watch fires for records
	// that arrive. The bus has no cursor to remember, and a record's key is
	// the only thing that identifies it across two reads of the same tail.
	seen map[string]bool
}

// Tick makes the bus pane watch itself. Fifteen seconds: heartbeats land
// every ten, so this sees each one without reading the tail four times a
// minute for the privilege.
func (p *busPane) Tick() time.Duration { return 15 * time.Second }

type schemaInfo struct {
	subject string
	names   map[int]string
}

// Key opens the bus pane from the roster.
func (p *busPane) Key() string { return "b" }

// Title names the pane, and keys its refresh gate.
func (p *busPane) Title() string { return "bus" }

// Help is the footer: the topics, and how to widen a message.
func (p *busPane) Help() string {
	return "tab: topic · j/k: move · / search · W: notify me when a new " +
		"record matches · E: what the companion does · r: read again " +
		"· q: back\n" +
		"decoded against the schema each record names -- puffin " +
		"holds no per-topic code · /slashes/ make a pattern " +
		"a regex"
}

// Load reads a bounded slice of a topic through the cluster, because the
// broker is headless and there is no name to dial from outside.
func (p *busPane) Load(string) tea.Cmd {
	// the read is SLOW -- listing topics, then an eight second consumer
	// window, then a schema fetch per distinct id -- and while it ran the
	// pane said "nothing on - in the last few seconds", which reads as
	// broken rather than busy. Saying so is the whole fix.
	p.loading = true
	ctx := kubeContext()
	topic := ""
	if p.topic < len(p.v.Topics) {
		topic = p.v.Topics[p.topic]
	}
	return func() tea.Msg {
		v := BusView{Read: time.Now(), Topics: p.v.Topics}
		if len(v.Topics) == 0 {
			t, err := busTopics(ctx)
			if err != nil {
				v.Warnings = append(v.Warnings, err.Error())
				return busFetched{v}
			}
			v.Topics = t
		}
		if topic == "" && len(v.Topics) > 0 {
			topic = v.Topics[0]
		}
		v.Topic = topic
		if topic == "" {
			return busFetched{v}
		}
		msgs, err := busRead(ctx, topic, busMaxMessages)
		if err != nil {
			v.Warnings = append(v.Warnings, err.Error())
		}
		// name the fields from the registry: one fetch per distinct
		// schema id, and ids are immutable so the answer is cacheable
		// forever
		seen := map[int]schemaInfo{}
		for i := range msgs {
			id := msgs[i].SchemaID
			if id == 0 {
				continue
			}
			info, ok := seen[id]
			if !ok {
				subject, names, err := fetchSchema(
					ctx,
					id,
					msgs[i].MsgIndex,
				)
				if err != nil {
					v.Warnings = append(
						v.Warnings,
						fmt.Sprintf(
							"schema %d: %v",
							id,
							err,
						),
					)
				}
				info = schemaInfo{
					subject: subject,
					names:   names,
				}
				seen[id] = info
			}
			msgs[i].Schema = info.subject
			nameFields(&msgs[i], info.names)
		}
		v.Messages = msgs
		return busFetched{v}
	}
}

// Update folds in messages or a change of topic. Changing topic reloads,
// because the two topics share nothing worth keeping.
func (p *busPane) Update(msg tea.Msg) (pane, tea.Cmd) {
	switch msg := msg.(type) {
	case busFetched:
		p.v = msg.v
		p.check()
		if p.cursor >= len(p.v.Messages) {
			p.cursor = maxInt(0, len(p.v.Messages)-1)
		}
		p.loading = false
	case tea.KeyMsg:
		if p.typing {
			switch msg.String() {
			case "esc":
				p.typing, p.query = false, ""
				return p, nil
			case "enter":
				p.typing = false
				return p, nil
			case "backspace":
				if p.query != "" {
					p.query = p.query[:len(p.query)-1]
				}
				return p, nil
			}
			if r := msg.String(); len([]rune(r)) == 1 {
				p.query += r
			}
			return p, nil
		}
		switch msg.String() {
		case "/":
			p.typing, p.query = true, ""
			return p, nil
		case "E":
			if p.query != "" {
				cycleWatchEmote(p.query, "bus")
			}
			return p, nil
		case "W":
			if p.query == "" {
				return p, nil
			}
			for _, w := range watchesFor("bus") {
				if w.Pattern == p.query {
					dropWatch(p.query, "bus")
					return p, nil
				}
			}
			addWatch(p.query, "bus")
			enableNotify(true)
			rememberNotify(true)
			return p, nil
		case "tab":
			if len(p.v.Topics) > 0 {
				p.topic = (p.topic + 1) % len(p.v.Topics)
				p.cursor, p.loading = 0, true
				return p, p.Load("")
			}
		case "down", "j":
			if p.cursor < len(p.v.Messages)-1 {
				p.cursor++
			}
		case "up", "k":
			if p.cursor > 0 {
				p.cursor--
			}
		}
	}
	return p, nil
}

// View draws each message decoded against its registered schema, with the
// field names the schema carries rather than field numbers.
func (p *busPane) View(s Styles, width, height int) string {
	var b strings.Builder
	for _, w := range p.v.Warnings {
		b.WriteString(s.Caution.Render("! "+w) + "\n")
	}
	// the topics, with the one being read marked
	for i, t := range p.v.Topics {
		style := s.Dim
		if i == p.topic {
			style = s.AccentAlt
		}
		b.WriteString(style.Render(t))
		if i < len(p.v.Topics)-1 {
			b.WriteString(s.Dim.Render("  ·  "))
		}
	}
	b.WriteString("\n")
	if p.loading {
		// name what is slow and why, because a spinner that says
		// nothing is indistinguishable from a hang
		b.WriteString(s.Dim.Render(
			"reading the bus: kafka has no route, so this runs a "+
				"consumer inside the cluster.\n"+
				"it waits up to 8s for records, then asks "+
				"the registry what each schema is.",
		) + "\n")
		return b.String()
	}
	if len(p.v.Messages) == 0 {
		b.WriteString(
			s.Dim.Render("nothing arrived on " + orDash(p.v.Topic) +
				" · tab for another topic · r reads again"),
		)
		return b.String()
	}
	b.WriteString(
		s.Dim.Render(fmt.Sprintf("%d records", len(p.v.Messages))),
	)
	if p.typing {
		b.WriteString(s.Accent.Render("   /" + p.query + "_"))
	} else if p.query != "" {
		b.WriteString(s.AccentAlt.Render("   /" + p.query))
	}
	if ws := watchesFor("bus"); len(ws) > 0 {
		labels := make([]string, 0, len(ws))
		for _, w := range ws {
			labels = append(labels, w.Label())
		}
		b.WriteString(
			s.Accent.Render(
				"   watching: " + strings.Join(labels, ", "),
			),
		)
	}
	b.WriteString("\n\n")

	size := height - 12
	if height <= 0 {
		size = len(p.v.Messages)
	} else if size < 3 {
		size = 3
	}
	off := windowOffset(len(p.v.Messages), p.cursor, size, p.offset)
	p.offset = off
	last := minInt(off+size, len(p.v.Messages))
	for i := off; i < last; i++ {
		m := p.v.Messages[i]
		on := i == p.cursor
		marker := s.On(s.Row, on).Render("  ")
		if on {
			marker = s.On(s.RowSel, on).Render("▸ ")
		}
		b.WriteString(
			marker + s.On(s.AccentAlt, on).
				Render(pad(shortSchema(m.Schema), 34)),
		)
		if m.Err != "" {
			b.WriteString(s.On(s.Warn, on).Render(m.Err) + "\n")
			continue
		}
		parts := make([]string, 0, len(m.Order))
		for _, k := range m.Order {
			parts = append(parts, k+"="+m.Fields[k])
		}
		b.WriteString(s.On(s.Row, on).Render(
			padTo(
				strings.Join(parts, " "),
				maxInt(20, width-44),
			)) +
			"\n")
	}
	return b.String()
}

// shortSchema drops the package, which is the same on every record and so
// carries no information once you have read it once.
func shortSchema(s string) string {
	if s == "" {
		return "(unframed)"
	}
	if i := strings.LastIndex(s, "."); i >= 0 {
		return s[i+1:]
	}
	return s
}

// jsonObject is a decoded json record, keys in sorted order so a screen that
// refreshes does not reshuffle its own columns.
type jsonObject struct {
	fields map[string]string
	order  []string
}

// decodeJSON reads a record that carries no contract. The keys ARE the field
// names here -- json is self-describing, which is the one thing it has over
// a wire format, and it is enough for a screen.
func decodeJSON(b []byte) (jsonObject, bool) {
	var raw map[string]any
	if json.Unmarshal(b, &raw) != nil || len(raw) == 0 {
		return jsonObject{}, false
	}
	out := jsonObject{fields: make(map[string]string, len(raw))}
	for k, v := range raw {
		out.fields[k] = renderJSONValue(v)
		out.order = append(out.order, k)
	}
	sort.Strings(out.order)
	return out, true
}

// renderJSONValue flattens a value to one line. A nested object is shown as
// its keys rather than expanded: this is a list, not a document viewer.
func renderJSONValue(v any) string {
	switch t := v.(type) {
	case string:
		return oneLine(t, 120)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'g', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	case nil:
		return "null"
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return "{" + strings.Join(keys, " ") + "}"
	case []any:
		return fmt.Sprintf("[%d]", len(t))
	}
	return fmt.Sprintf("%v", v)
}

// Capturing keeps the search box's keystrokes out of the pane's shortcuts.
func (p *busPane) Capturing() bool { return p.typing }

// check tells the watches about records that have arrived. Records are
// identified by their idempotency key, which every message on this bus carries
// -- that is what an idempotency key is for, and it means a watch cannot fire
// twice for the same heartbeat just because the tail was read twice.
func (p *busPane) check() {
	ws := watchesFor("bus")
	if p.seen == nil {
		// the first read is the baseline: opening the pane must not
		// announce fifty records that were already on the bus
		p.seen = map[string]bool{}
		for _, m := range p.v.Messages {
			p.seen[recordKey(m)] = true
		}
		return
	}
	for _, m := range p.v.Messages {
		k := recordKey(m)
		if p.seen[k] {
			continue
		}
		p.seen[k] = true
		if len(ws) == 0 {
			continue
		}
		line := m.Schema + " " + recordLine(m)
		for _, w := range ws {
			if w.Matches(line) {
				Emit(
					Event{
						Kind:    WatchFired,
						Subject: "bus · " + w.Pattern,
						Detail: oneLine(
							line,
							140,
						),
						Emote: w.Emote,
					},
				)
				break
			}
		}
	}
	// the seen set is bounded by what the tail can hold, but a pane left
	// open all night reads the tail hundreds of times: drop keys that have
	// fallen off the end rather than growing a map forever
	if len(p.seen) > 4*busMaxMessages {
		fresh := make(map[string]bool, len(p.v.Messages))
		for _, m := range p.v.Messages {
			fresh[recordKey(m)] = true
		}
		p.seen = fresh
	}
}

// recordKey identifies a record across reads.
func recordKey(m BusMessage) string {
	for _, k := range []string{"idempotency_key", "id", "ts"} {
		if v := m.Fields[k]; v != "" {
			return v
		}
	}
	return recordLine(m)
}

// recordLine is the record as one string, for matching and for notifying.
func recordLine(m BusMessage) string {
	parts := make([]string, 0, len(m.Order))
	for _, k := range m.Order {
		parts = append(parts, k+"="+m.Fields[k])
	}
	return strings.Join(parts, " ")
}
