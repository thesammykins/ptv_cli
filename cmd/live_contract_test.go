package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/thesammykins/ptv_cli/internal/ptvapi"
)

func TestNextOutputGolden(t *testing.T) {
	scheduled := "2026-07-16T00:00:00Z"
	estimated := "2026-07-16T00:02:00Z"
	platform := " 1\n"
	note := " Board here\t"
	toDate := "2026-07-16T04:00:00Z"
	response := &ptvapi.DeparturesResponse{
		Departures: []ptvapi.Departure{{
			StopID: 101, RouteID: 7, RunID: 22, RunRef: "run-A", DirectionID: 3,
			DisruptionIDs: []int64{55}, ScheduledDepartureUTC: &scheduled,
			EstimatedDepartureUTC: &estimated, AtPlatform: true, PlatformNumber: &platform,
			Flags: " RR\t", DepartureSequence: 10, DepartureNote: &note,
		}},
		Stops: map[string]ptvapi.StopModel{
			"101": {StopID: 101, StopName: " Central Station\n", StopSuburb: " Melbourne ", RouteType: 0, StopLatitude: -37.81, StopLongitude: 144.96},
		},
		Routes: map[string]ptvapi.Route{
			"7": {RouteType: 0, RouteID: 7, RouteName: " City Loop ", RouteNumber: " 1 ", RouteGTFSID: " metro:1 "},
		},
		Runs: map[string]ptvapi.Run{
			"run-A": {
				RunID: 22, RunRef: " run-A ", RouteID: 7, RouteType: 0, FinalStopID: 999,
				DestinationName: " Flinders Street ", Status: " Active ", DirectionID: 3,
				VehicleDescriptor: &ptvapi.VehicleDescriptor{ID: "INTERNAL-VEHICLE-ID"},
			},
		},
		Directions: map[string]ptvapi.Direction{
			"3": {DirectionID: 3, DirectionName: " City ", RouteDirectionDescription: " via Loop ", RouteID: 7, RouteType: 0},
		},
		Disruptions: map[string]ptvapi.Disruption{
			"55": {
				DisruptionID: 55, Title: " Works\n", Description: " Replacement buses ",
				DisruptionStatus: " Current ", DisruptionType: " Planned ",
				PublishedOn: "2026-07-15T23:00:00Z", LastUpdated: "2026-07-15T23:30:00Z",
				FromDate: "2026-07-16T00:00:00Z", ToDate: &toDate,
			},
		},
		Status: ptvapi.Status{Version: " 3.0 ", Health: 1},
	}

	encoded := assertLiveContractGolden(t, "next.json.golden", newNextOutput(response))
	assertContractOmits(t, encoded, "INTERNAL-VEHICLE-ID", `"vehicle_descriptor"`, `"signature"`, `"devid"`)
	for _, required := range []string{`"ptv_run_ref": "run-A"`, `"scheduled_departure_utc": "2026-07-16T00:00:00Z"`, `"scheduled_departure": "2026-07-16T10:00:00+10:00"`, `"time_zone": "Australia/Melbourne"`} {
		if !strings.Contains(encoded, required) {
			t.Fatalf("next output missing %s:\n%s", required, encoded)
		}
	}
}

func TestNextOutputTimesIgnoreHostTimezone(t *testing.T) {
	previous := time.Local
	time.Local = time.FixedZone("host-zone", -7*60*60)
	t.Cleanup(func() { time.Local = previous })

	scheduled := "2026-12-15T00:00:00+00:00"
	output := newNextOutput(&ptvapi.DeparturesResponse{
		Departures: []ptvapi.Departure{{ScheduledDepartureUTC: &scheduled}},
	})
	departure := output.Departures[0]
	if departure.ScheduledDepartureUTC == nil || *departure.ScheduledDepartureUTC != "2026-12-15T00:00:00Z" {
		t.Fatalf("UTC compatibility time = %v", departure.ScheduledDepartureUTC)
	}
	if departure.ScheduledDeparture == nil || *departure.ScheduledDeparture != "2026-12-15T11:00:00+11:00" {
		t.Fatalf("Melbourne time = %v", departure.ScheduledDeparture)
	}
	if output.TimeZone != commandTimeZone {
		t.Fatalf("time zone = %q", output.TimeZone)
	}
}

func TestSearchOutputGolden(t *testing.T) {
	response := &ptvapi.SearchResult{
		Stops: []ptvapi.StopModel{{
			StopID: 10, StopName: " Richmond Station\n", StopSuburb: " Richmond ", RouteType: 0,
			StopLatitude: -37.824, StopLongitude: 144.998,
		}},
		Routes: []ptvapi.Route{{
			RouteType: 0, RouteID: 1, RouteName: " Alamein ", RouteNumber: " ALM ", RouteGTFSID: " metro:ALM ",
		}},
		Outlets: []ptvapi.ResultOutlet{{
			OutletSlidID: "INTERNAL-OUTLET-ID", OutletName: " Shop\t", OutletBusiness: " Newsagent ",
			OutletLatitude: -37.82, OutletLongitude: 145, OutletSuburb: " Richmond ", OutletDistance: 120,
		}},
		Status: ptvapi.Status{Version: " 3.0 ", Health: 1},
	}
	encoded := assertLiveContractGolden(t, "search.json.golden", newSearchOutput(response))
	assertContractOmits(t, encoded, "INTERNAL-OUTLET-ID", "outlet_slid_spid", `"signature"`, `"devid"`)
}

func TestLinesShowOutputGolden(t *testing.T) {
	output := newLinesShowOutput(
		ptvapi.Route{RouteType: 1, RouteID: 722, RouteName: " Box Hill - Port Melbourne ", RouteNumber: " 109 ", RouteGTFSID: " tram:109 "},
		[]ptvapi.Direction{{DirectionID: 11, DirectionName: " Port Melbourne ", RouteDirectionDescription: " outbound ", RouteID: 722, RouteType: 1}},
		map[string][]ptvapi.StopModel{
			"11": {{StopID: 1234, StopName: " Collins St\n", StopSuburb: " Melbourne ", RouteType: 1, StopSequence: 5}},
		},
	)
	assertLiveContractGolden(t, "lines-show.json.golden", output)
}

func TestOfficialDirectionDescriptionSurvivesDecodeAndMapping(t *testing.T) {
	var response ptvapi.DirectionsResponse
	if err := json.Unmarshal([]byte(`{
  "directions": [{
    "direction_id": 11,
    "direction_name": "Port Melbourne",
    "route_direction_description": "via Collins Street",
    "route_id": 722,
    "route_type": 1
  }],
  "status": {"version": "3.0", "health": 1}
}`), &response); err != nil {
		t.Fatal(err)
	}
	mapped := newLineDirectionOutput(response.Directions[0])
	if mapped.RouteDirectionDescription != "via Collins Street" {
		t.Fatalf("route direction description = %q", mapped.RouteDirectionDescription)
	}
}

func TestDisruptionsOutputGolden(t *testing.T) {
	toDate := "2026-07-17T00:00:00Z"
	response := &ptvapi.DisruptionsResponse{
		Disruptions: map[string][]ptvapi.Disruption{
			"metro_train": {{
				DisruptionID: 91, Title: " Track works ", URL: " https://user:password@example.test/works?devid=123&signature=SECRET&sig=SHORT&token=PRIVATE&code=CODE&subscription-key=SUBSCRIPTION&X-Amz-Signature=AWS#internal ",
				Description: " Buses replace trains\n", DisruptionStatus: " Current ", DisruptionType: " Planned ",
				PublishedOn: "2026-07-15T22:00:00Z", LastUpdated: "2026-07-15T23:00:00Z",
				FromDate: "2026-07-16T00:00:00Z", ToDate: &toDate,
				Routes: []ptvapi.DisruptionRoute{{
					RouteType: 0, RouteID: 1, RouteName: " Alamein ", RouteNumber: " ALM ",
					Direction: &ptvapi.DisruptionDirection{DirectionID: 2, DirectionName: " City "},
				}},
				Stops: []ptvapi.DisruptionStop{{RouteType: 0, StopID: 10, StopName: " Richmond "}},
			}},
		},
		Status: ptvapi.Status{Version: " 3.0 ", Health: 1},
	}
	encoded := assertLiveContractGolden(t, "disruptions.json.golden", newDisruptionsOutput(response))
	assertContractOmits(t, encoded, "SECRET", "SHORT", "PRIVATE", "CODE", "SUBSCRIPTION", "AWS", "password", "internal", "?", "#")
	for _, required := range []string{`"ptv_disruption_id": 91`, `"from_date": "2026-07-16T10:00:00+10:00"`, `"time_zone": "Australia/Melbourne"`} {
		if !strings.Contains(encoded, required) {
			t.Fatalf("disruptions output missing %s:\n%s", required, encoded)
		}
	}
}

func TestFareOutletAndModeMappers(t *testing.T) {
	fare := &ptvapi.FareEstimateResponse{}
	fare.FareEstimateResultStatus.StatusCode = 0
	fare.FareEstimateResultStatus.Message = " Success\n"
	fare.FareEstimateResult.ZoneInfo.MinZone = 1
	fare.FareEstimateResult.ZoneInfo.MaxZone = 2
	fare.FareEstimateResult.ZoneInfo.UniqueZones = []int{1, 2}
	fare.FareEstimateResult.PassengerFares = append(fare.FareEstimateResult.PassengerFares, struct {
		PassengerType    string  `json:"PassengerType"`
		Fare2HourPeak    float64 `json:"Fare2HourPeak"`
		Fare2HourOffPeak float64 `json:"Fare2HourOffPeak"`
		FareDailyPeak    float64 `json:"FareDailyPeak"`
		FareDailyOffPeak float64 `json:"FareDailyOffPeak"`
	}{PassengerType: " fullFare ", Fare2HourPeak: 5.5, FareDailyPeak: 11})
	fareOutput := newFareOutput(fare)
	if fareOutput.FareEstimateResultStatus.Message != "Success" || fareOutput.FareEstimateResult.PassengerFares[0].PassengerType != "fullFare" {
		t.Fatalf("fare mapper did not normalize presentation text: %+v", fareOutput)
	}
	fareJSON, err := json.Marshal(fareOutput)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(fareJSON), `"status"`) {
		t.Fatalf("fare output invented a generic status field absent from the endpoint contract: %s", fareJSON)
	}

	outlets := newOutletsOutput([]ptvapi.ResultOutlet{{
		OutletSlidID: "PRIVATE", OutletName: " Store ", OutletBusiness: " Business\n", OutletSuburb: " CBD ",
	}}, ptvapi.Status{Version: " 3.0 ", Health: 1})
	outletJSON, err := json.Marshal(outlets)
	if err != nil {
		t.Fatal(err)
	}
	if outlets.Outlets[0].OutletName != "Store" || strings.Contains(string(outletJSON), "PRIVATE") || strings.Contains(string(outletJSON), "outlet_slid_spid") {
		t.Fatalf("outlet mapper leaked or failed to normalize: %s", outletJSON)
	}

	mode := newModeShowOutput(
		ptvapi.Route{RouteType: 2, RouteID: 42, RouteName: " Bus route ", RouteNumber: " 900 "},
		[]ptvapi.Direction{{DirectionID: 8, DirectionName: " Rowville ", RouteDirectionDescription: " via Chadstone ", RouteID: 42, RouteType: 2}},
		map[string][]ptvapi.StopModel{"8": {{StopID: 4, StopName: " Chadstone ", RouteType: 2, StopSequence: 1}}},
		[]ptvapi.Disruption{{DisruptionID: 5, Title: " Detour "}},
	)
	if mode.Route.PTVRouteID != 42 || mode.Directions[0].PTVDirectionID != 8 || mode.Stops["8"][0].PTVStopID != 4 || mode.Disruptions[0].Title != "Detour" {
		t.Fatalf("mode mapper lost namespace or normalized fields: %+v", mode)
	}
}

func TestSearchCommandJSONIsOneDocumentAndLimitsAfterSorting(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if !strings.HasPrefix(r.URL.Path, "/v3/search/") {
			t.Errorf("request path = %q", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
  "stops": [
    {"stop_id": 3, "stop_name": "Zulu", "stop_suburb": "C", "route_type": 0},
    {"stop_id": 1, "stop_name": "Alpha", "stop_suburb": "A", "route_type": 0},
    {"stop_id": 2, "stop_name": "Bravo", "stop_suburb": "B", "route_type": 0}
  ],
  "routes": [{"route_id": 8, "route_name": "Hidden by global cap", "route_type": 0}],
  "outlets": [],
  "status": {"version": "3.0", "health": 1}
}`)
	}))
	defer server.Close()

	t.Setenv("PTV_API_KEY", "contract-test-key")
	t.Setenv("PTV_API_USERID", "123")
	t.Setenv("PTV_BASE_URL", server.URL)
	searchModes = nil
	rootCmd.SetContext(context.Background())
	stdout, stderr, err := executeLiveContractCommand(t, "--json", "--limit", "2", "search", "term")
	if err != nil {
		t.Fatalf("search command: %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if !json.Valid([]byte(stdout)) {
		t.Fatalf("stdout is not one JSON document:\n%s", stdout)
	}
	var output searchOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatal(err)
	}
	if len(output.Stops) != 2 || output.Stops[0].StopName != "Alpha" || output.Stops[1].StopName != "Bravo" {
		t.Fatalf("limited stops = %+v", output.Stops)
	}
	if len(output.Routes) != 0 || output.Routes == nil || output.Outlets == nil {
		t.Fatalf("global cap must retain empty arrays: routes=%#v outlets=%#v", output.Routes, output.Outlets)
	}
}

func TestSearchCommandPropagatesCommandContext(t *testing.T) {
	t.Setenv("PTV_API_KEY", "contract-test-key")
	t.Setenv("PTV_API_USERID", "123")
	t.Setenv("PTV_BASE_URL", "http://127.0.0.1:1")
	previousModes, previousEnv := append([]string(nil), searchModes...), flagEnv
	t.Cleanup(func() {
		searchCmd.SetContext(context.Background())
		searchModes, flagEnv = previousModes, previousEnv
	})

	requestContext, cancel := context.WithCancel(context.Background())
	cancel()
	searchCmd.SetContext(requestContext)
	searchModes, flagEnv = nil, ""
	err := searchCmd.RunE(searchCmd, []string{"term"})
	if !errors.Is(err, context.Canceled) || !ptvapi.IsKind(err, ptvapi.ErrorCanceled) {
		t.Fatalf("search error = %v, want typed command cancellation", err)
	}
}

func TestLinesShowParsesInheritedModeFlag(t *testing.T) {
	previousRunE := linesShowCmd.RunE
	previousModes := append([]string(nil), linesModes...)
	t.Cleanup(func() {
		linesShowCmd.RunE = previousRunE
		linesModes = previousModes
	})

	var captured []string
	linesShowCmd.RunE = func(cmd *cobra.Command, args []string) error {
		captured = append([]string(nil), linesModes...)
		return nil
	}
	linesModes = nil
	stdout, stderr, err := executeLiveContractCommand(t, "lines", "show", "--mode", "train", "Alamein")
	if err != nil {
		t.Fatalf("lines show parse: %v", err)
	}
	if stdout != "" || stderr != "" {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
	if len(captured) != 1 || captured[0] != "train" {
		t.Fatalf("inherited modes = %v, want [train]", captured)
	}
}

func TestDeterministicSortsPrecedeLimits(t *testing.T) {
	flagLimit = 1
	t.Cleanup(func() { flagLimit = 0 })

	departures := []ptvapi.Departure{{RouteID: 9, RunRef: "z"}, {RouteID: 1, RunRef: "a"}}
	sortDepartures(departures)
	if got := limitDepartures(departures); len(got) != 1 || got[0].RouteID != 1 {
		t.Fatalf("departure limit was nondeterministic: %+v", got)
	}

	disruptions := map[string][]ptvapi.Disruption{
		"metro_train": {{DisruptionID: 2, Title: "Zulu"}, {DisruptionID: 1, Title: "Alpha"}},
	}
	sortDisruptionMap(disruptions)
	limited := limitDisruptionMap(disruptions)
	if len(limited["metro_train"]) != 1 || limited["metro_train"][0].DisruptionID != 1 {
		t.Fatalf("disruption limit was nondeterministic: %+v", limited)
	}
}

func assertLiveContractGolden(t *testing.T, name string, value any) string {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", name, err)
	}
	data = append(data, '\n')
	if !json.Valid(data) {
		t.Fatalf("%s is not one JSON document:\n%s", name, data)
	}
	want, err := os.ReadFile("testdata/contracts/" + name)
	if err != nil {
		t.Fatalf("read %s: %v\ngot:\n%s", name, err, data)
	}
	if string(data) != string(want) {
		t.Fatalf("%s mismatch\n--- want ---\n%s\n--- got ---\n%s", name, want, data)
	}
	return string(data)
}

func assertContractOmits(t *testing.T, encoded string, forbidden ...string) {
	t.Helper()
	for _, value := range forbidden {
		if strings.Contains(encoded, value) {
			t.Fatalf("contract output contains forbidden value %q:\n%s", value, encoded)
		}
	}
}

func executeLiveContractCommand(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	os.Stdout, os.Stderr = outW, errW

	rootCmd.SetArgs(args)
	rootCmd.SetOut(outW)
	rootCmd.SetErr(errW)
	rootCmd.SetContext(context.Background())
	flagJSON, flagLimit, flagEnv = false, 0, ""
	execErr := rootCmd.Execute()

	_ = outW.Close()
	_ = errW.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	var stdout, stderr bytes.Buffer
	_, _ = io.Copy(&stdout, outR)
	_, _ = io.Copy(&stderr, errR)
	_ = outR.Close()
	_ = errR.Close()
	return stdout.String(), stderr.String(), execErr
}
