package gtfs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrLegacyDatabase identifies the unversioned database written by ptv
	// releases before immutable generations. It lacks source keys and transfer
	// semantics, so adding default columns cannot make it correctness-complete.
	ErrLegacyDatabase = errors.New("legacy GTFS database requires re-ingest")
	// ErrSchemaUpgradeRequired means the database is managed but older than the
	// current reader. Schema-changing upgrades are built as a new generation.
	ErrSchemaUpgradeRequired = errors.New("GTFS database schema upgrade required")
	// ErrSchemaTooNew means this binary cannot safely interpret the database.
	ErrSchemaTooNew = errors.New("GTFS database schema is newer than this binary")
	// ErrInvalidSchema means the declared schema version and actual tables do
	// not agree.
	ErrInvalidSchema = errors.New("invalid GTFS database schema")
)

// DatabaseKind classifies a database without changing it.
type DatabaseKind string

const (
	DatabaseMissing         DatabaseKind = "missing"
	DatabaseLegacy          DatabaseKind = "legacy"
	DatabaseManaged         DatabaseKind = "managed"
	DatabaseUpgradeRequired DatabaseKind = "upgrade_required"
	DatabaseTooNew          DatabaseKind = "too_new"
)

// DatabaseInfo is the non-mutating result of inspecting a SQLite file.
type DatabaseInfo struct {
	Path          string
	Kind          DatabaseKind
	SchemaVersion int
}

func initializeSchema(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning schema initialization: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating schema version %d: %w", CurrentSchemaVersion, err)
	}
	if err := setUserVersion(ctx, tx, CurrentSchemaVersion); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing schema initialization: %w", err)
	}
	return verifySchemaV2(ctx, db)
}

// migrateWritable initializes empty compatibility/staging files, but it never
// upgrades a populated database in place. Version 0 does not contain enough
// information to reconstruct namespaced identities, pathways, transfer
// specificity, or pickup/drop-off rules; it must be re-ingested.
func migrateWritable(ctx context.Context, db *sql.DB) error {
	version, err := readUserVersion(ctx, db)
	if err != nil {
		return err
	}
	if version > CurrentSchemaVersion {
		return fmt.Errorf("%w: found %d, support ends at %d", ErrSchemaTooNew, version, CurrentSchemaVersion)
	}
	if version == 0 {
		hasTables, err := hasApplicationTables(ctx, db)
		if err != nil {
			return err
		}
		if !hasTables {
			return initializeSchema(ctx, db)
		}
		return fmt.Errorf("%w; run 'ptv gtfs update' to build a managed generation", ErrLegacyDatabase)
	}
	if version < CurrentSchemaVersion {
		return fmt.Errorf("%w: found %d, want %d; run 'ptv gtfs update'", ErrSchemaUpgradeRequired, version, CurrentSchemaVersion)
	}
	return verifySchemaV2(ctx, db)
}

type sqlQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func columnExists(ctx context.Context, q sqlQueryer, table, column string) (bool, error) {
	// table is selected exclusively from constants in this package. Quoting it
	// still prevents punctuation from changing the PRAGMA statement.
	table = strings.ReplaceAll(table, `"`, `""`)
	rows, err := q.QueryContext(ctx, fmt.Sprintf(`PRAGMA table_info("%s")`, table))
	if err != nil {
		return false, fmt.Errorf("inspecting table %s: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, fmt.Errorf("reading table %s columns: %w", table, err)
		}
		if name == column {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterating table %s columns: %w", table, err)
	}
	return false, nil
}

func tableExists(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, table string) (bool, error) {
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM sqlite_schema WHERE type = 'table' AND name = ?`, table).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking table %s: %w", table, err)
	}
	return true, nil
}

func hasApplicationTables(ctx context.Context, db *sql.DB) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&count); err != nil {
		return false, fmt.Errorf("checking database schema: %w", err)
	}
	return count > 0, nil
}

func readUserVersion(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (int, error) {
	var version int
	if err := q.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return 0, fmt.Errorf("reading schema version: %w", err)
	}
	return version, nil
}

func setUserVersion(ctx context.Context, tx *sql.Tx, version int) error {
	if version < 0 || version > CurrentSchemaVersion {
		return fmt.Errorf("invalid schema version %d", version)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, version)); err != nil {
		return fmt.Errorf("setting schema version %d: %w", version, err)
	}
	return nil
}

func verifySchemaV2(ctx context.Context, db *sql.DB) error {
	required := map[string][]string{
		"feeds":          {"feed_key", "feed_mode", "source_namespace"},
		"stops":          {"stop_key", "feed_key", "feed_mode", "source_stop_id", "location_type", "parent_stop_key", "stop_access"},
		"routes":         {"route_key", "feed_key", "feed_mode", "source_route_id"},
		"calendar":       {"service_key", "feed_key", "feed_mode", "source_service_id"},
		"trips":          {"trip_key", "feed_key", "source_trip_id", "route_key", "service_key", "source_block_id", "block_id"},
		"stop_times":     {"stop_time_key", "trip_key", "stop_key", "pickup_type", "drop_off_type"},
		"calendar_dates": {"service_key", "date", "exception_type"},
		"transfers":      {"transfer_key", "feed_key", "from_stop_key", "to_stop_key", "transfer_type", "from_route_key", "to_route_key", "from_trip_key", "to_trip_key", "source"},
		"levels":         {"level_key", "feed_key", "source_level_id", "level_index"},
		"pathways":       {"pathway_key", "feed_key", "source_pathway_id", "from_stop_key", "to_stop_key", "is_bidirectional"},
		"connections":    {"connection_key", "feed_key", "service_key", "trip_key", "route_key", "dep_stop_key", "arr_stop_key", "dep_sequence", "arr_sequence"},
		"trip_links":     {"trip_link_key", "feed_key", "from_trip_key", "to_trip_key", "transfer_type"},
		"dataset_state":  {"generation_id", "coverage_start", "coverage_end", "feed_count", "service_count", "connection_count", "trip_link_count"},
	}
	for table, columns := range required {
		exists, err := tableExists(ctx, db, table)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("%w: schema version 2 is missing table %s", ErrInvalidSchema, table)
		}
		for _, column := range columns {
			exists, err := columnExists(ctx, db, table, column)
			if err != nil {
				return err
			}
			if !exists {
				return fmt.Errorf("%w: schema version 2 is missing %s.%s", ErrInvalidSchema, table, column)
			}
		}
	}
	return nil
}
