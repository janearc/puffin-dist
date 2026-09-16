package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	dpb "google.golang.org/protobuf/types/descriptorpb"
)

// Acting on the enclave, not only looking at it.
//
// The descriptor that lets puffin render a contract also carries every input
// message's shape -- so puffin builds a protojson request skeleton from the
// schema, hands it over for editing, and fires it at the method. Zero
// per-service code, same premise as viewing: requirement 4 pays out twice.

// Skeleton builds an editable protojson request body for a message type,
// with every field present at its zero value so the shape teaches itself.
//
// Rules that keep the output valid protojson rather than merely plausible:
// oneofs emit only their first arm (protojson refuses two arms of one
// oneof); recursion is depth-capped and cycle-guarded; int64/uint64 render
// as strings per protojson; maps render empty.
func Skeleton(
	fds *dpb.FileDescriptorSet,
	messageFQN string,
) (string, error) {
	// no descriptor is an answer, not a crash: a member can be listed from
	// discovery before its /api has been read
	if fds == nil {
		return "", fmt.Errorf("no descriptor for %s", messageFQN)
	}
	idx := indexMessages(fds)
	m, ok := idx[strings.TrimPrefix(messageFQN, ".")]
	if !ok {
		return "", fmt.Errorf(
			"message %s not in the descriptor",
			messageFQN,
		)
	}
	v := messageSkeleton(m, idx, map[string]bool{}, 0)
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// indexMessages maps fully-qualified names to descriptors, nested types
// included.
func indexMessages(
	fds *dpb.FileDescriptorSet,
) map[string]*dpb.DescriptorProto {
	idx := map[string]*dpb.DescriptorProto{}
	var walk func(prefix string, msgs []*dpb.DescriptorProto)
	walk = func(prefix string, msgs []*dpb.DescriptorProto) {
		for _, m := range msgs {
			fqn := prefix + m.GetName()
			idx[fqn] = m
			walk(fqn+".", m.NestedType)
		}
	}
	for _, f := range fds.File {
		prefix := ""
		if f.GetPackage() != "" {
			prefix = f.GetPackage() + "."
		}
		walk(prefix, f.MessageType)
	}
	return idx
}

// messageSkeleton builds the zero-valued object for one message.
func messageSkeleton(
	m *dpb.DescriptorProto,
	idx map[string]*dpb.DescriptorProto,
	seen map[string]bool,
	depth int,
) map[string]any {

	out := map[string]any{}
	seenOneof := map[int32]bool{}
	for _, f := range m.Field {
		// one arm per oneof: the first declared wins the skeleton
		if f.OneofIndex != nil {
			if seenOneof[*f.OneofIndex] {
				continue
			}
			seenOneof[*f.OneofIndex] = true
		}
		name := f.GetJsonName()
		if name == "" {
			name = f.GetName()
		}
		v := fieldZero(f, idx, seen, depth)
		if f.GetLabel() == dpb.FieldDescriptorProto_LABEL_REPEATED {
			// maps arrive as repeated MapEntry messages; render
			// them as {}
			if mt, ok := idx[strings.TrimPrefix(
				f.GetTypeName(),
				".",
			)]; ok &&
				mt.GetOptions().GetMapEntry() {
				out[name] = map[string]any{}
				continue
			}
			out[name] = []any{v}
			continue
		}
		out[name] = v
	}
	return out
}

// fieldZero renders one field's zero value in protojson's dialect.
func fieldZero(
	f *dpb.FieldDescriptorProto,
	idx map[string]*dpb.DescriptorProto,
	seen map[string]bool,
	depth int,
) any {

	switch f.GetType() {
	case dpb.FieldDescriptorProto_TYPE_BOOL:
		return false
	case dpb.FieldDescriptorProto_TYPE_STRING:
		return ""
	case dpb.FieldDescriptorProto_TYPE_BYTES:
		return ""
	case dpb.FieldDescriptorProto_TYPE_INT64,
		dpb.FieldDescriptorProto_TYPE_UINT64,
		dpb.FieldDescriptorProto_TYPE_SINT64,
		dpb.FieldDescriptorProto_TYPE_FIXED64,
		dpb.FieldDescriptorProto_TYPE_SFIXED64:
		return "0" // protojson renders 64-bit as strings
	case dpb.FieldDescriptorProto_TYPE_DOUBLE,
		dpb.FieldDescriptorProto_TYPE_FLOAT,
		dpb.FieldDescriptorProto_TYPE_INT32,
		dpb.FieldDescriptorProto_TYPE_UINT32,
		dpb.FieldDescriptorProto_TYPE_SINT32,
		dpb.FieldDescriptorProto_TYPE_FIXED32,
		dpb.FieldDescriptorProto_TYPE_SFIXED32:
		return 0
	case dpb.FieldDescriptorProto_TYPE_ENUM:
		return ""
	case dpb.FieldDescriptorProto_TYPE_MESSAGE,
		dpb.FieldDescriptorProto_TYPE_GROUP:
		fqn := strings.TrimPrefix(f.GetTypeName(), ".")
		if depth >= 4 || seen[fqn] {
			// cycle or depth guard: an empty object is valid
			return map[string]any{}
		}
		if mt, ok := idx[fqn]; ok {
			seen[fqn] = true
			v := messageSkeleton(mt, idx, seen, depth+1)
			delete(seen, fqn)
			return v
		}
		return map[string]any{}
	default:
		return nil
	}
}

// InvokeResult is what came back from firing a method.
type InvokeResult struct {
	Status int
	Body   string
}

// Invoke fires one method with an edited protojson body. The response body
// is pretty-printed when it parses and shown verbatim when it does not --
// an error page shown honestly beats a parse error hiding it.
func Invoke(base, serviceFQN, method, body string) (InvokeResult, error) {
	// the body must at least be JSON before it travels; catching the typo
	// here gives a better message than the wire's
	var probe any
	if err := json.Unmarshal([]byte(body), &probe); err != nil {
		return InvokeResult{}, fmt.Errorf(
			"request body is not valid JSON: %w",
			err,
		)
	}
	url := fmt.Sprintf("%s/%s/%s", base, serviceFQN, method)
	resp, err := client.Post(
		url,
		"application/json",
		strings.NewReader(body),
	)
	if err != nil {
		return InvokeResult{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return InvokeResult{}, err
	}
	out := InvokeResult{Status: resp.StatusCode, Body: string(raw)}
	var buf bytes.Buffer
	if json.Indent(&buf, raw, "", "  ") == nil {
		out.Body = buf.String()
	}
	return out, nil
}
