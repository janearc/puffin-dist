package main

// puffin reaches flipr through the published Go client and nothing else (the
// dev rules of 2026-09-06; Clients/contract.md in the flipr repository).
//
// Before this file puffin posted to flipr's RPCs by hand in four places, with
// no retry, no caller on two of them and no client header on any: the shape the
// libfliprclient sprint squashes.
//
// puffin is a TOOL. It reads every namespace (the operator's roster, the deploy
// audit, the enclave view) and it flips flags on the operator's behalf, which
// are the two tool verbs the contract names, List and Set.
//
// It declares one flag of its own, because a tool that can write to ring 0
// needs an off switch the operator can reach without stopping the tool:
// flip.enabled, on by default, off means puffin is read-only.

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync"

	flipr "github.com/janearc/flipr-dist/clients/go"
	fliprv1 "github.com/janearc/flipr-dist/clients/go/flipr/v1"
)

// puffinFlags is what puffin declares about itself.
var puffinFlags = []flipr.Flag{{
	Key: "flip.enabled", On: true,
	Desc: "on: puffin's set verb and flip screen may write flags, one " +
		"SetFlag per flip with the operator's reason. " +
		"off: puffin is read-only and refuses to flip; a flip in " +
		"flight is not affected.",
}}

// newFlipr makes first contact with the flipr behind base, ONCE per base for
// the life of the process: the client pings and publishes puffin's declaration
// at construction, and a publish is a write flipr records, so constructing per
// screen read would make every roster fetch a mutation.
//
// A flipr that is down is not an error here; the readers below turn it into the
// warning the screens already show, and every check pings so it heals when
// flipr does.
func newFlipr(base string) (*flipr.Client, error) {
	fliprMu.Lock()
	defer fliprMu.Unlock()
	if c, ok := fliprByBase[base]; ok {
		return c, nil
	}
	c, err := flipr.New(flipr.Config{
		Service:   "puffin",
		Version:   "v1",
		URL:       base,
		EnvPrefix: "PUFFIN",
		Declared:  puffinFlags,
		// a flip on an unknown answer never happens
		OnUnknown: flipr.Refuse,
		Log: slog.New(
			slog.NewTextHandler(
				os.Stderr,
				&slog.HandlerOptions{Level: slog.LevelWarn},
			),
		),
		// puffin's shared client, so the tests' proxy reaches the fakes
		HTTP:     client,
		CacheTTL: flipr.NoCache,
	})
	if err != nil {
		return nil, err
	}
	fliprByBase[base] = c
	return c, nil
}

// one client per base, for the process; tests reset it between fakes
var (
	fliprMu     sync.Mutex
	fliprByBase = map[string]*flipr.Client{}
)

// resetFlipr forgets every client, so a test's fake gets first contact.
func resetFlipr() {
	fliprMu.Lock()
	defer fliprMu.Unlock()
	fliprByBase = map[string]*flipr.Client{}
}

// listNamespaces is the one read every screen shares: every namespace flipr
// holds, with its flags. An unreachable flipr is an error the caller shows.
func listNamespaces(base string) ([]*fliprv1.Namespace, error) {
	c, err := newFlipr(base)
	if err != nil {
		return nil, err
	}
	return c.List()
}

// valueOf renders a proto Value as (kind, display), the shape the rows use.
func valueOf(v *fliprv1.Value) (string, string) {
	switch k := v.GetKind().(type) {
	case *fliprv1.Value_BoolValue:
		if k.BoolValue {
			return "bool", "true"
		}
		return "bool", "false"
	case *fliprv1.Value_StringValue:
		return "string", k.StringValue
	case *fliprv1.Value_IntValue:
		return "int", strconv.FormatInt(k.IntValue, 10)
	default:
		return "unset", "(unset)"
	}
}

// flipValue builds the proto Value a flip carries, typed by the row's kind so
// a string flag spelling "true" stays a string.
func flipValue(kind, newValue string) (*fliprv1.Value, error) {
	switch kind {
	case "bool":
		b, err := strconv.ParseBool(newValue)
		if err != nil {
			return nil, fmt.Errorf(
				"a boolean flag takes true or false, not %q",
				newValue,
			)
		}
		return &fliprv1.Value{
			Kind: &fliprv1.Value_BoolValue{BoolValue: b},
		}, nil
	case "int":
		n, err := strconv.ParseInt(newValue, 10, 64)
		if err != nil {
			return nil, fmt.Errorf(
				"an integer flag takes an integer, not %q",
				newValue,
			)
		}
		return &fliprv1.Value{
			Kind: &fliprv1.Value_IntValue{IntValue: n},
		}, nil
	default:
		return &fliprv1.Value{
			Kind: &fliprv1.Value_StringValue{StringValue: newValue},
		}, nil
	}
}

// flip is the operator's write: refused when puffin's own flip.enabled is off
// or unknown, else one SetFlag in the row's namespace with the reason.
func flip(
	base string,
	row FlagRow,
	newValue, reason string,
) (InvokeResult, error) {
	c, err := newFlipr(base)
	if err != nil {
		return InvokeResult{}, err
	}
	if on, why := c.Enabled("flip.enabled"); !on {
		return InvokeResult{}, errors.New("puffin is read-only: " + why)
	}
	v, err := flipValue(row.Kind, newValue)
	if err != nil {
		return InvokeResult{}, err
	}
	f, err := c.Set(row.Service, row.Version, row.Key, v, reason)
	if err != nil {
		// flipr's refusal, verbatim, with its status where there was
		// one
		var se interface{ Status() int }
		if errors.As(err, &se) {
			return InvokeResult{
				Status: se.Status(),
				Body:   err.Error(),
			}, nil
		}
		return InvokeResult{}, err
	}
	_, shown := valueOf(f.GetValue())
	return InvokeResult{
		Status: 200,
		Body: fmt.Sprintf(
			"%s/%s = %s (%s@%s)",
			row.Service,
			row.Key,
			shown,
			row.Service,
			row.Version,
		),
	}, nil
}
