package gtfs

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func writeOuterGTFSZip(t *testing.T, path string, files map[string]string) {
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
	w, err := outerZip.Create("2/google_transit.zip")
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
