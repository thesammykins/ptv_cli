package router

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/thesammykins/ptv_cli/internal/model"
)

// fixture builds a tiny network:
//
//	stop0 --tripA--> stop1 --tripA--> stop2
//	stop1 --walk(2m)--> stop3
//	stop3 --tripB--> stop2 (faster alternative leaving later)
//
// Times are unix seconds anchored at base.
func fixture(base int64) *model.Timetable {
	tt := &model.Timetable{
		Stops: []model.Stop{
			{Index: 0, ID: "s0", Name: "Origin"},
			{Index: 1, ID: "s1", Name: "Mid"},
			{Index: 2, ID: "s2", Name: "Dest"},
			{Index: 3, ID: "s3", Name: "Alt"},
		},
		Routes: []model.RouteInfo{
			{ShortName: "A", RouteType: 2},
			{ShortName: "B", RouteType: 3},
		},
		TripHeadsign: map[string]string{"A": "to Dest", "B": "via Alt"},
		NameIndex:    map[string][]int{},
	}
	tt.Footpaths = make([][]model.Footpath, len(tt.Stops))
	tt.Footpaths[1] = []model.Footpath{{ToStop: 3, Seconds: 120}}
	tt.Footpaths[3] = []model.Footpath{{ToStop: 1, Seconds: 120}}

	min := func(m int) int64 { return base + int64(m*60) }
	tt.Connections = []model.Connection{
		{DepStop: 0, ArrStop: 1, DepTime: min(0), ArrTime: min(5), TripID: "A", RouteIdx: 0},
		{DepStop: 1, ArrStop: 2, DepTime: min(5), ArrTime: min(20), TripID: "A", RouteIdx: 0},
		{DepStop: 3, ArrStop: 2, DepTime: min(10), ArrTime: min(14), TripID: "B", RouteIdx: 1},
	}
	return tt
}

func TestPlanEarliestArrival(t *testing.T) {
	base := time.Date(2025, 1, 1, 8, 0, 0, 0, time.UTC).Unix()
	tt := fixture(base)

	depart := time.Unix(base, 0)
	j, err := PlanEarliestArrival(tt, []int{0}, []int{2}, depart)
	if err != nil {
		t.Fatalf("PlanEarliestArrival: %v", err)
	}

	// Best route: tripA to s1 (arr 8:05), walk to s3 (8:07), tripB to s2 (8:14)
	// which beats staying on tripA (arr 8:20).
	wantArr := time.Unix(base+14*60, 0)
	if !j.ArrTime.Equal(wantArr) {
		t.Errorf("arrival = %s, want %s", j.ArrTime.UTC(), wantArr.UTC())
	}
	if len(j.Legs) != 3 {
		t.Fatalf("expected 3 legs (transit, walk, transit), got %d", len(j.Legs))
	}
	if !j.Legs[1].Walk {
		t.Errorf("expected middle leg to be a walk transfer")
	}
	if j.Transfers != 1 {
		t.Errorf("transfers = %d, want 1", j.Transfers)
	}
}

func TestPlanLatestDeparture(t *testing.T) {
	base := time.Date(2025, 1, 1, 8, 0, 0, 0, time.UTC).Unix()
	tt := fixture(base)

	arriveBy := time.Unix(base+20*60, 0) // 8:20
	j, err := PlanLatestDeparture(tt, []int{0}, []int{2}, arriveBy)
	if err != nil {
		t.Fatalf("PlanLatestDeparture: %v", err)
	}

	// To arrive by 8:20 the only origin departure is tripA at 8:00.
	wantDep := time.Unix(base, 0)
	if !j.DepTime.Equal(wantDep) {
		t.Errorf("departure = %s, want %s", j.DepTime.UTC(), wantDep.UTC())
	}
	if j.ArrTime.After(arriveBy) {
		t.Errorf("arrival %s is after arrive-by %s", j.ArrTime.UTC(), arriveBy.UTC())
	}
	if len(j.Legs) == 0 {
		t.Fatalf("expected at least one leg")
	}
	if !j.Legs[0].DepTime.Equal(wantDep) {
		t.Errorf("first leg departs %s, want %s", j.Legs[0].DepTime.UTC(), wantDep.UTC())
	}
}

func TestZeroDurationEqualTimeConnectionsReachFixpoint(t *testing.T) {
	base := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC).Unix()
	stops := []model.Stop{{Index: 0, Name: "Origin"}, {Index: 1, Name: "Link"}, {Index: 2, Name: "Target"}}
	routes := []model.RouteInfo{{ShortName: "A"}, {ShortName: "B"}}
	first := model.Connection{DepStop: 0, ArrStop: 1, DepTime: base, ArrTime: base, TripID: "A", TripInstanceID: 1, RouteIdx: 0}
	second := model.Connection{DepStop: 1, ArrStop: 2, DepTime: base, ArrTime: base, TripID: "B", TripInstanceID: 2, RouteIdx: 1}

	t.Run("forward source order is not dependency order", func(t *testing.T) {
		tt := &model.Timetable{
			Stops: stops, Routes: routes, WalkEdges: make([][]model.WalkEdge, len(stops)),
			Connections: []model.Connection{second, first},
		}
		journey, err := PlanEarliestArrival(tt, []int{0}, []int{2}, time.Unix(base, 0))
		if err != nil {
			t.Fatalf("PlanEarliestArrival: %v", err)
		}
		if len(journey.Legs) != 2 || !journey.ArrTime.Equal(time.Unix(base, 0)) {
			t.Fatalf("journey = %+v, want two zero-duration legs", journey)
		}
	})

	t.Run("reverse source order is not dependency order", func(t *testing.T) {
		tt := &model.Timetable{
			Stops: stops, Routes: routes, WalkEdges: make([][]model.WalkEdge, len(stops)),
			Connections: []model.Connection{first, second},
			ReverseConnections: []model.Connection{
				{DepStop: 1, ArrStop: 0, DepTime: -base, ArrTime: -base, TripID: "A", TripInstanceID: 1, RouteIdx: 0},
				{DepStop: 2, ArrStop: 1, DepTime: -base, ArrTime: -base, TripID: "B", TripInstanceID: 2, RouteIdx: 1},
			},
		}
		journey, err := PlanLatestDeparture(tt, []int{0}, []int{2}, time.Unix(base, 0))
		if err != nil {
			t.Fatalf("PlanLatestDeparture: %v", err)
		}
		if len(journey.Legs) != 2 || !journey.DepTime.Equal(time.Unix(base, 0)) {
			t.Fatalf("journey = %+v, want two zero-duration legs", journey)
		}
	})
}

func TestLatestDepartureSameTripEqualArrivalUsesReverseSequenceOrder(t *testing.T) {
	base := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC).Unix()
	tt := &model.Timetable{
		Stops:     []model.Stop{{Index: 0}, {Index: 1}, {Index: 2}},
		Routes:    []model.RouteInfo{{ShortName: "A"}},
		WalkEdges: make([][]model.WalkEdge, 3),
		Connections: []model.Connection{
			{DepStop: 0, ArrStop: 1, DepTime: base, ArrTime: base + 600, TripID: "A", TripInstanceID: 1, RouteIdx: 0},
			{DepStop: 1, ArrStop: 2, DepTime: base + 600, ArrTime: base + 600, TripID: "A", TripInstanceID: 1, RouteIdx: 0},
		},
		TripHeadsign: map[string]string{},
	}

	journey, err := PlanLatestDeparture(tt, []int{0}, []int{1}, time.Unix(base+600, 0))
	if err != nil {
		t.Fatalf("PlanLatestDeparture: %v", err)
	}
	if got, want := len(journey.Legs), 1; got != want {
		t.Fatalf("legs = %d, want %d", got, want)
	}
	if journey.DepTime.Unix() != base || journey.ArrTime.Unix() != base+600 {
		t.Fatalf("journey times = %v to %v", journey.DepTime, journey.ArrTime)
	}
}

func TestJourneyTimesUseExplicitQueryLocation(t *testing.T) {
	base := time.Date(2025, 1, 1, 8, 0, 0, 0, time.UTC).Unix()
	location := time.FixedZone("Australia/Melbourne-test", 11*60*60)
	query := time.Unix(base, 0).In(location)

	for _, test := range []struct {
		name string
		plan func(*model.Timetable) (*model.Journey, error)
	}{
		{
			name: "forward",
			plan: func(tt *model.Timetable) (*model.Journey, error) {
				return PlanEarliestArrival(tt, []int{0}, []int{2}, query)
			},
		},
		{
			name: "reverse",
			plan: func(tt *model.Timetable) (*model.Journey, error) {
				return PlanLatestDeparture(tt, []int{0}, []int{2}, query.Add(20*time.Minute))
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			journey, err := test.plan(fixture(base))
			if err != nil {
				t.Fatal(err)
			}
			if journey.DepTime.Location() != location || journey.ArrTime.Location() != location {
				t.Fatalf("journey locations = %v/%v, want %v", journey.DepTime.Location(), journey.ArrTime.Location(), location)
			}
			for i, leg := range journey.Legs {
				if leg.DepTime.Location() != location || leg.ArrTime.Location() != location {
					t.Fatalf("leg %d locations = %v/%v, want %v", i, leg.DepTime.Location(), leg.ArrTime.Location(), location)
				}
			}
		})
	}
}

func TestPlanNoJourney(t *testing.T) {
	base := time.Date(2025, 1, 1, 8, 0, 0, 0, time.UTC).Unix()
	tt := fixture(base)
	// Depart after every connection has left.
	depart := time.Unix(base+60*60, 0)
	if _, err := PlanEarliestArrival(tt, []int{0}, []int{2}, depart); err != ErrNoJourney {
		t.Errorf("expected ErrNoJourney, got %v", err)
	}
}

func TestPlanEarliestArrivalAllowsEndpointWalking(t *testing.T) {
	base := time.Date(2025, 1, 1, 8, 0, 0, 0, time.UTC).Unix()
	tt := &model.Timetable{
		Stops: []model.Stop{
			{Index: 0, ID: "s0", Name: "Origin Stop"},
			{Index: 1, ID: "s1", Name: "Dest"},
			{Index: 2, ID: "s2", Name: "Nearby Origin"},
		},
		Routes:       []model.RouteInfo{{ShortName: "A", RouteType: 2}},
		TripHeadsign: map[string]string{"A": "to Dest"},
		Footpaths:    make([][]model.Footpath, 3),
		Connections: []model.Connection{
			{DepStop: 0, ArrStop: 1, DepTime: base + 60, ArrTime: base + 10*60, TripID: "A", RouteIdx: 0},
		},
	}
	tt.Footpaths[2] = []model.Footpath{{ToStop: 0, Seconds: 60}}

	j, err := PlanEarliestArrival(tt, []int{2}, []int{1}, time.Unix(base, 0))
	if err != nil {
		t.Fatalf("PlanEarliestArrival: %v", err)
	}
	if len(j.Legs) != 2 {
		t.Fatalf("expected walk plus transit, got %d legs", len(j.Legs))
	}
	if !j.Legs[0].Walk {
		t.Fatalf("first leg should be the endpoint walk")
	}
	if got, want := j.ArrTime, time.Unix(base+10*60, 0); !got.Equal(want) {
		t.Fatalf("arrival = %s, want %s", got.UTC(), want.UTC())
	}
}

func TestPlanLatestDepartureChoosesLatestValidTrip(t *testing.T) {
	base := time.Date(2025, 1, 1, 8, 0, 0, 0, time.UTC).Unix()
	min := func(m int) int64 { return base + int64(m*60) }
	tt := &model.Timetable{
		Stops: []model.Stop{
			{Index: 0, ID: "s0", Name: "Origin"},
			{Index: 1, ID: "s1", Name: "Dest"},
		},
		Routes:       []model.RouteInfo{{ShortName: "A", RouteType: 2}},
		TripHeadsign: map[string]string{"early": "to Dest", "late": "to Dest"},
		Footpaths:    make([][]model.Footpath, 2),
		Connections: []model.Connection{
			{DepStop: 0, ArrStop: 1, DepTime: min(0), ArrTime: min(10), TripID: "early", RouteIdx: 0},
			{DepStop: 0, ArrStop: 1, DepTime: min(5), ArrTime: min(15), TripID: "late", RouteIdx: 0},
		},
	}

	j, err := PlanLatestDeparture(tt, []int{0}, []int{1}, time.Unix(min(15), 0))
	if err != nil {
		t.Fatalf("PlanLatestDeparture: %v", err)
	}
	if got, want := j.DepTime, time.Unix(min(5), 0); !got.Equal(want) {
		t.Fatalf("departure = %s, want %s", got.UTC(), want.UTC())
	}
}

func TestPlanEarliestArrivalMissedTransferIsNoJourney(t *testing.T) {
	base := time.Date(2025, 1, 1, 8, 0, 0, 0, time.UTC).Unix()
	min := func(m int) int64 { return base + int64(m*60) }
	tt := &model.Timetable{
		Stops: []model.Stop{
			{Index: 0, ID: "s0", Name: "Origin"},
			{Index: 1, ID: "s1", Name: "Mid"},
			{Index: 2, ID: "s2", Name: "Dest"},
		},
		Routes:       []model.RouteInfo{{ShortName: "A", RouteType: 2}, {ShortName: "B", RouteType: 2}},
		TripHeadsign: map[string]string{"A": "to Mid", "B": "to Dest"},
		Footpaths:    make([][]model.Footpath, 3),
		Connections: []model.Connection{
			{DepStop: 0, ArrStop: 1, DepTime: min(0), ArrTime: min(5), TripID: "A", RouteIdx: 0},
			{DepStop: 1, ArrStop: 2, DepTime: min(4), ArrTime: min(10), TripID: "B", RouteIdx: 1},
		},
	}

	if _, err := PlanEarliestArrival(tt, []int{0}, []int{2}, time.Unix(base, 0)); err != ErrNoJourney {
		t.Fatalf("expected ErrNoJourney, got %v", err)
	}
}

func TestPlanEarliestArrivalKeepsSameTripAsOneLeg(t *testing.T) {
	base := time.Date(2025, 1, 1, 8, 0, 0, 0, time.UTC).Unix()
	min := func(m int) int64 { return base + int64(m*60) }
	tt := &model.Timetable{
		Stops: []model.Stop{
			{Index: 0, ID: "s0", Name: "Origin"},
			{Index: 1, ID: "s1", Name: "Mid"},
			{Index: 2, ID: "s2", Name: "Dest"},
		},
		Routes:       []model.RouteInfo{{ShortName: "A", RouteType: 2}},
		TripHeadsign: map[string]string{"A": "to Dest"},
		Footpaths:    make([][]model.Footpath, 3),
		Connections: []model.Connection{
			{DepStop: 0, ArrStop: 1, DepTime: min(0), ArrTime: min(5), TripID: "A", RouteIdx: 0},
			{DepStop: 1, ArrStop: 2, DepTime: min(5), ArrTime: min(20), TripID: "A", RouteIdx: 0},
		},
	}

	j, err := PlanEarliestArrival(tt, []int{0}, []int{2}, time.Unix(base, 0))
	if err != nil {
		t.Fatalf("PlanEarliestArrival: %v", err)
	}
	if len(j.Legs) != 1 {
		t.Fatalf("expected same-trip continuation to be one leg, got %d", len(j.Legs))
	}
	if j.Legs[0].FromStop.Index != 0 || j.Legs[0].ToStop.Index != 2 {
		t.Fatalf("leg = %s to %s, want Origin to Dest", j.Legs[0].FromStop.Name, j.Legs[0].ToStop.Name)
	}
}

func TestPlanEarliestArrivalRejectsInvalidStopIndex(t *testing.T) {
	tt := &model.Timetable{
		Stops:     []model.Stop{{Index: 0, ID: "s0", Name: "Origin"}},
		Footpaths: make([][]model.Footpath, 1),
	}
	_, err := PlanEarliestArrival(tt, []int{1}, []int{0}, time.Unix(0, 0))
	if err == nil {
		t.Fatal("expected invalid source stop index error")
	}
}

func TestPlanEarliestArrivalRelaxesTransitiveWalks(t *testing.T) {
	base := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC).Unix()
	tt := &model.Timetable{
		Stops: []model.Stop{
			{Index: 0, Name: "Origin"},
			{Index: 1, Name: "Walk One"},
			{Index: 2, Name: "Walk Two"},
			{Index: 3, Name: "Destination"},
		},
		WalkEdges: make([][]model.WalkEdge, 4),
		Connections: []model.Connection{
			{DepStop: 2, ArrStop: 3, DepTime: base + 60, ArrTime: base + 120, TripID: "trip", RouteIdx: -1},
		},
		TripHeadsign: map[string]string{},
	}
	tt.WalkEdges[0] = []model.WalkEdge{{ToStop: 1, Seconds: 30}}
	tt.WalkEdges[1] = []model.WalkEdge{{ToStop: 2, Seconds: 30}}

	journey, err := PlanEarliestArrival(tt, []int{0}, []int{3}, time.Unix(base, 0))
	if err != nil {
		t.Fatalf("PlanEarliestArrival: %v", err)
	}
	if got, want := len(journey.Legs), 3; got != want {
		t.Fatalf("legs = %d, want %d", got, want)
	}
	if !journey.Legs[0].Walk || !journey.Legs[1].Walk || journey.Legs[2].Walk {
		t.Fatalf("leg kinds = %#v, want walk, walk, transit", journey.Legs)
	}
}

func TestPlanLatestDepartureRelaxesTransitiveWalks(t *testing.T) {
	base := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC).Unix()
	tt := &model.Timetable{
		Stops: []model.Stop{
			{Index: 0, Name: "Origin"},
			{Index: 1, Name: "Transit Arrival"},
			{Index: 2, Name: "Walk One"},
			{Index: 3, Name: "Destination"},
		},
		WalkEdges: make([][]model.WalkEdge, 4),
		Connections: []model.Connection{
			{DepStop: 0, ArrStop: 1, DepTime: base, ArrTime: base + 100, TripID: "trip", RouteIdx: -1},
		},
		TripHeadsign: map[string]string{},
	}
	tt.WalkEdges[1] = []model.WalkEdge{{ToStop: 2, Seconds: 30}}
	tt.WalkEdges[2] = []model.WalkEdge{{ToStop: 3, Seconds: 30}}

	journey, err := PlanLatestDeparture(tt, []int{0}, []int{3}, time.Unix(base+160, 0))
	if err != nil {
		t.Fatalf("PlanLatestDeparture: %v", err)
	}
	if got, want := len(journey.Legs), 3; got != want {
		t.Fatalf("legs = %d, want %d", got, want)
	}
	if got, want := journey.DepTime, time.Unix(base, 0); !got.Equal(want) {
		t.Fatalf("departure = %s, want %s", got, want)
	}
}

func TestDenseTripInstancesDoNotCrossServiceDays(t *testing.T) {
	base := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC).Unix()
	tt := &model.Timetable{
		Stops: []model.Stop{
			{Index: 0, Name: "Previous Origin"},
			{Index: 1, Name: "Previous Destination"},
			{Index: 2, Name: "Current Origin"},
			{Index: 3, Name: "Current Destination"},
		},
		WalkEdges: make([][]model.WalkEdge, 4),
		Connections: []model.Connection{
			{DepStop: 0, ArrStop: 1, DepTime: base, ArrTime: base + 60, TripID: "same-raw-trip", TripInstanceID: 1, RouteIdx: -1},
			{DepStop: 2, ArrStop: 3, DepTime: base + 120, ArrTime: base + 180, TripID: "same-raw-trip", TripInstanceID: 2, RouteIdx: -1},
		},
		TripHeadsign: map[string]string{},
	}

	_, err := PlanEarliestArrival(tt, []int{0}, []int{3}, time.Unix(base, 0))
	if !errors.Is(err, ErrNoJourney) {
		t.Fatalf("error = %v, want ErrNoJourney", err)
	}
}

func TestPlanRejectsSparseTripInstanceIDs(t *testing.T) {
	base := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC).Unix()
	tt := &model.Timetable{
		Stops:     []model.Stop{{Index: 0}, {Index: 1}},
		WalkEdges: make([][]model.WalkEdge, 2),
		Connections: []model.Connection{
			{DepStop: 0, ArrStop: 1, DepTime: base, ArrTime: base + 60, TripInstanceID: 2},
		},
	}
	_, err := PlanEarliestArrival(tt, []int{0}, []int{1}, time.Unix(base, 0))
	if err == nil {
		t.Fatal("expected sparse trip-instance validation error")
	}
}

func TestPassengerActionPolicies(t *testing.T) {
	base := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC).Unix()

	t.Run("forbidden pickup", func(t *testing.T) {
		tt := &model.Timetable{
			Stops:     []model.Stop{{Index: 0}, {Index: 1}},
			WalkEdges: make([][]model.WalkEdge, 2),
			Connections: []model.Connection{
				{DepStop: 0, ArrStop: 1, DepTime: base, ArrTime: base + 60, TripID: "trip", PickupPolicy: model.PassengerActionForbidden},
			},
		}
		_, err := PlanEarliestArrival(tt, []int{0}, []int{1}, time.Unix(base, 0))
		if !errors.Is(err, ErrNoJourney) {
			t.Fatalf("error = %v, want ErrNoJourney", err)
		}
	})

	t.Run("stay onboard through non-service stop", func(t *testing.T) {
		tt := &model.Timetable{
			Stops:     []model.Stop{{Index: 0}, {Index: 1}, {Index: 2}},
			WalkEdges: make([][]model.WalkEdge, 3),
			Connections: []model.Connection{
				{DepStop: 0, ArrStop: 1, DepTime: base, ArrTime: base + 60, TripID: "trip", TripInstanceID: 1, DropOffPolicy: model.PassengerActionForbidden, RouteIdx: -1},
				{DepStop: 1, ArrStop: 2, DepTime: base + 60, ArrTime: base + 120, TripID: "trip", TripInstanceID: 1, PickupPolicy: model.PassengerActionForbidden, RouteIdx: -1},
			},
			TripHeadsign: map[string]string{},
		}
		journey, err := PlanEarliestArrival(tt, []int{0}, []int{2}, time.Unix(base, 0))
		if err != nil {
			t.Fatalf("PlanEarliestArrival: %v", err)
		}
		if got, want := len(journey.Legs), 1; got != want {
			t.Fatalf("legs = %d, want %d", got, want)
		}
	})

	t.Run("conditional requires opt in", func(t *testing.T) {
		tt := &model.Timetable{
			Stops:     []model.Stop{{Index: 0}, {Index: 1}},
			WalkEdges: make([][]model.WalkEdge, 2),
			Connections: []model.Connection{
				{
					DepStop: 0, ArrStop: 1, DepTime: base, ArrTime: base + 60, TripID: "trip", RouteIdx: -1,
					PickupPolicy: model.PassengerActionPhoneAgency, DropOffPolicy: model.PassengerActionCoordinateDriver,
				},
			},
			TripHeadsign: map[string]string{},
		}
		_, err := PlanEarliestArrival(tt, []int{0}, []int{1}, time.Unix(base, 0))
		if !errors.Is(err, ErrNoJourney) {
			t.Fatalf("default error = %v, want ErrNoJourney", err)
		}

		journey, err := PlanEarliestArrivalContext(
			context.Background(), tt,
			[]model.Endpoint{{Stop: 0}}, []model.Endpoint{{Stop: 1}},
			time.Unix(base, 0), PlanOptions{AllowConditional: true},
		)
		if err != nil {
			t.Fatalf("conditional plan: %v", err)
		}
		if !journey.Legs[0].Conditional {
			t.Fatal("conditional leg was not marked")
		}
	})
}

func TestWeightedAccessAndEgress(t *testing.T) {
	base := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC).Unix()
	origin := model.Stop{Index: -1, Name: "Address A"}
	destination := model.Stop{Index: -1, Name: "Address B"}
	tt := &model.Timetable{
		Stops: []model.Stop{
			{Index: 0, Name: "Origin Stop"},
			{Index: 1, Name: "Destination Stop"},
		},
		WalkEdges: make([][]model.WalkEdge, 2),
		Connections: []model.Connection{
			{DepStop: 0, ArrStop: 1, DepTime: base + 60, ArrTime: base + 300, TripID: "trip", RouteIdx: -1},
		},
		TripHeadsign: map[string]string{},
	}
	journey, err := PlanEarliestArrivalContext(
		context.Background(), tt,
		[]model.Endpoint{{Stop: 0, WalkSeconds: 60, Location: &origin}},
		[]model.Endpoint{{Stop: 1, WalkSeconds: 120, Location: &destination}},
		time.Unix(base, 0), PlanOptions{},
	)
	if err != nil {
		t.Fatalf("PlanEarliestArrivalContext: %v", err)
	}
	if got, want := len(journey.Legs), 3; got != want {
		t.Fatalf("legs = %d, want %d", got, want)
	}
	if got, want := journey.DepTime, time.Unix(base, 0); !got.Equal(want) {
		t.Fatalf("departure = %s, want %s", got, want)
	}
	if got, want := journey.ArrTime, time.Unix(base+420, 0); !got.Equal(want) {
		t.Fatalf("arrival = %s, want %s", got, want)
	}
	if journey.Legs[0].FromStop.Name != "Address A" || journey.Legs[2].ToStop.Name != "Address B" {
		t.Fatalf("endpoint labels not preserved: %#v", journey.Legs)
	}
}

func TestLatestDepartureWeightedAccessAndEgress(t *testing.T) {
	base := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC).Unix()
	origin := model.Stop{Index: -1, Name: "Address A"}
	destination := model.Stop{Index: -1, Name: "Address B"}
	tt := &model.Timetable{
		Stops: []model.Stop{
			{Index: 0, Name: "Origin Stop"},
			{Index: 1, Name: "Destination Stop"},
		},
		WalkEdges: make([][]model.WalkEdge, 2),
		Connections: []model.Connection{
			{DepStop: 0, ArrStop: 1, DepTime: base + 60, ArrTime: base + 300, TripID: "trip", RouteIdx: -1},
		},
		TripHeadsign: map[string]string{},
	}
	journey, err := PlanLatestDepartureContext(
		context.Background(), tt,
		[]model.Endpoint{{Stop: 0, WalkSeconds: 60, Location: &origin}},
		[]model.Endpoint{{Stop: 1, WalkSeconds: 120, Location: &destination}},
		time.Unix(base+420, 0), PlanOptions{},
	)
	if err != nil {
		t.Fatalf("PlanLatestDepartureContext: %v", err)
	}
	if got, want := len(journey.Legs), 3; got != want {
		t.Fatalf("legs = %d, want %d", got, want)
	}
	if got, want := journey.DepTime, time.Unix(base, 0); !got.Equal(want) {
		t.Fatalf("departure = %s, want %s", got, want)
	}
	if got, want := journey.ArrTime, time.Unix(base+420, 0); !got.Equal(want) {
		t.Fatalf("arrival = %s, want %s", got, want)
	}
}

func TestLatestDepartureUsesPrecomputedReverseGraph(t *testing.T) {
	base := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC).Unix()
	tt := &model.Timetable{
		Stops:            []model.Stop{{Index: 0}, {Index: 1}},
		WalkEdges:        make([][]model.WalkEdge, 2),
		ReverseWalkEdges: make([][]model.WalkEdge, 2),
		ReverseConnections: []model.Connection{{
			DepStop: 1, ArrStop: 0, DepTime: -(base + 60), ArrTime: -base, TripID: "trip", RouteIdx: -1,
		}},
		TripHeadsign: map[string]string{},
	}
	journey, err := PlanLatestDeparture(tt, []int{0}, []int{1}, time.Unix(base+60, 0))
	if err != nil {
		t.Fatalf("PlanLatestDeparture: %v", err)
	}
	if got, want := journey.DepTime, time.Unix(base, 0); !got.Equal(want) {
		t.Fatalf("departure = %s, want %s", got, want)
	}
}

func TestTripInstanceMetadataLabelsCompactConnection(t *testing.T) {
	base := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC).Unix()
	tt := &model.Timetable{
		Stops:     []model.Stop{{Index: 0}, {Index: 1}},
		Routes:    []model.RouteInfo{{ShortName: "R", RouteType: 2}},
		WalkEdges: make([][]model.WalkEdge, 2),
		Connections: []model.Connection{
			{DepStop: 0, ArrStop: 1, DepTime: base, ArrTime: base + 60, TripInstanceID: 1, RouteIdx: -1},
		},
		TripInstances: []model.ServiceInstance{
			{},
			{ID: 1, TripID: "trip", RouteIdx: 0, Headsign: "Destination", BlockID: "2:block"},
		},
	}
	journey, err := PlanEarliestArrival(tt, []int{0}, []int{1}, time.Unix(base, 0))
	if err != nil {
		t.Fatalf("PlanEarliestArrival: %v", err)
	}
	leg := journey.Legs[0]
	if leg.TripID != "trip" || leg.RouteShortName != "R" || leg.Headsign != "Destination" || leg.BlockID != "2:block" {
		t.Fatalf("leg metadata = %#v", leg)
	}
}

func TestSameStopJourneyUsesQueryTimestamp(t *testing.T) {
	query := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)
	tt := &model.Timetable{
		Stops:     []model.Stop{{Index: 0, Name: "Here"}},
		WalkEdges: make([][]model.WalkEdge, 1),
	}
	journey, err := PlanEarliestArrival(tt, []int{0}, []int{0}, query)
	if err != nil {
		t.Fatalf("PlanEarliestArrival: %v", err)
	}
	if len(journey.Legs) != 0 {
		t.Fatalf("legs = %d, want 0", len(journey.Legs))
	}
	if !journey.DepTime.Equal(query) || !journey.ArrTime.Equal(query) {
		t.Fatalf("journey times = %s to %s, want %s", journey.DepTime, journey.ArrTime, query)
	}
}

func TestContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tt := &model.Timetable{
		Stops:     []model.Stop{{Index: 0}},
		WalkEdges: make([][]model.WalkEdge, 1),
	}
	_, err := PlanEarliestArrivalWithContext(ctx, tt, []int{0}, []int{0}, time.Now())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestReconstructRejectsMissingPredecessor(t *testing.T) {
	tt := &model.Timetable{Stops: []model.Stop{{Index: 0}, {Index: 1}}}
	target := &journeyLabel{
		stop:    1,
		arrival: 200,
		step:    labelStep{kind: labelStepWalk},
	}
	_, err := reconstruct(tt, nil, target, []model.Endpoint{{Stop: 0}}, 100)
	if err == nil {
		t.Fatal("expected missing predecessor error")
	}
}

func TestTransferRuleSpecificityPrecedence(t *testing.T) {
	base := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC).Unix()
	stopForbidden := model.TransferRule{
		FromStop: 1, ToStop: 1, Type: model.TransferForbidden,
		FromRouteIdx: -1, ToRouteIdx: -1,
	}
	routePermitted := model.TransferRule{
		FromStop: -1, ToStop: -1, Type: model.TransferRecommended,
		FromRouteIdx: 0, ToRouteIdx: 1,
	}
	tripForbidden := model.TransferRule{
		FromStop: -1, ToStop: -1, Type: model.TransferForbidden,
		FromRouteIdx: -1, ToRouteIdx: -1,
		FromTripInstanceID: 1, ToTripInstanceID: 2,
	}

	t.Run("route pair overrides stop pair", func(t *testing.T) {
		tt := transferFixture(base)
		tt.TransferRules = []model.TransferRule{stopForbidden, routePermitted}
		if _, err := PlanEarliestArrival(tt, []int{0}, []int{2}, time.Unix(base, 0)); err != nil {
			t.Fatalf("PlanEarliestArrival: %v", err)
		}
	})

	t.Run("trip pair overrides route pair", func(t *testing.T) {
		tt := transferFixture(base)
		tt.TransferRules = []model.TransferRule{stopForbidden, routePermitted, tripForbidden}
		_, err := PlanEarliestArrival(tt, []int{0}, []int{2}, time.Unix(base, 0))
		if !errors.Is(err, ErrNoJourney) {
			t.Fatalf("error = %v, want ErrNoJourney", err)
		}
	})
}

func TestTransferRuleSpecificityUsesOfficialGTFSOrder(t *testing.T) {
	wildcard := model.TransferRule{FromStop: -1, ToStop: -1, FromRouteIdx: -1, ToRouteIdx: -1}
	rules := []struct {
		name string
		rule model.TransferRule
	}{
		{name: "stops only", rule: model.TransferRule{FromStop: 1, ToStop: 1, FromRouteIdx: -1, ToRouteIdx: -1}},
		{name: "one route", rule: func() model.TransferRule { r := wildcard; r.FromRouteIdx = 0; return r }()},
		{name: "both routes", rule: func() model.TransferRule { r := wildcard; r.FromRouteIdx, r.ToRouteIdx = 0, 1; return r }()},
		{name: "one trip", rule: func() model.TransferRule { r := wildcard; r.FromTripInstanceID = 1; return r }()},
		{name: "one trip plus opposite route", rule: func() model.TransferRule { r := wildcard; r.FromTripInstanceID, r.ToRouteIdx = 1, 1; return r }()},
		{name: "both trips", rule: func() model.TransferRule { r := wildcard; r.FromTripInstanceID, r.ToTripInstanceID = 1, 2; return r }()},
	}
	previous := -1
	for _, test := range rules {
		score := transferRuleSpecificity(test.rule)
		if score <= previous {
			t.Fatalf("%s score = %d, want greater than prior class score %d", test.name, score, previous)
		}
		previous = score
	}
}

func TestTripSpecificityOverridesRoutePairInPlanning(t *testing.T) {
	base := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC).Unix()
	wildcard := model.TransferRule{FromStop: -1, ToStop: -1, FromRouteIdx: -1, ToRouteIdx: -1}
	routePairForbidden := wildcard
	routePairForbidden.FromRouteIdx = 0
	routePairForbidden.ToRouteIdx = 1
	routePairForbidden.Type = model.TransferForbidden
	oneTripPermitted := wildcard
	oneTripPermitted.FromTripInstanceID = 1
	oneTripPermitted.Type = model.TransferRecommended

	tt := transferFixture(base)
	tt.TransferRules = []model.TransferRule{routePairForbidden, oneTripPermitted}
	if _, err := PlanEarliestArrival(tt, []int{0}, []int{2}, time.Unix(base, 0)); err != nil {
		t.Fatalf("one-trip rule must override both-route prohibition: %v", err)
	}

	oneTripForbidden := oneTripPermitted
	oneTripForbidden.Type = model.TransferForbidden
	oneTripOppositeRoutePermitted := oneTripPermitted
	oneTripOppositeRoutePermitted.ToRouteIdx = 1
	tt = transferFixture(base)
	tt.TransferRules = []model.TransferRule{oneTripForbidden, oneTripOppositeRoutePermitted}
	if _, err := PlanEarliestArrival(tt, []int{0}, []int{2}, time.Unix(base, 0)); err != nil {
		t.Fatalf("one-trip-plus-opposite-route rule must override one-trip prohibition: %v", err)
	}
}

func TestForbiddenTransferRetainsAlternativeArrivalContext(t *testing.T) {
	base := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC).Unix()
	tt := &model.Timetable{
		Stops:     []model.Stop{{Index: 0}, {Index: 1}, {Index: 2}},
		Routes:    []model.RouteInfo{{ShortName: "A1"}, {ShortName: "A2"}, {ShortName: "B"}},
		WalkEdges: make([][]model.WalkEdge, 3),
		Connections: []model.Connection{
			{DepStop: 0, ArrStop: 1, DepTime: base, ArrTime: base + 50, TripID: "A1", TripInstanceID: 1, RouteIdx: 0},
			{DepStop: 0, ArrStop: 1, DepTime: base + 10, ArrTime: base + 60, TripID: "A2", TripInstanceID: 2, RouteIdx: 1},
			{DepStop: 1, ArrStop: 2, DepTime: base + 70, ArrTime: base + 100, TripID: "B", TripInstanceID: 3, RouteIdx: 2},
		},
		TransferRules: []model.TransferRule{{
			FromStop: -1, ToStop: -1, Type: model.TransferForbidden,
			FromRouteIdx: -1, ToRouteIdx: -1,
			FromTripInstanceID: 1, ToTripInstanceID: 3,
		}},
		TripHeadsign: map[string]string{},
	}
	journey, err := PlanEarliestArrival(tt, []int{0}, []int{2}, time.Unix(base, 0))
	if err != nil {
		t.Fatalf("PlanEarliestArrival: %v", err)
	}
	if got, want := journey.Legs[0].TripID, "A2"; got != want {
		t.Fatalf("incoming trip = %q, want %q", got, want)
	}
}

func TestMinimumTransferTime(t *testing.T) {
	base := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC).Unix()
	for _, tc := range []struct {
		name        string
		minimum     int
		wantJourney bool
	}{
		{name: "exact minimum", minimum: 60, wantJourney: true},
		{name: "missed by one second", minimum: 61, wantJourney: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tt := transferFixture(base)
			tt.TransferRules = []model.TransferRule{{
				FromStop: 1, ToStop: 1, Type: model.TransferMinimumTime,
				MinTransferSeconds: tc.minimum,
				FromRouteIdx:       -1, ToRouteIdx: -1,
			}}
			_, err := PlanEarliestArrival(tt, []int{0}, []int{2}, time.Unix(base, 0))
			if tc.wantJourney && err != nil {
				t.Fatalf("PlanEarliestArrival: %v", err)
			}
			if !tc.wantJourney && !errors.Is(err, ErrNoJourney) {
				t.Fatalf("error = %v, want ErrNoJourney", err)
			}
		})
	}
}

func TestTimedTransferPermitsFeasibleScheduledConnection(t *testing.T) {
	base := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC).Unix()
	tt := transferFixture(base)
	tt.TransferRules = []model.TransferRule{{
		FromStop: 1, ToStop: 1, Type: model.TransferTimed,
		FromRouteIdx: -1, ToRouteIdx: -1,
	}}
	if _, err := PlanEarliestArrival(tt, []int{0}, []int{2}, time.Unix(base, 0)); err != nil {
		t.Fatalf("PlanEarliestArrival: %v", err)
	}
}

func TestMinimumTransferTimeReverseParity(t *testing.T) {
	base := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC).Unix()
	for _, tc := range []struct {
		minimum     int
		wantJourney bool
	}{
		{minimum: 60, wantJourney: true},
		{minimum: 61, wantJourney: false},
	} {
		t.Run(fmt.Sprintf("minimum_%d", tc.minimum), func(t *testing.T) {
			tt := transferFixture(base)
			tt.TransferRules = []model.TransferRule{{
				FromStop: 1, ToStop: 1, Type: model.TransferMinimumTime,
				MinTransferSeconds: tc.minimum,
				FromRouteIdx:       -1, ToRouteIdx: -1,
			}}
			_, err := PlanLatestDeparture(tt, []int{0}, []int{2}, time.Unix(base+180, 0))
			if tc.wantJourney && err != nil {
				t.Fatalf("PlanLatestDeparture: %v", err)
			}
			if !tc.wantJourney && !errors.Is(err, ErrNoJourney) {
				t.Fatalf("error = %v, want ErrNoJourney", err)
			}
		})
	}
}

func TestStayOnboardContinuation(t *testing.T) {
	base := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC).Unix()
	tt := continuationFixture(base)
	tt.Continuations = []model.Continuation{{
		FromTripInstanceID: 1,
		ToTripInstanceID:   2,
		FromStop:           1,
		ToStop:             1,
		Type:               model.TransferStayOnboard,
	}}
	journey, err := PlanEarliestArrival(tt, []int{0}, []int{2}, time.Unix(base, 0))
	if err != nil {
		t.Fatalf("PlanEarliestArrival: %v", err)
	}
	if got, want := len(journey.Legs), 2; got != want {
		t.Fatalf("legs = %d, want %d", got, want)
	}
	if journey.Legs[0].StayOnboard || !journey.Legs[1].StayOnboard {
		t.Fatalf("stay-onboard markers = %v, %v", journey.Legs[0].StayOnboard, journey.Legs[1].StayOnboard)
	}
	if journey.Transfers != 0 {
		t.Fatalf("transfers = %d, want 0", journey.Transfers)
	}
}

func TestStayOnboardContinuationAcrossDistinctStops(t *testing.T) {
	base := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC).Unix()
	tt := &model.Timetable{
		Stops:     []model.Stop{{Index: 0}, {Index: 1}, {Index: 2}, {Index: 3}},
		Routes:    []model.RouteInfo{{ShortName: "A"}, {ShortName: "B"}},
		WalkEdges: make([][]model.WalkEdge, 4),
		Connections: []model.Connection{
			{
				DepStop: 0, ArrStop: 1, DepTime: base, ArrTime: base + 60,
				TripID: "A", TripInstanceID: 1, RouteIdx: 0,
				DropOffPolicy: model.PassengerActionForbidden,
			},
			{
				DepStop: 2, ArrStop: 3, DepTime: base + 90, ArrTime: base + 150,
				TripID: "B", TripInstanceID: 2, RouteIdx: 1,
				PickupPolicy: model.PassengerActionForbidden,
			},
		},
		Continuations: []model.Continuation{{
			FromTripInstanceID: 1,
			ToTripInstanceID:   2,
			FromStop:           1,
			ToStop:             2,
			Type:               model.TransferStayOnboard,
		}},
		TripHeadsign: map[string]string{},
	}

	journey, err := PlanEarliestArrival(tt, []int{0}, []int{3}, time.Unix(base, 0))
	if err != nil {
		t.Fatalf("PlanEarliestArrival: %v", err)
	}
	if got, want := len(journey.Legs), 2; got != want {
		t.Fatalf("legs = %d, want %d", got, want)
	}
	if journey.Legs[0].StayOnboard || !journey.Legs[1].StayOnboard {
		t.Fatalf("stay-onboard markers = %v, %v", journey.Legs[0].StayOnboard, journey.Legs[1].StayOnboard)
	}
	if journey.Transfers != 0 {
		t.Fatalf("transfers = %d, want 0", journey.Transfers)
	}
}

func TestNoStayOnboardOverridesContinuation(t *testing.T) {
	base := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC).Unix()
	tt := continuationFixture(base)
	tt.Continuations = []model.Continuation{
		{FromTripInstanceID: 1, ToTripInstanceID: 2, FromStop: 1, ToStop: 1, Type: model.TransferStayOnboard},
		{FromTripInstanceID: 1, ToTripInstanceID: 2, FromStop: 1, ToStop: 1, Type: model.TransferNoStayOnboard},
	}
	_, err := PlanEarliestArrival(tt, []int{0}, []int{2}, time.Unix(base, 0))
	if !errors.Is(err, ErrNoJourney) {
		t.Fatalf("error = %v, want ErrNoJourney", err)
	}
}

func TestStayOnboardContinuationReverseParity(t *testing.T) {
	base := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC).Unix()
	tt := continuationFixture(base)
	tt.Continuations = []model.Continuation{{
		FromTripInstanceID: 1,
		ToTripInstanceID:   2,
		FromStop:           1,
		ToStop:             1,
		Type:               model.TransferStayOnboard,
	}}
	journey, err := PlanLatestDeparture(tt, []int{0}, []int{2}, time.Unix(base+120, 0))
	if err != nil {
		t.Fatalf("PlanLatestDeparture: %v", err)
	}
	if got, want := len(journey.Legs), 2; got != want {
		t.Fatalf("legs = %d, want %d", got, want)
	}
	if journey.Legs[0].StayOnboard || !journey.Legs[1].StayOnboard {
		t.Fatalf("stay-onboard markers = %v, %v", journey.Legs[0].StayOnboard, journey.Legs[1].StayOnboard)
	}
	if journey.Transfers != 0 {
		t.Fatalf("transfers = %d, want 0", journey.Transfers)
	}
}

func TestNoStayOnboardOverridesContinuationReverseParity(t *testing.T) {
	base := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC).Unix()
	tt := continuationFixture(base)
	tt.Continuations = []model.Continuation{
		{FromTripInstanceID: 1, ToTripInstanceID: 2, FromStop: 1, ToStop: 1, Type: model.TransferStayOnboard},
		{FromTripInstanceID: 1, ToTripInstanceID: 2, FromStop: 1, ToStop: 1, Type: model.TransferNoStayOnboard},
	}
	_, err := PlanLatestDeparture(tt, []int{0}, []int{2}, time.Unix(base+120, 0))
	if !errors.Is(err, ErrNoJourney) {
		t.Fatalf("error = %v, want ErrNoJourney", err)
	}
}

func transferFixture(base int64) *model.Timetable {
	return &model.Timetable{
		Stops:     []model.Stop{{Index: 0}, {Index: 1}, {Index: 2}},
		Routes:    []model.RouteInfo{{ShortName: "A"}, {ShortName: "B"}},
		WalkEdges: make([][]model.WalkEdge, 3),
		Connections: []model.Connection{
			{DepStop: 0, ArrStop: 1, DepTime: base, ArrTime: base + 60, TripID: "A", TripInstanceID: 1, RouteIdx: 0},
			{DepStop: 1, ArrStop: 2, DepTime: base + 120, ArrTime: base + 180, TripID: "B", TripInstanceID: 2, RouteIdx: 1},
		},
		TripHeadsign: map[string]string{},
	}
}

func continuationFixture(base int64) *model.Timetable {
	return &model.Timetable{
		Stops:     []model.Stop{{Index: 0}, {Index: 1}, {Index: 2}},
		Routes:    []model.RouteInfo{{ShortName: "A"}, {ShortName: "B"}},
		WalkEdges: make([][]model.WalkEdge, 3),
		Connections: []model.Connection{
			{
				DepStop: 0, ArrStop: 1, DepTime: base, ArrTime: base + 60,
				TripID: "A", TripInstanceID: 1, RouteIdx: 0,
				DropOffPolicy: model.PassengerActionForbidden,
			},
			{
				DepStop: 1, ArrStop: 2, DepTime: base + 60, ArrTime: base + 120,
				TripID: "B", TripInstanceID: 2, RouteIdx: 1,
				PickupPolicy: model.PassengerActionForbidden,
			},
		},
		TripHeadsign: map[string]string{},
	}
}
