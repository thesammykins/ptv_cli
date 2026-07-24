package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/thesammykins/ptv_cli/internal/gtfsrt"
	"github.com/thesammykins/ptv_cli/internal/render"
)

func runDisruptionsGTFS(ctx context.Context, sources *resolvedSources, routeTypes []int, routeQuery string) error {
	output := disruptionsOutput{Disruptions: map[string][]disruptionOutput{}, Status: disruptionStatusOutput{}, TimeZone: commandTimeZone, DataSource: "opendata_alerts", Freshness: freshnessPtr(currentGTFSFreshness(ctx, sources.GTFSStore)), Warnings: []string{}}
	if sources.OpenDataKey == "" {
		output.Warnings = append(output.Warnings, "Open Data service alerts unavailable; run 'ptv auth opendata login'; an empty result does not prove that no disruptions exist")
		if flagJSON {
			return printJSON(output)
		}
		fmt.Fprintln(os.Stderr, output.Warnings[0])
		fmt.Println("No disruptions available without Open Data credentials.")
		return nil
	}
	cache := gtfsrt.NewInvocationCache()
	client := gtfsrt.New(sources.OpenDataKey)
	requestedModes := routeTypes
	if len(requestedModes) == 0 {
		requestedModes = []int{0, 1}
	}
	for _, routeType := range requestedModes {
		if routeType != 0 && routeType != 1 {
			output.Warnings = append(output.Warnings, fmt.Sprintf("Open Data service-alert feed is not catalogued for %s", routeTypeName(routeType)))
			continue
		}
		feedName := map[int]string{0: "metro-service-alerts", 1: "tram-service-alerts"}[routeType]
		feed, _ := gtfsrt.FeedByID(feedName)
		snapshot, err := cache.GetOrFetch(ctx, client, feed)
		if err != nil {
			output.Warnings = append(output.Warnings, fmt.Sprintf("%s alerts unavailable: %s", routeTypeName(routeType), err))
			continue
		}
		output.Freshness.OpenDataRealtime = sourceFreshnessFromSnapshot(snapshot)
		for _, alert := range snapshot.AllAlerts() {
			if routeQuery != "" && !alertMatchesQuery(alert, routeQuery) {
				continue
			}
			item := newGTFSDisruptionOutput(alert, routeType)
			output.Disruptions[routeTypeName(routeType)] = append(output.Disruptions[routeTypeName(routeType)], item)
		}
	}
	if sources.V3Client != nil {
		if enrichment, enrichmentErr := sources.V3Client.DisruptionsAll(ctx, []int{2, 3}); enrichmentErr == nil {
			for mode, items := range enrichment.Disruptions {
				for _, disruption := range items {
					item := newDisruptionOutput(disruption)
					item.ID = fmt.Sprintf("v3:%d", disruption.DisruptionID)
					item.Source = "v3"
					output.Disruptions[mode] = append(output.Disruptions[mode], item)
				}
			}
			output.DataSource = "opendata_alerts+v3_bus_vline"
			fmt.Fprintln(os.Stderr, "bus and V/Line disruptions from PTV API; metro and tram from Open Data")
		} else {
			output.Warnings = append(output.Warnings, "bus and V/Line disruption enrichment unavailable")
		}
	}
	if flagJSON {
		return printJSON(output)
	}
	for mode, items := range output.Disruptions {
		if len(items) == 0 {
			continue
		}
		fmt.Printf("\n%s\n", render.CleanText(mode))
		table := render.NewTable("STATUS", "TITLE")
		for _, item := range items {
			table.Row(item.Effect, item.Title)
		}
		if err := table.Flush(); err != nil {
			return err
		}
	}
	return nil
}

func alertMatchesQuery(alert gtfsrt.Alert, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	for _, entity := range alert.InformedEntities {
		if strings.Contains(strings.ToLower(entity.RouteID), query) || strings.Contains(strings.ToLower(entity.StopID), query) {
			return true
		}
	}
	for _, text := range alert.HeaderText {
		if strings.Contains(strings.ToLower(text.Text), query) {
			return true
		}
	}
	return false
}

func newGTFSDisruptionOutput(alert gtfsrt.Alert, routeType int) disruptionOutput {
	item := disruptionOutput{ID: string(alert.EntityID), Source: "opendata", Title: firstTranslation(alert.HeaderText), Description: firstTranslation(alert.DescriptionText), Routes: []disruptionRouteOutput{}, Stops: []disruptionStopOutput{}}
	if alert.Cause != "" {
		value := alert.Cause
		item.Cause = &value
	}
	if alert.Effect != "" {
		value := alert.Effect
		item.Effect = &value
	}
	item.DisruptionStatus = "current"
	for _, period := range alert.ActivePeriods {
		if period.Start != nil {
			item.FromDate = period.Start.Format(time.RFC3339)
		}
		if period.End != nil {
			value := period.End.Format(time.RFC3339)
			item.ToDate = &value
		}
		break
	}
	seenRoutes := map[string]bool{}
	seenStops := map[string]bool{}
	for _, entity := range alert.InformedEntities {
		if entity.RouteID != "" {
			key := fmt.Sprintf("%d:%s", routeType, entity.RouteID)
			if !seenRoutes[key] {
				seenRoutes[key] = true
				item.Routes = append(item.Routes, disruptionRouteOutput{RouteType: routeType, RouteGTFSID: entity.RouteID, RouteName: entity.RouteID})
			}
		}
		if entity.StopID != "" {
			key := fmt.Sprintf("%d:%s", routeType, entity.StopID)
			if !seenStops[key] {
				seenStops[key] = true
				item.Stops = append(item.Stops, disruptionStopOutput{RouteType: routeType, GTFSStopID: entity.StopID, StopName: entity.StopID})
			}
		}
	}
	return item
}
func firstTranslation(values []gtfsrt.TranslatedString) string {
	if len(values) == 0 {
		return ""
	}
	return values[0].Text
}
