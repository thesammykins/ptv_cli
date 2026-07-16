package gtfs

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
)

const earthRadiusMetres = 6_371_000.0

func materializeTripLinks(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO trip_links(
			feed_key,from_trip_key,to_trip_key,from_stop_key,to_stop_key,transfer_type,min_transfer_time
		)
		SELECT feed_key,from_trip_key,to_trip_key,from_stop_key,to_stop_key,
		       MAX(transfer_type),MAX(min_transfer_time)
		FROM transfers
		WHERE source='gtfs' AND transfer_type IN (4,5)
		GROUP BY feed_key,from_trip_key,to_trip_key,from_stop_key,to_stop_key`)
	if err != nil {
		return fmt.Errorf("materializing linked-trip templates: %w", err)
	}
	return nil
}

type graphStop struct {
	key          int64
	id           string
	lat          float64
	lon          float64
	hasPosition  bool
	locationType int
	parent       int64
	directAccess bool
}

type stopPair struct{ from, to int64 }

func generateProximityTransfers(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT stop_key,stop_id,stop_lat,stop_lon,location_type,parent_stop_key,stop_access FROM stops`)
	if err != nil {
		return err
	}
	stops := make(map[int64]graphStop)
	children := make(map[int64][]int64)
	for rows.Next() {
		var stop graphStop
		var lat, lon sql.NullFloat64
		var parent sql.NullInt64
		var stopAccess sql.NullInt64
		if err := rows.Scan(&stop.key, &stop.id, &lat, &lon, &stop.locationType, &parent, &stopAccess); err != nil {
			rows.Close()
			return err
		}
		stop.lat, stop.lon = lat.Float64, lon.Float64
		stop.hasPosition = lat.Valid && lon.Valid
		stop.directAccess = stopAccess.Valid && stopAccess.Int64 == 1
		if parent.Valid {
			stop.parent = parent.Int64
			children[stop.parent] = append(children[stop.parent], stop.key)
		}
		stops[stop.key] = stop
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}

	rootMemo := make(map[int64]int64, len(stops))
	var rootOf func(int64, map[int64]bool) (int64, error)
	rootOf = func(key int64, visiting map[int64]bool) (int64, error) {
		if root := rootMemo[key]; root != 0 {
			return root, nil
		}
		if visiting[key] {
			return 0, fmt.Errorf("stop hierarchy contains a cycle at stop_key %d", key)
		}
		visiting[key] = true
		stop, ok := stops[key]
		if !ok {
			return 0, fmt.Errorf("stop hierarchy references missing stop_key %d", key)
		}
		root := key
		if stop.parent != 0 {
			var err error
			root, err = rootOf(stop.parent, visiting)
			if err != nil {
				return 0, err
			}
		}
		delete(visiting, key)
		rootMemo[key] = root
		return root, nil
	}
	for key := range stops {
		if _, err := rootOf(key, make(map[int64]bool)); err != nil {
			return err
		}
	}

	authoritativeRoots := make(map[int64]bool)
	pathRows, err := tx.QueryContext(ctx, `SELECT from_stop_key,to_stop_key FROM pathways`)
	if err != nil {
		return err
	}
	for pathRows.Next() {
		var from, to int64
		if err := pathRows.Scan(&from, &to); err != nil {
			pathRows.Close()
			return err
		}
		authoritativeRoots[rootMemo[from]] = true
		authoritativeRoots[rootMemo[to]] = true
	}
	if err := pathRows.Close(); err != nil {
		return err
	}
	if err := pathRows.Err(); err != nil {
		return err
	}

	explicitPairs := make(map[stopPair]bool)
	transferRows, err := tx.QueryContext(ctx, `
		SELECT from_stop_key,to_stop_key FROM transfers
		WHERE source='gtfs' AND transfer_type BETWEEN 0 AND 3
		  AND from_stop_key IS NOT NULL AND to_stop_key IS NOT NULL
		  AND from_route_key IS NULL AND to_route_key IS NULL
		  AND from_trip_key IS NULL AND to_trip_key IS NULL`)
	if err != nil {
		return err
	}
	for transferRows.Next() {
		var from, to int64
		if err := transferRows.Scan(&from, &to); err != nil {
			transferRows.Close()
			return err
		}
		fromStops := platformDescendants(from, stops, children)
		toStops := platformDescendants(to, stops, children)
		for _, expandedFrom := range fromStops {
			for _, expandedTo := range toStops {
				explicitPairs[stopPair{from: expandedFrom, to: expandedTo}] = true
			}
		}
	}
	if err := transferRows.Close(); err != nil {
		return err
	}
	if err := transferRows.Err(); err != nil {
		return err
	}

	const metresPerLatitudeDegree = earthRadiusMetres * math.Pi / 180
	cellDegrees := maxTransferMeters / metresPerLatitudeDegree
	longitudeCells := int(math.Ceil(360 / cellDegrees))
	type cell struct{ lat, lon int }
	grid := make(map[cell][]int)
	var eligible []graphStop
	for _, stop := range stops {
		if stop.locationType != 0 || !stop.hasPosition ||
			(authoritativeRoots[rootMemo[stop.key]] && !stop.directAccess) {
			continue
		}
		eligible = append(eligible, stop)
	}
	sort.Slice(eligible, func(i, j int) bool { return eligible[i].key < eligible[j].key })
	longitudeCell := func(longitude float64) int {
		normalized := math.Mod(longitude+180, 360)
		if normalized < 0 {
			normalized += 360
		}
		index := int(math.Floor(normalized / cellDegrees))
		if index >= longitudeCells {
			index = 0
		}
		return index
	}
	for index, stop := range eligible {
		key := cell{lat: int(math.Floor(stop.lat / cellDegrees)), lon: longitudeCell(stop.lon)}
		grid[key] = append(grid[key], index)
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO transfers(
		feed_key,from_stop_key,to_stop_key,from_stop_id,to_stop_id,transfer_type,min_transfer_time,source
	) VALUES(NULL,?,?,?,?,2,?,'proximity')`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for i, stop := range eligible {
		if i&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		base := cell{lat: int(math.Floor(stop.lat / cellDegrees)), lon: longitudeCell(stop.lon)}
		polarLatitude := min(90.0, math.Abs(stop.lat)+cellDegrees)
		cosLatitude := math.Abs(math.Cos(polarLatitude * math.Pi / 180))
		longitudeSpan := longitudeCells
		if cosLatitude > 0 && 1/cosLatitude+1 < float64(longitudeCells) {
			longitudeSpan = int(math.Ceil(1/cosLatitude)) + 1
		}
		fullLongitude := longitudeSpan*2+1 >= longitudeCells
		for dLat := -1; dLat <= 1; dLat++ {
			firstLongitude, lastLongitude := -longitudeSpan, longitudeSpan
			if fullLongitude {
				firstLongitude, lastLongitude = 0, longitudeCells-1
			}
			for longitudeOffset := firstLongitude; longitudeOffset <= lastLongitude; longitudeOffset++ {
				longitudeIndex := longitudeOffset
				if !fullLongitude {
					longitudeIndex = (base.lon + longitudeOffset) % longitudeCells
					if longitudeIndex < 0 {
						longitudeIndex += longitudeCells
					}
				}
				for _, candidateIndex := range grid[cell{lat: base.lat + dLat, lon: longitudeIndex}] {
					if candidateIndex <= i {
						continue
					}
					candidate := eligible[candidateIndex]
					distance := haversine(stop.lat, stop.lon, candidate.lat, candidate.lon)
					if distance > maxTransferMeters {
						continue
					}
					seconds := int(math.Ceil(distance / walkMetersPerSec))
					if seconds < 1 {
						seconds = 1
					}
					for _, pair := range []struct{ from, to graphStop }{{stop, candidate}, {candidate, stop}} {
						if explicitPairs[stopPair{from: pair.from.key, to: pair.to.key}] {
							continue
						}
						if _, err := stmt.ExecContext(ctx, pair.from.key, pair.to.key, pair.from.id, pair.to.id, seconds); err != nil {
							return err
						}
					}
				}
			}
		}
	}
	return nil
}

func platformDescendants(key int64, stops map[int64]graphStop, children map[int64][]int64) []int64 {
	if stops[key].locationType == 0 {
		return []int64{key}
	}
	queue := []int64{key}
	var result []int64
	seen := map[int64]bool{key: true}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, child := range children[current] {
			if seen[child] {
				continue
			}
			seen[child] = true
			if stops[child].locationType == 0 {
				result = append(result, child)
			} else {
				queue = append(queue, child)
			}
		}
	}
	return result
}

func haversine(lat1, lon1, lat2, lon2 float64) float64 {
	first := lat1 * math.Pi / 180
	second := lat2 * math.Pi / 180
	deltaLatitude := (lat2 - lat1) * math.Pi / 180
	deltaLongitude := (lon2 - lon1) * math.Pi / 180
	a := math.Sin(deltaLatitude/2)*math.Sin(deltaLatitude/2) +
		math.Cos(first)*math.Cos(second)*math.Sin(deltaLongitude/2)*math.Sin(deltaLongitude/2)
	return earthRadiusMetres * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}
