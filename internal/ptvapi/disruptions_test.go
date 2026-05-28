package ptvapi

import (
	"encoding/json"
	"testing"
)

func TestDisruptionParsing(t *testing.T) {
	// A representative slice of a /v3/disruptions response body.
	body := `{
	  "disruptions": {
	    "metro_train": [
	      {
	        "disruption_id": 12345,
	        "title": "Buses replace trains on the Lilydale line",
	        "url": "https://ptv.vic.gov.au/x",
	        "disruption_status": "Current",
	        "disruption_type": "Planned Works",
	        "routes": [
	          {
	            "route_type": 0,
	            "route_id": 6,
	            "route_name": "Lilydale",
	            "route_number": "",
	            "direction": {"direction_id": 1, "direction_name": "City"}
	          }
	        ],
	        "stops": [
	          {"route_type": 0, "stop_id": 1071, "stop_name": "Lilydale Station"}
	        ]
	      }
	    ]
	  },
	  "status": {"version": "3.0", "health": 1}
	}`

	var resp DisruptionsResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	metro := resp.Disruptions["metro_train"]
	if len(metro) != 1 {
		t.Fatalf("expected 1 metro_train disruption, got %d", len(metro))
	}
	d := metro[0]
	if d.DisruptionID != 12345 {
		t.Errorf("id = %d", d.DisruptionID)
	}
	if len(d.Routes) != 1 || d.Routes[0].RouteName != "Lilydale" {
		t.Fatalf("routes not parsed: %+v", d.Routes)
	}
	if d.Routes[0].Direction == nil || d.Routes[0].Direction.DirectionName != "City" {
		t.Errorf("direction not parsed: %+v", d.Routes[0].Direction)
	}
	if len(d.Stops) != 1 || d.Stops[0].StopName != "Lilydale Station" {
		t.Errorf("stops not parsed: %+v", d.Stops)
	}
}
