package router

import (
	"container/heap"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"testing"
	"time"

	"github.com/thesammykins/ptv_cli/internal/model"
)

type oracleContext struct {
	tripInstanceID model.TripInstanceID
	routeIdx       int
	fromStop       int
	alightTime     int64
}

type oracleState struct {
	stop    int
	context oracleContext
}

type oracleItem struct {
	state oracleState
	time  int64
}

type oracleQueue []oracleItem

func (q oracleQueue) Len() int           { return len(q) }
func (q oracleQueue) Less(i, j int) bool { return q[i].time < q[j].time }
func (q oracleQueue) Swap(i, j int)      { q[i], q[j] = q[j], q[i] }
func (q *oracleQueue) Push(x any)        { *q = append(*q, x.(oracleItem)) }
func (q *oracleQueue) Pop() any {
	old := *q
	n := len(old)
	x := old[n-1]
	*q = old[:n-1]
	return x
}

// timeExpandedOracleEarliest runs a small event-based Dijkstra independent of
// CSA. Its states are offboard passenger contexts at specific event times;
// boarding expands every downstream alighting event on the selected trip.
func timeExpandedOracleEarliest(
	tt *model.Timetable,
	source, target int,
	depart int64,
	opts PlanOptions,
) (int64, bool) {
	outgoing := make([][]int, len(tt.Stops))
	tripConnections := make(map[model.TripInstanceID][]int)
	for i, connection := range tt.Connections {
		outgoing[connection.DepStop] = append(outgoing[connection.DepStop], i)
		tripConnections[connection.TripInstanceID] = append(tripConnections[connection.TripInstanceID], i)
	}
	for trip := range tripConnections {
		sort.Slice(tripConnections[trip], func(i, j int) bool {
			return tt.Connections[tripConnections[trip][i]].DepTime <
				tt.Connections[tripConnections[trip][j]].DepTime
		})
	}

	start := oracleState{stop: source, context: oracleContext{routeIdx: -1, fromStop: -1}}
	best := map[oracleState]int64{start: depart}
	q := &oracleQueue{{state: start, time: depart}}
	heap.Init(q)
	push := func(state oracleState, at int64) {
		if current, ok := best[state]; ok && current <= at {
			return
		}
		best[state] = at
		heap.Push(q, oracleItem{state: state, time: at})
	}

	for q.Len() > 0 {
		item := heap.Pop(q).(oracleItem)
		if best[item.state] != item.time {
			continue
		}
		if item.state.stop == target {
			return item.time, true
		}
		for _, edge := range tt.WalkEdges[item.state.stop] {
			push(
				oracleState{stop: edge.ToStop, context: item.state.context},
				item.time+int64(edge.Seconds),
			)
		}

		for _, connectionIdx := range outgoing[item.state.stop] {
			connection := tt.Connections[connectionIdx]
			if !connection.PickupPolicy.Allowed(opts.AllowConditional) {
				continue
			}
			ready, allowed := oracleTransferReady(tt, item.state.context, item.time, connection)
			if !allowed || ready > connection.DepTime {
				continue
			}

			trip := tripConnections[connection.TripInstanceID]
			startAt := -1
			for i, idx := range trip {
				if idx == connectionIdx {
					startAt = i
					break
				}
			}
			if startAt < 0 {
				panic("oracle trip index invariant")
			}
			previousStop := connection.DepStop
			previousArrival := connection.DepTime
			for _, idx := range trip[startAt:] {
				segment := tt.Connections[idx]
				if segment.DepStop != previousStop || segment.DepTime < previousArrival {
					break
				}
				if segment.DropOffPolicy.Allowed(opts.AllowConditional) {
					push(oracleState{
						stop: segment.ArrStop,
						context: oracleContext{
							tripInstanceID: segment.TripInstanceID,
							routeIdx:       segment.RouteIdx,
							fromStop:       segment.ArrStop,
							alightTime:     segment.ArrTime,
						},
					}, segment.ArrTime)
				}
				previousStop = segment.ArrStop
				previousArrival = segment.ArrTime
			}
		}
	}
	return 0, false
}

func oracleTransferReady(
	tt *model.Timetable,
	from oracleContext,
	at int64,
	connection model.Connection,
) (int64, bool) {
	if from.tripInstanceID == model.UnknownTripInstanceID {
		return at, true
	}
	bestScore := -1
	var selected model.TransferRule
	found := false
	for _, rule := range tt.TransferRules {
		if rule.FromStop >= 0 && rule.FromStop != from.fromStop {
			continue
		}
		if rule.ToStop >= 0 && rule.ToStop != connection.DepStop {
			continue
		}
		if rule.FromRouteIdx >= 0 && rule.FromRouteIdx != from.routeIdx {
			continue
		}
		if rule.ToRouteIdx >= 0 && rule.ToRouteIdx != connection.RouteIdx {
			continue
		}
		if rule.FromTripInstanceID != model.UnknownTripInstanceID &&
			rule.FromTripInstanceID != from.tripInstanceID {
			continue
		}
		if rule.ToTripInstanceID != model.UnknownTripInstanceID &&
			rule.ToTripInstanceID != connection.TripInstanceID {
			continue
		}
		score := oracleRuleSpecificity(rule)
		if score > bestScore {
			bestScore = score
			selected = rule
			found = true
		}
	}
	if !found {
		return at, true
	}
	switch selected.Type {
	case model.TransferRecommended, model.TransferTimed:
		return at, true
	case model.TransferMinimumTime:
		minimumReady := from.alightTime + int64(selected.MinTransferSeconds)
		if minimumReady > at {
			return minimumReady, true
		}
		return at, true
	case model.TransferForbidden:
		return 0, false
	default:
		panic(fmt.Sprintf("unsupported oracle transfer type %d", selected.Type))
	}
}

func oracleRuleSpecificity(rule model.TransferRule) int {
	fromTrip := rule.FromTripInstanceID != model.UnknownTripInstanceID
	toTrip := rule.ToTripInstanceID != model.UnknownTripInstanceID
	fromRoute := rule.FromRouteIdx >= 0
	toRoute := rule.ToRouteIdx >= 0
	stopSpecificity := 0
	if rule.FromStop >= 0 {
		stopSpecificity++
	}
	if rule.ToStop >= 0 {
		stopSpecificity++
	}
	switch {
	case fromTrip && toTrip:
		return 600 + stopSpecificity
	case (fromTrip && toRoute) || (fromRoute && toTrip):
		return 500 + stopSpecificity
	case fromTrip || toTrip:
		return 400 + stopSpecificity
	case fromRoute && toRoute:
		return 300 + stopSpecificity
	case fromRoute || toRoute:
		return 200 + stopSpecificity
	case stopSpecificity > 0:
		return 100 + stopSpecificity
	default:
		return 0
	}
}

func TestRandomNetworksMatchTimeExpandedOracle(t *testing.T) {
	const (
		baseUnix       = int64(1_800_000_000)
		networkCount   = 40
		deadlineOffset = int64(240)
	)
	forwardOffsets := []int64{0, 17, 43}
	feasibleForward := 0
	feasibleReverse := 0

	for seed := int64(0); seed < networkCount; seed++ {
		seed := seed
		t.Run(fmt.Sprintf("seed_%02d", seed), func(t *testing.T) {
			tt := randomOracleTimetable(seed, baseUnix)
			opts := PlanOptions{AllowConditional: seed%2 == 0}
			source := 0
			target := len(tt.Stops) - 1

			for _, offset := range forwardOffsets {
				wantArrival, wantOK := timeExpandedOracleEarliest(tt, source, target, baseUnix+offset, opts)
				journey, err := PlanEarliestArrivalContext(
					t.Context(), tt,
					[]model.Endpoint{{Stop: source}},
					[]model.Endpoint{{Stop: target}},
					time.Unix(baseUnix+offset, 0), opts,
				)
				if !wantOK {
					if !errors.Is(err, ErrNoJourney) {
						t.Fatalf("depart +%d: error = %v, want ErrNoJourney", offset, err)
					}
					continue
				}
				feasibleForward++
				if err != nil {
					t.Fatalf("depart +%d: %v", offset, err)
				}
				if got := journey.ArrTime.Unix(); got != wantArrival {
					t.Fatalf("depart +%d: arrival = %d, oracle = %d", offset, got, wantArrival)
				}
				assertJourneyActions(t, tt, journey, opts)
			}

			deadline := baseUnix + deadlineOffset
			latestOffset := int64(-1)
			for offset := int64(0); offset <= deadlineOffset; offset++ {
				arrival, ok := timeExpandedOracleEarliest(tt, source, target, baseUnix+offset, opts)
				if ok && arrival <= deadline {
					latestOffset = offset
				}
			}
			journey, err := PlanLatestDepartureContext(
				t.Context(), tt,
				[]model.Endpoint{{Stop: source}},
				[]model.Endpoint{{Stop: target}},
				time.Unix(deadline, 0), opts,
			)
			if latestOffset < 0 {
				if !errors.Is(err, ErrNoJourney) {
					t.Fatalf("latest: error = %v, want ErrNoJourney", err)
				}
				return
			}
			feasibleReverse++
			if err != nil {
				t.Fatalf("latest: %v", err)
			}
			if got, want := journey.DepTime.Unix(), baseUnix+latestOffset; got != want {
				t.Fatalf("latest departure = %d, oracle = %d", got, want)
			}
			if journey.ArrTime.Unix() > deadline {
				t.Fatalf("latest arrival %d exceeds deadline %d", journey.ArrTime.Unix(), deadline)
			}
			assertJourneyActions(t, tt, journey, opts)
		})
	}
	if feasibleForward == 0 || feasibleReverse == 0 {
		t.Fatalf("oracle generated insufficient feasible coverage: forward=%d reverse=%d", feasibleForward, feasibleReverse)
	}
}

func randomOracleTimetable(seed, base int64) *model.Timetable {
	rng := rand.New(rand.NewSource(seed + 0x5eed))
	const (
		stopCount  = 6
		routeCount = 3
		extraTrips = 5
	)
	tt := &model.Timetable{
		Stops:        make([]model.Stop, stopCount),
		Routes:       make([]model.RouteInfo, routeCount),
		WalkEdges:    make([][]model.WalkEdge, stopCount),
		TripHeadsign: make(map[string]string),
	}
	for i := range tt.Stops {
		tt.Stops[i] = model.Stop{Index: i, ID: fmt.Sprintf("s%d", i), Name: fmt.Sprintf("Stop %d", i)}
	}
	for i := range tt.Routes {
		tt.Routes[i] = model.RouteInfo{ShortName: fmt.Sprintf("R%d", i)}
	}

	// A regular backbone makes feasible cases common while rules, alternative
	// trips and walking still change which journeys survive.
	nextTripID := model.TripInstanceID(1)
	for from := 0; from < stopCount-1; from++ {
		depart := base + 10 + int64(from*30)
		tripID := nextTripID
		nextTripID++
		tt.Connections = append(tt.Connections, model.Connection{
			DepStop: from, ArrStop: from + 1,
			DepTime: depart, ArrTime: depart + 10,
			TripID: fmt.Sprintf("t%d", tripID), TripInstanceID: tripID,
			RouteIdx: from % routeCount,
		})
	}

	for trip := 0; trip < extraTrips; trip++ {
		tripID := nextTripID
		nextTripID++
		segments := 1 + rng.Intn(3)
		from := rng.Intn(stopCount)
		depart := base + int64(5+rng.Intn(170))
		route := rng.Intn(routeCount)
		for segment := 0; segment < segments; segment++ {
			to := rng.Intn(stopCount - 1)
			if to >= from {
				to++
			}
			duration := int64(5 + rng.Intn(16))
			connection := model.Connection{
				DepStop: from, ArrStop: to,
				DepTime: depart, ArrTime: depart + duration,
				TripID: fmt.Sprintf("t%d", tripID), TripInstanceID: tripID,
				RouteIdx:      route,
				PickupPolicy:  randomPassengerPolicy(rng),
				DropOffPolicy: randomPassengerPolicy(rng),
			}
			tt.Connections = append(tt.Connections, connection)
			from = to
			depart = connection.ArrTime + int64(rng.Intn(6))
		}
	}

	for from := 0; from < stopCount; from++ {
		for to := 0; to < stopCount; to++ {
			if from != to && rng.Intn(100) < 12 {
				tt.WalkEdges[from] = append(tt.WalkEdges[from], model.WalkEdge{
					ToStop: to, Seconds: 5 + rng.Intn(21), Kind: model.WalkEdgePathway,
				})
			}
		}
	}

	randomTransferType := func() model.TransferType {
		values := []model.TransferType{
			model.TransferRecommended,
			model.TransferTimed,
			model.TransferMinimumTime,
			model.TransferForbidden,
		}
		return values[rng.Intn(len(values))]
	}
	stopRule := model.TransferRule{
		FromStop: rng.Intn(stopCount), ToStop: rng.Intn(stopCount),
		FromRouteIdx: -1, ToRouteIdx: -1,
		Type: randomTransferType(), MinTransferSeconds: rng.Intn(31),
	}
	routeRule := model.TransferRule{
		FromStop: -1, ToStop: -1,
		FromRouteIdx: rng.Intn(routeCount), ToRouteIdx: rng.Intn(routeCount),
		Type: randomTransferType(), MinTransferSeconds: rng.Intn(31),
	}
	tripRule := model.TransferRule{
		FromStop: -1, ToStop: -1,
		FromRouteIdx: -1, ToRouteIdx: -1,
		FromTripInstanceID: model.TripInstanceID(1 + rng.Intn(int(nextTripID-1))),
		ToTripInstanceID:   model.TripInstanceID(1 + rng.Intn(int(nextTripID-1))),
		Type:               randomTransferType(), MinTransferSeconds: rng.Intn(31),
	}
	tt.TransferRules = []model.TransferRule{stopRule, routeRule, tripRule}

	sort.SliceStable(tt.Connections, func(i, j int) bool {
		if tt.Connections[i].DepTime == tt.Connections[j].DepTime {
			return tt.Connections[i].TripInstanceID < tt.Connections[j].TripInstanceID
		}
		return tt.Connections[i].DepTime < tt.Connections[j].DepTime
	})
	return tt
}

func randomPassengerPolicy(rng *rand.Rand) model.PassengerActionPolicy {
	switch value := rng.Intn(10); {
	case value < 7:
		return model.PassengerActionRegular
	case value == 7:
		return model.PassengerActionForbidden
	case value == 8:
		return model.PassengerActionPhoneAgency
	default:
		return model.PassengerActionCoordinateDriver
	}
}

func assertJourneyActions(t *testing.T, tt *model.Timetable, journey *model.Journey, opts PlanOptions) {
	t.Helper()
	var (
		haveTransferContext bool
		contextState        oracleContext
		offboardTime        int64
	)
	for legIndex, leg := range journey.Legs {
		if leg.Walk {
			if leg.FromStop.Index < 0 || leg.ToStop.Index < 0 {
				t.Fatalf("leg %d unexpectedly uses a virtual endpoint", legIndex)
			}
			found := false
			for _, edge := range tt.WalkEdges[leg.FromStop.Index] {
				if edge.ToStop == leg.ToStop.Index &&
					int64(edge.Seconds) == leg.ArrTime.Unix()-leg.DepTime.Unix() {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("leg %d uses nonexistent walk %d -> %d", legIndex, leg.FromStop.Index, leg.ToStop.Index)
			}
			offboardTime = leg.ArrTime.Unix()
			continue
		}

		boardIdx, exitIdx := transitLegBounds(tt, leg)
		if boardIdx < 0 || exitIdx < boardIdx {
			t.Fatalf("leg %d does not map to a contiguous trip segment: %#v", legIndex, leg)
		}
		board := tt.Connections[boardIdx]
		exit := tt.Connections[exitIdx]
		if !board.PickupPolicy.Allowed(opts.AllowConditional) {
			t.Fatalf("leg %d boards with forbidden pickup policy %d", legIndex, board.PickupPolicy)
		}
		if !exit.DropOffPolicy.Allowed(opts.AllowConditional) {
			t.Fatalf("leg %d alights with forbidden drop-off policy %d", legIndex, exit.DropOffPolicy)
		}
		if haveTransferContext && !leg.StayOnboard {
			ready, allowed := oracleTransferReady(tt, contextState, offboardTime, board)
			if !allowed || ready > board.DepTime {
				t.Fatalf("leg %d violates contextual transfer rule", legIndex)
			}
		}
		haveTransferContext = true
		contextState = oracleContext{
			tripInstanceID: exit.TripInstanceID,
			routeIdx:       exit.RouteIdx,
			fromStop:       exit.ArrStop,
			alightTime:     exit.ArrTime,
		}
		offboardTime = exit.ArrTime
	}
}

func transitLegBounds(tt *model.Timetable, leg model.Leg) (int, int) {
	trip := make([]int, 0)
	for i, connection := range tt.Connections {
		if connection.TripInstanceID == leg.TripInstanceID {
			trip = append(trip, i)
		}
	}
	sort.Slice(trip, func(i, j int) bool {
		return tt.Connections[trip[i]].DepTime < tt.Connections[trip[j]].DepTime
	})
	board := -1
	for position, idx := range trip {
		connection := tt.Connections[idx]
		if board < 0 && connection.DepStop == leg.FromStop.Index && connection.DepTime == leg.DepTime.Unix() {
			board = idx
		}
		if board >= 0 && connection.ArrStop == leg.ToStop.Index && connection.ArrTime == leg.ArrTime.Unix() {
			// Ensure all trip entries between board and exit form a contiguous suffix.
			boardPosition := -1
			for p, candidate := range trip {
				if candidate == board {
					boardPosition = p
					break
				}
			}
			if boardPosition < 0 || position < boardPosition {
				return -1, -1
			}
			previous := tt.Connections[trip[boardPosition]]
			for _, middle := range trip[boardPosition+1 : position+1] {
				next := tt.Connections[middle]
				if previous.ArrStop != next.DepStop || previous.ArrTime > next.DepTime {
					return -1, -1
				}
				previous = next
			}
			return board, idx
		}
	}
	return -1, -1
}
