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

// Ingest parses the GTFS zip-of-zips at zipPath into the store. Existing data
// is cleared first.
func Ingest(ctx context.Context, store *Store, zipPath string, progress func(string)) error {
	if progress == nil {
		progress = func(string) {}
	}

	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("opening GTFS archive: %w", err)
	}
	defer zr.Close()

	if err := store.clear(); err != nil {
		return err
	}

	tx, err := store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

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
	rc, err := f.Open()
	if err != nil {
		return err
	}
	data, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
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
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	r := csv.NewReader(rc)
	r.ReuseRecord = true
	r.FieldsPerRecord = -1
	return fn(r)
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
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		lat, _ := strconv.ParseFloat(get(row, idx, "stop_lat"), 64)
		lon, _ := strconv.ParseFloat(get(row, idx, "stop_lon"), 64)
		parent := get(row, idx, "parent_station")
		if parent != "" {
			parent = prefix(feedMode, parent)
		}
		if _, err := stmt.Exec(prefix(feedMode, get(row, idx, "stop_id")), get(row, idx, "stop_name"), lat, lon, parent); err != nil {
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
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		rtype, _ := strconv.Atoi(get(row, idx, "route_type"))
		if _, err := stmt.Exec(prefix(feedMode, get(row, idx, "route_id")), get(row, idx, "route_short_name"), get(row, idx, "route_long_name"), rtype, feedMode); err != nil {
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
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		dir, _ := strconv.Atoi(get(row, idx, "direction_id"))
		if _, err := stmt.Exec(prefix(feedMode, get(row, idx, "trip_id")), prefix(feedMode, get(row, idx, "route_id")), prefix(feedMode, get(row, idx, "service_id")), get(row, idx, "trip_headsign"), dir); err != nil {
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
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		seq, _ := strconv.Atoi(get(row, idx, "stop_sequence"))
		arr, _ := parseGTFSTime(get(row, idx, "arrival_time"))
		dep, _ := parseGTFSTime(get(row, idx, "departure_time"))
		if _, err := stmt.Exec(prefix(feedMode, get(row, idx, "trip_id")), prefix(feedMode, get(row, idx, "stop_id")), seq, arr, dep); err != nil {
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
	atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }
	for {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if _, err := stmt.Exec(
			prefix(feedMode, get(row, idx, "service_id")),
			atoi(get(row, idx, "monday")), atoi(get(row, idx, "tuesday")), atoi(get(row, idx, "wednesday")),
			atoi(get(row, idx, "thursday")), atoi(get(row, idx, "friday")), atoi(get(row, idx, "saturday")),
			atoi(get(row, idx, "sunday")), get(row, idx, "start_date"), get(row, idx, "end_date")); err != nil {
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
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		ex, _ := strconv.Atoi(get(row, idx, "exception_type"))
		if _, err := stmt.Exec(prefix(feedMode, get(row, idx, "service_id")), get(row, idx, "date"), ex); err != nil {
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
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		ttype, _ := strconv.Atoi(get(row, idx, "transfer_type"))
		mt, _ := strconv.Atoi(get(row, idx, "min_transfer_time"))
		if _, err := stmt.Exec(prefix(feedMode, get(row, idx, "from_stop_id")), prefix(feedMode, get(row, idx, "to_stop_id")), ttype, mt); err != nil {
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

// clear removes all ingested rows.
func (s *Store) clear() error {
	for _, t := range []string{"stops", "routes", "trips", "stop_times", "calendar", "calendar_dates", "transfers", "meta"} {
		if _, err := s.db.Exec("DELETE FROM " + t); err != nil {
			return err
		}
	}
	return nil
}
