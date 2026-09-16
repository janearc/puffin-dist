package main

import (
	"strings"
	"unicode"
)

// JSON syntax highlighting, in the theme: a large packet is highlighted
// and opened in a pager rather than dumped.
//
// A hand lexer rather than a library: JSON has five token kinds and a
// dependency has to earn its place.
//
// Colors follow the dataviz discipline -- color carries the token's JOB: keys
// wear the accent (they are the structure), strings the ink (they are the
// content), numbers the caution yellow, booleans and null the beak orange (they
// are decisions), punctuation the dim (it is scaffolding and should recede).

// HighlightJSON colors a pretty-printed JSON document. Input that is not JSON
// comes back unharmed -- the lexer colors what it recognises and passes through
// what it does not, so an error page stays readable. HighlightJSON now reads
// json with a real lexer rather than the hand-rolled scanner below.
//
// The contract is unchanged and still tested: stripped of colour, the output is
// byte-identical to the input. Chroma earns its place by covering the shapes
// the scanner guessed at -- escapes, exponents, nested structures -- and by
// bringing the other languages this estate reads with it.
func HighlightJSON(src string, st Styles) string {
	if out := chromaJSON(src, st); out != "" {
		return out
	}
	return highlightJSONFallback(src, st)
}

// highlightJSONFallback is the original scanner, kept because a highlighter
// that fails closed is better than a screen that fails blank.
func highlightJSONFallback(src string, st Styles) string {
	var b strings.Builder
	i := 0
	n := len(src)
	for i < n {
		c := src[i]
		switch {
		case c == '"':
			// a string: find its end honouring escapes, then decide
			// whether it is a KEY (followed by a colon) or a value
			j := i + 1
			for j < n {
				if src[j] == '\\' {
					j += 2
					continue
				}
				if src[j] == '"' {
					break
				}
				j++
			}
			if j >= n {
				b.WriteString(src[i:])
				return b.String()
			}
			tok := src[i : j+1]
			// peek past whitespace for the colon that makes this a
			// key
			k := j + 1
			for k < n && (src[k] == ' ' || src[k] == '\t') {
				k++
			}
			if k < n && src[k] == ':' {
				b.WriteString(st.Accent.Render(tok))
			} else {
				b.WriteString(st.Row.Render(tok))
			}
			i = j + 1
		case c == '-' || (c >= '0' && c <= '9'):
			j := i
			for j < n && (src[j] == '-' || src[j] == '+' ||
				src[j] == '.' ||
				src[j] == 'e' || src[j] == 'E' ||
				(src[j] >= '0' && src[j] <= '9')) {
				j++
			}
			b.WriteString(st.Caution.Render(src[i:j]))
			i = j
		case strings.HasPrefix(src[i:], "true"),
			strings.HasPrefix(src[i:], "null"):
			b.WriteString(st.Beak.Render(src[i : i+4]))
			i += 4
		case strings.HasPrefix(src[i:], "false"):
			b.WriteString(st.Beak.Render(src[i : i+5]))
			i += 5
		case c == '{' || c == '}' || c == '[' || c == ']' ||
			c == ':' || c == ',':
			b.WriteString(st.Dim.Render(string(c)))
			i++
		case unicode.IsSpace(rune(c)):
			b.WriteByte(c)
			i++
		default:
			// unrecognised: pass through untinted, so non-JSON
			// stays whole
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}
