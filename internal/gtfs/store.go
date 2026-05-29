package gtfs

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

// Store wraps the GTFS SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens (and creates if needed) the GTFS database at path.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	if f, err := os.OpenFile(path, os.O_CREATE, 0o600); err != nil {
		return nil, err
	} else if err := f.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite TEMP tables are connection-scoped.
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating schema: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Meta returns a metadata value, or "" if absent.
func (s *Store) Meta(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func (s *Store) setMeta(tx *sql.Tx, key, value string) error {
	_, err := tx.Exec(`INSERT INTO meta(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// SetMeta writes a single metadata value outside of an ingest transaction
// (used to record feed provenance and update-check state).
func (s *Store) SetMeta(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO meta(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// Counts returns row counts for the primary tables (for status output).
func (s *Store) Counts() (map[string]int, error) {
	out := map[string]int{}
	for _, t := range []string{"stops", "routes", "trips", "stop_times"} {
		var n int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM " + t).Scan(&n); err != nil {
			return nil, err
		}
		out[t] = n
	}
	return out, nil
}

// IsIngested reports whether the database has been populated.
func (s *Store) IsIngested() bool {
	v, _ := s.Meta("ingested_at")
	return v != ""
}

// parseGTFSTime parses an HH:MM:SS GTFS time (which may exceed 24h) into
// seconds since midnight.
func parseGTFSTime(v string) (int, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	parts := strings.Split(v, ":")
	if len(parts) != 3 {
		return 0, false
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	sec, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, false
	}
	if h < 0 || m < 0 || m > 59 || sec < 0 || sec > 59 {
		return 0, false
	}
	return h*3600 + m*60 + sec, true
}
