package gtfs

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thesammykins/ptv_cli/internal/localtime"
	"github.com/thesammykins/ptv_cli/internal/model"
	"github.com/thesammykins/ptv_cli/internal/router"
)

func TestFeedModeFromID(t *testing.T) {
	cases := []struct {
		id   string
		want int
	}{{"2:11217", 2}, {"4:30817", 4}, {"11:55", 11}, {"noprefix", -1}, {"abc:123", -1}, {":123", -1}}
	for _, test := range cases {
		if got := feedModeFromID(test.id); got != test.want {
			t.Errorf("feedModeFromID(%q) = %d, want %d", test.id, got, test.want)
		}
	}
}

func TestModeFromRouteTypeFallsBackWhenFeedModeMissing(t *testing.T) {
	cases := []struct {
		routeType, feedMode, want int
	}{{400, 2, 2}, {400, 0, 2}, {0, 0, 3}, {701, 0, 4}, {102, 0, 1}, {204, 0, 5}}
	for _, test := range cases {
		if got := modeFromRouteType(test.routeType, test.feedMode); got != test.want {
			t.Fatalf("modeFromRouteType(%d, %d) = %d, want %d", test.routeType, test.feedMode, got, test.want)
		}
	}
}

func TestLoadTimetableContextUsesDistinctConsecutiveServiceDayInstances(t *testing.T) {
	store := compileTimetableFeed(t, minimalFeed(map[string]string{
		"stop_times.txt": "trip_id,arrival_time,departure_time,stop_id,stop_sequence,pickup_type,drop_off_type\n" +
			"t1,23:59:00,23:59:00,s1,1,0,1\n" +
			"t1,24:02:00,24:02:00,s2,2,1,0\n",
		"calendar.txt": "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\n" +
			"svc,1,1,1,1,1,1,1,20260714,20260718\n",
	}))
	query := time.Date(2026, 7, 16, 23, 58, 0, 0, localtime.Melbourne())
	timetable, err := store.LoadTimetableContext(t.Context(), query, TimetableForward)
	if err != nil {
		t.Fatal(err)
	}
	if len(timetable.Connections) < 2 {
		t.Fatalf("connections = %d, want previous and current service-day instances", len(timetable.Connections))
	}
	instances := make(map[model.TripInstanceID]bool)
	for i, connection := range timetable.Connections {
		if i > 0 && timetable.Connections[i-1].DepTime > connection.DepTime {
			t.Fatal("forward connections are not preordered")
		}
		instances[connection.TripInstanceID] = true
	}
	if len(instances) < 2 {
		t.Fatalf("trip instances = %v, want distinct consecutive service dates", instances)
	}
	for id := model.TripInstanceID(1); int(id) < len(timetable.TripInstances); id++ {
		if timetable.TripInstances[id].ID != id {
			t.Fatalf("TripInstances[%d] = %+v", id, timetable.TripInstances[id])
		}
	}
	first := timetable.Connections[0]
	if first.PickupPolicy != model.PassengerActionRegular || first.DropOffPolicy != model.PassengerActionRegular {
		t.Fatalf("connection policies = pickup %d drop %d; policies must use departure and arrival boundaries", first.PickupPolicy, first.DropOffPolicy)
	}
	if timetable.ReverseConnections != nil {
		t.Fatalf("forward load unexpectedly materialized %d reverse connections", len(timetable.ReverseConnections))
	}
	reverse, err := store.LoadTimetableContext(t.Context(), query, TimetableReverse)
	if err != nil {
		t.Fatal(err)
	}
	if reverse.Connections != nil {
		t.Fatalf("reverse load unexpectedly materialized %d forward connections", len(reverse.Connections))
	}
	for i := 1; i < len(reverse.ReverseConnections); i++ {
		if reverse.ReverseConnections[i-1].DepTime > reverse.ReverseConnections[i].DepTime {
			t.Fatal("reverse connections are not preordered")
		}
	}
}

func TestLoadTimetableContextUsesMelbourneDSTServiceAnchor(t *testing.T) {
	store := compileTimetableFeed(t, minimalFeed(map[string]string{
		"stop_times.txt": "trip_id,arrival_time,departure_time,stop_id,stop_sequence\n" +
			"t1,03:00:00,03:00:00,s1,1\n" +
			"t1,03:10:00,03:10:00,s2,2\n",
		"calendar.txt":       "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nsvc,0,0,0,0,0,0,0,20261004,20261004\n",
		"calendar_dates.txt": "service_id,date,exception_type\nsvc,20261004,1\n",
	}))
	query := time.Date(2026, 10, 4, 0, 0, 0, 0, localtime.Melbourne())
	timetable, err := store.LoadTimetableContext(t.Context(), query, TimetableForward)
	if err != nil {
		t.Fatal(err)
	}
	if len(timetable.Connections) != 1 {
		t.Fatalf("connections = %d, want 1", len(timetable.Connections))
	}
	want := localtime.ServiceDayAnchor(query).Add(3 * time.Hour).Unix()
	if got := timetable.Connections[0].DepTime; got != want {
		t.Fatalf("DST departure = %s, want %s", time.Unix(got, 0), time.Unix(want, 0))
	}
}

func TestLoadTimetableContextReturnsTypedCoverageError(t *testing.T) {
	store := compileTimetableFeed(t, minimalFeed(map[string]string{
		"calendar.txt": "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nsvc,1,1,1,1,1,1,1,20260701,20260731\n",
	}))
	_, err := store.LoadTimetableContext(t.Context(), time.Date(2026, 8, 1, 9, 0, 0, 0, localtime.Melbourne()), TimetableForward)
	if !errors.Is(err, ErrQueryOutsideCoverage) {
		t.Fatalf("LoadTimetableContext() error = %v, want ErrQueryOutsideCoverage", err)
	}
	var coverageErr *CoverageOutsideError
	if !errors.As(err, &coverageErr) || coverageErr.Coverage.End != "20260731" {
		t.Fatalf("coverage error = %#v", err)
	}
}

func TestLoadTimetableContextAllowsPreviousServiceDayOverflowPastCoverageDate(t *testing.T) {
	store := compileTimetableFeed(t, minimalFeed(map[string]string{
		"calendar.txt": "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nsvc,1,1,1,1,1,1,1,20260731,20260731\n",
		"stop_times.txt": "trip_id,arrival_time,departure_time,stop_id,stop_sequence\n" +
			"t1,25:00:00,25:00:00,s1,1\n" +
			"t1,25:10:00,25:10:00,s2,2\n",
	}))
	query := time.Date(2026, 8, 1, 0, 30, 0, 0, localtime.Melbourne())
	timetable, err := store.LoadTimetableContext(t.Context(), query, TimetableForward)
	if err != nil {
		t.Fatal(err)
	}
	if len(timetable.Connections) != 1 {
		t.Fatalf("overflow connections = %d, want 1", len(timetable.Connections))
	}
	want := localtime.ServiceDayAnchor(time.Date(2026, 7, 31, 12, 0, 0, 0, localtime.Melbourne())).Add(25 * time.Hour).Unix()
	if timetable.Connections[0].DepTime != want {
		t.Fatalf("overflow departure = %s, want %s", time.Unix(timetable.Connections[0].DepTime, 0), time.Unix(want, 0))
	}
}

func TestLoadTimetableContextBuildsPathwaysAndStationExpandedRules(t *testing.T) {
	feed := minimalFeed(map[string]string{
		"stops.txt": "stop_id,stop_name,stop_lat,stop_lon,location_type,parent_station\n" +
			"station,Station,-37.8000,144.9000,1,\n" +
			"a,Platform A,-37.8000,144.9000,0,station\n" +
			"b,Platform B,-37.8001,144.9001,0,station\n" +
			"c,Street Stop,-37.8002,144.9002,0,\n",
		"stop_times.txt": "trip_id,arrival_time,departure_time,stop_id,stop_sequence\n" +
			"t1,08:00:00,08:00:00,a,1\n" +
			"t1,08:10:00,08:10:00,b,2\n",
		"calendar.txt": "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nsvc,1,1,1,1,1,1,1,20260701,20260731\n",
		"pathways.txt": "pathway_id,from_stop_id,to_stop_id,pathway_mode,is_bidirectional,traversal_time\nwalk,a,b,1,0,45\n",
		"transfers.txt": "from_stop_id,to_stop_id,transfer_type,min_transfer_time\n" +
			"a,b,2,5\n" +
			"station,c,3,\n",
	})
	store := compileTimetableFeed(t, feed)
	timetable, err := store.LoadTimetableContext(t.Context(), time.Date(2026, 7, 16, 7, 0, 0, 0, localtime.Melbourne()), TimetableForward)
	if err != nil {
		t.Fatal(err)
	}
	a, b, c := stopIndex(t, timetable, "2:a"), stopIndex(t, timetable, "2:b"), stopIndex(t, timetable, "2:c")
	if edge, ok := findWalkEdge(timetable.WalkEdges[a], b); !ok || edge.Kind != model.WalkEdgePathway || edge.Seconds != 45 {
		t.Fatalf("A->B pathway = %+v, %v", edge, ok)
	}
	if _, ok := findWalkEdge(timetable.WalkEdges[b], a); ok {
		t.Fatal("one-way pathway gained a reverse/proximity shortcut")
	}
	for _, from := range []int{a, b} {
		found := false
		for _, rule := range timetable.TransferRules {
			if rule.FromStop == from && rule.ToStop == c && rule.Type == model.TransferForbidden {
				if rule.FromRouteIdx != -1 || rule.ToRouteIdx != -1 ||
					rule.FromTripInstanceID != model.UnknownTripInstanceID || rule.ToTripInstanceID != model.UnknownTripInstanceID {
					t.Fatalf("wildcard transfer fields = %+v", rule)
				}
				found = true
			}
		}
		if !found {
			t.Fatalf("station-scoped prohibition missing for child stop %d", from)
		}
	}
}

func TestLoadTimetableContextStopAccessKeepsDirectStreetProximity(t *testing.T) {
	store := compileTimetableFeed(t, minimalFeed(map[string]string{
		"stops.txt": "stop_id,stop_name,stop_lat,stop_lon,location_type,parent_station,stop_access\n" +
			"station,Station,-37.8000,144.9000,1,,\n" +
			"direct,Direct Platform,-37.8000,144.9000,0,station,1\n" +
			"platform,Internal Platform,-37.8001,144.9001,0,station,\n" +
			"entrance,Entrance,-37.8002,144.9002,2,station,\n" +
			"street,Street Stop,-37.8000,144.9010,0,,\n",
		"stop_times.txt": "trip_id,arrival_time,departure_time,stop_id,stop_sequence\n" +
			"t1,08:00:00,08:00:00,direct,1\n" +
			"t1,08:10:00,08:10:00,platform,2\n",
		"calendar.txt": "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nsvc,1,1,1,1,1,1,1,20260701,20260731\n",
		"pathways.txt": "pathway_id,from_stop_id,to_stop_id,pathway_mode,is_bidirectional,traversal_time\n" +
			"internal,platform,entrance,1,1,60\n",
	}))
	timetable, err := store.LoadTimetableContext(t.Context(), time.Date(2026, 7, 16, 7, 0, 0, 0, localtime.Melbourne()), TimetableForward)
	if err != nil {
		t.Fatal(err)
	}
	direct := stopIndex(t, timetable, "2:direct")
	platform := stopIndex(t, timetable, "2:platform")
	street := stopIndex(t, timetable, "2:street")
	if edge, ok := findWalkEdge(timetable.WalkEdges[direct], street); !ok || edge.Kind != model.WalkEdgeProximity {
		t.Fatalf("stop_access=1 direct street edge = %+v, %v", edge, ok)
	}
	if _, ok := findWalkEdge(timetable.WalkEdges[platform], street); ok {
		t.Fatal("internal platform bypassed its authoritative pathway component")
	}
}

func TestLoadTimetableContextProximitySpanAndExplicitProhibition(t *testing.T) {
	store := compileTimetableFeed(t, minimalFeed(map[string]string{
		"stops.txt": "stop_id,stop_name,stop_lat,stop_lon\na,A,-37.8000,144.9000\nb,B,-37.8000,144.9027\n",
		"stop_times.txt": "trip_id,arrival_time,departure_time,stop_id,stop_sequence\n" +
			"t1,08:00:00,08:00:00,a,1\n" +
			"t1,08:10:00,08:10:00,b,2\n",
		"calendar.txt":  "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nsvc,1,1,1,1,1,1,1,20260701,20260731\n",
		"transfers.txt": "from_stop_id,to_stop_id,transfer_type,min_transfer_time\na,b,3,\n",
	}))
	timetable, err := store.LoadTimetableContext(t.Context(), time.Date(2026, 7, 16, 7, 0, 0, 0, localtime.Melbourne()), TimetableForward)
	if err != nil {
		t.Fatal(err)
	}
	a, b := stopIndex(t, timetable, "2:a"), stopIndex(t, timetable, "2:b")
	if _, exists := findWalkEdge(timetable.WalkEdges[a], b); exists {
		t.Fatal("explicit type-3 prohibition was bypassed by a proximity edge")
	}
	edge, exists := findWalkEdge(timetable.WalkEdges[b], a)
	if !exists || edge.Kind != model.WalkEdgeProximity {
		t.Fatalf("reverse proximity edge across latitude-derived grid span = %+v, %v", edge, exists)
	}
}

func TestLoadTimetableContextSpecificProhibitionDoesNotRemoveGeneralProximity(t *testing.T) {
	store := compileTimetableFeed(t, minimalFeed(map[string]string{
		"trips.txt": "route_id,service_id,trip_id,trip_headsign,direction_id\n" +
			"r1,svc,t1,First,0\n" +
			"r1,svc,t2,Second,0\n",
		"stops.txt": "stop_id,stop_name,stop_lat,stop_lon\n" +
			"x,X,-37.8000,144.8990\n" +
			"a,A,-37.8000,144.9000\n" +
			"b,B,-37.8000,144.9010\n" +
			"y,Y,-37.8000,144.9020\n",
		"stop_times.txt": "trip_id,arrival_time,departure_time,stop_id,stop_sequence\n" +
			"t1,08:00:00,08:00:00,x,1\n" +
			"t1,08:10:00,08:10:00,a,2\n" +
			"t2,08:15:00,08:15:00,b,1\n" +
			"t2,08:25:00,08:25:00,y,2\n",
		"calendar.txt": "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nsvc,1,1,1,1,1,1,1,20260701,20260731\n",
		"transfers.txt": "from_stop_id,to_stop_id,from_trip_id,to_trip_id,transfer_type,min_transfer_time\n" +
			"a,b,t1,t2,3,\n" +
			"b,a,t1,t2,2,900\n",
	}))
	timetable, err := store.LoadTimetableContext(t.Context(), time.Date(2026, 7, 16, 7, 0, 0, 0, localtime.Melbourne()), TimetableForward)
	if err != nil {
		t.Fatal(err)
	}
	a, b := stopIndex(t, timetable, "2:a"), stopIndex(t, timetable, "2:b")
	edge, exists := findWalkEdge(timetable.WalkEdges[a], b)
	if !exists || edge.Kind != model.WalkEdgeProximity {
		t.Fatalf("trip-specific prohibition removed the general proximity edge: %+v, %v", edge, exists)
	}
	var foundSpecific bool
	for _, rule := range timetable.TransferRules {
		if rule.FromStop == a && rule.ToStop == b &&
			rule.FromTripInstanceID != model.UnknownTripInstanceID &&
			rule.ToTripInstanceID != model.UnknownTripInstanceID && rule.Type == model.TransferForbidden {
			foundSpecific = true
		}
	}
	if !foundSpecific {
		t.Fatalf("specific prohibition missing from transfer rules: %+v", timetable.TransferRules)
	}
	reverseEdge, reverseExists := findWalkEdge(timetable.WalkEdges[b], a)
	if !reverseExists || reverseEdge.Seconds >= 900 {
		t.Fatalf("trip-qualified minimum became a global physical duration: %+v, %v", reverseEdge, reverseExists)
	}
	var foundMinimum bool
	for _, rule := range timetable.TransferRules {
		if rule.FromStop == b && rule.ToStop == a && rule.Type == model.TransferMinimumTime && rule.MinTransferSeconds == 900 {
			foundMinimum = true
		}
	}
	if !foundMinimum {
		t.Fatalf("specific minimum missing from transfer rules: %+v", timetable.TransferRules)
	}
}

func TestLoadTimetableContextExplicitNoStayOnboardOverridesBlock(t *testing.T) {
	store := compileTimetableFeed(t, minimalFeed(map[string]string{
		"trips.txt": "route_id,service_id,trip_id,trip_headsign,direction_id,block_id\n" +
			"r1,svc,t1,Middle,0,block\n" +
			"r1,svc,t2,End,0,block\n",
		"stops.txt": "stop_id,stop_name,stop_lat,stop_lon\na,A,-37.8,144.9\nb,B,-37.81,144.91\nc,C,-37.82,144.92\n",
		"stop_times.txt": "trip_id,arrival_time,departure_time,stop_id,stop_sequence\n" +
			"t1,08:00:00,08:00:00,a,1\n" +
			"t1,08:10:00,08:10:00,b,2\n" +
			"t2,08:10:00,08:10:00,b,1\n" +
			"t2,08:20:00,08:20:00,c,2\n",
		"calendar.txt": "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nsvc,1,1,1,1,1,1,1,20260701,20260731\n",
		"transfers.txt": "from_stop_id,to_stop_id,from_trip_id,to_trip_id,transfer_type,min_transfer_time\n" +
			"b,b,t1,t2,4,\n" +
			"b,b,t1,t2,5,\n",
	}))
	timetable, err := store.LoadTimetableContext(t.Context(), time.Date(2026, 7, 16, 7, 0, 0, 0, localtime.Melbourne()), TimetableForward)
	if err != nil {
		t.Fatal(err)
	}
	var forbidden, allowed bool
	for _, continuation := range timetable.Continuations {
		if continuation.Type == model.TransferNoStayOnboard {
			forbidden = true
		}
		if continuation.Type == model.TransferStayOnboard {
			allowed = true
		}
	}
	if !forbidden || allowed {
		t.Fatalf("continuations = %+v, want explicit type 5 only", timetable.Continuations)
	}
}

func TestLoadTimetableContextDoesNotInferContinuationFromBlockID(t *testing.T) {
	store := compileTimetableFeed(t, minimalFeed(map[string]string{
		"trips.txt": "route_id,service_id,trip_id,trip_headsign,direction_id,block_id\n" +
			"r1,svc,t1,Middle,0,block\n" +
			"r1,svc,t2,End,0,block\n",
		"stops.txt": "stop_id,stop_name,stop_lat,stop_lon\na,A,-37.8,144.9\nb,B,-37.81,144.91\nc,C,-37.82,144.92\n",
		"stop_times.txt": "trip_id,arrival_time,departure_time,stop_id,stop_sequence\n" +
			"t1,08:00:00,08:00:00,a,1\n" +
			"t1,08:10:00,08:10:00,b,2\n" +
			"t2,08:10:00,08:10:00,b,1\n" +
			"t2,08:20:00,08:20:00,c,2\n",
		"calendar.txt": "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nsvc,1,1,1,1,1,1,1,20260701,20260731\n",
	}))
	timetable, err := store.LoadTimetableContext(t.Context(), time.Date(2026, 7, 16, 7, 0, 0, 0, localtime.Melbourne()), TimetableForward)
	if err != nil {
		t.Fatal(err)
	}
	if len(timetable.Continuations) != 0 {
		t.Fatalf("equal block_id inferred routing continuation: %+v", timetable.Continuations)
	}
}

func TestLoadTimetableContextLinkedTripsAcrossDistinctStops(t *testing.T) {
	tests := []struct {
		name              string
		transfer          string
		wantEndpointStops bool
	}{
		{name: "specified endpoints", transfer: "b,c,t1,t2,4,\n", wantEndpointStops: true},
		{name: "omitted endpoints", transfer: ",,t1,t2,4,\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := compileTimetableFeed(t, minimalFeed(map[string]string{
				"trips.txt": "route_id,service_id,trip_id,trip_headsign,direction_id\n" +
					"r1,svc,t1,Middle,0\n" +
					"r1,svc,t2,End,0\n",
				"stops.txt": "stop_id,stop_name,stop_lat,stop_lon\n" +
					"a,A,-37.8000,144.9000\n" +
					"b,B,-37.8100,144.9100\n" +
					"c,C,-37.8118,144.9100\n" +
					"d,D,-37.8200,144.9200\n",
				"stop_times.txt": "trip_id,arrival_time,departure_time,stop_id,stop_sequence,pickup_type,drop_off_type\n" +
					"t1,08:00:00,08:00:00,a,1,0,0\n" +
					"t1,08:10:00,08:10:00,b,2,0,1\n" +
					"t2,08:11:00,08:11:00,c,1,1,0\n" +
					"t2,08:20:00,08:20:00,d,2,0,0\n",
				"calendar.txt":  "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nsvc,1,1,1,1,1,1,1,20260701,20260731\n",
				"transfers.txt": "from_stop_id,to_stop_id,from_trip_id,to_trip_id,transfer_type,min_transfer_time\n" + test.transfer,
			}))
			query := time.Date(2026, 7, 16, 7, 0, 0, 0, localtime.Melbourne())
			timetable, err := store.LoadTimetableContext(t.Context(), query, TimetableForward)
			if err != nil {
				t.Fatal(err)
			}
			a := stopIndex(t, timetable, "2:a")
			b := stopIndex(t, timetable, "2:b")
			c := stopIndex(t, timetable, "2:c")
			d := stopIndex(t, timetable, "2:d")
			var linked bool
			for _, continuation := range timetable.Continuations {
				if continuation.Type != model.TransferStayOnboard {
					continue
				}
				wantFrom, wantTo := -1, -1
				if test.wantEndpointStops {
					wantFrom, wantTo = b, c
				}
				if continuation.FromStop == wantFrom && continuation.ToStop == wantTo {
					linked = true
				}
			}
			if !linked {
				t.Fatalf("distinct-stop continuation = %+v", timetable.Continuations)
			}
			journey, err := router.PlanEarliestArrival(timetable, []int{a}, []int{d}, query)
			if err != nil {
				t.Fatal(err)
			}
			if len(journey.Legs) != 2 || journey.Legs[0].StayOnboard || !journey.Legs[1].StayOnboard || journey.Transfers != 0 {
				t.Fatalf("linked-trip journey = %+v", journey)
			}

			reverse, err := store.LoadTimetableContext(
				t.Context(),
				time.Date(2026, 7, 16, 8, 30, 0, 0, localtime.Melbourne()),
				TimetableReverse,
			)
			if err != nil {
				t.Fatal(err)
			}
			reverseA := stopIndex(t, reverse, "2:a")
			reverseD := stopIndex(t, reverse, "2:d")
			reverseJourney, err := router.PlanLatestDeparture(
				reverse,
				[]int{reverseA},
				[]int{reverseD},
				time.Date(2026, 7, 16, 8, 30, 0, 0, localtime.Melbourne()),
			)
			if err != nil {
				t.Fatal(err)
			}
			if len(reverseJourney.Legs) != 2 || reverseJourney.Legs[0].StayOnboard || !reverseJourney.Legs[1].StayOnboard || reverseJourney.Transfers != 0 {
				t.Fatalf("reverse linked-trip journey = %+v", reverseJourney)
			}
		})
	}
}

func TestLoadTimetableContextPropagatesCancellation(t *testing.T) {
	store := compileTimetableFeed(t, minimalFeed(map[string]string{
		"calendar.txt": "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nsvc,1,1,1,1,1,1,1,20260701,20260731\n",
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := store.LoadTimetableContext(ctx, time.Date(2026, 7, 16, 7, 0, 0, 0, localtime.Melbourne()), TimetableForward)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestLoadTimetableContextResolvesParentStationNameToRoutablePlatforms(t *testing.T) {
	store := compileTimetableFeed(t, minimalFeed(map[string]string{
		"stops.txt": "stop_id,stop_name,stop_lat,stop_lon,location_type,parent_station\n" +
			"station,Central Railway Station,-37.8,144.9,1,\n" +
			"s1,Central Platform 1,-37.8,144.9,0,station\n" +
			"s2,Central Platform 2,-37.81,144.91,0,station\n",
		"calendar.txt": "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nsvc,1,1,1,1,1,1,1,20260701,20260731\n",
	}))
	timetable, err := store.LoadTimetableContext(
		t.Context(),
		time.Date(2026, 7, 16, 7, 0, 0, 0, localtime.Melbourne()),
		TimetableForward,
	)
	if err != nil {
		t.Fatal(err)
	}
	indexes := timetable.NameIndex["central railway station"]
	if len(indexes) != 2 {
		t.Fatalf("station endpoints = %v, want two platforms", indexes)
	}
	for _, index := range indexes {
		if timetable.Stops[index].ID == "2:station" {
			t.Fatalf("station parent was exposed as a routing endpoint: %v", indexes)
		}
	}
}

func compileTimetableFeed(t *testing.T, files map[string]string) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "timetable.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	archive := filepath.Join(t.TempDir(), "feed.zip")
	writeOuterGTFSZip(t, archive, files)
	_, err = IngestGeneration(t.Context(), store, archive, IngestGenerationOptions{
		GenerationID: "g-test", Provenance: FeedProvenance{SourceURL: "https://example.test/gtfs.zip", ActualBytes: 1},
		IngestedAt: time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func stopIndex(t *testing.T, timetable *model.Timetable, id string) int {
	t.Helper()
	for _, stop := range timetable.Stops {
		if stop.ID == id {
			return stop.Index
		}
	}
	t.Fatalf("stop %s not found", id)
	return -1
}

func findWalkEdge(edges []model.WalkEdge, to int) (model.WalkEdge, bool) {
	for _, edge := range edges {
		if edge.ToStop == to {
			return edge, true
		}
	}
	return model.WalkEdge{}, false
}

func TestConnectionQueryPlanUsesMaterializedTemplates(t *testing.T) {
	store := compileTimetableFeed(t, minimalFeed(map[string]string{
		"calendar.txt": "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nsvc,1,1,1,1,1,1,1,20260701,20260731\n",
	}))
	if _, err := store.db.ExecContext(t.Context(), indexes); err != nil {
		t.Fatal(err)
	}
	rows, err := store.db.QueryContext(t.Context(), `EXPLAIN QUERY PLAN SELECT connection_key FROM connections WHERE service_key IN (?) AND departure_sec>=? AND departure_sec<=? ORDER BY departure_sec`, 1, 0, 86400)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(detail)
	}
	if !strings.Contains(plan.String(), "idx_connections_forward") {
		t.Fatalf("connection query plan = %q, want idx_connections_forward", plan.String())
	}

	reverseRows, err := store.db.QueryContext(t.Context(), `EXPLAIN QUERY PLAN SELECT connection_key FROM connections WHERE service_key IN (?) AND arrival_sec>=? AND arrival_sec<=? ORDER BY arrival_sec DESC`, 1, 0, 86400)
	if err != nil {
		t.Fatal(err)
	}
	defer reverseRows.Close()
	plan.Reset()
	for reverseRows.Next() {
		var id, parent, unused int
		var detail string
		if err := reverseRows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(detail)
	}
	if !strings.Contains(plan.String(), "idx_connections_reverse") {
		t.Fatalf("reverse connection query plan = %q, want idx_connections_reverse", plan.String())
	}
}

func TestMergeConnectionStreamsUsesDeterministicTemplateTieBreak(t *testing.T) {
	streams := [][]connectionRecord{
		{{connection: model.Connection{DepTime: 100, TripInstanceID: 2}, orderKey: 20}},
		{{connection: model.Connection{DepTime: 100, TripInstanceID: 1}, orderKey: 10}},
		{{connection: model.Connection{DepTime: 100, TripInstanceID: 3}, orderKey: 10}},
	}
	merged := mergeConnectionStreams(streams)
	if len(merged) != 3 {
		t.Fatalf("merged length = %d", len(merged))
	}
	wantInstances := []model.TripInstanceID{1, 3, 2}
	for i, want := range wantInstances {
		if got := merged[i].connection.TripInstanceID; got != want {
			t.Fatalf("merged[%d].TripInstanceID = %d, want %d", i, got, want)
		}
	}
}

func TestLoadTimetableContextZeroTimeSegmentsFollowStopSequence(t *testing.T) {
	store := compileTimetableFeed(t, minimalFeed(map[string]string{
		"stops.txt": "stop_id,stop_name,stop_lat,stop_lon\ns1,One,-37.8,144.9\ns2,Two,-37.81,144.91\ns3,Three,-37.82,144.92\n",
		"stop_times.txt": "trip_id,arrival_time,departure_time,stop_id,stop_sequence\n" +
			"t1,08:00:00,08:00:00,s1,1\n" +
			"t1,08:00:00,08:00:00,s2,2\n" +
			"t1,08:00:00,08:00:00,s3,3\n",
		"calendar.txt": "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nsvc,1,1,1,1,1,1,1,20260701,20260731\n",
	}))
	timetable, err := store.LoadTimetableContext(t.Context(), time.Date(2026, 7, 16, 7, 0, 0, 0, localtime.Melbourne()), TimetableForward)
	if err != nil {
		t.Fatal(err)
	}
	s1 := stopIndex(t, timetable, "2:s1")
	s2 := stopIndex(t, timetable, "2:s2")
	s3 := stopIndex(t, timetable, "2:s3")
	if len(timetable.Connections) < 2 ||
		timetable.Connections[0].DepStop != s1 || timetable.Connections[0].ArrStop != s2 ||
		timetable.Connections[1].DepStop != s2 || timetable.Connections[1].ArrStop != s3 {
		t.Fatalf("forward zero-time connection order = %+v", timetable.Connections)
	}
	reverse, err := store.LoadTimetableContext(t.Context(), time.Date(2026, 7, 16, 9, 0, 0, 0, localtime.Melbourne()), TimetableReverse)
	if err != nil {
		t.Fatal(err)
	}
	if len(reverse.ReverseConnections) < 2 ||
		reverse.ReverseConnections[0].DepStop != s3 || reverse.ReverseConnections[0].ArrStop != s2 ||
		reverse.ReverseConnections[1].DepStop != s2 || reverse.ReverseConnections[1].ArrStop != s1 {
		t.Fatalf("reverse zero-time connection order = %+v", reverse.ReverseConnections)
	}
}

func TestLoadTimetableWindowContextBoundsAndDirection(t *testing.T) {
	store := compileTimetableFeed(t, minimalFeed(map[string]string{
		"stop_times.txt": "trip_id,arrival_time,departure_time,stop_id,stop_sequence\n" +
			"t1,09:30:00,09:30:00,s1,1\n" +
			"t1,09:40:00,09:40:00,s2,2\n",
		"calendar.txt": "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nsvc,1,1,1,1,1,1,1,20260701,20260731\n",
	}))
	query := time.Date(2026, 7, 16, 7, 0, 0, 0, localtime.Melbourne())

	short, err := store.LoadTimetableWindowContext(t.Context(), query, TimetableForward, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(short.Connections) != 0 || short.ReverseConnections != nil {
		t.Fatalf("two-hour forward window = forward %d reverse %d", len(short.Connections), len(short.ReverseConnections))
	}

	wide, err := store.LoadTimetableWindowContext(t.Context(), query, TimetableForward, 6*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(wide.Connections) != 1 || wide.ReverseConnections != nil {
		t.Fatalf("six-hour forward window = forward %d reverse %d", len(wide.Connections), len(wide.ReverseConnections))
	}

	reverse, err := store.LoadTimetableWindowContext(t.Context(), query.Add(3*time.Hour), TimetableReverse, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if reverse.Connections != nil || len(reverse.ReverseConnections) != 1 {
		t.Fatalf("reverse window = forward %d reverse %d", len(reverse.Connections), len(reverse.ReverseConnections))
	}
}

func TestLoadTimetableWindowContextRejectsInvalidHorizon(t *testing.T) {
	store := compileTimetableFeed(t, minimalFeed(map[string]string{
		"calendar.txt": "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nsvc,1,1,1,1,1,1,1,20260701,20260731\n",
	}))
	query := time.Date(2026, 7, 16, 7, 0, 0, 0, localtime.Melbourne())
	for _, horizon := range []time.Duration{0, -time.Second, TimetableHorizon + time.Second} {
		if _, err := store.LoadTimetableWindowContext(t.Context(), query, TimetableForward, horizon); err == nil {
			t.Fatalf("horizon %s unexpectedly succeeded", horizon)
		}
	}
}
