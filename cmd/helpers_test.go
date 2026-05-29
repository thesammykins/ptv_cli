package cmd

import (
	"testing"

	"github.com/thesammykins/ptv_cli/internal/ptvapi"
)

func TestLimitDeparturesUsesGlobalLimit(t *testing.T) {
	flagLimit = 2
	t.Cleanup(func() { flagLimit = 0 })

	deps := []ptvapi.Departure{{RunID: 1}, {RunID: 2}, {RunID: 3}}
	got := limitDepartures(deps)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[1].RunID != 2 {
		t.Fatalf("second run = %d, want 2", got[1].RunID)
	}
}

func TestLimitDisruptionMapCapsDeterministically(t *testing.T) {
	flagLimit = 2
	t.Cleanup(func() { flagLimit = 0 })

	got := limitDisruptionMap(map[string][]ptvapi.Disruption{
		"metro_tram":  {{DisruptionID: 3}},
		"metro_train": {{DisruptionID: 1}, {DisruptionID: 2}},
	})
	if len(got["metro_train"]) != 2 {
		t.Fatalf("metro_train len = %d, want 2", len(got["metro_train"]))
	}
	if len(got["metro_tram"]) != 0 {
		t.Fatalf("metro_tram len = %d, want 0 after global cap", len(got["metro_tram"]))
	}
}

func TestSortStopsBySequenceMovesZeroSequenceLast(t *testing.T) {
	stops := []ptvapi.StopModel{
		{StopName: "zero", StopSequence: 0},
		{StopName: "three", StopSequence: 3},
		{StopName: "one", StopSequence: 1},
	}
	sortStopsBySequence(stops)
	if stops[0].StopName != "one" || stops[1].StopName != "three" || stops[2].StopName != "zero" {
		t.Fatalf("unexpected order: %#v", stops)
	}
}
