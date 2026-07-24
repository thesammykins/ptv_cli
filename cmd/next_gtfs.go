package cmd

import (
	"context"
	"fmt"
	"os"
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
	anchor := localtime.ServiceDayAnchor(date)
	low := int(now.Sub(anchor).Seconds())
	if low < 0 {
		low = 0
	}
	departures, err := sources.GTFSStore.StopDepartures(ctx, stop.StopID, date, nextRoute, gtfsFeedModes(modeTypes), 0)
	if err != nil {
		return err
	}
	filtered := departures[:0]
	for _, departure := range departures {
		if departure.DepartureSec >= low && departure.DepartureSec <= low+2*60*60 {
			filtered = append(filtered, departure)
		}
	}
	if flagLimit > 0 && len(filtered) > flagLimit {
		filtered = filtered[:flagLimit]
	}
	output := nextOutput{Departures: []nextDepartureOutput{}, Stops: map[string]nextStopOutput{}, Routes: map[string]nextRouteOutput{}, Runs: map[string]nextRunOutput{}, Directions: map[string]nextDirectionOutput{}, Disruptions: map[string]disruptionOutput{}, Status: nextStatusOutput{}, TimeZone: commandTimeZone, DataSource: "gtfs_static", Freshness: freshnessPtr(currentGTFSFreshness(ctx, sources.GTFSStore)), Warnings: []string{}}
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
		for index := range output.Departures {
			if !nextDepartureMatches(output.Departures[index], v3Departure) {
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
			break
		}
	}
	if matched > 0 {
		output.DataSource = "gtfs_static+opendata_realtime+v3_platforms"
		fmt.Fprintln(os.Stderr, "platform numbers enriched from PTV API")
	}
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
		sequenceMatch := stopUpdate.StopSequence != nil && int(*stopUpdate.StopSequence) == departure.StopSequence
		stopMatch := false
		if stopUpdate.StopID != "" {
			staticStop, _ := staticSourceID(departure.StopID)
			stopMatch = staticStop == string(stopUpdate.StopID)
		}
		if stopUpdate.StopSequence != nil && stopUpdate.StopID != "" && !sequenceMatch && !stopMatch {
			return
		}
		if !sequenceMatch && !stopMatch {
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
