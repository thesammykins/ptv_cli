package cmd

import "testing"

func TestAPIFeedModesKeepVLineTrainAndCoachDistinct(t *testing.T) {
	if got := gtfsFeedModes([]int{3}); len(got) != 1 || got[0] != 1 {
		t.Fatalf("vline train feed modes = %v, want [1]", got)
	}
	if got := gtfsFeedModes([]int{4}); len(got) != 1 || got[0] != 5 {
		t.Fatalf("vline coach feed modes = %v, want [5]", got)
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
