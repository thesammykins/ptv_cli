package cmd

import (
	"reflect"
	"strings"
	"testing"

	"github.com/thesammykins/ptv_cli/internal/ptvapi"
)

func TestLimitDeparturesUsesGlobalLimit(t *testing.T) {
	flagLimit = 2
	t.Cleanup(func() { flagLimit = 0 })

	deps := []ptvapi.Departure{{RunID: 1}, {RunID: 2}, {RunID: 3}}
	got := limitDepartures(deps)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[1].RunID != 2 {
		t.Fatalf("second run = %d, want 2", got[1].RunID)
	}
}

func TestLimitDisruptionMapCapsDeterministically(t *testing.T) {
	flagLimit = 2
	t.Cleanup(func() { flagLimit = 0 })

	got := limitDisruptionMap(map[string][]ptvapi.Disruption{
		"metro_tram":  {{DisruptionID: 3}},
		"metro_train": {{DisruptionID: 1}, {DisruptionID: 2}},
	})
	if len(got["metro_train"]) != 2 {
		t.Fatalf("metro_train len = %d, want 2", len(got["metro_train"]))
	}
	if len(got["metro_tram"]) != 0 {
		t.Fatalf("metro_tram len = %d, want 0 after global cap", len(got["metro_tram"]))
	}
}

func TestSortStopsBySequenceMovesZeroSequenceLast(t *testing.T) {
	stops := []ptvapi.StopModel{
		{StopName: "zero", StopSequence: 0},
		{StopName: "three", StopSequence: 3},
		{StopName: "one", StopSequence: 1},
	}
	sortStopsBySequence(stops)
	if stops[0].StopName != "one" || stops[1].StopName != "three" || stops[2].StopName != "zero" {
		t.Fatalf("unexpected order: %#v", stops)
	}
}

func TestChooseStopPrefersImplicitStationName(t *testing.T) {
	stops := []ptvapi.StopModel{
		{StopID: 1227, StopName: "Hawkstowe Station", StopSuburb: "South Morang"},
		{StopID: 1224, StopName: "South Morang Station", StopSuburb: "South Morang"},
	}

	got := chooseStop("South Morang", stops)
	if got.StopID != 1224 {
		t.Fatalf("stop id = %d, want 1224", got.StopID)
	}
}

func TestChooseStopPrefersExactName(t *testing.T) {
	stops := []ptvapi.StopModel{
		{StopID: 1, StopName: "South Morang Station"},
		{StopID: 2, StopName: "South Morang"},
	}

	got := chooseStop("South Morang", stops)
	if got.StopID != 2 {
		t.Fatalf("stop id = %d, want 2", got.StopID)
	}
}

func TestStopsNearDistanceDefaultsToShortWalk(t *testing.T) {
	stopsMaxDistance = 0
	t.Cleanup(func() { stopsMaxDistance = 0 })

	if got := stopsNearDistance(); got != 1000 {
		t.Fatalf("stopsNearDistance() = %.0f, want 1000", got)
	}
}

func TestStopsNearDistanceUsesFlagValue(t *testing.T) {
	stopsMaxDistance = 500
	t.Cleanup(func() { stopsMaxDistance = 0 })

	if got := stopsNearDistance(); got != 500 {
		t.Fatalf("stopsNearDistance() = %.0f, want 500", got)
	}
}

func TestFareRejectsReversedZones(t *testing.T) {
	stdout, stderr, err := executeCommand(t, "fare", "--min-zone", "2", "--max-zone", "1")
	if err == nil {
		t.Fatal("expected reversed zone error")
	}
	if stdout != "" || stderr != "" {
		t.Fatalf("stdout=%q stderr=%q, want no direct output from Execute", stdout, stderr)
	}
}

func TestFareEstimateAllZeroDetectsUnavailableAPIData(t *testing.T) {
	resp := &ptvapi.FareEstimateResponse{}
	resp.FareEstimateResult.ZoneInfo.MinZone = 1
	resp.FareEstimateResult.ZoneInfo.MaxZone = 2
	resp.FareEstimateResult.PassengerFares = append(resp.FareEstimateResult.PassengerFares, struct {
		PassengerType    string  `json:"PassengerType"`
		Fare2HourPeak    float64 `json:"Fare2HourPeak"`
		Fare2HourOffPeak float64 `json:"Fare2HourOffPeak"`
		FareDailyPeak    float64 `json:"FareDailyPeak"`
		FareDailyOffPeak float64 `json:"FareDailyOffPeak"`
	}{PassengerType: "fullFare"})

	if !fareEstimateAllZero(resp) {
		t.Fatal("expected all-zero fare estimate to be treated as unavailable")
	}
	resp.FareEstimateResult.PassengerFares[0].Fare2HourPeak = 5.50
	if fareEstimateAllZero(resp) {
		t.Fatal("non-zero fare estimate should not be treated as unavailable")
	}
}

func TestCleanJSONStringsTrimsNestedAPIStrings(t *testing.T) {
	resp := &ptvapi.SearchResult{
		Stops:  []ptvapi.StopModel{{StopName: "Flinders Street ", StopSuburb: " Melbourne "}},
		Routes: []ptvapi.Route{{RouteName: "North Coburg - Flinders Street Station ", RouteNumber: "19 "}},
	}
	cleanJSONStrings(reflect.ValueOf(resp))
	if resp.Stops[0].StopName != "Flinders Street" || resp.Stops[0].StopSuburb != "Melbourne" {
		t.Fatalf("stop strings were not trimmed: %+v", resp.Stops[0])
	}
	if resp.Routes[0].RouteNumber != "19" || strings.HasSuffix(resp.Routes[0].RouteName, " ") {
		t.Fatalf("route strings were not trimmed: %+v", resp.Routes[0])
	}
}

func TestLimitSearchResultUsesGlobalCap(t *testing.T) {
	flagLimit = 2
	t.Cleanup(func() { flagLimit = 0 })

	resp := &ptvapi.SearchResult{
		Stops:   []ptvapi.StopModel{{StopID: 1}, {StopID: 2}},
		Routes:  []ptvapi.Route{{RouteID: 3}, {RouteID: 4}},
		Outlets: []ptvapi.ResultOutlet{{OutletSlidID: "5"}},
	}
	limitSearchResult(resp)
	if len(resp.Stops) != 2 || len(resp.Routes) != 0 || len(resp.Outlets) != 0 {
		t.Fatalf("unexpected global limit result: stops=%d routes=%d outlets=%d", len(resp.Stops), len(resp.Routes), len(resp.Outlets))
	}
	if resp.Routes == nil || resp.Outlets == nil {
		t.Fatal("capped categories should encode as empty JSON arrays, not null")
	}
}

func TestPlanAcceptsSeparatorBeforeNegativeCoordinate(t *testing.T) {
	err := planCmd.Args(planCmd, []string{"-37.8183,144.9671", "Camberwell"})
	if err != nil {
		t.Fatalf("plan Args rejected separated negative coordinate: %v", err)
	}
	if err := planCmd.Args(planCmd, []string{"--", "-37.8183,144.9671", "Camberwell"}); err != nil {
		t.Fatalf("plan Args rejected preserved -- separator: %v", err)
	}
}

func TestPlanSeparatorIsNotAPlanArgument(t *testing.T) {
	stdout, stderr, err := executeCommand(t, "plan", "--arrive-by", "09:00", "--", "-37.8183,144.9671", "Camberwell", "extra")
	if err == nil {
		t.Fatal("expected too many args error")
	}
	if !strings.Contains(err.Error(), "accepts 2 arg(s), received 3") {
		t.Fatalf("unexpected plan args error: %v", err)
	}
	if stdout != "" || stderr != "" {
		t.Fatalf("stdout=%q stderr=%q, want no direct output from Execute", stdout, stderr)
	}
}
