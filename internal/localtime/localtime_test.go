package localtime

import (
	"testing"
	"time"
)

func TestServiceDayAnchorAcrossDSTStart(t *testing.T) {
	day := time.Date(2026, time.October, 4, 12, 0, 0, 0, Melbourne())
	anchor := ServiceDayAnchor(day)
	if got := anchor.Format(time.RFC3339); got != "2026-10-03T23:00:00+10:00" {
		t.Fatalf("anchor = %s", got)
	}
	if got := anchor.Add(3 * time.Hour).Format(time.RFC3339); got != "2026-10-04T03:00:00+11:00" {
		t.Fatalf("03:00 GTFS instant = %s", got)
	}
}

func TestServiceDayAnchorAcrossDSTEnd(t *testing.T) {
	day := time.Date(2026, time.April, 5, 12, 0, 0, 0, Melbourne())
	anchor := ServiceDayAnchor(day)
	if got := anchor.Format(time.RFC3339); got != "2026-04-05T01:00:00+11:00" {
		t.Fatalf("anchor = %s", got)
	}
	if got := anchor.Add(3 * time.Hour).Format(time.RFC3339); got != "2026-04-05T03:00:00+10:00" {
		t.Fatalf("03:00 GTFS instant = %s", got)
	}
}

func TestServiceDaySupportsTimesBeyond24Hours(t *testing.T) {
	day := time.Date(2026, time.July, 16, 12, 0, 0, 0, Melbourne())
	got := ServiceDayAnchor(day).Add(25*time.Hour + 30*time.Minute)
	if formatted := got.Format(time.RFC3339); formatted != "2026-07-17T01:30:00+10:00" {
		t.Fatalf("25:30 = %s", formatted)
	}
}

func TestInMelbourneIgnoresHostLocation(t *testing.T) {
	instant := time.Date(2026, time.July, 16, 0, 0, 0, 0, time.UTC)
	if got := InMelbourne(instant).Format(time.RFC3339); got != "2026-07-16T10:00:00+10:00" {
		t.Fatalf("InMelbourne = %s", got)
	}
}
