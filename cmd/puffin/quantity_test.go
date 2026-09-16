package main

import "testing"

// Kubernetes reports memory in whatever unit it likes: the metrics API
// answers in Ki, a pod spec is written in Mi or Gi, a bare number is bytes.
// Comparing 170156Ki against 2Gi requires both to become one thing first.
func TestParseQuantity(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int64
		ok   bool
	}{
		{"170156Ki", 170156 * 1024, true},
		{"2Gi", 2 << 30, true},
		{"128Mi", 128 << 20, true},
		{"1Ti", 1 << 40, true},
		{"512", 512, true}, // bare: bytes
		{"1M", 1_000_000, true},
		{"1k", 1000, true},
		{" 64Mi ", 64 << 20, true},
		{"", 0, false},
		{"lots", 0, false},
		{"-5Mi", 0, false},
		{"12x", 0, false},
	} {
		got, ok := parseQuantity(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf(
				"parseQuantity(%q) = %d,%v want %d,%v",
				c.in,
				got,
				ok,
				c.want,
				c.ok,
			)
		}
	}
}

// "Mi" must be tried before "M". Tried the other way, 128Mi parses as
// 128M with a stray i and comes out 4.8% short -- close enough to look
// right and wrong enough to matter against a limit.
func TestLongestSuffixWins(t *testing.T) {
	mi, _ := parseQuantity("128Mi")
	m, _ := parseQuantity("128M")
	if mi == m {
		t.Fatal("Mi and M parsed the same")
	}
	if mi != 128*1024*1024 {
		t.Errorf("128Mi = %d, want %d", mi, 128*1024*1024)
	}
}

// The rendering has to match kubectl's, or the operator is comparing two
// numbers that look different and are the same.
func TestKubeBytesMatchesKubectl(t *testing.T) {
	for _, c := range []struct {
		in   int64
		want string
	}{
		{185 * 1024 * 1024, "185Mi"},
		{2 << 30, "2Gi"},
		{1536 * 1024 * 1024, "1.5Gi"},
		{20768 * 1024, "20Mi"},
		{722*1024*1024 + 900*1024, "722Mi"},
		{512, "512B"},
	} {
		if got := kubeBytes(c.in); got != c.want {
			t.Errorf(
				"kubeBytes(%d) = %q, want %q",
				c.in,
				got,
				c.want,
			)
		}
	}
}
