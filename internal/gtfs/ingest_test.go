package gtfs

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIngestFailureDoesNotClearExistingData(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "gtfs.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.db.Exec(`INSERT INTO stops(stop_id, stop_name, stop_lat, stop_lon) VALUES('old', 'Old Stop', -37.8, 144.9)`); err != nil {
		t.Fatal(err)
	}

	zipPath := filepath.Join(t.TempDir(), "bad.zip")
	writeOuterGTFSZip(t, zipPath, map[string]string{
		"stops.txt": "stop_id,stop_name,stop_lat,stop_lon\nnew,New Stop,not-a-lat,144.9\n",
	})

	if err := Ingest(context.Background(), store, zipPath, nil); err == nil {
		t.Fatal("Ingest succeeded, want parse error")
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM stops WHERE stop_id = 'old'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("existing data count = %d, want 1", count)
	}
}

func TestIngestRejectsInvalidGTFSTime(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "gtfs.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	zipPath := filepath.Join(t.TempDir(), "bad-time.zip")
	writeOuterGTFSZip(t, zipPath, minimalFeed(map[string]string{
		"stop_times.txt": "trip_id,arrival_time,departure_time,stop_id,stop_sequence\nt1,25:99:00,25:99:00,s1,1\n",
	}))

	err = Ingest(context.Background(), store, zipPath, nil)
	if err == nil || !strings.Contains(err.Error(), "invalid arrival_time") {
		t.Fatalf("Ingest error = %v, want invalid arrival_time", err)
	}
}

func TestByteLimitReaderReportsOverflow(t *testing.T) {
	r := csv.NewReader(&byteLimitReader{r: strings.NewReader("col\nvalue\n"), limit: 4})

	if _, err := r.Read(); err != nil {
		t.Fatalf("Read header: %v", err)
	}
	if _, err := r.Read(); err == nil || err == io.EOF || !strings.Contains(err.Error(), "CSV entry too large") {
		t.Fatalf("Read overflow error = %v, want CSV entry too large", err)
	}
}

func TestIngestRejectsInvalidFeedContracts(t *testing.T) {
	tests := []struct {
		name      string
		innerName string
		feed      func() map[string]string
		want      string
	}{
		{
			name: "unknown branch", innerName: "99/google_transit.zip",
			feed: func() map[string]string { return minimalFeed(nil) }, want: "no recognized",
		},
		{
			name: "missing core file", innerName: "2/google_transit.zip",
			feed: func() map[string]string { feed := minimalFeed(nil); delete(feed, "routes.txt"); return feed }, want: "missing required GTFS file routes.txt",
		},
		{
			name: "malformed core header", innerName: "2/google_transit.zip",
			feed: func() map[string]string {
				return minimalFeed(map[string]string{"routes.txt": "route_id,route_short_name\nr1,R\n"})
			}, want: "missing required header \"route_type\"",
		},
		{
			name: "missing calendar condition", innerName: "2/google_transit.zip",
			feed: func() map[string]string {
				feed := minimalFeed(nil)
				delete(feed, "calendar.txt")
				delete(feed, "calendar_dates.txt")
				return feed
			}, want: "requires calendar.txt or calendar_dates.txt",
		},
		{
			name: "zero core rows", innerName: "2/google_transit.zip",
			feed: func() map[string]string {
				return minimalFeed(map[string]string{
					"trips.txt":      "route_id,service_id,trip_id,trip_headsign,direction_id\n",
					"stop_times.txt": "trip_id,arrival_time,departure_time,stop_id,stop_sequence\n",
				})
			}, want: "zero core rows",
		},
		{
			name: "unresolved trip route", innerName: "2/google_transit.zip",
			feed: func() map[string]string {
				return minimalFeed(map[string]string{"trips.txt": "route_id,service_id,trip_id,trip_headsign,direction_id\nmissing,svc,t1,Towards,0\n"})
			}, want: "references unknown route_id missing",
		},
		{
			name: "unresolved transfer route", innerName: "2/google_transit.zip",
			feed: func() map[string]string {
				return minimalFeed(map[string]string{"transfers.txt": "from_stop_id,to_stop_id,from_route_id,transfer_type,min_transfer_time\ns1,s2,missing,2,60\n"})
			}, want: "unknown route_id missing",
		},
		{
			name: "unresolved transfer trip", innerName: "2/google_transit.zip",
			feed: func() map[string]string {
				return minimalFeed(map[string]string{"transfers.txt": "from_stop_id,to_stop_id,from_trip_id,to_trip_id,transfer_type,min_transfer_time\ns1,s2,missing,t1,4,\n"})
			}, want: "unknown trip_id missing",
		},
		{
			name: "unresolved pathway stop", innerName: "2/google_transit.zip",
			feed: func() map[string]string {
				return minimalFeed(map[string]string{"pathways.txt": "pathway_id,from_stop_id,to_stop_id,pathway_mode,is_bidirectional\np,s1,missing,1,1\n"})
			}, want: "unknown to_stop_id missing",
		},
		{
			name: "unresolved parent station", innerName: "2/google_transit.zip",
			feed: func() map[string]string {
				return minimalFeed(map[string]string{"stops.txt": "stop_id,stop_name,stop_lat,stop_lon,location_type,parent_station\ns1,One,-37.8,144.9,0,missing\ns2,Two,-37.81,144.91,0,\n"})
			}, want: "references unknown parent_station missing",
		},
		{
			name: "out of range coordinate", innerName: "2/google_transit.zip",
			feed: func() map[string]string {
				return minimalFeed(map[string]string{"stops.txt": "stop_id,stop_name,stop_lat,stop_lon\ns1,One,91,144.9\ns2,Two,-37.81,144.91\n"})
			}, want: "invalid stop_lat",
		},
		{
			name: "invalid hierarchy type", innerName: "2/google_transit.zip",
			feed: func() map[string]string {
				return minimalFeed(map[string]string{"stops.txt": "stop_id,stop_name,stop_lat,stop_lon,location_type,parent_station\ns1,One,-37.8,144.9,0,s2\ns2,Two,-37.81,144.91,0,\n"})
			}, want: "requires a station parent",
		},
		{
			name: "pathway station endpoint", innerName: "2/google_transit.zip",
			feed: func() map[string]string {
				return minimalFeed(map[string]string{
					"stops.txt":    "stop_id,stop_name,stop_lat,stop_lon,location_type,parent_station\nstation,Station,-37.8,144.9,1,\ns1,One,-37.8,144.9,0,station\ns2,Two,-37.81,144.91,0,station\n",
					"pathways.txt": "pathway_id,from_stop_id,to_stop_id,pathway_mode,is_bidirectional\np,station,s1,1,1\n",
				})
			}, want: "pathway endpoints must not be stations",
		},
		{
			name: "duplicate calendar exception", innerName: "2/google_transit.zip",
			feed: func() map[string]string {
				return minimalFeed(map[string]string{"calendar_dates.txt": "service_id,date,exception_type\nsvc,20260716,1\nsvc,20260716,2\n"})
			}, want: "duplicate calendar date",
		},
		{
			name: "date-only removal", innerName: "2/google_transit.zip",
			feed: func() map[string]string {
				feed := minimalFeed(map[string]string{"calendar_dates.txt": "service_id,date,exception_type\nsvc,20260716,2\n"})
				delete(feed, "calendar.txt")
				return feed
			}, want: "must use exception_type 1",
		},
		{
			name: "duplicate transfer primary key", innerName: "2/google_transit.zip",
			feed: func() map[string]string {
				return minimalFeed(map[string]string{"transfers.txt": "from_stop_id,to_stop_id,transfer_type,min_transfer_time\ns1,s2,2,60\ns1,s2,2,120\n"})
			}, want: "duplicate transfers.txt primary key",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := Open(filepath.Join(t.TempDir(), "gtfs.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			archive := filepath.Join(t.TempDir(), "feed.zip")
			writeOuterGTFSZipAt(t, archive, test.innerName, test.feed())
			err = Ingest(t.Context(), store, archive, nil)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Ingest() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestIngestPreservesSignedPathwayStairCount(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "gtfs.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	archive := filepath.Join(t.TempDir(), "feed.zip")
	writeOuterGTFSZip(t, archive, minimalFeed(map[string]string{
		"pathways.txt": "pathway_id,from_stop_id,to_stop_id,pathway_mode,is_bidirectional,stair_count\ndown,s1,s2,2,0,-12\n",
	}))
	if err := Ingest(t.Context(), store, archive, nil); err != nil {
		t.Fatal(err)
	}
	var stairs int
	if err := store.db.QueryRowContext(t.Context(), `SELECT stair_count FROM pathways WHERE source_pathway_id='down'`).Scan(&stairs); err != nil {
		t.Fatal(err)
	}
	if stairs != -12 {
		t.Fatalf("stair_count = %d, want -12", stairs)
	}
}

func TestIngestNormalizesTransportVictoriaUnknownPathwayZeros(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "gtfs.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	archive := filepath.Join(t.TempDir(), "feed.zip")
	writeOuterGTFSZip(t, archive, minimalFeed(map[string]string{
		"pathways.txt": "pathway_id,from_stop_id,to_stop_id,pathway_mode,is_bidirectional,traversal_time,stair_count\nunknown,s1,s2,2,0,0,0\n",
	}))
	if err := Ingest(t.Context(), store, archive, nil); err != nil {
		t.Fatal(err)
	}
	var traversal, stairs any
	if err := store.db.QueryRowContext(t.Context(), `SELECT traversal_time,stair_count FROM pathways WHERE source_pathway_id='unknown'`).Scan(&traversal, &stairs); err != nil {
		t.Fatal(err)
	}
	if traversal != nil || stairs != nil {
		t.Fatalf("normalized pathway values = traversal:%v stairs:%v, want NULL/NULL", traversal, stairs)
	}
}

func TestIngestProximityGridCoversDatelineAndPolarNeighbors(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "gtfs.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	archive := filepath.Join(t.TempDir(), "feed.zip")
	writeOuterGTFSZip(t, archive, minimalFeed(map[string]string{
		"stops.txt": "stop_id,stop_name,stop_lat,stop_lon\n" +
			"d1,Dateline East,0,179.999\n" +
			"d2,Dateline West,0,-179.999\n" +
			"p1,Polar One,89.999,0\n" +
			"p2,Polar Two,89.999,90\n",
		"stop_times.txt": "trip_id,arrival_time,departure_time,stop_id,stop_sequence\n" +
			"t1,08:00:00,08:00:00,d1,1\n" +
			"t1,08:10:00,08:10:00,d2,2\n",
	}))
	if err := Ingest(t.Context(), store, archive, nil); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM transfers
		WHERE source='proximity' AND (
			(from_stop_id='2:d1' AND to_stop_id='2:d2') OR
			(from_stop_id='2:d2' AND to_stop_id='2:d1') OR
			(from_stop_id='2:p1' AND to_stop_id='2:p2') OR
			(from_stop_id='2:p2' AND to_stop_id='2:p1')
		)`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Fatalf("edge-case proximity transfer count = %d, want 4", count)
	}
}

func TestIngestGenerationPersistsResolvedStateAndHTTPPublicationTime(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "gtfs.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	archive := filepath.Join(t.TempDir(), "feed.zip")
	writeOuterGTFSZip(t, archive, minimalFeed(map[string]string{
		"calendar.txt":       "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nsvc,1,1,1,1,1,1,1,20260701,20260731\n",
		"calendar_dates.txt": "service_id,date,exception_type\nsvc,20280101,2\n",
		"stop_times.txt": "trip_id,arrival_time,departure_time,stop_id,stop_sequence,pickup_type,drop_off_type\n" +
			"t1,08:00:00,08:00:00,s1,1,2,1\n" +
			"t1,08:10:00,08:10:00,s2,2,1,3\n",
	}))
	state, err := IngestGeneration(t.Context(), store, archive, IngestGenerationOptions{
		GenerationID: "g-compiler",
		Provenance: FeedProvenance{
			SourceURL: "https://example.test/gtfs.zip", LastModified: "Thu, 16 Jul 2026 00:00:00 GMT", ActualBytes: 123,
		},
		IngestedAt: time.Date(2026, 7, 16, 1, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Provenance.PublicationTime.Format(time.RFC3339); got != "2026-07-16T00:00:00Z" {
		t.Fatalf("publication time = %s", got)
	}
	if state.Counts.Feeds != 1 || state.Counts.Connections != 1 || state.Counts.StopTimes != 2 {
		t.Fatalf("counts = %+v", state.Counts)
	}
	if state.Coverage != (ServiceCoverage{Start: "20260701", End: "20260731"}) {
		t.Fatalf("coverage = %+v", state.Coverage)
	}
	if err := store.ValidateResolvedDataset(t.Context()); err != nil {
		t.Fatalf("ValidateResolvedDataset() error = %v", err)
	}
	var resolved int
	if err := store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM trips WHERE feed_key IS NOT NULL AND route_key IS NOT NULL AND service_key IS NOT NULL AND source_trip_id!=''`).Scan(&resolved); err != nil {
		t.Fatal(err)
	}
	if resolved != 1 {
		t.Fatalf("resolved trips = %d, want 1", resolved)
	}
	spools, err := filepath.Glob(filepath.Join(dir, ".gtfs-inner-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(spools) != 0 {
		t.Fatalf("private spool directories remain: %v", spools)
	}
}

func TestIngestGenerationFailureDoesNotSaveDatasetState(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "gtfs.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	archive := filepath.Join(t.TempDir(), "bad.zip")
	writeOuterGTFSZip(t, archive, minimalFeed(map[string]string{
		"stop_times.txt": "trip_id,arrival_time,departure_time,stop_id,stop_sequence\nt1,bad,bad,s1,1\n",
	}))
	_, err = IngestGeneration(t.Context(), store, archive, IngestGenerationOptions{
		GenerationID: "g-fail", Provenance: FeedProvenance{SourceURL: "https://example.test/gtfs.zip"},
	})
	if err == nil {
		t.Fatal("IngestGeneration() error = nil")
	}
	if _, stateErr := store.DatasetState(t.Context()); !errors.Is(stateErr, ErrDatasetStateMissing) {
		t.Fatalf("DatasetState() error = %v, want ErrDatasetStateMissing", stateErr)
	}
}

func TestIngestGenerationPropagatesCancellationWithoutSavingState(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "gtfs.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	archive := filepath.Join(t.TempDir(), "feed.zip")
	writeOuterGTFSZip(t, archive, minimalFeed(nil))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = IngestGeneration(ctx, store, archive, IngestGenerationOptions{
		GenerationID: "g-cancelled",
		Provenance:   FeedProvenance{SourceURL: "https://example.test/gtfs.zip"},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("IngestGeneration() error = %v, want context.Canceled", err)
	}
	if _, stateErr := store.DatasetState(t.Context()); !errors.Is(stateErr, ErrDatasetStateMissing) {
		t.Fatalf("DatasetState() error = %v, want ErrDatasetStateMissing", stateErr)
	}
}

func minimalFeed(overrides map[string]string) map[string]string {
	feed := map[string]string{
		"stops.txt":          "stop_id,stop_name,stop_lat,stop_lon\ns1,One,-37.8,144.9\ns2,Two,-37.81,144.91\n",
		"routes.txt":         "route_id,route_short_name,route_long_name,route_type\nr1,R,Route,3\n",
		"trips.txt":          "route_id,service_id,trip_id,trip_headsign,direction_id\nr1,svc,t1,Towards,0\n",
		"stop_times.txt":     "trip_id,arrival_time,departure_time,stop_id,stop_sequence\nt1,08:00:00,08:00:00,s1,1\nt1,08:10:00,08:10:00,s2,2\n",
		"calendar.txt":       "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nsvc,1,1,1,1,1,1,1,20250101,20251231\n",
		"calendar_dates.txt": "service_id,date,exception_type\n",
		"transfers.txt":      "from_stop_id,to_stop_id,transfer_type,min_transfer_time\n",
	}
	for k, v := range overrides {
		feed[k] = v
	}
	return feed
}

func writeOuterGTFSZip(t testing.TB, path string, files map[string]string) {
	writeOuterGTFSZipAt(t, path, "2/google_transit.zip", files)
}

func writeOuterGTFSZipAt(t testing.TB, path, innerName string, files map[string]string) {
	t.Helper()
	var inner bytes.Buffer
	innerZip := zip.NewWriter(&inner)
	for name, body := range files {
		w, err := innerZip.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := innerZip.Close(); err != nil {
		t.Fatal(err)
	}

	var outer bytes.Buffer
	outerZip := zip.NewWriter(&outer)
	w, err := outerZip.Create(innerName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(inner.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := outerZip.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, outer.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}
