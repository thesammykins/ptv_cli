package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/thesammykins/ptv_cli/internal/gtfs"
	"github.com/thesammykins/ptv_cli/internal/gtfsrt"
	"github.com/thesammykins/ptv_cli/internal/localtime"
	"github.com/thesammykins/ptv_cli/internal/ptvapi"
	"github.com/thesammykins/ptv_cli/internal/render"
)

func runNextGTFS(ctx context.Context, sources *resolvedSources, query string, modeTypes []int) error {
	stop, err := resolveGTFSStop(ctx, sources.GTFSStore, query, gtfsFeedModes(modeTypes))
	if err != nil {
		return err
	}
	date, err := parseServiceDate("")
	if err != nil {
		return err
	}
	now := time.Now().In(localtime.Melbourne())
	feedModes := gtfsFeedModes(modeTypes)
	routeID := strings.TrimSpace(nextRoute)
	if routeID != "" {
		route, routeErr := resolveGTFSRoute(ctx, sources.GTFSStore, routeID, feedModes)
		if routeErr != nil {
			return routeErr
		}
		if routeErr := ensureGTFSRouteServesStop(ctx, sources.GTFSStore, stop, route); routeErr != nil {
			return routeErr
		}
		routeID = route.RouteID
	}
	serviceDates := nextServiceDates(date)
	anchors := make(map[string]time.Time, len(serviceDates))
	var departures []gtfs.DepartureResult
	for _, serviceDate := range serviceDates {
		serviceAnchor := localtime.ServiceDayAnchor(serviceDate)
		serviceDateKey := serviceDate.Format("20060102")
		anchors[serviceDateKey] = serviceAnchor
		items, queryErr := sources.GTFSStore.StopDepartures(ctx, stop.StopID, serviceDate, routeID, feedModes, 0)
		if queryErr != nil {
			if !serviceDate.Equal(date) && errors.Is(queryErr, gtfs.ErrQueryOutsideCoverage) {
				continue
			}
			return queryErr
		}
		departures = append(departures, items...)
	}
	filtered := filterNextGTFSDepartures(departures, anchors, now)
	if flagLimit > 0 && len(filtered) > flagLimit {
		filtered = filtered[:flagLimit]
	}
	output := nextOutput{Departures: []nextDepartureOutput{}, Stops: map[string]nextStopOutput{}, Routes: map[string]nextRouteOutput{}, Runs: map[string]nextRunOutput{}, Directions: map[string]nextDirectionOutput{}, Disruptions: map[string]disruptionOutput{}, Status: nextStatusOutput{}, TimeZone: commandTimeZone, DataSource: "gtfs_static", Freshness: freshnessPtr(currentGTFSFreshness(ctx, sources.GTFSStore, sources.GTFSFreshness)), Warnings: []string{}}
	output.Stops[stop.StopID] = nextStopOutput{StopName: stop.StopName, StopLatitude: stop.StopLat, StopLongitude: stop.StopLon, RouteType: feedToAPIType(stop.FeedMode), GTFSStopID: stop.StopID}
	cache := gtfsrt.NewInvocationCache()
	var snapshot *gtfsrt.Snapshot
	var realtimeErr error
	if sources.OpenDataKey != "" {
		if feed, ok := realtimeFeedForMode(stop.FeedMode); ok {
			snapshot, realtimeErr = cache.GetOrFetch(ctx, gtfsrt.New(sources.OpenDataKey), feed)
			if realtimeErr == nil && snapshot != nil {
				output.DataSource = "gtfs_static+opendata_realtime"
				output.Freshness.OpenDataRealtime = sourceFreshnessFromSnapshot(snapshot)
			}
		}
	}
	if sources.OpenDataKey == "" || realtimeErr != nil {
		output.Warnings = append(output.Warnings, realtimeWarning(realtimeErr))
		if !flagJSON {
			fmt.Fprintln(os.Stderr, output.Warnings[len(output.Warnings)-1])
		}
	}
	for _, departure := range filtered {
		routeLabel := departure.RouteShortName
		if routeLabel == "" {
			routeLabel = departure.RouteLongName
		}
		output.Routes[departure.RouteID] = nextRouteOutput{RouteName: departure.RouteLongName, RouteNumber: departure.RouteShortName, RouteType: feedToAPIType(departure.FeedMode), RouteGTFSID: departure.RouteID}
		anchor := anchors[departure.ServiceDate]
		scheduled := anchor.Add(time.Duration(departure.DepartureSec) * time.Second).UTC()
		scheduledString := scheduled.Format(time.RFC3339)
		localString := scheduled.In(localtime.Melbourne()).Format(time.RFC3339)
		item := nextDepartureOutput{ScheduledDepartureUTC: &scheduledString, ScheduledDeparture: &localString, RouteLabel: routeLabel, Towards: departure.Headsign, ServiceStatus: "scheduled", GTFSStopID: departure.StopID, GTFSRouteID: departure.RouteID, GTFSServiceDate: departure.ServiceDate, TripID: departure.TripID}
		if snapshot != nil {
			applyTripUpdate(&item, departure, snapshot, anchor)
		}
		if item.ScheduleRelationship == "CANCELED" {
			continue
		}
		output.Departures = append(output.Departures, item)
	}
	mergeNextPlatformsFromV3(ctx, sources, query, modeTypes, &output)
	if nextPlatform != "" {
		output.Departures = filterNextPlatform(output.Departures, nextPlatform)
	}
	if flagJSON {
		return printJSON(output)
	}
	fmt.Printf("Next departures — %s (%s)\n\n", render.CleanText(stop.StopName), gtfsModeName(stop.FeedMode))
	if len(output.Departures) == 0 {
		fmt.Println("No upcoming departures.")
		return nil
	}
	table := render.NewTable("IN", "SCHEDULED", "EST", "ROUTE", "TOWARDS", "STATUS")
	for _, item := range output.Departures {
		when, _ := time.Parse(time.RFC3339, *item.ScheduledDepartureUTC)
		if item.EstimatedDepartureUTC != nil {
			when, _ = time.Parse(time.RFC3339, *item.EstimatedDepartureUTC)
		}
		table.Row(formatCountdown(when.Sub(time.Now().UTC())), formatLocal(*item.ScheduledDepartureUTC), formatOptionalUTC(item.EstimatedDepartureUTC), item.RouteLabel, item.Towards, item.ServiceStatus)
	}
	return table.Flush()
}

func filterNextGTFSDepartures(departures []gtfs.DepartureResult, anchors map[string]time.Time, now time.Time) []gtfs.DepartureResult {
	windowEnd := now.Add(2 * time.Hour)
	filtered := make([]gtfs.DepartureResult, 0, len(departures))
	for _, departure := range departures {
		anchor, ok := anchors[departure.ServiceDate]
		if !ok {
			continue
		}
		when := anchor.Add(time.Duration(departure.DepartureSec) * time.Second)
		if when.Before(now) || when.After(windowEnd) {
			continue
		}
		filtered = append(filtered, departure)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		left := anchors[filtered[i].ServiceDate].Add(time.Duration(filtered[i].DepartureSec) * time.Second)
		right := anchors[filtered[j].ServiceDate].Add(time.Duration(filtered[j].DepartureSec) * time.Second)
		return left.Before(right)
	})
	return filtered
}

func ensureGTFSRouteServesStop(ctx context.Context, store *gtfs.Store, stop gtfs.StopResult, route gtfs.RouteResult) error {
	routes, err := store.RoutesServingStop(ctx, stop.StopID)
	if err != nil {
		return fmt.Errorf("checking whether route serves stop: %w", err)
	}
	for _, served := range routes {
		if served.RouteID == route.RouteID {
			return nil
		}
	}
	routeName := route.ShortName
	if routeName == "" {
		routeName = route.LongName
	}
	if routeName == "" {
		routeName = route.RouteID
	}
	stopName := stop.StopName
	if stopName == "" {
		stopName = stop.StopID
	}
	return fmt.Errorf("route %q does not serve stop %q; use 'ptv stops on %s' to list its stops", routeName, stopName, routeName)
}

func mergeNextPlatformsFromV3(ctx context.Context, sources *resolvedSources, query string, modeTypes []int, output *nextOutput) {
	if sources == nil || sources.V3Client == nil || output == nil || len(output.Departures) == 0 {
		return
	}
	stopID, ok := resolveV3StopID(ctx, sources.GTFSStore, sources.V3Client, query, gtfsFeedModes(modeTypes))
	if !ok {
		return
	}
	feedMode := 2
	if len(modeTypes) == 1 {
		feedModes := gtfsFeedModes(modeTypes)
		if len(feedModes) == 1 {
			feedMode = feedModes[0]
		}
	}
	response, err := sources.V3Client.Departures(ctx, feedToAPIType(feedMode), stopID, ptvapi.DeparturesOptions{
		MaxResults: maxInt(flagLimit, 8),
		Expand:     []string{ptvapi.ExpandRoute, ptvapi.ExpandRun, ptvapi.ExpandDirection, ptvapi.ExpandDisruption},
	})
	if err != nil {
		return
	}
	v3Output := newNextOutput(response)
	matched := 0
	for _, v3Departure := range v3Output.Departures {
		index, ok := uniqueNextDepartureMatch(output.Departures, v3Departure)
		if !ok {
			continue
		}
		output.Departures[index].PTVStopID = v3Departure.PTVStopID
		output.Departures[index].PTVRouteID = v3Departure.PTVRouteID
		output.Departures[index].PTVRunRef = v3Departure.PTVRunRef
		output.Departures[index].RunRef = v3Departure.RunRef
		output.Departures[index].PlatformNumber = v3Departure.PlatformNumber
		output.Departures[index].AtPlatform = v3Departure.AtPlatform
		output.Departures[index].PTVDisruptionIDs = v3Departure.PTVDisruptionIDs
		output.Departures[index].DisruptionIDs = v3Departure.DisruptionIDs
		matched++
	}
	if matched > 0 {
		output.DataSource = "gtfs_static+opendata_realtime+v3_platforms"
		fmt.Fprintln(os.Stderr, "platform numbers enriched from PTV API")
	}
}

func nextServiceDates(date time.Time) []time.Time {
	return []time.Time{date.AddDate(0, 0, -1), date, date.AddDate(0, 0, 1)}
}

func filterNextPlatform(departures []nextDepartureOutput, platform string) []nextDepartureOutput {
	platform = strings.TrimSpace(platform)
	filtered := make([]nextDepartureOutput, 0, len(departures))
	for _, departure := range departures {
		if departure.PlatformNumber != nil && strings.TrimSpace(*departure.PlatformNumber) == platform {
			filtered = append(filtered, departure)
		}
	}
	return filtered
}

func nextDepartureMatches(primary, enrichment nextDepartureOutput) bool {
	if primary.ScheduledDepartureUTC == nil || enrichment.ScheduledDepartureUTC == nil {
		return false
	}
	primaryTime, primaryErr := time.Parse(time.RFC3339, *primary.ScheduledDepartureUTC)
	enrichmentTime, enrichmentErr := time.Parse(time.RFC3339, *enrichment.ScheduledDepartureUTC)
	if primaryErr != nil || enrichmentErr != nil || primaryTime.Sub(enrichmentTime) > time.Minute || enrichmentTime.Sub(primaryTime) > time.Minute {
		return false
	}
	if strings.TrimSpace(primary.RouteLabel) == "" || strings.TrimSpace(enrichment.RouteLabel) == "" {
		return true
	}
	return mergeKey(primary.RouteLabel, "") == mergeKey(enrichment.RouteLabel, "")
}

func uniqueNextDepartureMatch(departures []nextDepartureOutput, enrichment nextDepartureOutput) (int, bool) {
	matchedIndex := -1
	for index := range departures {
		if !nextDepartureMatches(departures[index], enrichment) {
			continue
		}
		if matchedIndex >= 0 {
			return -1, false
		}
		matchedIndex = index
	}
	return matchedIndex, matchedIndex >= 0
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func applyTripUpdate(item *nextDepartureOutput, departure gtfs.DepartureResult, snapshot *gtfsrt.Snapshot, anchor time.Time) {
	sourceTrip, ok := staticSourceID(departure.TripID)
	if !ok {
		return
	}
	update, ok := snapshot.FindTripUpdate(gtfsrt.StaticTripID(sourceTrip), departure.ServiceDate)
	if !ok {
		return
	}
	item.Freshness = sourceFreshnessFromSnapshot(snapshot)
	item.ScheduleRelationship = update.ScheduleRelationship
	if item.ScheduleRelationship == "" {
		item.ScheduleRelationship = "SCHEDULED"
	}
	for _, stopUpdate := range update.StopTimeUpdates {
		sequencePresent := stopUpdate.StopSequence != nil
		sequenceMatch := !sequencePresent || int(*stopUpdate.StopSequence) == departure.StopSequence
		stopPresent := stopUpdate.StopID != ""
		stopMatch := !stopPresent
		if stopUpdate.StopID != "" {
			staticStop, _ := staticSourceID(departure.StopID)
			stopMatch = staticStop == string(stopUpdate.StopID)
		}
		if !sequenceMatch || !stopMatch {
			continue
		}
		if stopUpdate.ScheduleRelationship == "SKIPPED" {
			item.ServiceStatus = "skipped"
		}
		if stopUpdate.DepartureTime != nil {
			value := time.Unix(*stopUpdate.DepartureTime, 0).UTC().Format(time.RFC3339)
			item.EstimatedDepartureUTC = &value
			local := time.Unix(*stopUpdate.DepartureTime, 0).In(localtime.Melbourne()).Format(time.RFC3339)
			item.EstimatedDeparture = &local
		}
		if item.EstimatedDepartureUTC == nil && stopUpdate.DepartureDelay != nil {
			scheduled := anchor.Add(time.Duration(departure.DepartureSec) * time.Second).UTC()
			value := scheduled.Add(time.Duration(*stopUpdate.DepartureDelay) * time.Second).Format(time.RFC3339)
			item.EstimatedDepartureUTC = &value
			local := scheduled.Add(time.Duration(*stopUpdate.DepartureDelay) * time.Second).In(localtime.Melbourne()).Format(time.RFC3339)
			item.EstimatedDeparture = &local
		}
		if stopUpdate.DepartureDelay != nil {
			item.DelaySeconds = stopUpdate.DepartureDelay
		}
		break
	}
}

func formatOptionalUTC(value *string) string {
	if value == nil {
		return "-"
	}
	return formatLocal(*value)
}
