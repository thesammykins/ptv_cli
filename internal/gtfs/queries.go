package gtfs

// This file is the public query boundary for schema-v2 generations. Text IDs
// are presentation and lookup values; relationship joins deliberately use the
// generation-local integer keys so two feeds can contain the same source ID.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/thesammykins/ptv_cli/internal/localtime"
)

type StopResult struct {
	StopID             string  `json:"stop_id"`
	StopName           string  `json:"stop_name"`
	StopLat            float64 `json:"stop_latitude"`
	StopLon            float64 `json:"stop_longitude"`
	FeedMode           int     `json:"feed_mode"`
	ParentStation      string  `json:"parent_station,omitempty"`
	LocationType       int     `json:"location_type"`
	WheelchairBoarding int     `json:"wheelchair_boarding"`
}

type NearbyStopResult struct {
	StopResult
	DistanceMetres float64 `json:"distance_metres"`
}

type RouteResult struct {
	RouteID   string `json:"route_id"`
	ShortName string `json:"route_short_name"`
	LongName  string `json:"route_long_name"`
	FeedMode  int    `json:"feed_mode"`
	RouteType int    `json:"route_type"`
}

type RouteDetailResult struct {
	Route      RouteResult          `json:"route"`
	Directions []DirectionResult    `json:"directions"`
	Stops      map[int][]StopResult `json:"stops"`
}

type DirectionResult struct {
	DirectionID int    `json:"direction_id"`
	Headsign    string `json:"headsign"`
	Description string `json:"description"`
}

type DepartureResult struct {
	TripID         string `json:"trip_id"`
	RouteID        string `json:"route_id"`
	StopID         string `json:"stop_id"`
	StopSequence   int    `json:"stop_sequence"`
	DepartureSec   int    `json:"departure_sec"`
	ArrivalSec     int    `json:"arrival_sec"`
	Headsign       string `json:"headsign"`
	RouteShortName string `json:"route_short_name"`
	RouteLongName  string `json:"route_long_name"`
	FeedMode       int    `json:"feed_mode"`
	DirectionID    int    `json:"direction_id"`
	ServiceDate    string `json:"service_date"`
	BlockID        string `json:"block_id,omitempty"`
	PickupType     int    `json:"pickup_type"`
	DropOffType    int    `json:"drop_off_type"`
}

type TripDetailResult struct {
	TripID      string           `json:"trip_id"`
	RouteID     string           `json:"route_id"`
	Headsign    string           `json:"headsign"`
	DirectionID int              `json:"direction_id"`
	BlockID     string           `json:"block_id,omitempty"`
	ServiceID   string           `json:"service_id"`
	FeedMode    int              `json:"feed_mode"`
	Stops       []TripStopResult `json:"stops"`
}

type TripStopResult struct {
	StopID       string  `json:"stop_id"`
	StopName     string  `json:"stop_name"`
	StopLat      float64 `json:"stop_latitude"`
	StopLon      float64 `json:"stop_longitude"`
	StopSequence int     `json:"stop_sequence"`
	ArrivalSec   int     `json:"arrival_sec"`
	DepartureSec int     `json:"departure_sec"`
	PickupType   int     `json:"pickup_type"`
	DropOffType  int     `json:"drop_off_type"`
}

type StopDetailResult struct {
	Stop      StopResult       `json:"stop"`
	Routes    []RouteResult    `json:"routes"`
	Transfers []TransferResult `json:"transfers"`
	Pathways  []PathwayResult  `json:"pathways"`
}

type TransferResult struct {
	FromStopID      string `json:"from_stop_id"`
	ToStopID        string `json:"to_stop_id"`
	TransferType    int    `json:"transfer_type"`
	MinTransferTime int    `json:"min_transfer_time"`
	Source          string `json:"source"`
}

type PathwayResult struct {
	PathwayID            string   `json:"pathway_id"`
	FromStopID           string   `json:"from_stop_id"`
	ToStopID             string   `json:"to_stop_id"`
	PathwayMode          int      `json:"pathway_mode"`
	IsBidirectional      bool     `json:"is_bidirectional"`
	Length               *float64 `json:"length,omitempty"`
	TraversalTime        *int     `json:"traversal_time,omitempty"`
	SignpostedAs         string   `json:"signposted_as,omitempty"`
	ReversedSignpostedAs string   `json:"reversed_signposted_as,omitempty"`
}

// AmbiguousIDError is returned instead of choosing an arbitrary feed when a
// raw source ID occurs in more than one feed.
type AmbiguousIDError struct {
	Kind       string
	Query      string
	Candidates []string
}

func (e *AmbiguousIDError) Error() string {
	return fmt.Sprintf("ambiguous %s %q; candidates: %s", e.Kind, e.Query, strings.Join(e.Candidates, ", "))
}

var ErrNotFound = errors.New("GTFS result not found")

func modeFilter(column string, modes []int) (string, []any) {
	if len(modes) == 0 {
		return "", nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(modes)), ",")
	args := make([]any, len(modes))
	for i, mode := range modes {
		args[i] = mode
	}
	return " AND " + column + " IN (" + placeholders + ")", args
}

func publicID(mode int, source string) string { return strconv.Itoa(mode) + ":" + source }

func stopFromRow(scan func(...any) error) (StopResult, error) {
	var r StopResult
	var parent sql.NullString
	if err := scan(&r.StopID, &r.StopName, &r.StopLat, &r.StopLon, &r.FeedMode, &parent, &r.LocationType, &r.WheelchairBoarding); err != nil {
		return StopResult{}, err
	}
	if parent.Valid {
		r.ParentStation = parent.String
	}
	return r, nil
}

func routeFromRow(scan func(...any) error) (RouteResult, error) {
	var r RouteResult
	if err := scan(&r.RouteID, &r.ShortName, &r.LongName, &r.FeedMode, &r.RouteType); err != nil {
		return RouteResult{}, err
	}
	return r, nil
}

func (s *Store) stopByID(ctx context.Context, query string, feedModes []int) (StopResult, int64, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return StopResult{}, 0, fmt.Errorf("stop query is empty")
	}
	where, args := modeFilter("s.feed_mode", feedModes)
	conditions := []string{"s.stop_id = ?"}
	queryArgs := []any{query}
	if mode, source, ok := splitPublicID(query); ok {
		conditions = []string{"s.feed_mode = ? AND s.source_stop_id = ?"}
		queryArgs = []any{mode, source}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT s.stop_id,coalesce(s.stop_name,''),coalesce(s.stop_lat,0),coalesce(s.stop_lon,0),s.feed_mode,
		p.stop_id,s.location_type,s.wheelchair_boarding,s.stop_key
		FROM stops s LEFT JOIN stops p ON p.stop_key=s.parent_stop_key
		WHERE (`+strings.Join(conditions, " AND ")+`)`+where+` ORDER BY s.stop_id`, append(queryArgs, args...)...)
	if err != nil {
		return StopResult{}, 0, err
	}
	defer rows.Close()
	var found []struct {
		result StopResult
		key    int64
	}
	for rows.Next() {
		var r StopResult
		var parent sql.NullString
		var key int64
		if err := rows.Scan(&r.StopID, &r.StopName, &r.StopLat, &r.StopLon, &r.FeedMode, &parent, &r.LocationType, &r.WheelchairBoarding, &key); err != nil {
			return StopResult{}, 0, err
		}
		if parent.Valid {
			r.ParentStation = parent.String
		}
		found = append(found, struct {
			result StopResult
			key    int64
		}{r, key})
	}
	if err := rows.Err(); err != nil {
		return StopResult{}, 0, err
	}
	if len(found) == 0 && !strings.Contains(query, ":") {
		rows, err = s.db.QueryContext(ctx, `SELECT s.stop_id,coalesce(s.stop_name,''),coalesce(s.stop_lat,0),coalesce(s.stop_lon,0),s.feed_mode,
			p.stop_id,s.location_type,s.wheelchair_boarding,s.stop_key
			FROM stops s LEFT JOIN stops p ON p.stop_key=s.parent_stop_key
			WHERE s.source_stop_id=?`+where+` ORDER BY s.stop_id`, append([]any{query}, args...)...)
		if err != nil {
			return StopResult{}, 0, err
		}
		defer rows.Close()
		for rows.Next() {
			var r StopResult
			var parent sql.NullString
			var key int64
			if err := rows.Scan(&r.StopID, &r.StopName, &r.StopLat, &r.StopLon, &r.FeedMode, &parent, &r.LocationType, &r.WheelchairBoarding, &key); err != nil {
				return StopResult{}, 0, err
			}
			if parent.Valid {
				r.ParentStation = parent.String
			}
			found = append(found, struct {
				result StopResult
				key    int64
			}{r, key})
		}
	}
	if len(found) == 0 {
		return StopResult{}, 0, fmt.Errorf("%w: stop %q", ErrNotFound, query)
	}
	if len(found) > 1 {
		candidates := make([]string, len(found))
		for i, v := range found {
			candidates[i] = v.result.StopID
		}
		return StopResult{}, 0, &AmbiguousIDError{Kind: "stop", Query: query, Candidates: candidates}
	}
	return found[0].result, found[0].key, nil
}

func splitPublicID(value string) (int, string, bool) {
	mode, source, ok := strings.Cut(strings.TrimSpace(value), ":")
	if !ok || mode == "" || source == "" {
		return 0, "", false
	}
	n, err := strconv.Atoi(mode)
	return n, source, err == nil
}

func (s *Store) StopSearch(ctx context.Context, term string, feedModes []int, limit int) ([]StopResult, error) {
	tokens := strings.FieldsFunc(strings.ToLower(strings.TrimSpace(term)), unicode.IsSpace)
	if len(tokens) == 0 {
		return []StopResult{}, nil
	}
	where, modeArgs := modeFilter("s.feed_mode", feedModes)
	clauses := make([]string, len(tokens))
	args := make([]any, 0, len(tokens)+len(modeArgs))
	for i, token := range tokens {
		clauses[i] = "lower(coalesce(s.stop_name,'')) LIKE ?"
		args = append(args, "%"+token+"%")
	}
	args = append(args, modeArgs...)
	query := `SELECT s.stop_id,coalesce(s.stop_name,''),coalesce(s.stop_lat,0),coalesce(s.stop_lon,0),s.feed_mode,p.stop_id,s.location_type,s.wheelchair_boarding
		FROM stops s LEFT JOIN stops p ON p.stop_key=s.parent_stop_key WHERE ` + strings.Join(clauses, " AND ") + where
	results, err := s.queryStops(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 && len(tokens) > 1 {
		results, err = s.queryStops(ctx, `SELECT s.stop_id,coalesce(s.stop_name,''),coalesce(s.stop_lat,0),coalesce(s.stop_lon,0),s.feed_mode,p.stop_id,s.location_type,s.wheelchair_boarding
			FROM stops s LEFT JOIN stops p ON p.stop_key=s.parent_stop_key WHERE lower(coalesce(s.stop_name,'')) LIKE ?`+where, append([]any{"%" + strings.ToLower(strings.TrimSpace(term)) + "%"}, modeArgs...)...)
		if err != nil {
			return nil, err
		}
	}
	if len(results) == 0 {
		return results, nil
	}
	sort.SliceStable(results, func(i, j int) bool {
		li, lj := strings.ToLower(results[i].StopName), strings.ToLower(results[j].StopName)
		si, sj := stopSearchScore(li, tokens), stopSearchScore(lj, tokens)
		if si != sj {
			return si > sj
		}
		if len(li) != len(lj) {
			return len(li) < len(lj)
		}
		return results[i].StopID < results[j].StopID
	})
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// ResolveStop resolves an exact namespaced public ID or an unambiguous source
// stop ID. It is intentionally separate from name search.
func (s *Store) ResolveStop(ctx context.Context, stopID string, feedModes []int) (StopResult, error) {
	result, _, err := s.stopByID(ctx, stopID, feedModes)
	return result, err
}

// ResolveRoute resolves an exact namespaced public ID or an unambiguous source
// route ID.
func (s *Store) ResolveRoute(ctx context.Context, routeID string, feedModes []int) (RouteResult, error) {
	result, _, err := s.routeByID(ctx, routeID, feedModes)
	return result, err
}

func stopSearchScore(name string, tokens []string) int {
	score := 0
	for _, token := range tokens {
		if strings.Contains(name, token) {
			score += 100
		}
		if strings.HasPrefix(name, token) {
			score += 20
		}
	}
	return score
}

func (s *Store) queryStops(ctx context.Context, query string, args ...any) ([]StopResult, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StopResult
	for rows.Next() {
		r, err := stopFromRow(func(dest ...any) error { return rows.Scan(dest...) })
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) NearbyStops(ctx context.Context, lat, lng float64, feedModes []int, maxDistance float64, limit int) ([]NearbyStopResult, error) {
	if maxDistance <= 0 {
		maxDistance = 5000
	}
	latDelta := maxDistance / 111320
	cos := math.Cos(lat * math.Pi / 180)
	if math.Abs(cos) < 0.01 {
		cos = 0.01
	}
	lngDelta := maxDistance / (111320 * cos)
	where, args := modeFilter("s.feed_mode", feedModes)
	query := `SELECT s.stop_id,coalesce(s.stop_name,''),coalesce(s.stop_lat,0),coalesce(s.stop_lon,0),s.feed_mode,p.stop_id,s.location_type,s.wheelchair_boarding
		FROM stops s LEFT JOIN stops p ON p.stop_key=s.parent_stop_key WHERE s.stop_lat BETWEEN ? AND ? AND s.stop_lon BETWEEN ? AND ?` + where
	args = append([]any{lat - latDelta, lat + latDelta, lng - lngDelta, lng + lngDelta}, args...)
	stops, err := s.queryStops(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	out := make([]NearbyStopResult, 0, len(stops))
	for _, stop := range stops {
		d := haversineMetres(lat, lng, stop.StopLat, stop.StopLon)
		if d <= maxDistance {
			out = append(out, NearbyStopResult{StopResult: stop, DistanceMetres: d})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].DistanceMetres != out[j].DistanceMetres {
			return out[i].DistanceMetres < out[j].DistanceMetres
		}
		return out[i].StopID < out[j].StopID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func haversineMetres(lat1, lng1, lat2, lng2 float64) float64 {
	const earth = 6371000
	p1, p2 := lat1*math.Pi/180, lat2*math.Pi/180
	dp := (lat2 - lat1) * math.Pi / 180
	dl := (lng2 - lng1) * math.Pi / 180
	a := math.Sin(dp/2)*math.Sin(dp/2) + math.Cos(p1)*math.Cos(p2)*math.Sin(dl/2)*math.Sin(dl/2)
	return earth * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

func (s *Store) RoutesByMode(ctx context.Context, feedModes []int) ([]RouteResult, error) {
	where, args := modeFilter("r.feed_mode", feedModes)
	rows, err := s.db.QueryContext(ctx, `SELECT r.route_id,coalesce(r.route_short_name,''),coalesce(r.route_long_name,''),r.feed_mode,coalesce(r.route_type,0) FROM routes r WHERE 1=1`+where+` ORDER BY r.feed_mode,r.route_short_name,r.route_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RouteResult
	for rows.Next() {
		r, err := routeFromRow(func(dest ...any) error { return rows.Scan(dest...) })
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) routeByID(ctx context.Context, query string, feedModes []int) (RouteResult, int64, error) {
	query = strings.TrimSpace(query)
	where, args := modeFilter("r.feed_mode", feedModes)
	cond := []string{"r.route_id=?"}
	qargs := []any{query}
	if mode, source, ok := splitPublicID(query); ok {
		cond = []string{"r.feed_mode=? AND r.source_route_id=?"}
		qargs = []any{mode, source}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT r.route_id,coalesce(r.route_short_name,''),coalesce(r.route_long_name,''),r.feed_mode,coalesce(r.route_type,0),r.route_key FROM routes r WHERE `+strings.Join(cond, " AND ")+where, append(qargs, args...)...)
	if err != nil {
		return RouteResult{}, 0, err
	}
	defer rows.Close()
	var found []struct {
		r   RouteResult
		key int64
	}
	for rows.Next() {
		var r RouteResult
		var key int64
		if err := rows.Scan(&r.RouteID, &r.ShortName, &r.LongName, &r.FeedMode, &r.RouteType, &key); err != nil {
			return RouteResult{}, 0, err
		}
		found = append(found, struct {
			r   RouteResult
			key int64
		}{r, key})
	}
	if len(found) == 0 && !strings.Contains(query, ":") {
		rows, err = s.db.QueryContext(ctx, `SELECT r.route_id,coalesce(r.route_short_name,''),coalesce(r.route_long_name,''),r.feed_mode,coalesce(r.route_type,0),r.route_key FROM routes r WHERE r.source_route_id=?`+where, append([]any{query}, args...)...)
		if err != nil {
			return RouteResult{}, 0, err
		}
		defer rows.Close()
		for rows.Next() {
			var r RouteResult
			var key int64
			if err := rows.Scan(&r.RouteID, &r.ShortName, &r.LongName, &r.FeedMode, &r.RouteType, &key); err != nil {
				return RouteResult{}, 0, err
			}
			found = append(found, struct {
				r   RouteResult
				key int64
			}{r, key})
		}
	}
	if len(found) == 0 && !strings.Contains(query, ":") {
		rows, err = s.db.QueryContext(ctx, `SELECT r.route_id,coalesce(r.route_short_name,''),coalesce(r.route_long_name,''),r.feed_mode,coalesce(r.route_type,0),r.route_key FROM routes r WHERE r.route_short_name=?`+where, append([]any{query}, args...)...)
		if err != nil {
			return RouteResult{}, 0, err
		}
		defer rows.Close()
		for rows.Next() {
			var r RouteResult
			var key int64
			if err := rows.Scan(&r.RouteID, &r.ShortName, &r.LongName, &r.FeedMode, &r.RouteType, &key); err != nil {
				return RouteResult{}, 0, err
			}
			found = append(found, struct {
				r   RouteResult
				key int64
			}{r, key})
		}
	}
	if len(found) == 0 {
		return RouteResult{}, 0, fmt.Errorf("%w: route %q", ErrNotFound, query)
	}
	if len(found) > 1 {
		c := make([]string, len(found))
		for i, v := range found {
			c[i] = v.r.RouteID
		}
		return RouteResult{}, 0, &AmbiguousIDError{Kind: "route", Query: query, Candidates: c}
	}
	return found[0].r, found[0].key, nil
}

func (s *Store) RouteDetail(ctx context.Context, routeID string) (*RouteDetailResult, error) {
	route, routeKey, err := s.routeByID(ctx, routeID, nil)
	if err != nil {
		return nil, err
	}
	result := &RouteDetailResult{Route: route, Directions: []DirectionResult{}, Stops: map[int][]StopResult{}}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT coalesce(t.direction_id,0),coalesce(t.trip_headsign,'') FROM trips t WHERE t.route_key=? ORDER BY t.direction_id,t.trip_headsign`, routeKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[int]bool{}
	for rows.Next() {
		var id int
		var head string
		if err := rows.Scan(&id, &head); err != nil {
			return nil, err
		}
		if !seen[id] {
			result.Directions = append(result.Directions, DirectionResult{DirectionID: id, Headsign: head, Description: strings.TrimSpace(route.LongName + " " + head)})
			seen[id] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT DISTINCT t.direction_id,s.stop_key FROM trips t JOIN stop_times st ON st.trip_key=t.trip_key JOIN stops s ON s.stop_key=st.stop_key WHERE t.route_key=? ORDER BY t.direction_id,st.stop_sequence,s.stop_id`, routeKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var direction int
		var stopKey int64
		if err := rows.Scan(&direction, &stopKey); err != nil {
			return nil, err
		}
		stop, err := s.stopByKey(ctx, stopKey)
		if err != nil {
			return nil, err
		}
		result.Stops[direction] = append(result.Stops[direction], stop)
	}
	return result, rows.Err()
}

func (s *Store) StopsOnRoute(ctx context.Context, routeID string, directionID *int) ([]StopResult, error) {
	route, key, err := s.routeByID(ctx, routeID, nil)
	_ = route
	if err != nil {
		return nil, err
	}
	q := `SELECT t.direction_id,st.stop_sequence,st.stop_key FROM trips t JOIN stop_times st ON st.trip_key=t.trip_key WHERE t.route_key=?`
	args := []any{key}
	if directionID != nil {
		q += ` AND t.direction_id=?`
		args = append(args, *directionID)
	}
	q += ` ORDER BY t.direction_id,st.stop_sequence,st.stop_key`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StopResult
	seen := map[string]bool{}
	for rows.Next() {
		var d, seq int
		var key int64
		if err := rows.Scan(&d, &seq, &key); err != nil {
			return nil, err
		}
		stop, err := s.stopByKey(ctx, key)
		if err != nil {
			return nil, err
		}
		if !seen[stop.StopID] {
			out = append(out, stop)
			seen[stop.StopID] = true
		}
	}
	return out, rows.Err()
}

func (s *Store) StopDepartures(ctx context.Context, stopID string, date time.Time, routeID string, feedModes []int, limit int) ([]DepartureResult, error) {
	stop, stopKey, err := s.stopByID(ctx, stopID, feedModes)
	if err != nil {
		return nil, err
	}
	active, err := s.activeServiceKeys(ctx, date.In(localtime.Melbourne()))
	if err != nil {
		return nil, err
	}
	if len(active) == 0 {
		return []DepartureResult{}, nil
	}
	whereMode, argsMode := modeFilter("c.feed_mode", feedModes)
	stopKeys := []int64{stopKey}
	if stop.ParentStation != "" {
		if parent, parentKey, parentErr := s.stopByID(ctx, stop.ParentStation, nil); parentErr == nil {
			stop = parent
			stopKeys = []int64{parentKey}
		}
	}
	if stop.LocationType == 1 {
		rows, childErr := s.db.QueryContext(ctx, `SELECT stop_key FROM stops WHERE parent_stop_key=? ORDER BY stop_key`, stopKeys[0])
		if childErr != nil {
			return nil, childErr
		}
		for rows.Next() {
			var childKey int64
			if scanErr := rows.Scan(&childKey); scanErr != nil {
				_ = rows.Close()
				return nil, scanErr
			}
			stopKeys = append(stopKeys, childKey)
		}
		if closeErr := rows.Close(); closeErr != nil {
			return nil, closeErr
		}
	}
	q := `SELECT t.trip_id,r.route_id,st.stop_id,c.dep_sequence,c.departure_sec,c.arrival_sec,coalesce(t.trip_headsign,''),coalesce(r.route_short_name,''),coalesce(r.route_long_name,''),c.feed_mode,coalesce(t.direction_id,0),coalesce(t.block_id,''),c.pickup_type,c.drop_off_type
		FROM connections c JOIN trips t ON t.trip_key=c.trip_key JOIN routes r ON r.route_key=c.route_key JOIN stops st ON st.stop_key=c.dep_stop_key WHERE c.service_key IN (` + placeholders(len(active)) + `) AND c.dep_stop_key IN (` + placeholders(len(stopKeys)+0) + `)` + whereMode
	args := make([]any, 0, len(active)+len(stopKeys)+len(argsMode))
	for _, k := range active {
		args = append(args, k)
	}
	for _, k := range stopKeys {
		args = append(args, k)
	}
	args = append(args, argsMode...)
	if routeID != "" {
		route, routeKey, rerr := s.routeByID(ctx, routeID, feedModes)
		_ = route
		if rerr != nil {
			return nil, rerr
		}
		q += ` AND c.route_key=?`
		args = append(args, routeKey)
	}
	q += ` ORDER BY c.departure_sec,c.connection_key`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	serviceDate := date.In(localtime.Melbourne()).Format("20060102")
	var out []DepartureResult
	for rows.Next() {
		var d DepartureResult
		if err := rows.Scan(&d.TripID, &d.RouteID, &d.StopID, &d.StopSequence, &d.DepartureSec, &d.ArrivalSec, &d.Headsign, &d.RouteShortName, &d.RouteLongName, &d.FeedMode, &d.DirectionID, &d.BlockID, &d.PickupType, &d.DropOffType); err != nil {
			return nil, err
		}
		d.ServiceDate = serviceDate
		out = append(out, d)
	}
	return out, rows.Err()
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

func (s *Store) TripDetail(ctx context.Context, tripID string, date time.Time) (*TripDetailResult, error) {
	active, err := s.activeServiceKeys(ctx, date.In(localtime.Melbourne()))
	if err != nil {
		return nil, err
	}
	if len(active) == 0 {
		return nil, fmt.Errorf("%w: no active service on %s", ErrNotFound, date.Format("2006-01-02"))
	}
	cond := "t.trip_id=?"
	args := []any{tripID}
	if mode, source, ok := splitPublicID(tripID); ok {
		cond = "t.feed_mode=? AND t.source_trip_id=?"
		args = []any{mode, source}
	}
	q := `SELECT t.trip_id,r.route_id,coalesce(t.trip_headsign,''),coalesce(t.direction_id,0),coalesce(t.block_id,''),coalesce(t.service_id,''),t.feed_mode,t.trip_key FROM trips t JOIN routes r ON r.route_key=t.route_key WHERE ` + cond + ` AND t.service_key IN (` + placeholders(len(active)) + `)`
	for _, k := range active {
		args = append(args, k)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var found struct {
		result TripDetailResult
		key    int64
	}
	count := 0
	for rows.Next() {
		var r TripDetailResult
		var key int64
		if err := rows.Scan(&r.TripID, &r.RouteID, &r.Headsign, &r.DirectionID, &r.BlockID, &r.ServiceID, &r.FeedMode, &key); err != nil {
			return nil, err
		}
		found = struct {
			result TripDetailResult
			key    int64
		}{r, key}
		count++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, fmt.Errorf("%w: trip %q", ErrNotFound, tripID)
	}
	if count > 1 {
		return nil, fmt.Errorf("%w: trip %q", &AmbiguousIDError{Kind: "trip", Query: tripID}, tripID)
	}
	rows, err = s.db.QueryContext(ctx, `SELECT s.stop_id,coalesce(s.stop_name,''),coalesce(s.stop_lat,0),coalesce(s.stop_lon,0),st.stop_sequence,st.arrival_sec,st.departure_sec,st.pickup_type,st.drop_off_type FROM stop_times st JOIN stops s ON s.stop_key=st.stop_key WHERE st.trip_key=? ORDER BY st.stop_sequence`, found.key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var stop TripStopResult
		if err := rows.Scan(&stop.StopID, &stop.StopName, &stop.StopLat, &stop.StopLon, &stop.StopSequence, &stop.ArrivalSec, &stop.DepartureSec, &stop.PickupType, &stop.DropOffType); err != nil {
			return nil, err
		}
		found.result.Stops = append(found.result.Stops, stop)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &found.result, nil
}

// ResolveTripSourceID returns namespaced static IDs for one feed-local trip
// identity. It is used by realtime Phase 0 validation and never picks a
// namespace implicitly when the source ID is ambiguous.
func (s *Store) ResolveTripSourceID(ctx context.Context, sourceID string, feedMode int, serviceDate string) ([]string, error) {
	query := `SELECT t.trip_id FROM trips t WHERE t.source_trip_id=? AND t.feed_mode=?`
	args := []any{sourceID, feedMode}
	if strings.TrimSpace(serviceDate) != "" {
		query += ` AND t.service_key IN (SELECT service_key FROM calendar WHERE service_id=t.service_id AND start_date<=? AND end_date>=?)`
		args = append(args, serviceDate, serviceDate)
	}
	query += ` ORDER BY t.trip_id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

func (s *Store) StopDetail(ctx context.Context, stopID string) (*StopDetailResult, error) {
	stop, key, err := s.stopByID(ctx, stopID, nil)
	if err != nil {
		return nil, err
	}
	result := &StopDetailResult{Stop: stop, Routes: []RouteResult{}, Transfers: []TransferResult{}, Pathways: []PathwayResult{}}
	keys := []int64{key}
	if stop.LocationType == 1 {
		rows, e := s.db.QueryContext(ctx, `SELECT stop_key FROM stops WHERE parent_stop_key=? ORDER BY stop_key`, key)
		if e != nil {
			return nil, e
		}
		defer rows.Close()
		for rows.Next() {
			var child int64
			if e := rows.Scan(&child); e != nil {
				return nil, e
			}
			keys = append(keys, child)
		}
	}
	keyArgs := make([]any, len(keys))
	for i, k := range keys {
		keyArgs[i] = k
	}
	in := placeholders(len(keys))
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT r.route_id,coalesce(r.route_short_name,''),coalesce(r.route_long_name,''),r.feed_mode,coalesce(r.route_type,0) FROM trips t JOIN routes r ON r.route_key=t.route_key JOIN stop_times st ON st.trip_key=t.trip_key WHERE st.stop_key IN (`+in+`) ORDER BY r.feed_mode,r.route_id`, keyArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		r, e := routeFromRow(func(dest ...any) error { return rows.Scan(dest...) })
		if e != nil {
			return nil, e
		}
		result.Routes = append(result.Routes, r)
	}
	rows, err = s.db.QueryContext(ctx, `SELECT from_stop_id,to_stop_id,coalesce(transfer_type,0),coalesce(min_transfer_time,0),source FROM transfers WHERE from_stop_key IN (`+in+`) OR to_stop_key IN (`+in+`) ORDER BY transfer_key`, append(keyArgs, keyArgs...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var tr TransferResult
		if err := rows.Scan(&tr.FromStopID, &tr.ToStopID, &tr.TransferType, &tr.MinTransferTime, &tr.Source); err != nil {
			return nil, err
		}
		result.Transfers = append(result.Transfers, tr)
	}
	rows, err = s.db.QueryContext(ctx, `SELECT pathway_id,from_stop_id,to_stop_id,pathway_mode,is_bidirectional,length,traversal_time,signposted_as,reversed_signposted_as FROM pathways WHERE from_stop_key IN (`+in+`) OR to_stop_key IN (`+in+`) ORDER BY pathway_key`, append(keyArgs, keyArgs...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p PathwayResult
		var bidi int
		var length sql.NullFloat64
		var traversal sql.NullInt64
		var sign, reversed sql.NullString
		if err := rows.Scan(&p.PathwayID, &p.FromStopID, &p.ToStopID, &p.PathwayMode, &bidi, &length, &traversal, &sign, &reversed); err != nil {
			return nil, err
		}
		p.IsBidirectional = bidi != 0
		if length.Valid {
			v := length.Float64
			p.Length = &v
		}
		if traversal.Valid {
			v := int(traversal.Int64)
			p.TraversalTime = &v
		}
		p.SignpostedAs = sign.String
		p.ReversedSignpostedAs = reversed.String
		result.Pathways = append(result.Pathways, p)
	}
	return result, nil
}

func (s *Store) RoutesServingStop(ctx context.Context, stopID string) ([]RouteResult, error) {
	detail, err := s.StopDetail(ctx, stopID)
	if err != nil {
		return nil, err
	}
	return detail.Routes, nil
}

func (s *Store) stopByKey(ctx context.Context, key int64) (StopResult, error) {
	row := s.db.QueryRowContext(ctx, `SELECT s.stop_id,coalesce(s.stop_name,''),coalesce(s.stop_lat,0),coalesce(s.stop_lon,0),s.feed_mode,p.stop_id,s.location_type,s.wheelchair_boarding FROM stops s LEFT JOIN stops p ON p.stop_key=s.parent_stop_key WHERE s.stop_key=?`, key)
	return stopFromRow(func(dest ...any) error { return row.Scan(dest...) })
}
