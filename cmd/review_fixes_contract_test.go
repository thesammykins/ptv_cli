package cmd

import (
	"testing"
	"time"

	"github.com/thesammykins/ptv_cli/internal/gtfs"
	"github.com/thesammykins/ptv_cli/internal/localtime"
)

func TestAPIFeedModesKeepVLineTrainAndCoachDistinct(t *testing.T) {
	if got := gtfsFeedModes([]int{3}); len(got) != 2 || got[0] != 1 || got[1] != 5 {
		t.Fatalf("vline feed modes = %v, want [1 5]", got)
	}
	if got := gtfsFeedModes([]int{4}); len(got) != 2 || got[0] != 4 || got[1] != 6 {
		t.Fatalf("nightbus feed modes = %v, want [4 6]", got)
	}
}

func TestFilterNextGTFSDeparturesIncludesPreviousServiceDateAfterMidnight(t *testing.T) {
	now := time.Date(2026, 7, 24, 0, 30, 0, 0, localtime.Melbourne())
	previousDate := now.AddDate(0, 0, -1).Format("20060102")
	currentDate := now.Format("20060102")
	anchors := map[string]time.Time{
		previousDate: localtime.ServiceDayAnchor(now.AddDate(0, 0, -1)),
		currentDate:  localtime.ServiceDayAnchor(now),
	}
	departures := filterNextGTFSDepartures([]gtfs.DepartureResult{
		{ServiceDate: previousDate, DepartureSec: 24*60*60 + 45*60},
		{ServiceDate: currentDate, DepartureSec: 90 * 60},
		{ServiceDate: currentDate, DepartureSec: 10 * 60},
	}, anchors, now)
	if len(departures) != 2 || departures[0].ServiceDate != previousDate || departures[1].ServiceDate != currentDate {
		t.Fatalf("filtered departures = %+v, want previous and current service dates in time order", departures)
	}
}

func TestWorseSourceFreshnessPreservesOldestRealtimeEvidence(t *testing.T) {
	current := &sourceFreshness{State: "current", AgeSeconds: 5}
	stale := &sourceFreshness{State: "stale", AgeSeconds: 120}
	if got := worseSourceFreshness(current, stale); got != stale {
		t.Fatalf("stale freshness = %+v, want candidate", got)
	}
	if got := worseSourceFreshness(stale, current); got != stale {
		t.Fatalf("freshness was overwritten by current candidate: %+v", got)
	}
	older := &sourceFreshness{State: "current", AgeSeconds: 60}
	if got := worseSourceFreshness(current, older); got != older {
		t.Fatalf("older equal-state freshness = %+v, want candidate", got)
	}
}
