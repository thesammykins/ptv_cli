package cmd

import (
	"testing"
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

func stringPtr(value string) *string { return &value }
