package main

import "testing"

// The bug this pins: the flags screen used to see every namespace, so a
// cursor could land on a dead one. Flipping it returned 200 and changed
// nothing a running service would ever read. newestOnly is what makes a
// row that is visible also a row that is live.
func TestNewestOnlyDropsTheStaleNamespace(t *testing.T) {
	rows := []FlagRow{
		{
			Service:   "metricsd",
			Version:   "dev",
			Key:       "smc.enabled",
			UpdatedAt: "2026-08-29T15:15:16Z",
		},
		{
			Service:   "metricsd",
			Version:   "v1",
			Key:       "smc.enabled",
			UpdatedAt: "2026-08-29T15:18:06Z",
		},
		{
			Service:   "athlete",
			Version:   "v1",
			Key:       "fetch.osrm",
			UpdatedAt: "2026-08-30T05:32:09Z",
		},
	}
	got := newestOnly(rows)
	if len(got) != 2 {
		t.Fatalf(
			"want 2 rows (metricsd@v1, athlete@v1), got %d: %+v",
			len(got),
			got,
		)
	}
	for _, r := range got {
		if r.Service == "metricsd" && r.Version != "v1" {
			t.Errorf(
				"kept the stale namespace metricsd@%s; v1 is "+
					"newer",
				r.Version,
			)
		}
	}
	// sorted by service then key, so the screen and the shell door agree
	if got[0].Service != "athlete" || got[1].Service != "metricsd" {
		t.Errorf("not sorted by service: %+v", got)
	}
}

// A namespace's stamp is the newest stamp any of its flags carries: a
// namespace with one freshly-flipped flag is live even if its others are old.
func TestNewestOnlyUsesTheNewestStampInTheNamespace(t *testing.T) {
	rows := []FlagRow{
		{
			Service:   "dodo",
			Version:   "old",
			Key:       "a",
			UpdatedAt: "2026-08-01T00:00:00Z",
		},
		{
			Service:   "dodo",
			Version:   "old",
			Key:       "b",
			UpdatedAt: "2026-08-20T00:00:00Z",
		},
		{
			Service:   "dodo",
			Version:   "new",
			Key:       "a",
			UpdatedAt: "2026-08-10T00:00:00Z",
		},
		{
			Service:   "dodo",
			Version:   "new",
			Key:       "b",
			UpdatedAt: "2026-08-25T00:00:00Z",
		},
	}
	got := newestOnly(rows)
	if len(got) != 2 {
		t.Fatalf("want dodo@new's 2 rows, got %d", len(got))
	}
	for _, r := range got {
		if r.Version != "new" {
			t.Errorf(
				"picked @%s; @new carries the newer stamp "+
					"(08-25 > 08-20)",
				r.Version,
			)
		}
	}
}

// Map iteration is unordered; the resolution must not be. Same input, same
// answer, every run -- or the screen and the shell door disagree at random.
func TestNewestOnlyIsDeterministicOnATie(t *testing.T) {
	rows := []FlagRow{
		{
			Service:   "s",
			Version:   "b",
			Key:       "k",
			UpdatedAt: "2026-08-01T00:00:00Z",
		},
		{
			Service:   "s",
			Version:   "a",
			Key:       "k",
			UpdatedAt: "2026-08-01T00:00:00Z",
		},
	}
	first := newestOnly(rows)[0].Version
	for i := 0; i < 200; i++ {
		if v := newestOnly(rows)[0].Version; v != first {
			t.Fatalf(
				"tie resolved to %q then %q; it must be stable",
				first,
				v,
			)
		}
	}
}

// Nothing in, nothing out -- and no panic on the error path, where the
// fetch returns nil rows alongside its error.
func TestNewestOnlyHandlesNoRows(t *testing.T) {
	if got := newestOnly(nil); len(got) != 0 {
		t.Errorf("want no rows, got %+v", got)
	}
}
