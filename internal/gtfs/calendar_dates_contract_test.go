package gtfs

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestIngestAllowsAddOnlyServiceAlongsideCalendar(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "gtfs.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	archive := filepath.Join(t.TempDir(), "feed.zip")
	writeOuterGTFSZip(t, archive, minimalFeed(map[string]string{
		"calendar.txt": "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\n" +
			"base,1,1,1,1,1,1,1,20260701,20260731\n",
		"calendar_dates.txt": "service_id,date,exception_type\n" +
			"special,20260716,1\n" +
			"special,20260718,1\n",
		"trips.txt": "route_id,service_id,trip_id,trip_headsign,direction_id\n" +
			"r1,special,t1,Towards,0\n",
	}))

	if err := Ingest(t.Context(), store, archive, nil); err != nil {
		t.Fatalf("Ingest() error = %v", err)
	}
	if err := store.ValidateResolvedDataset(t.Context()); err != nil {
		t.Fatalf("ValidateResolvedDataset() error = %v", err)
	}

	var serviceKey int64
	var startDate, endDate string
	var activeWeekdays int
	if err := store.db.QueryRowContext(t.Context(), `SELECT service_key,start_date,end_date,
		monday+tuesday+wednesday+thursday+friday+saturday+sunday
		FROM calendar WHERE source_service_id='special'`).Scan(&serviceKey, &startDate, &endDate, &activeWeekdays); err != nil {
		t.Fatal(err)
	}
	if startDate != "20260716" || endDate != "20260718" || activeWeekdays != 0 {
		t.Fatalf("synthetic calendar row = %s..%s weekdays=%d, want 20260716..20260718 weekdays=0", startDate, endDate, activeWeekdays)
	}

	var exceptionCount int
	if err := store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM calendar_dates
		WHERE service_key=? AND exception_type=1`, serviceKey).Scan(&exceptionCount); err != nil {
		t.Fatal(err)
	}
	if exceptionCount != 2 {
		t.Fatalf("add-only exception count = %d, want 2", exceptionCount)
	}

	var tripServiceKey int64
	if err := store.db.QueryRowContext(t.Context(), `SELECT service_key FROM trips WHERE source_trip_id='t1'`).Scan(&tripServiceKey); err != nil {
		t.Fatal(err)
	}
	if tripServiceKey != serviceKey {
		t.Fatalf("trip service_key = %d, want synthetic service_key %d", tripServiceKey, serviceKey)
	}
}

func TestIngestRejectsUnknownRemovalAlongsideCalendar(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "gtfs.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	archive := filepath.Join(t.TempDir(), "feed.zip")
	writeOuterGTFSZip(t, archive, minimalFeed(map[string]string{
		"calendar_dates.txt": "service_id,date,exception_type\nspecial,20260716,2\n",
	}))

	err = Ingest(t.Context(), store, archive, nil)
	if err == nil || !strings.Contains(err.Error(), "date-only service_id special must use exception_type 1") {
		t.Fatalf("Ingest() error = %v, want unknown-removal validation error", err)
	}
}
