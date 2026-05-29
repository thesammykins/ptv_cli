package gtfs

import (
	"testing"
	"time"

	"github.com/thesammykins/ptv_cli/internal/model"
)

func TestFeedModeFromID(t *testing.T) {
	cases := []struct {
		id   string
		want int
	}{
		{"2:11217", 2},
		{"4:30817", 4},
		{"11:55", 11},
		{"noprefix", -1},
		{"abc:123", -1},
		{":123", -1},
	}
	for _, c := range cases {
		if got := feedModeFromID(c.id); got != c.want {
			t.Errorf("feedModeFromID(%q) = %d, want %d", c.id, got, c.want)
		}
	}
}

func TestAppendConnectionsIncludesPreviousDayCrossMidnightSegment(t *testing.T) {
	day := time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC)
	tt := &model.Timetable{TripRoute: map[string]int{"trip": 0}}
	stopIdx := map[string]int{"s1": 0, "s2": 1}
	active := map[string]bool{"svc": true}

	// Use a real store query path by creating the minimal DB rows needed by
	// appendConnections; this specifically checks prevDep < 24h but arr >= 24h.
	store := newTestStoreForTimetable(t)
	insertTimetableRows(t, store)
	err := store.appendConnections(tt, stopIdx, active, day.AddDate(0, 0, -1).Unix(), 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(tt.Connections) != 1 {
		t.Fatalf("connections = %d, want 1", len(tt.Connections))
	}
	if got, want := tt.Connections[0].DepTime, day.AddDate(0, 0, -1).Unix()+86340; got != want {
		t.Fatalf("DepTime = %d, want %d", got, want)
	}
}

func newTestStoreForTimetable(t *testing.T) *Store {
	t.Helper()
	store := newTestStore(t)
	return store
}

func insertTimetableRows(t *testing.T, store *Store) {
	t.Helper()
	stmts := []string{
		`INSERT INTO stops(stop_id, stop_name, stop_lat, stop_lon) VALUES('s1', 'One', -37.8, 144.9)`,
		`INSERT INTO stops(stop_id, stop_name, stop_lat, stop_lon) VALUES('s2', 'Two', -37.81, 144.91)`,
		`INSERT INTO trips(trip_id, route_id, service_id, trip_headsign, direction_id) VALUES('trip', 'route', 'svc', '', 0)`,
		`INSERT INTO stop_times(trip_id, stop_id, stop_sequence, arrival_sec, departure_sec) VALUES('trip', 's1', 1, 86340, 86340)`,
		`INSERT INTO stop_times(trip_id, stop_id, stop_sequence, arrival_sec, departure_sec) VALUES('trip', 's2', 2, 86520, 86520)`,
	}
	for _, stmt := range stmts {
		if _, err := store.db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
}
