package main

import (
	"testing"

	"github.com/thesammykins/ptv_cli/internal/v3static"
)

func TestParseStationSeed(t *testing.T) {
	seed, err := parseStationSeed("0:1071")
	if err != nil || seed.RouteType != 0 || seed.StopID != 1071 {
		t.Fatalf("seed=%+v err=%v", seed, err)
	}
	if _, err := parseStationSeed("1071"); err == nil {
		t.Fatal("accepted station without route type")
	}
	if _, err := parseStationSeed("0:-1"); err == nil {
		t.Fatal("accepted non-positive stop id")
	}
}

func TestSnapshotHashIgnoresGenerationTime(t *testing.T) {
	first := &v3static.Snapshot{GeneratedAt: "2026-01-01T00:00:00Z", Outlets: []v3static.Outlet{{OutletName: "Central News"}}}
	second := &v3static.Snapshot{GeneratedAt: "2026-02-01T00:00:00Z", Outlets: []v3static.Outlet{{OutletName: "Central News"}}}
	if err := first.RecomputeHash(); err != nil {
		t.Fatal(err)
	}
	if err := second.RecomputeHash(); err != nil {
		t.Fatal(err)
	}
	if first.ContentHash != second.ContentHash {
		t.Fatalf("hashes differ for generation-time-only change: %q != %q", first.ContentHash, second.ContentHash)
	}
}
