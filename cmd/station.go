package cmd

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/thesammykins/ptv_cli/internal/gtfs"
	"github.com/thesammykins/ptv_cli/internal/ptvapi"
	"github.com/thesammykins/ptv_cli/internal/render"
)

var stationMode string

var stationCmd = &cobra.Command{
	Use:   "station <stop-id|name>",
	Short: "Show facilities and platforms for a station/stop",
	Long:  "Show stop details, routes and facilities. Metro and V/Line stations return the most detail.",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var modeHint []int
		if stationMode != "" {
			rt, ok := parseMode(stationMode)
			if !ok {
				return fmt.Errorf("unknown mode %q", stationMode)
			}
			modeHint = []int{rt}
		}
		if numericID, numericErr := strconv.Atoi(strings.TrimSpace(joinArgs(args))); numericErr == nil && numericID > 0 && len(modeHint) == 0 {
			return fmt.Errorf("a numeric station id needs --mode (train, tram, bus, vline, nightbus)")
		}

		sources, err := resolveSources(cmd.Context())
		if err != nil {
			return err
		}
		defer closeSources(sources)

		commandCtx := cmd.Context()
		if commandCtx == nil {
			commandCtx = context.Background()
		}
		stop, err := resolveGTFSStop(commandCtx, sources.GTFSStore, joinArgs(args), gtfsFeedModes(modeHint))
		if err != nil {
			if numericID, numericErr := strconv.Atoi(strings.TrimSpace(joinArgs(args))); numericErr == nil && numericID > 0 && len(modeHint) == 1 && sources.V3Client != nil {
				routeType := 0
				if len(modeHint) == 1 {
					routeType = modeHint[0]
				}
				if response, v3Err := sources.V3Client.StopDetails(commandCtx, numericID, routeType); v3Err == nil {
					output := newStationOutput(response, &ptvapi.StopModel{StopID: numericID, RouteType: routeType})
					output.DataSource = "ptv_api_v3"
					output.Warnings = []string{"GTFS identity unavailable; using PTV API v3 station details"}
					if flagJSON {
						return printJSON(&output)
					}
					return renderStationOutput(output)
				}
			}
			return err
		}
		detail, err := sources.GTFSStore.StopDetail(commandCtx, stop.StopID)
		if err != nil {
			return err
		}
		output := newGTFSStationOutput(commandCtx, sources.GTFSStore, detail)
		mergeStationFacilitiesFromV3(commandCtx, sources, joinArgs(args), modeHint, stop, &output)
		if flagJSON {
			return printJSON(&output)
		}
		return renderStationOutput(output)
	},
}

// resolveStationStopContext resolves a numeric stop without a network lookup,
// or a station name with exactly one Search request. Stop Details remains the
// sole source for facility and coordinate data.
func resolveStationStopContext(ctx context.Context, client *ptvapi.Client, query string, modeHint []int) (*ptvapi.StopModel, error) {
	query = strings.TrimSpace(query)
	if id, err := strconv.Atoi(query); err == nil {
		stop := &ptvapi.StopModel{StopID: id, RouteType: -1}
		if len(modeHint) == 1 {
			stop.RouteType = modeHint[0]
		}
		return stop, nil
	}
	if len(modeHint) == 0 {
		modeHint = []int{0}
	}

	resp, err := client.Search(ctx, query, modeHint)
	if err != nil {
		return nil, err
	}
	if len(resp.Stops) == 0 {
		return nil, fmt.Errorf("no station matching %q", query)
	}
	return chooseStop(query, resp.Stops), nil
}

// stationOutput is the stable command boundary. It keeps the truthful fields
// exposed by the current major release while adding the official nested Stop
// Details data instead of leaking the upstream transport DTO.
type stationOutput struct {
	Stop         stationStopOutput                  `json:"stop"`
	Disruptions  map[string]stationDisruptionOutput `json:"disruptions"`
	Status       stationStatusOutput                `json:"status"`
	TimeZone     string                             `json:"time_zone"`
	DataSource   string                             `json:"data_source,omitempty"`
	SourceNotice *sourceNoticeOutput                `json:"source_notice,omitempty"`
	Freshness    *freshnessOutput                   `json:"freshness,omitempty"`
	Warnings     []string                           `json:"warnings,omitempty"`
}

type stationStopOutput struct {
	StopID             int                         `json:"stop_id"`
	PTVStopID          int                         `json:"ptv_stop_id"`
	StopName           string                      `json:"stop_name"`
	RouteType          int                         `json:"route_type"`
	StationType        string                      `json:"station_type,omitempty"`
	StationDescription string                      `json:"station_description,omitempty"`
	StopLatitude       *float64                    `json:"stop_latitude,omitempty"`
	StopLongitude      *float64                    `json:"stop_longitude,omitempty"`
	StopLandmark       *string                     `json:"stop_landmark,omitempty"`
	StopLocation       *stationLocationOutput      `json:"stop_location,omitempty"`
	StopAmenities      *stationAmenitiesOutput     `json:"stop_amenities,omitempty"`
	StopAccessibility  *stationAccessibilityOutput `json:"stop_accessibility,omitempty"`
	StopStaffing       *stationStaffingOutput      `json:"stop_staffing,omitempty"`
	Routes             []stationRouteOutput        `json:"routes"`
	DisruptionIDs      []int64                     `json:"disruption_ids,omitempty"`
	PTVDisruptionIDs   []int64                     `json:"ptv_disruption_ids,omitempty"`
	GTFSStopID         string                      `json:"gtfs_stop_id,omitempty"`
	ParentStation      string                      `json:"parent_station,omitempty"`
	LocationType       int                         `json:"location_type,omitempty"`
	WheelchairBoarding int                         `json:"wheelchair_boarding,omitempty"`
	Transfers          []gtfs.TransferResult       `json:"transfers,omitempty"`
	Pathways           []gtfs.PathwayResult        `json:"pathways,omitempty"`
}

type stationLocationOutput struct {
	GPS *stationGPSOutput `json:"gps,omitempty"`
}

type stationGPSOutput struct {
	Latitude  *float64 `json:"latitude,omitempty"`
	Longitude *float64 `json:"longitude,omitempty"`
}

type stationAmenitiesOutput struct {
	Toilet     *bool   `json:"toilet,omitempty"`
	TaxiRank   *bool   `json:"taxi_rank,omitempty"`
	CarParking *string `json:"car_parking,omitempty"`
	CCTV       *bool   `json:"cctv,omitempty"`
}

type stationAccessibilityOutput struct {
	Lighting                      *bool                                 `json:"lighting,omitempty"`
	PlatformNumber                *int                                  `json:"platform_number,omitempty"`
	AudioCustomerInformation      *bool                                 `json:"audio_customer_information,omitempty"`
	Escalator                     *bool                                 `json:"escalator,omitempty"`
	HearingLoop                   *bool                                 `json:"hearing_loop,omitempty"`
	Lift                          *bool                                 `json:"lift,omitempty"`
	Stairs                        *bool                                 `json:"stairs,omitempty"`
	StopAccessible                *bool                                 `json:"stop_accessible,omitempty"`
	TactileGroundSurfaceIndicator *bool                                 `json:"tactile_ground_surface_indicator,omitempty"`
	WaitingRoom                   *bool                                 `json:"waiting_room,omitempty"`
	Wheelchair                    *stationWheelchairAccessibilityOutput `json:"wheelchair,omitempty"`
}

type stationWheelchairAccessibilityOutput struct {
	AccessibleRamp        *bool `json:"accessible_ramp,omitempty"`
	Parking               *bool `json:"parking,omitempty"`
	Telephone             *bool `json:"telephone,omitempty"`
	Toilet                *bool `json:"toilet,omitempty"`
	LowTicketCounter      *bool `json:"low_ticket_counter,omitempty"`
	Manoeuvring           *bool `json:"manoeuvring,omitempty"`
	RaisedPlatform        *bool `json:"raised_platform,omitempty"`
	Ramp                  *bool `json:"ramp,omitempty"`
	SecondaryPath         *bool `json:"secondary_path,omitempty"`
	RaisedPlatformShelter *bool `json:"raised_platform_shelter,omitempty"`
	SteepRamp             *bool `json:"steep_ramp,omitempty"`
}

type stationStaffingOutput struct {
	MondayAMFrom                *string `json:"monday_am_from,omitempty"`
	MondayAMTo                  *string `json:"monday_am_to,omitempty"`
	MondayPMFrom                *string `json:"monday_pm_from,omitempty"`
	MondayPMTo                  *string `json:"monday_pm_to,omitempty"`
	TuesdayAMFrom               *string `json:"tuesday_am_from,omitempty"`
	TuesdayAMTo                 *string `json:"tuesday_am_to,omitempty"`
	TuesdayPMFrom               *string `json:"tuesday_pm_from,omitempty"`
	TuesdayPMTo                 *string `json:"tuesday_pm_to,omitempty"`
	WednesdayAMFrom             *string `json:"wednesday_am_from,omitempty"`
	WednesdayAMTo               *string `json:"wednesday_am_to,omitempty"`
	WednesdayPMFrom             *string `json:"wednesday_pm_from,omitempty"`
	WednesdayPMTo               *string `json:"wednesday_pm_to,omitempty"`
	ThursdayAMFrom              *string `json:"thursday_am_from,omitempty"`
	ThursdayAMTo                *string `json:"thursday_am_to,omitempty"`
	ThursdayPMFrom              *string `json:"thursday_pm_from,omitempty"`
	ThursdayPMTo                *string `json:"thursday_pm_to,omitempty"`
	FridayAMFrom                *string `json:"friday_am_from,omitempty"`
	FridayAMTo                  *string `json:"friday_am_to,omitempty"`
	FridayPMFrom                *string `json:"friday_pm_from,omitempty"`
	FridayPMTo                  *string `json:"friday_pm_to,omitempty"`
	SaturdayAMFrom              *string `json:"saturday_am_from,omitempty"`
	SaturdayAMTo                *string `json:"saturday_am_to,omitempty"`
	SaturdayPMFrom              *string `json:"saturday_pm_from,omitempty"`
	SaturdayPMTo                *string `json:"saturday_pm_to,omitempty"`
	SundayAMFrom                *string `json:"sunday_am_from,omitempty"`
	SundayAMTo                  *string `json:"sunday_am_to,omitempty"`
	SundayPMFrom                *string `json:"sunday_pm_from,omitempty"`
	SundayPMTo                  *string `json:"sunday_pm_to,omitempty"`
	PublicHolidayFrom           *string `json:"public_holiday_from,omitempty"`
	PublicHolidayTo             *string `json:"public_holiday_to,omitempty"`
	PublicHolidayAdditionalText *string `json:"public_holiday_additional_text,omitempty"`
}

type stationRouteOutput struct {
	RouteType   int    `json:"route_type"`
	RouteID     int    `json:"route_id"`
	PTVRouteID  int    `json:"ptv_route_id"`
	RouteName   string `json:"route_name"`
	RouteNumber string `json:"route_number"`
	RouteGTFSID string `json:"route_gtfs_id,omitempty"`
}

type stationDisruptionOutput = disruptionOutput

type stationStatusOutput struct {
	Version string `json:"version"`
	Health  int    `json:"health"`
}

func newStationOutput(response *ptvapi.StopResponse, resolved *ptvapi.StopModel) stationOutput {
	details := response.Stop
	stopID := details.StopID
	if stopID == 0 {
		stopID = resolved.StopID
	}
	stopName := stationText(details.StopName)
	if stopName == "" {
		stopName = stationText(resolved.StopName)
	}

	output := stationOutput{
		Stop: stationStopOutput{
			StopID:             stopID,
			PTVStopID:          stopID,
			StopName:           stopName,
			RouteType:          details.RouteType,
			StationType:        stationText(details.StationType),
			StationDescription: stationText(details.StationDescription),
			StopLandmark:       stationString(details.StopLandmark),
			StopAmenities:      newStationAmenitiesOutput(details.StopAmenities),
			StopAccessibility:  newStationAccessibilityOutput(details.StopAccessibility),
			StopStaffing:       newStationStaffingOutput(details.StopStaffing),
			Routes:             make([]stationRouteOutput, 0, len(details.Routes)),
			DisruptionIDs:      append([]int64(nil), details.DisruptionIDs...),
			PTVDisruptionIDs:   append([]int64(nil), details.DisruptionIDs...),
		},
		Disruptions: make(map[string]stationDisruptionOutput, len(response.Disruptions)),
		Status: stationStatusOutput{
			Version: stationText(response.Status.Version),
			Health:  response.Status.Health,
		},
		TimeZone: commandTimeZone,
	}

	if details.StopLocation != nil && details.StopLocation.GPS != nil &&
		(details.StopLocation.GPS.Latitude != nil || details.StopLocation.GPS.Longitude != nil) {
		gps := &stationGPSOutput{
			Latitude:  details.StopLocation.GPS.Latitude,
			Longitude: details.StopLocation.GPS.Longitude,
		}
		output.Stop.StopLatitude = gps.Latitude
		output.Stop.StopLongitude = gps.Longitude
		output.Stop.StopLocation = &stationLocationOutput{GPS: gps}
	}

	for _, route := range deduplicateRoutes(details.Routes) {
		output.Stop.Routes = append(output.Stop.Routes, stationRouteOutput{
			RouteType:   route.RouteType,
			RouteID:     route.RouteID,
			PTVRouteID:  route.RouteID,
			RouteName:   stationText(route.RouteName),
			RouteNumber: stationText(route.RouteNumber),
			RouteGTFSID: stationText(route.RouteGTFSID),
		})
	}
	for key, disruption := range response.Disruptions {
		output.Disruptions[key] = newStationDisruptionOutput(disruption)
	}
	return output
}

func newStationAmenitiesOutput(value *ptvapi.StopAmenityDetails) *stationAmenitiesOutput {
	if value == nil {
		return nil
	}
	output := &stationAmenitiesOutput{
		Toilet:     value.Toilet,
		TaxiRank:   value.TaxiRank,
		CarParking: stationString(value.CarParking),
		CCTV:       value.CCTV,
	}
	if output.Toilet == nil && output.TaxiRank == nil && output.CarParking == nil && output.CCTV == nil {
		return nil
	}
	return output
}

func newStationAccessibilityOutput(value *ptvapi.StopAccessibility) *stationAccessibilityOutput {
	if value == nil {
		return nil
	}
	output := &stationAccessibilityOutput{
		Lighting:                      value.Lighting,
		PlatformNumber:                value.PlatformNumber,
		AudioCustomerInformation:      value.AudioCustomerInformation,
		Escalator:                     value.Escalator,
		HearingLoop:                   value.HearingLoop,
		Lift:                          value.Lift,
		Stairs:                        value.Stairs,
		StopAccessible:                value.StopAccessible,
		TactileGroundSurfaceIndicator: value.TactileGroundSurfaceIndicator,
		WaitingRoom:                   value.WaitingRoom,
	}
	if value.Wheelchair != nil {
		wheelchair := &stationWheelchairAccessibilityOutput{
			AccessibleRamp:        value.Wheelchair.AccessibleRamp,
			Parking:               value.Wheelchair.Parking,
			Telephone:             value.Wheelchair.Telephone,
			Toilet:                value.Wheelchair.Toilet,
			LowTicketCounter:      value.Wheelchair.LowTicketCounter,
			Manoeuvring:           value.Wheelchair.Manouvering,
			RaisedPlatform:        value.Wheelchair.RaisedPlatform,
			Ramp:                  value.Wheelchair.Ramp,
			SecondaryPath:         value.Wheelchair.SecondaryPath,
			RaisedPlatformShelter: value.Wheelchair.RaisedPlatformShelter,
			SteepRamp:             value.Wheelchair.SteepRamp,
		}
		if stationWheelchairAccessibilityPresent(wheelchair) {
			output.Wheelchair = wheelchair
		}
	}
	if !stationAccessibilityPresent(output) {
		return nil
	}
	return output
}

func newStationStaffingOutput(value *ptvapi.StopStaffing) *stationStaffingOutput {
	if value == nil {
		return nil
	}
	output := &stationStaffingOutput{
		MondayAMFrom:                stationString(value.MonAMFrom),
		MondayAMTo:                  stationString(value.MonAMTo),
		MondayPMFrom:                stationString(value.MonPMFrom),
		MondayPMTo:                  stationString(value.MonPMTo),
		TuesdayAMFrom:               stationString(value.TueAMFrom),
		TuesdayAMTo:                 stationString(value.TueAMTo),
		TuesdayPMFrom:               stationString(value.TuePMFrom),
		TuesdayPMTo:                 stationString(value.TuePMTo),
		WednesdayAMFrom:             stationString(value.WedAMFrom),
		WednesdayAMTo:               stationString(value.WedAMTo),
		WednesdayPMFrom:             stationString(value.WedPMFrom),
		WednesdayPMTo:               stationString(value.WedPMTo),
		ThursdayAMFrom:              stationString(value.ThuAMFrom),
		ThursdayAMTo:                stationString(value.ThuAMTo),
		ThursdayPMFrom:              stationString(value.ThuPMFrom),
		ThursdayPMTo:                stationString(value.ThuPMTo),
		FridayAMFrom:                stationString(value.FriAMFrom),
		FridayAMTo:                  stationString(value.FriAMTo),
		FridayPMFrom:                stationString(value.FriPMFrom),
		FridayPMTo:                  stationString(value.FriPMTo),
		SaturdayAMFrom:              stationString(value.SatAMFrom),
		SaturdayAMTo:                stationString(value.SatAMTo),
		SaturdayPMFrom:              stationString(value.SatPMFrom),
		SaturdayPMTo:                stationString(value.SatPMTo),
		SundayAMFrom:                stationString(value.SunAMFrom),
		SundayAMTo:                  stationString(value.SunAMTo),
		SundayPMFrom:                stationString(value.SunPMFrom),
		SundayPMTo:                  stationString(value.SunPMTo),
		PublicHolidayFrom:           stationString(value.PHFrom),
		PublicHolidayTo:             stationString(value.PHTo),
		PublicHolidayAdditionalText: stationString(value.PHAdditionalText),
	}
	if !stationStaffingPresent(output) {
		return nil
	}
	return output
}

func newStationDisruptionOutput(value ptvapi.Disruption) stationDisruptionOutput {
	return newDisruptionOutput(value)
}

func renderStationOutput(output stationOutput) error {
	stop := output.Stop
	fmt.Println(render.CleanText(stop.StopName))
	fmt.Printf("Stop ID: %d   Mode: %s\n", stop.StopID, routeTypeName(stop.RouteType))
	if stop.StationType != "" {
		fmt.Printf("Type: %s\n", render.CleanText(stop.StationType))
	}
	if stop.StationDescription != "" {
		fmt.Println(render.CleanText(stop.StationDescription))
	}
	if stop.StopLandmark != nil {
		fmt.Printf("Landmark: %s\n", render.CleanText(*stop.StopLandmark))
	}
	renderStationLocation(stop.StopLocation)
	renderStationAmenities(stop.StopAmenities)
	renderStationAccessibility(stop.StopAccessibility)
	renderStationStaffing(stop.StopStaffing)
	renderStationDisruptions(output.Disruptions)

	if len(stop.Routes) > 0 {
		fmt.Println("\nRoutes serving this stop")
		table := render.NewTable("ID", "NUMBER", "NAME", "MODE")
		for _, route := range stop.Routes {
			table.Row(route.RouteID, route.RouteNumber, route.RouteName, routeTypeName(route.RouteType))
		}
		if err := table.Flush(); err != nil {
			return err
		}
	}
	return nil
}

func renderStationLocation(location *stationLocationOutput) {
	if location == nil || location.GPS == nil {
		return
	}
	latitude, longitude := location.GPS.Latitude, location.GPS.Longitude
	switch {
	case latitude != nil && longitude != nil:
		fmt.Printf("Location: %s, %s\n", formatStationCoordinate(*latitude), formatStationCoordinate(*longitude))
	case latitude != nil:
		fmt.Printf("Latitude: %s\n", formatStationCoordinate(*latitude))
	case longitude != nil:
		fmt.Printf("Longitude: %s\n", formatStationCoordinate(*longitude))
	}
}

func renderStationAmenities(value *stationAmenitiesOutput) {
	if value == nil {
		return
	}
	fmt.Println("\nAmenities")
	printStationBool("Toilet", value.Toilet)
	printStationBool("Taxi rank", value.TaxiRank)
	if value.CarParking != nil {
		fmt.Printf("  Car parking: %s\n", render.CleanText(*value.CarParking))
	}
	printStationBool("CCTV", value.CCTV)
}

func renderStationAccessibility(value *stationAccessibilityOutput) {
	if value == nil {
		return
	}
	fmt.Println("\nAccessibility")
	printStationBool("Accessible stop", value.StopAccessible)
	if value.PlatformNumber != nil {
		fmt.Printf("  Platform: %d\n", *value.PlatformNumber)
	}
	printStationBool("Lighting", value.Lighting)
	printStationBool("Audio customer information", value.AudioCustomerInformation)
	printStationBool("Escalator", value.Escalator)
	printStationBool("Hearing loop", value.HearingLoop)
	printStationBool("Lift", value.Lift)
	printStationBool("Stairs", value.Stairs)
	printStationBool("Tactile ground surface indicator", value.TactileGroundSurfaceIndicator)
	printStationBool("Waiting room", value.WaitingRoom)
	if value.Wheelchair == nil {
		return
	}
	fmt.Println("  Wheelchair facilities")
	printStationBool("  Accessible ramp", value.Wheelchair.AccessibleRamp)
	printStationBool("  Parking", value.Wheelchair.Parking)
	printStationBool("  Telephone", value.Wheelchair.Telephone)
	printStationBool("  Toilet", value.Wheelchair.Toilet)
	printStationBool("  Low ticket counter", value.Wheelchair.LowTicketCounter)
	printStationBool("  Manoeuvring space", value.Wheelchair.Manoeuvring)
	printStationBool("  Raised platform", value.Wheelchair.RaisedPlatform)
	printStationBool("  Ramp", value.Wheelchair.Ramp)
	printStationBool("  Secondary path", value.Wheelchair.SecondaryPath)
	printStationBool("  Raised platform shelter", value.Wheelchair.RaisedPlatformShelter)
	printStationBool("  Steep ramp", value.Wheelchair.SteepRamp)
}

func renderStationStaffing(value *stationStaffingOutput) {
	if value == nil {
		return
	}
	fmt.Println("\nStaffing")
	days := []struct {
		name                       string
		amFrom, amTo, pmFrom, pmTo *string
	}{
		{"Monday", value.MondayAMFrom, value.MondayAMTo, value.MondayPMFrom, value.MondayPMTo},
		{"Tuesday", value.TuesdayAMFrom, value.TuesdayAMTo, value.TuesdayPMFrom, value.TuesdayPMTo},
		{"Wednesday", value.WednesdayAMFrom, value.WednesdayAMTo, value.WednesdayPMFrom, value.WednesdayPMTo},
		{"Thursday", value.ThursdayAMFrom, value.ThursdayAMTo, value.ThursdayPMFrom, value.ThursdayPMTo},
		{"Friday", value.FridayAMFrom, value.FridayAMTo, value.FridayPMFrom, value.FridayPMTo},
		{"Saturday", value.SaturdayAMFrom, value.SaturdayAMTo, value.SaturdayPMFrom, value.SaturdayPMTo},
		{"Sunday", value.SundayAMFrom, value.SundayAMTo, value.SundayPMFrom, value.SundayPMTo},
	}
	for _, day := range days {
		if period := stationTimeRange(day.amFrom, day.amTo); period != "" {
			fmt.Printf("  %s AM: %s\n", day.name, render.CleanText(period))
		}
		if period := stationTimeRange(day.pmFrom, day.pmTo); period != "" {
			fmt.Printf("  %s PM: %s\n", day.name, render.CleanText(period))
		}
	}
	if period := stationTimeRange(value.PublicHolidayFrom, value.PublicHolidayTo); period != "" {
		fmt.Printf("  Public holiday: %s\n", render.CleanText(period))
	}
	if value.PublicHolidayAdditionalText != nil {
		fmt.Printf("  Public holiday note: %s\n", render.CleanText(*value.PublicHolidayAdditionalText))
	}
}

func renderStationDisruptions(disruptions map[string]stationDisruptionOutput) {
	if len(disruptions) == 0 {
		return
	}
	fmt.Println("\nDisruptions")
	keys := make([]string, 0, len(disruptions))
	for key := range disruptions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		disruption := disruptions[key]
		title := disruption.Title
		if title == "" {
			title = fmt.Sprintf("Disruption %d", disruption.DisruptionID)
		}
		qualifiers := make([]string, 0, 2)
		if disruption.DisruptionStatus != "" {
			qualifiers = append(qualifiers, disruption.DisruptionStatus)
		}
		if disruption.DisruptionType != "" {
			qualifiers = append(qualifiers, disruption.DisruptionType)
		}
		if len(qualifiers) > 0 {
			fmt.Printf("  %s (%s)\n", render.CleanText(title), render.CleanText(strings.Join(qualifiers, ", ")))
		} else {
			fmt.Printf("  %s\n", render.CleanText(title))
		}
		printStationText("    Description", disruption.Description)
		printStationText("    From", disruption.FromDate)
		if disruption.ToDate != nil {
			printStationText("    To", *disruption.ToDate)
		}
		printStationText("    Published", disruption.PublishedOn)
		printStationText("    Updated", disruption.LastUpdated)
		printStationText("    URL", disruption.URL)
	}
}

func printStationBool(label string, value *bool) {
	if value == nil {
		return
	}
	answer := "No"
	if *value {
		answer = "Yes"
	}
	fmt.Printf("  %s: %s\n", label, answer)
}

func printStationText(label, value string) {
	if value != "" {
		fmt.Printf("%s: %s\n", label, render.CleanText(value))
	}
}

func stationTimeRange(from, to *string) string {
	switch {
	case from != nil && to != nil:
		return *from + "-" + *to
	case from != nil:
		return "from " + *from
	case to != nil:
		return "until " + *to
	default:
		return ""
	}
}

func formatStationCoordinate(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func stationText(value string) string {
	return strings.TrimSpace(value)
}

func stationString(value *string) *string {
	if value == nil {
		return nil
	}
	cleaned := stationText(*value)
	if cleaned == "" {
		return nil
	}
	return &cleaned
}

func stationAccessibilityPresent(value *stationAccessibilityOutput) bool {
	return value.Lighting != nil || value.PlatformNumber != nil || value.AudioCustomerInformation != nil ||
		value.Escalator != nil || value.HearingLoop != nil || value.Lift != nil || value.Stairs != nil ||
		value.StopAccessible != nil || value.TactileGroundSurfaceIndicator != nil || value.WaitingRoom != nil ||
		value.Wheelchair != nil
}

func stationWheelchairAccessibilityPresent(value *stationWheelchairAccessibilityOutput) bool {
	return value.AccessibleRamp != nil || value.Parking != nil || value.Telephone != nil || value.Toilet != nil ||
		value.LowTicketCounter != nil || value.Manoeuvring != nil || value.RaisedPlatform != nil ||
		value.Ramp != nil || value.SecondaryPath != nil || value.RaisedPlatformShelter != nil || value.SteepRamp != nil
}

func stationStaffingPresent(value *stationStaffingOutput) bool {
	values := []*string{
		value.MondayAMFrom, value.MondayAMTo, value.MondayPMFrom, value.MondayPMTo,
		value.TuesdayAMFrom, value.TuesdayAMTo, value.TuesdayPMFrom, value.TuesdayPMTo,
		value.WednesdayAMFrom, value.WednesdayAMTo, value.WednesdayPMFrom, value.WednesdayPMTo,
		value.ThursdayAMFrom, value.ThursdayAMTo, value.ThursdayPMFrom, value.ThursdayPMTo,
		value.FridayAMFrom, value.FridayAMTo, value.FridayPMFrom, value.FridayPMTo,
		value.SaturdayAMFrom, value.SaturdayAMTo, value.SaturdayPMFrom, value.SaturdayPMTo,
		value.SundayAMFrom, value.SundayAMTo, value.SundayPMFrom, value.SundayPMTo,
		value.PublicHolidayFrom, value.PublicHolidayTo, value.PublicHolidayAdditionalText,
	}
	for _, item := range values {
		if item != nil {
			return true
		}
	}
	return false
}

// deduplicateRoutes removes duplicate routes by route_id, keeping the first
// occurrence. The PTV API sometimes returns the same route entry twice (once
// per direction) for stations served by multiple V/Line routes.
func deduplicateRoutes(routes []ptvapi.Route) []ptvapi.Route {
	seen := make(map[int]bool, len(routes))
	out := make([]ptvapi.Route, 0, len(routes))
	for _, route := range routes {
		if seen[route.RouteID] {
			continue
		}
		seen[route.RouteID] = true
		out = append(out, route)
	}
	return out
}

func init() {
	stationCmd.Flags().StringVar(&stationMode, "mode", "", "mode hint when passing a numeric stop id")
	rootCmd.AddCommand(stationCmd)
}
