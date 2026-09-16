package main

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"
	// dpb, because the generated names in this package run to
	// forty characters and they are the subject of every line
	// that uses them.
	dpb "google.golang.org/protobuf/types/descriptorpb"
)

// The universal-client half: every enclave member serves its contract at /api
// as a FileDescriptorSet, so puffin renders any service's API without
// per-service code.
//
// This is container-test requirement 4 collecting its payoff -- the reason a
// service answers "what is your API" itself is so a tool like this never has to
// be told.

// API is one service's parsed contract.
type API struct {
	Services []APIService
	Messages int
	Bytes    int
	// FDS is the raw descriptor set, kept so invocation can build request
	// skeletons from the same bytes the listing came from.
	FDS *dpb.FileDescriptorSet
	// Versions are the proto package versions published, deduplicated and
	// sorted: the "v1" in flipr.v1. The roster shows these because an API
	// version is a promise, and promises belong on the first display.
	Versions []string
}

// APIService is one proto service and its methods.
type APIService struct {
	Name string
	// package-qualified: the route segment protojson serves on
	FullName string
	Methods  []APIMethod
}

// APIMethod is one rpc: name, input and output types trimmed for humans,
// and the input's fully-qualified name for the skeleton builder.
type APIMethod struct {
	Name     string
	Input    string
	Output   string
	InputFQN string
}

// FetchAPI pulls and parses a service's descriptor.
func FetchAPI(base string) (*API, error) {
	resp, err := client.Get(base + "/api")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("/api answered %d", resp.StatusCode)
	}
	return ParseAPI(raw)
}

// ParseAPI decodes a FileDescriptorSet into the browsable shape.
func ParseAPI(raw []byte) (*API, error) {
	var fds dpb.FileDescriptorSet
	if err := proto.Unmarshal(raw, &fds); err != nil {
		return nil, fmt.Errorf("not a FileDescriptorSet: %w", err)
	}
	out := &API{Bytes: len(raw), FDS: &fds}
	seenV := map[string]bool{}
	for _, f := range fds.File {
		out.Messages += len(f.MessageType)
		// the version is the package's trailing vN segment, when it has
		// one
		if parts := strings.Split(f.GetPackage(), "."); len(parts) > 0 {
			last := parts[len(parts)-1]
			if len(last) >= 2 && last[0] == 'v' && last[1] >= '0' &&
				last[1] <= '9' &&
				!seenV[last] {
				seenV[last] = true
				out.Versions = append(out.Versions, last)
			}
		}
		for _, svc := range f.Service {
			full := svc.GetName()
			if f.GetPackage() != "" {
				full = f.GetPackage() + "." + svc.GetName()
			}
			s := APIService{Name: svc.GetName(), FullName: full}
			for _, m := range svc.Method {
				s.Methods = append(s.Methods, APIMethod{
					Name:     m.GetName(),
					Input:    trimType(m.GetInputType()),
					Output:   trimType(m.GetOutputType()),
					InputFQN: m.GetInputType(),
				})
			}
			sort.Slice(
				s.Methods,
				func(i, j int) bool {
					ms := s.Methods
					return ms[i].Name < ms[j].Name
				},
			)
			out.Services = append(out.Services, s)
		}
	}
	sort.Slice(
		out.Services,
		func(
			i,
			j int,
		) bool {
			return out.Services[i].Name < out.Services[j].Name
		},
	)
	sort.Strings(out.Versions)
	// /health is part of an api, and nobody has no api. A descriptor with
	// message types but no service block is a valid shape -- a daemon's
	// contract is what it consumes, and health is api too.
	//
	// What still gets refused is emptiness: bytes that describe nothing are
	// not a contract, whatever parsed.
	if len(out.Services) == 0 && out.Messages == 0 {
		return nil, fmt.Errorf(
			"descriptor parses but describes nothing: no " +
				"services, no message types",
		)
	}
	return out, nil
}

// trimType renders .flipr.v1.GetFlagRequest as GetFlagRequest; the package
// survives in the service header, not on every line.
func trimType(t string) string {
	for i := len(t) - 1; i >= 0; i-- {
		if t[i] == '.' {
			return t[i+1:]
		}
	}
	return t
}
