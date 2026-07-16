package gtfs

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	maxOuterZipEntries = 100
	maxInnerZipEntries = 64
	maxInnerZipBytes   = int64(256 << 20)
	maxCSVEntryBytes   = int64(2 << 30)
	maxGTFSSeconds     = 72 * 60 * 60
	maxTransferMeters  = 250.0
	walkMetersPerSec   = 1.3
)

var supportedFeedModes = map[int]string{
	1:  "Regional Train",
	2:  "Metropolitan Train",
	3:  "Metropolitan Tram",
	4:  "myki Bus",
	5:  "Regional Coach",
	6:  "Regional Bus",
	10: "Interstate Train",
	11: "SkyBus",
}

// IngestGenerationOptions supplies the identity and upstream evidence that
// must be recorded before a staging store can be published by GenerationManager.
type IngestGenerationOptions struct {
	GenerationID string
	Provenance   FeedProvenance
	IngestedAt   time.Time
	Progress     func(string)
}

// IngestGeneration compiles a Transport Victoria zip-of-zips into an empty
// schema-v2 staging store and records exact checked counts and coverage. A0's
// Publish performs the final resolved-identity and integrity validation.
func IngestGeneration(ctx context.Context, store *Store, zipPath string, options IngestGenerationOptions) (DatasetState, error) {
	if options.GenerationID == "" {
		return DatasetState{}, errors.New("GTFS generation id is empty")
	}
	if options.Provenance.SourceURL == "" {
		return DatasetState{}, errors.New("GTFS source URL is empty")
	}
	if options.IngestedAt.IsZero() {
		options.IngestedAt = time.Now().UTC()
	}
	if options.Provenance.PublicationTime.IsZero() && options.Provenance.LastModified != "" {
		if published, err := http.ParseTime(options.Provenance.LastModified); err == nil {
			options.Provenance.PublicationTime = published.UTC()
		}
	}
	coverage, err := compileArchive(ctx, store, zipPath, options.Progress)
	if err != nil {
		return DatasetState{}, err
	}
	counts, err := store.ComputeDatasetCounts(ctx)
	if err != nil {
		return DatasetState{}, err
	}
	state := DatasetState{
		GenerationID: options.GenerationID,
		Provenance:   options.Provenance,
		IngestedAt:   options.IngestedAt.UTC(),
		Coverage:     coverage,
		Counts:       counts,
	}
	if err := store.SaveDatasetState(ctx, state); err != nil {
		return DatasetState{}, err
	}
	return state, nil
}

// Ingest preserves the legacy API for callers not yet using immutable
// generations. It compiles the same checked schema but records only the legacy
// ingest marker; a compatibility import is deliberately not publishable.
func Ingest(ctx context.Context, store *Store, zipPath string, progress func(string)) error {
	if _, err := compileArchive(ctx, store, zipPath, progress); err != nil {
		return err
	}
	if _, err := store.db.ExecContext(ctx, indexes); err != nil {
		return fmt.Errorf("creating GTFS indexes: %w", err)
	}
	return nil
}

type outerFeed struct {
	entry *zip.File
	mode  int
	name  string
}

type feedCompileStats struct {
	stops       int64
	routes      int64
	services    int64
	trips       int64
	stopTimes   int64
	connections int64
}

func compileArchive(ctx context.Context, store *Store, zipPath string, progress func(string)) (ServiceCoverage, error) {
	if store == nil || store.db == nil {
		return ServiceCoverage{}, errors.New("GTFS store is nil")
	}
	if store.readOnly {
		return ServiceCoverage{}, errors.New("cannot ingest into a read-only GTFS generation")
	}
	if progress == nil {
		progress = func(string) {}
	}
	outer, err := zip.OpenReader(zipPath)
	if err != nil {
		return ServiceCoverage{}, fmt.Errorf("opening GTFS archive: %w", err)
	}
	defer outer.Close()
	if len(outer.File) == 0 {
		return ServiceCoverage{}, errors.New("GTFS archive is empty")
	}
	if len(outer.File) > maxOuterZipEntries {
		return ServiceCoverage{}, fmt.Errorf("GTFS archive has too many entries: %d exceeds %d", len(outer.File), maxOuterZipEntries)
	}
	feeds, err := discoverOuterFeeds(outer.File)
	if err != nil {
		return ServiceCoverage{}, err
	}
	if len(feeds) == 0 {
		return ServiceCoverage{}, errors.New("GTFS archive contains no recognized Transport Victoria feeds")
	}
	sort.Slice(feeds, func(i, j int) bool { return feeds[i].mode < feeds[j].mode })

	tempDir, err := os.MkdirTemp(filepath.Dir(store.Path()), ".gtfs-inner-*")
	if err != nil {
		return ServiceCoverage{}, fmt.Errorf("creating private GTFS spool directory: %w", err)
	}
	defer os.RemoveAll(tempDir)
	if err := os.Chmod(tempDir, 0o700); err != nil {
		return ServiceCoverage{}, fmt.Errorf("securing private GTFS spool directory: %w", err)
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return ServiceCoverage{}, fmt.Errorf("beginning GTFS compilation: %w", err)
	}
	defer tx.Rollback()
	if err := clearTx(ctx, tx); err != nil {
		return ServiceCoverage{}, err
	}
	for _, feed := range feeds {
		if err := ctx.Err(); err != nil {
			return ServiceCoverage{}, err
		}
		progress(fmt.Sprintf("ingesting feed %s", feed.name))
		path, err := spoolInnerArchive(ctx, feed.entry, tempDir)
		if err != nil {
			return ServiceCoverage{}, fmt.Errorf("feed %s: %w", feed.name, err)
		}
		stats, compileErr := compileInnerArchive(ctx, tx, path, feed)
		_ = os.Remove(path)
		if compileErr != nil {
			return ServiceCoverage{}, fmt.Errorf("feed %s: %w", feed.name, compileErr)
		}
		if stats.stops == 0 || stats.routes == 0 || stats.services == 0 || stats.trips == 0 || stats.stopTimes == 0 || stats.connections == 0 {
			return ServiceCoverage{}, fmt.Errorf("feed %s has zero core rows: %+v", feed.name, stats)
		}
	}
	progress("materializing transfer and walking graph")
	if err := materializeTripLinks(ctx, tx); err != nil {
		return ServiceCoverage{}, err
	}
	if err := generateProximityTransfers(ctx, tx); err != nil {
		return ServiceCoverage{}, fmt.Errorf("generating proximity transfers: %w", err)
	}
	coverage, err := coverageTx(ctx, tx)
	if err != nil {
		return ServiceCoverage{}, err
	}
	if err := setMetaTx(ctx, tx, "ingested_at", time.Now().UTC().Format(time.RFC3339)); err != nil {
		return ServiceCoverage{}, err
	}
	if err := tx.Commit(); err != nil {
		return ServiceCoverage{}, fmt.Errorf("committing GTFS compilation: %w", err)
	}
	return coverage, nil
}

func discoverOuterFeeds(entries []*zip.File) ([]outerFeed, error) {
	seen := make(map[int]bool)
	var feeds []outerFeed
	for _, entry := range entries {
		name := strings.ReplaceAll(entry.Name, "\\", "/")
		if !strings.EqualFold(filepath.Base(name), "google_transit.zip") {
			continue
		}
		mode := feedModeFromName(name)
		label, supported := supportedFeedModes[mode]
		if !supported {
			continue
		}
		if seen[mode] {
			return nil, fmt.Errorf("GTFS archive contains duplicate feed mode %d", mode)
		}
		seen[mode] = true
		feeds = append(feeds, outerFeed{entry: entry, mode: mode, name: label})
	}
	return feeds, nil
}

func spoolInnerArchive(ctx context.Context, entry *zip.File, dir string) (path string, err error) {
	if entry.UncompressedSize64 > uint64(maxInnerZipBytes) {
		return "", fmt.Errorf("inner GTFS zip too large: %d bytes exceeds %d", entry.UncompressedSize64, maxInnerZipBytes)
	}
	source, err := entry.Open()
	if err != nil {
		return "", fmt.Errorf("opening inner GTFS zip: %w", err)
	}
	defer source.Close()
	temp, err := os.CreateTemp(dir, ".feed-*.zip")
	if err != nil {
		return "", fmt.Errorf("creating inner GTFS spool: %w", err)
	}
	path = temp.Name()
	remove := true
	defer func() {
		if remove {
			_ = os.Remove(path)
		}
	}()
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return "", err
	}
	n, copyErr := copyContextLimit(ctx, temp, source, maxInnerZipBytes)
	if copyErr != nil {
		temp.Close()
		return "", copyErr
	}
	if entry.UncompressedSize64 != 0 && n != int64(entry.UncompressedSize64) {
		temp.Close()
		return "", fmt.Errorf("inner GTFS zip length mismatch: received %d, expected %d", n, entry.UncompressedSize64)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return "", fmt.Errorf("syncing inner GTFS spool: %w", err)
	}
	if err := temp.Close(); err != nil {
		return "", fmt.Errorf("closing inner GTFS spool: %w", err)
	}
	remove = false
	return path, nil
}

func copyContextLimit(ctx context.Context, dst io.Writer, src io.Reader, limit int64) (int64, error) {
	buffer := make([]byte, 128<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		remaining := limit + 1 - total
		if remaining <= 0 {
			return total, fmt.Errorf("inner GTFS zip too large: exceeded %d bytes", limit)
		}
		chunk := buffer
		if int64(len(chunk)) > remaining {
			chunk = chunk[:remaining]
		}
		n, readErr := src.Read(chunk)
		if n > 0 {
			written, writeErr := dst.Write(chunk[:n])
			total += int64(written)
			if writeErr != nil {
				return total, fmt.Errorf("writing inner GTFS spool: %w", writeErr)
			}
			if written != n {
				return total, io.ErrShortWrite
			}
			if total > limit {
				return total, fmt.Errorf("inner GTFS zip too large: exceeded %d bytes", limit)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return total, nil
			}
			return total, fmt.Errorf("reading inner GTFS zip: %w", readErr)
		}
	}
}

// feedModeFromName extracts a supported branch number from paths such as
// "2/google_transit.zip".
func feedModeFromName(name string) int {
	parts := strings.Split(strings.ReplaceAll(name, "\\", "/"), "/")
	if len(parts) < 2 {
		return 0
	}
	mode, err := strconv.Atoi(parts[len(parts)-2])
	if err != nil {
		return 0
	}
	return mode
}

func prefix(feedMode int, id string) string { return strconv.Itoa(feedMode) + ":" + id }

func withCSV(file *zip.File, fn func(*csv.Reader) error) error {
	if file.UncompressedSize64 > uint64(maxCSVEntryBytes) {
		return fmt.Errorf("CSV entry too large: %d bytes exceeds %d", file.UncompressedSize64, maxCSVEntryBytes)
	}
	raw, err := file.Open()
	if err != nil {
		return err
	}
	defer raw.Close()
	reader := csv.NewReader(&byteLimitReader{r: raw, limit: maxCSVEntryBytes})
	reader.ReuseRecord = true
	reader.FieldsPerRecord = -1
	return fn(reader)
}

type byteLimitReader struct {
	r     io.Reader
	limit int64
	read  int64
}

func (r *byteLimitReader) Read(p []byte) (int, error) {
	remaining := r.limit - r.read
	if remaining <= 0 {
		var probe [1]byte
		n, err := r.r.Read(probe[:])
		if n > 0 {
			return 0, fmt.Errorf("CSV entry too large: exceeded %d bytes", r.limit)
		}
		return 0, err
	}
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := r.r.Read(p)
	r.read += int64(n)
	return n, err
}

func headerIndex(reader *csv.Reader, required ...string) (map[string]int, error) {
	row, err := reader.Read()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("CSV is empty")
		}
		return nil, err
	}
	index := make(map[string]int, len(row))
	for i, name := range row {
		name = strings.TrimSpace(strings.TrimPrefix(name, "\ufeff"))
		if name == "" {
			continue
		}
		if _, duplicate := index[name]; duplicate {
			return nil, fmt.Errorf("duplicate CSV header %q", name)
		}
		index[name] = i
	}
	for _, name := range required {
		if _, ok := index[name]; !ok {
			return nil, fmt.Errorf("missing required header %q", name)
		}
	}
	return index, nil
}

func get(row []string, index map[string]int, name string) string {
	i, ok := index[name]
	if !ok || i >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[i])
}

func parseRequiredInt(row []string, index map[string]int, name string) (int, error) {
	value := get(row, index, name)
	if value == "" {
		return 0, fmt.Errorf("missing %s", name)
	}
	number, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", name, err)
	}
	return number, nil
}

func parseOptionalInt(row []string, index map[string]int, name string, fallback int) (int, error) {
	value := get(row, index, name)
	if value == "" {
		return fallback, nil
	}
	number, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", name, err)
	}
	return number, nil
}

func parseOptionalFloat(row []string, index map[string]int, name string) (sql.NullFloat64, error) {
	value := get(row, index, name)
	if value == "" {
		return sql.NullFloat64{}, nil
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return sql.NullFloat64{}, fmt.Errorf("invalid %s: %w", name, err)
	}
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return sql.NullFloat64{}, fmt.Errorf("invalid %s %q", name, value)
	}
	return sql.NullFloat64{Float64: number, Valid: true}, nil
}

func parseGTFSDate(value string) (string, error) {
	if _, err := time.Parse("20060102", value); err != nil {
		return "", fmt.Errorf("invalid GTFS date %q: %w", value, err)
	}
	return value, nil
}

func clearTx(ctx context.Context, tx *sql.Tx) error {
	for _, table := range []string{
		"trip_links", "connections", "pathways", "levels", "transfers",
		"stop_times", "trips", "calendar_dates", "calendar", "routes",
		"stops", "feeds", "dataset_state", "meta",
	} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			return fmt.Errorf("clearing %s: %w", table, err)
		}
	}
	return nil
}

func setMetaTx(ctx context.Context, tx *sql.Tx, key, value string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func coverageTx(ctx context.Context, tx *sql.Tx) (ServiceCoverage, error) {
	var start, end sql.NullString
	err := tx.QueryRowContext(ctx, `
		SELECT MIN(day), MAX(day) FROM (
			SELECT start_date AS day FROM calendar
			WHERE start_date != '' AND (monday+tuesday+wednesday+thursday+friday+saturday+sunday)>0
			UNION ALL SELECT end_date FROM calendar
			WHERE end_date != '' AND (monday+tuesday+wednesday+thursday+friday+saturday+sunday)>0
			UNION ALL SELECT date FROM calendar_dates WHERE date != '' AND exception_type=1
		)`).Scan(&start, &end)
	if err != nil {
		return ServiceCoverage{}, fmt.Errorf("computing GTFS coverage: %w", err)
	}
	if !start.Valid || !end.Valid {
		return ServiceCoverage{}, errors.New("GTFS feed has no service coverage")
	}
	return ServiceCoverage{Start: start.String, End: end.String}, nil
}
