package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thesammykins/ptv_cli/internal/ptvapi"
)

func TestStationJSONGoldenUsesOneOfficialStopDetailsRequest(t *testing.T) {
	fixture := readStationFixture(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		assertStationDetailsRequest(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	}))
	defer server.Close()
	setStationTestEnvironment(t, server.URL)

	stdout, stderr, err := executeStationCommand(t, "--json", "station", "--mode", "train", "1071")
	if err != nil {
		t.Fatalf("station --json: %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want exactly one Stop Details request", requests.Load())
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if !json.Valid([]byte(stdout)) {
		t.Fatalf("stdout is not one valid JSON document:\n%s", stdout)
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
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		assertStationDetailsRequest(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	}))
	defer server.Close()
	setStationTestEnvironment(t, server.URL)

	stdout, stderr, err := executeStationCommand(t, "station", "--mode", "train", "1071")
	if err != nil {
		t.Fatalf("station: %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want exactly one Stop Details request", requests.Load())
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	assertStationGolden(t, "station.txt.golden", stdout)
}

func TestStationNameResolutionUsesOneSearchThenOneStopDetailsRequest(t *testing.T) {
	fixture := readStationFixture(t)
	var searchRequests atomic.Int32
	var detailsRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/v3/search/"):
			searchRequests.Add(1)
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
		case r.URL.Path == "/v3/stops/1071/route_type/0":
			detailsRequests.Add(1)
			assertStationDetailsRequest(t, r)
			_, _ = w.Write(fixture)
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	setStationTestEnvironment(t, server.URL)

	stdout, stderr, err := executeStationCommand(t, "--json", "station", "Flinders Street")
	if err != nil {
		t.Fatalf("station by name: %v", err)
	}
	if searchRequests.Load() != 1 || detailsRequests.Load() != 1 {
		t.Fatalf("search requests = %d, details requests = %d; want 1 each", searchRequests.Load(), detailsRequests.Load())
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if strings.Contains(stdout, `"latitude": -1`) || !strings.Contains(stdout, `"latitude": -37.818175`) {
		t.Fatalf("station coordinates did not come exclusively from Stop Details:\n%s", stdout)
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
