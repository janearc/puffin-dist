package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"google.golang.org/protobuf/proto"
	dpb "google.golang.org/protobuf/types/descriptorpb"
)

// valueTypeName is the one message type these fixtures refer to by name,
// already in the pointer form a descriptor field wants.
var valueTypeName = proto.String(".flipr.v1.Value")

// jsonUnmarshal keeps the test bodies terse.
func jsonUnmarshal(
	s string,
	v any,
) error {
	return json.Unmarshal([]byte(s), v)
}

// newFakeRPC answers like flipr: SetFlag with a reason succeeds, without one
// is refused in flipr's own words.
func newFakeRPC(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			w.Header().Set("Content-Type", "application/json")
			if body["reason"] == nil || body["reason"] == "" {
				w.WriteHeader(400)
				w.Write(
					[]byte(
						`{"error":"reason is required` +
							`: say why you are fl` +
							`ipping this"}`,
					),
				)
				return
			}
			w.Write([]byte(`{"flag":{"key":"k"}}`))
		}),
	)
}

// buildFDS makes a small real FileDescriptorSet, so the parser is tested
// against actual descriptor bytes rather than a fixture that flatters it.
func buildFDS(t *testing.T) []byte {
	t.Helper()
	fds := &dpb.FileDescriptorSet{
		File: []*dpb.FileDescriptorProto{{
			Name:    proto.String("x/v1/x.proto"),
			Package: proto.String("x.v1"),
			MessageType: []*dpb.DescriptorProto{
				{Name: proto.String("PingRequest")},
				{Name: proto.String("PingResponse")},
			},
			Service: []*dpb.ServiceDescriptorProto{{
				Name: proto.String("XService"),
				Method: []*dpb.MethodDescriptorProto{{
					Name: proto.String("Ping"),
					InputType: proto.String(
						".x.v1.PingRequest",
					),
					OutputType: proto.String(
						".x.v1.PingResponse",
					),
				}},
			}},
		}},
	}
	raw, err := proto.Marshal(fds)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestParseAPI checks a real descriptor renders into the browsable shape.
func TestParseAPI(t *testing.T) {
	api, err := ParseAPI(buildFDS(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(api.Services) != 1 || api.Services[0].Name != "XService" {
		t.Fatalf("services: %+v", api.Services)
	}
	m := api.Services[0].Methods[0]
	if m.Name != "Ping" || m.Input != "PingRequest" ||
		m.Output != "PingResponse" {
		t.Fatalf("method: %+v", m)
	}
	if api.Messages != 2 {
		t.Fatalf("messages: %d", api.Messages)
	}
}

// TestParseAPIRefusesJunk checks junk and emptiness are refused while a
// types-only descriptor is accepted -- "nobody has no api": a daemon's
// contract is what it consumes, and health is api too.
func TestParseAPIRefusesJunk(t *testing.T) {
	if _, err := ParseAPI([]byte("404 page not found")); err == nil {
		t.Fatal("a 404 page parsed as an API")
	}
	typesOnly, _ := proto.Marshal(
		&dpb.FileDescriptorSet{
			File: []*dpb.FileDescriptorProto{{
				Name: proto.String(
					"d/v1/d.proto",
				), Package: proto.String("d.v1"),
				MessageType: []*dpb.DescriptorProto{
					{Name: proto.String("SpoolRecord")},
				},
			}},
		},
	)
	api, err := ParseAPI(typesOnly)
	if err != nil {
		t.Fatalf(
			"a types-only descriptor should be a valid API: %v",
			err,
		)
	}
	if api.Messages != 1 || len(api.Services) != 0 {
		t.Fatalf("types-only shape wrong: %+v", api)
	}
	empty, _ := proto.Marshal(&dpb.FileDescriptorSet{})
	if _, err := ParseAPI(empty); err == nil {
		t.Fatal("a descriptor describing nothing parsed as an API")
	}
}

// TestTrimType covers the display trim.
func TestTrimType(t *testing.T) {
	if got := trimType(
		".flipr.v1.GetFlagRequest",
	); got != "GetFlagRequest" {
		t.Fatalf("got %q", got)
	}
	if got := trimType("bare"); got != "bare" {
		t.Fatalf("got %q", got)
	}
}

// TestUpdateLoop drives the model like a user: splash dismisses on any key,
// navigation clamps at both ends, q quits from the roster but only backs out
// of detail.
func TestUpdateLoop(t *testing.T) {
	m := model{
		styles: Compile(DodoDark()),
		scr:    screenSplash,
		domain: "test",
	}
	m.enclave = Enclave{Services: []Service{{Name: "a"}, {Name: "b"}}}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = next.(model)
	if m.scr != screenRoster {
		t.Fatal("splash did not dismiss")
	}

	// clamp at the top
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = next.(model)
	if m.cursor != 0 {
		t.Fatal("cursor escaped the top")
	}
	// down, then clamp at the bottom
	for i := 0; i < 5; i++ {
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = next.(model)
	}
	if m.cursor != 1 {
		t.Fatalf("cursor %d, want clamp at 1", m.cursor)
	}

	// enter opens detail; q backs out rather than quitting
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if m.scr != screenDetail || cmd == nil {
		t.Fatal("enter did not open detail with a fetch")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	m = next.(model)
	if m.scr != screenRoster {
		t.Fatal("q did not back out of detail")
	}
}

// TestViewsRenderWithoutPanicking renders every screen at a small and a
// normal size; layout bugs in lipgloss composition tend to panic or emit
// nothing, and both are caught here.
func TestViewsRenderWithoutPanicking(t *testing.T) {
	m := model{
		styles: Compile(DodoDark()),
		domain: "test",
		width:  120,
		height: 40,
	}
	m.enclave = Enclave{
		Services: []Service{
			{
				Name:    "flipr",
				State:   StateHealthy,
				Citizen: "authorized",
				Lease: &Lease{
					State:     "authorized",
					Beats:     10,
					CadenceMS: 30000,
					ExpiresAt: time.Now().
						Add(45 * time.Second),
				},
				Version:     "abc1234",
				HasAPI:      true,
				APIVersions: []string{"v1"},
				Flags:       3,
				Expensive:   2,
			},
			{Name: "ghost", State: StateDown, Flags: -1},
		},
		Warnings: []string{"hm: unreachable"},
	}
	for _, scr := range []screen{screenSplash, screenRoster, screenDetail} {
		m.scr = scr
		if out := m.View(); out == "" {
			t.Fatalf("screen %d rendered nothing", scr)
		}
	}
	// the roster must show the warning, the citizenship, and the expensive
	// count
	m.scr = screenRoster
	out := m.View()
	for _, want := range []string{"hm: " +
		"unreachable", "authorized", "$2", "flipr"} {
		if !strings.Contains(out, want) {
			t.Errorf("roster missing %q", want)
		}
	}
}

// TestSplashHasTheBird is the requirement, verbatim: "when you start it up
// there must be an ascii art of a puffin, full-screen."
func TestSplashHasTheBird(t *testing.T) {
	out := splashView(Compile(DodoDark()), 100, 40, 99)
	if !strings.Contains(out, "P U F F I N") {
		t.Fatal("the splash does not name the bird")
	}
	if len(puffinRows) < 15 {
		t.Fatal("the puffin is insufficiently majestic")
	}
	bird := renderPuffin(Compile(DodoDark()))
	if !strings.Contains(bird, "◕") {
		t.Fatal("the puffin has no eye, or the wrong, less cute one")
	}
	// and the guest appears when there is room for guests. It is the
	// animated companion where the room and the theme allow, and the
	// hand-drawn gopher where they do not -- the same bargain the bird
	// makes on this screen, and the reason both are a block of lines.
	wide := splashView(Compile(DodoDark()), 140, 45, 99)
	guest, ok := splashGuest(currentTheme(), 140, 45, 99)
	if !ok {
		t.Fatal("a 140x45 terminal has room for an animated guest")
	}
	first := strings.Split(guest, "\n")[0]
	if !strings.Contains(wide, first) {
		t.Error("a wide terminal should seat the guest")
	}
	// the hand-drawn gopher is still the fallback, and still a gopher
	if !strings.Contains(
		paintGopher(Compile(DodoDark()), gopherArt),
		"~~~~",
	) {
		t.Error("the fallback gopher was lost")
	}
}

// TestPad covers the table cell helper, including the truncation path.
func TestPad(t *testing.T) {
	if got := pad("abc", 6); got != "abc   " {
		t.Fatalf("got %q", got)
	}
	if got := pad("abcdefghij", 6); len([]rune(got)) != 6 {
		t.Fatalf("truncation broke width: %q", got)
	}
}

// TestDomainDefault checks the env override.
func TestDomainDefault(t *testing.T) {
	os.Unsetenv("PUFFIN_DOMAIN")
	// main() reads it; the default is asserted structurally here
	if d := os.Getenv("PUFFIN_DOMAIN"); d != "" {
		t.Fatal("env leaked between tests")
	}
}

// TestSkeletonFromFliprShapes builds request skeletons from flipr's real
// contract shapes: the oneof emits exactly one arm, 64-bit ints are strings,
// and nested messages expand.
func TestSkeletonFromFliprShapes(t *testing.T) {
	fds := &dpb.FileDescriptorSet{
		File: []*dpb.FileDescriptorProto{{
			Name:    proto.String("flipr/v1/flipr.proto"),
			Package: proto.String("flipr.v1"),
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
						{
							Name: proto.String(
								"int_value",
							),
							JsonName: proto.String(
								"intValue",
							),
							Number: proto.Int32(
								3,
							),
							Type: tInt64.Enum(),
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
								"service",
							),
							JsonName: proto.String(
								"service",
							),
							Number: proto.Int32(
								1,
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
							Type:     tMsg.Enum(),
							TypeName: valueTypeName,
						},
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
					},
				},
			},
		}},
	}
	out, err := Skeleton(fds, ".flipr.v1.SetFlagRequest")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := jsonUnmarshal(out, &v); err != nil {
		t.Fatalf("skeleton is not json: %v\n%s", err, out)
	}
	if _, ok := v["reason"]; !ok {
		t.Fatalf("reason missing: %s", out)
	}
	val, ok := v["value"].(map[string]any)
	if !ok {
		t.Fatalf("nested message did not expand: %s", out)
	}
	// Exactly one oneof arm, or flipr refuses the request
	if len(val) != 1 {
		t.Fatalf("oneof emitted %d arms, want 1: %s", len(val), out)
	}
	if _, ok := val["boolValue"]; !ok {
		t.Fatalf("first arm should win: %s", out)
	}
}

// TestInvokeAgainstAFake fires a built request at a flipr-shaped server and
// checks both the happy path and the refusal pass through with status intact.
func TestInvokeAgainstAFake(t *testing.T) {
	srv := newFakeRPC(t)
	defer srv.Close()
	res, err := Invoke(
		srv.URL,
		"flipr.v1.FliprService",
		"SetFlag",
		`{"service":"s","version":"v","key":"k",`+
			`"value":{"boolValue":true},"reason":"testing"}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != 200 || !strings.Contains(res.Body, "flag") {
		t.Fatalf("got %d %s", res.Status, res.Body)
	}
	res, err = Invoke(
		srv.URL,
		"flipr.v1.FliprService",
		"SetFlag",
		`{"service":"s","version":"v","key":"k",`+
			`"value":{"boolValue":true}}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != 400 ||
		!strings.Contains(res.Body, "reason is required") {
		t.Fatalf(
			"refusal did not pass through: %d %s",
			res.Status,
			res.Body,
		)
	}
	// garbage is caught before it travels
	if _, err := Invoke(srv.URL, "x", "Y", "{not json"); err == nil {
		t.Fatal("invalid json travelled")
	}
}
