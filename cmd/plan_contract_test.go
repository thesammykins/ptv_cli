package cmd

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/thesammykins/ptv_cli/internal/model"
)

func TestResolvePlanEndpointsWeightsCoordinatesButNotStopNames(t *testing.T) {
	tt := &model.Timetable{
		Stops: []model.Stop{
			{Index: 0, ID: "2:A", Name: "Alpha", Lat: -37.8100, Lon: 144.9600},
			{Index: 1, ID: "2:B", Name: "Beta", Lat: -37.8110, Lon: 144.9600},
		},
		NameIndex: map[string][]int{"alpha": {0}},
	}

	local, err := resolvePlanEndpoints(context.Background(), tt, "Alpha", 1000, nil)
	if err != nil {
		t.Fatalf("local stop resolution: %v", err)
	}
	if len(local.Endpoints) != 1 || local.Endpoints[0].WalkSeconds != 0 || local.Endpoints[0].Location != nil {
		t.Fatalf("local endpoints = %#v, want zero-cost stop", local.Endpoints)
	}

	coordinate, err := resolvePlanEndpoints(context.Background(), tt, "-37.8105,144.9600", 1000, nil)
	if err != nil {
		t.Fatalf("coordinate resolution: %v", err)
	}
	if len(coordinate.Endpoints) != 2 {
		t.Fatalf("coordinate endpoints = %d, want 2", len(coordinate.Endpoints))
	}
	for _, endpoint := range coordinate.Endpoints {
		if endpoint.WalkSeconds <= 0 || endpoint.Location == nil {
			t.Fatalf("weighted endpoint = %#v", endpoint)
		}
	}
}

func TestResolvePlanEndpointsSortsAndDeduplicatesAmbiguousLocalMatches(t *testing.T) {
	stops := []model.Stop{
		{Index: 0, Name: "Central Alpha", Mode: 2},
		{Index: 1, Name: "Central Beta", Mode: 2},
		{Index: 2, Name: "Central Gamma", Mode: 2},
	}
	indexes := []map[string][]int{
		{"central beta": {2, 1}, "central alpha": {1, 0, 1}},
		{"central alpha": {1, 0, 1}, "central beta": {2, 1}},
	}
	for i, nameIndex := range indexes {
		resolution, err := resolvePlanEndpoints(context.Background(), &model.Timetable{
			Stops: stops, NameIndex: nameIndex,
		}, "central", 1000, nil)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if len(resolution.Endpoints) != 3 {
			t.Fatalf("case %d endpoint count = %d, want 3", i, len(resolution.Endpoints))
		}
		if got := []int{
			resolution.Endpoints[0].Stop,
			resolution.Endpoints[1].Stop,
			resolution.Endpoints[2].Stop,
		}; got[0] != 0 || got[1] != 1 || got[2] != 2 {
			t.Fatalf("case %d endpoints = %v, want [0 1 2]", i, got)
		}
	}
}

func TestPlanOutputPreservesJourneyFieldsAndAddsEvidence(t *testing.T) {
	when := time.Date(2026, 7, 16, 9, 0, 0, 0, melbourneLocation())
	encoded, err := json.Marshal(newPlanOutput(
		&model.Journey{Legs: []model.Leg{}, DepTime: when, ArrTime: when},
		[]string{"© OpenStreetMap contributors"},
		[]string{"conditional service"},
	))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"legs", "depart", "arrive", "transfers", "time_zone", "attribution", "warnings"} {
		if _, ok := got[field]; !ok {
			t.Fatalf("missing %q in %s", field, encoded)
		}
	}
}

func TestPlanOutputUsesMelbourneTimesAndExplicitGTFSNamespaces(t *testing.T) {
	utc := time.Date(2026, 7, 15, 23, 0, 0, 0, time.UTC)
	journey := &model.Journey{
		DepTime: utc,
		ArrTime: utc.Add(10 * time.Minute),
		Legs: []model.Leg{{
			FromStop:  model.Stop{Index: 1, ID: "2:from", Name: "From", Mode: 2},
			ToStop:    model.Stop{Index: 2, ID: "2:to", Name: "To", Mode: 2},
			DepTime:   utc,
			ArrTime:   utc.Add(10 * time.Minute),
			RouteType: 2,
			TripID:    "2:trip",
			BlockID:   "2:block",
		}},
	}
	output := newPlanOutput(journey, nil, nil)
	if output.Depart != "2026-07-16T09:00:00+10:00" || output.TimeZone != "Australia/Melbourne" {
		t.Fatalf("time contract = %q (%s)", output.Depart, output.TimeZone)
	}
	leg := output.Legs[0]
	if leg.GTFSTripID != "2:trip" || leg.GTFSFeedMode != 2 || leg.From.GTFSStopID != "2:from" || leg.GTFSBlockID != "2:block" {
		t.Fatalf("GTFS namespace fields = %+v", leg)
	}
}

func TestPlanDisruptionOutputSanitizesRawURLs(t *testing.T) {
	when := time.Date(2026, 7, 16, 9, 0, 0, 0, melbourneLocation())
	output := newPlanOutput(&model.Journey{
		DepTime: when,
		ArrTime: when,
		Disruptions: []model.DisruptionNote{{
			ID:    91,
			Title: " Track works ",
			URL:   "https://user:password@example.test/works?devid=123&signature=SECRET&sig=SHORT&token=PRIVATE&code=CODE&subscription-key=SUBSCRIPTION&X-Amz-Signature=AWS#internal",
		}},
	}, nil, nil)
	if len(output.Disruptions) != 1 || output.Disruptions[0].URL != "https://example.test/works" {
		t.Fatalf("plan disruption URL = %#v", output.Disruptions)
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"SECRET", "SHORT", "PRIVATE", "CODE", "SUBSCRIPTION", "AWS", "password", "internal", "?", "#"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("plan disruption output leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestNormalizedPublicURLRejectsUnsafeOrNonPublicURLs(t *testing.T) {
	tests := map[string]string{
		"relative/path?signature=SECRET": "",
		"javascript:alert(1)":            "",
		"ftp://example.test/file":        "",
		"https:///missing-host":          "",
		"HTTPS://Example.Test/path?q=1":  "https://Example.Test/path",
	}
	for input, want := range tests {
		if got := normalizedPublicURL(input); got != want {
			t.Errorf("normalizedPublicURL(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestUniqueNonEmptyAttribution(t *testing.T) {
	got := uniqueNonEmpty("", "OSM", " OSM ", "Provider B")
	if len(got) != 2 || got[0] != "OSM" || got[1] != "Provider B" {
		t.Fatalf("uniqueNonEmpty = %#v", got)
	}
}
