package cmd

import (
	"testing"

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
