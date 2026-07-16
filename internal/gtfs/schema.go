// Package gtfs downloads, ingests and queries the PTV GTFS static feed.
package gtfs

// CurrentSchemaVersion is the newest generation schema understood by this
// binary. Generation databases are immutable after publication.
const CurrentSchemaVersion = 2

// schema defines a complete version-2 generation database. The namespaced text
// IDs remain available for labels and compatibility queries, while every hot
// planning relationship uses a generation-local integer key. feed_key and the
// source_* columns preserve the originating feed namespace explicitly.
const schema = `
CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value TEXT
);

CREATE TABLE IF NOT EXISTS feeds (
	feed_key         INTEGER PRIMARY KEY,
	feed_mode        INTEGER NOT NULL,
	source_namespace TEXT NOT NULL,
	source_name      TEXT,
	UNIQUE(feed_mode, source_namespace)
);

CREATE TABLE IF NOT EXISTS stops (
	stop_key            INTEGER PRIMARY KEY,
	feed_key            INTEGER,
	feed_mode           INTEGER NOT NULL DEFAULT 0,
	source_stop_id      TEXT,
	stop_id             TEXT NOT NULL UNIQUE,
	stop_name           TEXT,
	stop_lat            REAL,
	stop_lon            REAL,
	parent_station      TEXT,
	parent_stop_key     INTEGER,
	location_type       INTEGER NOT NULL DEFAULT 0,
	level_id            TEXT,
	level_key           INTEGER,
	stop_access         INTEGER CHECK (stop_access IN (0, 1)),
	wheelchair_boarding INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS routes (
	route_key        INTEGER PRIMARY KEY,
	feed_key         INTEGER,
	feed_mode        INTEGER NOT NULL DEFAULT 0,
	source_route_id  TEXT,
	route_id         TEXT NOT NULL UNIQUE,
	route_short_name TEXT,
	route_long_name  TEXT,
	route_type       INTEGER
);

CREATE TABLE IF NOT EXISTS calendar (
	service_key       INTEGER PRIMARY KEY,
	feed_key          INTEGER,
	feed_mode         INTEGER NOT NULL DEFAULT 0,
	source_service_id TEXT,
	service_id        TEXT NOT NULL UNIQUE,
	monday INTEGER, tuesday INTEGER, wednesday INTEGER, thursday INTEGER,
	friday INTEGER, saturday INTEGER, sunday INTEGER,
	start_date TEXT, end_date TEXT
);

CREATE TABLE IF NOT EXISTS trips (
	trip_key       INTEGER PRIMARY KEY,
	feed_key       INTEGER,
	feed_mode      INTEGER NOT NULL DEFAULT 0,
	source_trip_id TEXT,
	trip_id        TEXT NOT NULL UNIQUE,
	route_key      INTEGER,
	route_id       TEXT,
	service_key    INTEGER,
	service_id     TEXT,
	trip_headsign  TEXT,
	direction_id   INTEGER,
	source_block_id TEXT,
	block_id       TEXT
);

CREATE TABLE IF NOT EXISTS stop_times (
	stop_time_key INTEGER PRIMARY KEY,
	trip_key      INTEGER,
	trip_id       TEXT,
	stop_key      INTEGER,
	stop_id       TEXT,
	stop_sequence INTEGER,
	arrival_sec   INTEGER,
	departure_sec INTEGER,
	pickup_type   INTEGER NOT NULL DEFAULT 0,
	drop_off_type INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS calendar_dates (
	service_key   INTEGER,
	service_id    TEXT,
	date          TEXT,
	exception_type INTEGER
);

-- Raw GTFS transfer rules retain both source IDs and resolved integer keys.
-- Stops are nullable because linked-trip transfer types may omit them.
CREATE TABLE IF NOT EXISTS transfers (
	transfer_key      INTEGER PRIMARY KEY,
	feed_key          INTEGER,
	from_stop_key     INTEGER,
	to_stop_key       INTEGER,
	from_stop_id      TEXT,
	to_stop_id        TEXT,
	transfer_type     INTEGER,
	min_transfer_time INTEGER,
	from_route_key    INTEGER,
	to_route_key      INTEGER,
	from_route_id     TEXT,
	to_route_id       TEXT,
	from_trip_key     INTEGER,
	to_trip_key       INTEGER,
	from_trip_id      TEXT,
	to_trip_id        TEXT,
	source             TEXT NOT NULL DEFAULT 'gtfs'
		CHECK (source IN ('gtfs', 'proximity'))
);

CREATE TABLE IF NOT EXISTS levels (
	level_key   INTEGER PRIMARY KEY,
	feed_key    INTEGER,
	feed_mode   INTEGER NOT NULL DEFAULT 0,
	source_level_id TEXT,
	level_id    TEXT NOT NULL UNIQUE,
	level_index REAL NOT NULL,
	level_name  TEXT
);

CREATE TABLE IF NOT EXISTS pathways (
	pathway_key            INTEGER PRIMARY KEY,
	feed_key               INTEGER,
	source_pathway_id      TEXT,
	pathway_id             TEXT NOT NULL UNIQUE,
	from_stop_key          INTEGER,
	to_stop_key            INTEGER,
	from_stop_id           TEXT NOT NULL,
	to_stop_id             TEXT NOT NULL,
	pathway_mode           INTEGER NOT NULL,
	is_bidirectional       INTEGER NOT NULL DEFAULT 0,
	length                 REAL,
	traversal_time         INTEGER,
	stair_count            INTEGER,
	max_slope              REAL,
	min_width              REAL,
	signposted_as          TEXT,
	reversed_signposted_as TEXT
);

-- Elementary connection templates are materialized once per trip/service.
-- The router selects active service keys for its bounded date window and gives
-- each (feed_key, service_date, trip_key) a query-local instance identity; the
-- rolling calendar must not multiply this table by every active service date.
CREATE TABLE IF NOT EXISTS connections (
	connection_key  INTEGER PRIMARY KEY,
	feed_key        INTEGER NOT NULL,
	feed_mode       INTEGER NOT NULL,
	service_key     INTEGER NOT NULL,
	trip_key        INTEGER NOT NULL,
	route_key       INTEGER NOT NULL,
	dep_stop_key    INTEGER NOT NULL,
	arr_stop_key    INTEGER NOT NULL,
	dep_sequence    INTEGER NOT NULL,
	arr_sequence    INTEGER NOT NULL,
	departure_sec   INTEGER NOT NULL,
	arrival_sec     INTEGER NOT NULL,
	pickup_type     INTEGER NOT NULL DEFAULT 0,
	drop_off_type   INTEGER NOT NULL DEFAULT 0,
	block_id        TEXT,
	CHECK(arrival_sec >= departure_sec)
);

-- transfer_type 4/5 rules are resolved to source trip-link templates here.
-- The router applies them only after assigning query-local active service
-- instances, so a raw trip link never implies cross-date reachability.
CREATE TABLE IF NOT EXISTS trip_links (
	trip_link_key INTEGER PRIMARY KEY,
	feed_key      INTEGER NOT NULL,
	from_trip_key INTEGER NOT NULL,
	to_trip_key   INTEGER NOT NULL,
	from_stop_key INTEGER,
	to_stop_key   INTEGER,
	transfer_type INTEGER NOT NULL CHECK (transfer_type IN (4, 5)),
	min_transfer_time INTEGER,
	UNIQUE(from_trip_key, to_trip_key, from_stop_key, to_stop_key)
);

-- A single checked row replaces expensive status-time COUNT(*) queries and
-- keeps provenance and service coverage in the generation it describes.
CREATE TABLE IF NOT EXISTS dataset_state (
	id                    INTEGER PRIMARY KEY CHECK (id = 1),
	generation_id         TEXT NOT NULL,
	source_url            TEXT NOT NULL,
	etag                  TEXT,
	last_modified         TEXT,
	declared_bytes        INTEGER,
	actual_bytes          INTEGER NOT NULL,
	publication_time_utc  TEXT,
	ingested_at_utc       TEXT NOT NULL,
	coverage_start        TEXT NOT NULL,
	coverage_end          TEXT NOT NULL,
	feed_count            INTEGER NOT NULL,
	stop_count            INTEGER NOT NULL,
	route_count           INTEGER NOT NULL,
	service_count         INTEGER NOT NULL,
	trip_count            INTEGER NOT NULL,
	stop_time_count       INTEGER NOT NULL,
	transfer_count        INTEGER NOT NULL,
	pathway_count         INTEGER NOT NULL,
	connection_count      INTEGER NOT NULL,
	trip_link_count       INTEGER NOT NULL
);
`

// indexes are created after bulk ingest. Calendar exceptions are led by date;
// connection templates support scans over a bounded set of active service keys.
const indexes = `
CREATE INDEX IF NOT EXISTS idx_stop_times_trip ON stop_times(trip_key, stop_sequence);
CREATE INDEX IF NOT EXISTS idx_stop_times_stop ON stop_times(stop_key);
CREATE INDEX IF NOT EXISTS idx_trips_route ON trips(route_key);
CREATE INDEX IF NOT EXISTS idx_trips_service ON trips(service_key);
DROP INDEX IF EXISTS idx_calendar_dates;
CREATE INDEX idx_calendar_dates ON calendar_dates(date, service_key);
CREATE INDEX IF NOT EXISTS idx_stops_name ON stops(stop_name);
CREATE INDEX IF NOT EXISTS idx_transfers_from ON transfers(from_stop_key);
CREATE INDEX IF NOT EXISTS idx_transfers_match ON transfers(from_stop_key, to_stop_key, from_trip_key, to_trip_key, from_route_key, to_route_key);
CREATE INDEX IF NOT EXISTS idx_pathways_from ON pathways(from_stop_key);
CREATE INDEX IF NOT EXISTS idx_connections_forward ON connections(service_key, departure_sec, connection_key);
CREATE INDEX IF NOT EXISTS idx_connections_reverse ON connections(service_key, arrival_sec DESC, connection_key DESC);
CREATE INDEX IF NOT EXISTS idx_connections_trip ON connections(trip_key, dep_sequence);
CREATE INDEX IF NOT EXISTS idx_trip_links_from ON trip_links(from_trip_key, from_stop_key);
`
