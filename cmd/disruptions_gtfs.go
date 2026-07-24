package cmd

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/thesammykins/ptv_cli/internal/gtfsrt"
	"github.com/thesammykins/ptv_cli/internal/ptvapi"
	"github.com/thesammykins/ptv_cli/internal/render"
)

func runDisruptionsGTFS(ctx context.Context, sources *resolvedSources, routeTypes []int, routeQuery string) error {
	output := disruptionsOutput{Disruptions: map[string][]disruptionOutput{}, Status: disruptionStatusOutput{}, TimeZone: commandTimeZone, DataSource: "opendata_alerts", Freshness: freshnessPtr(currentGTFSFreshness(ctx, sources.GTFSStore, sources.GTFSFreshness)), Warnings: []string{}}
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
		output.Freshness.OpenDataRealtime = worseSourceFreshness(output.Freshness.OpenDataRealtime, sourceFreshnessFromSnapshot(snapshot))
		for _, alert := range snapshot.AllAlerts() {
			if routeQuery != "" && !alertMatchesQuery(alert, routeQuery) {
				continue
			}
			item := newGTFSDisruptionOutput(alert, routeType)
			output.Disruptions[routeTypeName(routeType)] = append(output.Disruptions[routeTypeName(routeType)], item)
		}
	}
	if sources.V3Client != nil {
		if enrichment, enrichmentErr := fetchV3Disruptions(ctx, sources.V3Client, routeTypes, routeQuery); enrichmentErr == nil && enrichment != nil {
			if appendV3Disruptions(&output, enrichment, routeTypes, routeQuery) > 0 {
				output.DataSource = "opendata_alerts+v3_enrichment"
				fmt.Fprintln(os.Stderr, "PTV v3 disruption enrichment applied for requested bus/V/Line scope")
			}
		} else if enrichmentErr != nil {
			output.Warnings = append(output.Warnings, "PTV v3 disruption enrichment unavailable")
		}
	}
	output.Disruptions = limitDisruptionOutputMap(output.Disruptions)
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
			table.Row(item.DisruptionStatus, item.Title)
		}
		if err := table.Flush(); err != nil {
			return err
		}
	}
	return nil
}

func limitDisruptionOutputMap(items map[string][]disruptionOutput) map[string][]disruptionOutput {
	if flagLimit <= 0 {
		return items
	}
	out := make(map[string][]disruptionOutput, len(items))
	remaining := flagLimit
	modes := make([]string, 0, len(items))
	for mode := range items {
		modes = append(modes, mode)
	}
	sort.Strings(modes)
	for _, mode := range modes {
		itemsForMode := items[mode]
		if remaining <= 0 {
			out[mode] = []disruptionOutput{}
			continue
		}
		if len(itemsForMode) > remaining {
			out[mode] = itemsForMode[:remaining]
			remaining = 0
			continue
		}
		out[mode] = itemsForMode
		remaining -= len(itemsForMode)
	}
	return out
}

func fetchV3Disruptions(ctx context.Context, client *ptvapi.Client, routeTypes []int, routeQuery string) (*ptvapi.DisruptionsResponse, error) {
	v3RouteTypes := requestedV3DisruptionTypes(routeTypes)
	if len(v3RouteTypes) == 0 {
		return nil, nil
	}
	if strings.TrimSpace(routeQuery) == "" {
		return client.DisruptionsAll(ctx, v3RouteTypes)
	}
	route, err := resolveRouteWithTypesContext(ctx, client, routeQuery, v3RouteTypes)
	if err != nil {
		return nil, err
	}
	return client.DisruptionsForRoute(ctx, route.RouteID)
}

func requestedV3DisruptionTypes(routeTypes []int) []int {
	if len(routeTypes) == 0 {
		return []int{2, 3}
	}
	result := make([]int, 0, len(routeTypes))
	for _, routeType := range routeTypes {
		if routeType == 2 || routeType == 3 {
			result = append(result, routeType)
		}
	}
	return result
}

func appendV3Disruptions(output *disruptionsOutput, response *ptvapi.DisruptionsResponse, routeTypes []int, routeQuery string) int {
	if output == nil || response == nil {
		return 0
	}
	allowedTypes := requestedV3DisruptionTypes(routeTypes)
	appended := 0
	for mode, items := range response.Disruptions {
		routeType, ok := v3DisruptionModeAllowed(mode, allowedTypes)
		if !ok {
			continue
		}
		bucket := routeTypeName(routeType)
		for _, disruption := range items {
			item := newDisruptionOutput(disruption)
			if routeQuery != "" && !disruptionMatchesRoute(item, routeQuery) {
				continue
			}
			item.ID = fmt.Sprintf("v3:%d", disruption.DisruptionID)
			item.Source = "v3"
			output.Disruptions[bucket] = append(output.Disruptions[bucket], item)
			appended++
		}
	}
	return appended
}

func v3DisruptionModeAllowed(mode string, routeTypes []int) (int, bool) {
	mode = strings.ToLower(strings.NewReplacer("/", "", "_", "", "-", "", " ", "").Replace(mode))
	var routeType int
	switch {
	case strings.Contains(mode, "vline") || strings.Contains(mode, "coach"):
		routeType = 3
	case strings.Contains(mode, "bus"):
		routeType = 2
	default:
		return 0, false
	}
	for _, allowed := range routeTypes {
		if allowed == routeType {
			return routeType, true
		}
	}
	return 0, false
}

func disruptionMatchesRoute(item disruptionOutput, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	for _, route := range item.Routes {
		for _, value := range []string{route.RouteNumber, route.RouteName, route.RouteGTFSID, fmt.Sprint(route.RouteID), fmt.Sprint(route.PTVRouteID)} {
			if strings.EqualFold(strings.TrimSpace(value), query) {
				return true
			}
		}
	}
	return false
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
