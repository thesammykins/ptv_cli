package gtfs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// ErrDatasetStateMissing means a database has not recorded its checked counts,
// coverage, and provenance yet.
var ErrDatasetStateMissing = errors.New("GTFS dataset state is missing")

// ErrUnresolvedDataset means staging rows have not been resolved from source
// text IDs into the explicit feed and integer-key contract required by v2.
var ErrUnresolvedDataset = errors.New("GTFS dataset contains unresolved identities")

// DatasetCounts contains persisted row counts for status and publication
// validation. int64 avoids narrowing the production-sized stop_times count.
type DatasetCounts struct {
	Feeds       int64 `json:"feeds"`
	Stops       int64 `json:"stops"`
	Routes      int64 `json:"routes"`
	Services    int64 `json:"services"`
	Trips       int64 `json:"trips"`
	StopTimes   int64 `json:"stop_times"`
	Transfers   int64 `json:"transfers"`
	Pathways    int64 `json:"pathways"`
	Connections int64 `json:"connections"`
	TripLinks   int64 `json:"trip_links"`
}

// ServiceCoverage is the inclusive GTFS service-date range in YYYYMMDD form.
type ServiceCoverage struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// FeedProvenance identifies the exact upstream object used for a generation.
type FeedProvenance struct {
	SourceURL       string    `json:"source_url"`
	ETag            string    `json:"etag,omitempty"`
	LastModified    string    `json:"last_modified,omitempty"`
	DeclaredBytes   int64     `json:"declared_bytes,omitempty"`
	ActualBytes     int64     `json:"actual_bytes"`
	PublicationTime time.Time `json:"publication_time,omitempty,omitzero"`
}

// DatasetState is written to a staging database before it can be published.
type DatasetState struct {
	GenerationID string          `json:"generation_id"`
	Provenance   FeedProvenance  `json:"provenance"`
	IngestedAt   time.Time       `json:"ingested_at"`
	Coverage     ServiceCoverage `json:"coverage"`
	Counts       DatasetCounts   `json:"counts"`
}

// SaveDatasetState atomically persists counts, coverage, provenance, and
// compatibility metadata in the generation they describe.
func (s *Store) SaveDatasetState(ctx context.Context, state DatasetState) error {
	if s.readOnly {
		return errors.New("cannot write dataset state to a read-only generation")
	}
	if err := validateDatasetState(state, false); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning dataset state write: %w", err)
	}
	defer tx.Rollback()

	publication := ""
	if !state.Provenance.PublicationTime.IsZero() {
		publication = state.Provenance.PublicationTime.UTC().Format(time.RFC3339Nano)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO dataset_state(
			id, generation_id, source_url, etag, last_modified,
			declared_bytes, actual_bytes, publication_time_utc,
			ingested_at_utc, coverage_start, coverage_end,
			feed_count, stop_count, route_count, service_count,
			trip_count, stop_time_count, transfer_count, pathway_count,
			connection_count, trip_link_count
		) VALUES(1,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			generation_id=excluded.generation_id,
			source_url=excluded.source_url,
			etag=excluded.etag,
			last_modified=excluded.last_modified,
			declared_bytes=excluded.declared_bytes,
			actual_bytes=excluded.actual_bytes,
			publication_time_utc=excluded.publication_time_utc,
			ingested_at_utc=excluded.ingested_at_utc,
			coverage_start=excluded.coverage_start,
			coverage_end=excluded.coverage_end,
			feed_count=excluded.feed_count,
			stop_count=excluded.stop_count,
			route_count=excluded.route_count,
			service_count=excluded.service_count,
			trip_count=excluded.trip_count,
			stop_time_count=excluded.stop_time_count,
			transfer_count=excluded.transfer_count,
			pathway_count=excluded.pathway_count,
			connection_count=excluded.connection_count,
			trip_link_count=excluded.trip_link_count`,
		state.GenerationID,
		state.Provenance.SourceURL,
		state.Provenance.ETag,
		state.Provenance.LastModified,
		state.Provenance.DeclaredBytes,
		state.Provenance.ActualBytes,
		publication,
		state.IngestedAt.UTC().Format(time.RFC3339Nano),
		state.Coverage.Start,
		state.Coverage.End,
		state.Counts.Feeds,
		state.Counts.Stops,
		state.Counts.Routes,
		state.Counts.Services,
		state.Counts.Trips,
		state.Counts.StopTimes,
		state.Counts.Transfers,
		state.Counts.Pathways,
		state.Counts.Connections,
		state.Counts.TripLinks,
	)
	if err != nil {
		return fmt.Errorf("writing dataset state: %w", err)
	}

	compatibilityMeta := map[string]string{
		"generation_id":       state.GenerationID,
		"source_url":          state.Provenance.SourceURL,
		"feed_etag":           state.Provenance.ETag,
		"feed_last_modified":  state.Provenance.LastModified,
		"feed_content_length": strconv.FormatInt(state.Provenance.ActualBytes, 10),
		"ingested_at":         state.IngestedAt.UTC().Format(time.RFC3339),
		"coverage_start":      state.Coverage.Start,
		"coverage_end":        state.Coverage.End,
	}
	for key, value := range compatibilityMeta {
		if err := s.setMetaContext(ctx, tx, key, value); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing dataset state: %w", err)
	}
	return nil
}

// DatasetState returns the checked state stored in this generation.
func (s *Store) DatasetState(ctx context.Context) (DatasetState, error) {
	var (
		state                            DatasetState
		publicationRaw, ingestedRaw      string
		declaredBytes, actualBytes       int64
		feeds, stops, routes, services   int64
		trips, stopTimes, transfers      int64
		pathways, connections, tripLinks int64
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT generation_id, source_url, etag, last_modified,
		       declared_bytes, actual_bytes, publication_time_utc,
		       ingested_at_utc, coverage_start, coverage_end,
		       feed_count, stop_count, route_count, service_count,
		       trip_count, stop_time_count, transfer_count, pathway_count,
		       connection_count, trip_link_count
		FROM dataset_state WHERE id = 1`).Scan(
		&state.GenerationID,
		&state.Provenance.SourceURL,
		&state.Provenance.ETag,
		&state.Provenance.LastModified,
		&declaredBytes,
		&actualBytes,
		&publicationRaw,
		&ingestedRaw,
		&state.Coverage.Start,
		&state.Coverage.End,
		&feeds,
		&stops,
		&routes,
		&services,
		&trips,
		&stopTimes,
		&transfers,
		&pathways,
		&connections,
		&tripLinks,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return DatasetState{}, ErrDatasetStateMissing
	}
	if err != nil {
		return DatasetState{}, fmt.Errorf("reading dataset state: %w", err)
	}
	state.Provenance.DeclaredBytes = declaredBytes
	state.Provenance.ActualBytes = actualBytes
	state.Counts = DatasetCounts{
		Feeds:       feeds,
		Stops:       stops,
		Routes:      routes,
		Services:    services,
		Trips:       trips,
		StopTimes:   stopTimes,
		Transfers:   transfers,
		Pathways:    pathways,
		Connections: connections,
		TripLinks:   tripLinks,
	}
	if publicationRaw != "" {
		state.Provenance.PublicationTime, err = time.Parse(time.RFC3339Nano, publicationRaw)
		if err != nil {
			return DatasetState{}, fmt.Errorf("parsing dataset publication time: %w", err)
		}
	}
	state.IngestedAt, err = time.Parse(time.RFC3339Nano, ingestedRaw)
	if err != nil {
		return DatasetState{}, fmt.Errorf("parsing dataset ingest time: %w", err)
	}
	if err := validateDatasetState(state, false); err != nil {
		return DatasetState{}, fmt.Errorf("reading dataset state: %w", err)
	}
	return state, nil
}

// PersistedCounts returns counts without scanning large feed tables.
func (s *Store) PersistedCounts(ctx context.Context) (DatasetCounts, error) {
	state, err := s.DatasetState(ctx)
	if err != nil {
		return DatasetCounts{}, err
	}
	return state.Counts, nil
}

// ComputeDatasetCounts performs the expensive validation scan intended for
// staging publication, not normal status output.
func (s *Store) ComputeDatasetCounts(ctx context.Context) (DatasetCounts, error) {
	var result DatasetCounts
	targets := []struct {
		table string
		out   *int64
	}{
		{"feeds", &result.Feeds},
		{"stops", &result.Stops},
		{"routes", &result.Routes},
		{"calendar", &result.Services},
		{"trips", &result.Trips},
		{"stop_times", &result.StopTimes},
		{"transfers", &result.Transfers},
		{"pathways", &result.Pathways},
		{"connections", &result.Connections},
		{"trip_links", &result.TripLinks},
	}
	for _, target := range targets {
		// target.table comes from the fixed list above.
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+target.table).Scan(target.out); err != nil {
			return DatasetCounts{}, fmt.Errorf("counting %s: %w", target.table, err)
		}
	}
	return result, nil
}

// ValidateResolvedDataset is the frozen handoff contract for the ingest lane.
// A schema-v2 file is publishable only after every planning row has an explicit
// feed namespace, source identifier, and valid integer relationship. This
// prevents compatibility inserts (which deliberately leave key columns NULL)
// from being mistaken for a correctness-complete managed generation.
func (s *Store) ValidateResolvedDataset(ctx context.Context) error {
	checks := []struct {
		name  string
		query string
	}{
		{
			"feeds",
			`SELECT COUNT(*) FROM feeds WHERE feed_mode <= 0 OR trim(source_namespace) = ''`,
		},
		{
			"stops",
			`SELECT COUNT(*) FROM stops s
			 LEFT JOIN feeds f ON f.feed_key = s.feed_key
			 LEFT JOIN stops p ON p.stop_key = s.parent_stop_key
			 LEFT JOIN levels l ON l.level_key = s.level_key
			 WHERE f.feed_key IS NULL OR s.feed_mode != f.feed_mode OR trim(coalesce(s.source_stop_id,'')) = ''
			    OR s.location_type NOT BETWEEN 0 AND 4
			    OR (s.parent_stop_key IS NOT NULL AND (p.stop_key IS NULL OR p.feed_key != s.feed_key))
			    OR (s.parent_stop_key IS NOT NULL AND s.location_type IN (0,2,3) AND p.location_type != 1)
			    OR (s.parent_stop_key IS NOT NULL AND s.location_type = 4 AND p.location_type != 0)
			    OR (s.location_type = 1 AND s.parent_stop_key IS NOT NULL)
			    OR (s.location_type IN (2,3,4) AND s.parent_stop_key IS NULL)
			    OR (s.level_key IS NOT NULL AND (l.level_key IS NULL OR l.feed_key != s.feed_key))
			    OR s.stop_access NOT IN (0,1)
			    OR (s.stop_access IS NOT NULL AND (s.location_type != 0 OR s.parent_stop_key IS NULL))`,
		},
		{
			"routes",
			`SELECT COUNT(*) FROM routes r
			 LEFT JOIN feeds f ON f.feed_key = r.feed_key
			 WHERE f.feed_key IS NULL OR r.feed_mode != f.feed_mode OR trim(coalesce(r.source_route_id,'')) = ''`,
		},
		{
			"services",
			`SELECT COUNT(*) FROM calendar c
			 LEFT JOIN feeds f ON f.feed_key = c.feed_key
			 WHERE f.feed_key IS NULL OR c.feed_mode != f.feed_mode OR trim(coalesce(c.source_service_id,'')) = ''`,
		},
		{
			"trips",
			`SELECT COUNT(*) FROM trips t
			 LEFT JOIN feeds f ON f.feed_key = t.feed_key
			 LEFT JOIN routes r ON r.route_key = t.route_key
			 LEFT JOIN calendar c ON c.service_key = t.service_key
			 WHERE f.feed_key IS NULL OR r.route_key IS NULL OR c.service_key IS NULL
			    OR t.feed_mode != f.feed_mode OR r.feed_key != t.feed_key OR c.feed_key != t.feed_key
			    OR trim(coalesce(t.source_trip_id,'')) = ''`,
		},
		{
			"stop_times",
			`SELECT COUNT(*) FROM stop_times st
			 LEFT JOIN trips t ON t.trip_key = st.trip_key
			 LEFT JOIN stops s ON s.stop_key = st.stop_key
			 WHERE t.trip_key IS NULL OR s.stop_key IS NULL OR t.feed_key != s.feed_key`,
		},
		{
			"calendar exceptions",
			`SELECT COUNT(*) FROM calendar_dates cd
			 LEFT JOIN calendar c ON c.service_key = cd.service_key
			 WHERE c.service_key IS NULL`,
		},
		{
			"GTFS transfer rules",
			`SELECT COUNT(*) FROM transfers tr
			 LEFT JOIN feeds f ON f.feed_key = tr.feed_key
			 LEFT JOIN stops a ON a.stop_key = tr.from_stop_key
			 LEFT JOIN stops b ON b.stop_key = tr.to_stop_key
			 LEFT JOIN routes fr ON fr.route_key = tr.from_route_key
			 LEFT JOIN routes tor ON tor.route_key = tr.to_route_key
			 LEFT JOIN trips ft ON ft.trip_key = tr.from_trip_key
			 LEFT JOIN trips tt ON tt.trip_key = tr.to_trip_key
			 WHERE tr.source = 'gtfs' AND (
			       f.feed_key IS NULL OR tr.transfer_type NOT BETWEEN 0 AND 5
			    OR tr.min_transfer_time < 0
			    OR (tr.transfer_type = 2 AND tr.min_transfer_time IS NULL)
			    OR (tr.transfer_type BETWEEN 0 AND 3 AND (tr.from_stop_key IS NULL OR tr.to_stop_key IS NULL))
			    OR (tr.transfer_type BETWEEN 4 AND 5 AND (tr.from_trip_key IS NULL OR tr.to_trip_key IS NULL))
			    OR (tr.from_stop_key IS NOT NULL AND (a.stop_key IS NULL OR a.feed_key != tr.feed_key OR trim(coalesce(tr.from_stop_id,'')) = ''))
			    OR (tr.to_stop_key IS NOT NULL AND (b.stop_key IS NULL OR b.feed_key != tr.feed_key OR trim(coalesce(tr.to_stop_id,'')) = ''))
			    OR (tr.from_route_key IS NOT NULL AND (fr.route_key IS NULL OR fr.feed_key != tr.feed_key OR trim(coalesce(tr.from_route_id,'')) = ''))
			    OR (tr.to_route_key IS NOT NULL AND (tor.route_key IS NULL OR tor.feed_key != tr.feed_key OR trim(coalesce(tr.to_route_id,'')) = ''))
			    OR (tr.from_trip_key IS NOT NULL AND (ft.trip_key IS NULL OR ft.feed_key != tr.feed_key OR trim(coalesce(tr.from_trip_id,'')) = ''))
			    OR (tr.to_trip_key IS NOT NULL AND (tt.trip_key IS NULL OR tt.feed_key != tr.feed_key OR trim(coalesce(tr.to_trip_id,'')) = ''))
			    OR (tr.from_trip_key IS NOT NULL AND tr.from_route_key IS NOT NULL AND ft.route_key != tr.from_route_key)
			    OR (tr.to_trip_key IS NOT NULL AND tr.to_route_key IS NOT NULL AND tt.route_key != tr.to_route_key)
			 )`,
		},
		{
			"proximity transfers",
			`SELECT COUNT(*) FROM transfers tr
			 LEFT JOIN stops a ON a.stop_key = tr.from_stop_key
			 LEFT JOIN stops b ON b.stop_key = tr.to_stop_key
			 WHERE tr.source = 'proximity' AND (
			       tr.feed_key IS NOT NULL OR tr.transfer_type != 2
			    OR tr.min_transfer_time IS NULL OR tr.min_transfer_time < 0
			    OR a.stop_key IS NULL OR b.stop_key IS NULL
			    OR trim(coalesce(tr.from_stop_id,'')) = '' OR trim(coalesce(tr.to_stop_id,'')) = ''
			    OR tr.from_route_key IS NOT NULL OR tr.to_route_key IS NOT NULL
			    OR tr.from_trip_key IS NOT NULL OR tr.to_trip_key IS NOT NULL
			 )`,
		},
		{
			"levels",
			`SELECT COUNT(*) FROM levels l
			 LEFT JOIN feeds f ON f.feed_key = l.feed_key
			 WHERE f.feed_key IS NULL OR l.feed_mode != f.feed_mode OR trim(coalesce(l.source_level_id,'')) = ''`,
		},
		{
			"pathways",
			`SELECT COUNT(*) FROM pathways p
			 LEFT JOIN feeds f ON f.feed_key = p.feed_key
			 LEFT JOIN stops a ON a.stop_key = p.from_stop_key
			 LEFT JOIN stops b ON b.stop_key = p.to_stop_key
			 WHERE f.feed_key IS NULL OR a.stop_key IS NULL OR b.stop_key IS NULL
			    OR a.feed_key != p.feed_key OR b.feed_key != p.feed_key
			    OR a.location_type = 1 OR b.location_type = 1
			    OR a.stop_access = 1 OR b.stop_access = 1
			    OR trim(coalesce(p.source_pathway_id,'')) = ''`,
		},
		{
			"connections",
			`SELECT COUNT(*) FROM connections x
			 LEFT JOIN feeds f ON f.feed_key = x.feed_key
			 LEFT JOIN calendar c ON c.service_key = x.service_key
			 LEFT JOIN trips t ON t.trip_key = x.trip_key
			 LEFT JOIN routes r ON r.route_key = x.route_key
			 LEFT JOIN stops a ON a.stop_key = x.dep_stop_key
			 LEFT JOIN stops b ON b.stop_key = x.arr_stop_key
			 WHERE f.feed_key IS NULL OR c.service_key IS NULL OR t.trip_key IS NULL
			    OR r.route_key IS NULL OR a.stop_key IS NULL OR b.stop_key IS NULL
			    OR x.feed_mode != f.feed_mode OR c.feed_key != x.feed_key
			    OR t.feed_key != x.feed_key OR r.feed_key != x.feed_key
			    OR a.feed_key != x.feed_key OR b.feed_key != x.feed_key
			    OR t.service_key != x.service_key OR t.route_key != x.route_key
			    OR x.arr_sequence <= x.dep_sequence`,
		},
		{
			"trip links",
			`SELECT COUNT(*) FROM trip_links l
			 LEFT JOIN feeds f ON f.feed_key = l.feed_key
			 LEFT JOIN trips a ON a.trip_key = l.from_trip_key
			 LEFT JOIN trips b ON b.trip_key = l.to_trip_key
			 LEFT JOIN stops fs ON fs.stop_key = l.from_stop_key
			 LEFT JOIN stops ts ON ts.stop_key = l.to_stop_key
			 WHERE f.feed_key IS NULL OR a.trip_key IS NULL OR b.trip_key IS NULL
			    OR a.feed_key != l.feed_key OR b.feed_key != l.feed_key
			    OR l.transfer_type NOT IN (4,5) OR l.min_transfer_time < 0
			    OR (l.from_stop_key IS NOT NULL AND (fs.stop_key IS NULL OR fs.feed_key != l.feed_key))
			    OR (l.to_stop_key IS NOT NULL AND (ts.stop_key IS NULL OR ts.feed_key != l.feed_key))`,
		},
	}
	for _, check := range checks {
		var invalid int64
		if err := s.db.QueryRowContext(ctx, check.query).Scan(&invalid); err != nil {
			return fmt.Errorf("validating resolved %s: %w", check.name, err)
		}
		if invalid > 0 {
			return fmt.Errorf("%w: %s has %d invalid row(s)", ErrUnresolvedDataset, check.name, invalid)
		}
	}
	return nil
}

func validateDatasetState(state DatasetState, publishable bool) error {
	if state.GenerationID == "" {
		return errors.New("dataset generation id is empty")
	}
	if state.Provenance.SourceURL == "" {
		return errors.New("dataset source URL is empty")
	}
	if state.IngestedAt.IsZero() {
		return errors.New("dataset ingest time is empty")
	}
	if _, err := time.Parse("20060102", state.Coverage.Start); err != nil {
		return fmt.Errorf("invalid coverage start %q: %w", state.Coverage.Start, err)
	}
	if _, err := time.Parse("20060102", state.Coverage.End); err != nil {
		return fmt.Errorf("invalid coverage end %q: %w", state.Coverage.End, err)
	}
	if state.Coverage.Start > state.Coverage.End {
		return fmt.Errorf("coverage start %s is after end %s", state.Coverage.Start, state.Coverage.End)
	}
	if state.Provenance.DeclaredBytes < -1 || state.Provenance.ActualBytes < 0 {
		return errors.New("dataset byte counts must be non-negative (declared may be -1 when unknown)")
	}
	counts := state.Counts
	if counts.Feeds < 0 || counts.Stops < 0 || counts.Routes < 0 || counts.Services < 0 || counts.Trips < 0 || counts.StopTimes < 0 || counts.Transfers < 0 || counts.Pathways < 0 || counts.Connections < 0 || counts.TripLinks < 0 {
		return errors.New("dataset row counts must be non-negative")
	}
	if publishable && (counts.Feeds == 0 || counts.Stops == 0 || counts.Routes == 0 || counts.Services == 0 || counts.Trips == 0 || counts.StopTimes == 0 || counts.Connections == 0) {
		return errors.New("dataset core row counts must be non-zero before publication")
	}
	return nil
}
