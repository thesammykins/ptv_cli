package cmd

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	gtfs "github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"github.com/thesammykins/ptv_cli/internal/gtfsrt"
	"github.com/thesammykins/ptv_cli/internal/ptvapi"
	"google.golang.org/protobuf/proto"
)

type fakeVehicleClient struct {
	routes       map[int][]ptvapi.Route
	runsForRoute map[int]*ptvapi.RunsResponse
	runs         map[int]map[string]*ptvapi.RunResponse
	patterns     map[int]map[string]*ptvapi.StoppingPatternResponse
	search       map[string]*ptvapi.SearchResult
	departures   map[string]*ptvapi.DeparturesResponse
}

type fakeGTFSRealtimeVehicleClient struct {
	feeds map[string]*gtfs.FeedMessage
	feed  *gtfs.FeedMessage
	err   error
}

func (f *fakeGTFSRealtimeVehicleClient) FetchVehiclePositions(_ context.Context, feedURL string) (*gtfs.FeedMessage, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.feeds != nil {
		if feed, ok := f.feeds[feedURL]; ok {
			return feed, nil
		}
		return &gtfs.FeedMessage{}, nil
	}
	return f.feed, nil
}

func (f *fakeVehicleClient) Routes(_ context.Context, routeTypes []int, _ string) (*ptvapi.RouteResponse, error) {
	var routes []ptvapi.Route
	for _, routeType := range routeTypes {
		routes = append(routes, f.routes[routeType]...)
	}
	return &ptvapi.RouteResponse{Routes: routes}, nil
}

func (f *fakeVehicleClient) RunsForRoute(_ context.Context, routeID, _ int, _ ptvapi.RunsOptions) (*ptvapi.RunsResponse, error) {
	if resp, ok := f.runsForRoute[routeID]; ok {
		return resp, nil
	}
	return &ptvapi.RunsResponse{}, nil
}

func (f *fakeVehicleClient) Run(_ context.Context, runRef string, routeType int, _ ptvapi.RunsOptions) (*ptvapi.RunResponse, error) {
	if byType, ok := f.runs[routeType]; ok {
		if resp, ok := byType[runRef]; ok {
			return resp, nil
		}
	}
	return nil, errors.New("not found")
}

func (f *fakeVehicleClient) Pattern(_ context.Context, runRef string, routeType int, _ ptvapi.PatternOptions) (*ptvapi.StoppingPatternResponse, error) {
	if byType, ok := f.patterns[routeType]; ok {
		if resp, ok := byType[runRef]; ok {
			return resp, nil
		}
	}
	return &ptvapi.StoppingPatternResponse{}, nil
}

func (f *fakeVehicleClient) Search(_ context.Context, term string, _ []int) (*ptvapi.SearchResult, error) {
	if resp, ok := f.search[term]; ok {
		return resp, nil
	}
	return &ptvapi.SearchResult{}, nil
}

func (f *fakeVehicleClient) Departures(_ context.Context, routeType, stopID int, opts ptvapi.DeparturesOptions) (*ptvapi.DeparturesResponse, error) {
	if resp, ok := f.departures[departureKey(routeType, stopID, opts.LookBackwards)]; ok {
		return resp, nil
	}
	if resp, ok := f.departures[departureKey(-1, stopID, opts.LookBackwards)]; ok {
		return resp, nil
	}
	return &ptvapi.DeparturesResponse{}, nil
}

func departureKey(routeType, stopID int, lookBackwards bool) string {
	return strconv.Itoa(routeType) + ":" + strconv.Itoa(stopID) + ":" + strconv.FormatBool(lookBackwards)
}

func TestLookupVehicleMatchesDescriptorBeforeRunRef(t *testing.T) {
	lat := -37.8101
	lng := 144.9631
	client := &fakeVehicleClient{
		routes: map[int][]ptvapi.Route{
			0: {{RouteType: 0, RouteID: 1, RouteName: "Mernda"}},
		},
		runsForRoute: map[int]*ptvapi.RunsResponse{
			1: {Runs: []ptvapi.Run{{
				RunRef:          "run-123",
				RouteID:         1,
				RouteType:       0,
				DestinationName: "Flinders Street",
				VehicleDescriptor: &ptvapi.VehicleDescriptor{
					ID:          "8605",
					Description: "6 Car Xtrapolis",
				},
				VehiclePosition: &ptvapi.VehiclePosition{Latitude: &lat, Longitude: &lng},
			}}},
		},
		runs: map[int]map[string]*ptvapi.RunResponse{
			0: {"8605": {Run: ptvapi.Run{RunRef: "8605", RouteType: 0}}},
		},
		patterns: map[int]map[string]*ptvapi.StoppingPatternResponse{
			0: {"run-123": patternWithFutureStop("run-123")},
		},
	}

	got, err := lookupVehicle(context.Background(), client, "8605", 10)
	if err != nil {
		t.Fatalf("lookupVehicle: %v", err)
	}
	if got.MatchedBy != "vehicle_descriptor.id" {
		t.Fatalf("MatchedBy = %q, want vehicle_descriptor.id", got.MatchedBy)
	}
	if got.VehicleID != "8605" || got.RunRef != "run-123" || got.Mode != "Train" {
		t.Fatalf("unexpected result: %+v", got)
	}
	if got.Position == nil || got.Position.Kind != "gps" {
		t.Fatalf("position = %+v, want gps", got.Position)
	}
	if got.NextStop == nil || got.NextStop.StopName != "Parliament Station" {
		t.Fatalf("next stop = %+v", got.NextStop)
	}
}

func TestLookupVehicleMatchesTrainConsistComponent(t *testing.T) {
	client := &fakeVehicleClient{
		routes: map[int][]ptvapi.Route{
			0: {{RouteType: 0, RouteID: 6, RouteName: "Frankston Line"}},
		},
		runsForRoute: map[int]*ptvapi.RunsResponse{
			6: {Runs: []ptvapi.Run{{
				RunRef:    "952377",
				RouteID:   6,
				RouteType: 0,
				VehicleDescriptor: &ptvapi.VehicleDescriptor{
					ID:          "113M-114M-1357T-1422T-243M-244M",
					Description: "6 Car Xtrapolis",
				},
			}}},
		},
		patterns: map[int]map[string]*ptvapi.StoppingPatternResponse{
			0: {"952377": patternWithFutureStop("952377")},
		},
	}

	got, err := lookupVehicle(context.Background(), client, "243M", 10)
	if err != nil {
		t.Fatalf("lookupVehicle: %v", err)
	}
	if got.MatchedBy != "vehicle_descriptor.id" {
		t.Fatalf("MatchedBy = %q, want vehicle_descriptor.id", got.MatchedBy)
	}
	if got.VehicleID != "113M-114M-1357T-1422T-243M-244M" {
		t.Fatalf("VehicleID = %q, want full consist", got.VehicleID)
	}
}

func TestLookupVehicleFallsBackToRunRef(t *testing.T) {
	client := &fakeVehicleClient{
		routes:       map[int][]ptvapi.Route{},
		runsForRoute: map[int]*ptvapi.RunsResponse{},
		runs: map[int]map[string]*ptvapi.RunResponse{
			2: {"8605": {Run: ptvapi.Run{RunRef: "8605", RouteID: 30, RouteType: 2, DestinationName: "City"}}},
		},
		patterns: map[int]map[string]*ptvapi.StoppingPatternResponse{
			2: {"8605": patternWithFutureStop("8605")},
		},
	}

	got, err := lookupVehicle(context.Background(), client, "8605", 0)
	if err != nil {
		t.Fatalf("lookupVehicle: %v", err)
	}
	if got.MatchedBy != "run_ref" {
		t.Fatalf("MatchedBy = %q, want run_ref", got.MatchedBy)
	}
	if got.VehicleID != "" || got.Mode != "Bus" {
		t.Fatalf("unexpected result: %+v", got)
	}
	if len(got.Warnings) == 0 {
		t.Fatal("expected run_ref warning")
	}
}

func TestLookupVehicleWithStopHintMatchesDepartureRunDescriptor(t *testing.T) {
	client := &fakeVehicleClient{
		routes: map[int][]ptvapi.Route{
			0: {{RouteType: 0, RouteID: 6, RouteName: "Frankston Line"}},
		},
		search: map[string]*ptvapi.SearchResult{
			"Mordialloc": {Stops: []ptvapi.StopModel{{StopID: 1134, StopName: "Mordialloc Station", RouteType: 0}}},
		},
		departures: map[string]*ptvapi.DeparturesResponse{
			departureKey(0, 1134, false): {
				Departures: []ptvapi.Departure{{StopID: 1134, RunRef: "run-8605"}},
				Runs: map[string]ptvapi.Run{
					"run-8605": {
						RunRef:          "run-8605",
						RouteID:         6,
						RouteType:       0,
						DestinationName: "Frankston",
						VehicleDescriptor: &ptvapi.VehicleDescriptor{
							ID:          "8605",
							Description: "6 Car Comeng",
						},
					},
				},
				Routes: map[string]ptvapi.Route{"6": {RouteType: 0, RouteID: 6, RouteName: "Frankston Line"}},
			},
		},
		patterns: map[int]map[string]*ptvapi.StoppingPatternResponse{
			0: {"run-8605": patternWithFutureStop("run-8605")},
		},
	}

	got, err := lookupVehicleWithHints(context.Background(), client, "8605", vehicleLookupHints{Stop: "Mordialloc", Route: "Frankston"})
	if err != nil {
		t.Fatalf("lookupVehicleWithHints: %v", err)
	}
	if got.MatchedBy != "stop_departure.vehicle_descriptor.id" {
		t.Fatalf("MatchedBy = %q, want stop_departure.vehicle_descriptor.id", got.MatchedBy)
	}
	if got.RouteID != 6 || got.VehicleID != "8605" || got.RunRef != "run-8605" {
		t.Fatalf("unexpected result: %+v", got)
	}
	if got.ServiceState != "current" || got.NextStop == nil || got.NextStop.StopName != "Mordialloc Station" {
		t.Fatalf("unexpected service state/next stop: %+v", got)
	}
}

func TestLookupVehicleWithStopHintMatchesDepartureRunRef(t *testing.T) {
	client := &fakeVehicleClient{
		search: map[string]*ptvapi.SearchResult{
			"Chadstone": {Stops: []ptvapi.StopModel{{StopID: 11293, StopName: "Chadstone Shopping Center/Eastern Access Rd", RouteType: 2}}},
		},
		departures: map[string]*ptvapi.DeparturesResponse{
			departureKey(2, 11293, false): {
				Departures: []ptvapi.Departure{{StopID: 11293, RunRef: "17-903--1-Sun12-903738"}},
				Runs: map[string]ptvapi.Run{
					"17-903--1-Sun12-903738": {
						RunRef:          "17-903--1-Sun12-903738",
						RouteID:         15789,
						RouteType:       2,
						DestinationName: "Mordialloc",
					},
				},
				Routes: map[string]ptvapi.Route{"15789": {RouteType: 2, RouteID: 15789, RouteNumber: "903", RouteName: "Altona - Mordialloc"}},
			},
		},
		patterns: map[int]map[string]*ptvapi.StoppingPatternResponse{
			2: {"17-903--1-Sun12-903738": patternWithFutureStop("17-903--1-Sun12-903738")},
		},
	}

	got, err := lookupVehicleWithHints(context.Background(), client, "17-903--1-Sun12-903738", vehicleLookupHints{Stop: "Chadstone"})
	if err != nil {
		t.Fatalf("lookupVehicleWithHints: %v", err)
	}
	if got.MatchedBy != "stop_departure.run_ref" {
		t.Fatalf("MatchedBy = %q, want stop_departure.run_ref", got.MatchedBy)
	}
	if got.RunRef != "17-903--1-Sun12-903738" || got.RouteID != 15789 || got.VehicleID != "" {
		t.Fatalf("unexpected result: %+v", got)
	}
	if len(got.Warnings) == 0 {
		t.Fatal("expected run_ref warning")
	}
}

func TestLookupVehicleWithStopHintProbesOtherTypesForRunRef(t *testing.T) {
	client := &fakeVehicleClient{
		search: map[string]*ptvapi.SearchResult{
			"Southern Cross": {Stops: []ptvapi.StopModel{{StopID: 1071, StopName: "Southern Cross Station", RouteType: 0}}},
		},
		departures: map[string]*ptvapi.DeparturesResponse{
			departureKey(3, 1071, false): {
				Departures: []ptvapi.Departure{{StopID: 1071, RunRef: "25168"}},
				Runs: map[string]ptvapi.Run{
					"25168": {
						RunRef:          "25168",
						RouteID:         1717,
						RouteType:       3,
						DestinationName: "Pakenham",
					},
				},
				Routes: map[string]ptvapi.Route{"1717": {RouteType: 3, RouteID: 1717, RouteName: "Canberra - Melbourne via Bairnsdale"}},
			},
		},
		patterns: map[int]map[string]*ptvapi.StoppingPatternResponse{
			3: {"25168": patternWithFutureStop("25168")},
		},
	}

	got, err := lookupVehicleWithHints(context.Background(), client, "25168", vehicleLookupHints{Stop: "Southern Cross"})
	if err != nil {
		t.Fatalf("lookupVehicleWithHints: %v", err)
	}
	if got.MatchedBy != "stop_departure.run_ref" || got.Mode != "V/Line" {
		t.Fatalf("unexpected result: %+v", got)
	}
}

func TestEnrichWithGTFSRealtimeBusMatchesRunRef(t *testing.T) {
	lat := float32(-37.744010)
	lng := float32(144.992079)
	bearing := float32(94)
	timestamp := uint64(1780207369)
	feed := &gtfs.FeedMessage{Entity: []*gtfs.FeedEntity{{
		Id: proto.String("entity-1"),
		Vehicle: &gtfs.VehiclePosition{
			Trip: &gtfs.TripDescriptor{
				TripId:  proto.String("17-903--1-Sun12-903738"),
				RouteId: proto.String("4-903"),
			},
			Vehicle: &gtfs.VehicleDescriptor{
				Id:    proto.String("bus-internal-1"),
				Label: proto.String("1234"),
			},
			Position:  &gtfs.Position{Latitude: &lat, Longitude: &lng, Bearing: &bearing},
			Timestamp: &timestamp,
		},
	}}}
	result := &vehicleResult{RouteType: intPtr(2), RouteID: 15789, RunRef: "17-903--1-Sun12-903738"}
	busFeed, ok := gtfsrt.FeedByID("bus-vehicle-positions")
	if !ok {
		t.Fatal("missing bus-vehicle-positions feed")
	}

	got := enrichWithGTFSRealtime(context.Background(), &fakeGTFSRealtimeVehicleClient{feeds: map[string]*gtfs.FeedMessage{busFeed.URL: feed}}, "ignored", result)

	if got.GTFSRealtime == nil {
		t.Fatal("GTFSRealtime is nil")
	}
	if got.VehicleID != "1234" {
		t.Fatalf("VehicleID = %q, want label", got.VehicleID)
	}
	if got.Position == nil || got.Position.Latitude == nil || *got.Position.Latitude == 0 {
		t.Fatalf("position not attached: %+v", got.Position)
	}
	if got.PositionSource != "Metro and regional bus vehicle positions" {
		t.Fatalf("PositionSource = %q", got.PositionSource)
	}
}

func TestEnrichWithGTFSRealtimeBusMatchesVehicleID(t *testing.T) {
	lat := float32(-37.744010)
	lng := float32(144.992079)
	feed := &gtfs.FeedMessage{Entity: []*gtfs.FeedEntity{{
		Id: proto.String("entity-1"),
		Vehicle: &gtfs.VehiclePosition{
			Trip: &gtfs.TripDescriptor{TripId: proto.String("trip-1"), RouteId: proto.String("903")},
			Vehicle: &gtfs.VehicleDescriptor{
				Id: proto.String("fleet-777"),
			},
			Position:      &gtfs.Position{Latitude: &lat, Longitude: &lng},
			CurrentStatus: gtfs.VehiclePosition_IN_TRANSIT_TO.Enum(),
		},
	}}}
	result := &vehicleResult{Query: "fleet-777", MatchedBy: "none"}
	busFeed, ok := gtfsrt.FeedByID("bus-vehicle-positions")
	if !ok {
		t.Fatal("missing bus-vehicle-positions feed")
	}

	got := enrichWithGTFSRealtime(context.Background(), &fakeGTFSRealtimeVehicleClient{feeds: map[string]*gtfs.FeedMessage{busFeed.URL: feed}}, "fleet-777", result)

	if got.GTFSRealtime == nil || got.GTFSRealtime.VehicleID != "fleet-777" {
		t.Fatalf("unexpected GTFSRealtime: %+v", got.GTFSRealtime)
	}
	if got.MatchedBy != "gtfs_realtime.vehicle" || got.Mode != "Bus" || got.RunRef != "trip-1" {
		t.Fatalf("GTFS-R match did not promote result: %+v", got)
	}
	if got.Position == nil || got.PositionSource != "Metro and regional bus vehicle positions" {
		t.Fatalf("GTFS-R position not promoted: %+v", got)
	}
	if len(got.Warnings) != 0 {
		t.Fatalf("warnings = %v, want none", got.Warnings)
	}
}

func TestEnrichWithGTFSRealtimeMatchesTrainVehicleID(t *testing.T) {
	lat := float32(-37.818175)
	lng := float32(144.966776)
	feed := &gtfs.FeedMessage{Entity: []*gtfs.FeedEntity{{
		Id: proto.String("train-entity"),
		Vehicle: &gtfs.VehiclePosition{
			Trip: &gtfs.TripDescriptor{TripId: proto.String("train-trip"), RouteId: proto.String("2-WER")},
			Vehicle: &gtfs.VehicleDescriptor{
				Label: proto.String("931M"),
			},
			Position: &gtfs.Position{Latitude: &lat, Longitude: &lng},
		},
	}}}
	trainFeed, ok := gtfsrt.FeedByID("metro-vehicle-positions")
	if !ok {
		t.Fatal("missing metro-vehicle-positions feed")
	}
	result := &vehicleResult{Query: "931M", MatchedBy: "none"}

	got := enrichWithGTFSRealtime(context.Background(), &fakeGTFSRealtimeVehicleClient{feeds: map[string]*gtfs.FeedMessage{trainFeed.URL: feed}}, "931M", result)

	if got.MatchedBy != "gtfs_realtime.vehicle" || got.Mode != "Train" || got.VehicleID != "931M" {
		t.Fatalf("unexpected GTFS-R train result: %+v", got)
	}
	if got.PositionSource != "Metro Train vehicle positions" {
		t.Fatalf("PositionSource = %q", got.PositionSource)
	}
}

func TestLookupVehicleWithStopHintReportsLastSpotted(t *testing.T) {
	past := time.Now().Add(-30 * time.Minute).UTC().Format(time.RFC3339)
	client := &fakeVehicleClient{
		routes: map[int][]ptvapi.Route{
			0: {{RouteType: 0, RouteID: 6, RouteName: "Frankston Line"}},
		},
		search: map[string]*ptvapi.SearchResult{
			"Mordialloc": {Stops: []ptvapi.StopModel{{StopID: 1134, StopName: "Mordialloc Station", RouteType: 0}}},
		},
		departures: map[string]*ptvapi.DeparturesResponse{
			departureKey(0, 1134, true): {
				Departures: []ptvapi.Departure{{StopID: 1134, RunRef: "run-8605", EstimatedDepartureUTC: &past}},
				Runs: map[string]ptvapi.Run{
					"run-8605": {
						RunRef:          "run-8605",
						RouteID:         6,
						RouteType:       0,
						DestinationName: "Frankston",
						VehicleDescriptor: &ptvapi.VehicleDescriptor{
							ID: "8605",
						},
					},
				},
				Routes: map[string]ptvapi.Route{"6": {RouteType: 0, RouteID: 6, RouteName: "Frankston Line"}},
			},
		},
		patterns: map[int]map[string]*ptvapi.StoppingPatternResponse{
			0: {"run-8605": patternWithFutureStop("run-8605")},
		},
	}

	got, err := lookupVehicleWithHints(context.Background(), client, "8605", vehicleLookupHints{Stop: "Mordialloc", Route: "Frankston"})
	if err != nil {
		t.Fatalf("lookupVehicleWithHints: %v", err)
	}
	if got.MatchedBy != "stop_departure_history.vehicle_descriptor.id" {
		t.Fatalf("MatchedBy = %q, want history match", got.MatchedBy)
	}
	if got.ServiceState != "last_spotted" || got.LastSeen == nil || got.LastSeen.StopName != "Mordialloc Station" {
		t.Fatalf("unexpected last spotted result: %+v", got)
	}
	if len(got.Warnings) < 2 {
		t.Fatalf("warnings = %#v, want last-spotted warnings", got.Warnings)
	}
}

func TestLookupVehicleReturnsNotFoundResult(t *testing.T) {
	client := &fakeVehicleClient{
		routes:       map[int][]ptvapi.Route{},
		runsForRoute: map[int]*ptvapi.RunsResponse{},
		runs:         map[int]map[string]*ptvapi.RunResponse{},
		patterns:     map[int]map[string]*ptvapi.StoppingPatternResponse{},
	}

	got, err := lookupVehicle(context.Background(), client, "missing", 0)
	if err != nil {
		t.Fatalf("lookupVehicle: %v", err)
	}
	if got.MatchedBy != "none" {
		t.Fatalf("MatchedBy = %q, want none", got.MatchedBy)
	}
	if len(got.Warnings) != 2 {
		t.Fatalf("warnings = %#v, want 2", got.Warnings)
	}
}

func patternWithFutureStop(runRef string) *ptvapi.StoppingPatternResponse {
	future := time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339)
	return &ptvapi.StoppingPatternResponse{
		Departures: []ptvapi.PatternDeparture{{
			Departure: ptvapi.Departure{
				StopID:                1001,
				RunRef:                runRef,
				EstimatedDepartureUTC: &future,
				DepartureSequence:     100,
			},
		}},
		Stops: map[string]ptvapi.StopModel{
			"1001": {StopID: 1001, StopName: "Parliament Station", StopLatitude: -37.811, StopLongitude: 144.973},
		},
	}
}
