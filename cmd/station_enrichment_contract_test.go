package cmd

import (
	"testing"

	"github.com/thesammykins/ptv_cli/internal/gtfs"
	"github.com/thesammykins/ptv_cli/internal/ptvapi"
	"github.com/thesammykins/ptv_cli/internal/v3static"
)

func TestMergeStationFacilityOutputKeepsGTFSPrimaryIdentity(t *testing.T) {
	toilet := true
	response := &ptvapi.StopResponse{
		Stop: ptvapi.StopDetails{
			StopID:        1071,
			StopAmenities: &ptvapi.StopAmenityDetails{Toilet: &toilet},
			StopName:      "PTV name",
		},
	}
	output := stationOutput{Stop: stationStopOutput{GTFSStopID: "2:1071", StopName: "GTFS name"}, Disruptions: map[string]stationDisruptionOutput{}}

	if !mergeStationFacilityOutput(response, &output) {
		t.Fatal("expected station facilities to merge")
	}
	if output.Stop.GTFSStopID != "2:1071" || output.Stop.StopName != "GTFS name" {
		t.Fatalf("primary identity changed: %+v", output.Stop)
	}
	if output.Stop.StopAmenities == nil || output.Stop.StopAmenities.Toilet == nil || !*output.Stop.StopAmenities.Toilet {
		t.Fatalf("amenities = %+v", output.Stop.StopAmenities)
	}
}

func TestGTFSStationOutputBackfillsLegacyIDsFromEnrichment(t *testing.T) {
	detail := &gtfs.StopDetailResult{
		Stop:   gtfs.StopResult{StopID: "2:1071", StopName: "GTFS name", FeedMode: 2},
		Routes: []gtfs.RouteResult{{RouteID: "2:route-alamein", LongName: "Alamein", FeedMode: 2}},
	}
	output := newGTFSStationOutput(t.Context(), nil, detail)
	if output.Stop.StopID != 1071 || output.Stop.PTVStopID != 1071 {
		t.Fatalf("GTFS legacy stop IDs = %d/%d, want 1071", output.Stop.StopID, output.Stop.PTVStopID)
	}

	toilet := true
	response := &ptvapi.StopResponse{Stop: ptvapi.StopDetails{
		StopID:        1071,
		StopAmenities: &ptvapi.StopAmenityDetails{Toilet: &toilet},
		Routes:        []ptvapi.Route{{RouteID: 1, RouteName: "Alamein", RouteType: 0}},
	}}
	if !mergeStationFacilityOutput(response, &output) {
		t.Fatal("expected station facilities to merge")
	}
	if len(output.Stop.Routes) != 1 || output.Stop.Routes[0].RouteID != 1 || output.Stop.Routes[0].PTVRouteID != 1 {
		t.Fatalf("legacy route IDs = %+v, want PTV route 1", output.Stop.Routes)
	}
}

func TestStaticStationFacilityMergeKeepsGTFSPrimaryIdentity(t *testing.T) {
	toilet := true
	facility := v3static.StationFacility{
		RouteType: 0, PTVStopID: 1071, StopName: "Bundled name",
		StopAmenities: &v3static.Amenities{Toilet: &toilet},
	}
	output := stationOutput{Stop: stationStopOutput{GTFSStopID: "2:1071", StopName: "GTFS name"}, Disruptions: map[string]stationDisruptionOutput{}}
	if !mergeStationFacilityOutput(staticStationResponse(facility), &output) {
		t.Fatal("expected bundled station facilities to merge")
	}
	if output.Stop.GTFSStopID != "2:1071" || output.Stop.StopName != "GTFS name" {
		t.Fatalf("primary identity changed: %+v", output.Stop)
	}
}
