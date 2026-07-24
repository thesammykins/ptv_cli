package cmd

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/thesammykins/ptv_cli/internal/gtfs"
	"github.com/thesammykins/ptv_cli/internal/render"
)

func resolveGTFSRoute(ctx context.Context, store *gtfs.Store, query string, modes []int) (gtfs.RouteResult, error) {
	routes, err := store.RoutesByMode(ctx, modes)
	if err != nil {
		return gtfs.RouteResult{}, err
	}
	query = strings.TrimSpace(query)
	var matches []gtfs.RouteResult
	for _, route := range routes {
		if strings.EqualFold(route.RouteID, query) || strings.EqualFold(route.ShortName, query) || strings.EqualFold(route.LongName, query) || strings.HasSuffix(route.RouteID, ":"+query) {
			matches = append(matches, route)
		}
	}
	if len(matches) == 0 {
		return gtfs.RouteResult{}, fmt.Errorf("GTFS route not found: %q", query)
	}
	if len(matches) > 1 {
		ids := make([]string, len(matches))
		for i, route := range matches {
			ids[i] = route.RouteID
		}
		return gtfs.RouteResult{}, &gtfs.AmbiguousIDError{Kind: "route", Query: query, Candidates: ids}
	}
	return matches[0], nil
}

func newGTFSLinesListOutput(ctx context.Context, store *gtfs.Store, routes []gtfs.RouteResult) linesListOutput {
	output := linesListOutput{Routes: make([]lineRouteOutput, 0, len(routes)), Status: lineStatusOutput{}, DataSource: "gtfs_static", Freshness: freshnessPtr(currentGTFSFreshness(ctx, store)), Warnings: []string{}}
	for _, route := range routes {
		output.Routes = append(output.Routes, lineRouteOutput{RouteName: route.LongName, RouteNumber: route.ShortName, RouteGTFSID: route.RouteID, GTFSRouteID: route.RouteID, RouteType: feedToAPIType(route.FeedMode), Mode: gtfsModeName(route.FeedMode)})
	}
	return output
}

func runLineShowGTFS(ctx context.Context, store *gtfs.Store, query string, modes []int) error {
	route, err := resolveGTFSRoute(ctx, store, query, modes)
	if err != nil {
		return err
	}
	detail, err := store.RouteDetail(ctx, route.RouteID)
	if err != nil {
		return err
	}
	output := linesShowOutput{Route: lineRouteOutput{RouteName: route.LongName, RouteNumber: route.ShortName, RouteGTFSID: route.RouteID, GTFSRouteID: route.RouteID, RouteType: feedToAPIType(route.FeedMode), Mode: gtfsModeName(route.FeedMode)}, Directions: []lineDirectionOutput{}, Stops: map[string][]lineStopOutput{}, DataSource: "gtfs_static", Freshness: freshnessPtr(currentGTFSFreshness(ctx, store)), Warnings: []string{}}
	for _, direction := range detail.Directions {
		output.Directions = append(output.Directions, lineDirectionOutput{DirectionID: direction.DirectionID, DirectionName: direction.Headsign, RouteDirectionDescription: direction.Description, RouteType: feedToAPIType(route.FeedMode)})
		stops := detail.Stops[direction.DirectionID]
		items := make([]lineStopOutput, 0, len(stops))
		for _, stop := range stops {
			items = append(items, lineStopOutput{StopName: stop.StopName, StopLatitude: stop.StopLat, StopLongitude: stop.StopLon, RouteType: feedToAPIType(stop.FeedMode), GTFSStopID: stop.StopID})
		}
		output.Stops[strconv.Itoa(direction.DirectionID)] = items
	}
	if flagJSON {
		return printJSON(output)
	}
	fmt.Printf("%s — %s (%s)\n\n", render.CleanText(route.LongName), render.CleanText(route.ShortName), gtfsModeName(route.FeedMode))
	if len(output.Directions) > 0 {
		fmt.Println("Directions")
		t := render.NewTable("ID", "NAME")
		for _, direction := range output.Directions {
			t.Row(direction.DirectionID, direction.DirectionName)
		}
		if err := t.Flush(); err != nil {
			return err
		}
		fmt.Println()
	}
	if len(output.Directions) > 0 {
		direction := output.Directions[0]
		fmt.Printf("Stops (towards %s)\n", render.CleanText(direction.DirectionName))
		t := render.NewTable("ID", "STOP")
		for _, stop := range output.Stops[strconv.Itoa(direction.DirectionID)] {
			t.Row(stop.GTFSStopID, stop.StopName)
		}
		return t.Flush()
	}
	return nil
}
