package gtfsrt

import (
	"testing"
	"time"

	gtfs "github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"google.golang.org/protobuf/proto"
)

func TestNormalizeTripUpdatesAndAlertsPreservesNamespacesAndFreshness(t *testing.T) {
	now := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
	feedTimestamp := uint64(now.Add(-10 * time.Second).Unix())
	updateTimestamp := uint64(now.Add(-20 * time.Second).Unix())
	tripID, routeID, startDate, stopID := "local-trip", "local-route", "20260724", "local-stop"
	sequence := uint32(3)
	arrivalTime := now.Add(2 * time.Minute).Unix()
	delay := int32(60)
	relationship := gtfs.TripUpdate_StopTimeUpdate_SKIPPED
	tripRelationship := gtfs.TripDescriptor_SCHEDULED
	message := &gtfs.FeedMessage{
		Header: &gtfs.FeedHeader{GtfsRealtimeVersion: proto.String("2.0"), Timestamp: &feedTimestamp},
		Entity: []*gtfs.FeedEntity{{
			Id: proto.String("entity-update"),
			TripUpdate: &gtfs.TripUpdate{
				Trip:           &gtfs.TripDescriptor{TripId: &tripID, RouteId: &routeID, StartDate: &startDate, ScheduleRelationship: &tripRelationship},
				Timestamp:      &updateTimestamp,
				StopTimeUpdate: []*gtfs.TripUpdate_StopTimeUpdate{{StopSequence: &sequence, StopId: &stopID, Arrival: &gtfs.TripUpdate_StopTimeEvent{Time: &arrivalTime, Delay: &delay}, ScheduleRelationship: &relationship}},
			},
		}, {
			Id: proto.String("entity-alert"),
			Alert: &gtfs.Alert{
				Cause: gtfs.Alert_WEATHER.Enum(), Effect: gtfs.Alert_REDUCED_SERVICE.Enum(),
				ActivePeriod:   []*gtfs.TimeRange{{Start: proto.Uint64(uint64(now.Add(-time.Minute).Unix())), End: proto.Uint64(uint64(now.Add(time.Hour).Unix()))}},
				InformedEntity: []*gtfs.EntitySelector{{RouteId: &routeID, StopId: &stopID}},
				HeaderText:     &gtfs.TranslatedString{Translation: []*gtfs.TranslatedString_Translation{{Language: proto.String("fr"), Text: proto.String("Meteo")}, {Language: proto.String("en"), Text: proto.String("Weather")}}},
			},
		}},
	}
	snapshot := NormalizeSnapshot(testFeed(), message, now)
	update, ok := snapshot.FindTripUpdate(StaticTripID(tripID), startDate)
	if !ok || update.RouteID != StaticRouteID(routeID) || update.Freshness.Overall != FreshnessCurrent {
		t.Fatalf("trip update = %+v, %t", update, ok)
	}
	if update.StopTimeUpdates[0].StopSequence == nil || *update.StopTimeUpdates[0].StopSequence != 3 || update.StopTimeUpdates[0].ScheduleRelationship != "SKIPPED" || update.StopTimeUpdates[0].ArrivalDelay == nil || *update.StopTimeUpdates[0].ArrivalDelay != 60 {
		t.Fatalf("stop update = %+v", update.StopTimeUpdates[0])
	}
	if len(snapshot.Alerts) != 1 || snapshot.Alerts[0].Cause != "WEATHER" || snapshot.Alerts[0].Effect != "REDUCED_SERVICE" || snapshot.Alerts[0].HeaderText[0].Text != "Weather" {
		t.Fatalf("alerts = %+v", snapshot.Alerts)
	}
	if len(snapshot.AlertsForRoute(routeID)) != 1 || len(snapshot.AlertsForStop(stopID)) != 1 {
		t.Fatalf("alert indexes = %+v", snapshot.Alerts)
	}
}

func TestNormalizeAlertWithoutActivePeriodIsAlwaysActive(t *testing.T) {
	now := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
	feedTimestamp := uint64(now.Unix())
	message := &gtfs.FeedMessage{
		Header: &gtfs.FeedHeader{GtfsRealtimeVersion: proto.String("2.0"), Timestamp: &feedTimestamp},
		Entity: []*gtfs.FeedEntity{{
			Id:    proto.String("always-active"),
			Alert: &gtfs.Alert{Effect: gtfs.Alert_NO_SERVICE.Enum()},
		}},
	}
	snapshot := NormalizeSnapshot(testFeed(), message, now)
	if len(snapshot.Alerts) != 1 || snapshot.Alerts[0].Freshness.Entity.State != FreshnessCurrent || snapshot.Alerts[0].Freshness.Overall != FreshnessCurrent {
		t.Fatalf("alert freshness = %+v, want current entity and overall", snapshot.Alerts)
	}
}
