package gtfsrt

import (
	"testing"

	gtfs "github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"google.golang.org/protobuf/proto"
)

func TestVehicleIdentifierMatchesConsistComponent(t *testing.T) {
	if !vehicleIdentifierMatches("1041T-1098T-381M-382M-495M-496M", "381M") {
		t.Fatal("expected consist component to match")
	}
}

func TestVehicleIdentifierMatchesExactOnlyForPlainIDs(t *testing.T) {
	if vehicleIdentifierMatches("BS04FR", "BS04") {
		t.Fatal("unexpected partial plain id match")
	}
}
func TestVehiclesOmitsAbsentOccupancyStatus(t *testing.T) {
	feed := &gtfs.FeedMessage{Entity: []*gtfs.FeedEntity{{
		Id: proto.String("entity-1"),
		Vehicle: &gtfs.VehiclePosition{
			Vehicle: &gtfs.VehicleDescriptor{Id: proto.String("vehicle-1")},
		},
	}}}

	vehicles := Vehicles(feed)
	if len(vehicles) != 1 {
		t.Fatalf("vehicles = %d, want 1", len(vehicles))
	}
	if vehicles[0].OccupancyStatus != "" {
		t.Fatalf("OccupancyStatus = %q, want empty for absent field", vehicles[0].OccupancyStatus)
	}
}

func TestVehiclesKeepsPresentOccupancyStatus(t *testing.T) {
	status := gtfs.VehiclePosition_FULL
	feed := &gtfs.FeedMessage{Entity: []*gtfs.FeedEntity{{
		Id: proto.String("entity-1"),
		Vehicle: &gtfs.VehiclePosition{
			Vehicle:         &gtfs.VehicleDescriptor{Id: proto.String("vehicle-1")},
			OccupancyStatus: &status,
		},
	}}}

	vehicles := Vehicles(feed)
	if vehicles[0].OccupancyStatus != "FULL" {
		t.Fatalf("OccupancyStatus = %q, want FULL", vehicles[0].OccupancyStatus)
	}
}

func TestVehiclesOmitsAbsentOptionalPositionFields(t *testing.T) {
	lat := float32(-37.818175)
	lng := float32(144.966776)
	feed := &gtfs.FeedMessage{Entity: []*gtfs.FeedEntity{{
		Id: proto.String("entity-1"),
		Vehicle: &gtfs.VehiclePosition{
			Vehicle:  &gtfs.VehicleDescriptor{Id: proto.String("vehicle-1")},
			Position: &gtfs.Position{Latitude: &lat, Longitude: &lng},
		},
	}}}

	vehicles := Vehicles(feed)
	if vehicles[0].CurrentStatus != "" {
		t.Fatalf("CurrentStatus = %q, want empty for absent field", vehicles[0].CurrentStatus)
	}
	if vehicles[0].Bearing != nil {
		t.Fatalf("Bearing = %v, want nil for absent field", *vehicles[0].Bearing)
	}
	if vehicles[0].Speed != nil {
		t.Fatalf("Speed = %v, want nil for absent field", *vehicles[0].Speed)
	}
}

func TestVehiclesKeepsPresentOptionalPositionFields(t *testing.T) {
	lat := float32(-37.818175)
	lng := float32(144.966776)
	bearing := float32(94)
	speed := float32(13.5)
	status := gtfs.VehiclePosition_INCOMING_AT
	feed := &gtfs.FeedMessage{Entity: []*gtfs.FeedEntity{{
		Id: proto.String("entity-1"),
		Vehicle: &gtfs.VehiclePosition{
			Vehicle:       &gtfs.VehicleDescriptor{Id: proto.String("vehicle-1")},
			Position:      &gtfs.Position{Latitude: &lat, Longitude: &lng, Bearing: &bearing, Speed: &speed},
			CurrentStatus: &status,
		},
	}}}

	vehicles := Vehicles(feed)
	if vehicles[0].CurrentStatus != "INCOMING_AT" {
		t.Fatalf("CurrentStatus = %q, want INCOMING_AT", vehicles[0].CurrentStatus)
	}
	if vehicles[0].Bearing == nil || *vehicles[0].Bearing != 94 {
		t.Fatalf("Bearing = %v, want 94", vehicles[0].Bearing)
	}
	if vehicles[0].Speed == nil || *vehicles[0].Speed != 13.5 {
		t.Fatalf("Speed = %v, want 13.5", vehicles[0].Speed)
	}
}
