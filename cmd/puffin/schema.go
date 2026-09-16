package main

import (
	"fmt"
	"strings"

	dpb "google.golang.org/protobuf/types/descriptorpb"
)

// The field type constants, at a length a nested literal can carry. The
// generated names are forty characters and say the same thing.
const (
	tBool   = dpb.FieldDescriptorProto_TYPE_BOOL
	tString = dpb.FieldDescriptorProto_TYPE_STRING
	tInt64  = dpb.FieldDescriptorProto_TYPE_INT64
	tMsg    = dpb.FieldDescriptorProto_TYPE_MESSAGE
	lRepeat = dpb.FieldDescriptorProto_LABEL_REPEATED
)

// The composer's schema view: open the composer and the schema it expects
// is in front of you.
//
// The descriptor carries everything an author ever said about a message: field
// names, types, labels, oneof groupings -- and, because buf keeps
// SourceCodeInfo, the leading comments from the .proto itself.
//
// So the composer shows the contract the way its author wrote it, comments
// included, next to the request being built. Nobody composes blind.

// FieldDoc is one field, documented.
type FieldDoc struct {
	Name     string // json name, the one the composer types
	Type     string // human-readable type
	Repeated bool
	Oneof    string // the oneof group's name, "" when not in one
	Comment  string // the author's leading comment, first sentence
	Nested   []FieldDoc
}

// SchemaDoc documents a message's fields for the composer, comments
// resolved, nested messages expanded one level.
func SchemaDoc(
	fds *dpb.FileDescriptorSet,
	messageFQN string,
) []FieldDoc {
	idx := indexMessages(fds)
	comments := indexComments(fds)
	fqn := strings.TrimPrefix(messageFQN, ".")
	m, ok := idx[fqn]
	if !ok {
		return nil
	}
	return fieldDocs(fqn, m, idx, comments, 0)
}

// fieldDocs renders one message's fields.
func fieldDocs(
	fqn string,
	m *dpb.DescriptorProto,
	idx map[string]*dpb.DescriptorProto,
	comments map[string]string,
	depth int,
) []FieldDoc {

	var out []FieldDoc
	for _, f := range m.Field {
		name := f.GetJsonName()
		if name == "" {
			name = f.GetName()
		}
		d := FieldDoc{
			Name:     name,
			Type:     humanType(f),
			Repeated: f.GetLabel() == lRepeat,
			Comment:  firstSentence(comments[fqn+"."+f.GetName()]),
		}
		if f.OneofIndex != nil &&
			int(*f.OneofIndex) < len(m.OneofDecl) {
			d.Oneof = m.OneofDecl[*f.OneofIndex].GetName()
		}
		if f.GetType() == tMsg &&
			depth < 2 {
			nfqn := strings.TrimPrefix(f.GetTypeName(), ".")
			if nm, ok := idx[nfqn]; ok &&
				!nm.GetOptions().GetMapEntry() {
				d.Nested = fieldDocs(
					nfqn,
					nm,
					idx,
					comments,
					depth+1,
				)
			}
		}
		out = append(out, d)
	}
	return out
}

// humanType renders a field's type the way a person says it.
func humanType(f *dpb.FieldDescriptorProto) string {
	switch f.GetType() {
	case tMsg,
		dpb.FieldDescriptorProto_TYPE_GROUP,
		dpb.FieldDescriptorProto_TYPE_ENUM:
		return trimType(f.GetTypeName())
	default:
		return strings.TrimPrefix(
			strings.ToLower(f.GetType().String()),
			"type_",
		)
	}
}

// firstSentence trims a proto comment to its opening sentence, compacted.
func firstSentence(c string) string {
	c = strings.TrimSpace(strings.ReplaceAll(c, "\n", " "))
	if c == "" {
		return ""
	}
	if i := strings.Index(c, ". "); i > 0 {
		c = c[:i+1]
	}
	if len(c) > 110 {
		c = c[:107] + "..."
	}
	return c
}

// indexComments maps message-field paths ("pkg.Message.field") to their
// leading comments, walked out of SourceCodeInfo.
//
// The path grammar, for whoever reads this next: a location path is a walk
// through the FileDescriptorProto by field number, so [4, mi] is
// message_type[mi], [4, mi, 2, fi] is its field[fi], and [4, mi, 3, ni, ...]
// descends into nested_type[ni].
//
// Field 4 is message_type, 2 is field, 3 is nested_type -- the numbers come
// from descriptor.proto itself.
func indexComments(fds *dpb.FileDescriptorSet) map[string]string {
	out := map[string]string{}
	for _, f := range fds.File {
		if f.SourceCodeInfo == nil {
			continue
		}
		prefix := ""
		if f.GetPackage() != "" {
			prefix = f.GetPackage() + "."
		}
		for _, loc := range f.SourceCodeInfo.Location {
			c := strings.TrimSpace(loc.GetLeadingComments())
			if c == "" {
				continue
			}
			if name, ok := fieldPath(f, loc.Path); ok {
				out[prefix+name] = c
			}
		}
	}
	return out
}

// fieldPath resolves a source location path to "Message.field" when the
// path names a field, descending nested types as needed.
func fieldPath(
	f *dpb.FileDescriptorProto,
	path []int32,
) (string, bool) {
	// minimum shape: [4, mi, 2, fi]
	if len(path) < 4 || path[0] != 4 {
		return "", false
	}
	if int(path[1]) >= len(f.MessageType) {
		return "", false
	}
	m := f.MessageType[path[1]]
	name := m.GetName()
	rest := path[2:]
	for len(rest) >= 2 {
		switch rest[0] {
		case 2: // field
			if len(rest) != 2 || int(rest[1]) >= len(m.Field) {
				return "", false
			}
			return fmt.Sprintf(
				"%s.%s",
				name,
				m.Field[rest[1]].GetName(),
			), true
		case 3: // nested_type
			if int(rest[1]) >= len(m.NestedType) {
				return "", false
			}
			m = m.NestedType[rest[1]]
			name = name + "." + m.GetName()
			rest = rest[2:]
		default:
			return "", false
		}
	}
	return "", false
}

// renderSchema draws the doc panel for the composer.
func renderSchema(s Styles, docs []FieldDoc, width int) string {
	var b strings.Builder
	b.WriteString(
		s.Header.Render("the contract, in its author's words") + "\n",
	)
	var walk func(docs []FieldDoc, indent string)
	walk = func(docs []FieldDoc, indent string) {
		lastOneof := ""
		for _, d := range docs {
			if d.Oneof != "" && d.Oneof != lastOneof {
				b.WriteString(
					indent + s.Caution.Render(
						"one of ("+d.Oneof+") — pick "+
							"exactly one:",
					) + "\n",
				)
			}
			lastOneof = d.Oneof
			pre := indent
			if d.Oneof != "" {
				pre += "  "
			}
			t := d.Type
			if d.Repeated {
				t = "[]" + t
			}
			line := pre + s.Accent.Render(
				d.Name,
			) + " " + s.Dim.Render(
				t,
			)
			b.WriteString(line + "\n")
			if d.Comment != "" {
				b.WriteString(
					pre + "  " + s.Help.Render(
						d.Comment,
					) + "\n",
				)
			}
			if len(d.Nested) > 0 {
				walk(d.Nested, pre+"  ")
			}
		}
	}
	walk(docs, "")
	_ = width
	return b.String()
}
