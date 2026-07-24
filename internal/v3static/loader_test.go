package v3static

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEmbeddedSnapshotIsValid(t *testing.T) {
	snapshot, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Source != Source || snapshot.Attribution != Attribution {
		t.Fatalf("provenance = %q / %q", snapshot.Source, snapshot.Attribution)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"routes"`) || strings.Contains(string(encoded), `"route_types"`) || strings.Contains(string(encoded), `"directions"`) {
		t.Fatalf("embedded snapshot contains static route data: %s", encoded[:min(len(encoded), 500)])
	}
}

func TestSearchOutletsDoesNotInventDistance(t *testing.T) {
	snapshot := &Snapshot{Outlets: []Outlet{{OutletName: "Central News", OutletBusiness: "News", OutletSuburb: "Melbourne"}}}
	items := snapshot.SearchOutlets("central", 5)
	if len(items) != 1 || items[0].OutletName != "Central News" {
		t.Fatalf("items = %+v", items)
	}
	if items[0].OutletLatitude != 0 || items[0].OutletLongitude != 0 {
		t.Fatalf("unexpected invented coordinates: %+v", items[0])
	}
}

func TestFindStationRequiresUniqueModeScopedMatch(t *testing.T) {
	snapshot := &Snapshot{StationFacilities: []StationFacility{
		{RouteType: 0, PTVStopID: 1, StopName: "Central"},
		{RouteType: 1, PTVStopID: 2, StopName: "Central"},
	}}
	if _, ok := snapshot.FindStation("Central", nil); ok {
		t.Fatal("unscoped ambiguous station matched")
	}
	mixed := &Snapshot{StationFacilities: []StationFacility{
		{RouteType: 0, PTVStopID: 1, StopName: "Central"},
		{RouteType: 0, PTVStopID: 2, StopName: "Central"},
		{RouteType: 0, PTVStopID: 3, StopName: "Central Station"},
	}}
	if _, ok := mixed.FindStation("Central", []int{0}); ok {
		t.Fatal("ambiguous exact station fell back to fuzzy match")
	}
	if station, ok := snapshot.FindStation("Central", []int{0}); !ok || station.PTVStopID != 1 {
		t.Fatalf("train station = %+v, ok=%v", station, ok)
	}
	if station, ok := (&Snapshot{StationFacilities: []StationFacility{{RouteType: 3, PTVStopID: 7, StopName: "Flinders Street Railway Station"}}}).FindStation("Flinders Street", []int{3}); !ok || station.PTVStopID != 7 {
		t.Fatalf("fuzzy station = %+v, ok=%v", station, ok)
	}
}
