package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/lipgloss"
)

// Highlighting logs.
//
// The rule here is structured JSON logging, so a good share of these
// lines ARE json and chroma's json lexer reads them properly -- keys,
// strings, numbers, punctuation, from a real parser rather than a guess.
//
// The rest are not json and never will be: coredns, zookeeper and traefik write
// what they write.
//
// For those, the highlighting is deliberately small -- level, timestamp, quoted
// string, number, path, ip -- because a line where everything is coloured is a
// line where nothing is emphasised, and the point of colour here is to make a
// level and a number findable at a glance in a wall of text.
//
// Colours come from the theme, not from a chroma style: a log pane that
// ignores the palette everything else obeys is the thing the theme exists
// to stop.

// No regexes.
//
// A log line is scanned left to right by a machine that already knows what it
// is looking at; a pattern language buys nothing here and costs plenty --
// backtracking on adversarial input, rules nobody can read six months later,
// and the particular misery of a character class that was almost right.
//
// So these are scanners. Each one answers a yes/no question about a token or
// consumes a field from a known position, and every one of them is a
// function you can step through.

// levels are the words worth finding at a glance in a wall of text.
var levels = map[string]bool{
	"error": true, "err": true, "fatal": true, "panic": true,
	"warn":    true,
	"warning": true,
	"info":    true,
	"debug":   true,
	"trace":   true,
}

// isDigits is the scanner's test for a number, which is the token a
// reader is most often hunting for in a wall of log text.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// isNumberish covers 200, 12.5, 340ms, 87% -- a number with an optional unit.
func isNumberish(s string) bool {
	i := 0
	for i < len(s) &&
		(s[i] >= '0' && s[i] <= '9' || s[i] == '.' || s[i] == '-') {
		i++
	}
	if i == 0 {
		return false
	}
	switch strings.ToLower(s[i:]) {
	case "", "ms", "s", "m", "h", "kb", "mb", "gb", "%":
		return true
	}
	return false
}

// isIPish is a dotted quad with an optional port. Deliberately not a
// validator: this decides a colour, and 999.1.1.1 being painted like an
// address costs nothing.
func isIPish(s string) bool {
	if i := strings.LastIndex(s, ":"); i > 0 && isDigits(s[i+1:]) {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if !isDigits(p) || len(p) > 3 {
			return false
		}
	}
	return true
}

// isPathish is an absolute path with at least two segments.
func isPathish(s string) bool {
	return strings.HasPrefix(s, "/") && strings.Count(s, "/") >= 2 &&
		!strings.Contains(s, " ")
}

// isTimeish covers 00:26:45 and 00:26:45.416 -- the clock, not the date.
func isTimeish(s string) bool {
	s = strings.TrimSuffix(s, "Z")
	if i := strings.Index(s, "."); i >= 0 {
		if !isDigits(s[i+1:]) {
			return false
		}
		s = s[:i]
	}
	f := strings.Split(s, ":")
	if len(f) != 3 {
		return false
	}
	for _, p := range f {
		if len(p) != 2 || !isDigits(p) {
			return false
		}
	}
	return true
}

// isDateish covers 2026-08-30 and 2026/08/30, and the ISO form joined to a
// clock by a T.
func isDateish(s string) bool {
	if i := strings.IndexAny(s, "T"); i > 0 && isDateish(s[:i]) {
		return true
	}
	sep := ""
	switch {
	case strings.Count(s, "-") == 2:
		sep = "-"
	case strings.Count(s, "/") == 2:
		sep = "/"
	default:
		return false
	}
	f := strings.Split(s, sep)
	return len(f) == 3 && isDigits(f[0]) && isDigits(f[1]) &&
		isDigits(f[2]) &&
		len(f[0]) == 4
}

// isMonth is the three-letter month a syslog line starts with.
func isMonth(s string) bool {
	switch s {
	case "Jan", "Feb", "Mar", "Apr", "May", "Jun",
		"Jul", "Aug", "Sep", "Oct", "Nov", "Dec":
		return true
	}
	return false
}

// splitTag pulls "sshd[1234]:" apart into tag and pid, and says whether the
// token was tag-shaped at all -- it must end in a colon.
func splitTag(tok string) (tag, pid string, ok bool) {
	if !strings.HasSuffix(tok, ":") {
		return "", "", false
	}
	tok = strings.TrimSuffix(tok, ":")
	if i := strings.IndexByte(tok, '['); i > 0 &&
		strings.HasSuffix(tok, "]") {
		pid = tok[i+1 : len(tok)-1]
		if !isDigits(pid) {
			return "", "", false
		}
		tok = tok[:i]
	}
	if tok == "" || strings.ContainsAny(tok, " \t") {
		return "", "", false
	}
	return tok, pid, true
}

// highlightSyslog paints a syslog line by field, which is how the tools that
// do this well work -- chroma has no syslog lexer at all, and lnav and ccze
// use per-format field rules rather than a grammar. The host is the host
// wherever it appears, and a number in the message is not a pid.
//
//	Aug 30 00:26:45 gluttony sshd[1234]: Accepted publickey
//	2026-08-30T00:26:45Z gluttony kernel: oom
func highlightSyslog(line string, s Styles) (string, bool) {
	var b strings.Builder
	rest := line
	// a forwarded line still carries its priority: <134>
	if strings.HasPrefix(rest, "<") {
		if i := strings.IndexByte(rest, '>'); i > 1 && i <= 4 &&
			isDigits(rest[1:i]) {
			b.WriteString(s.Dim.Render(rest[:i+1]))
			rest = rest[i+1:]
		}
	}
	f := strings.Fields(rest)
	// the timestamp is either three fields (Aug 30 00:26:45) or one (ISO)
	n := 0
	switch {
	case len(f) >= 3 && isMonth(f[0]) && isDigits(f[1]) && isTimeish(f[2]):
		n = 3
	case len(f) >= 1 && isDateish(f[0]):
		n = 1
	default:
		return "", false
	}
	if len(f) < n+2 {
		return "", false
	}
	tag, pid, ok := splitTag(f[n+1])
	if !ok {
		return "", false
	}
	ts := strings.Join(f[:n], " ")
	host := f[n]
	// rebuilt from the original string rather than from the fields, so the
	// spacing the line actually had survives: this highlighter is not
	// allowed to change content, and Fields eats runs of spaces
	at := strings.Index(rest, ts)
	b.WriteString(s.Row.Render(rest[:at]))
	b.WriteString(s.Dim.Render(ts))
	rest = rest[at+len(ts):]
	gap := len(rest) - len(strings.TrimLeft(rest, " "))
	b.WriteString(s.Row.Render(rest[:gap]))
	rest = rest[gap:]
	b.WriteString(s.AccentAlt.Render(host))
	rest = rest[len(host):]
	gap = len(rest) - len(strings.TrimLeft(rest, " "))
	b.WriteString(s.Row.Render(rest[:gap]))
	rest = rest[gap:]
	b.WriteString(s.Accent.Render(tag))
	rest = rest[len(tag):]
	if pid != "" {
		b.WriteString(s.Dim.Render("[" + pid + "]"))
		rest = rest[len(pid)+2:]
	}
	b.WriteString(s.Dim.Render(":"))
	rest = rest[1:]
	b.WriteString(highlightPlain(rest, s))
	return b.String(), true
}

// HighlightLog paints one log line. Stripped of colour the output is
// byte-identical to the input -- the same contract the json highlighter has
// always held, and the reason it can be trusted on a screen people read for
// facts rather than for decoration.
func HighlightLog(line string, s Styles) string {
	if j, ok := asJSON(line); ok {
		return HighlightJSON(j, s)
	}
	if out, ok := highlightSyslog(line, s); ok {
		return out
	}
	return highlightPlain(line, s)
}

// highlightPlain is the fallback for lines with no structure to read: the
// small set of things worth finding at a glance, and nothing else. A line
// where everything is coloured is a line where nothing is emphasised.
func highlightPlain(line string, s Styles) string {
	if line == "" {
		return ""
	}
	var b strings.Builder
	i := 0
	for i < len(line) {
		// runs of spaces are written through untouched: the spacing is
		// content and this highlighter does not change content
		if line[i] == ' ' {
			j := i
			for j < len(line) && line[j] == ' ' {
				j++
			}
			b.WriteString(line[i:j])
			i = j
			continue
		}
		j := i
		for j < len(line) && line[j] != ' ' {
			j++
		}
		b.WriteString(styleForToken2(line[i:j], s).Render(line[i:j]))
		i = j
	}
	return b.String()
}

// styleForToken2 classifies one whitespace-delimited token. Order is the
// specific before the general, and every branch is a function you can step
// through rather than a character class you have to squint at.
func styleForToken2(tok string, s Styles) lipgloss.Style {
	bare := strings.Trim(tok, `"',;()[]{}=`)
	switch {
	case levels[strings.ToLower(strings.TrimSuffix(bare, ":"))]:
		return s.Warn
	case strings.HasPrefix(
		tok,
		`"`,
	) && strings.HasSuffix(tok, `"`) && len(tok) > 1:
		return s.Ok
	case isIPish(bare):
		return s.AccentAlt
	case isPathish(bare):
		return s.AccentAlt
	case isTimeish(bare), isDateish(bare):
		return s.Dim
	case isNumberish(bare):
		return s.Accent
	}
	return s.Row
}

// asJSON says whether a line is a json object, and hands back the object
// itself when the line merely contains one -- many services print a prefix
// and then their structured payload, and the payload is the part worth
// reading properly.
func asJSON(line string) (string, bool) {
	t := strings.TrimSpace(line)
	if i := strings.Index(t, "{"); i >= 0 {
		cand := t[i:]
		var v map[string]any
		if json.Unmarshal([]byte(cand), &v) == nil && len(v) > 0 {
			return cand, true
		}
	}
	return "", false
}

// chromaJSON is chroma reading json properly, mapped onto the theme. It is
// used by HighlightJSON, which keeps its old name and contract.
func chromaJSON(src string, s Styles) string {
	lexer := lexers.Get("json")
	if lexer == nil {
		return src
	}
	it, err := lexer.Tokenise(nil, src)
	if err != nil {
		return src
	}
	var b strings.Builder
	for _, t := range it.Tokens() {
		st := styleForToken(t.Type, s)
		// a token is rendered LINE BY LINE and the newlines are written
		// raw.
		//
		// lipgloss pads a multi-line string out to its widest line, so
		// handing it a token that spans lines silently inserts trailing
		// spaces -- which is content, and this highlighter is not
		// allowed to change content.
		parts := strings.Split(t.Value, "\n")
		for i, part := range parts {
			if part != "" {
				b.WriteString(st.Render(part))
			}
			if i < len(parts)-1 {
				b.WriteString("\n")
			}
		}
	}
	out := b.String()
	// chroma appends a newline the source did not have (EnsureNL), which
	// breaks the highlighter's one hard law: stripped of colour, the output
	// is byte-identical to the input.
	//
	// A highlighter that edits content is a liar with good taste, and this
	// one edits it by exactly one byte -- which is the kind of difference
	// that survives review.
	if !strings.HasSuffix(src, "\n") && strings.HasSuffix(out, "\n") {
		out = strings.TrimSuffix(out, "\n")
	}
	return out
}

// styleForToken maps chroma's token classes onto the estate's palette.
func styleForToken(t chroma.TokenType, s Styles) lipgloss.Style {
	switch {
	case t.InCategory(chroma.Keyword), t == chroma.KeywordConstant:
		return s.Accent
	case t == chroma.NameTag,
		t == chroma.NameAttribute,
		t.InCategory(chroma.Name):
		return s.AccentAlt
	case t.InCategory(chroma.Literal):
		if t.InSubCategory(chroma.LiteralNumber) {
			return s.Accent
		}
		return s.Ok
	case t.InCategory(chroma.Comment):
		return s.Dim
	case t.InCategory(chroma.Punctuation), t.InCategory(chroma.Operator):
		return s.Dim
	case t.InCategory(chroma.Error):
		return s.Warn
	}
	return s.Row
}

// Structured lines are reordered before they are shown.
//
// The estate's rule is structured json logging, and json objects have no
// meaningful order -- most emitters write their keys alphabetically.
//
// flipr's startup line begins with "addr" and "db" and buries "msg" in the
// middle, so a pane that prints the raw object and truncates it at the
// terminal's width shows you the listen address and loses the sentence.
//
// Colouring that line more carefully does not help: the problem is which end
// got cut off.
//
// So the fields that answer "what happened" come first, in a fixed order,
// and everything else follows as k=v. The timestamp is dropped because the
// pane already has a column for it, and repeating it costs the width that
// the message needs.

// leadFields come first, in this order, when a line has them.
var leadFields = []string{
	"level",
	"severity",
	"lvl",
	"msg",
	"message",
	"error",
	"err",
}

// dropFields are already shown elsewhere, or are never the answer.
var dropFields = map[string]bool{
	"ts": true, "time": true, "timestamp": true, "@timestamp": true,
	"@version": true, "caller": true,
}

// structuredLine turns a json log object into a readable line, and says
// whether it was one at all.
func structuredLine(raw string) (level, text string, ok bool) {
	obj, found := asJSON(raw)
	if !found {
		return "", "", false
	}
	var m map[string]any
	if json.Unmarshal([]byte(obj), &m) != nil || len(m) == 0 {
		return "", "", false
	}
	var b strings.Builder
	used := map[string]bool{}
	for _, k := range leadFields {
		v, has := m[k]
		if !has {
			continue
		}
		used[k] = true
		s := flatten(v)
		if s == "" {
			continue
		}
		switch k {
		case "level", "severity", "lvl":
			level = s
			// the level is returned separately, to be coloured
			continue
		}
		if b.Len() > 0 {
			b.WriteString("  ")
		}
		b.WriteString(s)
	}
	// everything else, alphabetically, so the same line looks the same
	// every time it appears
	rest := make([]string, 0, len(m))
	for k := range m {
		if !used[k] && !dropFields[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		v := flatten(m[k])
		if v == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("  ")
		}
		b.WriteString(k + "=" + v)
	}
	return level, b.String(), true
}

// flatten renders one value on one line. A nested object becomes its keys
// rather than its contents: this is a list, and the whole object is one
// keystroke away in the pager.
func flatten(v any) string {
	switch t := v.(type) {
	case string:
		return oneLine(t, 160)
	case bool:
		return strconv.FormatBool(t)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'g', -1, 64)
	case nil:
		return ""
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return "{" + strings.Join(keys, ",") + "}"
	case []any:
		return fmt.Sprintf("[%d]", len(t))
	}
	return fmt.Sprintf("%v", v)
}

// levelStyle colours a log level. Only the ones that mean something get a
// colour: a line where every field is coloured is a line where nothing is
// emphasised.
func levelStyle(s Styles, level string) lipgloss.Style {
	switch strings.ToLower(level) {
	case "error", "err", "fatal", "panic", "critical":
		return s.Warn
	case "warn", "warning":
		return s.Caution
	case "debug", "trace":
		return s.Dim
	}
	return s.Ok
}
