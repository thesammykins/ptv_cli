package cmd

import (
	"testing"
	"time"

	realtimegtfs "github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	gtfsdata "github.com/thesammykins/ptv_cli/internal/gtfs"
	"github.com/thesammykins/ptv_cli/internal/gtfsrt"
	"google.golang.org/protobuf/proto"
)

func TestNextDepartureMatchesByTimeAndRouteWithoutReplacingGTFSIdentity(t *testing.T) {
	when := "2026-07-24T03:44:00Z"
	primary := nextDepartureOutput{ScheduledDepartureUTC: &when, RouteLabel: "109", GTFSRouteID: "3:route-109"}
	enrichment := nextDepartureOutput{ScheduledDepartureUTC: &when, RouteLabel: "109", PTVRouteID: 1234, PlatformNumber: stringPtr("2")}

	if !nextDepartureMatches(primary, enrichment) {
		t.Fatal("expected matching departure")
	}
	if primary.GTFSRouteID != "3:route-109" {
		t.Fatalf("GTFS route ID = %q", primary.GTFSRouteID)
	}
}

func TestApplyTripUpdateContinuesPastNonMatchingStop(t *testing.T) {
	now := time.Date(2026, 7, 24, 3, 0, 0, 0, time.UTC)
	tripID, startDate := "trip-1", "20260724"
	firstSequence, matchingSequence := uint32(1), uint32(2)
	firstStopID, matchingStopID := "other-stop", "stop-2"
	estimated := now.Add(3 * time.Minute).Unix()
	relationship := realtimegtfs.TripDescriptor_SCHEDULED
	snapshot := gtfsrt.NormalizeSnapshot(gtfsrt.Feed{ID: "test-trip-updates", Kind: gtfsrt.FeedKindTripUpdates}, &realtimegtfs.FeedMessage{Entity: []*realtimegtfs.FeedEntity{{
		Id: proto.String("entity-1"),
		TripUpdate: &realtimegtfs.TripUpdate{
			Trip: &realtimegtfs.TripDescriptor{TripId: &tripID, StartDate: &startDate, ScheduleRelationship: &relationship},
			StopTimeUpdate: []*realtimegtfs.TripUpdate_StopTimeUpdate{
				{StopSequence: &firstSequence, StopId: &firstStopID},
				{StopSequence: &matchingSequence, StopId: &matchingStopID, Departure: &realtimegtfs.TripUpdate_StopTimeEvent{Time: &estimated}},
			},
		},
	}}}, now)

	item := nextDepartureOutput{}
	departure := gtfsdata.DepartureResult{TripID: "2:" + tripID, StopID: "2:" + matchingStopID, StopSequence: 2, ServiceDate: startDate}
	applyTripUpdate(&item, departure, snapshot, now)
	if item.EstimatedDepartureUTC == nil || *item.EstimatedDepartureUTC != now.Add(3*time.Minute).Format(time.RFC3339) {
		t.Fatalf("estimated departure = %v, want later matching stop update", item.EstimatedDepartureUTC)
	}
}

func TestFilterNextPlatformAfterEnrichment(t *testing.T) {
	first, second, missing := "1", "2", ""
	filtered := filterNextPlatform([]nextDepartureOutput{
		{PlatformNumber: &first},
		{PlatformNumber: &second},
		{PlatformNumber: &missing},
	}, "2")
	if len(filtered) != 1 || filtered[0].PlatformNumber == nil || *filtered[0].PlatformNumber != "2" {
		t.Fatalf("filtered departures = %+v, want only platform 2", filtered)
	}
}

func stringPtr(value string) *string { return &value }
