package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/thesammykins/ptv_cli/internal/ptvapi"
)

func TestNewStopsOutputOwnsNormalizedContract(t *testing.T) {
	output := newStopsOutput([]ptvapi.StopModel{{
		StopID:        1071,
		StopName:      "  Flinders   Street\nStation  ",
		StopSuburb:    " Melbourne ",
		RouteType:     0,
		StopLatitude:  -37.8183,
		StopLongitude: 144.9671,
		StopSequence:  2,
	}}, ptvapi.Status{Version: " 3.0 ", Health: 1}, " © OpenStreetMap contributors ")

	if len(output.Stops) != 1 || output.Stops[0].PTVStopID != 1071 {
		t.Fatalf("output stop identity = %+v", output.Stops)
	}
	if output.Stops[0].StopName != "Flinders Street Station" || output.Status.Version != "3.0" {
		t.Fatalf("output was not normalized: %+v", output)
	}
	raw, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{`"stops"`, `"ptv_stop_id":1071`, `"status"`, `"attribution"`} {
		if !strings.Contains(string(raw), required) {
			t.Fatalf("JSON %s missing %s", raw, required)
		}
	}
}
