package router

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/thesammykins/ptv_cli/internal/model"
)

var benchmarkJourneySink *model.Journey

func BenchmarkPlanEarliestArrival(b *testing.B) {
	for _, benchmark := range []struct {
		name       string
		contextual bool
	}{
		{name: "no_rules"},
		{name: "contextual", contextual: true},
	} {
		b.Run(benchmark.name, func(b *testing.B) {
			tt, depart, _ := benchmarkTimetable(benchmark.contextual)
			sources := []model.Endpoint{{Stop: 0}}
			targets := []model.Endpoint{{Stop: len(tt.Stops) - 1}}
			ctx := context.Background()

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				journey, err := PlanEarliestArrivalContext(
					ctx, tt, sources, targets, depart, PlanOptions{},
				)
				if err != nil {
					b.Fatal(err)
				}
				benchmarkJourneySink = journey
			}
		})
	}
}

func BenchmarkPlanLatestDeparture(b *testing.B) {
	for _, benchmark := range []struct {
		name       string
		contextual bool
	}{
		{name: "no_rules"},
		{name: "contextual", contextual: true},
	} {
		b.Run(benchmark.name, func(b *testing.B) {
			tt, _, arriveBy := benchmarkTimetable(benchmark.contextual)
			sources := []model.Endpoint{{Stop: 0}}
			targets := []model.Endpoint{{Stop: len(tt.Stops) - 1}}
			ctx := context.Background()

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				journey, err := PlanLatestDepartureContext(
					ctx, tt, sources, targets, arriveBy, PlanOptions{},
				)
				if err != nil {
					b.Fatal(err)
				}
				benchmarkJourneySink = journey
			}
		})
	}
}

func benchmarkTimetable(contextual bool) (*model.Timetable, time.Time, time.Time) {
	const (
		connectionCount = 1024
		segmentsPerTrip = 16
		baseUnix        = int64(1_800_100_000)
	)
	stopCount := connectionCount + 1
	tripCount := connectionCount / segmentsPerTrip
	tt := &model.Timetable{
		Stops:         make([]model.Stop, stopCount),
		Routes:        make([]model.RouteInfo, 4),
		Connections:   make([]model.Connection, 0, connectionCount),
		WalkEdges:     make([][]model.WalkEdge, stopCount),
		TripInstances: make([]model.ServiceInstance, tripCount+1),
	}
	for i := range tt.Stops {
		tt.Stops[i] = model.Stop{Index: i, ID: fmt.Sprintf("s%d", i), Name: fmt.Sprintf("Stop %d", i)}
	}
	for i := range tt.Routes {
		tt.Routes[i] = model.RouteInfo{ShortName: fmt.Sprintf("R%d", i)}
	}

	clock := baseUnix + 60
	for segment := 0; segment < connectionCount; segment++ {
		tripInstanceID := model.TripInstanceID(1 + segment/segmentsPerTrip)
		tripID := fmt.Sprintf("trip-%d", tripInstanceID)
		routeIdx := int(tripInstanceID) % len(tt.Routes)
		if tt.TripInstances[tripInstanceID].ID == model.UnknownTripInstanceID {
			tt.TripInstances[tripInstanceID] = model.ServiceInstance{
				ID: tripInstanceID, TripID: tripID, RouteIdx: routeIdx,
			}
		}
		tt.Connections = append(tt.Connections, model.Connection{
			DepStop: segment, ArrStop: segment + 1,
			DepTime: clock, ArrTime: clock + 30,
			TripID: tripID, TripInstanceID: tripInstanceID, RouteIdx: routeIdx,
		})
		clock += 35
	}
	if contextual {
		tt.TransferRules = []model.TransferRule{{
			FromStop: -1, ToStop: -1,
			FromRouteIdx: -1, ToRouteIdx: -1,
			Type: model.TransferMinimumTime, MinTransferSeconds: 3,
		}}
	}

	// Keep reverse setup out of the measured query. Production query loaders
	// can provide these canonical reverse indexes directly.
	tt.ReverseConnections = reverseConnections(tt.Connections)
	tt.ReverseWalkEdges = reverseWalkEdges(tt.WalkEdges, len(tt.Stops))
	return tt, time.Unix(baseUnix, 0), time.Unix(clock, 0)
}
