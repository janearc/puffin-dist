package main

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	dpb "google.golang.org/protobuf/types/descriptorpb"
)

// commentedFDS builds a descriptor with SourceCodeInfo the way protoc/buf
// emits it, so the path-walking is tested against the real grammar.
func commentedFDS() *dpb.FileDescriptorSet {
	return &dpb.FileDescriptorSet{
		File: []*dpb.FileDescriptorProto{{
			Name:    proto.String("f/v1/f.proto"),
			Package: proto.String("f.v1"),
			MessageType: []*dpb.DescriptorProto{
				{
					Name: proto.String("Value"),
					OneofDecl: []*dpb.OneofDescriptorProto{
						{Name: proto.String("kind")},
					},
					Field: []*dpb.FieldDescriptorProto{
						{
							Name: proto.String(
								"bool_value",
							),
							JsonName: proto.String(
								"boolValue",
							),
							Number: proto.Int32(
								1,
							),
							Type: tBool.Enum(),
							OneofIndex: proto.Int32(
								0,
							),
						},
						{
							Name: proto.String(
								"string_value",
							),
							JsonName: proto.String(
								"stringValue",
							),
							Number: proto.Int32(
								2,
							),
							Type: tString.Enum(),
							OneofIndex: proto.Int32(
								0,
							),
						},
					},
				},
				{
					Name: proto.String("SetFlagRequest"),
					Field: []*dpb.FieldDescriptorProto{
						{
							Name: proto.String(
								"reason",
							),
							JsonName: proto.String(
								"reason",
							),
							Number: proto.Int32(
								5,
							),
							Type: tString.Enum(),
						},
						{
							Name: proto.String(
								"value",
							),
							JsonName: proto.String(
								"value",
							),
							Number: proto.Int32(
								4,
							),
							Type: tMsg.Enum(),
							TypeName: proto.String(
								".f.v1.Value",
							),
						},
					},
				},
			},
			SourceCodeInfo: &dpb.SourceCodeInfo{
				Location: []*dpb.SourceCodeInfo_Location{
					// [4,1,2,0]: message_type[1].field[0] =
					// SetFlagRequest.reason
					{Path: []int32{4, 1, 2, 0},
						LeadingComments: proto.String(
							" Who or what is " +
								"flipping " +
								"this, and " +
								"why. " +
								"Recorded " +
								"and shipped " +
								"to kafka.\n " +
								"More prose " +
								"that the " +
								"panel trims.",
						)},
					// [4,0,2,0]: message_type[0].field[0] =
					// Value.bool_value
					{
						Path: []int32{
							4,
							0,
							2,
							0,
						},
						LeadingComments: proto.String(
							" The boolean arm.",
						),
					},
					// a message-level comment, which is NOT
					// a field and must be skipped
					{
						Path: []int32{4, 0},
						LeadingComments: proto.String(
							" The Value message " +
								"itself.",
						),
					},
				},
			},
		}},
	}
}

// TestSchemaDocCarriesTheAuthorsWords checks fields arrive typed, oneofs
// grouped, and comments resolved to their first sentence.
func TestSchemaDocCarriesTheAuthorsWords(t *testing.T) {
	docs := SchemaDoc(commentedFDS(), ".f.v1.SetFlagRequest")
	if len(docs) != 2 {
		t.Fatalf("want 2 fields, got %+v", docs)
	}
	reason := docs[0]
	if reason.Name != "reason" || reason.Type != "string" {
		t.Fatalf("reason doc: %+v", reason)
	}
	if reason.Comment != "Who or what is flipping this, and why." {
		t.Fatalf(
			"comment not trimmed to first sentence: %q",
			reason.Comment,
		)
	}
	value := docs[1]
	if value.Type != "Value" || len(value.Nested) != 2 {
		t.Fatalf("nested message not expanded: %+v", value)
	}
	if value.Nested[0].Oneof != "kind" || value.Nested[1].Oneof != "kind" {
		t.Fatalf("oneof grouping lost: %+v", value.Nested)
	}
	if value.Nested[0].Comment != "The boolean arm." {
		t.Fatalf("nested comment lost: %q", value.Nested[0].Comment)
	}
}

// TestRenderSchemaShowsOneofsAsAChoice checks the panel says "pick exactly
// one" -- the instruction that keeps a composed request valid.
func TestRenderSchemaShowsOneofsAsAChoice(t *testing.T) {
	docs := SchemaDoc(commentedFDS(), ".f.v1.SetFlagRequest")
	out := renderSchema(Compile(DodoDark()), docs, 60)
	for _, want := range []string{
		"pick exactly one",
		"reason",
		"boolValue",
		"Who or what is flipping this",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("panel missing %q", want)
		}
	}
}

// TestFieldPathGrammar covers the walker against paths protoc emits,
// including the ones it must refuse.
func TestFieldPathGrammar(t *testing.T) {
	f := commentedFDS().File[0]
	if name, ok := fieldPath(f, []int32{4, 1, 2, 0}); !ok ||
		name != "SetFlagRequest.reason" {
		t.Fatalf("got %q %v", name, ok)
	}
	for _, bad := range [][]int32{
		{4, 0},          // a message, not a field
		{5, 0, 2, 0},    // not message_type
		{4, 9, 2, 0},    // message index out of range
		{4, 0, 2, 9},    // field index out of range
		{4, 0, 8, 0, 1}, // an unknown descent
	} {
		if _, ok := fieldPath(f, bad); ok {
			t.Errorf("path %v should not resolve", bad)
		}
	}
}

// TestFirstSentence covers the trim.
func TestFirstSentence(t *testing.T) {
	if got := firstSentence(" One. Two. Three."); got != "One." {
		t.Fatalf("got %q", got)
	}
	if got := firstSentence(""); got != "" {
		t.Fatalf("got %q", got)
	}
	long := strings.Repeat("x", 150)
	if got := firstSentence(long); len(got) != 110 {
		t.Fatalf("long comment not capped: %d", len(got))
	}
}
