package gtfs

import (
	"testing"
	"time"

	"github.com/thesammykins/ptv_cli/internal/localtime"
)

func TestSchemaV2QueriesResolvePublicIDsAndJoinKeys(t *testing.T) {
	store, state := newPopulatedStagingStore(t, t.TempDir()+"/queries.sqlite", "queries")
	defer store.Close()
	if err := store.SaveDatasetState(t.Context(), state); err != nil {
		t.Fatal(err)
	}

	stop, err := store.ResolveStop(t.Context(), "1:a", []int{1})
	if err != nil {
		t.Fatal(err)
	}
	if stop.StopID != "1:a" || stop.FeedMode != 1 {
		t.Fatalf("stop = %+v", stop)
	}
	results, err := store.StopSearch(t.Context(), "A", []int{1}, 5)
	if err != nil || len(results) != 1 || results[0].StopID != "1:a" {
		t.Fatalf("search = %+v, %v", results, err)
	}

	date := time.Date(2026, 7, 16, 12, 0, 0, 0, localtime.Melbourne())
	departures, err := store.StopDepartures(t.Context(), "1:a", date, "1:r", []int{1}, 10)
	if err != nil || len(departures) != 1 {
		t.Fatalf("departures = %+v, %v", departures, err)
	}
	if departures[0].TripID != "1:t" || departures[0].RouteID != "1:r" || departures[0].StopID != "1:a" {
		t.Fatalf("departure identity = %+v", departures[0])
	}

	trip, err := store.TripDetail(t.Context(), "1:t", date)
	if err != nil || len(trip.Stops) != 2 {
		t.Fatalf("trip = %+v, %v", trip, err)
	}
	if trip.Stops[1].StopID != "1:b" {
		t.Fatalf("trip stop = %+v", trip.Stops[1])
	}
}

func TestStopSearchTokenRankingAndSubstringFallback(t *testing.T) {
	store, err := CreateStaging(t.Context(), t.TempDir()+"/search.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, statement := range []string{
		`INSERT INTO feeds(feed_key,feed_mode,source_namespace) VALUES(1,2,'metro')`,
		`INSERT INTO stops(stop_key,feed_key,feed_mode,source_stop_id,stop_id,stop_name) VALUES(1,1,2,'1','2:1','Flinders Street Station'),(2,1,2,'2','2:2','Flinders Street'),(3,1,2,'3','2:3','Southern Cross Station')`,
	} {
		if _, err := store.db.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	results, err := store.StopSearch(t.Context(), "Flinders Street", []int{2}, 5)
	if err != nil || len(results) != 2 || results[0].StopID != "2:2" {
		t.Fatalf("token search = %+v, %v", results, err)
	}
	results, err = store.StopSearch(t.Context(), "Street Station", []int{2}, 5)
	if err != nil || len(results) != 1 || results[0].StopID != "2:1" {
		t.Fatalf("fallback search = %+v, %v", results, err)
	}
}

func TestRouteResolutionAcceptsShortNameAndStationDeparturesAggregateChildren(t *testing.T) {
	store, err := CreateStaging(t.Context(), t.TempDir()+"/station.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, statement := range []string{
		`INSERT INTO feeds(feed_key,feed_mode,source_namespace) VALUES(1,2,'metro')`,
		`INSERT INTO routes(route_key,feed_key,feed_mode,source_route_id,route_id,route_short_name,route_long_name) VALUES(1,1,2,'source-109','2:source-109','109','Box Hill - Port Melbourne')`,
		`INSERT INTO stops(stop_key,feed_key,feed_mode,source_stop_id,stop_id,stop_name,location_type) VALUES(1,1,2,'station','2:station','Example Station',1),(2,1,2,'platform','2:platform','Example Station',0)`,
		`UPDATE stops SET parent_stop_key=1 WHERE stop_key=2`,
		`INSERT INTO calendar(service_key,service_id,start_date,end_date,monday,tuesday,wednesday,thursday,friday,saturday,sunday) VALUES(1,'service','20260701','20260731',1,1,1,1,1,1,1)`,
		`INSERT INTO trips(trip_key,feed_key,route_key,service_key,feed_mode,source_trip_id,trip_id) VALUES(1,1,1,1,2,'source-trip','2:source-trip')`,
		`INSERT INTO stop_times(stop_time_key,trip_key,stop_key,stop_sequence,arrival_sec,departure_sec) VALUES(1,1,2,1,3600,3600)`,
		`INSERT INTO connections(connection_key,feed_key,feed_mode,service_key,trip_key,route_key,dep_stop_key,arr_stop_key,dep_sequence,arr_sequence,departure_sec,arrival_sec) VALUES(1,1,2,1,1,1,2,2,1,1,3600,3600)`,
	} {
		if _, err := store.db.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	route, err := store.ResolveRoute(t.Context(), "109", []int{2})
	if err != nil || route.RouteID != "2:source-109" {
		t.Fatalf("short-name route = %+v, %v", route, err)
	}
	departures, err := store.StopDepartures(t.Context(), "2:station", time.Date(2026, 7, 24, 12, 0, 0, 0, localtime.Melbourne()), "109", []int{2}, 5)
	if err != nil || len(departures) != 1 || departures[0].StopID != "2:platform" {
		t.Fatalf("station departures = %+v, %v", departures, err)
	}
}
