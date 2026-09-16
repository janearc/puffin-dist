package main

import (
	"strconv"
	"strings"
)

// Kubernetes quantities, parsed to bytes.
//
// kubectl reports memory in whatever unit it feels like: the metrics API
// answers in Ki, a pod spec is written in Mi or Gi, and a bare number is bytes.
// Comparing "170156Ki" against "2Gi" requires both to become one thing first,
// and doing that at each call site is how two of them end up disagreeing.
//
// Only the memory suffixes are handled. CPU quantities use "m" for
// millicores, which is a different scale entirely and does not belong in
// the same function pretending to be a unit of the same kind.
var memSuffix = []struct {
	name  string
	scale int64
}{
	// longest first: "Mi" must be tried before "M", or "Mi" parses as
	// "M" with a trailing i and the number is wrong by 5%
	{"Ei", 1 << 60}, {"Pi", 1 << 50}, {"Ti", 1 << 40},
	{"Gi", 1 << 30}, {"Mi", 1 << 20}, {"Ki", 1 << 10},
	{
		"E",
		1e18,
	}, {"P", 1e15}, {"T", 1e12}, {"G", 1e9}, {"M", 1e6}, {"k", 1e3},
}

// parseQuantity turns a Kubernetes memory quantity into bytes. It returns
// 0 and false for anything it does not understand, because a wrong byte
// count silently drawn as a percentage is worse than an absent one.
func parseQuantity(q string) (int64, bool) {
	q = strings.TrimSpace(q)
	if q == "" {
		return 0, false
	}
	for _, s := range memSuffix {
		if !strings.HasSuffix(q, s.name) {
			continue
		}
		n, err := strconv.ParseFloat(strings.TrimSuffix(q, s.name), 64)
		if err != nil || n < 0 {
			return 0, false
		}
		return int64(n * float64(s.scale)), true
	}
	n, err := strconv.ParseFloat(q, 64) // bare number: bytes
	if err != nil || n < 0 {
		return 0, false
	}
	return int64(n), true
}

// kubeBytes renders bytes the way kubectl renders them -- "185Mi", no
// space, no trailing B -- so a number on this screen and the same number
// from `kubectl top` are recognisably the same number.
//
// It deliberately differs from humanBytes in maps.go, which prints
// "180.9 MiB" for the maps pane. Those are two audiences: one is reading
// prose about a file, the other is checking a pod against a limit somebody
// wrote as "2Gi". Merging them would make one of the two wrong.
func kubeBytes(n int64) string {
	// Gi keeps one decimal because the difference between 1.5Gi and 2Gi is
	// a decision; Mi and Ki do not, because kubectl prints whole ones and
	// "722.9Mi" is three characters of noise in a column that has to fit
	// beside a limit and a percentage.
	switch {
	case n >= 1<<30:
		return trimZero(float64(n)/(1<<30)) + "Gi"
	case n >= 1<<20:
		return strconv.FormatInt(n/(1<<20), 10) + "Mi"
	case n >= 1<<10:
		return strconv.FormatInt(n/(1<<10), 10) + "Ki"
	}
	return strconv.FormatInt(n, 10) + "B"
}

// trimZero renders one decimal place, and drops it when it is zero: 2Gi
// rather than 2.0Gi, but 1.5Gi rather than 2Gi.
func trimZero(f float64) string {
	s := strconv.FormatFloat(f, 'f', 1, 64)
	return strings.TrimSuffix(s, ".0")
}
