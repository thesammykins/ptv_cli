package gtfs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const defaultSQLiteBusyTimeout = 5 * time.Second

// Store wraps one SQLite generation. Published generations are opened
// read-only; compatibility and staging stores are writable.
type Store struct {
	db       *sql.DB
	path     string
	readOnly bool
}

// Open preserves the original create-or-open signature for callers that have
// not yet moved to GenerationManager. Empty files are initialized, but
// populated legacy and older managed schemas fail closed and require re-ingest;
// this function must not be used for a published immutable generation.
func Open(path string) (*Store, error) {
	return OpenContext(context.Background(), path)
}

// OpenContext is the context-aware compatibility form of Open.
func OpenContext(ctx context.Context, path string) (*Store, error) {
	path, err := absoluteDatabasePath(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("creating GTFS data directory: %w", err)
	}
	if err := ensureDatabaseFile(path, false); err != nil {
		return nil, err
	}
	store, err := openStore(ctx, path, "rw", false, defaultSQLiteBusyTimeout)
	if err != nil {
		return nil, err
	}
	if err := migrateWritable(ctx, store.db); err != nil {
		store.Close()
		return nil, err
	}
	if err := configureWritable(ctx, store.db); err != nil {
		store.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		store.Close()
		return nil, fmt.Errorf("securing GTFS database: %w", err)
	}
	return store, nil
}

// CreateStaging creates a new version-2 writable database. It refuses to open
// or truncate an existing path.
func CreateStaging(ctx context.Context, path string) (*Store, error) {
	path, err := absoluteDatabasePath(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("creating staging directory: %w", err)
	}
	if err := ensureDatabaseFile(path, true); err != nil {
		return nil, err
	}
	store, err := openStore(ctx, path, "rw", false, defaultSQLiteBusyTimeout)
	if err != nil {
		removeSQLiteFiles(path)
		return nil, err
	}
	if err := initializeSchema(ctx, store.db); err != nil {
		store.Close()
		removeSQLiteFiles(path)
		return nil, err
	}
	if err := configureWritable(ctx, store.db); err != nil {
		store.Close()
		removeSQLiteFiles(path)
		return nil, err
	}
	return store, nil
}

// OpenReadOnly opens an existing managed generation without creating or
// migrating it. Paths containing URI metacharacters remain literal paths.
func OpenReadOnly(ctx context.Context, path string) (*Store, error) {
	path, err := absoluteDatabasePath(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("opening GTFS database: %w", err)
		}
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("GTFS database must not be a symbolic link: %s", path)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("GTFS database is not a regular file: %s", path)
	}
	store, err := openStore(ctx, path, "ro", true, defaultSQLiteBusyTimeout)
	if err != nil {
		return nil, err
	}
	version, err := store.SchemaVersion(ctx)
	if err != nil {
		store.Close()
		return nil, err
	}
	switch {
	case version == 0:
		store.Close()
		return nil, fmt.Errorf("%w: %s", ErrLegacyDatabase, path)
	case version < CurrentSchemaVersion:
		store.Close()
		return nil, fmt.Errorf("%w: found %d, want %d", ErrSchemaUpgradeRequired, version, CurrentSchemaVersion)
	case version > CurrentSchemaVersion:
		store.Close()
		return nil, fmt.Errorf("%w: found %d, support ends at %d", ErrSchemaTooNew, version, CurrentSchemaVersion)
	}
	if err := verifySchemaV2(ctx, store.db); err != nil {
		store.Close()
		return nil, err
	}
	return store, nil
}

// InspectDatabase classifies path without creating or changing it.
func InspectDatabase(ctx context.Context, path string) (DatabaseInfo, error) {
	path, err := absoluteDatabasePath(path)
	if err != nil {
		return DatabaseInfo{}, err
	}
	result := DatabaseInfo{Path: path, Kind: DatabaseMissing}
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return result, nil
		}
		return DatabaseInfo{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return DatabaseInfo{}, fmt.Errorf("GTFS database must not be a symbolic link: %s", path)
	}
	if !info.Mode().IsRegular() {
		return DatabaseInfo{}, fmt.Errorf("GTFS database is not a regular file: %s", path)
	}
	db, err := openSQLite(ctx, path, "ro", true, defaultSQLiteBusyTimeout)
	if err != nil {
		return DatabaseInfo{}, err
	}
	defer db.Close()
	version, err := readUserVersion(ctx, db)
	if err != nil {
		return DatabaseInfo{}, err
	}
	result.SchemaVersion = version
	switch {
	case version == 0:
		result.Kind = DatabaseLegacy
	case version < CurrentSchemaVersion:
		result.Kind = DatabaseUpgradeRequired
	case version == CurrentSchemaVersion:
		result.Kind = DatabaseManaged
		if err := verifySchemaV2(ctx, db); err != nil {
			return DatabaseInfo{}, err
		}
	case version > CurrentSchemaVersion:
		result.Kind = DatabaseTooNew
	}
	return result, nil
}

func openStore(ctx context.Context, path, mode string, readOnly bool, busyTimeout time.Duration) (*Store, error) {
	db, err := openSQLite(ctx, path, mode, readOnly, busyTimeout)
	if err != nil {
		return nil, err
	}
	return &Store{db: db, path: path, readOnly: readOnly}, nil
}

func openSQLite(ctx context.Context, path, mode string, readOnly bool, busyTimeout time.Duration) (*sql.DB, error) {
	dsn, err := sqliteDSN(path, mode, readOnly, busyTimeout)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening GTFS database: %w", err)
	}
	if readOnly {
		db.SetMaxOpenConns(4)
		db.SetMaxIdleConns(2)
	} else {
		// Existing timetable construction uses connection-scoped TEMP tables.
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
	}
	db.SetConnMaxIdleTime(time.Minute)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("opening GTFS database %s: %w", path, err)
	}
	return db, nil
}

func sqliteDSN(path, mode string, readOnly bool, busyTimeout time.Duration) (string, error) {
	path, err := absoluteDatabasePath(path)
	if err != nil {
		return "", err
	}
	u := &url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	q := url.Values{}
	q.Set("mode", mode)
	if busyTimeout < 0 {
		busyTimeout = 0
	}
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeout.Milliseconds()))
	q.Add("_pragma", "foreign_keys(1)")
	if readOnly {
		q.Add("_pragma", "query_only(1)")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func configureWritable(ctx context.Context, db *sql.DB) error {
	var journal string
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode = WAL`).Scan(&journal); err != nil {
		return fmt.Errorf("enabling SQLite WAL: %w", err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA synchronous = NORMAL`); err != nil {
		return fmt.Errorf("configuring SQLite durability: %w", err)
	}
	return nil
}

func absoluteDatabasePath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("GTFS database path is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolving GTFS database path: %w", err)
	}
	return filepath.Clean(abs), nil
}

func ensureDatabaseFile(path string, exclusive bool) error {
	flags := os.O_CREATE | os.O_RDWR
	if exclusive {
		flags |= os.O_EXCL
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		if exclusive && errors.Is(err, os.ErrExist) {
			return fmt.Errorf("staging database already exists: %s", path)
		}
		return fmt.Errorf("creating GTFS database: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing GTFS database file: %w", err)
	}
	return nil
}

func removeSQLiteFiles(path string) {
	_ = os.Remove(path)
	_ = os.Remove(path + "-wal")
	_ = os.Remove(path + "-shm")
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Path is the canonical filesystem path backing the store.
func (s *Store) Path() string { return s.path }

// ReadOnly reports whether the store was opened through OpenReadOnly.
func (s *Store) ReadOnly() bool { return s.readOnly }

// SchemaVersion returns PRAGMA user_version.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	return readUserVersion(ctx, s.db)
}

// MetaContext returns a metadata value, or "" if absent.
func (s *Store) MetaContext(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading GTFS metadata %q: %w", key, err)
	}
	return value, nil
}

// Meta is the compatibility wrapper for MetaContext.
func (s *Store) Meta(key string) (string, error) {
	return s.MetaContext(context.Background(), key)
}

func (s *Store) setMetaContext(ctx context.Context, tx *sql.Tx, key, value string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("writing GTFS metadata %q: %w", key, err)
	}
	return nil
}

// setMeta preserves the package-internal API used by existing ingest code.
func (s *Store) setMeta(tx *sql.Tx, key, value string) error {
	return s.setMetaContext(context.Background(), tx, key, value)
}

// SetMetaContext writes a single metadata value outside an ingest transaction.
func (s *Store) SetMetaContext(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("writing GTFS metadata %q: %w", key, err)
	}
	return nil
}

// SetMeta is the compatibility wrapper for SetMetaContext.
func (s *Store) SetMeta(key, value string) error {
	return s.SetMetaContext(context.Background(), key, value)
}

// CountsContext counts the primary tables. New status paths should use
// PersistedCounts instead; this scan remains for compatibility and validation.
func (s *Store) CountsContext(ctx context.Context) (map[string]int, error) {
	out := map[string]int{}
	for _, table := range []string{"stops", "routes", "trips", "stop_times"} {
		var count int
		// table is selected from the fixed allowlist above.
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			return nil, fmt.Errorf("counting %s: %w", table, err)
		}
		out[table] = count
	}
	return out, nil
}

// Counts is the compatibility wrapper for CountsContext.
func (s *Store) Counts() (map[string]int, error) {
	return s.CountsContext(context.Background())
}

// IsIngestedContext reports whether a dataset state or legacy ingest marker is
// present.
func (s *Store) IsIngestedContext(ctx context.Context) bool {
	var one int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM dataset_state WHERE id = 1`).Scan(&one); err == nil {
		return true
	}
	value, err := s.MetaContext(ctx, "ingested_at")
	return err == nil && value != ""
}

// IsIngested is the compatibility wrapper for IsIngestedContext.
func (s *Store) IsIngested() bool {
	return s.IsIngestedContext(context.Background())
}

// parseGTFSTime parses an HH:MM:SS GTFS time (which may exceed 24h) into
// seconds since midnight.
func parseGTFSTime(value string) (int, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	parts := strings.Split(value, ":")
	if len(parts) != 3 {
		return 0, false
	}
	hours, errHours := strconv.Atoi(parts[0])
	minutes, errMinutes := strconv.Atoi(parts[1])
	seconds, errSeconds := strconv.Atoi(parts[2])
	if errHours != nil || errMinutes != nil || errSeconds != nil {
		return 0, false
	}
	if hours < 0 || minutes < 0 || minutes > 59 || seconds < 0 || seconds > 59 {
		return 0, false
	}
	return hours*3600 + minutes*60 + seconds, true
}
