package gtfs

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const legacyTestSchema = `
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT);
CREATE TABLE stops (stop_id TEXT PRIMARY KEY, stop_name TEXT, stop_lat REAL, stop_lon REAL, parent_station TEXT);
CREATE TABLE routes (route_id TEXT PRIMARY KEY, route_short_name TEXT, route_long_name TEXT, route_type INTEGER, feed_mode INTEGER);
CREATE TABLE trips (trip_id TEXT PRIMARY KEY, route_id TEXT, service_id TEXT, trip_headsign TEXT, direction_id INTEGER);
CREATE TABLE stop_times (trip_id TEXT, stop_id TEXT, stop_sequence INTEGER, arrival_sec INTEGER, departure_sec INTEGER);
CREATE TABLE calendar (service_id TEXT PRIMARY KEY, monday INTEGER, tuesday INTEGER, wednesday INTEGER, thursday INTEGER, friday INTEGER, saturday INTEGER, sunday INTEGER, start_date TEXT, end_date TEXT);
CREATE TABLE calendar_dates (service_id TEXT, date TEXT, exception_type INTEGER);
CREATE TABLE transfers (from_stop_id TEXT, to_stop_id TEXT, transfer_type INTEGER, min_transfer_time INTEGER);
`

func TestOpenReadOnlyDoesNotCreateMissingDatabase(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "missing.sqlite")
	_, err := OpenReadOnly(t.Context(), path)
	if err == nil {
		t.Fatal("OpenReadOnly() error = nil, want missing-file error")
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("OpenReadOnly() created path or returned unexpected stat error: %v", statErr)
	}
}

func TestCreateStagingUsesCheckedSchemaV2AndURISafePath(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "feed ?# one.sqlite")
	store, err := CreateStaging(t.Context(), path)
	if err != nil {
		t.Fatalf("CreateStaging() error = %v", err)
	}
	version, err := store.SchemaVersion(t.Context())
	if err != nil {
		t.Fatalf("SchemaVersion() error = %v", err)
	}
	if version != CurrentSchemaVersion {
		t.Fatalf("SchemaVersion() = %d, want %d", version, CurrentSchemaVersion)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	read, err := OpenReadOnly(t.Context(), path)
	if err != nil {
		t.Fatalf("OpenReadOnly() error = %v", err)
	}
	defer read.Close()
	if !read.ReadOnly() {
		t.Fatal("ReadOnly() = false, want true")
	}
	if _, err := read.db.ExecContext(t.Context(), `INSERT INTO meta(key,value) VALUES('x','y')`); err == nil {
		t.Fatal("write through OpenReadOnly() succeeded")
	}
}

func TestCreateStagingRefusesExistingFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "existing.sqlite")
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateStaging(t.Context(), path); err == nil {
		t.Fatal("CreateStaging() error = nil, want existing-file error")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep" {
		t.Fatalf("existing file changed to %q", data)
	}
}

func TestConnectionSchemaStoresTemplatesWithoutServiceDateExpansion(t *testing.T) {
	t.Parallel()
	store, err := CreateStaging(t.Context(), filepath.Join(t.TempDir(), "templates.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, table := range []string{"connections", "trip_links"} {
		exists, err := columnExists(t.Context(), store.db, table, "service_date")
		if err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Fatalf("%s.service_date exists; service instances must remain query-local", table)
		}
	}
}

func TestInspectAndCompatibilityOpenRejectLegacyDatabase(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	createLegacyDatabase(t, path, 0)

	info, err := InspectDatabase(t.Context(), path)
	if err != nil {
		t.Fatalf("InspectDatabase() error = %v", err)
	}
	if info.Kind != DatabaseLegacy || info.SchemaVersion != 0 {
		t.Fatalf("InspectDatabase() = %+v, want legacy version 0", info)
	}
	if _, err := OpenReadOnly(t.Context(), path); !errors.Is(err, ErrLegacyDatabase) {
		t.Fatalf("OpenReadOnly() error = %v, want ErrLegacyDatabase", err)
	}

	if _, err := Open(path); !errors.Is(err, ErrLegacyDatabase) {
		t.Fatalf("Open() error = %v, want ErrLegacyDatabase", err)
	}
	info, err = InspectDatabase(t.Context(), path)
	if err != nil {
		t.Fatalf("InspectDatabase() after rejected open error = %v", err)
	}
	if info.Kind != DatabaseLegacy || info.SchemaVersion != 0 {
		t.Fatalf("InspectDatabase() after rejected open = %+v, want unchanged legacy database", info)
	}
	db := openLegacyForAssertion(t, path)
	defer db.Close()
	var name string
	if err := db.QueryRowContext(t.Context(), `SELECT stop_name FROM stops WHERE stop_id='legacy-stop'`).Scan(&name); err != nil {
		t.Fatalf("legacy row changed after rejected open: %v", err)
	}
	if name != "Legacy Stop" {
		t.Fatalf("legacy stop name = %q", name)
	}
}

func TestCompatibilityOpenRejectsManagedVersionOne(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "v1.sqlite")
	createLegacyDatabase(t, path, 1)
	if _, err := OpenContext(t.Context(), path); !errors.Is(err, ErrSchemaUpgradeRequired) {
		t.Fatalf("OpenContext() error = %v, want ErrSchemaUpgradeRequired", err)
	}
	info, err := InspectDatabase(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Kind != DatabaseUpgradeRequired || info.SchemaVersion != 1 {
		t.Fatalf("InspectDatabase() = %+v, want unchanged version 1 database", info)
	}
}

func TestDatasetStateRoundTripUsesPersistedCounts(t *testing.T) {
	t.Parallel()
	store, state := newPopulatedStagingStore(t, filepath.Join(t.TempDir(), "state.sqlite"), "g-state")
	defer store.Close()

	if err := store.SaveDatasetState(t.Context(), state); err != nil {
		t.Fatalf("SaveDatasetState() error = %v", err)
	}
	got, err := store.DatasetState(t.Context())
	if err != nil {
		t.Fatalf("DatasetState() error = %v", err)
	}
	if got.GenerationID != state.GenerationID || got.Counts != state.Counts || got.Coverage != state.Coverage {
		t.Fatalf("DatasetState() = %+v, want %+v", got, state)
	}
	counts, err := store.PersistedCounts(t.Context())
	if err != nil {
		t.Fatalf("PersistedCounts() error = %v", err)
	}
	if counts != state.Counts {
		t.Fatalf("PersistedCounts() = %+v, want %+v", counts, state.Counts)
	}
	if marker, err := store.MetaContext(t.Context(), "ingested_at"); err != nil || marker == "" {
		t.Fatalf("compatibility ingested_at = %q, %v", marker, err)
	}
}

func TestOpenReadOnlyRejectsNewerSchema(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "newer.sqlite")
	store, err := CreateStaging(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), `PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReadOnly(t.Context(), path); !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("OpenReadOnly() error = %v, want ErrSchemaTooNew", err)
	}
}

func createLegacyDatabase(t *testing.T, path string, version int) {
	t.Helper()
	db := openLegacyForAssertion(t, path)
	defer db.Close()
	if _, err := db.ExecContext(t.Context(), legacyTestSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO stops(stop_id,stop_name,stop_lat,stop_lon,parent_station) VALUES('legacy-stop','Legacy Stop',-37.8,144.9,'')`); err != nil {
		t.Fatal(err)
	}
	if version > 0 {
		if _, err := db.ExecContext(t.Context(), `ALTER TABLE trips ADD COLUMN block_id TEXT`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(t.Context(), `PRAGMA user_version = 1`); err != nil {
			t.Fatal(err)
		}
	}
}

func openLegacyForAssertion(t *testing.T, path string) *sql.DB {
	t.Helper()
	dsn, err := sqliteDSN(path, "rwc", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.PingContext(t.Context()); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db
}

func newPopulatedStagingStore(t *testing.T, path, generationID string) (*Store, DatasetState) {
	t.Helper()
	store, err := CreateStaging(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	populateResolvedFixture(t, store)
	counts, err := store.ComputeDatasetCounts(t.Context())
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, DatasetState{
		GenerationID: generationID,
		Provenance: FeedProvenance{
			SourceURL:     "https://example.test/gtfs.zip",
			DeclaredBytes: 100,
			ActualBytes:   100,
		},
		IngestedAt: time.Date(2026, 7, 16, 1, 2, 3, 0, time.UTC),
		Coverage: ServiceCoverage{
			Start: "20260716",
			End:   "20260815",
		},
		Counts: counts,
	}
}

func populateResolvedFixture(t *testing.T, store *Store) {
	t.Helper()
	statements := []string{
		`INSERT INTO feeds(feed_key,feed_mode,source_namespace,source_name)
		 VALUES(1,1,'ptv-static-vline-train','V/Line Train')`,
		`INSERT INTO stops(stop_key,feed_key,feed_mode,source_stop_id,stop_id,stop_name,stop_lat,stop_lon)
		 VALUES(1,1,1,'a','1:a','A',-37.8,144.9),(2,1,1,'b','1:b','B',-37.81,144.91)`,
		`INSERT INTO routes(route_key,feed_key,feed_mode,source_route_id,route_id,route_short_name,route_long_name,route_type)
		 VALUES(1,1,1,'r','1:r','R','Route',2)`,
		`INSERT INTO calendar(service_key,feed_key,feed_mode,source_service_id,service_id,
		 monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date)
		 VALUES(1,1,1,'s','1:s',1,1,1,1,1,1,1,'20260716','20260815')`,
		`INSERT INTO trips(trip_key,feed_key,feed_mode,source_trip_id,trip_id,route_key,route_id,
		 service_key,service_id,trip_headsign,direction_id,source_block_id,block_id)
		 VALUES(1,1,1,'t','1:t',1,'1:r',1,'1:s','B',0,'block','1:block')`,
		`INSERT INTO stop_times(stop_time_key,trip_key,trip_id,stop_key,stop_id,stop_sequence,
		 arrival_sec,departure_sec,pickup_type,drop_off_type)
		 VALUES(1,1,'1:t',1,'1:a',1,0,0,0,0),(2,1,'1:t',2,'1:b',2,60,60,0,0)`,
		`INSERT INTO connections(connection_key,feed_key,feed_mode,service_key,
		 trip_key,route_key,dep_stop_key,arr_stop_key,dep_sequence,arr_sequence,
		 departure_sec,arrival_sec,pickup_type,drop_off_type,block_id)
		 VALUES(1,1,1,1,1,1,1,2,1,2,0,60,0,0,'1:block')`,
	}
	for _, statement := range statements {
		if _, err := store.db.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
}
