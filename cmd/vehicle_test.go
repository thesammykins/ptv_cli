package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	gtfs "github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"github.com/thesammykins/ptv_cli/internal/gtfsrt"
	"github.com/thesammykins/ptv_cli/internal/ptvapi"
	"google.golang.org/protobuf/proto"
)

type fakeVehicleClient struct {
	routes           map[int][]ptvapi.Route
	runsForRoute     map[int]*ptvapi.RunsResponse
	runsForRouteErrs map[int]error
	runsForRouteFunc func(context.Context, int, int, ptvapi.RunsOptions) (*ptvapi.RunsResponse, error)
	runs             map[int]map[string]*ptvapi.RunResponse
	runsByRefErr     error
	patterns         map[int]map[string]*ptvapi.StoppingPatternResponse
	search           map[string]*ptvapi.SearchResult
	departures       map[string]*ptvapi.DeparturesResponse
	departureErrs    map[string]error
	routesCalls      int
	runsByRefCalls   int
}

type fakeGTFSRealtimeVehicleClient struct {
	feeds    map[string]*gtfs.FeedMessage
	feed     *gtfs.FeedMessage
	err      error
	now      time.Time
	requests map[string]int
}

type gtfsRealtimeVehicleClientFunc func(context.Context, gtfsrt.Feed) (*gtfsrt.Snapshot, error)

func (f gtfsRealtimeVehicleClientFunc) FetchSnapshot(ctx context.Context, feed gtfsrt.Feed) (*gtfsrt.Snapshot, error) {
	return f(ctx, feed)
}

func (f *fakeGTFSRealtimeVehicleClient) FetchSnapshot(_ context.Context, feed gtfsrt.Feed) (*gtfsrt.Snapshot, error) {
	if f.requests == nil {
		f.requests = make(map[string]int)
	}
	f.requests[feed.ID]++
	if f.err != nil {
		return nil, f.err
	}
	message := f.feed
	if f.feeds != nil {
		if configured, ok := f.feeds[feed.URL]; ok {
			message = configured
		} else {
			message = &gtfs.FeedMessage{}
		}
	}
	now := f.now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return gtfsrt.NormalizeSnapshot(feed, message, now), nil
}

func (f *fakeVehicleClient) Routes(_ context.Context, routeTypes []int, _ string) (*ptvapi.RouteResponse, error) {
	f.routesCalls++
	var routes []ptvapi.Route
	for _, routeType := range routeTypes {
		routes = append(routes, f.routes[routeType]...)
	}
	return &ptvapi.RouteResponse{Routes: routes}, nil
}

func (f *fakeVehicleClient) RunsForRoute(ctx context.Context, routeID, routeType int, opts ptvapi.RunsOptions) (*ptvapi.RunsResponse, error) {
	if f.runsForRouteFunc != nil {
		return f.runsForRouteFunc(ctx, routeID, routeType, opts)
	}
	if err, ok := f.runsForRouteErrs[routeID]; ok {
		return nil, err
	}
	if resp, ok := f.runsForRoute[routeID]; ok {
		return resp, nil
	}
	return &ptvapi.RunsResponse{}, nil
}

func (f *fakeVehicleClient) RunsByRef(_ context.Context, runRef ptvapi.RunRef, _ ptvapi.RunsOptions) (*ptvapi.RunsResponse, error) {
	f.runsByRefCalls++
	if f.runsByRefErr != nil {
		return nil, f.runsByRefErr
	}
	for _, byType := range f.runs {
		if response, ok := byType[string(runRef)]; ok {
			return &ptvapi.RunsResponse{Runs: []ptvapi.Run{response.Run}}, nil
		}
	}
	return nil, &ptvapi.Error{Kind: ptvapi.ErrorNotFound, StatusCode: 404, Message: "run not found"}
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
	if err, ok := f.departureErrs[departureKey(routeType, stopID, opts.LookBackwards)]; ok {
		return nil, err
	}
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
	if client.routesCalls != 1 {
		t.Fatalf("route catalog requests = %d, want one multi-valued request", client.routesCalls)
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
	if client.runsByRefCalls != 1 {
		t.Fatalf("RunsByRef calls = %d, want 1", client.runsByRefCalls)
	}
}

func TestLookupVehiclePropagatesRunsByRefOperationalErrors(t *testing.T) {
	for _, test := range vehicleOperationalErrorCases() {
		t.Run(test.name, func(t *testing.T) {
			apiErr := &ptvapi.Error{Kind: test.kind, Message: "injected lookup failure", Err: test.cause}
			client := &fakeVehicleClient{runsByRefErr: apiErr}

			_, err := lookupVehicle(context.Background(), client, "run-42", 0)

			if err != apiErr {
				t.Fatalf("lookupVehicle error = %v, want original %v", err, apiErr)
			}
			if !ptvapi.IsKind(err, test.kind) {
				t.Fatalf("lookupVehicle error kind = %v, want %v", err, test.kind)
			}
			if test.cause != nil && !errors.Is(err, test.cause) {
				t.Fatalf("lookupVehicle error = %v, want wrapped cause %v", err, test.cause)
			}
		})
	}
}

func TestLookupVehicleUsesOneSharedPTVDeadline(t *testing.T) {
	requests := 0
	client := &fakeVehicleClient{
		routes: map[int][]ptvapi.Route{
			0: {
				{RouteID: 7, RouteType: 0, RouteName: "First route"},
				{RouteID: 8, RouteType: 0, RouteName: "Second route"},
			},
		},
		runsForRouteFunc: func(ctx context.Context, _ int, _ int, _ ptvapi.RunsOptions) (*ptvapi.RunsResponse, error) {
			requests++
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}

	_, err := lookupVehicleWithinBudget(context.Background(), client, "vehicle-42", vehicleLookupHints{RouteScanLimit: 2}, 20*time.Millisecond)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lookupVehicleWithinBudget error = %v, want context deadline", err)
	}
	if requests != 1 {
		t.Fatalf("route-run requests = %d, want only the in-flight request before shared expiry", requests)
	}
	if client.runsByRefCalls != 0 {
		t.Fatalf("RunsByRef calls = %d, want none after shared expiry", client.runsByRefCalls)
	}
}

func TestVehicleScanRoutesRange(t *testing.T) {
	for _, limit := range []int{0, vehicleMaxRouteScan} {
		if err := validateVehicleRouteScan(limit); err != nil {
			t.Fatalf("validateVehicleRouteScan(%d): %v", limit, err)
		}
	}

	previous := vehicleScanRoutes
	t.Cleanup(func() { vehicleScanRoutes = previous })
	for _, limit := range []int{-1, vehicleMaxRouteScan + 1} {
		vehicleScanRoutes = 0
		_, _, err := executeCommand(t, "vehicle", "vehicle-42", "--scan-routes", strconv.Itoa(limit))
		if err == nil || !strings.Contains(err.Error(), "--scan-routes must be between 0 and 100") {
			t.Fatalf("vehicle --scan-routes %d error = %v, want range validation", limit, err)
		}
	}
}

func TestLookupVehicleAtStopPropagatesProbeOperationalErrors(t *testing.T) {
	tests := []struct {
		name            string
		stopRouteType   int
		errorRouteType  int
		lookBackwards   bool
		operationalCase vehicleOperationalErrorCase
	}{
		{name: "unknown-mode upcoming", stopRouteType: -1, errorRouteType: 0, operationalCase: vehicleOperationalErrorCases()[0]},
		{name: "unknown-mode history", stopRouteType: -1, errorRouteType: 0, lookBackwards: true, operationalCase: vehicleOperationalErrorCases()[1]},
		{name: "cross-mode upcoming", stopRouteType: 0, errorRouteType: 1, operationalCase: vehicleOperationalErrorCases()[2]},
		{name: "cross-mode history", stopRouteType: 0, errorRouteType: 1, lookBackwards: true, operationalCase: vehicleOperationalErrorCases()[3]},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			apiErr := &ptvapi.Error{Kind: test.operationalCase.kind, Message: "injected departure failure", Err: test.operationalCase.cause}
			client := &fakeVehicleClient{
				search: map[string]*ptvapi.SearchResult{
					"Hinted stop": {Stops: []ptvapi.StopModel{{StopID: 99, StopName: "Hinted stop", RouteType: test.stopRouteType}}},
				},
				departureErrs: map[string]error{
					departureKey(test.errorRouteType, 99, test.lookBackwards): apiErr,
				},
			}

			_, err := lookupVehicleAtStop(context.Background(), client, "vehicle-42", vehicleLookupHints{Stop: "Hinted stop"})

			if err != apiErr {
				t.Fatalf("lookupVehicleAtStop error = %v, want original %v", err, apiErr)
			}
		})
	}
}

func TestLookupVehicleInStopDeparturesTreatsTypedNotFoundAsAbsence(t *testing.T) {
	client := &fakeVehicleClient{
		departureErrs: map[string]error{
			departureKey(0, 99, false): &ptvapi.Error{Kind: ptvapi.ErrorNotFound, StatusCode: 404, Message: "departures not found"},
		},
	}

	_, err := lookupVehicleInStopDepartures(context.Background(), client, "vehicle-42", &ptvapi.StopModel{StopID: 99}, nil, 0, false)
	if !errors.Is(err, errVehicleNotFound) {
		t.Fatalf("lookupVehicleInStopDepartures error = %v, want vehicle absence", err)
	}
}

func TestScanRunsForVehicleIDPropagatesOperationalErrors(t *testing.T) {
	for _, test := range vehicleOperationalErrorCases() {
		t.Run(test.name, func(t *testing.T) {
			apiErr := &ptvapi.Error{Kind: test.kind, Message: "injected route scan failure", Err: test.cause}
			client := &fakeVehicleClient{
				routes: map[int][]ptvapi.Route{
					0: {{RouteID: 7, RouteType: 0, RouteName: "Test route"}},
				},
				runsForRouteErrs: map[int]error{7: apiErr},
			}

			_, err := scanRunsForVehicleID(context.Background(), client, "vehicle-42", 10)

			if err != apiErr {
				t.Fatalf("scanRunsForVehicleID error = %v, want original %v", err, apiErr)
			}
		})
	}
}

func TestScanRunsForVehicleIDSkipsTypedNotFound(t *testing.T) {
	client := &fakeVehicleClient{
		routes: map[int][]ptvapi.Route{
			0: {
				{RouteID: 7, RouteType: 0, RouteName: "Unavailable route"},
				{RouteID: 8, RouteType: 0, RouteName: "Matching route"},
			},
		},
		runsForRouteErrs: map[int]error{
			7: &ptvapi.Error{Kind: ptvapi.ErrorNotFound, StatusCode: 404, Message: "runs not found"},
		},
		runsForRoute: map[int]*ptvapi.RunsResponse{
			8: {Runs: []ptvapi.Run{{
				RunRef:    "run-8",
				RouteID:   8,
				RouteType: 0,
				VehicleDescriptor: &ptvapi.VehicleDescriptor{
					ID: "vehicle-42",
				},
			}}},
		},
	}

	matches, err := scanRunsForVehicleID(context.Background(), client, "vehicle-42", 10)
	if err != nil {
		t.Fatalf("scanRunsForVehicleID: %v", err)
	}
	if len(matches) != 1 || matches[0].run.RunRef != "run-8" {
		t.Fatalf("matches = %+v, want matching route after typed absence", matches)
	}
}

type vehicleOperationalErrorCase struct {
	name  string
	kind  ptvapi.ErrorKind
	cause error
}

func vehicleOperationalErrorCases() []vehicleOperationalErrorCase {
	return []vehicleOperationalErrorCase{
		{name: "authentication", kind: ptvapi.ErrorAuthentication},
		{name: "timeout", kind: ptvapi.ErrorTimeout, cause: context.DeadlineExceeded},
		{name: "rate-limit", kind: ptvapi.ErrorRateLimit},
		{name: "upstream", kind: ptvapi.ErrorUpstream},
		{name: "cancellation", kind: ptvapi.ErrorCanceled, cause: context.Canceled},
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

func TestEnrichWithGTFSRealtimeDoesNotJoinPTVRunRefAcrossNamespaces(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	runRef := "17-903--1-Sun12-903738"
	message := vehicleRealtimeMessage(now, -10*time.Second, -20*time.Second, runRef, "private-internal", runRef, runRef)
	result := &vehicleResult{MatchedBy: "run_ref", RouteType: intPtr(2), RouteID: 15789, RunRef: runRef}
	busFeed, ok := gtfsrt.FeedByID("bus-vehicle-positions")
	if !ok {
		t.Fatal("missing bus-vehicle-positions feed")
	}
	client := &fakeGTFSRealtimeVehicleClient{feeds: map[string]*gtfs.FeedMessage{busFeed.URL: message}, now: now}

	got := enrichWithGTFSRealtime(context.Background(), client, runRef, result)

	if got.GTFSRealtime != nil {
		t.Fatalf("cross-namespace enrichment occurred: %+v", got.GTFSRealtime)
	}
	if client.requests[busFeed.ID] != 0 {
		t.Fatalf("requests = %v, want no feed request without a public-label join key", client.requests)
	}
	if len(got.Warnings) == 0 || !strings.Contains(got.Warnings[len(got.Warnings)-1], "different namespaces") {
		t.Fatalf("warnings = %v, want namespace warning", got.Warnings)
	}
}

func TestEnrichWithGTFSRealtimeDoesNotMatchInternalTripOrEntityIDs(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	query := "shared-collision"
	message := vehicleRealtimeMessage(now, -10*time.Second, -20*time.Second, "different-public-label", query, query, query)
	result := &vehicleResult{Query: query, MatchedBy: "none", RouteType: intPtr(2)}
	busFeed, ok := gtfsrt.FeedByID("bus-vehicle-positions")
	if !ok {
		t.Fatal("missing bus-vehicle-positions feed")
	}
	client := &fakeGTFSRealtimeVehicleClient{feeds: map[string]*gtfs.FeedMessage{busFeed.URL: message}, now: now}

	got := enrichWithGTFSRealtime(context.Background(), client, query, result)

	if got.GTFSRealtime != nil || got.MatchedBy != "none" {
		t.Fatalf("collision crossed identifier namespaces: %+v", got)
	}
	if client.requests[busFeed.ID] != 1 {
		t.Fatalf("requests = %v, want one bus snapshot", client.requests)
	}
	if len(got.Warnings) == 0 {
		t.Fatal("expected public-label miss warning")
	}
}

func TestEnrichWithGTFSRealtimeMatchesOnlyPublicLabelAndKeepsEvidenceSeparate(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	message := vehicleRealtimeMessage(now, -10*time.Second, -20*time.Second, "931M-932M", "private-42", "train-entity", "train-trip")
	trainFeed, ok := gtfsrt.FeedByID("metro-vehicle-positions")
	if !ok {
		t.Fatal("missing metro-vehicle-positions feed")
	}
	result := &vehicleResult{Query: "931M", MatchedBy: "none"}
	client := &fakeGTFSRealtimeVehicleClient{feeds: map[string]*gtfs.FeedMessage{trainFeed.URL: message}, now: now}

	got := enrichWithGTFSRealtime(context.Background(), client, "931M", result)

	if got.MatchedBy != "gtfs_realtime.public_label" || got.Mode != "Train" || got.PublicLabel != "931M-932M" {
		t.Fatalf("unexpected GTFS-R train result: %+v", got)
	}
	if got.VehicleID != "" || got.RunRef != "" || got.RouteID != 0 || got.Route != "" {
		t.Fatalf("GTFS identifiers leaked into PTV fields: %+v", got)
	}
	if got.Position != nil || got.PositionSource != "" || got.GTFSRealtime == nil || got.GTFSRealtime.Position == nil {
		t.Fatalf("PTV and GTFS-R position evidence was mixed: %+v", got)
	}
	if got.GTFSRealtime.ObservationState != "current" || got.GTFSRealtime.AgeSeconds == nil || *got.GTFSRealtime.AgeSeconds != 20 {
		t.Fatalf("freshness = %+v", got.GTFSRealtime)
	}
	if client.requests[trainFeed.ID] != 1 {
		t.Fatalf("requests = %v, want one train snapshot", client.requests)
	}
}

func TestVehicleFreshnessMapsFutureSkewToUnknown(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	feed, ok := gtfsrt.FeedByID("metro-vehicle-positions")
	if !ok {
		t.Fatal("missing metro-vehicle-positions feed")
	}
	tests := []struct {
		name         string
		entityOffset time.Duration
		want         string
	}{
		{"current", -90 * time.Second, "current"},
		{"stale", -91 * time.Second, "stale"},
		{"future skew", 31 * time.Second, "unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message := vehicleRealtimeMessage(now, -10*time.Second, test.entityOffset, "931M", "private", "entity", "trip")
			client := &fakeGTFSRealtimeVehicleClient{feeds: map[string]*gtfs.FeedMessage{feed.URL: message}, now: now}
			result := enrichWithGTFSRealtime(context.Background(), client, "931M", &vehicleResult{Query: "931M", MatchedBy: "none", RouteType: intPtr(0)})
			if result.GTFSRealtime == nil || result.GTFSRealtime.ObservationState != test.want {
				t.Fatalf("observation = %+v, want %s", result.GTFSRealtime, test.want)
			}
		})
	}
}

func TestEnrichWithGTFSRealtimeFetchesEachCandidateFeedAtMostOnce(t *testing.T) {
	client := &fakeGTFSRealtimeVehicleClient{feed: &gtfs.FeedMessage{}, now: time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)}
	result := enrichWithGTFSRealtime(context.Background(), client, "missing-label", &vehicleResult{Query: "missing-label", MatchedBy: "none"})
	if result.GTFSRealtime != nil {
		t.Fatalf("unexpected match: %+v", result.GTFSRealtime)
	}
	for _, source := range allVehicleRealtimeSources() {
		if client.requests[source.Feed.ID] != 1 {
			t.Fatalf("feed %s requests = %d, want 1; all=%v", source.Feed.ID, client.requests[source.Feed.ID], client.requests)
		}
	}
}

func TestEnrichWithGTFSRealtimeHonorsSharedDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	requests := 0
	client := gtfsRealtimeVehicleClientFunc(func(ctx context.Context, _ gtfsrt.Feed) (*gtfsrt.Snapshot, error) {
		requests++
		<-ctx.Done()
		return nil, ctx.Err()
	})

	result := enrichWithGTFSRealtime(ctx, client, "missing-label", &vehicleResult{Query: "missing-label", MatchedBy: "none"})
	if requests != 1 {
		t.Fatalf("requests = %d, want only the in-flight feed before the shared deadline", requests)
	}
	if len(result.Warnings) == 0 || !strings.Contains(strings.Join(result.Warnings, " "), "deadline exceeded") {
		t.Fatalf("warnings = %v, want shared-deadline evidence", result.Warnings)
	}
}

func TestVehicleJSONGoldenKeepsNamespacesAndPositionEvidenceSeparate(t *testing.T) {
	result := goldenVehicleResult(t)
	stdout := captureVehicleStdout(t, func() error { return printJSON(result) })
	if !json.Valid([]byte(stdout)) {
		t.Fatalf("stdout is not valid JSON:\n%s", stdout)
	}
	if strings.Contains(stdout, "private-internal-42") {
		t.Fatalf("internal vehicle ID leaked:\n%s", stdout)
	}
	var decoded struct {
		Position     *vehiclePositionResult `json:"position"`
		GTFSRealtime map[string]any         `json:"gtfs_realtime"`
	}
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatal(err)
	}
	if _, leaked := decoded.GTFSRealtime["vehicle_id"]; leaked {
		t.Fatalf("GTFS-R internal vehicle_id field leaked: %v", decoded.GTFSRealtime)
	}
	if decoded.Position == nil || decoded.Position.Latitude == nil || *decoded.Position.Latitude != -37.81 {
		t.Fatalf("top-level PTV position = %+v", decoded.Position)
	}
	assertVehicleGolden(t, "vehicle.json.golden", stdout)
}

func TestVehicleJSONMirrorsSanitizedWarningsToStderr(t *testing.T) {
	previousJSON := flagJSON
	flagJSON = true
	defer func() { flagJSON = previousJSON }()

	result := &vehicleResult{
		Query:     "missing",
		MatchedBy: "none",
		Warnings:  []string{"upstream\nwarning\x00"},
	}
	stdout, stderr := captureVehicleOutput(t, func() error {
		return printVehicleCommandResult(result)
	})

	if !json.Valid([]byte(stdout)) {
		t.Fatalf("stdout is not one valid JSON document:\n%s", stdout)
	}
	if strings.Contains(stdout, "\nWarnings") {
		t.Fatalf("human warning polluted JSON stdout:\n%s", stdout)
	}
	if !strings.Contains(stderr, "  - upstreamwarning\n") {
		t.Fatalf("stderr did not contain sanitized warning: %q", stderr)
	}
	if strings.Contains(stderr, "upstream\nwarning") || strings.ContainsRune(stderr, '\x00') {
		t.Fatalf("stderr contained unsanitized warning: %q", stderr)
	}
}

func TestVehicleHumanWarningsUseStderr(t *testing.T) {
	previousJSON := flagJSON
	flagJSON = false
	defer func() { flagJSON = previousJSON }()

	result := &vehicleResult{Query: "missing", MatchedBy: "none", Warnings: []string{"lookup unavailable"}}
	stdout, stderr := captureVehicleOutput(t, func() error {
		return printVehicleCommandResult(result)
	})

	if strings.Contains(stdout, "Warnings") || strings.Contains(stdout, "lookup unavailable") {
		t.Fatalf("warning polluted human stdout: %q", stdout)
	}
	if !strings.Contains(stderr, "Warnings") || !strings.Contains(stderr, "lookup unavailable") {
		t.Fatalf("human warning missing from stderr: %q", stderr)
	}
}

func TestVehicleHumanGoldenShowsPublicIdentityAndFreshness(t *testing.T) {
	result := goldenVehicleResult(t)
	stdout := captureVehicleStdout(t, func() error {
		printVehicleResult(result)
		return nil
	})
	if strings.Contains(stdout, "private-internal-42") {
		t.Fatalf("internal vehicle ID leaked:\n%s", stdout)
	}
	assertVehicleGolden(t, "vehicle.txt.golden", stdout)
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

func goldenVehicleResult(t *testing.T) *vehicleResult {
	t.Helper()
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	message := vehicleRealtimeMessage(now, -10*time.Second, -20*time.Second, "931M-932M", "private-internal-42", "feed-entity-7", "static-trip-9")
	feed, ok := gtfsrt.FeedByID("metro-vehicle-positions")
	if !ok {
		t.Fatal("missing metro-vehicle-positions feed")
	}
	snapshot := gtfsrt.NormalizeSnapshot(feed, message, now)
	observation, ok := snapshot.FindVehicleByLabel(gtfsrt.PublicVehicleLabel("931M"))
	if !ok {
		t.Fatal("golden public label did not match")
	}
	ptvLatitude := -37.81
	ptvLongitude := 144.96
	result := resultFromRun("vehicle_descriptor.id", "931M", ptvapi.Route{RouteType: 0, RouteID: 16, RouteName: "Werribee Line"}, ptvapi.Run{
		RunRef:          "ptv-run-77",
		RouteID:         16,
		RouteType:       0,
		DestinationName: "Werribee",
		Status:          "scheduled",
		VehicleDescriptor: &ptvapi.VehicleDescriptor{
			Operator:    "Metro Trains Melbourne",
			ID:          "931M-932M",
			Description: "6 Car Xtrapolis",
		},
		VehiclePosition: &ptvapi.VehiclePosition{
			Latitude:    &ptvLatitude,
			Longitude:   &ptvLongitude,
			DatetimeUTC: "2026-07-16T11:59:30Z",
		},
	})
	return applyGTFSRealtimeVehicle(result, realtimeVehicleSource{Feed: feed, RouteType: 0, Mode: "Train"}, *observation)
}

func captureVehicleStdout(t *testing.T, run func() error) string {
	t.Helper()
	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	os.Stdout = writer
	err = run()
	_ = writer.Close()
	os.Stdout = oldStdout
	if err != nil {
		_ = reader.Close()
		t.Fatalf("render vehicle output: %v", err)
	}
	var output bytes.Buffer
	if _, err := io.Copy(&output, reader); err != nil {
		_ = reader.Close()
		t.Fatalf("read vehicle output: %v", err)
	}
	_ = reader.Close()
	return output.String()
}

func captureVehicleOutput(t *testing.T, run func() error) (string, string) {
	t.Helper()
	oldStdout, oldStderr := os.Stdout, os.Stderr
	outReader, outWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	errReader, errWriter, err := os.Pipe()
	if err != nil {
		_ = outReader.Close()
		_ = outWriter.Close()
		t.Fatalf("stderr pipe: %v", err)
	}
	os.Stdout, os.Stderr = outWriter, errWriter
	runErr := run()
	_ = outWriter.Close()
	_ = errWriter.Close()
	os.Stdout, os.Stderr = oldStdout, oldStderr

	var stdout, stderr bytes.Buffer
	_, stdoutErr := io.Copy(&stdout, outReader)
	_, stderrErr := io.Copy(&stderr, errReader)
	_ = outReader.Close()
	_ = errReader.Close()
	if runErr != nil {
		t.Fatalf("render vehicle output: %v", runErr)
	}
	if stdoutErr != nil {
		t.Fatalf("read vehicle stdout: %v", stdoutErr)
	}
	if stderrErr != nil {
		t.Fatalf("read vehicle stderr: %v", stderrErr)
	}
	return stdout.String(), stderr.String()
}

func assertVehicleGolden(t *testing.T, name, got string) {
	t.Helper()
	want, err := os.ReadFile("testdata/vehicle/" + name)
	if err != nil {
		t.Fatalf("read golden %s: %v\ngot:\n%s", name, err, got)
	}
	if got != string(want) {
		t.Fatalf("%s mismatch\n--- want ---\n%s\n--- got ---\n%s", name, want, got)
	}
}

func vehicleRealtimeMessage(now time.Time, feedOffset, entityOffset time.Duration, label, internalID, entityID, tripID string) *gtfs.FeedMessage {
	feedTimestamp := uint64(now.Add(feedOffset).Unix())
	entityTimestamp := uint64(now.Add(entityOffset).Unix())
	latitude := float32(-37.818175)
	longitude := float32(144.966776)
	bearing := float32(94)
	speed := float32(13.5)
	return &gtfs.FeedMessage{
		Header: &gtfs.FeedHeader{
			GtfsRealtimeVersion: proto.String("2.0"),
			Timestamp:           &feedTimestamp,
		},
		Entity: []*gtfs.FeedEntity{{
			Id: proto.String(entityID),
			Vehicle: &gtfs.VehiclePosition{
				Trip: &gtfs.TripDescriptor{
					TripId:    proto.String(tripID),
					RouteId:   proto.String("2-WER"),
					StartDate: proto.String("20260716"),
					StartTime: proto.String("12:00:00"),
				},
				Vehicle: &gtfs.VehicleDescriptor{
					Id:           proto.String(internalID),
					Label:        proto.String(label),
					LicensePlate: proto.String("PUBLIC-PLATE"),
				},
				Position:      &gtfs.Position{Latitude: &latitude, Longitude: &longitude, Bearing: &bearing, Speed: &speed},
				CurrentStatus: gtfs.VehiclePosition_IN_TRANSIT_TO.Enum(),
				Timestamp:     &entityTimestamp,
				StopId:        proto.String("stop-1"),
			},
		}},
	}
}
