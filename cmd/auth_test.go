package cmd

import (
	"testing"

	"github.com/thesammykins/ptv_cli/internal/ptvapi"
)

func TestNewAuthCheckOutputOwnsNormalizedContract(t *testing.T) {
	output := newAuthCheckOutput(&ptvapi.RouteTypesResponse{
		RouteTypes: []ptvapi.RouteType{{RouteTypeName: " Metro\nTrain ", RouteType: 0}},
		Status:     ptvapi.Status{Version: " 3.0 ", Health: 1},
	})
	if len(output.RouteTypes) != 1 || output.RouteTypes[0].RouteTypeName != "Metro Train" {
		t.Fatalf("route types = %+v", output.RouteTypes)
	}
	if output.Status.Version != "3.0" || output.Status.Health != 1 {
		t.Fatalf("status = %+v", output.Status)
	}
}
