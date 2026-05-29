package router

import (
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
