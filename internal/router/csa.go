// Package router plans journeys over a timetable using the Connection Scan
// Algorithm (CSA), supporting earliest-arrival and latest-departure queries.
package router

import (
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

// scanResult holds the predecessor bookkeeping from a CSA scan.
type scanResult struct {
	arrival   []int64
	exitConn  []int // connection index used to arrive by transit, -1 otherwise
	boardConn []int // boarding connection index for that transit arrival
	walkFrom  []int // source stop of a walking arrival, -1 otherwise
}

// scan runs forward CSA from the given source stops at startUnix over the
// connections (sorted ascending by DepTime) and footpaths.
func scan(conns []model.Connection, foot [][]model.Footpath, nStops int, sources []int, startUnix int64, targets []int) scanResult {
	r := scanResult{
		arrival:   make([]int64, nStops),
		exitConn:  make([]int, nStops),
		boardConn: make([]int, nStops),
		walkFrom:  make([]int, nStops),
	}
	for i := 0; i < nStops; i++ {
		r.arrival[i] = maxInt64
		r.exitConn[i] = -1
		r.boardConn[i] = -1
		r.walkFrom[i] = -1
	}
	for _, src := range sources {
		if startUnix < r.arrival[src] {
			r.arrival[src] = startUnix
		}
		for _, fp := range foot[src] {
			t := startUnix + int64(fp.Seconds)
			if t < r.arrival[fp.ToStop] {
				r.arrival[fp.ToStop] = t
				r.walkFrom[fp.ToStop] = src
			}
		}
	}

	bestTarget := func() int64 {
		best := int64(maxInt64)
		for _, t := range targets {
			if r.arrival[t] < best {
				best = r.arrival[t]
			}
		}
		return best
	}

	inConnection := map[string]int{}
	lo := sort.Search(len(conns), func(i int) bool { return conns[i].DepTime >= startUnix })
	for i := lo; i < len(conns); i++ {
		c := conns[i]
		if bt := bestTarget(); bt != maxInt64 && c.DepTime > bt {
			break
		}
		board, reachable := inConnection[c.TripID]
		if !reachable {
			if r.arrival[c.DepStop] <= c.DepTime {
				inConnection[c.TripID] = i
				board = i
				reachable = true
			}
		}
		if !reachable {
			continue
		}
		if c.ArrTime < r.arrival[c.ArrStop] {
			r.arrival[c.ArrStop] = c.ArrTime
			r.exitConn[c.ArrStop] = i
			r.boardConn[c.ArrStop] = board
			r.walkFrom[c.ArrStop] = -1
			for _, fp := range foot[c.ArrStop] {
				t := c.ArrTime + int64(fp.Seconds)
				if t < r.arrival[fp.ToStop] {
					r.arrival[fp.ToStop] = t
					r.walkFrom[fp.ToStop] = c.ArrStop
					r.exitConn[fp.ToStop] = -1
				}
			}
		}
	}
	return r
}

// bestOf returns the target with the smallest arrival value.
func bestOf(arrival []int64, targets []int) (int, bool) {
	best := -1
	var bestVal int64 = maxInt64
	for _, t := range targets {
		if arrival[t] < bestVal {
			bestVal = arrival[t]
			best = t
		}
	}
	return best, best != -1
}

// PlanEarliestArrival finds the journey arriving as early as possible departing
// at or after depart, from any source stop to any target stop.
func PlanEarliestArrival(tt *model.Timetable, sources, targets []int, depart time.Time) (*model.Journey, error) {
	if err := validateInputs(tt, sources, targets); err != nil {
		return nil, err
	}
	r := scan(tt.Connections, tt.Footpaths, len(tt.Stops), sources, depart.Unix(), targets)
	target, ok := bestOf(r.arrival, targets)
	if !ok {
		return nil, ErrNoJourney
	}
	legs := reconstruct(tt, tt.Connections, r, sources, target)
	return assemble(legs), nil
}

// PlanLatestDeparture finds the journey departing as late as possible while
// arriving at or before arriveBy.
func PlanLatestDeparture(tt *model.Timetable, sources, targets []int, arriveBy time.Time) (*model.Journey, error) {
	if err := validateInputs(tt, sources, targets); err != nil {
		return nil, err
	}
	rev := reverseConnections(tt.Connections)
	revFoot := reverseFootpaths(tt.Footpaths, len(tt.Stops))
	// In the reversed graph, search from the targets at -arriveBy.
	r := scan(rev, revFoot, len(tt.Stops), targets, -arriveBy.Unix(), sources)
	source, ok := bestOf(r.arrival, sources)
	if !ok {
		return nil, ErrNoJourney
	}
	legs := reconstruct(tt, rev, r, targets, source)
	legs = flipLegs(legs)
	return assemble(legs), nil
}

func validateInputs(tt *model.Timetable, sources, targets []int) error {
	if tt == nil {
		return errors.New("nil timetable")
	}
	nStops := len(tt.Stops)
	if len(sources) == 0 || len(targets) == 0 {
		return ErrNoJourney
	}
	if len(tt.Footpaths) < nStops {
		return fmt.Errorf("invalid timetable: %d footpath buckets for %d stops", len(tt.Footpaths), nStops)
	}
	for _, src := range sources {
		if src < 0 || src >= nStops {
			return fmt.Errorf("invalid source stop index %d", src)
		}
	}
	for _, target := range targets {
		if target < 0 || target >= nStops {
			return fmt.Errorf("invalid target stop index %d", target)
		}
	}
	for i, c := range tt.Connections {
		if c.DepStop < 0 || c.DepStop >= nStops || c.ArrStop < 0 || c.ArrStop >= nStops {
			return fmt.Errorf("invalid connection %d stop index", i)
		}
	}
	for from, fps := range tt.Footpaths[:nStops] {
		for i, fp := range fps {
			if fp.ToStop < 0 || fp.ToStop >= nStops {
				return fmt.Errorf("invalid footpath %d from stop %d", i, from)
			}
		}
	}
	return nil
}

// reconstruct rebuilds journey legs from scan data, walking back from target
// until any source stop is reached.
func reconstruct(tt *model.Timetable, conns []model.Connection, r scanResult, sources []int, target int) []model.Leg {
	srcSet := map[int]bool{}
	for _, s := range sources {
		srcSet[s] = true
	}
	var legs []model.Leg
	cur := target
	for !srcSet[cur] {
		if r.walkFrom[cur] != -1 {
			u := r.walkFrom[cur]
			legs = append(legs, model.Leg{
				Walk:     true,
				FromStop: tt.Stops[u],
				ToStop:   tt.Stops[cur],
				DepTime:  unix(r.arrival[u]),
				ArrTime:  unix(r.arrival[cur]),
			})
			cur = u
			continue
		}
		ei := r.exitConn[cur]
		bi := r.boardConn[cur]
		if ei < 0 || bi < 0 {
			break
		}
		ec := conns[ei]
		bc := conns[bi]
		leg := model.Leg{
			FromStop: tt.Stops[bc.DepStop],
			ToStop:   tt.Stops[ec.ArrStop],
			DepTime:  unix(bc.DepTime),
			ArrTime:  unix(ec.ArrTime),
			TripID:   ec.TripID,
			BlockID:  ec.BlockID,
		}
		if ec.RouteIdx >= 0 && ec.RouteIdx < len(tt.Routes) {
			ri := tt.Routes[ec.RouteIdx]
			leg.RouteShortName = ri.ShortName
			leg.RouteLongName = ri.LongName
			leg.RouteType = ri.RouteType
		}
		leg.Headsign = tt.TripHeadsign[ec.TripID]
		legs = append(legs, leg)
		cur = bc.DepStop
	}
	for i, j := 0, len(legs)-1; i < j; i, j = i+1, j-1 {
		legs[i], legs[j] = legs[j], legs[i]
	}
	return legs
}

// reverseConnections builds the time-reversed connection set (sorted ascending
// by reversed departure time) for latest-departure queries.
func reverseConnections(conns []model.Connection) []model.Connection {
	out := make([]model.Connection, len(conns))
	for i, c := range conns {
		out[i] = model.Connection{
			DepStop:  c.ArrStop,
			ArrStop:  c.DepStop,
			DepTime:  -c.ArrTime,
			ArrTime:  -c.DepTime,
			TripID:   c.TripID,
			RouteIdx: c.RouteIdx,
			BlockID:  c.BlockID,
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DepTime < out[j].DepTime })
	return out
}

// reverseFootpaths reverses footpath direction (symmetric distances).
func reverseFootpaths(foot [][]model.Footpath, nStops int) [][]model.Footpath {
	out := make([][]model.Footpath, nStops)
	for from := range foot {
		for _, fp := range foot[from] {
			out[fp.ToStop] = append(out[fp.ToStop], model.Footpath{ToStop: from, Seconds: fp.Seconds})
		}
	}
	return out
}

// flipLegs converts legs produced in the reversed graph back into real,
// forward-time legs and restores chronological order.
func flipLegs(legs []model.Leg) []model.Leg {
	for i := range legs {
		l := &legs[i]
		l.FromStop, l.ToStop = l.ToStop, l.FromStop
		dep := unix(-l.ArrTime.Unix())
		arr := unix(-l.DepTime.Unix())
		l.DepTime, l.ArrTime = dep, arr
	}
	for i, j := 0, len(legs)-1; i < j; i, j = i+1, j-1 {
		legs[i], legs[j] = legs[j], legs[i]
	}
	return legs
}

// assemble computes journey-level summary fields from its legs.
func assemble(legs []model.Leg) *model.Journey {
	j := &model.Journey{Legs: legs}
	if len(legs) == 0 {
		return j
	}
	j.DepTime = legs[0].DepTime
	j.ArrTime = legs[len(legs)-1].ArrTime
	transit := 0
	for _, l := range legs {
		if !l.Walk {
			transit++
		}
	}
	if transit > 0 {
		j.Transfers = transit - 1
	}
	return j
}

func unix(sec int64) time.Time { return time.Unix(sec, 0) }
