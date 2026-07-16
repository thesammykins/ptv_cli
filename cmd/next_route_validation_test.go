package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNextRouteFilterDistinguishesMismatchFromNoDepartures(t *testing.T) {
	tests := []struct {
		name            string
		routeServesStop bool
		wantRequests    int32
		wantError       bool
	}{
		{name: "route mismatch", wantRequests: 3, wantError: true},
		{name: "served route with no departures", routeServesStop: true, wantRequests: 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests atomic.Int32
			var departureRequests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasPrefix(r.URL.Path, "/v3/search/"):
					_, _ = io.WriteString(w, `{
  "stops": [{"stop_id": 2214, "stop_name": "Melbourne University/Swanston St #1", "stop_suburb": "Carlton", "route_type": 1}],
  "routes": [], "outlets": [], "status": {"version": "3.0", "health": 1}
}`)
				case r.URL.Path == "/v3/routes":
					if got := r.URL.Query()["route_types"]; len(got) != 1 || got[0] != "1" {
						t.Errorf("route_types = %v, want tram route type", got)
					}
					_, _ = io.WriteString(w, `{
  "routes": [{"route_id": 722, "route_name": "Box Hill - Port Melbourne", "route_number": "109", "route_type": 1}],
  "status": {"version": "3.0", "health": 1}
}`)
				case r.URL.Path == "/v3/stops/route/722/route_type/1":
					stops := `[{"stop_id": 2409, "stop_name": "Box Hill Central/Whitehorse Rd", "route_type": 1}]`
					if tt.routeServesStop {
						stops = `[{"stop_id": 2214, "stop_name": "Melbourne University/Swanston St #1", "route_type": 1}]`
					}
					_, _ = io.WriteString(w, `{"stops":`+stops+`,"status":{"version":"3.0","health":1}}`)
				case r.URL.Path == "/v3/departures/route_type/1/stop/2214/route/722":
					departureRequests.Add(1)
					_, _ = io.WriteString(w, `{"departures":[],"stops":{},"routes":{},"runs":{},"directions":{},"disruptions":{},"status":{"version":"3.0","health":1}}`)
				default:
					t.Errorf("unexpected request path %q", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			t.Setenv("PTV_API_KEY", "contract-test-key")
			t.Setenv("PTV_API_USERID", "123")
			t.Setenv("PTV_BASE_URL", server.URL)
			previousMode, previousRoute, previousPlatform := nextMode, nextRoute, nextPlatform
			t.Cleanup(func() {
				nextMode, nextRoute, nextPlatform = previousMode, previousRoute, previousPlatform
			})
			nextMode, nextRoute, nextPlatform = "", "", ""

			stdout, stderr, err := executeLiveContractCommand(
				t,
				"--json", "--limit", "3", "next", "Melbourne University", "--mode", "tram", "--route", "109",
			)
			if tt.wantError {
				if err == nil || !strings.Contains(err.Error(), `route "109" does not serve stop "Melbourne University/Swanston St #1"`) {
					t.Fatalf("error = %v, want route/stop mismatch", err)
				}
				if stdout != "" || stderr != "" {
					t.Fatalf("stdout=%q stderr=%q, want command error without partial output", stdout, stderr)
				}
				if departureRequests.Load() != 0 {
					t.Fatalf("departure requests = %d, want none for invalid filter", departureRequests.Load())
				}
			} else {
				if err != nil {
					t.Fatalf("valid route with no departures: %v", err)
				}
				if stderr != "" || !json.Valid([]byte(stdout)) {
					t.Fatalf("stdout=%q stderr=%q, want clean JSON", stdout, stderr)
				}
				if departureRequests.Load() != 1 {
					t.Fatalf("departure requests = %d, want one", departureRequests.Load())
				}
			}
			if requests.Load() != tt.wantRequests {
				t.Fatalf("requests = %d, want %d", requests.Load(), tt.wantRequests)
			}
		})
	}
}
