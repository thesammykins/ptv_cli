package cmd

import (
	"context"

	"github.com/thesammykins/ptv_cli/internal/gtfs"
)

func newGTFSStopsOutput(ctx context.Context, store *gtfs.Store, stops []gtfs.NearbyStopResult, attribution string, reports ...*gtfs.FreshnessReport) stopsOutput {
	output := stopsOutput{Stops: make([]stopsStopOutput, 0, len(stops)), Attribution: normalizedText(attribution), DataSource: "gtfs_static", Freshness: freshnessPtr(currentGTFSFreshness(ctx, store, reports...)), Warnings: []string{}}
	for _, item := range stops {
		stop := item.StopResult
		output.Stops = append(output.Stops, stopsStopOutput{StopName: stop.StopName, StopLatitude: stop.StopLat, StopLongitude: stop.StopLon, RouteType: feedToAPIType(stop.FeedMode), StopDistance: item.DistanceMetres, GTFSStopID: stop.StopID, Mode: gtfsModeName(stop.FeedMode)})
	}
	return output
}
