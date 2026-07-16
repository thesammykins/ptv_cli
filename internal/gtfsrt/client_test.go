package gtfsrt

import (
	"testing"
	"time"

	gtfs "github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"google.golang.org/protobuf/proto"
)

func TestSnapshotPublicLabelMatchesConsistComponent(t *testing.T) {
	now := time.Now().UTC()
	snapshot := NormalizeSnapshot(testFeed(), vehicleFeed(now, now, "1041T-1098T-381M-382M-495M-496M", "private-42"), now)

	observation, ok := snapshot.FindVehicleByLabel(PublicVehicleLabel("381M"))
	if !ok || observation.Label != PublicVehicleLabel("1041T-1098T-381M-382M-495M-496M") {
		t.Fatalf("component lookup = %+v, %t", observation, ok)
	}
	if _, ok := snapshot.FindVehicleByLabel(PublicVehicleLabel("private-42")); ok {
		t.Fatal("internal descriptor ID matched the public label index")
	}
	if _, ok := snapshot.FindVehicleByLabel(PublicVehicleLabel("381")); ok {
		t.Fatal("partial plain label unexpectedly matched")
	}
}

func TestSnapshotVehicleOptionalEnumsReflectPresence(t *testing.T) {
	now := time.Now().UTC()
	status := gtfs.VehiclePosition_INCOMING_AT
	occupancy := gtfs.VehiclePosition_FULL
	message := &gtfs.FeedMessage{Entity: []*gtfs.FeedEntity{
		{
			Id: proto.String("absent"),
			Vehicle: &gtfs.VehiclePosition{
				Vehicle: &gtfs.VehicleDescriptor{Label: proto.String("absent-label")},
			},
		},
		{
			Id: proto.String("present"),
			Vehicle: &gtfs.VehiclePosition{
				Vehicle:         &gtfs.VehicleDescriptor{Label: proto.String("present-label")},
				CurrentStatus:   &status,
				OccupancyStatus: &occupancy,
			},
		},
	}}

	snapshot := NormalizeSnapshot(testFeed(), message, now)
	if snapshot.Vehicles[0].CurrentStatus != "" || snapshot.Vehicles[0].OccupancyStatus != "" {
		t.Fatalf("absent enum fields were invented: %+v", snapshot.Vehicles[0])
	}
	if snapshot.Vehicles[1].CurrentStatus != "INCOMING_AT" || snapshot.Vehicles[1].OccupancyStatus != "FULL" {
		t.Fatalf("present enum fields = %+v", snapshot.Vehicles[1])
	}
}

func TestSnapshotVehicleOptionalPositionFieldsReflectPresence(t *testing.T) {
	now := time.Now().UTC()
	latitude := float32(-37.818175)
	longitude := float32(144.966776)
	bearing := float32(94)
	speed := float32(13.5)
	message := &gtfs.FeedMessage{Entity: []*gtfs.FeedEntity{
		{
			Id: proto.String("coordinates-only"),
			Vehicle: &gtfs.VehiclePosition{
				Vehicle:  &gtfs.VehicleDescriptor{Label: proto.String("coordinates-only")},
				Position: &gtfs.Position{Latitude: &latitude, Longitude: &longitude},
			},
		},
		{
			Id: proto.String("all-position-fields"),
			Vehicle: &gtfs.VehiclePosition{
				Vehicle:  &gtfs.VehicleDescriptor{Label: proto.String("all-position-fields")},
				Position: &gtfs.Position{Latitude: &latitude, Longitude: &longitude, Bearing: &bearing, Speed: &speed},
			},
		},
	}}

	snapshot := NormalizeSnapshot(testFeed(), message, now)
	if snapshot.Vehicles[0].Bearing != nil || snapshot.Vehicles[0].Speed != nil {
		t.Fatalf("absent position fields were invented: %+v", snapshot.Vehicles[0])
	}
	if snapshot.Vehicles[1].Bearing == nil || *snapshot.Vehicles[1].Bearing != 94 || snapshot.Vehicles[1].Speed == nil || *snapshot.Vehicles[1].Speed != 13.5 {
		t.Fatalf("present position fields = %+v", snapshot.Vehicles[1])
	}
}
