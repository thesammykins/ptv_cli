package gtfs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/thesammykins/ptv_cli/internal/localtime"
	"github.com/thesammykins/ptv_cli/internal/model"
)

const (
	defaultTransferSeconds = 120
	TimetableHorizon       = 36 * time.Hour
)

// TimetableDirection selects the bounded service window around a query time.
type TimetableDirection uint8

const (
	TimetableForward TimetableDirection = iota
	TimetableReverse
)

var ErrQueryOutsideCoverage = errors.New("query is outside GTFS service coverage")

// CoverageOutsideError reports the checked generation coverage for callers
// that need an actionable update message.
type CoverageOutsideError struct {
	Query    time.Time
	Coverage ServiceCoverage
}

func (e *CoverageOutsideError) Error() string {
	return fmt.Sprintf("%s: query date %s, available %s to %s",
		ErrQueryOutsideCoverage, e.Query.In(localtime.Melbourne()).Format("2006-01-02"),
		formatCoverageDate(e.Coverage.Start), formatCoverageDate(e.Coverage.End))
}

func (e *CoverageOutsideError) Unwrap() error { return ErrQueryOutsideCoverage }

// LoadTimetable preserves the legacy API and loads a forward 36-hour window.
func (s *Store) LoadTimetable(queryTime time.Time) (*model.Timetable, error) {
	return s.LoadTimetableContext(context.Background(), queryTime, TimetableForward)
}

// LoadTimetableContext loads connection templates for active service instances
// intersecting the direction-specific 36-hour window.
func (s *Store) LoadTimetableContext(ctx context.Context, queryTime time.Time, direction TimetableDirection) (*model.Timetable, error) {
	return s.LoadTimetableWindowContext(ctx, queryTime, direction, TimetableHorizon)
}

// LoadTimetableWindowContext loads one preordered connection view for a bounded
// planning horizon. Shorter horizons keep the common interactive path fast;
// callers can retry with a larger horizon when no journey is found.
func (s *Store) LoadTimetableWindowContext(ctx context.Context, queryTime time.Time, direction TimetableDirection, horizon time.Duration) (*model.Timetable, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if direction != TimetableForward && direction != TimetableReverse {
		return nil, fmt.Errorf("invalid timetable direction %d", direction)
	}
	if horizon <= 0 || horizon > TimetableHorizon {
		return nil, fmt.Errorf("timetable horizon must be within (0, %s]", TimetableHorizon)
	}
	queryTime = queryTime.In(localtime.Melbourne())
	coverage, err := s.queryCoverage(ctx)
	if err != nil {
		return nil, err
	}
	queryDate := queryTime.Format("20060102")
	outsideCoverage := queryDate < coverage.Start || queryDate > coverage.End
	windowStart, windowEnd := queryTime, queryTime.Add(horizon)
	if direction == TimetableReverse {
		windowStart, windowEnd = queryTime.Add(-horizon), queryTime
	}
	day := time.Date(queryTime.Year(), queryTime.Month(), queryTime.Day(), 0, 0, 0, 0, localtime.Melbourne())
	timetable := &model.Timetable{
		Day:           day,
		TripRoute:     make(map[string]int),
		TripHeadsign:  make(map[string]string),
		TripBlock:     make(map[string]string),
		NameIndex:     make(map[string][]int),
		TripInstances: []model.ServiceInstance{{}},
	}
	index, err := s.loadQueryStops(ctx, timetable)
	if err != nil {
		return nil, err
	}
	if err := s.loadQueryRoutes(ctx, timetable, index); err != nil {
		return nil, err
	}
	window, err := s.loadConnectionWindow(ctx, index, windowStart, windowEnd, direction)
	if err != nil {
		return nil, err
	}
	if outsideCoverage && len(window.forward) == 0 && len(window.reverse) == 0 {
		return nil, &CoverageOutsideError{Query: queryTime, Coverage: coverage}
	}
	timetable.Connections = window.forward
	timetable.ReverseConnections = window.reverse
	timetable.TripInstances = window.instances
	index.instancesByTrip = window.instancesByTrip
	index.instanceTrips = window.instanceTrips

	walk, reverseWalk, rules, err := s.loadWalkingAndTransferRules(ctx, index)
	if err != nil {
		return nil, err
	}
	timetable.WalkEdges = walk
	timetable.ReverseWalkEdges = reverseWalk
	timetable.Footpaths = walk
	timetable.TransferRules = rules
	timetable.Continuations, err = s.loadContinuations(ctx, index, window.bounds)
	if err != nil {
		return nil, err
	}
	timetable.BuildStopModes()
	return timetable, nil
}

type queryStop struct {
	key          int64
	index        int
	locationType int
	parentKey    int64
}

type queryIndex struct {
	stopsByKey      map[int64]queryStop
	stopChildren    map[int64][]int64
	routesByKey     map[int64]int
	instancesByTrip map[int64][]model.TripInstanceID
	instanceTrips   map[model.TripInstanceID]int64
}

func (s *Store) loadQueryStops(ctx context.Context, timetable *model.Timetable) (*queryIndex, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT stop_key,stop_id,coalesce(stop_name,''),coalesce(stop_lat,0),coalesce(stop_lon,0),
		       feed_mode,location_type,parent_stop_key
		FROM stops ORDER BY stop_key`)
	if err != nil {
		return nil, fmt.Errorf("loading GTFS stops: %w", err)
	}
	defer rows.Close()
	index := &queryIndex{
		stopsByKey: make(map[int64]queryStop), stopChildren: make(map[int64][]int64),
		routesByKey: make(map[int64]int), instancesByTrip: make(map[int64][]model.TripInstanceID),
		instanceTrips: make(map[model.TripInstanceID]int64),
	}
	var stationKeys []int64
	for rows.Next() {
		if len(timetable.Stops)&4095 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		var stop model.Stop
		var key int64
		var locationType int
		var parent sql.NullInt64
		if err := rows.Scan(&key, &stop.ID, &stop.Name, &stop.Lat, &stop.Lon, &stop.Mode, &locationType, &parent); err != nil {
			return nil, err
		}
		stop.Index = len(timetable.Stops)
		timetable.Stops = append(timetable.Stops, stop)
		metadata := queryStop{key: key, index: stop.Index, locationType: locationType}
		if parent.Valid {
			metadata.parentKey = parent.Int64
			index.stopChildren[parent.Int64] = append(index.stopChildren[parent.Int64], key)
		}
		index.stopsByKey[key] = metadata
		if locationType == 1 {
			stationKeys = append(stationKeys, key)
		}
		if name := strings.ToLower(strings.TrimSpace(stop.Name)); name != "" && locationType == 0 {
			timetable.NameIndex[name] = append(timetable.NameIndex[name], stop.Index)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Parent stations are user-visible names, but trips depart from their child
	// platforms. Resolve a station query directly to those routable platforms;
	// seeding the parent node itself would strand the journey outside the
	// connection graph when pathways only connect platform/entrance nodes.
	for _, key := range stationKeys {
		station := index.stopsByKey[key]
		name := strings.ToLower(strings.TrimSpace(timetable.Stops[station.index].Name))
		if name == "" {
			continue
		}
		timetable.NameIndex[name] = append(
			timetable.NameIndex[name], queryPlatformDescendants(index, key)...,
		)
	}
	return index, nil
}

func queryPlatformDescendants(index *queryIndex, root int64) []int {
	var result []int
	var visit func(int64)
	visit = func(parent int64) {
		for _, childKey := range index.stopChildren[parent] {
			child, ok := index.stopsByKey[childKey]
			if !ok {
				continue
			}
			if child.locationType == 0 {
				result = append(result, child.index)
				continue
			}
			visit(childKey)
		}
	}
	visit(root)
	return result
}

func (s *Store) loadQueryRoutes(ctx context.Context, timetable *model.Timetable, index *queryIndex) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT route_key,coalesce(route_short_name,''),coalesce(route_long_name,''),route_type,feed_mode
		FROM routes ORDER BY route_key`)
	if err != nil {
		return fmt.Errorf("loading GTFS routes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key int64
		var routeType, feedMode int
		var route model.RouteInfo
		if err := rows.Scan(&key, &route.ShortName, &route.LongName, &routeType, &feedMode); err != nil {
			return err
		}
		route.RouteType = modeFromRouteType(routeType, feedMode)
		index.routesByKey[key] = len(timetable.Routes)
		timetable.Routes = append(timetable.Routes, route)
	}
	return rows.Err()
}

func (s *Store) loadWalkingAndTransferRules(ctx context.Context, index *queryIndex) ([][]model.WalkEdge, [][]model.WalkEdge, []model.TransferRule, error) {
	walk := make([][]model.WalkEdge, len(index.stopsByKey))
	edgeBest := make(map[struct{ from, to int }]model.WalkEdge)
	addEdge := func(from, to, seconds int, kind model.WalkEdgeKind) {
		if from < 0 || to < 0 || from == to {
			return
		}
		if seconds <= 0 {
			seconds = defaultTransferSeconds
		}
		key := struct{ from, to int }{from, to}
		candidate := model.WalkEdge{ToStop: to, Seconds: seconds, Kind: kind}
		existing, ok := edgeBest[key]
		candidatePathway := candidate.Kind == model.WalkEdgePathway
		existingPathway := ok && existing.Kind == model.WalkEdgePathway
		if !ok || (candidatePathway && !existingPathway) ||
			(candidatePathway == existingPathway && candidate.Seconds < existing.Seconds) {
			edgeBest[key] = candidate
		}
	}

	pathRows, err := s.db.QueryContext(ctx, `
		SELECT from_stop_key,to_stop_key,pathway_mode,is_bidirectional,length,traversal_time FROM pathways`)
	if err != nil {
		return nil, nil, nil, err
	}
	for pathRows.Next() {
		var fromKey, toKey int64
		var mode, bidirectional int
		var length sql.NullFloat64
		var traversal sql.NullInt64
		if err := pathRows.Scan(&fromKey, &toKey, &mode, &bidirectional, &length, &traversal); err != nil {
			pathRows.Close()
			return nil, nil, nil, err
		}
		from, fromOK := index.stopsByKey[fromKey]
		to, toOK := index.stopsByKey[toKey]
		if !fromOK || !toOK {
			continue
		}
		seconds := pathwaySeconds(mode, length, traversal)
		addEdge(from.index, to.index, seconds, model.WalkEdgePathway)
		if bidirectional == 1 {
			addEdge(to.index, from.index, seconds, model.WalkEdgePathway)
		}
	}
	if err := pathRows.Close(); err != nil {
		return nil, nil, nil, err
	}
	if err := pathRows.Err(); err != nil {
		return nil, nil, nil, err
	}

	transferRows, err := s.db.QueryContext(ctx, `
		SELECT source,from_stop_key,to_stop_key,transfer_type,min_transfer_time,
		       from_route_key,to_route_key,from_trip_key,to_trip_key
		FROM transfers WHERE transfer_type BETWEEN 0 AND 3`)
	if err != nil {
		return nil, nil, nil, err
	}
	defer transferRows.Close()
	var rules []model.TransferRule
	ruleSeen := make(map[model.TransferRule]bool)
	for transferRows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, err
		}
		var source string
		var fromStop, toStop, fromRoute, toRoute, fromTrip, toTrip sql.NullInt64
		var transferType, minimum int
		if err := transferRows.Scan(&source, &fromStop, &toStop, &transferType, &minimum, &fromRoute, &toRoute, &fromTrip, &toTrip); err != nil {
			return nil, nil, nil, err
		}
		fromStops := expandRuleStops(fromStop, index)
		toStops := expandRuleStops(toStop, index)
		if source == "proximity" {
			for _, from := range fromStops {
				for _, to := range toStops {
					addEdge(from, to, minimum, model.WalkEdgeProximity)
				}
			}
			continue
		}
		if transferType < 3 {
			physicalSeconds := minimum
			if fromRoute.Valid || toRoute.Valid || fromTrip.Valid || toTrip.Valid {
				// Route/trip-qualified minimums are enforced by TransferRule below;
				// they must not become the physical duration for every rider.
				physicalSeconds = 0
			}
			for _, from := range fromStops {
				for _, to := range toStops {
					addEdge(from, to, physicalSeconds, model.WalkEdgeExplicitTransfer)
				}
			}
		}
		fromRoutes := queryRouteIndex(fromRoute, index)
		toRoutes := queryRouteIndex(toRoute, index)
		fromInstances := queryTripInstances(fromTrip, index)
		toInstances := queryTripInstances(toTrip, index)
		if len(fromInstances) == 0 || len(toInstances) == 0 {
			continue
		}
		for _, from := range fromStops {
			for _, to := range toStops {
				for _, fromInstance := range fromInstances {
					for _, toInstance := range toInstances {
						rule := model.TransferRule{
							FromStop: from, ToStop: to, Type: model.TransferType(transferType),
							MinTransferSeconds: minimum, FromRouteIdx: fromRoutes, ToRouteIdx: toRoutes,
							FromTripInstanceID: fromInstance, ToTripInstanceID: toInstance,
						}
						if !ruleSeen[rule] {
							ruleSeen[rule] = true
							rules = append(rules, rule)
						}
					}
				}
			}
		}
	}
	if err := transferRows.Err(); err != nil {
		return nil, nil, nil, err
	}
	for key, edge := range edgeBest {
		walk[key.from] = append(walk[key.from], edge)
	}
	for from := range walk {
		sort.Slice(walk[from], func(i, j int) bool {
			if walk[from][i].ToStop != walk[from][j].ToStop {
				return walk[from][i].ToStop < walk[from][j].ToStop
			}
			if walk[from][i].Seconds != walk[from][j].Seconds {
				return walk[from][i].Seconds < walk[from][j].Seconds
			}
			return walk[from][i].Kind < walk[from][j].Kind
		})
	}
	reverse := make([][]model.WalkEdge, len(walk))
	for from, edges := range walk {
		for _, edge := range edges {
			reverse[edge.ToStop] = append(reverse[edge.ToStop], model.WalkEdge{ToStop: from, Seconds: edge.Seconds, Kind: edge.Kind})
		}
	}
	for to := range reverse {
		sort.Slice(reverse[to], func(i, j int) bool {
			if reverse[to][i].ToStop != reverse[to][j].ToStop {
				return reverse[to][i].ToStop < reverse[to][j].ToStop
			}
			if reverse[to][i].Seconds != reverse[to][j].Seconds {
				return reverse[to][i].Seconds < reverse[to][j].Seconds
			}
			return reverse[to][i].Kind < reverse[to][j].Kind
		})
	}
	return walk, reverse, rules, nil
}

func expandRuleStops(value sql.NullInt64, index *queryIndex) []int {
	if !value.Valid {
		return []int{-1}
	}
	stop, ok := index.stopsByKey[value.Int64]
	if !ok {
		return nil
	}
	if stop.locationType == 0 {
		return []int{stop.index}
	}
	queue := append([]int64(nil), index.stopChildren[value.Int64]...)
	seen := make(map[int64]bool)
	var result []int
	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]
		if seen[key] {
			continue
		}
		seen[key] = true
		child, ok := index.stopsByKey[key]
		if !ok {
			continue
		}
		if child.locationType == 0 {
			result = append(result, child.index)
		} else {
			queue = append(queue, index.stopChildren[key]...)
		}
	}
	return result
}

func queryRouteIndex(value sql.NullInt64, index *queryIndex) int {
	if !value.Valid {
		return -1
	}
	if route, ok := index.routesByKey[value.Int64]; ok {
		return route
	}
	return -1
}

func queryTripInstances(value sql.NullInt64, index *queryIndex) []model.TripInstanceID {
	if !value.Valid {
		return []model.TripInstanceID{model.UnknownTripInstanceID}
	}
	return index.instancesByTrip[value.Int64]
}

func pathwaySeconds(mode int, length sql.NullFloat64, traversal sql.NullInt64) int {
	if traversal.Valid && traversal.Int64 > 0 {
		return int(traversal.Int64)
	}
	if length.Valid && length.Float64 > 0 {
		speed := walkMetersPerSec
		if mode == 2 {
			speed = 0.7
		}
		return max(1, int(math.Ceil(length.Float64/speed)))
	}
	return defaultTransferSeconds
}

func (s *Store) queryCoverage(ctx context.Context) (ServiceCoverage, error) {
	if state, err := s.DatasetState(ctx); err == nil {
		return state.Coverage, nil
	} else if !errors.Is(err, ErrDatasetStateMissing) {
		return ServiceCoverage{}, err
	}
	var coverage ServiceCoverage
	err := s.db.QueryRowContext(ctx, `
		SELECT coalesce(MIN(day),''),coalesce(MAX(day),'') FROM (
			SELECT start_date AS day FROM calendar
			WHERE start_date != '' AND (monday+tuesday+wednesday+thursday+friday+saturday+sunday)>0
			UNION ALL SELECT end_date FROM calendar
			WHERE end_date != '' AND (monday+tuesday+wednesday+thursday+friday+saturday+sunday)>0
			UNION ALL SELECT date FROM calendar_dates WHERE date != '' AND exception_type=1
		)`).Scan(&coverage.Start, &coverage.End)
	if err != nil {
		return ServiceCoverage{}, err
	}
	if coverage.Start == "" || coverage.End == "" {
		return ServiceCoverage{}, ErrDatasetStateMissing
	}
	return coverage, nil
}

func formatCoverageDate(value string) string {
	parsed, err := time.Parse("20060102", value)
	if err != nil {
		return value
	}
	return parsed.Format("2006-01-02")
}

func modeFromRouteType(routeType, feedMode int) int {
	if feedMode > 0 {
		return feedMode
	}
	switch routeType {
	case 0, 900, 901, 902, 903, 904, 905, 906:
		return 3
	case 1, 400, 401, 402, 403, 404, 405:
		return 2
	case 2, 100, 101, 102, 103, 104, 105, 106, 107, 108, 109:
		return 1
	case 3, 700, 701, 702, 703, 704, 705, 706, 707, 708, 709:
		return 4
	case 200, 201, 202, 203, 204:
		return 5
	default:
		return feedMode
	}
}

func feedModeFromID(id string) int {
	separator := strings.IndexByte(id, ':')
	if separator <= 0 {
		return -1
	}
	mode := 0
	for _, character := range id[:separator] {
		if character < '0' || character > '9' {
			return -1
		}
		mode = mode*10 + int(character-'0')
	}
	return mode
}
