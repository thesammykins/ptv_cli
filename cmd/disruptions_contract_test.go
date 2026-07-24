package cmd

import (
	"encoding/json"
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

func TestGTFSDisruptionJSONOmitsPTVNumericIDs(t *testing.T) {
	encoded, err := json.Marshal(newGTFSDisruptionOutput(gtfsrt.Alert{EntityID: "alert-1", HeaderText: []gtfsrt.TranslatedString{{Text: "Alert"}}}, 0))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["id"] != "alert-1" || fields["source"] != "opendata" {
		t.Fatalf("identity fields = %v", fields)
	}
	if _, ok := fields["disruption_id"]; ok {
		t.Fatalf("Open Data JSON contains disruption_id: %s", encoded)
	}
	if _, ok := fields["ptv_disruption_id"]; ok {
		t.Fatalf("Open Data JSON contains ptv_disruption_id: %s", encoded)
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
