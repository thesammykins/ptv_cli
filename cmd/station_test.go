package cmd

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/thesammykins/ptv_cli/internal/ptvapi"
)

func TestStationJSONGoldenMapsOfficialStopDetails(t *testing.T) {
	fixture := readStationFixture(t)
	var response ptvapi.StopResponse
	if err := json.Unmarshal(fixture, &response); err != nil {
		t.Fatalf("decode station fixture: %v", err)
	}
	encoded, err := json.MarshalIndent(newStationOutput(&response, &ptvapi.StopModel{StopID: 1071, RouteType: 0}), "", "  ")
	if err != nil {
		t.Fatalf("marshal station output: %v", err)
	}
	stdout := string(append(encoded, '\n'))
	if !json.Valid([]byte(stdout)) {
		t.Fatalf("station output is not valid JSON:\n%s", stdout)
	}
	for _, falseField := range []string{`"stop_type"`, `"manouvering"`, `"raised_platform_shelther"`, `"wed_pm_To"`} {
		if strings.Contains(stdout, falseField) {
			t.Fatalf("stdout contains upstream compatibility/misspelling %s:\n%s", falseField, stdout)
		}
	}
	for _, correctedField := range []string{`"manoeuvring"`, `"raised_platform_shelter"`, `"wednesday_pm_to"`} {
		if !strings.Contains(stdout, correctedField) {
			t.Fatalf("stdout missing normalized field %s:\n%s", correctedField, stdout)
		}
	}
	for _, required := range []string{
		`"url": "https://www.ptv.vic.gov.au/disruptions/101"`,
		`"published_on": "2026-07-15T10:00:00+10:00"`,
		`"from_date": "2026-07-16T10:00:00+10:00"`,
		`"time_zone": "Australia/Melbourne"`,
	} {
		if !strings.Contains(stdout, required) {
			t.Fatalf("stdout missing normalized disruption evidence %s:\n%s", required, stdout)
		}
	}
	for _, forbidden := range []string{"SECRET", "SHORT", "PRIVATE", "CODE", "SUBSCRIPTION", "AWS", "password", "devid=", "signature=", "sig=", "token="} {
		if strings.Contains(stdout, forbidden) {
			t.Fatalf("stdout leaked disruption URL value %q:\n%s", forbidden, stdout)
		}
	}
	assertStationGolden(t, "station.json.golden", stdout)
}

func TestStationDisruptionContractKeepsStableEmptyCollections(t *testing.T) {
	encoded, err := json.Marshal(newStationOutput(&ptvapi.StopResponse{
		Disruptions: map[string]ptvapi.Disruption{
			"empty": {DisruptionID: 7, Title: "Empty scope"},
		},
	}, &ptvapi.StopModel{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{`"routes":[]`, `"stops":[]`, `"time_zone":"Australia/Melbourne"`} {
		if !strings.Contains(string(encoded), required) {
			t.Fatalf("station disruption contract missing %s: %s", required, encoded)
		}
	}
}

func TestStationHumanGoldenRendersOfficialFacilities(t *testing.T) {
	fixture := readStationFixture(t)
	var response ptvapi.StopResponse
	if err := json.Unmarshal(fixture, &response); err != nil {
		t.Fatalf("decode station fixture: %v", err)
	}
	stdout := captureStationRender(t, newStationOutput(&response, &ptvapi.StopModel{StopID: 1071, RouteType: 0}))
	assertStationGolden(t, "station.txt.golden", stdout)
}

func TestStationNameResolutionUsesOneSearchRequest(t *testing.T) {
	searchRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/v3/search/"):
			searchRequests++
			if got := r.URL.Query()["route_types"]; len(got) != 1 || got[0] != "0" {
				t.Errorf("search route_types = %v, want [0]", got)
			}
			_, _ = io.WriteString(w, `{
  "stops": [{
    "stop_id": 1071,
    "stop_name": "Flinders Street Railway Station",
    "route_type": 0,
    "stop_latitude": -1,
    "stop_longitude": 1
  }],
  "routes": [],
  "outlets": [],
  "status": {"version": "3.0", "health": 1}
}`)
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := ptvapi.New(server.URL, "station-test-key", "123")
	stop, err := resolveStationStopContext(context.Background(), client, "Flinders Street", nil)
	if err != nil {
		t.Fatalf("resolve station by name: %v", err)
	}
	if searchRequests != 1 {
		t.Fatalf("search requests = %d, want one", searchRequests)
	}
	if stop.StopID != 1071 || stop.RouteType != 0 {
		t.Fatalf("resolved stop = %+v, want Flinders Street train stop", stop)
	}
}

func TestStationNumericIDStillRequiresMode(t *testing.T) {
	t.Setenv("PTV_API_KEY", "station-test-key")
	t.Setenv("PTV_API_USERID", "123")
	t.Setenv("PTV_BASE_URL", "http://localhost:1")

	stdout, stderr, err := executeStationCommand(t, "station", "1071")
	if err == nil || !strings.Contains(err.Error(), "needs --mode") {
		t.Fatalf("station numeric error = %v, want --mode guidance", err)
	}
	if stdout != "" || stderr != "" {
		t.Fatalf("stdout=%q stderr=%q, want empty", stdout, stderr)
	}
}

func executeStationCommand(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	stationMode = ""
	if err := stationCmd.Flags().Set("mode", ""); err != nil {
		t.Fatalf("reset station mode: %v", err)
	}
	return executeCommand(t, args...)
}

func setStationTestEnvironment(t *testing.T, baseURL string) {
	t.Helper()
	t.Setenv("PTV_API_KEY", "station-test-key")
	t.Setenv("PTV_API_USERID", "123")
	t.Setenv("PTV_BASE_URL", baseURL)
	t.Setenv("PTV_OPENDATA_KEY_ID", "not-used-by-station")
}

func assertStationDetailsRequest(t *testing.T, r *http.Request) {
	t.Helper()
	if r.URL.Path != "/v3/stops/1071/route_type/0" {
		t.Errorf("request path = %q, want Stop Details", r.URL.Path)
	}
	for _, name := range []string{"stop_location", "stop_amenities", "stop_accessibility", "stop_staffing", "stop_disruptions"} {
		if r.URL.Query().Get(name) != "true" {
			t.Errorf("%s = %q, want true", name, r.URL.Query().Get(name))
		}
	}
}

func captureStationRender(t *testing.T, output stationOutput) string {
	t.Helper()
	previous := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create station output pipe: %v", err)
	}
	os.Stdout = writer
	renderErr := renderStationOutput(output)
	if closeErr := writer.Close(); renderErr == nil {
		renderErr = closeErr
	}
	os.Stdout = previous
	data, readErr := io.ReadAll(reader)
	_ = reader.Close()
	if renderErr != nil {
		t.Fatalf("render station output: %v", renderErr)
	}
	if readErr != nil {
		t.Fatalf("read station output: %v", readErr)
	}
	return string(data)
}

func readStationFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/station/stop_details.json")
	if err != nil {
		t.Fatalf("read station fixture: %v", err)
	}
	return data
}

func assertStationGolden(t *testing.T, name, got string) {
	t.Helper()
	want, err := os.ReadFile("testdata/station/" + name)
	if err != nil {
		t.Fatalf("read golden %s: %v\ngot:\n%s", name, err, got)
	}
	if got != string(want) {
		t.Fatalf("%s mismatch\n--- want ---\n%s\n--- got ---\n%s", name, want, got)
	}
}
