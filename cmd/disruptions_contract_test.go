package cmd

import (
	"testing"

	"github.com/thesammykins/ptv_cli/internal/gtfsrt"
)

func TestNewGTFSDisruptionOutputDeduplicatesRepeatedEntities(t *testing.T) {
	item := newGTFSDisruptionOutput(gtfsrt.Alert{
		EntityID: "alert-1",
		InformedEntities: []gtfsrt.AlertEntity{
			{RouteID: "route-1", StopID: "stop-1"},
			{RouteID: "route-1", StopID: "stop-1"},
		},
	}, 0)
	if len(item.Routes) != 1 || len(item.Stops) != 1 {
		t.Fatalf("routes=%d stops=%d, want one each", len(item.Routes), len(item.Stops))
	}
}
