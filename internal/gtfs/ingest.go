package gtfs

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
)

// walk transfer generation parameters.
const (
	maxTransferMeters = 250.0 // connect stops within this distance
	walkMetersPerSec  = 1.3   // ~4.7 km/h
	gridDegrees       = 0.0025
)

const (
	maxOuterZipEntries = 100
	maxInnerZipEntries = 32
	maxInnerZipBytes   = int64(256 << 20)
	maxCSVEntryBytes   = int64(2 << 30)
)

// Ingest parses the GTFS zip-of-zips at zipPath into the store. Existing data
// is cleared inside the ingest transaction.
func Ingest(ctx context.Context, store *Store, zipPath string, progress func(string)) error {
	if progress == nil {
		progress = func(string) {}
	}

	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("opening GTFS archive: %w", err)
	}
	defer zr.Close()
	if len(zr.File) > maxOuterZipEntries {
		return fmt.Errorf("GTFS archive has too many entries: %d exceeds %d", len(zr.File), maxOuterZipEntries)
	}

	tx, err := store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := clearTx(tx); err != nil {
		return err
	}

	for _, f := range zr.File {
		if !strings.HasSuffix(strings.ToLower(f.Name), "google_transit.zip") {
			continue
		}
		feedMode := feedModeFromName(f.Name)
		progress(fmt.Sprintf("ingesting feed %s", f.Name))
		if err := ingestInnerZip(ctx, tx, f, feedMode); err != nil {
			return fmt.Errorf("feed %s: %w", f.Name, err)
		}
	}

	progress("generating walking transfers")
	if err := generateFootTransfers(tx); err != nil {
		return fmt.Errorf("generating transfers: %w", err)
	}

	if err := store.setMeta(tx, "ingested_at", time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	progress("building indexes")
	if _, err := store.db.Exec(indexes); err != nil {
		return fmt.Errorf("creating indexes: %w", err)
	}
	return nil
}

// feedModeFromName extracts the leading feed number from a path like
// "2/google_transit.zip".
func feedModeFromName(name string) int {
	name = strings.ReplaceAll(name, "\\", "/")
	parts := strings.Split(name, "/")
	if len(parts) >= 2 {
		if n, err := strconv.Atoi(parts[0]); err == nil {
			return n
		}
	}
	return 0
}

// prefix namespaces an id by feed mode to avoid cross-feed collisions.
func prefix(feedMode int, id string) string {
	return strconv.Itoa(feedMode) + ":" + id
}

func ingestInnerZip(ctx context.Context, tx *sql.Tx, f *zip.File, feedMode int) error {
	if f.UncompressedSize64 > uint64(maxInnerZipBytes) {
		return fmt.Errorf("inner GTFS zip too large: %d bytes exceeds %d", f.UncompressedSize64, maxInnerZipBytes)
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(rc, maxInnerZipBytes+1))
	rc.Close()
	if err != nil {
		return err
	}
	if int64(len(data)) > maxInnerZipBytes {
		return fmt.Errorf("inner GTFS zip too large: exceeded %d bytes", maxInnerZipBytes)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	if len(zr.File) > maxInnerZipEntries {
		return fmt.Errorf("inner GTFS zip has too many entries: %d exceeds %d", len(zr.File), maxInnerZipEntries)
	}

	for _, inner := range zr.File {
		base := strings.ToLower(inner.Name)
		var handler func(context.Context, *sql.Tx, *csv.Reader, int) error
		switch base {
		case "stops.txt":
			handler = ingestStops
		case "routes.txt":
			handler = ingestRoutes
		case "trips.txt":
			handler = ingestTrips
		case "stop_times.txt":
			handler = ingestStopTimes
		case "calendar.txt":
			handler = ingestCalendar
		case "calendar_dates.txt":
			handler = ingestCalendarDates
		case "transfers.txt":
			handler = ingestTransfers
		default:
			continue
		}
		if err := withCSV(inner, func(r *csv.Reader) error {
			return handler(ctx, tx, r, feedMode)
		}); err != nil {
			return fmt.Errorf("%s: %w", base, err)
		}
	}
	return nil
}

// withCSV opens a zip entry and invokes fn with a configured CSV reader.
func withCSV(f *zip.File, fn func(*csv.Reader) error) error {
	if f.UncompressedSize64 > uint64(maxCSVEntryBytes) {
		return fmt.Errorf("CSV entry too large: %d bytes exceeds %d", f.UncompressedSize64, maxCSVEntryBytes)
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	r := csv.NewReader(&byteLimitReader{r: rc, limit: maxCSVEntryBytes})
	r.ReuseRecord = true
	r.FieldsPerRecord = -1
	return fn(r)
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

// headerIndex reads the header row and returns a name→index map.
func headerIndex(r *csv.Reader) (map[string]int, error) {
	row, err := r.Read()
	if err != nil {
		return nil, err
	}
	idx := make(map[string]int, len(row))
	for i, name := range row {
		idx[strings.TrimSpace(strings.TrimPrefix(name, "\ufeff"))] = i
	}
	return idx, nil
}

// get safely returns the column value or "".
func get(row []string, idx map[string]int, name string) string {
	i, ok := idx[name]
	if !ok || i >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[i])
}

func ingestStops(ctx context.Context, tx *sql.Tx, r *csv.Reader, feedMode int) error {
	idx, err := headerIndex(r)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO stops(stop_id,stop_name,stop_lat,stop_lon,parent_station) VALUES(?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		stopID := get(row, idx, "stop_id")
		if stopID == "" {
			return fmt.Errorf("missing stop_id")
		}
		lat, err := parseRequiredFloat(row, idx, "stop_lat")
		if err != nil {
			return fmt.Errorf("stop %s: %w", stopID, err)
		}
		lon, err := parseRequiredFloat(row, idx, "stop_lon")
		if err != nil {
			return fmt.Errorf("stop %s: %w", stopID, err)
		}
		parent := get(row, idx, "parent_station")
		if parent != "" {
			parent = prefix(feedMode, parent)
		}
		if _, err := stmt.Exec(prefix(feedMode, stopID), get(row, idx, "stop_name"), lat, lon, parent); err != nil {
			return err
		}
	}
	return nil
}

func ingestRoutes(ctx context.Context, tx *sql.Tx, r *csv.Reader, feedMode int) error {
	idx, err := headerIndex(r)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO routes(route_id,route_short_name,route_long_name,route_type,feed_mode) VALUES(?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		routeID := get(row, idx, "route_id")
		if routeID == "" {
			return fmt.Errorf("missing route_id")
		}
		rtype, err := parseRequiredInt(row, idx, "route_type")
		if err != nil {
			return fmt.Errorf("route %s: %w", routeID, err)
		}
		if _, err := stmt.Exec(prefix(feedMode, routeID), get(row, idx, "route_short_name"), get(row, idx, "route_long_name"), rtype, feedMode); err != nil {
			return err
		}
	}
	return nil
}

func ingestTrips(ctx context.Context, tx *sql.Tx, r *csv.Reader, feedMode int) error {
	idx, err := headerIndex(r)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO trips(trip_id,route_id,service_id,trip_headsign,direction_id) VALUES(?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		tripID := get(row, idx, "trip_id")
		routeID := get(row, idx, "route_id")
		serviceID := get(row, idx, "service_id")
		if tripID == "" || routeID == "" || serviceID == "" {
			return fmt.Errorf("trip row missing trip_id, route_id, or service_id")
		}
		dir, err := parseOptionalInt(row, idx, "direction_id", 0)
		if err != nil {
			return fmt.Errorf("trip %s: %w", tripID, err)
		}
		if _, err := stmt.Exec(prefix(feedMode, tripID), prefix(feedMode, routeID), prefix(feedMode, serviceID), get(row, idx, "trip_headsign"), dir); err != nil {
			return err
		}
	}
	return nil
}

func ingestStopTimes(ctx context.Context, tx *sql.Tx, r *csv.Reader, feedMode int) error {
	idx, err := headerIndex(r)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO stop_times(trip_id,stop_id,stop_sequence,arrival_sec,departure_sec) VALUES(?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		tripID := get(row, idx, "trip_id")
		stopID := get(row, idx, "stop_id")
		if tripID == "" || stopID == "" {
			return fmt.Errorf("stop_times row missing trip_id or stop_id")
		}
		seq, err := parseRequiredInt(row, idx, "stop_sequence")
		if err != nil {
			return fmt.Errorf("trip %s stop %s: %w", tripID, stopID, err)
		}
		arr, ok := parseGTFSTime(get(row, idx, "arrival_time"))
		if !ok {
			return fmt.Errorf("trip %s stop %s: invalid arrival_time", tripID, stopID)
		}
		dep, ok := parseGTFSTime(get(row, idx, "departure_time"))
		if !ok {
			return fmt.Errorf("trip %s stop %s: invalid departure_time", tripID, stopID)
		}
		if _, err := stmt.Exec(prefix(feedMode, tripID), prefix(feedMode, stopID), seq, arr, dep); err != nil {
			return err
		}
	}
	return nil
}

func ingestCalendar(ctx context.Context, tx *sql.Tx, r *csv.Reader, feedMode int) error {
	idx, err := headerIndex(r)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO calendar(service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date) VALUES(?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		serviceID := get(row, idx, "service_id")
		if serviceID == "" {
			return fmt.Errorf("missing service_id")
		}
		monday, err := parseRequiredInt(row, idx, "monday")
		if err != nil {
			return fmt.Errorf("service %s: %w", serviceID, err)
		}
		tuesday, err := parseRequiredInt(row, idx, "tuesday")
		if err != nil {
			return fmt.Errorf("service %s: %w", serviceID, err)
		}
		wednesday, err := parseRequiredInt(row, idx, "wednesday")
		if err != nil {
			return fmt.Errorf("service %s: %w", serviceID, err)
		}
		thursday, err := parseRequiredInt(row, idx, "thursday")
		if err != nil {
			return fmt.Errorf("service %s: %w", serviceID, err)
		}
		friday, err := parseRequiredInt(row, idx, "friday")
		if err != nil {
			return fmt.Errorf("service %s: %w", serviceID, err)
		}
		saturday, err := parseRequiredInt(row, idx, "saturday")
		if err != nil {
			return fmt.Errorf("service %s: %w", serviceID, err)
		}
		sunday, err := parseRequiredInt(row, idx, "sunday")
		if err != nil {
			return fmt.Errorf("service %s: %w", serviceID, err)
		}
		if _, err := stmt.Exec(
			prefix(feedMode, serviceID),
			monday, tuesday, wednesday,
			thursday, friday, saturday,
			sunday, get(row, idx, "start_date"), get(row, idx, "end_date")); err != nil {
			return err
		}
	}
	return nil
}

func ingestCalendarDates(ctx context.Context, tx *sql.Tx, r *csv.Reader, feedMode int) error {
	idx, err := headerIndex(r)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO calendar_dates(service_id,date,exception_type) VALUES(?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		serviceID := get(row, idx, "service_id")
		if serviceID == "" {
			return fmt.Errorf("missing service_id")
		}
		ex, err := parseRequiredInt(row, idx, "exception_type")
		if err != nil {
			return fmt.Errorf("service %s: %w", serviceID, err)
		}
		if _, err := stmt.Exec(prefix(feedMode, serviceID), get(row, idx, "date"), ex); err != nil {
			return err
		}
	}
	return nil
}

func ingestTransfers(ctx context.Context, tx *sql.Tx, r *csv.Reader, feedMode int) error {
	idx, err := headerIndex(r)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO transfers(from_stop_id,to_stop_id,transfer_type,min_transfer_time) VALUES(?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		fromStopID := get(row, idx, "from_stop_id")
		toStopID := get(row, idx, "to_stop_id")
		if fromStopID == "" || toStopID == "" {
			return fmt.Errorf("transfer row missing from_stop_id or to_stop_id")
		}
		ttype, err := parseRequiredInt(row, idx, "transfer_type")
		if err != nil {
			return fmt.Errorf("transfer %s to %s: %w", fromStopID, toStopID, err)
		}
		mt, err := parseOptionalInt(row, idx, "min_transfer_time", 0)
		if err != nil {
			return fmt.Errorf("transfer %s to %s: %w", fromStopID, toStopID, err)
		}
		if _, err := stmt.Exec(prefix(feedMode, fromStopID), prefix(feedMode, toStopID), ttype, mt); err != nil {
			return err
		}
	}
	return nil
}

// stopGeo is a stop's coordinates for transfer generation.
type stopGeo struct {
	id  string
	lat float64
	lon float64
}

// generateFootTransfers adds symmetric walking transfers between stops that
// are within maxTransferMeters of each other (including across feeds), using a
// spatial grid to keep it tractable.
func generateFootTransfers(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT stop_id, stop_lat, stop_lon FROM stops WHERE stop_lat != 0 AND stop_lon != 0`)
	if err != nil {
		return err
	}
	var stops []stopGeo
	grid := map[[2]int][]int{}
	for rows.Next() {
		var s stopGeo
		if err := rows.Scan(&s.id, &s.lat, &s.lon); err != nil {
			rows.Close()
			return err
		}
		i := len(stops)
		stops = append(stops, s)
		key := [2]int{int(math.Floor(s.lat / gridDegrees)), int(math.Floor(s.lon / gridDegrees))}
		grid[key] = append(grid[key], i)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	stmt, err := tx.Prepare(`INSERT INTO transfers(from_stop_id,to_stop_id,transfer_type,min_transfer_time) VALUES(?,?,2,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for i, s := range stops {
		gi := int(math.Floor(s.lat / gridDegrees))
		gj := int(math.Floor(s.lon / gridDegrees))
		for di := -1; di <= 1; di++ {
			for dj := -1; dj <= 1; dj++ {
				for _, j := range grid[[2]int{gi + di, gj + dj}] {
					if j <= i {
						continue
					}
					o := stops[j]
					d := haversine(s.lat, s.lon, o.lat, o.lon)
					if d > maxTransferMeters {
						continue
					}
					secs := int(d/walkMetersPerSec) + 1
					if _, err := stmt.Exec(s.id, o.id, secs); err != nil {
						return err
					}
					if _, err := stmt.Exec(o.id, s.id, secs); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// haversine returns the great-circle distance in metres.
func haversine(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371000.0
	p1 := lat1 * math.Pi / 180
	p2 := lat2 * math.Pi / 180
	dp := (lat2 - lat1) * math.Pi / 180
	dl := (lon2 - lon1) * math.Pi / 180
	a := math.Sin(dp/2)*math.Sin(dp/2) + math.Cos(p1)*math.Cos(p2)*math.Sin(dl/2)*math.Sin(dl/2)
	return r * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

func parseRequiredFloat(row []string, idx map[string]int, name string) (float64, error) {
	v := get(row, idx, name)
	if v == "" {
		return 0, fmt.Errorf("missing %s", name)
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", name, err)
	}
	return n, nil
}

func parseRequiredInt(row []string, idx map[string]int, name string) (int, error) {
	v := get(row, idx, name)
	if v == "" {
		return 0, fmt.Errorf("missing %s", name)
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", name, err)
	}
	return n, nil
}

func parseOptionalInt(row []string, idx map[string]int, name string, fallback int) (int, error) {
	v := get(row, idx, name)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", name, err)
	}
	return n, nil
}

func clearTx(tx *sql.Tx) error {
	for _, t := range []string{"stops", "routes", "trips", "stop_times", "calendar", "calendar_dates", "transfers", "meta"} {
		if _, err := tx.Exec("DELETE FROM " + t); err != nil {
			return err
		}
	}
	return nil
}
