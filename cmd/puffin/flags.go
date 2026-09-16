package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The flipr screen: the intuitive path to the flag store. The generic composer
// can already call SetFlag, but "intuitive" means the flag under your cursor
// becomes the request: pick it, see it, give the reason, flip.
//
// The contract is the admin browser's, unchanged: a reason is required, flipr's
// refusals pass through verbatim, expensive wears the $.

// FlagRow is one flag with its namespace, flattened for a cursor.
type FlagRow struct {
	Service   string
	Version   string
	Key       string
	Kind      string // bool | string | int | unset
	Value     string // rendered
	Desc      string
	Expensive bool
	UpdatedAt string
}

// fetchFlags pulls every namespace from flipr and flattens to rows, grouped by
// namespace and sorted the way the store sorts. Callers that are about to FLIP
// something want newestOnly around this; callers that want to see the whole
// store, drift included, do not.
//
// Through the published client since 2026-09-06; the caller on the wire is
// puffin@v1.
func fetchFlags(base string) ([]FlagRow, error) {
	namespaces, err := listNamespaces(base)
	if err != nil {
		return nil, err
	}
	var rows []FlagRow
	for _, ns := range namespaces {
		for _, f := range ns.GetFlags() {
			kind, val := valueOf(f.GetValue())
			rows = append(rows, FlagRow{
				Service:   ns.GetService(),
				Version:   ns.GetVersion(),
				Key:       f.GetKey(),
				Kind:      kind,
				Value:     val,
				Desc:      f.GetDescription(),
				Expensive: f.GetExpensive(),
				UpdatedAt: f.GetUpdatedAt(),
			})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Service != rows[j].Service {
			return rows[i].Service < rows[j].Service
		}
		if rows[i].Version != rows[j].Version {
			return rows[i].Version < rows[j].Version
		}
		return rows[i].Key < rows[j].Key
	})
	return rows, nil
}

// renderValue decodes the protojson Value oneof into (kind, display).
func renderValue(raw json.RawMessage) (string, string) {
	var v struct {
		BoolValue   *bool           `json:"boolValue"`
		StringValue *string         `json:"stringValue"`
		IntValue    json.RawMessage `json:"intValue"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return "unset", "(unset)"
	}
	switch {
	case v.BoolValue != nil:
		if *v.BoolValue {
			return "bool", "true"
		}
		return "bool", "false"
	case v.StringValue != nil:
		return "string", *v.StringValue
	case len(v.IntValue) > 0:
		return "int", strings.Trim(string(v.IntValue), `"`)
	default:
		return "unset", "(unset)"
	}
}

// flipBody renders the SetFlag protojson for an edited row, for the confirm
// screen to SHOW; the flip itself goes through the client (flipr.go). Bools
// arrive already-toggled by the caller; strings and ints carry the new value.
func flipBody(row FlagRow, newValue, reason string) string {
	var value string
	switch row.Kind {
	case "bool":
		value = fmt.Sprintf(`{"boolValue":%s}`, newValue)
	case "int":
		value = fmt.Sprintf(`{"intValue":"%s"}`, newValue)
	default:
		b, _ := json.Marshal(newValue)
		value = fmt.Sprintf(`{"stringValue":%s}`, b)
	}
	body, _ := json.Marshal(map[string]any{
		"service": row.Service, "version": row.Version, "key": row.Key,
		"reason": reason,
	})
	// splice the raw value in: it is already valid json
	return string(body[:len(body)-1]) + fmt.Sprintf(`,"value":%s}`, value)
}

// newestOnly keeps each service's newest namespace and drops the rest,
// resolved by the flags' own updatedAt stamps -- nobody types commit hashes.
//
// This is the ONE place that resolution happens. It used to live only in the
// shell door, and the flags screen saw every namespace: you could put the
// cursor on a dead one, flip it, and get a 200 and a success line while the
// running service -- reading a different namespace -- never saw the change.
//
// A flip that reports success and changes nothing is the worst thing this tool
// can do, so both doors resolve here or neither does.
func newestOnly(rows []FlagRow) []FlagRow {
	// the newest stamp each service+version carries
	when := map[string]string{}
	for _, r := range rows {
		k := r.Service + "\x00" + r.Version
		if r.UpdatedAt > when[k] {
			when[k] = r.UpdatedAt
		}
	}
	// the winning version per service. Sorted keys, not map order: a tie
	// between two namespaces must resolve the same way on every run, or the
	// screen shows one thing and the shell door another.
	keys := make([]string, 0, len(when))
	for k := range when {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	best, bestWhen := map[string]string{}, map[string]string{}
	for _, k := range keys {
		i := strings.Index(k, "\x00")
		svc, ver := k[:i], k[i+1:]
		if cur, ok := bestWhen[svc]; !ok || when[k] > cur {
			best[svc], bestWhen[svc] = ver, when[k]
		}
	}
	var out []FlagRow
	for _, r := range rows {
		if best[r.Service] == r.Version {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Service != out[j].Service {
			return out[i].Service < out[j].Service
		}
		return out[i].Key < out[j].Key
	})
	return out
}
