package ptvapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFareEstimateUsesEndpointSpecificContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/fare_estimate/min_zone/1/max_zone/2" {
			t.Errorf("path = %q", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("journey_touch_on_utc"); got != "2026-7-16 00:00" {
			t.Errorf("journey_touch_on_utc = %q", got)
		}
		if got := r.URL.Query().Get("journey_touch_off_utc"); got != "2026-7-16 02:00" {
			t.Errorf("journey_touch_off_utc = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
  "FareEstimateResultStatus": {"StatusCode": 0, "Message": "success"},
  "FareEstimateResult": {
    "ZoneInfo": {"MinZone": 1, "MaxZone": 2, "UniqueZones": [1, 2]},
    "PassengerFares": [{
      "PassengerType": "fullFare",
      "Fare2HourPeak": 5.50,
      "Fare2HourOffPeak": 5.50,
      "FareDailyPeak": 11.00,
      "FareDailyOffPeak": 11.00
    }]
  }
}`)
	}))
	defer server.Close()

	client := New(server.URL, "contract-test-key", "123")
	touchOn := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)
	response, err := client.FareEstimate(context.Background(), 1, 2, touchOn, touchOn.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if response.FareEstimateResultStatus.StatusCode != 0 || len(response.FareEstimateResult.PassengerFares) != 1 {
		t.Fatalf("fare response = %+v", response)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"status"`) {
		t.Fatalf("fare DTO invented a generic status field: %s", encoded)
	}
}
