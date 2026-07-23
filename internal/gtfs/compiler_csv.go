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
	"path/filepath"
	"strings"
)

type compiledStop struct {
	key          int64
	locationType int
	parentSource string
	levelSource  string
	stopAccess   *int64
}

type compiledTrip struct {
	key        int64
	routeKey   int64
	serviceKey int64
}

type feedCompiler struct {
	feedKey                int64
	mode                   int
	files                  map[string]*zip.File
	stops                  map[string]compiledStop
	routes                 map[string]int64
	services               map[string]int64
	calendarServiceIDs     map[string]struct{}
	trips                  map[string]compiledTrip
	levels                 map[string]int64
	duplicateTrips         int64
	nonIncreasingSegments  int64
}

func compileInnerArchive(ctx context.Context, tx *sql.Tx, path string, feed outerFeed) (feedCompileStats, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return feedCompileStats{}, fmt.Errorf("opening spooled GTFS feed: %w", err)
	}
	defer reader.Close()
	if len(reader.File) == 0 {
		return feedCompileStats{}, errors.New("inner GTFS archive is empty")
	}
	if len(reader.File) > maxInnerZipEntries {
		return feedCompileStats{}, fmt.Errorf("inner GTFS archive has too many entries: %d exceeds %d", len(reader.File), maxInnerZipEntries)
	}
	files := make(map[string]*zip.File, len(reader.File))
	for _, file := range reader.File {
		name := strings.ToLower(filepath.Base(strings.ReplaceAll(file.Name, "\\", "/")))
		if name == "." || name == "" || strings.HasSuffix(file.Name, "/") {
			continue
		}
		if _, duplicate := files[name]; duplicate {
			return feedCompileStats{}, fmt.Errorf("duplicate inner GTFS file %s", name)
		}
		files[name] = file
	}
	for _, name := range []string{"stops.txt", "routes.txt", "trips.txt", "stop_times.txt"} {
		if files[name] == nil {
			return feedCompileStats{}, fmt.Errorf("missing required GTFS file %s", name)
		}
	}
	if files["calendar.txt"] == nil && files["calendar_dates.txt"] == nil {
		return feedCompileStats{}, errors.New("feed requires calendar.txt or calendar_dates.txt")
	}

	result, err := tx.ExecContext(ctx, `INSERT INTO feeds(feed_mode,source_namespace,source_name) VALUES(?,?,?)`,
		feed.mode, fmt.Sprintf("transport-victoria:%d", feed.mode), feed.name)
	if err != nil {
		return feedCompileStats{}, fmt.Errorf("inserting feed namespace: %w", err)
	}
	feedKey, err := result.LastInsertId()
	if err != nil {
		return feedCompileStats{}, fmt.Errorf("reading feed key: %w", err)
	}
	compiler := &feedCompiler{
		feedKey: feedKey, mode: feed.mode, files: files,
		stops:              make(map[string]compiledStop),
		routes:             make(map[string]int64),
		services:           make(map[string]int64),
		calendarServiceIDs: make(map[string]struct{}),
		trips:              make(map[string]compiledTrip),
		levels:             make(map[string]int64),
	}
	var stats feedCompileStats
	if file := files["levels.txt"]; file != nil {
		if _, err := compiler.compileLevels(ctx, tx, file); err != nil {
			return stats, fmt.Errorf("levels.txt: %w", err)
		}
	}
	if stats.stops, err = compiler.compileStops(ctx, tx, files["stops.txt"]); err != nil {
		return stats, fmt.Errorf("stops.txt: %w", err)
	}
	if stats.routes, err = compiler.compileRoutes(ctx, tx, files["routes.txt"]); err != nil {
		return stats, fmt.Errorf("routes.txt: %w", err)
	}
	if file := files["calendar.txt"]; file != nil {
		if _, err := compiler.compileCalendar(ctx, tx, file); err != nil {
			return stats, fmt.Errorf("calendar.txt: %w", err)
		}
	}
	if file := files["calendar_dates.txt"]; file != nil {
		if _, err := compiler.compileCalendarDates(ctx, tx, file); err != nil {
			return stats, fmt.Errorf("calendar_dates.txt: %w", err)
		}
	}
	stats.services = int64(len(compiler.services))
	if stats.trips, err = compiler.compileTrips(ctx, tx, files["trips.txt"]); err != nil {
		return stats, fmt.Errorf("trips.txt: %w", err)
	}
	stats.duplicateTrips = compiler.duplicateTrips
	if stats.stopTimes, err = compiler.compileStopTimes(ctx, tx, files["stop_times.txt"]); err != nil {
		return stats, fmt.Errorf("stop_times.txt: %w", err)
	}
	if file := files["transfers.txt"]; file != nil {
		if _, err := compiler.compileTransfers(ctx, tx, file); err != nil {
			return stats, fmt.Errorf("transfers.txt: %w", err)
		}
	}
	if file := files["pathways.txt"]; file != nil {
		if _, err := compiler.compilePathways(ctx, tx, file); err != nil {
			return stats, fmt.Errorf("pathways.txt: %w", err)
		}
	}
	if stats.connections, err = compiler.materializeConnections(ctx, tx); err != nil {
		return stats, err
	}
	stats.nonIncreasingSegments = compiler.nonIncreasingSegments
	return stats, nil
}

func readCSV(ctx context.Context, file *zip.File, required []string, row func(map[string]int, []string) error) (int64, error) {
	var count int64
	err := withCSV(file, func(reader *csv.Reader) error {
		index, err := headerIndex(reader, required...)
		if err != nil {
			return err
		}
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			record, err := reader.Read()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
			if len(record) == 1 && strings.TrimSpace(record[0]) == "" {
				continue
			}
			if err := row(index, record); err != nil {
				return fmt.Errorf("row %d: %w", count+2, err)
			}
			count++
		}
	})
	return count, err
}

func (c *feedCompiler) compileLevels(ctx context.Context, tx *sql.Tx, file *zip.File) (int64, error) {
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO levels(feed_key,feed_mode,source_level_id,level_id,level_index,level_name) VALUES(?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	return readCSV(ctx, file, []string{"level_id", "level_index"}, func(index map[string]int, row []string) error {
		id := get(row, index, "level_id")
		if id == "" {
			return errors.New("missing level_id")
		}
		if _, duplicate := c.levels[id]; duplicate {
			return fmt.Errorf("duplicate level_id %q", id)
		}
		levelIndex, err := parseOptionalFloat(row, index, "level_index")
		if err != nil || !levelIndex.Valid {
			return errors.New("missing or invalid level_index")
		}
		result, err := stmt.ExecContext(ctx, c.feedKey, c.mode, id, prefix(c.mode, id), levelIndex.Float64, get(row, index, "level_name"))
		if err != nil {
			return err
		}
		key, err := result.LastInsertId()
		if err != nil {
			return err
		}
		c.levels[id] = key
		return nil
	})
}

func (c *feedCompiler) compileStops(ctx context.Context, tx *sql.Tx, file *zip.File) (int64, error) {
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO stops(
		feed_key,feed_mode,source_stop_id,stop_id,stop_name,stop_lat,stop_lon,
		parent_station,location_type,level_id,wheelchair_boarding,stop_access
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	count, err := readCSV(ctx, file, []string{"stop_id", "stop_name", "stop_lat", "stop_lon"}, func(index map[string]int, row []string) error {
		id := get(row, index, "stop_id")
		if id == "" {
			return errors.New("missing stop_id")
		}
		if _, duplicate := c.stops[id]; duplicate {
			return fmt.Errorf("duplicate stop_id %q", id)
		}
		locationType, err := parseOptionalInt(row, index, "location_type", 0)
		if err != nil || locationType < 0 || locationType > 4 {
			return fmt.Errorf("invalid location_type %q", get(row, index, "location_type"))
		}
		if locationType <= 2 && get(row, index, "stop_name") == "" {
			return fmt.Errorf("location_type %d requires stop_name", locationType)
		}
		lat, err := parseOptionalFloat(row, index, "stop_lat")
		if err != nil {
			return err
		}
		lon, err := parseOptionalFloat(row, index, "stop_lon")
		if err != nil {
			return err
		}
		if lat.Valid && (math.IsNaN(lat.Float64) || math.IsInf(lat.Float64, 0) || lat.Float64 < -90 || lat.Float64 > 90) {
			return fmt.Errorf("invalid stop_lat %q", get(row, index, "stop_lat"))
		}
		if lon.Valid && (math.IsNaN(lon.Float64) || math.IsInf(lon.Float64, 0) || lon.Float64 < -180 || lon.Float64 > 180) {
			return fmt.Errorf("invalid stop_lon %q", get(row, index, "stop_lon"))
		}
		if (lat.Valid != lon.Valid) || (locationType <= 2 && (!lat.Valid || !lon.Valid)) {
			return errors.New("stop_lat and stop_lon must both be present for stops, stations, and entrances")
		}
		wheelchair, err := parseOptionalInt(row, index, "wheelchair_boarding", 0)
		if err != nil || wheelchair < 0 || wheelchair > 2 {
			return fmt.Errorf("invalid wheelchair_boarding %q", get(row, index, "wheelchair_boarding"))
		}
		parent := get(row, index, "parent_station")
		level := get(row, index, "level_id")
		stopAccess, err := optionalInt(row, index, "stop_access")
		if err != nil || (stopAccess != nil && *stopAccess != 0 && *stopAccess != 1) {
			return fmt.Errorf("invalid stop_access %q", get(row, index, "stop_access"))
		}
		if stopAccess != nil && (locationType != 0 || parent == "") {
			return errors.New("stop_access is allowed only on a platform with parent_station")
		}
		parentID := ""
		if parent != "" {
			parentID = prefix(c.mode, parent)
		}
		levelID := ""
		if level != "" {
			levelID = prefix(c.mode, level)
		}
		result, err := stmt.ExecContext(ctx, c.feedKey, c.mode, id, prefix(c.mode, id), get(row, index, "stop_name"),
			nullFloatValue(lat), nullFloatValue(lon), parentID, locationType, levelID, wheelchair, nullableInt(stopAccess))
		if err != nil {
			return err
		}
		key, err := result.LastInsertId()
		if err != nil {
			return err
		}
		c.stops[id] = compiledStop{
			key: key, locationType: locationType, parentSource: parent,
			levelSource: level, stopAccess: stopAccess,
		}
		return nil
	})
	if err != nil {
		return count, err
	}
	resolve, err := tx.PrepareContext(ctx, `UPDATE stops SET parent_stop_key=?, level_key=? WHERE stop_key=?`)
	if err != nil {
		return count, err
	}
	defer resolve.Close()
	for sourceID, stop := range c.stops {
		var parentKey, levelKey any
		if stop.parentSource != "" {
			parent, ok := c.stops[stop.parentSource]
			if !ok {
				return count, fmt.Errorf("stop %s references unknown parent_station %s", sourceID, stop.parentSource)
			}
			switch stop.locationType {
			case 0, 2, 3:
				if parent.locationType != 1 {
					return count, fmt.Errorf("stop %s location_type %d requires a station parent", sourceID, stop.locationType)
				}
			case 1:
				return count, fmt.Errorf("station %s must not define parent_station", sourceID)
			case 4:
				if parent.locationType != 0 {
					return count, fmt.Errorf("boarding area %s requires a platform parent", sourceID)
				}
			}
			parentKey = parent.key
		} else if stop.locationType == 2 || stop.locationType == 3 || stop.locationType == 4 {
			return count, fmt.Errorf("stop %s location_type %d requires parent_station", sourceID, stop.locationType)
		}
		if stop.levelSource != "" {
			key, ok := c.levels[stop.levelSource]
			if !ok {
				return count, fmt.Errorf("stop %s references unknown level_id %s", sourceID, stop.levelSource)
			}
			levelKey = key
		}
		if _, err := resolve.ExecContext(ctx, parentKey, levelKey, stop.key); err != nil {
			return count, err
		}
	}
	return count, nil
}

func (c *feedCompiler) compileRoutes(ctx context.Context, tx *sql.Tx, file *zip.File) (int64, error) {
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO routes(feed_key,feed_mode,source_route_id,route_id,route_short_name,route_long_name,route_type) VALUES(?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	return readCSV(ctx, file, []string{"route_id", "route_type"}, func(index map[string]int, row []string) error {
		id := get(row, index, "route_id")
		if id == "" {
			return errors.New("missing route_id")
		}
		if _, duplicate := c.routes[id]; duplicate {
			return fmt.Errorf("duplicate route_id %q", id)
		}
		routeType, err := parseRequiredInt(row, index, "route_type")
		if err != nil || routeType < 0 {
			return fmt.Errorf("invalid route_type %q", get(row, index, "route_type"))
		}
		result, err := stmt.ExecContext(ctx, c.feedKey, c.mode, id, prefix(c.mode, id), get(row, index, "route_short_name"), get(row, index, "route_long_name"), routeType)
		if err != nil {
			return err
		}
		key, err := result.LastInsertId()
		if err != nil {
			return err
		}
		c.routes[id] = key
		return nil
	})
}

func (c *feedCompiler) compileCalendar(ctx context.Context, tx *sql.Tx, file *zip.File) (int64, error) {
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO calendar(
		feed_key,feed_mode,source_service_id,service_id,monday,tuesday,wednesday,
		thursday,friday,saturday,sunday,start_date,end_date
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	required := []string{"service_id", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday", "start_date", "end_date"}
	return readCSV(ctx, file, required, func(index map[string]int, row []string) error {
		id := get(row, index, "service_id")
		if id == "" {
			return errors.New("missing service_id")
		}
		if _, duplicate := c.services[id]; duplicate {
			return fmt.Errorf("duplicate service_id %q", id)
		}
		weekdays := make([]int, 7)
		for i, name := range required[1:8] {
			value, err := parseRequiredInt(row, index, name)
			if err != nil || (value != 0 && value != 1) {
				return fmt.Errorf("invalid %s", name)
			}
			weekdays[i] = value
		}
		start, err := parseGTFSDate(get(row, index, "start_date"))
		if err != nil {
			return err
		}
		end, err := parseGTFSDate(get(row, index, "end_date"))
		if err != nil {
			return err
		}
		if start > end {
			return fmt.Errorf("service %s starts after it ends", id)
		}
		result, err := stmt.ExecContext(ctx, c.feedKey, c.mode, id, prefix(c.mode, id),
			weekdays[0], weekdays[1], weekdays[2], weekdays[3], weekdays[4], weekdays[5], weekdays[6], start, end)
		if err != nil {
			return err
		}
		key, err := result.LastInsertId()
		if err != nil {
			return err
		}
		c.services[id] = key
		c.calendarServiceIDs[id] = struct{}{}
		return nil
	})
}

func (c *feedCompiler) compileCalendarDates(ctx context.Context, tx *sql.Tx, file *zip.File) (int64, error) {
	insertCalendar, err := tx.PrepareContext(ctx, `INSERT INTO calendar(
		feed_key,feed_mode,source_service_id,service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date
	) VALUES(?,?,?,?,0,0,0,0,0,0,0,?,?)`)
	if err != nil {
		return 0, err
	}
	defer insertCalendar.Close()
	insertDate, err := tx.PrepareContext(ctx, `INSERT INTO calendar_dates(service_key,service_id,date,exception_type) VALUES(?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer insertDate.Close()
	updateRange, err := tx.PrepareContext(ctx, `UPDATE calendar
		SET start_date=MIN(start_date,?),end_date=MAX(end_date,?) WHERE service_key=?`)
	if err != nil {
		return 0, err
	}
	defer updateRange.Close()
	seen := make(map[string]bool)
	return readCSV(ctx, file, []string{"service_id", "date", "exception_type"}, func(index map[string]int, row []string) error {
		id := get(row, index, "service_id")
		if id == "" {
			return errors.New("missing service_id")
		}
		date, err := parseGTFSDate(get(row, index, "date"))
		if err != nil {
			return err
		}
		exception, err := parseRequiredInt(row, index, "exception_type")
		if err != nil || (exception != 1 && exception != 2) {
			return fmt.Errorf("invalid exception_type %q", get(row, index, "exception_type"))
		}
		pair := id + "\x00" + date
		if seen[pair] {
			return fmt.Errorf("duplicate calendar date for service_id %s on %s", id, date)
		}
		seen[pair] = true
		serviceKey, ok := c.services[id]
		if !ok {
			if exception != 1 {
				return fmt.Errorf("date-only service_id %s must use exception_type 1", id)
			}
			result, err := insertCalendar.ExecContext(ctx, c.feedKey, c.mode, id, prefix(c.mode, id), date, date)
			if err != nil {
				return err
			}
			serviceKey, err = result.LastInsertId()
			if err != nil {
				return err
			}
			c.services[id] = serviceKey
		} else if _, calendarDefined := c.calendarServiceIDs[id]; !calendarDefined {
			if exception != 1 {
				return fmt.Errorf("date-only service_id %s must use exception_type 1", id)
			}
			if _, err := updateRange.ExecContext(ctx, date, date, serviceKey); err != nil {
				return err
			}
		}
		_, err = insertDate.ExecContext(ctx, serviceKey, prefix(c.mode, id), date, exception)
		return err
	})
}

func (c *feedCompiler) compileTrips(ctx context.Context, tx *sql.Tx, file *zip.File) (int64, error) {
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO trips(
		feed_key,feed_mode,source_trip_id,trip_id,route_key,route_id,service_key,service_id,
		trip_headsign,direction_id,source_block_id,block_id
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	return readCSV(ctx, file, []string{"route_id", "service_id", "trip_id"}, func(index map[string]int, row []string) error {
		id := get(row, index, "trip_id")
		routeID := get(row, index, "route_id")
		serviceID := get(row, index, "service_id")
		if id == "" || routeID == "" || serviceID == "" {
			return errors.New("trip row missing trip_id, route_id, or service_id")
		}
		if _, duplicate := c.trips[id]; duplicate {
			// TODO(5.0.1): upstream Regional Coach feed contains duplicate trip_ids.
			// Skipping keeps the first occurrence and lets ingestion succeed, but the
			// root cause (upstream data quality vs. parser assumption) needs investigation.
			// Consider reporting to PTV or adding dedup diagnostics that log both rows.
			c.duplicateTrips++
			return nil
		}
		routeKey, ok := c.routes[routeID]
		if !ok {
			return fmt.Errorf("trip %s references unknown route_id %s", id, routeID)
		}
		serviceKey, ok := c.services[serviceID]
		if !ok {
			return fmt.Errorf("trip %s references unknown service_id %s", id, serviceID)
		}
		direction, err := parseOptionalInt(row, index, "direction_id", 0)
		if err != nil || direction < 0 || direction > 1 {
			return fmt.Errorf("invalid direction_id %q", get(row, index, "direction_id"))
		}
		blockSource := get(row, index, "block_id")
		blockID := ""
		if blockSource != "" {
			blockID = prefix(c.mode, blockSource)
		}
		result, err := stmt.ExecContext(ctx, c.feedKey, c.mode, id, prefix(c.mode, id), routeKey, prefix(c.mode, routeID),
			serviceKey, prefix(c.mode, serviceID), get(row, index, "trip_headsign"), direction, blockSource, blockID)
		if err != nil {
			return err
		}
		key, err := result.LastInsertId()
		if err != nil {
			return err
		}
		c.trips[id] = compiledTrip{key: key, routeKey: routeKey, serviceKey: serviceKey}
		return nil
	})
}

func (c *feedCompiler) compileStopTimes(ctx context.Context, tx *sql.Tx, file *zip.File) (int64, error) {
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO stop_times(
		trip_key,trip_id,stop_key,stop_id,stop_sequence,arrival_sec,departure_sec,pickup_type,drop_off_type
	) VALUES(?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	required := []string{"trip_id", "arrival_time", "departure_time", "stop_id", "stop_sequence"}
	return readCSV(ctx, file, required, func(index map[string]int, row []string) error {
		tripID := get(row, index, "trip_id")
		stopID := get(row, index, "stop_id")
		trip, ok := c.trips[tripID]
		if !ok {
			return fmt.Errorf("unknown trip_id %s", tripID)
		}
		stop, ok := c.stops[stopID]
		if !ok {
			return fmt.Errorf("unknown stop_id %s", stopID)
		}
		sequence, err := parseRequiredInt(row, index, "stop_sequence")
		if err != nil || sequence < 0 {
			return fmt.Errorf("invalid stop_sequence %q", get(row, index, "stop_sequence"))
		}
		arrival, valid := parseGTFSTime(get(row, index, "arrival_time"))
		if !valid || arrival > maxGTFSSeconds {
			return errors.New("invalid arrival_time")
		}
		departure, valid := parseGTFSTime(get(row, index, "departure_time"))
		if !valid || departure > maxGTFSSeconds || departure < arrival {
			return errors.New("invalid departure_time")
		}
		pickup, err := parseOptionalInt(row, index, "pickup_type", 0)
		if err != nil || pickup < 0 || pickup > 3 {
			return fmt.Errorf("invalid pickup_type %q", get(row, index, "pickup_type"))
		}
		dropOff, err := parseOptionalInt(row, index, "drop_off_type", 0)
		if err != nil || dropOff < 0 || dropOff > 3 {
			return fmt.Errorf("invalid drop_off_type %q", get(row, index, "drop_off_type"))
		}
		_, err = stmt.ExecContext(ctx, trip.key, prefix(c.mode, tripID), stop.key, prefix(c.mode, stopID), sequence, arrival, departure, pickup, dropOff)
		return err
	})
}

func (c *feedCompiler) compileTransfers(ctx context.Context, tx *sql.Tx, file *zip.File) (int64, error) {
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO transfers(
		feed_key,from_stop_key,to_stop_key,from_stop_id,to_stop_id,transfer_type,min_transfer_time,
		from_route_key,to_route_key,from_route_id,to_route_id,from_trip_key,to_trip_key,from_trip_id,to_trip_id,source
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'gtfs')`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	type transferSourceKey struct {
		fromStop, toStop, fromTrip, toTrip, fromRoute, toRoute string
	}
	type seenTransfer struct {
		transferType int
		rows         int
	}
	seen := make(map[transferSourceKey]seenTransfer)
	return readCSV(ctx, file, []string{"transfer_type"}, func(index map[string]int, row []string) error {
		transferType, err := parseOptionalInt(row, index, "transfer_type", 0)
		if err != nil || transferType < 0 || transferType > 5 {
			return fmt.Errorf("invalid transfer_type %q", get(row, index, "transfer_type"))
		}
		sourceKey := transferSourceKey{
			fromStop: get(row, index, "from_stop_id"), fromTrip: get(row, index, "from_trip_id"),
			fromRoute: get(row, index, "from_route_id"), toStop: get(row, index, "to_stop_id"),
			toTrip: get(row, index, "to_trip_id"), toRoute: get(row, index, "to_route_id"),
		}
		if previous, duplicate := seen[sourceKey]; duplicate {
			linkedConflict := previous.rows == 1 &&
				((previous.transferType == 4 && transferType == 5) || (previous.transferType == 5 && transferType == 4))
			if !linkedConflict {
				return errors.New("duplicate transfers.txt primary key")
			}
			seen[sourceKey] = seenTransfer{transferType: max(previous.transferType, transferType), rows: 2}
		} else {
			seen[sourceKey] = seenTransfer{transferType: transferType, rows: 1}
		}
		minimumRaw := get(row, index, "min_transfer_time")
		minimum, err := parseOptionalInt(row, index, "min_transfer_time", 0)
		if err != nil || minimum < 0 || (transferType == 2 && minimumRaw == "") {
			return fmt.Errorf("invalid min_transfer_time %q", minimumRaw)
		}
		fromStop, fromStopID, err := c.optionalStop(get(row, index, "from_stop_id"))
		if err != nil {
			return err
		}
		toStop, toStopID, err := c.optionalStop(get(row, index, "to_stop_id"))
		if err != nil {
			return err
		}
		fromRoute, fromRouteID, err := c.optionalRoute(get(row, index, "from_route_id"))
		if err != nil {
			return err
		}
		toRoute, toRouteID, err := c.optionalRoute(get(row, index, "to_route_id"))
		if err != nil {
			return err
		}
		fromTrip, fromTripID, err := c.optionalTrip(get(row, index, "from_trip_id"))
		if err != nil {
			return err
		}
		toTrip, toTripID, err := c.optionalTrip(get(row, index, "to_trip_id"))
		if err != nil {
			return err
		}
		if transferType <= 3 && (fromStop == nil || toStop == nil) {
			return errors.New("transfer types 0-3 require from_stop_id and to_stop_id")
		}
		if transferType >= 4 && (fromTrip == nil || toTrip == nil) {
			return errors.New("transfer types 4-5 require from_trip_id and to_trip_id")
		}
		if err := c.validateTransferStop(get(row, index, "from_stop_id"), transferType, "from_stop_id"); err != nil {
			return err
		}
		if err := c.validateTransferStop(get(row, index, "to_stop_id"), transferType, "to_stop_id"); err != nil {
			return err
		}
		if fromTrip != nil && fromRoute != nil && c.trips[get(row, index, "from_trip_id")].routeKey != *fromRoute {
			return errors.New("from_trip_id does not belong to from_route_id")
		}
		if toTrip != nil && toRoute != nil && c.trips[get(row, index, "to_trip_id")].routeKey != *toRoute {
			return errors.New("to_trip_id does not belong to to_route_id")
		}
		_, err = stmt.ExecContext(ctx, c.feedKey, nullableKey(fromStop), nullableKey(toStop), fromStopID, toStopID,
			transferType, minimum, nullableKey(fromRoute), nullableKey(toRoute), fromRouteID, toRouteID,
			nullableKey(fromTrip), nullableKey(toTrip), fromTripID, toTripID)
		return err
	})
}

func (c *feedCompiler) compilePathways(ctx context.Context, tx *sql.Tx, file *zip.File) (int64, error) {
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO pathways(
		feed_key,source_pathway_id,pathway_id,from_stop_key,to_stop_key,from_stop_id,to_stop_id,
		pathway_mode,is_bidirectional,length,traversal_time,stair_count,max_slope,min_width,signposted_as,reversed_signposted_as
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	required := []string{"pathway_id", "from_stop_id", "to_stop_id", "pathway_mode", "is_bidirectional"}
	return readCSV(ctx, file, required, func(index map[string]int, row []string) error {
		id := get(row, index, "pathway_id")
		if id == "" {
			return errors.New("missing pathway_id")
		}
		fromSource := get(row, index, "from_stop_id")
		toSource := get(row, index, "to_stop_id")
		from, ok := c.stops[fromSource]
		if !ok {
			return fmt.Errorf("unknown from_stop_id %s", fromSource)
		}
		to, ok := c.stops[toSource]
		if !ok {
			return fmt.Errorf("unknown to_stop_id %s", toSource)
		}
		if from.locationType == 1 || to.locationType == 1 {
			return errors.New("pathway endpoints must not be stations")
		}
		if (from.stopAccess != nil && *from.stopAccess == 1) || (to.stopAccess != nil && *to.stopAccess == 1) {
			return errors.New("pathway endpoints must not use stop_access 1")
		}
		mode, err := parseRequiredInt(row, index, "pathway_mode")
		if err != nil || mode < 1 || mode > 7 {
			return fmt.Errorf("invalid pathway_mode %q", get(row, index, "pathway_mode"))
		}
		if mode == 5 && c.files["levels.txt"] == nil {
			return errors.New("elevator pathways require levels.txt")
		}
		bidirectional, err := parseRequiredInt(row, index, "is_bidirectional")
		if err != nil || (bidirectional != 0 && bidirectional != 1) {
			return fmt.Errorf("invalid is_bidirectional %q", get(row, index, "is_bidirectional"))
		}
		if mode == 7 && bidirectional == 1 {
			return errors.New("exit-gate pathways must be unidirectional")
		}
		length, err := parseOptionalFloat(row, index, "length")
		if err != nil || (length.Valid && length.Float64 < 0) {
			return fmt.Errorf("invalid length %q", get(row, index, "length"))
		}
		traversal, err := optionalInt(row, index, "traversal_time")
		if err != nil || (traversal != nil && *traversal < 0) {
			return fmt.Errorf("invalid traversal_time %q", get(row, index, "traversal_time"))
		}
		// GTFS defines traversal_time as optional and positive, but the official
		// Transport Victoria feed currently serializes zero for unknown values.
		// Treat that producer-specific sentinel as absent so pathwaySeconds uses a
		// conservative length/mode fallback instead of inventing instant travel.
		if traversal != nil && *traversal == 0 {
			traversal = nil
		}
		stairs, err := optionalInt(row, index, "stair_count")
		if err != nil {
			return fmt.Errorf("invalid stair_count %q", get(row, index, "stair_count"))
		}
		// The same feed uses zero for an unknown optional stair count. Preserve
		// signed non-zero values and normalize only the non-conforming sentinel.
		if stairs != nil && *stairs == 0 {
			stairs = nil
		}
		maxSlope, err := parseOptionalFloat(row, index, "max_slope")
		if err != nil {
			return err
		}
		minWidth, err := parseOptionalFloat(row, index, "min_width")
		if err != nil || (minWidth.Valid && minWidth.Float64 <= 0) {
			return fmt.Errorf("invalid min_width %q", get(row, index, "min_width"))
		}
		_, err = stmt.ExecContext(ctx, c.feedKey, id, prefix(c.mode, id), from.key, to.key,
			prefix(c.mode, fromSource), prefix(c.mode, toSource), mode, bidirectional,
			nullFloatValue(length), nullableInt(traversal), nullableInt(stairs), nullFloatValue(maxSlope),
			nullFloatValue(minWidth), get(row, index, "signposted_as"), get(row, index, "reversed_signposted_as"))
		return err
	})
}

func (c *feedCompiler) materializeConnections(ctx context.Context, tx *sql.Tx) (int64, error) {
	// TODO(0.5.1): upstream Regional Coach feed contains non-increasing stop-time
	// segments (arrival at next stop < departure from current, or non-monotonic
	// stop_sequence). These are skipped below rather than failing ingestion, but
	// the root cause needs investigation — could be malformed upstream data or an
	// edge case in how we interpret GTFS times across service-day boundaries.
	// Consider inspecting the actual feed rows and reporting to PTV.
	if err := tx.QueryRowContext(ctx, `
		WITH ordered AS (
			SELECT trip_key,stop_sequence,departure_sec,
			       LEAD(stop_sequence) OVER (PARTITION BY trip_key ORDER BY stop_sequence,stop_time_key) AS next_sequence,
			       LEAD(arrival_sec) OVER (PARTITION BY trip_key ORDER BY stop_sequence,stop_time_key) AS next_arrival
			FROM stop_times WHERE trip_key IN (SELECT trip_key FROM trips WHERE feed_key=?)
		)
		SELECT COUNT(*) FROM ordered
		WHERE next_sequence IS NOT NULL AND (next_sequence <= stop_sequence OR next_arrival < departure_sec)`, c.feedKey).Scan(&c.nonIncreasingSegments); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `
		WITH ordered AS (
			SELECT st.trip_key,st.stop_key AS dep_stop_key,st.stop_sequence AS dep_sequence,
			       st.departure_sec,st.pickup_type,
			       LEAD(st.stop_key) OVER (PARTITION BY st.trip_key ORDER BY st.stop_sequence,st.stop_time_key) AS arr_stop_key,
			       LEAD(st.stop_sequence) OVER (PARTITION BY st.trip_key ORDER BY st.stop_sequence,st.stop_time_key) AS arr_sequence,
			       LEAD(st.arrival_sec) OVER (PARTITION BY st.trip_key ORDER BY st.stop_sequence,st.stop_time_key) AS arrival_sec,
			       LEAD(st.drop_off_type) OVER (PARTITION BY st.trip_key ORDER BY st.stop_sequence,st.stop_time_key) AS drop_off_type
			FROM stop_times st
			WHERE st.trip_key IN (SELECT trip_key FROM trips WHERE feed_key=?)
		)
		INSERT INTO connections(
			feed_key,feed_mode,service_key,trip_key,route_key,dep_stop_key,arr_stop_key,
			dep_sequence,arr_sequence,departure_sec,arrival_sec,pickup_type,drop_off_type,block_id
		)
		SELECT t.feed_key,t.feed_mode,t.service_key,t.trip_key,t.route_key,o.dep_stop_key,o.arr_stop_key,
		       o.dep_sequence,o.arr_sequence,o.departure_sec,o.arrival_sec,o.pickup_type,o.drop_off_type,t.block_id
		FROM ordered o JOIN trips t ON t.trip_key=o.trip_key
		WHERE o.arr_stop_key IS NOT NULL
		  AND NOT (o.arr_sequence IS NOT NULL AND (o.arr_sequence <= o.dep_sequence OR o.arrival_sec < o.departure_sec))
		ORDER BY t.feed_key,t.trip_key,o.dep_sequence,o.arr_sequence`, c.feedKey)
	if err != nil {
		return 0, fmt.Errorf("materializing elementary connections: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return count, nil
}

func (c *feedCompiler) optionalStop(source string) (*int64, string, error) {
	if source == "" {
		return nil, "", nil
	}
	stop, ok := c.stops[source]
	if !ok {
		return nil, "", fmt.Errorf("unknown stop_id %s", source)
	}
	return &stop.key, prefix(c.mode, source), nil
}

func (c *feedCompiler) optionalRoute(source string) (*int64, string, error) {
	if source == "" {
		return nil, "", nil
	}
	key, ok := c.routes[source]
	if !ok {
		return nil, "", fmt.Errorf("unknown route_id %s", source)
	}
	return &key, prefix(c.mode, source), nil
}

func (c *feedCompiler) optionalTrip(source string) (*int64, string, error) {
	if source == "" {
		return nil, "", nil
	}
	trip, ok := c.trips[source]
	if !ok {
		return nil, "", fmt.Errorf("unknown trip_id %s", source)
	}
	return &trip.key, prefix(c.mode, source), nil
}

func (c *feedCompiler) validateTransferStop(source string, transferType int, field string) error {
	if source == "" {
		return nil
	}
	stop := c.stops[source]
	if transferType <= 3 && stop.locationType != 0 && stop.locationType != 1 {
		return fmt.Errorf("%s must reference a stop or station for transfer_type %d", field, transferType)
	}
	if transferType >= 4 && stop.locationType != 0 {
		return fmt.Errorf("%s must reference a platform for transfer_type %d", field, transferType)
	}
	return nil
}

func optionalInt(row []string, index map[string]int, name string) (*int64, error) {
	value := get(row, index, name)
	if value == "" {
		return nil, nil
	}
	number, err := parseRequiredInt(row, index, name)
	if err != nil {
		return nil, fmt.Errorf("invalid %s %q", name, value)
	}
	converted := int64(number)
	return &converted, nil
}

func nullFloatValue(value sql.NullFloat64) any {
	if !value.Valid {
		return nil
	}
	return value.Float64
}

func nullableInt(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableKey(value *int64) any { return nullableInt(value) }
