package gtfs

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/thesammykins/ptv_cli/internal/atomicfile"
	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const (
	manifestFormatVersion = 1
	manifestFilename      = "current.json"
	generationsDirname    = "generations"
	stagingDirname        = "staging"
	updateLockFilename    = ".update-lock.sqlite"
	maxManifestBytes      = 64 << 10
)

var (
	// ErrNoCurrentGeneration means no managed dataset has been published.
	ErrNoCurrentGeneration = errors.New("no current GTFS generation")
	// ErrUpdateInProgress is returned immediately when another process owns the
	// SQLite-backed update lease.
	ErrUpdateInProgress = errors.New("GTFS update already in progress")
	// ErrNoPreviousGeneration means rollback has no retained predecessor.
	ErrNoPreviousGeneration = errors.New("no previous GTFS generation")
)

// GenerationRef identifies one immutable database file relative to the
// manager's generations directory.
type GenerationRef struct {
	ID             string `json:"id"`
	Filename       string `json:"filename"`
	SchemaVersion  int    `json:"schema_version"`
	PublishedAtUTC string `json:"published_at_utc"`
}

// CurrentManifest is the single atomic pointer readers consult. Previous is
// retained for an explicit rollback.
type CurrentManifest struct {
	FormatVersion int            `json:"format_version"`
	Current       GenerationRef  `json:"current"`
	Previous      *GenerationRef `json:"previous,omitempty"`
	UpdatedAtUTC  string         `json:"updated_at_utc"`
}

// GenerationManager owns immutable generation layout and publication.
type GenerationManager struct {
	dataDir       string
	manifestWrite func(path string, data []byte, mode os.FileMode) error
}

// NewGenerationManager validates dataDir without creating it.
func NewGenerationManager(dataDir string) (*GenerationManager, error) {
	if dataDir == "" {
		return nil, errors.New("GTFS data directory is empty")
	}
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, fmt.Errorf("resolving GTFS data directory: %w", err)
	}
	abs = filepath.Clean(abs)
	if filepath.Dir(abs) == abs {
		return nil, errors.New("GTFS data directory must not be a filesystem root")
	}
	return &GenerationManager{dataDir: abs}, nil
}

// DataDir returns the canonical manager directory.
func (m *GenerationManager) DataDir() string { return m.dataDir }

// LegacyDatabasePath is the pre-generation path retained for detection and
// operational rollback to an older binary.
func (m *GenerationManager) LegacyDatabasePath() string {
	return filepath.Join(m.dataDir, "gtfs.sqlite")
}

func (m *GenerationManager) manifestPath() string {
	return filepath.Join(m.dataDir, manifestFilename)
}

func (m *GenerationManager) generationsDir() string {
	return filepath.Join(m.dataDir, generationsDirname)
}

func (m *GenerationManager) stagingDir() string {
	return filepath.Join(m.dataDir, stagingDirname)
}

func (m *GenerationManager) ensureLayout() error {
	for _, dir := range []string{m.dataDir, m.generationsDir(), m.stagingDir()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("creating generation directory %s: %w", dir, err)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("securing generation directory %s: %w", dir, err)
		}
	}
	return nil
}

// ReadManifest reads and validates the bounded current manifest. When only the
// pre-generation gtfs.sqlite layout exists it returns ErrLegacyDatabase.
func (m *GenerationManager) ReadManifest(ctx context.Context) (CurrentManifest, error) {
	manifest, err := m.readManifestFile(ctx)
	if errors.Is(err, os.ErrNotExist) {
		if _, legacyErr := os.Stat(m.LegacyDatabasePath()); legacyErr == nil {
			return CurrentManifest{}, fmt.Errorf("%w: %s", ErrLegacyDatabase, m.LegacyDatabasePath())
		}
		return CurrentManifest{}, ErrNoCurrentGeneration
	}
	return manifest, err
}

func (m *GenerationManager) readManifestFile(ctx context.Context) (CurrentManifest, error) {
	if err := ctx.Err(); err != nil {
		return CurrentManifest{}, err
	}
	f, err := os.Open(m.manifestPath())
	if err != nil {
		return CurrentManifest{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxManifestBytes+1))
	if err != nil {
		return CurrentManifest{}, fmt.Errorf("reading GTFS generation manifest: %w", err)
	}
	if len(data) > maxManifestBytes {
		return CurrentManifest{}, fmt.Errorf("GTFS generation manifest exceeds %d bytes", maxManifestBytes)
	}
	var manifest CurrentManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return CurrentManifest{}, fmt.Errorf("decoding GTFS generation manifest: %w", err)
	}
	if err := validateManifest(manifest); err != nil {
		return CurrentManifest{}, err
	}
	return manifest, nil
}

// OpenCurrent resolves the manifest once and opens that immutable generation
// read-only. Publication cannot redirect an already-open reader.
func (m *GenerationManager) OpenCurrent(ctx context.Context) (*Store, GenerationRef, error) {
	manifest, err := m.ReadManifest(ctx)
	if err != nil {
		return nil, GenerationRef{}, err
	}
	path, err := m.generationPath(manifest.Current)
	if err != nil {
		return nil, GenerationRef{}, err
	}
	store, err := OpenReadOnly(ctx, path)
	if err != nil {
		return nil, GenerationRef{}, fmt.Errorf("opening current GTFS generation %s: %w", manifest.Current.ID, err)
	}
	version, err := store.SchemaVersion(ctx)
	if err != nil {
		store.Close()
		return nil, GenerationRef{}, err
	}
	if version != manifest.Current.SchemaVersion {
		store.Close()
		return nil, GenerationRef{}, fmt.Errorf("%w: manifest says schema %d, database says %d", ErrInvalidSchema, manifest.Current.SchemaVersion, version)
	}
	return store, manifest.Current, nil
}

// UpdateLock is a process-safe update lease implemented with SQLite's
// BEGIN IMMEDIATE lock. The operating system releases it if the process exits.
type UpdateLock struct {
	manager *GenerationManager
	db      *sql.DB
	conn    *sql.Conn

	mu       sync.Mutex
	released bool
}

// AcquireUpdate returns immediately with ErrUpdateInProgress if another
// process holds the lease.
func (m *GenerationManager) AcquireUpdate(ctx context.Context) (*UpdateLock, error) {
	if err := m.ensureLayout(); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(m.dataDir, updateLockFilename)
	if err := ensureDatabaseFile(lockPath, false); err != nil {
		return nil, err
	}
	db, err := openSQLite(ctx, lockPath, "rw", false, 0)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("opening GTFS update lock: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		conn.Close()
		db.Close()
		if isSQLiteBusy(err) {
			return nil, ErrUpdateInProgress
		}
		return nil, fmt.Errorf("acquiring GTFS update lock: %w", err)
	}
	return &UpdateLock{manager: m, db: db, conn: conn}, nil
}

// Release gives up the update lease. It is idempotent.
func (l *UpdateLock) Release() error {
	l.mu.Lock()
	if l.released {
		l.mu.Unlock()
		return nil
	}
	l.released = true
	conn, db := l.conn, l.db
	l.mu.Unlock()

	var result error
	if conn != nil {
		if _, err := conn.ExecContext(context.Background(), `ROLLBACK`); err != nil && !errors.Is(err, sql.ErrConnDone) {
			result = errors.Join(result, fmt.Errorf("releasing GTFS update transaction: %w", err))
		}
		result = errors.Join(result, conn.Close())
	}
	if db != nil {
		result = errors.Join(result, db.Close())
	}
	return result
}

func (l *UpdateLock) requireActive() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return errors.New("GTFS update lock has been released")
	}
	return nil
}

// StagingGeneration owns a private writable database until Publish consumes it.
type StagingGeneration struct {
	Ref   GenerationRef
	Store *Store

	path string
	mu   sync.Mutex
}

// Path returns the private staging path.
func (s *StagingGeneration) Path() string { return s.path }

// Close closes the staging database. It is safe to call after Publish.
func (s *StagingGeneration) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Store == nil {
		return nil
	}
	err := s.Store.Close()
	s.Store = nil
	return err
}

// NewStaging creates an empty schema-v2 candidate owned by this update lease.
func (l *UpdateLock) NewStaging(ctx context.Context) (*StagingGeneration, error) {
	if err := l.requireActive(); err != nil {
		return nil, err
	}
	id, err := newGenerationID()
	if err != nil {
		return nil, err
	}
	filename := id + ".sqlite"
	path := filepath.Join(l.manager.stagingDir(), filename+".tmp")
	store, err := CreateStaging(ctx, path)
	if err != nil {
		return nil, err
	}
	return &StagingGeneration{
		Ref: GenerationRef{
			ID:            id,
			Filename:      filename,
			SchemaVersion: CurrentSchemaVersion,
		},
		Store: store,
		path:  path,
	}, nil
}

// Publish validates and consumes staging, installs its immutable file, then
// atomically replaces current.json. Any failure before the manifest replace
// leaves existing readers and the current generation unchanged.
func (l *UpdateLock) Publish(ctx context.Context, staging *StagingGeneration) error {
	if err := l.requireActive(); err != nil {
		return err
	}
	if staging == nil || staging.Store == nil {
		return errors.New("staging generation is closed or nil")
	}
	if err := validateGenerationRef(staging.Ref); err != nil {
		return err
	}
	wantPath := filepath.Join(l.manager.stagingDir(), staging.Ref.Filename+".tmp")
	if filepath.Clean(staging.path) != filepath.Clean(wantPath) {
		return errors.New("staging database is outside the managed staging path")
	}
	if err := staging.Store.prepareForPublication(ctx, staging.Ref.ID); err != nil {
		return err
	}
	if err := staging.Close(); err != nil {
		return fmt.Errorf("closing staging generation: %w", err)
	}
	if err := syncFile(staging.path); err != nil {
		return err
	}

	finalPath := filepath.Join(l.manager.generationsDir(), staging.Ref.Filename)
	if _, err := os.Lstat(finalPath); err == nil {
		return fmt.Errorf("generation file already exists: %s", finalPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(staging.path, finalPath); err != nil {
		return fmt.Errorf("installing GTFS generation: %w", err)
	}
	staging.path = finalPath
	if err := os.Chmod(finalPath, 0o400); err != nil {
		return removeUnpublishedGeneration(finalPath, fmt.Errorf("making GTFS generation immutable: %w", err))
	}
	if err := atomicfile.SyncDirectory(l.manager.generationsDir()); err != nil {
		return removeUnpublishedGeneration(finalPath, fmt.Errorf("syncing GTFS generation directory: %w", err))
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	staging.Ref.PublishedAtUTC = now
	manifest := CurrentManifest{
		FormatVersion: manifestFormatVersion,
		Current:       staging.Ref,
		UpdatedAtUTC:  now,
	}
	old, err := l.manager.readManifestFile(ctx)
	if err == nil {
		previous := old.Current
		manifest.Previous = &previous
	} else if !errors.Is(err, os.ErrNotExist) {
		return removeUnpublishedGeneration(finalPath, err)
	}
	if err := l.manager.writeManifestAtomic(ctx, manifest); err != nil {
		// Atomic replacement can fail while syncing its directory after the
		// rename already committed. Read back the pointer before deciding
		// whether cleanup is safe or whether publication actually succeeded.
		observed, readErr := l.manager.readManifestFile(context.Background())
		if readErr == nil && observed.Current.ID == staging.Ref.ID {
			return nil
		}
		if readErr == nil || errors.Is(readErr, os.ErrNotExist) {
			return removeUnpublishedGeneration(finalPath, err)
		}
		return errors.Join(err, fmt.Errorf("GTFS publication state is ambiguous; preserving generation %s: %w", staging.Ref.ID, readErr))
	}
	return nil
}

func removeUnpublishedGeneration(path string, cause error) error {
	chmodErr := os.Chmod(path, 0o600)
	removeErr := os.Remove(path)
	if removeErr != nil {
		removeErr = fmt.Errorf("removing unpublished GTFS generation: %w", removeErr)
	}
	if chmodErr != nil && !errors.Is(chmodErr, os.ErrNotExist) {
		chmodErr = fmt.Errorf("making unpublished GTFS generation removable: %w", chmodErr)
	} else {
		chmodErr = nil
	}
	return errors.Join(cause, chmodErr, removeErr)
}

// Rollback atomically swaps current and previous after validating the retained
// generation. No database files are rewritten.
func (l *UpdateLock) Rollback(ctx context.Context) error {
	if err := l.requireActive(); err != nil {
		return err
	}
	manifest, err := l.manager.ReadManifest(ctx)
	if err != nil {
		return err
	}
	if manifest.Previous == nil {
		return ErrNoPreviousGeneration
	}
	previousPath, err := l.manager.generationPath(*manifest.Previous)
	if err != nil {
		return err
	}
	store, err := OpenReadOnly(ctx, previousPath)
	if err != nil {
		return fmt.Errorf("validating rollback generation: %w", err)
	}
	if err := store.Close(); err != nil {
		return err
	}
	current := manifest.Current
	manifest.Current = *manifest.Previous
	manifest.Previous = &current
	manifest.UpdatedAtUTC = time.Now().UTC().Format(time.RFC3339Nano)
	return l.manager.writeManifestAtomic(ctx, manifest)
}

func (s *Store) prepareForPublication(ctx context.Context, generationID string) error {
	if s.readOnly {
		return errors.New("cannot publish a read-only generation")
	}
	version, err := s.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	if version != CurrentSchemaVersion {
		return fmt.Errorf("%w: found %d, want %d", ErrInvalidSchema, version, CurrentSchemaVersion)
	}
	if err := verifySchemaV2(ctx, s.db); err != nil {
		return err
	}
	state, err := s.DatasetState(ctx)
	if err != nil {
		return err
	}
	if state.GenerationID != generationID {
		return fmt.Errorf("dataset state belongs to generation %s, want %s", state.GenerationID, generationID)
	}
	if err := validateDatasetState(state, true); err != nil {
		return err
	}
	if err := s.ValidateResolvedDataset(ctx); err != nil {
		return err
	}
	actual, err := s.ComputeDatasetCounts(ctx)
	if err != nil {
		return err
	}
	if actual != state.Counts {
		return fmt.Errorf("persisted dataset counts do not match database: persisted=%+v actual=%+v", state.Counts, actual)
	}
	if _, err := s.db.ExecContext(ctx, indexes); err != nil {
		return fmt.Errorf("building generation indexes: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA optimize`); err != nil {
		return fmt.Errorf("optimizing generation: %w", err)
	}
	var integrity string
	if err := s.db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return fmt.Errorf("checking generation integrity: %w", err)
	}
	if integrity != "ok" {
		return fmt.Errorf("generation integrity check failed: %s", integrity)
	}
	var busy, logFrames, checkpointed int
	if err := s.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointed); err != nil {
		return fmt.Errorf("checkpointing generation: %w", err)
	}
	if busy != 0 {
		return errors.New("generation WAL checkpoint remained busy")
	}
	var journalMode string
	if err := s.db.QueryRowContext(ctx, `PRAGMA journal_mode = DELETE`).Scan(&journalMode); err != nil {
		return fmt.Errorf("sealing generation journal: %w", err)
	}
	if journalMode != "delete" {
		return fmt.Errorf("sealing generation journal: got mode %q", journalMode)
	}
	return nil
}

func (m *GenerationManager) writeManifestAtomic(ctx context.Context, manifest CurrentManifest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateManifest(manifest); err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding GTFS generation manifest: %w", err)
	}
	data = append(data, '\n')
	if len(data) > maxManifestBytes {
		return fmt.Errorf("GTFS generation manifest exceeds %d bytes", maxManifestBytes)
	}
	write := atomicfile.WriteFile
	if m.manifestWrite != nil {
		write = m.manifestWrite
	}
	if err := write(m.manifestPath(), data, 0o600); err != nil {
		return fmt.Errorf("publishing GTFS generation manifest: %w", err)
	}
	return nil
}

func validateManifest(manifest CurrentManifest) error {
	if manifest.FormatVersion != manifestFormatVersion {
		return fmt.Errorf("unsupported GTFS manifest format %d", manifest.FormatVersion)
	}
	if err := validateGenerationRef(manifest.Current); err != nil {
		return fmt.Errorf("invalid current generation: %w", err)
	}
	if manifest.Current.PublishedAtUTC == "" {
		return errors.New("current generation publication time is empty")
	}
	if manifest.Previous != nil {
		if err := validateGenerationRef(*manifest.Previous); err != nil {
			return fmt.Errorf("invalid previous generation: %w", err)
		}
		if manifest.Previous.PublishedAtUTC == "" {
			return errors.New("previous generation publication time is empty")
		}
	}
	if _, err := time.Parse(time.RFC3339Nano, manifest.UpdatedAtUTC); err != nil {
		return fmt.Errorf("invalid manifest update time: %w", err)
	}
	return nil
}

func (m *GenerationManager) generationPath(ref GenerationRef) (string, error) {
	if err := validateGenerationRef(ref); err != nil {
		return "", err
	}
	path := filepath.Join(m.generationsDir(), ref.Filename)
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspecting GTFS generation %s: %w", ref.ID, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("GTFS generation is not a regular immutable file: %s", path)
	}
	return path, nil
}

func validateGenerationRef(ref GenerationRef) error {
	if ref.ID == "" || !safeGenerationID(ref.ID) {
		return fmt.Errorf("invalid generation id %q", ref.ID)
	}
	if ref.Filename != ref.ID+".sqlite" || filepath.Base(ref.Filename) != ref.Filename {
		return fmt.Errorf("invalid generation filename %q", ref.Filename)
	}
	if ref.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("invalid generation schema %d", ref.SchemaVersion)
	}
	if ref.PublishedAtUTC != "" {
		if _, err := time.Parse(time.RFC3339Nano, ref.PublishedAtUTC); err != nil {
			return fmt.Errorf("invalid generation publication time: %w", err)
		}
	}
	return nil
}

func safeGenerationID(id string) bool {
	if len(id) < 3 || len(id) > 96 {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return false
	}
	return true
}

func newGenerationID() (string, error) {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("generating GTFS generation id: %w", err)
	}
	return "g-" + time.Now().UTC().Format("20060102t150405000000000") + "-" + hex.EncodeToString(suffix[:]), nil
}

func syncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("opening generation for sync: %w", err)
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		return fmt.Errorf("syncing generation: %w", err)
	}
	return nil
}

func isSQLiteBusy(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	code := sqliteErr.Code() & 0xff
	return code == sqlite3.SQLITE_BUSY || code == sqlite3.SQLITE_LOCKED
}
