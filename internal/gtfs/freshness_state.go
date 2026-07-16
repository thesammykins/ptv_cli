package gtfs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	freshnessStateFilename      = ".freshness.sqlite"
	freshnessStateSchemaVersion = 1
)

// FreshnessCheckState is the durable, source-keyed check record. All fields are
// replaced atomically in one transaction after each evaluation.
type FreshnessCheckState struct {
	SourceURL            string
	DatasetSourceURL     string
	GenerationID         string
	LocalETag            string
	LocalLastModified    string
	DeclaredBytes        int64
	ActualBytes          int64
	PublicationTime      *time.Time
	IngestedAt           *time.Time
	CoverageStart        string
	CoverageEnd          string
	LastAttemptAt        *time.Time
	LastSuccessAt        *time.Time
	Result               FreshnessClassification
	CheckError           string
	RemoteETag           string
	RemoteLastModified   string
	RemoteContentLength  int64
	FailureCount         int
	NextAutomaticAttempt *time.Time
	UpdatedAt            time.Time
}

// FreshnessStateDatabasePath returns the separate mutable state path used for
// freshness checks. Published generation databases are never written.
func FreshnessStateDatabasePath(dataDir string) (string, error) {
	manager, err := NewGenerationManager(dataDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(manager.DataDir(), freshnessStateFilename), nil
}

func openFreshnessStateDB(ctx context.Context, dataDir string) (*sql.DB, string, error) {
	path, err := FreshnessStateDatabasePath(dataDir)
	if err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, "", fmt.Errorf("creating GTFS freshness state directory: %w", err)
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, "", fmt.Errorf("GTFS freshness state database must not be a symbolic link: %s", path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, "", err
	}
	if err := ensureDatabaseFile(path, false); err != nil {
		return nil, "", err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, "", fmt.Errorf("securing GTFS freshness state database: %w", err)
	}
	db, err := openSQLite(ctx, path, "rw", false, defaultSQLiteBusyTimeout)
	if err != nil {
		return nil, "", err
	}
	if err := configureWritable(ctx, db); err != nil {
		db.Close()
		return nil, "", err
	}
	if err := initializeFreshnessStateDB(ctx, db); err != nil {
		db.Close()
		return nil, "", err
	}
	return db, path, nil
}

func initializeFreshnessStateDB(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning GTFS freshness schema transaction: %w", err)
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("reading GTFS freshness schema version: %w", err)
	}
	if version > freshnessStateSchemaVersion {
		return fmt.Errorf("GTFS freshness state schema %d is newer than supported schema %d", version, freshnessStateSchemaVersion)
	}
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS freshness_state (
			source_url TEXT PRIMARY KEY,
			dataset_source_url TEXT NOT NULL,
			generation_id TEXT NOT NULL,
			local_etag TEXT NOT NULL,
			local_last_modified TEXT NOT NULL,
			declared_bytes INTEGER NOT NULL,
			actual_bytes INTEGER NOT NULL,
			publication_time_utc TEXT NOT NULL,
			ingested_at_utc TEXT NOT NULL,
			coverage_start TEXT NOT NULL,
			coverage_end TEXT NOT NULL,
			last_attempt_utc TEXT NOT NULL,
			last_success_utc TEXT NOT NULL,
			result TEXT NOT NULL CHECK(result IN ('current', 'changed', 'stale', 'unknown')),
			check_error TEXT NOT NULL,
			remote_etag TEXT NOT NULL,
			remote_last_modified TEXT NOT NULL,
			remote_content_length INTEGER NOT NULL,
			failure_count INTEGER NOT NULL CHECK(failure_count >= 0),
			next_automatic_attempt_utc TEXT NOT NULL,
			updated_at_utc TEXT NOT NULL
		) STRICT`); err != nil {
		return fmt.Errorf("initializing GTFS freshness state: %w", err)
	}
	if version == 0 {
		if _, err := tx.ExecContext(ctx, `PRAGMA user_version = 1`); err != nil {
			return fmt.Errorf("versioning GTFS freshness state: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing GTFS freshness schema: %w", err)
	}
	return nil
}

// LoadFreshnessCheckState reads one source-keyed durable record.
func LoadFreshnessCheckState(ctx context.Context, dataDir, sourceURL string) (FreshnessCheckState, bool, error) {
	sourceURL, err := validateFreshnessSourceURL(sourceURL)
	if err != nil {
		return FreshnessCheckState{}, false, err
	}
	db, _, err := openFreshnessStateDB(ctx, dataDir)
	if err != nil {
		return FreshnessCheckState{}, false, err
	}
	defer db.Close()
	return loadFreshnessCheckState(ctx, db, sourceURL)
}

func loadFreshnessCheckState(ctx context.Context, db *sql.DB, sourceURL string) (FreshnessCheckState, bool, error) {
	var state FreshnessCheckState
	var publication, ingested, lastAttempt, lastSuccess, nextAttempt, updated string
	err := db.QueryRowContext(ctx, `
		SELECT source_url, dataset_source_url, generation_id,
		       local_etag, local_last_modified, declared_bytes, actual_bytes,
		       publication_time_utc, ingested_at_utc, coverage_start, coverage_end,
		       last_attempt_utc, last_success_utc, result, check_error,
		       remote_etag, remote_last_modified, remote_content_length,
		       failure_count, next_automatic_attempt_utc, updated_at_utc
		FROM freshness_state WHERE source_url = ?`, sourceURL).Scan(
		&state.SourceURL, &state.DatasetSourceURL, &state.GenerationID,
		&state.LocalETag, &state.LocalLastModified, &state.DeclaredBytes, &state.ActualBytes,
		&publication, &ingested, &state.CoverageStart, &state.CoverageEnd,
		&lastAttempt, &lastSuccess, &state.Result, &state.CheckError,
		&state.RemoteETag, &state.RemoteLastModified, &state.RemoteContentLength,
		&state.FailureCount, &nextAttempt, &updated,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return FreshnessCheckState{}, false, nil
	}
	if err != nil {
		return FreshnessCheckState{}, false, fmt.Errorf("reading GTFS freshness state: %w", err)
	}
	if state.PublicationTime, err = parseOptionalFreshnessTime(publication); err != nil {
		return FreshnessCheckState{}, false, err
	}
	if state.IngestedAt, err = parseOptionalFreshnessTime(ingested); err != nil {
		return FreshnessCheckState{}, false, err
	}
	if state.LastAttemptAt, err = parseOptionalFreshnessTime(lastAttempt); err != nil {
		return FreshnessCheckState{}, false, err
	}
	if state.LastSuccessAt, err = parseOptionalFreshnessTime(lastSuccess); err != nil {
		return FreshnessCheckState{}, false, err
	}
	if state.NextAutomaticAttempt, err = parseOptionalFreshnessTime(nextAttempt); err != nil {
		return FreshnessCheckState{}, false, err
	}
	parsedUpdated, err := time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return FreshnessCheckState{}, false, fmt.Errorf("parsing GTFS freshness updated time: %w", err)
	}
	state.UpdatedAt = parsedUpdated
	return state, true, nil
}

func persistFreshnessCheckState(ctx context.Context, db *sql.DB, state FreshnessCheckState) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning GTFS freshness state transaction: %w", err)
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO freshness_state(
			source_url, dataset_source_url, generation_id,
			local_etag, local_last_modified, declared_bytes, actual_bytes,
			publication_time_utc, ingested_at_utc, coverage_start, coverage_end,
			last_attempt_utc, last_success_utc, result, check_error,
			remote_etag, remote_last_modified, remote_content_length,
			failure_count, next_automatic_attempt_utc, updated_at_utc
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(source_url) DO UPDATE SET
			dataset_source_url=excluded.dataset_source_url,
			generation_id=excluded.generation_id,
			local_etag=excluded.local_etag,
			local_last_modified=excluded.local_last_modified,
			declared_bytes=excluded.declared_bytes,
			actual_bytes=excluded.actual_bytes,
			publication_time_utc=excluded.publication_time_utc,
			ingested_at_utc=excluded.ingested_at_utc,
			coverage_start=excluded.coverage_start,
			coverage_end=excluded.coverage_end,
			last_attempt_utc=excluded.last_attempt_utc,
			last_success_utc=excluded.last_success_utc,
			result=excluded.result,
			check_error=excluded.check_error,
			remote_etag=excluded.remote_etag,
			remote_last_modified=excluded.remote_last_modified,
			remote_content_length=excluded.remote_content_length,
			failure_count=excluded.failure_count,
			next_automatic_attempt_utc=excluded.next_automatic_attempt_utc,
			updated_at_utc=excluded.updated_at_utc`,
		state.SourceURL, state.DatasetSourceURL, state.GenerationID,
		state.LocalETag, state.LocalLastModified, state.DeclaredBytes, state.ActualBytes,
		formatOptionalFreshnessTime(state.PublicationTime), formatOptionalFreshnessTime(state.IngestedAt), state.CoverageStart, state.CoverageEnd,
		formatOptionalFreshnessTime(state.LastAttemptAt), formatOptionalFreshnessTime(state.LastSuccessAt), state.Result, state.CheckError,
		state.RemoteETag, state.RemoteLastModified, state.RemoteContentLength,
		state.FailureCount, formatOptionalFreshnessTime(state.NextAutomaticAttempt), state.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("writing GTFS freshness state: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing GTFS freshness state: %w", err)
	}
	return nil
}

func timePointer(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	utc := value.UTC()
	return &utc
}

func formatOptionalFreshnessTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func parseOptionalFreshnessTime(raw string) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return nil, fmt.Errorf("parsing GTFS freshness state time: %w", err)
	}
	return &parsed, nil
}
