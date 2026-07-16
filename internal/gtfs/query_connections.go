package gtfs

import (
	"container/heap"
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/thesammykins/ptv_cli/internal/localtime"
	"github.com/thesammykins/ptv_cli/internal/model"
)

const maxSQLiteQueryParameters = 500

type serviceDateQuery struct {
	day         time.Time
	anchor      time.Time
	serviceKeys []int64
	lowSeconds  int
	highSeconds int
}

type tripInstanceKey struct {
	feedKey    int64
	serviceDay string
	tripKey    int64
}

type connectionRecord struct {
	connection  model.Connection
	orderKey    int64
	feedKey     int64
	tripKey     int64
	depStopKey  int64
	arrStopKey  int64
	depSequence int
	arrSequence int
}

type tripWindowBounds struct {
	id           model.TripInstanceID
	feedKey      int64
	tripKey      int64
	firstStopKey int64
	lastStopKey  int64
	firstStop    int
	lastStop     int
	firstTime    int64
	lastTime     int64
	haveFirst    bool
	haveLast     bool
}

type connectionWindow struct {
	forward         []model.Connection
	reverse         []model.Connection
	instances       []model.ServiceInstance
	instancesByTrip map[int64][]model.TripInstanceID
	instanceTrips   map[model.TripInstanceID]int64
	bounds          map[model.TripInstanceID]*tripWindowBounds
}

func (s *Store) loadConnectionWindow(ctx context.Context, index *queryIndex, start, end time.Time, direction TimetableDirection) (connectionWindow, error) {
	dates, err := s.serviceDatesForWindow(ctx, start, end)
	if err != nil {
		return connectionWindow{}, err
	}
	window := connectionWindow{
		instances:       []model.ServiceInstance{{}},
		instancesByTrip: make(map[int64][]model.TripInstanceID),
		instanceTrips:   make(map[model.TripInstanceID]int64),
		bounds:          make(map[model.TripInstanceID]*tripWindowBounds),
	}
	instanceIDs := make(map[tripInstanceKey]model.TripInstanceID)
	allocate := func(record connectionRow, day time.Time) (model.TripInstanceID, error) {
		key := tripInstanceKey{feedKey: record.feedKey, serviceDay: day.Format("20060102"), tripKey: record.tripKey}
		if id := instanceIDs[key]; id != model.UnknownTripInstanceID {
			return id, nil
		}
		if len(window.instances) >= int(^model.TripInstanceID(0)) {
			return 0, fmt.Errorf("too many active GTFS trip instances")
		}
		id := model.TripInstanceID(len(window.instances))
		routeIndex, ok := index.routesByKey[record.routeKey]
		if !ok {
			return 0, fmt.Errorf("connection references missing route_key %d", record.routeKey)
		}
		instanceIDs[key] = id
		window.instances = append(window.instances, model.ServiceInstance{
			ID: id, FeedMode: record.feedMode, ServiceDate: day,
			TripID: record.tripID, RouteIdx: routeIndex, Headsign: record.headsign, BlockID: record.blockID,
		})
		window.instancesByTrip[record.tripKey] = append(window.instancesByTrip[record.tripKey], id)
		window.instanceTrips[id] = record.tripKey
		window.bounds[id] = &tripWindowBounds{id: id, feedKey: record.feedKey, tripKey: record.tripKey}
		return id, nil
	}

	loadRecords := func(reverse bool) ([]connectionRecord, error) {
		var streams [][]connectionRecord
		for _, date := range dates {
			for offset := 0; offset < len(date.serviceKeys); offset += maxSQLiteQueryParameters {
				endOffset := min(offset+maxSQLiteQueryParameters, len(date.serviceKeys))
				stream, err := s.queryConnectionStream(ctx, date, date.serviceKeys[offset:endOffset], reverse, index, allocate)
				if err != nil {
					return nil, err
				}
				if len(stream) > 0 {
					streams = append(streams, stream)
				}
			}
		}
		return mergeConnectionStreams(streams), nil
	}

	var boundRecords []connectionRecord
	if direction == TimetableForward {
		forwardRecords, err := loadRecords(false)
		if err != nil {
			return connectionWindow{}, err
		}
		boundRecords = forwardRecords
		window.forward = make([]model.Connection, len(forwardRecords))
		for i, record := range forwardRecords {
			window.forward[i] = record.connection
		}
	} else {
		reverseRecords, err := loadRecords(true)
		if err != nil {
			return connectionWindow{}, err
		}
		boundRecords = reverseRecords
		window.reverse = make([]model.Connection, len(reverseRecords))
		for i, record := range reverseRecords {
			window.reverse[i] = record.connection
		}
	}

	templateBounds, err := s.loadTemplateSequenceBounds(ctx, window.instancesByTrip)
	if err != nil {
		return connectionWindow{}, err
	}
	for _, record := range boundRecords {
		bounds := window.bounds[record.connection.TripInstanceID]
		template := templateBounds[record.tripKey]
		if record.depSequence == template.firstSequence {
			bounds.firstStopKey = record.depStopKey
			bounds.firstStop = index.stopsByKey[record.depStopKey].index
			bounds.firstTime = record.connection.DepTime
			if direction == TimetableReverse {
				bounds.firstTime = -record.connection.ArrTime
			}
			bounds.haveFirst = true
		}
		if record.arrSequence == template.lastSequence {
			bounds.lastStopKey = record.arrStopKey
			bounds.lastStop = index.stopsByKey[record.arrStopKey].index
			bounds.lastTime = record.connection.ArrTime
			if direction == TimetableReverse {
				bounds.lastTime = -record.connection.DepTime
			}
			bounds.haveLast = true
		}
	}
	return window, nil
}

func (s *Store) serviceDatesForWindow(ctx context.Context, start, end time.Time) ([]serviceDateQuery, error) {
	start = start.In(localtime.Melbourne())
	end = end.In(localtime.Melbourne())
	lookback := start.Add(-time.Duration(maxGTFSSeconds) * time.Second)
	first := time.Date(lookback.Year(), lookback.Month(), lookback.Day(), 12, 0, 0, 0, localtime.Melbourne())
	last := time.Date(end.Year(), end.Month(), end.Day(), 12, 0, 0, 0, localtime.Melbourne())
	var result []serviceDateQuery
	for day := first; !day.After(last); day = day.AddDate(0, 0, 1) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		services, err := s.activeServiceKeys(ctx, day)
		if err != nil {
			return nil, err
		}
		if len(services) == 0 {
			continue
		}
		anchor := localtime.ServiceDayAnchor(day)
		low := int(math.Floor(start.Sub(anchor).Seconds()))
		high := int(math.Ceil(end.Sub(anchor).Seconds()))
		if high < 0 || low > maxGTFSSeconds {
			continue
		}
		low = max(0, low)
		high = min(maxGTFSSeconds, high)
		result = append(result, serviceDateQuery{day: day, anchor: anchor, serviceKeys: services, lowSeconds: low, highSeconds: high})
	}
	return result, nil
}

func (s *Store) activeServiceKeys(ctx context.Context, day time.Time) ([]int64, error) {
	date := day.Format("20060102")
	weekday := weekdayColumn(day.Weekday())
	active := make(map[int64]bool)
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(
		`SELECT service_key FROM calendar WHERE %s=1 AND start_date<=? AND end_date>=?`, weekday), date, date)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var key int64
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return nil, err
		}
		active[key] = true
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	exceptions, err := s.db.QueryContext(ctx, `SELECT service_key,exception_type FROM calendar_dates WHERE date=?`, date)
	if err != nil {
		return nil, err
	}
	for exceptions.Next() {
		var key int64
		var exception int
		if err := exceptions.Scan(&key, &exception); err != nil {
			exceptions.Close()
			return nil, err
		}
		if exception == 1 {
			active[key] = true
		} else if exception == 2 {
			delete(active, key)
		}
	}
	if err := exceptions.Close(); err != nil {
		return nil, err
	}
	if err := exceptions.Err(); err != nil {
		return nil, err
	}
	keys := make([]int64, 0, len(active))
	for key := range active {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys, nil
}

type connectionRow struct {
	connectionKey int64
	feedKey       int64
	feedMode      int
	serviceKey    int64
	tripKey       int64
	routeKey      int64
	depStopKey    int64
	arrStopKey    int64
	depSequence   int
	arrSequence   int
	departureSec  int
	arrivalSec    int
	pickupType    int
	dropOffType   int
	tripID        string
	headsign      string
	blockID       string
}

func (s *Store) queryConnectionStream(
	ctx context.Context,
	date serviceDateQuery,
	serviceKeys []int64,
	reverse bool,
	index *queryIndex,
	allocate func(connectionRow, time.Time) (model.TripInstanceID, error),
) ([]connectionRecord, error) {
	if len(serviceKeys) == 0 {
		return nil, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(serviceKeys)), ",")
	order := "c.departure_sec,c.connection_key"
	if reverse {
		order = "c.arrival_sec DESC,c.connection_key DESC"
	}
	timePredicate := "c.departure_sec>=? AND c.departure_sec<=?"
	if reverse {
		timePredicate = "c.arrival_sec>=? AND c.arrival_sec<=?"
	}
	query := `SELECT c.connection_key,c.feed_key,c.feed_mode,c.service_key,c.trip_key,c.route_key,
		c.dep_stop_key,c.arr_stop_key,c.dep_sequence,c.arr_sequence,c.departure_sec,c.arrival_sec,
		c.pickup_type,c.drop_off_type,t.trip_id,coalesce(t.trip_headsign,''),coalesce(t.block_id,'')
		FROM connections c JOIN trips t ON t.trip_key=c.trip_key
		WHERE c.service_key IN (` + placeholders + `) AND ` + timePredicate + `
		ORDER BY ` + order
	args := make([]any, 0, len(serviceKeys)+2)
	for _, key := range serviceKeys {
		args = append(args, key)
	}
	args = append(args, date.lowSeconds, date.highSeconds)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("loading bounded GTFS connections: %w", err)
	}
	defer rows.Close()
	var result []connectionRecord
	for rows.Next() {
		if len(result)&4095 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		var row connectionRow
		if err := rows.Scan(
			&row.connectionKey, &row.feedKey, &row.feedMode, &row.serviceKey, &row.tripKey, &row.routeKey,
			&row.depStopKey, &row.arrStopKey, &row.depSequence, &row.arrSequence, &row.departureSec, &row.arrivalSec,
			&row.pickupType, &row.dropOffType, &row.tripID, &row.headsign, &row.blockID,
		); err != nil {
			return nil, err
		}
		depStop, depOK := index.stopsByKey[row.depStopKey]
		arrStop, arrOK := index.stopsByKey[row.arrStopKey]
		routeIndex, routeOK := index.routesByKey[row.routeKey]
		if !depOK || !arrOK || !routeOK {
			return nil, fmt.Errorf("connection %d has unresolved stop or route key", row.connectionKey)
		}
		instanceID, err := allocate(row, date.day)
		if err != nil {
			return nil, err
		}
		departure := date.anchor.Unix() + int64(row.departureSec)
		arrival := date.anchor.Unix() + int64(row.arrivalSec)
		connection := model.Connection{
			DepStop: depStop.index, ArrStop: arrStop.index, DepTime: departure, ArrTime: arrival,
			TripID: row.tripID, TripInstanceID: instanceID, RouteIdx: routeIndex, BlockID: row.blockID,
			PickupPolicy: model.PassengerActionPolicy(row.pickupType), DropOffPolicy: model.PassengerActionPolicy(row.dropOffType),
		}
		if reverse {
			connection = model.Connection{
				DepStop: connection.ArrStop, ArrStop: connection.DepStop,
				DepTime: -connection.ArrTime, ArrTime: -connection.DepTime,
				TripID: connection.TripID, TripInstanceID: connection.TripInstanceID,
				RouteIdx: connection.RouteIdx, BlockID: connection.BlockID,
				PickupPolicy: connection.DropOffPolicy, DropOffPolicy: connection.PickupPolicy,
			}
		}
		orderKey := row.connectionKey
		if reverse {
			orderKey = -orderKey
		}
		result = append(result, connectionRecord{
			connection: connection, orderKey: orderKey, feedKey: row.feedKey, tripKey: row.tripKey,
			depStopKey: row.depStopKey, arrStopKey: row.arrStopKey,
			depSequence: row.depSequence, arrSequence: row.arrSequence,
		})
	}
	return result, rows.Err()
}

type streamCursor struct {
	stream   int
	index    int
	key      int64
	orderKey int64
	instance model.TripInstanceID
}

type cursorHeap []streamCursor

func (h cursorHeap) Len() int { return len(h) }
func (h cursorHeap) Less(i, j int) bool {
	if h[i].key != h[j].key {
		return h[i].key < h[j].key
	}
	if h[i].orderKey != h[j].orderKey {
		return h[i].orderKey < h[j].orderKey
	}
	if h[i].instance != h[j].instance {
		return h[i].instance < h[j].instance
	}
	return h[i].stream < h[j].stream
}
func (h cursorHeap) Swap(i, j int)   { h[i], h[j] = h[j], h[i] }
func (h *cursorHeap) Push(value any) { *h = append(*h, value.(streamCursor)) }
func (h *cursorHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}

func mergeConnectionStreams(streams [][]connectionRecord) []connectionRecord {
	total := 0
	queue := &cursorHeap{}
	for stream, records := range streams {
		total += len(records)
		if len(records) > 0 {
			heap.Push(queue, streamCursor{
				stream: stream, key: records[0].connection.DepTime,
				orderKey: records[0].orderKey, instance: records[0].connection.TripInstanceID,
			})
		}
	}
	result := make([]connectionRecord, 0, total)
	for queue.Len() > 0 {
		cursor := heap.Pop(queue).(streamCursor)
		record := streams[cursor.stream][cursor.index]
		result = append(result, record)
		cursor.index++
		if cursor.index < len(streams[cursor.stream]) {
			next := streams[cursor.stream][cursor.index]
			cursor.key = next.connection.DepTime
			cursor.orderKey = next.orderKey
			cursor.instance = next.connection.TripInstanceID
			heap.Push(queue, cursor)
		}
	}
	return result
}

type templateSequenceBound struct{ firstSequence, lastSequence int }

func (s *Store) loadTemplateSequenceBounds(ctx context.Context, active map[int64][]model.TripInstanceID) (map[int64]templateSequenceBound, error) {
	keys := make([]int64, 0, len(active))
	for key := range active {
		keys = append(keys, key)
	}
	result := make(map[int64]templateSequenceBound, len(keys))
	for offset := 0; offset < len(keys); offset += maxSQLiteQueryParameters {
		end := min(offset+maxSQLiteQueryParameters, len(keys))
		chunk := keys[offset:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, len(chunk))
		for i, key := range chunk {
			args[i] = key
		}
		rows, err := s.db.QueryContext(ctx, `SELECT trip_key,MIN(dep_sequence),MAX(arr_sequence)
			FROM connections WHERE trip_key IN (`+placeholders+`) GROUP BY trip_key`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var key int64
			var bound templateSequenceBound
			if err := rows.Scan(&key, &bound.firstSequence, &bound.lastSequence); err != nil {
				rows.Close()
				return nil, err
			}
			result[key] = bound
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (s *Store) loadContinuations(ctx context.Context, index *queryIndex, bounds map[model.TripInstanceID]*tripWindowBounds) ([]model.Continuation, error) {
	type continuationKey struct {
		from, to model.TripInstanceID
		fromStop int
		toStop   int
	}
	continuations := make(map[continuationKey]model.TransferType)
	rows, err := s.db.QueryContext(ctx, `SELECT from_trip_key,to_trip_key,from_stop_key,to_stop_key,transfer_type FROM trip_links`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var fromTripKey, toTripKey int64
		var fromStopKey, toStopKey sql.NullInt64
		var transferType int
		if err := rows.Scan(&fromTripKey, &toTripKey, &fromStopKey, &toStopKey, &transferType); err != nil {
			rows.Close()
			return nil, err
		}
		for _, fromID := range index.instancesByTrip[fromTripKey] {
			from := bounds[fromID]
			if from == nil || !from.haveLast || (fromStopKey.Valid && fromStopKey.Int64 != from.lastStopKey) {
				continue
			}
			bestGap := int64(math.MaxInt64)
			var targets []*tripWindowBounds
			for _, toID := range index.instancesByTrip[toTripKey] {
				to := bounds[toID]
				if to == nil || !to.haveFirst || to.feedKey != from.feedKey || to.id == from.id ||
					(toStopKey.Valid && toStopKey.Int64 != to.firstStopKey) || to.firstTime < from.lastTime {
					continue
				}
				gap := to.firstTime - from.lastTime
				if gap < bestGap {
					bestGap = gap
					targets = targets[:0]
					targets = append(targets, to)
				} else if gap == bestGap {
					targets = append(targets, to)
				}
			}
			for _, to := range targets {
				fromEndpoint, toEndpoint := -1, -1
				if fromStopKey.Valid {
					fromEndpoint = from.lastStop
				}
				if toStopKey.Valid {
					toEndpoint = to.firstStop
				}
				key := continuationKey{
					from: from.id, to: to.id,
					fromStop: fromEndpoint, toStop: toEndpoint,
				}
				typeValue := model.TransferType(transferType)
				if existing, ok := continuations[key]; !ok || typeValue == model.TransferNoStayOnboard || existing != model.TransferNoStayOnboard {
					continuations[key] = typeValue
				}
			}
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	type instancePair struct{ from, to model.TripInstanceID }
	forbidden := make(map[instancePair][]continuationKey)
	for key, transferType := range continuations {
		if transferType == model.TransferNoStayOnboard {
			pair := instancePair{from: key.from, to: key.to}
			forbidden[pair] = append(forbidden[pair], key)
		}
	}
	result := make([]model.Continuation, 0, len(continuations))
	for key, transferType := range continuations {
		if transferType == model.TransferStayOnboard {
			from, to := bounds[key.from], bounds[key.to]
			blocked := false
			for _, prohibition := range forbidden[instancePair{from: key.from, to: key.to}] {
				if (prohibition.fromStop < 0 || prohibition.fromStop == from.lastStop) &&
					(prohibition.toStop < 0 || prohibition.toStop == to.firstStop) {
					blocked = true
					break
				}
			}
			if blocked {
				continue
			}
		}
		result = append(result, model.Continuation{
			FromTripInstanceID: key.from, ToTripInstanceID: key.to,
			FromStop: key.fromStop, ToStop: key.toStop, Type: transferType,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].FromTripInstanceID != result[j].FromTripInstanceID {
			return result[i].FromTripInstanceID < result[j].FromTripInstanceID
		}
		if result[i].ToTripInstanceID != result[j].ToTripInstanceID {
			return result[i].ToTripInstanceID < result[j].ToTripInstanceID
		}
		if result[i].FromStop != result[j].FromStop {
			return result[i].FromStop < result[j].FromStop
		}
		if result[i].ToStop != result[j].ToStop {
			return result[i].ToStop < result[j].ToStop
		}
		return result[i].Type > result[j].Type
	})
	return result, nil
}

func weekdayColumn(day time.Weekday) string {
	switch day {
	case time.Monday:
		return "monday"
	case time.Tuesday:
		return "tuesday"
	case time.Wednesday:
		return "wednesday"
	case time.Thursday:
		return "thursday"
	case time.Friday:
		return "friday"
	case time.Saturday:
		return "saturday"
	default:
		return "sunday"
	}
}
