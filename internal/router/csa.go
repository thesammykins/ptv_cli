// Package router plans journeys over a timetable using the Connection Scan
// Algorithm (CSA), supporting earliest-arrival and latest-departure queries.
package router

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/thesammykins/ptv_cli/internal/model"
)

// ErrNoJourney indicates no path was found between the stops.
var ErrNoJourney = errors.New("no journey found")

const maxInt64 = math.MaxInt64

// PlanOptions controls routing behavior that can change journey feasibility.
type PlanOptions struct {
	// AllowConditional permits GTFS pickup/drop-off values 2 and 3. Returned
	// legs are marked Conditional so callers can present the requirement.
	AllowConditional bool
}

type labelStepKind uint8

const (
	labelStepInvalid labelStepKind = iota
	labelStepSeed
	labelStepWalk
	labelStepTransit
)

// transferContext is retained across physical walking. Transfer rules apply
// to the trip/route and stop where the passenger last alighted, not merely to
// whichever stop label happened to be globally earliest.
type transferContext struct {
	tripInstanceID model.TripInstanceID
	legacyTripID   string
	routeIdx       int
	fromStop       int
}

type labelStep struct {
	kind         labelStepKind
	seedEndpoint int
	boardConn    int
	exitConn     int
	stayOnboard  bool
}

// journeyLabel is an immutable predecessor node. Labels with distinct transfer
// contexts are intentionally not collapsed because a later arrival can remain
// feasible when a more-specific rule prohibits the globally earliest one.
type journeyLabel struct {
	stop       int
	arrival    int64
	alightTime int64
	context    transferContext
	pred       *journeyLabel
	step       labelStep
}

type stopLabelSet struct {
	byContext map[transferContext]*journeyLabel
	all       []*journeyLabel
	best      *journeyLabel
}

type scanResult struct {
	labels []stopLabelSet

	bestTargetLabel    *journeyLabel
	bestTargetEndpoint int
	bestTargetArrival  int64
}

func newScanResult(nStops int) scanResult {
	return scanResult{
		labels:             make([]stopLabelSet, nStops),
		bestTargetEndpoint: -1,
		bestTargetArrival:  maxInt64,
	}
}

func (r *scanResult) addLabel(label *journeyLabel) bool {
	set := &r.labels[label.stop]
	if set.byContext == nil {
		set.byContext = make(map[transferContext]*journeyLabel)
	}
	if current := set.byContext[label.context]; current != nil && current.arrival <= label.arrival {
		return false
	}
	set.byContext[label.context] = label
	set.all = append(set.all, label)
	if set.best == nil || label.arrival < set.best.arrival {
		set.best = label
	}
	return true
}

func (r *scanResult) current(label *journeyLabel) bool {
	set := &r.labels[label.stop]
	return set.byContext != nil && set.byContext[label.context] == label
}

type walkQueue []*journeyLabel

func (q walkQueue) Len() int           { return len(q) }
func (q walkQueue) Less(i, j int) bool { return q[i].arrival < q[j].arrival }
func (q walkQueue) Swap(i, j int)      { q[i], q[j] = q[j], q[i] }
func (q *walkQueue) Push(x any)        { *q = append(*q, x.(*journeyLabel)) }
func (q *walkQueue) Pop() any {
	old := *q
	n := len(old)
	x := old[n-1]
	*q = old[:n-1]
	return x
}

type onboardState struct {
	boardConn   int
	boardLabel  *journeyLabel
	stayOnboard bool
	lastConn    int
	lastStop    int
	lastArrival int64
}

type transferRuleIndex struct {
	byToStop map[int][]model.TransferRule
	wildcard []model.TransferRule
}

type continuationKey struct {
	fromTrip model.TripInstanceID
	toTrip   model.TripInstanceID
	fromStop int
	toStop   int
}

type continuationSource struct {
	fromTrip model.TripInstanceID
	fromStop int
}

type continuationIndex struct {
	allowedByTarget map[struct {
		toTrip model.TripInstanceID
		toStop int
	}][]continuationSource
	forbidden map[continuationKey]bool
}

func buildTransferRuleIndex(rules []model.TransferRule) transferRuleIndex {
	idx := transferRuleIndex{byToStop: make(map[int][]model.TransferRule)}
	for _, rule := range rules {
		if rule.ToStop < 0 {
			idx.wildcard = append(idx.wildcard, rule)
			continue
		}
		idx.byToStop[rule.ToStop] = append(idx.byToStop[rule.ToStop], rule)
	}
	return idx
}

func buildContinuationIndex(continuations []model.Continuation) continuationIndex {
	idx := continuationIndex{
		allowedByTarget: make(map[struct {
			toTrip model.TripInstanceID
			toStop int
		}][]continuationSource),
		forbidden: make(map[continuationKey]bool),
	}
	for _, continuation := range continuations {
		key := continuationKey{
			fromTrip: continuation.FromTripInstanceID,
			toTrip:   continuation.ToTripInstanceID,
			fromStop: continuation.FromStop,
			toStop:   continuation.ToStop,
		}
		if continuation.Type == model.TransferNoStayOnboard {
			idx.forbidden[key] = true
		}
	}
	seen := make(map[continuationKey]bool)
	for _, continuation := range continuations {
		if continuation.Type != model.TransferStayOnboard {
			continue
		}
		key := continuationKey{
			fromTrip: continuation.FromTripInstanceID,
			toTrip:   continuation.ToTripInstanceID,
			fromStop: continuation.FromStop,
			toStop:   continuation.ToStop,
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		target := struct {
			toTrip model.TripInstanceID
			toStop int
		}{toTrip: key.toTrip, toStop: key.toStop}
		idx.allowedByTarget[target] = append(idx.allowedByTarget[target], continuationSource{
			fromTrip: key.fromTrip,
			fromStop: key.fromStop,
		})
	}
	return idx
}

func continuationForbidden(
	idx continuationIndex,
	fromTrip, toTrip model.TripInstanceID,
	fromStop, toStop int,
) bool {
	for _, candidateFrom := range [...]int{fromStop, -1} {
		for _, candidateTo := range [...]int{toStop, -1} {
			if idx.forbidden[continuationKey{
				fromTrip: fromTrip,
				toTrip:   toTrip,
				fromStop: candidateFrom,
				toStop:   candidateTo,
			}] {
				return true
			}
		}
	}
	return false
}

func (idx continuationIndex) allowedFrom(toTrip model.TripInstanceID, toStop int) []continuationSource {
	exact := idx.allowedByTarget[struct {
		toTrip model.TripInstanceID
		toStop int
	}{toTrip: toTrip, toStop: toStop}]
	wildcard := idx.allowedByTarget[struct {
		toTrip model.TripInstanceID
		toStop int
	}{toTrip: toTrip, toStop: -1}]
	if len(wildcard) == 0 {
		return exact
	}
	out := make([]continuationSource, 0, len(exact)+len(wildcard))
	out = append(out, exact...)
	out = append(out, wildcard...)
	return out
}

// scan runs forward CSA from weighted source endpoints at startUnix over
// connections sorted by departure time and a directed physical walking graph.
func scan(
	ctx context.Context,
	tt *model.Timetable,
	conns []model.Connection,
	walk [][]model.WalkEdge,
	sources []model.Endpoint,
	startUnix int64,
	targets []model.Endpoint,
	rules []model.TransferRule,
	continuations []model.Continuation,
	opts PlanOptions,
) (scanResult, error) {
	if err := ctx.Err(); err != nil {
		return scanResult{}, err
	}

	r := newScanResult(len(tt.Stops))
	targetsByStop := make([][]int, len(tt.Stops))
	for i, target := range targets {
		targetsByStop[target.Stop] = append(targetsByStop[target.Stop], i)
	}
	updateBestTarget := func(label *journeyLabel) {
		for _, endpointIdx := range targetsByStop[label.stop] {
			total := saturatingAdd(label.arrival, int64(targets[endpointIdx].WalkSeconds))
			if total < r.bestTargetArrival {
				r.bestTargetArrival = total
				r.bestTargetLabel = label
				r.bestTargetEndpoint = endpointIdx
			}
		}
	}

	q := &walkQueue{}
	heap.Init(q)
	for i, source := range sources {
		label := &journeyLabel{
			stop:    source.Stop,
			arrival: saturatingAdd(startUnix, int64(source.WalkSeconds)),
			context: transferContext{routeIdx: -1, fromStop: -1},
			step:    labelStep{kind: labelStepSeed, seedEndpoint: i},
		}
		if r.addLabel(label) {
			updateBestTarget(label)
			heap.Push(q, label)
		}
	}
	if err := relaxWalks(ctx, walk, &r, q, updateBestTarget); err != nil {
		return scanResult{}, err
	}

	maxTripInstance, err := validateDenseTripInstances(conns)
	if err != nil {
		return scanResult{}, err
	}
	tripStates := make([]*onboardState, int(maxTripInstance)+1)
	legacyTripStates := make(map[string]*onboardState)
	getState := func(connection model.Connection) *onboardState {
		if connection.TripInstanceID != model.UnknownTripInstanceID {
			return tripStates[int(connection.TripInstanceID)]
		}
		if connection.TripID == "" {
			return nil
		}
		return legacyTripStates[connection.TripID]
	}
	setState := func(connection model.Connection, state *onboardState) {
		if connection.TripInstanceID != model.UnknownTripInstanceID {
			tripStates[int(connection.TripInstanceID)] = state
			return
		}
		if connection.TripID != "" {
			legacyTripStates[connection.TripID] = state
		}
	}

	ruleIdx := buildTransferRuleIndex(rules)
	continuationIdx := buildContinuationIndex(continuations)
	processConnection := func(i int) (bool, error) {
		connection := conns[i]
		state := getState(connection)
		if state != nil && state.lastConn >= 0 &&
			(state.lastStop != connection.DepStop || state.lastArrival > connection.DepTime) {
			// A zero-duration timestamp fixpoint may revisit an earlier connection
			// after this same trip was first boarded at a later connection in the
			// group. That earlier segment is before the boarding point and is simply
			// unreachable from this source; it is not a malformed trip ordering.
			if state.boardConn > i {
				return true, nil
			}
			return false, fmt.Errorf(
				"invalid non-contiguous trip instance %d at connection %d",
				connection.TripInstanceID, i,
			)
		}

		if state == nil && connection.TripInstanceID != model.UnknownTripInstanceID {
			state = continuationState(connection, i, conns, tripStates, continuationIdx)
			if state != nil {
				setState(connection, state)
			}
		}

		if state == nil && connection.PickupPolicy.Allowed(opts.AllowConditional) {
			boardLabel, boardErr := selectBoardingLabel(
				tt, connection, &r.labels[connection.DepStop], ruleIdx,
			)
			if boardErr != nil {
				return false, boardErr
			}
			if boardLabel != nil {
				state = &onboardState{
					boardConn:  i,
					boardLabel: boardLabel,
					lastConn:   -1,
					lastStop:   -1,
				}
				setState(connection, state)
			}
		}
		if state == nil {
			return false, nil
		}

		state.lastConn = i
		state.lastStop = connection.ArrStop
		state.lastArrival = connection.ArrTime
		if !connection.DropOffPolicy.Allowed(opts.AllowConditional) {
			return true, nil
		}

		label := transitLabel(tt, conns, state, i, len(rules) > 0)
		if r.addLabel(label) {
			updateBestTarget(label)
			heap.Push(q, label)
			if err := relaxWalks(ctx, walk, &r, q, updateBestTarget); err != nil {
				return false, err
			}
		}
		return true, nil
	}

	lo := sort.Search(len(conns), func(i int) bool { return conns[i].DepTime >= startUnix })
	for groupStart := lo; groupStart < len(conns); {
		if groupStart&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return scanResult{}, err
			}
		}
		departure := conns[groupStart].DepTime
		if r.bestTargetArrival != maxInt64 && departure > r.bestTargetArrival {
			break
		}
		groupEnd := groupStart + 1
		hasZeroDuration := conns[groupStart].ArrTime == departure
		for groupEnd < len(conns) && conns[groupEnd].DepTime == departure {
			hasZeroDuration = hasZeroDuration || conns[groupEnd].ArrTime == departure
			groupEnd++
		}
		if !hasZeroDuration {
			for i := groupStart; i < groupEnd; i++ {
				if _, err := processConnection(i); err != nil {
					return scanResult{}, err
				}
			}
			groupStart = groupEnd
			continue
		}

		// Equal-time zero-duration connections form a small event subgraph.
		// Their source order is not a valid dependency order across trips, so
		// retry only connections that have not yet become reachable until the
		// timestamp reaches a fixpoint. Every successful pass consumes at least
		// one connection, which bounds the loop by the group size.
		consumed := make([]bool, groupEnd-groupStart)
		for {
			progressed := false
			for i := groupStart; i < groupEnd; i++ {
				if consumed[i-groupStart] {
					continue
				}
				if (i-groupStart)&255 == 0 {
					if err := ctx.Err(); err != nil {
						return scanResult{}, err
					}
				}
				ok, err := processConnection(i)
				if err != nil {
					return scanResult{}, err
				}
				if ok {
					consumed[i-groupStart] = true
					progressed = true
				}
			}
			if !progressed {
				break
			}
		}
		groupStart = groupEnd
	}
	return r, nil
}

func validateDenseTripInstances(conns []model.Connection) (model.TripInstanceID, error) {
	maxTripInstance := model.UnknownTripInstanceID
	for _, connection := range conns {
		if connection.TripInstanceID > maxTripInstance {
			maxTripInstance = connection.TripInstanceID
		}
	}
	if uint64(maxTripInstance) > uint64(len(conns)) {
		return 0, fmt.Errorf(
			"invalid timetable: trip-instance id %d is not dense across %d connections",
			maxTripInstance, len(conns),
		)
	}
	seen := make([]bool, int(maxTripInstance)+1)
	distinct := 0
	for _, connection := range conns {
		if connection.TripInstanceID == model.UnknownTripInstanceID || seen[int(connection.TripInstanceID)] {
			continue
		}
		seen[int(connection.TripInstanceID)] = true
		distinct++
	}
	if distinct != int(maxTripInstance) {
		return 0, fmt.Errorf(
			"invalid timetable: %d populated trip instances with maximum id %d",
			distinct, maxTripInstance,
		)
	}
	return maxTripInstance, nil
}

func continuationState(
	connection model.Connection,
	boardConn int,
	conns []model.Connection,
	tripStates []*onboardState,
	idx continuationIndex,
) *onboardState {
	for _, source := range idx.allowedFrom(connection.TripInstanceID, connection.DepStop) {
		if int(source.fromTrip) >= len(tripStates) || continuationForbidden(
			idx, source.fromTrip, connection.TripInstanceID, source.fromStop, connection.DepStop,
		) {
			continue
		}
		fromState := tripStates[int(source.fromTrip)]
		if fromState == nil || fromState.lastConn < 0 ||
			(source.fromStop >= 0 && fromState.lastStop != source.fromStop) ||
			fromState.lastArrival > connection.DepTime {
			continue
		}
		return &onboardState{
			boardConn:   boardConn,
			boardLabel:  transitLabel(nil, conns, fromState, fromState.lastConn, false),
			stayOnboard: true,
			lastConn:    -1,
			lastStop:    -1,
		}
	}
	return nil
}

func transitLabel(
	tt *model.Timetable,
	conns []model.Connection,
	state *onboardState,
	exitConn int,
	retainTransferContext bool,
) *journeyLabel {
	exit := conns[exitConn]
	routeIdx := exit.RouteIdx
	tripID := exit.TripID
	if tt != nil {
		metadataTripID, metadataRouteIdx, _, _ := connectionMetadata(tt, exit)
		tripID = metadataTripID
		routeIdx = metadataRouteIdx
	}
	context := transferContext{routeIdx: -1, fromStop: -1}
	if retainTransferContext {
		context = transferContext{
			tripInstanceID: exit.TripInstanceID,
			legacyTripID:   tripID,
			routeIdx:       routeIdx,
			fromStop:       exit.ArrStop,
		}
	}
	return &journeyLabel{
		stop:       exit.ArrStop,
		arrival:    exit.ArrTime,
		alightTime: exit.ArrTime,
		context:    context,
		pred:       state.boardLabel,
		step: labelStep{
			kind:        labelStepTransit,
			boardConn:   state.boardConn,
			exitConn:    exitConn,
			stayOnboard: state.stayOnboard,
		},
	}
}

func selectBoardingLabel(
	tt *model.Timetable,
	connection model.Connection,
	labels *stopLabelSet,
	rules transferRuleIndex,
) (*journeyLabel, error) {
	if len(rules.byToStop[connection.DepStop]) == 0 && len(rules.wildcard) == 0 {
		if labels.best != nil && labels.best.arrival <= connection.DepTime {
			return labels.best, nil
		}
		return nil, nil
	}
	var best *journeyLabel
	bestReady := int64(maxInt64)
	_, toRouteIdx, _, _ := connectionMetadata(tt, connection)
	for _, label := range labels.all {
		if labels.byContext[label.context] != label {
			continue
		}
		ready, allowed, err := boardingReady(label, connection, toRouteIdx, rules)
		if err != nil {
			return nil, err
		}
		if !allowed || ready > connection.DepTime {
			continue
		}
		if best == nil || ready < bestReady || (ready == bestReady && label.arrival < best.arrival) {
			best = label
			bestReady = ready
		}
	}
	return best, nil
}

func boardingReady(
	label *journeyLabel,
	connection model.Connection,
	toRouteIdx int,
	rules transferRuleIndex,
) (ready int64, allowed bool, err error) {
	// An initial access label is not a transfer and is never restricted by
	// transfers.txt.
	if label.context.tripInstanceID == model.UnknownTripInstanceID && label.context.legacyTripID == "" {
		return label.arrival, true, nil
	}
	rule, found, err := selectTransferRule(label.context, connection, toRouteIdx, rules)
	if err != nil {
		return 0, false, err
	}
	if !found {
		return label.arrival, true, nil
	}
	switch rule.Type {
	case model.TransferRecommended, model.TransferTimed:
		return label.arrival, true, nil
	case model.TransferMinimumTime:
		return max(label.arrival, saturatingAdd(label.alightTime, int64(rule.MinTransferSeconds))), true, nil
	case model.TransferForbidden:
		return 0, false, nil
	default:
		return 0, false, fmt.Errorf("invalid transfer type %d", rule.Type)
	}
}

func selectTransferRule(
	from transferContext,
	connection model.Connection,
	toRouteIdx int,
	idx transferRuleIndex,
) (model.TransferRule, bool, error) {
	bestScore := -1
	var best model.TransferRule
	found := false
	consider := func(rule model.TransferRule) error {
		if !transferRuleMatches(rule, from, connection, toRouteIdx) {
			return nil
		}
		score := transferRuleSpecificity(rule)
		if score > bestScore {
			best = rule
			bestScore = score
			found = true
			return nil
		}
		if score == bestScore && !equivalentTransferRule(best, rule) {
			return fmt.Errorf(
				"conflicting transfer rules at equal specificity for stop %d",
				connection.DepStop,
			)
		}
		return nil
	}
	for _, rule := range idx.byToStop[connection.DepStop] {
		if err := consider(rule); err != nil {
			return model.TransferRule{}, false, err
		}
	}
	for _, rule := range idx.wildcard {
		if err := consider(rule); err != nil {
			return model.TransferRule{}, false, err
		}
	}
	return best, found, nil
}

func transferRuleMatches(
	rule model.TransferRule,
	from transferContext,
	connection model.Connection,
	toRouteIdx int,
) bool {
	if rule.FromStop >= 0 && rule.FromStop != from.fromStop {
		return false
	}
	if rule.ToStop >= 0 && rule.ToStop != connection.DepStop {
		return false
	}
	if rule.FromRouteIdx >= 0 && rule.FromRouteIdx != from.routeIdx {
		return false
	}
	if rule.ToRouteIdx >= 0 && rule.ToRouteIdx != toRouteIdx {
		return false
	}
	if rule.FromTripInstanceID != model.UnknownTripInstanceID &&
		rule.FromTripInstanceID != from.tripInstanceID {
		return false
	}
	if rule.ToTripInstanceID != model.UnknownTripInstanceID &&
		rule.ToTripInstanceID != connection.TripInstanceID {
		return false
	}
	return true
}

// transferRuleSpecificity follows the GTFS transfer-rule precedence exactly:
// both trips; one trip plus the opposite route; one trip; both routes; one
// route; stops only. Stop specificity only breaks ties inside one official
// class, so additional lower-precedence fields cannot leapfrog a trip rule.
func transferRuleSpecificity(rule model.TransferRule) int {
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

func equivalentTransferRule(a, b model.TransferRule) bool {
	return a.Type == b.Type && a.MinTransferSeconds == b.MinTransferSeconds
}

// relaxWalks performs a full shortest-path propagation from all queued label
// improvements. This makes multi-hop walking correct without requiring the
// supplied graph to be transitively closed.
func relaxWalks(
	ctx context.Context,
	walk [][]model.WalkEdge,
	r *scanResult,
	q *walkQueue,
	onImprove func(*journeyLabel),
) error {
	steps := 0
	for q.Len() > 0 {
		if steps&255 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		steps++
		label := heap.Pop(q).(*journeyLabel)
		if !r.current(label) {
			continue
		}
		for _, edge := range walk[label.stop] {
			candidate := &journeyLabel{
				stop:       edge.ToStop,
				arrival:    saturatingAdd(label.arrival, int64(edge.Seconds)),
				alightTime: label.alightTime,
				context:    label.context,
				pred:       label,
				step:       labelStep{kind: labelStepWalk},
			}
			if !r.addLabel(candidate) {
				continue
			}
			onImprove(candidate)
			heap.Push(q, candidate)
		}
	}
	return nil
}

// PlanEarliestArrival preserves the legacy stop-index API.
func PlanEarliestArrival(tt *model.Timetable, sources, targets []int, depart time.Time) (*model.Journey, error) {
	return PlanEarliestArrivalContext(
		context.Background(), tt, endpoints(sources), endpoints(targets), depart, PlanOptions{},
	)
}

// PlanEarliestArrivalWithContext is the context-aware legacy stop-index API.
func PlanEarliestArrivalWithContext(ctx context.Context, tt *model.Timetable, sources, targets []int, depart time.Time) (*model.Journey, error) {
	return PlanEarliestArrivalContext(ctx, tt, endpoints(sources), endpoints(targets), depart, PlanOptions{})
}

// PlanEarliestArrivalContext finds the journey arriving as early as possible
// from weighted source endpoints to weighted target endpoints.
func PlanEarliestArrivalContext(
	ctx context.Context,
	tt *model.Timetable,
	sources, targets []model.Endpoint,
	depart time.Time,
	opts PlanOptions,
) (*model.Journey, error) {
	var connections []model.Connection
	if tt != nil {
		connections = tt.Connections
	}
	walk, err := validateInputs(ctx, tt, connections, sources, targets)
	if err != nil {
		return nil, err
	}
	r, err := scan(
		ctx, tt, tt.Connections, walk, sources, depart.Unix(), targets,
		tt.TransferRules, tt.Continuations, opts,
	)
	if err != nil {
		return nil, err
	}
	if r.bestTargetLabel == nil {
		return nil, ErrNoJourney
	}
	legs, err := reconstruct(tt, tt.Connections, r.bestTargetLabel, sources, depart.Unix())
	if err != nil {
		return nil, err
	}
	legs = appendEgress(
		legs, tt, targets[r.bestTargetEndpoint],
		r.bestTargetLabel.arrival, r.bestTargetArrival,
	)
	return assembleAt(legs, depart), nil
}

// PlanLatestDeparture preserves the legacy stop-index API.
func PlanLatestDeparture(tt *model.Timetable, sources, targets []int, arriveBy time.Time) (*model.Journey, error) {
	return PlanLatestDepartureContext(
		context.Background(), tt, endpoints(sources), endpoints(targets), arriveBy, PlanOptions{},
	)
}

// PlanLatestDepartureWithContext is the context-aware legacy stop-index API.
func PlanLatestDepartureWithContext(ctx context.Context, tt *model.Timetable, sources, targets []int, arriveBy time.Time) (*model.Journey, error) {
	return PlanLatestDepartureContext(ctx, tt, endpoints(sources), endpoints(targets), arriveBy, PlanOptions{})
}

// PlanLatestDepartureContext finds the journey departing as late as possible
// while arriving at the weighted destination endpoint at or before arriveBy.
func PlanLatestDepartureContext(
	ctx context.Context,
	tt *model.Timetable,
	sources, targets []model.Endpoint,
	arriveBy time.Time,
	opts PlanOptions,
) (*model.Journey, error) {
	var rev []model.Connection
	if tt != nil {
		rev = tt.ReverseConnections
		if rev == nil {
			rev = reverseConnections(tt.Connections)
		}
	}
	walk, err := validateInputs(ctx, tt, rev, sources, targets)
	if err != nil {
		return nil, err
	}
	revWalk := tt.ReverseWalkEdges
	if revWalk == nil {
		revWalk = reverseWalkEdges(walk, len(tt.Stops))
	}
	r, err := scan(
		ctx, tt, rev, revWalk, targets, -arriveBy.Unix(), sources,
		reverseTransferRules(tt.TransferRules), reverseContinuations(tt.Continuations), opts,
	)
	if err != nil {
		return nil, err
	}
	if r.bestTargetLabel == nil {
		return nil, ErrNoJourney
	}
	legs, err := reconstruct(tt, rev, r.bestTargetLabel, targets, -arriveBy.Unix())
	if err != nil {
		return nil, err
	}
	legs = appendEgress(
		legs, tt, sources[r.bestTargetEndpoint],
		r.bestTargetLabel.arrival, r.bestTargetArrival,
	)
	legs, err = flipLegs(legs)
	if err != nil {
		return nil, err
	}
	return assembleAt(legs, arriveBy), nil
}

func endpoints(stops []int) []model.Endpoint {
	out := make([]model.Endpoint, len(stops))
	for i, stop := range stops {
		out[i] = model.Endpoint{Stop: stop}
	}
	return out
}

func walkEdges(tt *model.Timetable) [][]model.WalkEdge {
	if tt.WalkEdges != nil {
		return tt.WalkEdges
	}
	return tt.Footpaths
}

func validateInputs(ctx context.Context, tt *model.Timetable, connections []model.Connection, sources, targets []model.Endpoint) ([][]model.WalkEdge, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if tt == nil {
		return nil, errors.New("nil timetable")
	}
	nStops := len(tt.Stops)
	if len(sources) == 0 || len(targets) == 0 {
		return nil, ErrNoJourney
	}
	walk := walkEdges(tt)
	if len(walk) < nStops {
		return nil, fmt.Errorf("invalid timetable: %d walk buckets for %d stops", len(walk), nStops)
	}
	for _, endpointSet := range [][]model.Endpoint{sources, targets} {
		for _, endpoint := range endpointSet {
			if endpoint.Stop < 0 || endpoint.Stop >= nStops {
				return nil, fmt.Errorf("invalid endpoint stop index %d", endpoint.Stop)
			}
			if endpoint.WalkSeconds < 0 {
				return nil, fmt.Errorf("invalid endpoint walk duration %d", endpoint.WalkSeconds)
			}
		}
	}
	for i, connection := range connections {
		if i&4095 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if connection.DepStop < 0 || connection.DepStop >= nStops ||
			connection.ArrStop < 0 || connection.ArrStop >= nStops {
			return nil, fmt.Errorf("invalid connection %d stop index", i)
		}
		if connection.ArrTime < connection.DepTime {
			return nil, fmt.Errorf("invalid connection %d time order", i)
		}
		if connection.TripInstanceID != model.UnknownTripInstanceID && len(tt.TripInstances) > 0 {
			if int(connection.TripInstanceID) >= len(tt.TripInstances) {
				return nil, fmt.Errorf("invalid connection %d trip-instance id %d", i, connection.TripInstanceID)
			}
			instance := tt.TripInstances[int(connection.TripInstanceID)]
			if instance.ID != model.UnknownTripInstanceID && instance.ID != connection.TripInstanceID {
				return nil, fmt.Errorf("invalid trip-instance slot %d contains id %d", connection.TripInstanceID, instance.ID)
			}
		}
	}
	for from, edges := range walk[:nStops] {
		for i, edge := range edges {
			if edge.ToStop < 0 || edge.ToStop >= nStops {
				return nil, fmt.Errorf("invalid walk edge %d from stop %d", i, from)
			}
			if edge.Seconds < 0 {
				return nil, fmt.Errorf("invalid walk edge duration %d from stop %d", edge.Seconds, from)
			}
		}
	}
	maxTripInstance, err := validateDenseTripInstances(connections)
	if err != nil {
		return nil, err
	}
	for i, rule := range tt.TransferRules {
		if rule.FromStop < -1 || rule.ToStop < -1 || rule.FromStop >= nStops || rule.ToStop >= nStops {
			return nil, fmt.Errorf("invalid transfer rule %d stop index", i)
		}
		if rule.FromRouteIdx < -1 || rule.ToRouteIdx < -1 ||
			rule.FromRouteIdx >= len(tt.Routes) || rule.ToRouteIdx >= len(tt.Routes) {
			return nil, fmt.Errorf("invalid transfer rule %d route index", i)
		}
		if rule.FromTripInstanceID > maxTripInstance || rule.ToTripInstanceID > maxTripInstance {
			return nil, fmt.Errorf("invalid transfer rule %d trip-instance id", i)
		}
		if rule.MinTransferSeconds < 0 {
			return nil, fmt.Errorf("invalid transfer rule %d minimum time", i)
		}
		if rule.Type > model.TransferForbidden {
			return nil, fmt.Errorf("invalid transfer rule %d type %d", i, rule.Type)
		}
	}
	for i, continuation := range tt.Continuations {
		if continuation.FromStop >= nStops || continuation.FromStop < -1 ||
			continuation.ToStop >= nStops || continuation.ToStop < -1 {
			return nil, fmt.Errorf("invalid continuation %d stop index", i)
		}
		if continuation.FromTripInstanceID == model.UnknownTripInstanceID ||
			continuation.ToTripInstanceID == model.UnknownTripInstanceID {
			return nil, fmt.Errorf("invalid continuation %d trip-instance id", i)
		}
		if continuation.FromTripInstanceID > maxTripInstance || continuation.ToTripInstanceID > maxTripInstance {
			return nil, fmt.Errorf("invalid continuation %d trip-instance id", i)
		}
		if continuation.Type != model.TransferStayOnboard && continuation.Type != model.TransferNoStayOnboard {
			return nil, fmt.Errorf("invalid continuation %d type %d", i, continuation.Type)
		}
	}
	return walk, nil
}

// reconstruct follows immutable predecessor labels. Missing state is an
// invariant error rather than a silently truncated journey.
func reconstruct(
	tt *model.Timetable,
	conns []model.Connection,
	target *journeyLabel,
	sources []model.Endpoint,
	queryUnix int64,
) ([]model.Leg, error) {
	var legs []model.Leg
	seen := make(map[*journeyLabel]bool)
	for label := target; label != nil; label = label.pred {
		if seen[label] {
			return nil, errors.New("invalid routing predecessor cycle")
		}
		seen[label] = true
		switch label.step.kind {
		case labelStepSeed:
			seedIdx := label.step.seedEndpoint
			if seedIdx < 0 || seedIdx >= len(sources) {
				return nil, fmt.Errorf("invalid source endpoint predecessor %d", seedIdx)
			}
			seed := sources[seedIdx]
			if seed.WalkSeconds > 0 {
				legs = append(legs, model.Leg{
					Walk:     true,
					FromStop: endpointLocation(seed, tt.Stops[seed.Stop], "Origin"),
					ToStop:   tt.Stops[seed.Stop],
					DepTime:  unix(queryUnix),
					ArrTime:  unix(label.arrival),
				})
			}
			if label.pred != nil {
				return nil, errors.New("invalid source endpoint predecessor chain")
			}
		case labelStepWalk:
			if label.pred == nil {
				return nil, fmt.Errorf("missing walking predecessor at stop %d", label.stop)
			}
			legs = append(legs, model.Leg{
				Walk:     true,
				FromStop: tt.Stops[label.pred.stop],
				ToStop:   tt.Stops[label.stop],
				DepTime:  unix(label.pred.arrival),
				ArrTime:  unix(label.arrival),
			})
		case labelStepTransit:
			boardIdx := label.step.boardConn
			exitIdx := label.step.exitConn
			if boardIdx < 0 || boardIdx >= len(conns) || exitIdx < 0 || exitIdx >= len(conns) {
				return nil, fmt.Errorf("invalid transit predecessor at stop %d", label.stop)
			}
			board := conns[boardIdx]
			exit := conns[exitIdx]
			if board.TripInstanceID != model.UnknownTripInstanceID &&
				exit.TripInstanceID != model.UnknownTripInstanceID &&
				board.TripInstanceID != exit.TripInstanceID {
				return nil, fmt.Errorf("invalid routing trip-instance predecessor at stop %d", label.stop)
			}
			tripID, routeIdx, headsign, blockID := connectionMetadata(tt, exit)
			leg := model.Leg{
				FromStop:       tt.Stops[board.DepStop],
				ToStop:         tt.Stops[exit.ArrStop],
				DepTime:        unix(board.DepTime),
				ArrTime:        unix(exit.ArrTime),
				TripID:         tripID,
				TripInstanceID: exit.TripInstanceID,
				BlockID:        blockID,
				StayOnboard:    label.step.stayOnboard,
				PickupPolicy:   board.PickupPolicy,
				DropOffPolicy:  exit.DropOffPolicy,
				Conditional:    board.PickupPolicy.Conditional() || exit.DropOffPolicy.Conditional(),
			}
			if routeIdx >= 0 && routeIdx < len(tt.Routes) {
				route := tt.Routes[routeIdx]
				leg.RouteShortName = route.ShortName
				leg.RouteLongName = route.LongName
				leg.RouteType = route.RouteType
			}
			leg.Headsign = headsign
			legs = append(legs, leg)
		case labelStepInvalid:
			return nil, fmt.Errorf("missing routing predecessor at stop %d", label.stop)
		default:
			return nil, fmt.Errorf("unknown routing predecessor at stop %d", label.stop)
		}
	}
	for i, j := 0, len(legs)-1; i < j; i, j = i+1, j-1 {
		legs[i], legs[j] = legs[j], legs[i]
	}
	return legs, nil
}

func connectionMetadata(tt *model.Timetable, connection model.Connection) (tripID string, routeIdx int, headsign, blockID string) {
	tripID = connection.TripID
	routeIdx = connection.RouteIdx
	blockID = connection.BlockID
	if connection.TripInstanceID != model.UnknownTripInstanceID && int(connection.TripInstanceID) < len(tt.TripInstances) {
		instance := tt.TripInstances[int(connection.TripInstanceID)]
		if tripID == "" {
			tripID = instance.TripID
		}
		if routeIdx < 0 {
			routeIdx = instance.RouteIdx
		}
		headsign = instance.Headsign
		if blockID == "" {
			blockID = instance.BlockID
		}
	}
	if headsign == "" {
		headsign = tt.TripHeadsign[tripID]
	}
	return tripID, routeIdx, headsign, blockID
}

func appendEgress(legs []model.Leg, tt *model.Timetable, endpoint model.Endpoint, depUnix, arrUnix int64) []model.Leg {
	if endpoint.WalkSeconds <= 0 {
		return legs
	}
	return append(legs, model.Leg{
		Walk:     true,
		FromStop: tt.Stops[endpoint.Stop],
		ToStop:   endpointLocation(endpoint, tt.Stops[endpoint.Stop], "Destination"),
		DepTime:  unix(depUnix),
		ArrTime:  unix(arrUnix),
	})
}

func endpointLocation(endpoint model.Endpoint, fallback model.Stop, name string) model.Stop {
	if endpoint.Location != nil {
		return *endpoint.Location
	}
	fallback.Index = -1
	fallback.ID = ""
	fallback.Name = name
	return fallback
}

// reverseConnections builds the time-reversed connection set, sorted by
// reversed departure time. The v2 query loader can provide this ordering
// directly; this function remains the compatibility path for in-memory tables.
func reverseConnections(conns []model.Connection) []model.Connection {
	out := make([]model.Connection, len(conns))
	for i := range conns {
		// Begin in reverse source order so a stable time sort preserves reverse
		// stop-sequence order when adjacent segments share an arrival instant.
		connection := conns[len(conns)-1-i]
		out[i] = model.Connection{
			DepStop:        connection.ArrStop,
			ArrStop:        connection.DepStop,
			DepTime:        -connection.ArrTime,
			ArrTime:        -connection.DepTime,
			TripID:         connection.TripID,
			TripInstanceID: connection.TripInstanceID,
			RouteIdx:       connection.RouteIdx,
			BlockID:        connection.BlockID,
			PickupPolicy:   connection.DropOffPolicy,
			DropOffPolicy:  connection.PickupPolicy,
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].DepTime < out[j].DepTime })
	return out
}

func reverseWalkEdges(walk [][]model.WalkEdge, nStops int) [][]model.WalkEdge {
	out := make([][]model.WalkEdge, nStops)
	for from := range walk {
		for _, edge := range walk[from] {
			out[edge.ToStop] = append(out[edge.ToStop], model.WalkEdge{
				ToStop:  from,
				Seconds: edge.Seconds,
				Kind:    edge.Kind,
			})
		}
	}
	return out
}

func reverseTransferRules(rules []model.TransferRule) []model.TransferRule {
	out := make([]model.TransferRule, len(rules))
	for i, rule := range rules {
		out[i] = model.TransferRule{
			FromStop:           rule.ToStop,
			ToStop:             rule.FromStop,
			Type:               rule.Type,
			MinTransferSeconds: rule.MinTransferSeconds,
			FromRouteIdx:       rule.ToRouteIdx,
			ToRouteIdx:         rule.FromRouteIdx,
			FromTripInstanceID: rule.ToTripInstanceID,
			ToTripInstanceID:   rule.FromTripInstanceID,
		}
	}
	return out
}

func reverseContinuations(continuations []model.Continuation) []model.Continuation {
	out := make([]model.Continuation, len(continuations))
	for i, continuation := range continuations {
		out[i] = model.Continuation{
			FromTripInstanceID: continuation.ToTripInstanceID,
			ToTripInstanceID:   continuation.FromTripInstanceID,
			FromStop:           continuation.ToStop,
			ToStop:             continuation.FromStop,
			Type:               continuation.Type,
		}
	}
	return out
}

// flipLegs converts legs produced in the reversed graph back into real,
// forward-time legs and restores chronological order. A reverse continuation
// marks the earlier original leg, so the marker is shifted to the following
// transit leg after reversal.
func flipLegs(legs []model.Leg) ([]model.Leg, error) {
	for i := range legs {
		leg := &legs[i]
		leg.FromStop, leg.ToStop = leg.ToStop, leg.FromStop
		depart := unix(-leg.ArrTime.Unix())
		arrive := unix(-leg.DepTime.Unix())
		leg.DepTime, leg.ArrTime = depart, arrive
		leg.PickupPolicy, leg.DropOffPolicy = leg.DropOffPolicy, leg.PickupPolicy
	}
	for i, j := 0, len(legs)-1; i < j; i, j = i+1, j-1 {
		legs[i], legs[j] = legs[j], legs[i]
	}

	reverseContinuation := make([]bool, len(legs))
	for i := range legs {
		reverseContinuation[i] = legs[i].StayOnboard
		legs[i].StayOnboard = false
	}
	for i, marked := range reverseContinuation {
		if !marked {
			continue
		}
		shifted := false
		for j := i + 1; j < len(legs); j++ {
			if !legs[j].Walk {
				legs[j].StayOnboard = true
				shifted = true
				break
			}
		}
		if !shifted {
			return nil, errors.New("invalid reverse continuation predecessor")
		}
	}
	return legs, nil
}

func assembleAt(legs []model.Leg, queryTime time.Time) *model.Journey {
	// time.Unix constructs values in the host's local zone. Normalize every
	// reconstructed timestamp to the query's explicit transport zone so JSON
	// and callers are independent of TZ/time.Local.
	location := queryTime.Location()
	for i := range legs {
		legs[i].DepTime = legs[i].DepTime.In(location)
		legs[i].ArrTime = legs[i].ArrTime.In(location)
	}
	journey := &model.Journey{Legs: legs}
	if len(legs) == 0 {
		journey.DepTime = queryTime
		journey.ArrTime = queryTime
		return journey
	}
	journey.DepTime = legs[0].DepTime
	journey.ArrTime = legs[len(legs)-1].ArrTime
	transit := 0
	for _, leg := range legs {
		if !leg.Walk && !leg.StayOnboard {
			transit++
		}
	}
	if transit > 0 {
		journey.Transfers = transit - 1
	}
	return journey
}

func saturatingAdd(a, b int64) int64 {
	if b > 0 && a > maxInt64-b {
		return maxInt64
	}
	if b < 0 && a < math.MinInt64-b {
		return math.MinInt64
	}
	return a + b
}

func unix(sec int64) time.Time { return time.Unix(sec, 0) }
