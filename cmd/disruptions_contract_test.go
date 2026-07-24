package cmd

import (
	"testing"

	"github.com/thesammykins/ptv_cli/internal/gtfsrt"
	"github.com/thesammykins/ptv_cli/internal/ptvapi"
)

func TestNewGTFSDisruptionOutputDeduplicatesRepeatedEntities(t *testing.T) {
	item := newGTFSDisruptionOutput(gtfsrt.Alert{
		EntityID: "alert-1",
		InformedEntities: []gtfsrt.AlertEntity{
			{RouteID: "route-1", StopID: "stop-1"},
			{RouteID: "route-1", StopID: "stop-1"},
		},
	}, 0)
	if len(item.Routes) != 1 || len(item.Stops) != 1 {
		t.Fatalf("routes=%d stops=%d, want one each", len(item.Routes), len(item.Stops))
	}
}

func TestAppendV3DisruptionsHonorsModeAndRouteFilters(t *testing.T) {
	output := disruptionsOutput{Disruptions: map[string][]disruptionOutput{}}
	response := &ptvapi.DisruptionsResponse{Disruptions: map[string][]ptvapi.Disruption{
		"bus": {
			{DisruptionID: 1, Routes: []ptvapi.DisruptionRoute{{RouteType: 2, RouteID: 10, RouteNumber: "10"}}},
			{DisruptionID: 2, Routes: []ptvapi.DisruptionRoute{{RouteType: 2, RouteID: 20, RouteNumber: "20"}}},
		},
		"vline": {{DisruptionID: 3, Routes: []ptvapi.DisruptionRoute{{RouteType: 3, RouteID: 30, RouteNumber: "30"}}}},
	}}

	if got := appendV3Disruptions(&output, response, []int{2}, "10"); got != 1 {
		t.Fatalf("appended = %d, want one matching bus disruption", got)
	}
	if len(output.Disruptions["bus"]) != 1 || output.Disruptions["bus"][0].DisruptionID != 1 {
		t.Fatalf("bus disruptions = %+v", output.Disruptions["bus"])
	}
	if len(output.Disruptions["vline"]) != 0 {
		t.Fatalf("vline disruptions = %+v, want mode-filtered out", output.Disruptions["vline"])
	}
}
