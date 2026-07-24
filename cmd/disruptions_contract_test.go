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
	if len(output.Disruptions[routeTypeName(2)]) != 1 || output.Disruptions[routeTypeName(2)][0].DisruptionID != 1 {
		t.Fatalf("bus disruptions = %+v", output.Disruptions[routeTypeName(2)])
	}
	if len(output.Disruptions["vline"]) != 0 {
		t.Fatalf("vline disruptions = %+v, want mode-filtered out", output.Disruptions["vline"])
	}
}

func TestAppendV3DisruptionsUsesCanonicalRouteTypeBuckets(t *testing.T) {
	output := disruptionsOutput{Disruptions: map[string][]disruptionOutput{}}
	response := &ptvapi.DisruptionsResponse{Disruptions: map[string][]ptvapi.Disruption{
		"vline": {{DisruptionID: 7, Routes: []ptvapi.DisruptionRoute{{RouteType: 3, RouteID: 70, RouteNumber: "V/Line"}}}},
	}}

	if got := appendV3Disruptions(&output, response, []int{3}, ""); got != 1 {
		t.Fatalf("appended = %d, want one V/Line disruption", got)
	}
	if len(output.Disruptions[routeTypeName(3)]) != 1 || len(output.Disruptions["vline"]) != 0 {
		t.Fatalf("disruption buckets = %+v, want only %q bucket", output.Disruptions, routeTypeName(3))
	}
}

func TestLimitDisruptionOutputMapCapsMergedResults(t *testing.T) {
	previousLimit := flagLimit
	t.Cleanup(func() { flagLimit = previousLimit })
	flagLimit = 2
	limited := limitDisruptionOutputMap(map[string][]disruptionOutput{
		"Train": {{Title: "one"}, {Title: "two"}},
		"Tram":  {{Title: "three"}},
	})
	if len(limited["Train"]) != 2 || len(limited["Tram"]) != 0 {
		t.Fatalf("limited disruptions = %+v, want 2 train rows and no tram rows", limited)
	}
}
