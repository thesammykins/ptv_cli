// Package gtfs downloads, ingests and queries the PTV GTFS static feed.
package gtfs

// schema defines the SQLite tables used for journey planning and name
// resolution. Only the columns needed by the planner and CLI are stored.
const schema = `
PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;

CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value TEXT
);

CREATE TABLE IF NOT EXISTS stops (
	stop_id    TEXT PRIMARY KEY,
	stop_name  TEXT,
	stop_lat   REAL,
	stop_lon   REAL,
	parent_station TEXT
);

CREATE TABLE IF NOT EXISTS routes (
	route_id    TEXT PRIMARY KEY,
	route_short_name TEXT,
	route_long_name  TEXT,
	route_type  INTEGER,
	feed_mode   INTEGER
);

CREATE TABLE IF NOT EXISTS trips (
	trip_id     TEXT PRIMARY KEY,
	route_id    TEXT,
	service_id  TEXT,
	trip_headsign TEXT,
	direction_id INTEGER,
	block_id    TEXT
);

CREATE TABLE IF NOT EXISTS stop_times (
	trip_id      TEXT,
	stop_id      TEXT,
	stop_sequence INTEGER,
	arrival_sec   INTEGER,
	departure_sec INTEGER
);

CREATE TABLE IF NOT EXISTS calendar (
	service_id TEXT PRIMARY KEY,
	monday INTEGER, tuesday INTEGER, wednesday INTEGER, thursday INTEGER,
	friday INTEGER, saturday INTEGER, sunday INTEGER,
	start_date TEXT, end_date TEXT
);

CREATE TABLE IF NOT EXISTS calendar_dates (
	service_id TEXT,
	date TEXT,
	exception_type INTEGER
);

CREATE TABLE IF NOT EXISTS transfers (
	from_stop_id TEXT,
	to_stop_id   TEXT,
	transfer_type INTEGER,
	min_transfer_time INTEGER
);
`

// migrations run after the main schema to upgrade existing databases.
const migrations = `
ALTER TABLE trips ADD COLUMN block_id TEXT;
`

// indexes are created after bulk ingest for speed.
const indexes = `
CREATE INDEX IF NOT EXISTS idx_stop_times_trip ON stop_times(trip_id, stop_sequence);
CREATE INDEX IF NOT EXISTS idx_stop_times_stop ON stop_times(stop_id);
CREATE INDEX IF NOT EXISTS idx_trips_route ON trips(route_id);
CREATE INDEX IF NOT EXISTS idx_trips_service ON trips(service_id);
CREATE INDEX IF NOT EXISTS idx_calendar_dates ON calendar_dates(service_id, date);
CREATE INDEX IF NOT EXISTS idx_stops_name ON stops(stop_name);
CREATE INDEX IF NOT EXISTS idx_transfers_from ON transfers(from_stop_id);
`
