package cmd

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/thesammykins/ptv_cli/internal/gtfs"
	"github.com/thesammykins/ptv_cli/internal/ptvapi"
	"github.com/thesammykins/ptv_cli/internal/v3static"
)

func resolveGTFSStop(ctx context.Context, store *gtfs.Store, query string, modes []int) (gtfs.StopResult, error) {
	if result, err := store.ResolveStop(ctx, query, modes); err == nil {
		return result, nil
	}
	if result, err := store.StopSearch(ctx, query, modes, 2); err == nil && len(result) > 0 {
		if len(result) > 1 && !strings.Contains(query, ":") {
			// Prefer an exact name, otherwise surface ambiguity rather than
			// selecting a different mode accidentally.
			for _, candidate := range result {
				if strings.EqualFold(candidate.StopName, query) {
					return canonicalGTFSStop(ctx, store, candidate)
				}
			}
		}
		return canonicalGTFSStop(ctx, store, result[0])
	}
	// StopSearch intentionally operates on names. The query layer's exact
	// resolver is exercised by a single-result search against the public ID.
	if result, err := store.StopSearch(ctx, query, modes, 1); err == nil && len(result) == 1 {
		return canonicalGTFSStop(ctx, store, result[0])
	}
	return gtfs.StopResult{}, fmt.Errorf("GTFS stop not found: %q", query)
}

func canonicalGTFSStop(ctx context.Context, store *gtfs.Store, stop gtfs.StopResult) (gtfs.StopResult, error) {
	if stop.ParentStation == "" {
		return stop, nil
	}
	parent, err := store.ResolveStop(ctx, stop.ParentStation, nil)
	if err != nil {
		return stop, nil
	}
	return parent, nil
}

func mergeStationFacilitiesFromV3(ctx context.Context, sources *resolvedSources, query string, modes []int, stop gtfs.StopResult, output *stationOutput) {
	if sources == nil || output == nil {
		return
	}
	if sources.V3Client == nil && sources.V3Static != nil {
		routeTypes := []int{feedToAPIType(stop.FeedMode)}
		if facility, ok := sources.V3Static.FindStation(query, routeTypes); ok {
			if mergeStationFacilityOutput(staticStationResponse(facility), output) {
				output.DataSource = "gtfs_static+v3_static_facilities"
				output.SourceNotice = v3StaticNotice()
				fmt.Fprintln(os.Stderr, "station facilities enriched from bundled PTV API snapshot")
			}
		}
		return
	}
	if sources.V3Client == nil {
		return
	}
	stopID, ok := resolveV3StopID(ctx, sources.GTFSStore, sources.V3Client, query, gtfsFeedModes(modes))
	if !ok {
		return
	}
	routeTypes := []int{feedToAPIType(stop.FeedMode)}
	if len(modes) == 0 {
		routeTypes = append(routeTypes, 0, 1, 2, 3, 4)
	}
	var response *ptvapi.StopResponse
	var err error
	for _, routeType := range routeTypes {
		response, err = sources.V3Client.StopDetails(ctx, stopID, routeType)
		if err == nil {
			break
		}
	}
	if err != nil || response == nil {
		if sources.V3Static != nil {
			staticRouteTypes := []int{feedToAPIType(stop.FeedMode)}
			if facility, ok := sources.V3Static.FindStation(query, staticRouteTypes); ok && mergeStationFacilityOutput(staticStationResponse(facility), output) {
				output.DataSource = "gtfs_static+v3_static_facilities"
				output.SourceNotice = v3StaticNotice()
				fmt.Fprintln(os.Stderr, "station facilities enriched from bundled PTV API snapshot")
				return
			}
		}
		output.Warnings = append(output.Warnings, "PTV station facilities unavailable; showing GTFS station data")
		return
	}
	if !mergeStationFacilityOutput(response, output) {
		return
	}
	output.DataSource = "gtfs_static+v3_facilities"
	fmt.Fprintln(os.Stderr, "station facilities enriched from PTV API")
}

func staticStationResponse(facility v3static.StationFacility) *ptvapi.StopResponse {
	details := ptvapi.StopDetails{
		DisruptionIDs: append([]int64(nil), nil...), StationType: facility.StationType, StationDescription: facility.StationDescription,
		RouteType: facility.RouteType, StopID: facility.PTVStopID, StopName: facility.StopName, StopLandmark: staticStringPtr(facility.StopLandmark),
		Routes: make([]ptvapi.Route, 0),
	}
	if facility.StopLocation != nil {
		details.StopLocation = &ptvapi.StopLocation{GPS: &ptvapi.StopGPS{Latitude: facility.StopLocation.Latitude, Longitude: facility.StopLocation.Longitude}}
	}
	if facility.StopAmenities != nil {
		details.StopAmenities = &ptvapi.StopAmenityDetails{Toilet: facility.StopAmenities.Toilet, TaxiRank: facility.StopAmenities.TaxiRank, CarParking: facility.StopAmenities.CarParking, CCTV: facility.StopAmenities.CCTV}
	}
	if facility.StopAccessibility != nil {
		accessibility := facility.StopAccessibility
		details.StopAccessibility = &ptvapi.StopAccessibility{
			Lighting: accessibility.Lighting, PlatformNumber: accessibility.PlatformNumber, AudioCustomerInformation: accessibility.AudioCustomerInformation,
			Escalator: accessibility.Escalator, HearingLoop: accessibility.HearingLoop, Lift: accessibility.Lift, Stairs: accessibility.Stairs,
			StopAccessible: accessibility.StopAccessible, TactileGroundSurfaceIndicator: accessibility.TactileGroundSurfaceIndicator, WaitingRoom: accessibility.WaitingRoom,
		}
		if accessibility.Wheelchair != nil {
			wheelchair := accessibility.Wheelchair
			details.StopAccessibility.Wheelchair = &ptvapi.StopAccessibilityWheelchair{
				AccessibleRamp: wheelchair.AccessibleRamp, Parking: wheelchair.Parking, Telephone: wheelchair.Telephone, Toilet: wheelchair.Toilet,
				LowTicketCounter: wheelchair.LowTicketCounter, Manouvering: wheelchair.Manoeuvring, RaisedPlatform: wheelchair.RaisedPlatform,
				Ramp: wheelchair.Ramp, SecondaryPath: wheelchair.SecondaryPath, RaisedPlatformShelter: wheelchair.RaisedPlatformShelter, SteepRamp: wheelchair.SteepRamp,
			}
		}
	}
	if facility.StopStaffing != nil {
		staffing := facility.StopStaffing
		details.StopStaffing = &ptvapi.StopStaffing{
			MonAMFrom: staffing.MondayAMFrom, MonAMTo: staffing.MondayAMTo, MonPMFrom: staffing.MondayPMFrom, MonPMTo: staffing.MondayPMTo,
			TueAMFrom: staffing.TuesdayAMFrom, TueAMTo: staffing.TuesdayAMTo, TuePMFrom: staffing.TuesdayPMFrom, TuePMTo: staffing.TuesdayPMTo,
			WedAMFrom: staffing.WednesdayAMFrom, WedAMTo: staffing.WednesdayAMTo, WedPMFrom: staffing.WednesdayPMFrom, WedPMTo: staffing.WednesdayPMTo,
			ThuAMFrom: staffing.ThursdayAMFrom, ThuAMTo: staffing.ThursdayAMTo, ThuPMFrom: staffing.ThursdayPMFrom, ThuPMTo: staffing.ThursdayPMTo,
			FriAMFrom: staffing.FridayAMFrom, FriAMTo: staffing.FridayAMTo, FriPMFrom: staffing.FridayPMFrom, FriPMTo: staffing.FridayPMTo,
			SatAMFrom: staffing.SaturdayAMFrom, SatAMTo: staffing.SaturdayAMTo, SatPMFrom: staffing.SaturdayPMFrom, SatPMTo: staffing.SaturdayPMTo,
			SunAMFrom: staffing.SundayAMFrom, SunAMTo: staffing.SundayAMTo, SunPMFrom: staffing.SundayPMFrom, SunPMTo: staffing.SundayPMTo,
			PHFrom: staffing.PublicHolidayFrom, PHTo: staffing.PublicHolidayTo, PHAdditionalText: staffing.PublicHolidayAdditionalText,
		}
	}
	return &ptvapi.StopResponse{Stop: details, Disruptions: map[string]ptvapi.Disruption{}}
}

func staticStringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func mergeStationFacilityOutput(response *ptvapi.StopResponse, output *stationOutput) bool {
	if response == nil || output == nil {
		return false
	}
	enriched := newStationOutput(response, &ptvapi.StopModel{StopID: response.Stop.StopID, RouteType: response.Stop.RouteType})
	if enriched.Stop.StopAmenities == nil && enriched.Stop.StopAccessibility == nil && enriched.Stop.StopStaffing == nil && len(enriched.Disruptions) == 0 {
		return false
	}
	if output.Stop.StopID == 0 {
		output.Stop.StopID = enriched.Stop.StopID
	}
	if output.Stop.PTVStopID == 0 {
		output.Stop.PTVStopID = enriched.Stop.PTVStopID
	}
	for index := range output.Stop.Routes {
		if output.Stop.Routes[index].RouteID != 0 {
			continue
		}
		for _, route := range enriched.Stop.Routes {
			if stationRoutesMatch(output.Stop.Routes[index], route) {
				output.Stop.Routes[index].RouteID = route.RouteID
				output.Stop.Routes[index].PTVRouteID = route.PTVRouteID
				break
			}
		}
	}
	output.Stop.StationType = enriched.Stop.StationType
	output.Stop.StationDescription = enriched.Stop.StationDescription
	output.Stop.StopLandmark = enriched.Stop.StopLandmark
	output.Stop.StopAmenities = enriched.Stop.StopAmenities
	output.Stop.StopAccessibility = enriched.Stop.StopAccessibility
	output.Stop.StopStaffing = enriched.Stop.StopStaffing
	output.Stop.DisruptionIDs = append([]int64(nil), enriched.Stop.DisruptionIDs...)
	output.Stop.PTVDisruptionIDs = append([]int64(nil), enriched.Stop.PTVDisruptionIDs...)
	output.Disruptions = enriched.Disruptions
	return true
}

func stationRoutesMatch(primary stationRouteOutput, enrichment stationRouteOutput) bool {
	values := [][2]string{
		{primary.RouteNumber, enrichment.RouteNumber},
		{primary.RouteName, enrichment.RouteName},
		{primary.RouteGTFSID, enrichment.RouteGTFSID},
	}
	for _, pair := range values {
		left := strings.TrimSpace(pair[0])
		right := strings.TrimSpace(pair[1])
		if left != "" && right != "" && strings.EqualFold(left, right) {
			return true
		}
	}
	return false
}

func resolveV3StopID(ctx context.Context, store *gtfs.Store, client *ptvapi.Client, query string, modes []int) (int, bool) {
	if numeric, err := strconv.Atoi(strings.TrimSpace(query)); err == nil && numeric > 0 {
		return numeric, true
	}
	if client != nil {
		var routeTypes []int
		for _, mode := range modes {
			routeTypes = append(routeTypes, feedToAPIType(mode))
		}
		response, err := client.Search(ctx, query, routeTypes)
		if err == nil {
			for _, result := range response.Stops {
				if result.StopID > 0 {
					return result.StopID, true
				}
			}
		}
	}
	results, err := store.StopSearch(ctx, query, modes, 20)
	if err != nil {
		return 0, false
	}
	for _, result := range results {
		if _, source, ok := strings.Cut(result.StopID, ":"); ok {
			if numeric, err := strconv.Atoi(source); err == nil && numeric > 0 {
				return numeric, true
			}
		}
	}
	return 0, false
}

func newGTFSStationOutput(ctx context.Context, store *gtfs.Store, detail *gtfs.StopDetailResult) stationOutput {
	stop := detail.Stop
	output := stationOutput{Disruptions: map[string]stationDisruptionOutput{}, Status: stationStatusOutput{}, TimeZone: commandTimeZone, DataSource: "gtfs_static", Freshness: freshnessPtr(currentGTFSFreshness(ctx, store)), Warnings: []string{}}
	legacyStopID := numericSourceID(stop.StopID)
	output.Stop = stationStopOutput{StopID: legacyStopID, PTVStopID: legacyStopID, StopName: stop.StopName, StopLatitude: &stop.StopLat, StopLongitude: &stop.StopLon, RouteType: feedToAPIType(stop.FeedMode), Routes: make([]stationRouteOutput, 0, len(detail.Routes)), GTFSStopID: stop.StopID, ParentStation: stop.ParentStation, LocationType: stop.LocationType, WheelchairBoarding: stop.WheelchairBoarding, Transfers: detail.Transfers, Pathways: detail.Pathways}
	for _, route := range detail.Routes {
		output.Stop.Routes = append(output.Stop.Routes, stationRouteOutput{RouteType: feedToAPIType(route.FeedMode), RouteName: route.LongName, RouteNumber: route.ShortName, RouteGTFSID: route.RouteID})
	}
	return output
}

func numericSourceID(publicID string) int {
	if _, source, ok := strings.Cut(strings.TrimSpace(publicID), ":"); ok {
		publicID = source
	}
	numeric, err := strconv.Atoi(publicID)
	if err != nil || numeric <= 0 {
		return 0
	}
	return numeric
}
