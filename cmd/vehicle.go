package cmd

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	gtfs "github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"github.com/spf13/cobra"
	"github.com/thesammykins/ptv_cli/internal/gtfsrt"
	"github.com/thesammykins/ptv_cli/internal/ptvapi"
	"github.com/thesammykins/ptv_cli/internal/render"
)

var vehicleScanRoutes int
var vehicleStop string
var vehicleRoute string

var vehicleCmd = &cobra.Command{
	Use:     "vehicle <vehicle-id|run-ref>",
	Aliases: []string{"vehicles"},
	Short:   "Find the best available live information for a vehicle",
	Long: `Find the best available live information for a vehicle.

The argument is treated as a physical vehicle id first. For Metro trains,
PTV exposes a consist string such as "113M-114M-1357T-1422T-243M-244M";
you can search either the full consist string or one component such as
"243M". For trams, PTV may expose the tram number (for example "6059")
from some departure contexts. For buses, optional Transport Victoria GTFS
Realtime can match a physical bus id/label or enrich a PTV run_ref when
PTV_OPENDATA_KEY_ID is configured, with PTV_OPENDATA_API_ID when required by
your Open Data account. If no vehicle descriptor matches, ptv falls back to
trying the argument as a PTV run_ref across all route types.

Recommended usage is to provide where the vehicle was seen:

  ptv vehicle 243M --stop Mordialloc --route Frankston
  ptv vehicle 6059 --stop "Melbourne Central Station"
  ptv vehicle 952377 --json
  ptv vehicles BS11ZU --stop Chadstone

Shortfalls and caveats:

  * PTV has no direct "find vehicle by id" endpoint. The command searches
    expanded departure/run data where PTV happens to attach vehicle_descriptor
    and vehicle_position fields.
  * Metro train descriptors are available for many active services, but new,
    testing, commissioning, non-revenue or not-currently-running sets may not
    appear. External fleet databases can confirm a vehicle exists, but they do
    not prove it is visible in the PTV live API.
  * Tram descriptors are context-sensitive: some busy stop departure queries
    expose tram numbers, while route-filtered scans may not.
  * Bus and V/Line descriptors are often absent in the PTV Timetable API. With
    Transport Victoria GTFS Realtime credentials, bus lookups can also use the
    official bus vehicle-position feed for vehicle id, trip id, position,
    occupancy and status.
  * With --stop/--route, earlier departures can produce a "last_spotted"
    result. That means the vehicle appeared in prior PTV departure data for
    that stop/route but does not appear in upcoming departures there now.
  * --scan-routes is an explicit slow fallback for active route-run scans; keep
    it bounded unless you deliberately want broader network probing.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, cfg, err := loadClient()
		if err != nil {
			return err
		}

		query := strings.TrimSpace(args[0])
		result, err := lookupVehicleWithHints(ctx(), client, query, vehicleLookupHints{
			Stop:           vehicleStop,
			Route:          vehicleRoute,
			RouteScanLimit: orDefault(vehicleScanRoutes, 0),
		})
		if err != nil {
			return err
		}
		if cfg.OpenDataKeyID != "" {
			result = enrichWithGTFSRealtimeBus(ctx(), gtfsrt.New(cfg.OpenDataKeyID, cfg.OpenDataAPIID), cfg.GTFSRealtimeBusVehiclePositionsURL, query, result)
		} else if shouldMentionGTFSRealtimeBus(result) {
			result.Warnings = append(result.Warnings, "GTFS Realtime bus enrichment skipped; set PTV_OPENDATA_KEY_ID to enable Transport Victoria Open Data vehicle positions")
		}
		if flagJSON {
			return printJSON(result)
		}
		printVehicleResult(result)
		return nil
	},
}

type concreteVehicleClient interface {
	vehicleLookupClient
	Departures(ctx context.Context, routeType, stopID int, opts ptvapi.DeparturesOptions) (*ptvapi.DeparturesResponse, error)
	Search(ctx context.Context, term string, routeTypes []int) (*ptvapi.SearchResult, error)
}

type gtfsRealtimeVehicleClient interface {
	FetchVehiclePositions(ctx context.Context, feedURL string) (*gtfs.FeedMessage, error)
}

type vehicleLookupClient interface {
	Routes(ctx context.Context, routeTypes []int, name string) (*ptvapi.RouteResponse, error)
	RunsForRoute(ctx context.Context, routeID, routeType int, opts ptvapi.RunsOptions) (*ptvapi.RunsResponse, error)
	Run(ctx context.Context, runRef string, routeType int, opts ptvapi.RunsOptions) (*ptvapi.RunResponse, error)
	Pattern(ctx context.Context, runRef string, routeType int, opts ptvapi.PatternOptions) (*ptvapi.StoppingPatternResponse, error)
}

type vehicleLookupHints struct {
	Stop           string
	Route          string
	RouteScanLimit int
}

type vehicleResult struct {
	Query          string                    `json:"query"`
	MatchedBy      string                    `json:"matched_by"`
	VehicleID      string                    `json:"vehicle_id,omitempty"`
	RouteType      *int                      `json:"route_type,omitempty"`
	Mode           string                    `json:"mode,omitempty"`
	RouteID        int                       `json:"route_id,omitempty"`
	Route          string                    `json:"route,omitempty"`
	RunRef         string                    `json:"run_ref,omitempty"`
	Destination    string                    `json:"destination,omitempty"`
	Status         string                    `json:"status,omitempty"`
	ServiceState   string                    `json:"service_state,omitempty"`
	Position       *vehiclePositionResult    `json:"position,omitempty"`
	Descriptor     *ptvapi.VehicleDescriptor `json:"vehicle_descriptor,omitempty"`
	GTFSRealtime   *vehicleGTFSRealtime      `json:"gtfs_realtime,omitempty"`
	NextStop       *vehicleStopResult        `json:"next_stop,omitempty"`
	LastSeen       *vehicleStopResult        `json:"last_seen,omitempty"`
	PositionSource string                    `json:"position_source,omitempty"`
	Warnings       []string                  `json:"warnings,omitempty"`
}

type vehicleGTFSRealtime struct {
	Source          string                 `json:"source"`
	EntityID        string                 `json:"entity_id,omitempty"`
	TripID          string                 `json:"trip_id,omitempty"`
	RouteID         string                 `json:"route_id,omitempty"`
	StartDate       string                 `json:"start_date,omitempty"`
	StartTime       string                 `json:"start_time,omitempty"`
	VehicleID       string                 `json:"vehicle_id,omitempty"`
	Label           string                 `json:"label,omitempty"`
	LicensePlate    string                 `json:"license_plate,omitempty"`
	StopID          string                 `json:"stop_id,omitempty"`
	CurrentStatus   string                 `json:"current_status,omitempty"`
	OccupancyStatus string                 `json:"occupancy_status,omitempty"`
	TimestampUTC    string                 `json:"timestamp_utc,omitempty"`
	Position        *vehiclePositionResult `json:"position,omitempty"`
}

type vehiclePositionResult struct {
	Latitude    *float64 `json:"latitude,omitempty"`
	Longitude   *float64 `json:"longitude,omitempty"`
	Easting     *float64 `json:"easting,omitempty"`
	Northing    *float64 `json:"northing,omitempty"`
	Direction   string   `json:"direction,omitempty"`
	Bearing     *float64 `json:"bearing,omitempty"`
	Supplier    string   `json:"supplier,omitempty"`
	DatetimeUTC string   `json:"datetime_utc,omitempty"`
	ExpiryTime  string   `json:"expiry_time,omitempty"`
	Kind        string   `json:"kind"`
}

type vehicleStopResult struct {
	StopID                int     `json:"stop_id"`
	StopName              string  `json:"stop_name"`
	StopLatitude          float64 `json:"stop_latitude,omitempty"`
	StopLongitude         float64 `json:"stop_longitude,omitempty"`
	ScheduledDepartureUTC string  `json:"scheduled_departure_utc,omitempty"`
	EstimatedDepartureUTC string  `json:"estimated_departure_utc,omitempty"`
	DepartureSequence     int     `json:"departure_sequence,omitempty"`
}

type routeRun struct {
	route ptvapi.Route
	run   ptvapi.Run
}

var errVehicleNotFound = errors.New("vehicle not found")

func lookupVehicleWithHints(ctx context.Context, client concreteVehicleClient, query string, hints vehicleLookupHints) (*vehicleResult, error) {
	if strings.TrimSpace(hints.Stop) != "" {
		if result, err := lookupVehicleAtStop(ctx, client, query, hints); err == nil {
			return result, nil
		} else if !errors.Is(err, errVehicleNotFound) {
			return nil, err
		}
		if hints.RouteScanLimit == 0 {
			if result, err := lookupRunRef(ctx, client, query); err == nil {
				result.Warnings = append(result.Warnings, "no physical vehicle match was found at the hinted stop/route")
				return result, nil
			} else if !errors.Is(err, errVehicleNotFound) {
				return nil, err
			}
			return hintedVehicleNotFound(query), nil
		}
	}
	return lookupVehicle(ctx, client, query, hints.RouteScanLimit)
}

func lookupVehicle(ctx context.Context, client vehicleLookupClient, query string, routeScanLimit int) (*vehicleResult, error) {
	if query == "" {
		return nil, fmt.Errorf("vehicle id is required")
	}

	if routeScanLimit > 0 {
		if result, err := lookupVehicleDescriptor(ctx, client, query, routeScanLimit); err == nil {
			return result, nil
		} else if !errors.Is(err, errVehicleNotFound) {
			return nil, err
		}
	}

	if result, err := lookupRunRef(ctx, client, query); err == nil {
		return result, nil
	} else if !errors.Is(err, errVehicleNotFound) {
		return nil, err
	}

	return &vehicleResult{
		Query:     query,
		MatchedBy: "none",
		Warnings: []string{
			"no active PTV run exposes this physical vehicle id",
			"PTV has no direct vehicle-id lookup; pass --stop/--route hints or --scan-routes to search active route runs",
		},
	}, nil
}

func enrichWithGTFSRealtimeBus(ctx context.Context, client gtfsRealtimeVehicleClient, feedURL, query string, result *vehicleResult) *vehicleResult {
	if result == nil || !shouldMentionGTFSRealtimeBus(result) {
		return result
	}
	feed, err := client.FetchVehiclePositions(ctx, feedURL)
	if err != nil {
		result.Warnings = append(result.Warnings, "GTFS Realtime bus enrichment failed: "+err.Error())
		return result
	}

	var vehicle *gtfsrt.Vehicle
	if result.RunRef != "" {
		vehicle = gtfsrt.FindByRunRef(feed, result.RunRef)
	}
	if vehicle == nil {
		vehicle = gtfsrt.FindByVehicleID(feed, query)
	}
	if vehicle == nil {
		result.Warnings = append(result.Warnings, "GTFS Realtime bus feed did not expose a matching vehicle position")
		return result
	}

	result.GTFSRealtime = gtfsRealtimeFromVehicle(*vehicle)
	if result.MatchedBy == "none" {
		result.MatchedBy = "gtfs_realtime.vehicle"
		result.RouteType = intPtr(2)
		result.Mode = routeTypeName(2)
		result.RunRef = vehicle.TripID
		result.Route = vehicle.RouteID
		result.ServiceState = "current"
		result.Status = strings.TrimPrefix(vehicle.CurrentStatus, "VEHICLE_STOP_STATUS_")
		result.Warnings = nil
	}
	if result.Position == nil && result.GTFSRealtime.Position != nil {
		result.Position = result.GTFSRealtime.Position
		result.PositionSource = "GTFS Realtime bus vehicle position"
	}
	if result.VehicleID == "" {
		result.VehicleID = firstNonEmptyString(vehicle.Label, vehicle.VehicleID, vehicle.LicensePlate)
	}
	return result
}

func shouldMentionGTFSRealtimeBus(result *vehicleResult) bool {
	if result == nil {
		return false
	}
	if result.RouteType != nil && *result.RouteType == 2 {
		return true
	}
	return result.MatchedBy == "none"
}

func gtfsRealtimeFromVehicle(vehicle gtfsrt.Vehicle) *vehicleGTFSRealtime {
	result := &vehicleGTFSRealtime{
		Source:          "Transport Victoria GTFS Realtime bus vehicle positions",
		EntityID:        vehicle.EntityID,
		TripID:          vehicle.TripID,
		RouteID:         vehicle.RouteID,
		StartDate:       vehicle.StartDate,
		StartTime:       vehicle.StartTime,
		VehicleID:       vehicle.VehicleID,
		Label:           vehicle.Label,
		LicensePlate:    vehicle.LicensePlate,
		StopID:          vehicle.StopID,
		CurrentStatus:   vehicle.CurrentStatus,
		OccupancyStatus: vehicle.OccupancyStatus,
		TimestampUTC:    vehicle.TimestampUTC,
	}
	if vehicle.Latitude != nil && vehicle.Longitude != nil {
		result.Position = &vehiclePositionResult{
			Latitude:    vehicle.Latitude,
			Longitude:   vehicle.Longitude,
			Bearing:     vehicle.Bearing,
			DatetimeUTC: vehicle.TimestampUTC,
			Kind:        "gps",
		}
	}
	return result
}

func firstNonEmptyString(vals ...string) string {
	for _, val := range vals {
		if strings.TrimSpace(val) != "" {
			return val
		}
	}
	return ""
}

func hintedVehicleNotFound(query string) *vehicleResult {
	return &vehicleResult{
		Query:     query,
		MatchedBy: "none",
		Warnings: []string{
			"no current or earlier PTV departure at the hinted stop/route exposes this physical vehicle id",
			"it does not appear to be currently in service on that line from the available PTV data",
			"skipped all-network scanning to keep this lookup fast; use --scan-routes to opt in",
		},
	}
}

func lookupVehicleAtStop(ctx context.Context, client concreteVehicleClient, query string, hints vehicleLookupHints) (*vehicleResult, error) {
	stop, err := resolveVehicleStop(ctx, client, hints.Stop)
	if err != nil {
		return nil, err
	}
	route, err := resolveVehicleRouteHint(ctx, client, hints.Route, stop.RouteType)
	if err != nil {
		return nil, err
	}

	routeType := stop.RouteType
	if route != nil {
		routeType = route.RouteType
	}
	if routeType < 0 {
		for _, probeRouteType := range routeTypesToProbe() {
			if result, err := lookupVehicleInStopDepartures(ctx, client, query, stop, routeWithType(route, probeRouteType), probeRouteType, false); err == nil {
				return result, nil
			} else if !errors.Is(err, errVehicleNotFound) {
				continue
			}
		}
		for _, probeRouteType := range routeTypesToProbe() {
			if result, err := lookupVehicleInStopDepartures(ctx, client, query, stop, routeWithType(route, probeRouteType), probeRouteType, true); err == nil {
				return result, nil
			} else if !errors.Is(err, errVehicleNotFound) {
				continue
			}
		}
		return nil, errVehicleNotFound
	}

	if result, err := lookupVehicleInStopDepartures(ctx, client, query, stop, route, routeType, false); err == nil {
		return result, nil
	} else if !errors.Is(err, errVehicleNotFound) {
		return nil, err
	}
	if result, err := lookupVehicleInStopDepartures(ctx, client, query, stop, route, routeType, true); err == nil {
		return result, nil
	} else if !errors.Is(err, errVehicleNotFound) {
		return nil, err
	}
	if route != nil {
		return nil, errVehicleNotFound
	}
	for _, probeRouteType := range routeTypesToProbe() {
		if probeRouteType == routeType {
			continue
		}
		if result, err := lookupVehicleInStopDepartures(ctx, client, query, stop, nil, probeRouteType, false); err == nil {
			return result, nil
		} else if !errors.Is(err, errVehicleNotFound) {
			continue
		}
	}
	for _, probeRouteType := range routeTypesToProbe() {
		if probeRouteType == routeType {
			continue
		}
		if result, err := lookupVehicleInStopDepartures(ctx, client, query, stop, nil, probeRouteType, true); err == nil {
			return result, nil
		} else if !errors.Is(err, errVehicleNotFound) {
			continue
		}
	}
	return nil, errVehicleNotFound
}

func lookupVehicleInStopDepartures(ctx context.Context, client concreteVehicleClient, query string, stop *ptvapi.StopModel, route *ptvapi.Route, routeType int, lookBackwards bool) (*vehicleResult, error) {
	resp, err := client.Departures(ctx, routeType, stop.StopID, ptvapi.DeparturesOptions{
		RouteID:       routeIDOrZero(route),
		MaxResults:    20,
		LookBackwards: lookBackwards,
		Expand: []string{
			ptvapi.ExpandStop,
			ptvapi.ExpandRoute,
			ptvapi.ExpandRun,
			ptvapi.ExpandDirection,
			ptvapi.ExpandDepartureVehiclePosition,
			ptvapi.ExpandDepartureVehicleDescriptor,
		},
	})
	if err != nil {
		return nil, err
	}

	var matches []vehicleDepartureMatch
	for runRef, run := range resp.Runs {
		matchKind := ""
		if vehicleDescriptorMatches(run.VehicleDescriptor, query) {
			matchKind = "vehicle_descriptor.id"
		} else if runRefMatches(runRef, run.RunRef, query) {
			matchKind = "run_ref"
		}
		if matchKind != "" {
			matches = append(matches, vehicleDepartureMatch{run: run, departure: departureForRun(resp.Departures, runRef), matchKind: matchKind})
		}
	}
	if len(matches) == 0 {
		return nil, errVehicleNotFound
	}
	stop = stopFromDepartures(resp, stop)
	sort.Slice(matches, func(i, j int) bool {
		return departureSort(matches[i].departure) < departureSort(matches[j].departure)
	})

	match := matches[0]
	matchedRoute := routeFromDepartures(resp, match.run.RouteID, routeType)
	if route != nil && (route.RouteName != "" || route.RouteNumber != "") {
		matchedRoute = *route
	}
	result := resultFromRun("stop_departure."+match.matchKind, query, matchedRoute, match.run)
	if match.matchKind == "run_ref" {
		result.Warnings = append(result.Warnings, "matched a PTV run_ref from stop departures, not a physical vehicle id")
	}
	seen := vehicleStopFromDeparture(stop, match.departure)
	if lookBackwards {
		result.MatchedBy = "stop_departure_history." + match.matchKind
		result.ServiceState = "last_spotted"
		result.LastSeen = seen
		result.Warnings = append(result.Warnings, "vehicle was last spotted in earlier departures at the hinted stop")
		result.Warnings = append(result.Warnings, "it does not appear in upcoming departures for that stop/route right now")
	} else {
		result.ServiceState = "current"
		result.NextStop = seen
	}
	if len(matches) > 1 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("matched %d departures at %s; showing first", len(matches), stop.StopName))
	}
	attachNextStop(ctx, client, result)
	return result, nil
}

func stopFromDepartures(resp *ptvapi.DeparturesResponse, fallback *ptvapi.StopModel) *ptvapi.StopModel {
	if fallback == nil || resp == nil {
		return fallback
	}
	if stop, ok := resp.Stops[strconv.Itoa(fallback.StopID)]; ok {
		return &stop
	}
	return fallback
}

type vehicleDepartureMatch struct {
	run       ptvapi.Run
	departure ptvapi.Departure
	matchKind string
}

func resolveVehicleStop(ctx context.Context, client concreteVehicleClient, query string) (*ptvapi.StopModel, error) {
	query = strings.TrimSpace(query)
	if id, err := strconv.Atoi(query); err == nil {
		return &ptvapi.StopModel{StopID: id, RouteType: -1}, nil
	}
	resp, err := client.Search(ctx, query, nil)
	if err != nil {
		return nil, err
	}
	if len(resp.Stops) == 0 {
		return nil, fmt.Errorf("no stop matching %q", query)
	}
	return chooseVehicleStop(query, resp.Stops), nil
}

func chooseVehicleStop(query string, stops []ptvapi.StopModel) *ptvapi.StopModel {
	stationName := strings.TrimSpace(query)
	if !strings.Contains(strings.ToLower(stationName), "station") {
		stationName += " Station"
	}
	for i := range stops {
		if stops[i].RouteType == 0 && strings.EqualFold(stops[i].StopName, stationName) {
			return &stops[i]
		}
	}
	for i := range stops {
		if stops[i].RouteType == 0 && strings.Contains(strings.ToLower(stops[i].StopName), " station") {
			return &stops[i]
		}
	}
	return chooseStop(query, stops)
}

func resolveVehicleRouteHint(ctx context.Context, client concreteVehicleClient, query string, routeType int) (*ptvapi.Route, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if id, err := strconv.Atoi(query); err == nil {
		return &ptvapi.Route{RouteID: id, RouteType: routeType}, nil
	}
	var routeTypes []int
	if routeType >= 0 {
		routeTypes = []int{routeType}
	}
	resp, err := client.Routes(ctx, routeTypes, "")
	if err != nil {
		return nil, err
	}
	routes := resp.Routes
	for i := range routes {
		if strings.EqualFold(routes[i].RouteName, query) || strings.EqualFold(routes[i].RouteNumber, query) {
			return &routes[i], nil
		}
	}
	lower := strings.ToLower(query)
	var matches []ptvapi.Route
	for _, route := range routes {
		if strings.Contains(strings.ToLower(route.RouteName), lower) || strings.Contains(strings.ToLower(route.RouteNumber), lower) {
			matches = append(matches, route)
		}
	}
	if len(matches) == 1 {
		return &matches[0], nil
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("route %q is ambiguous", query)
	}
	return nil, fmt.Errorf("no route matching %q", query)
}

func routeIDOrZero(route *ptvapi.Route) int {
	if route == nil {
		return 0
	}
	return route.RouteID
}

func routeWithType(route *ptvapi.Route, routeType int) *ptvapi.Route {
	if route == nil {
		return &ptvapi.Route{RouteType: routeType}
	}
	copy := *route
	copy.RouteType = routeType
	return &copy
}

func routeFromDepartures(resp *ptvapi.DeparturesResponse, routeID, routeType int) ptvapi.Route {
	if resp != nil {
		if route, ok := resp.Routes[strconv.Itoa(routeID)]; ok {
			return route
		}
	}
	return ptvapi.Route{RouteID: routeID, RouteType: routeType}
}

func departureForRun(departures []ptvapi.Departure, runRef string) ptvapi.Departure {
	for _, departure := range departures {
		if departure.RunRef == runRef {
			return departure
		}
	}
	return ptvapi.Departure{RunRef: runRef}
}

func vehicleStopFromDeparture(stop *ptvapi.StopModel, departure ptvapi.Departure) *vehicleStopResult {
	if stop == nil {
		return nil
	}
	result := &vehicleStopResult{
		StopID:            stop.StopID,
		StopName:          stop.StopName,
		StopLatitude:      stop.StopLatitude,
		StopLongitude:     stop.StopLongitude,
		DepartureSequence: departure.DepartureSequence,
	}
	if result.StopName == "" {
		result.StopName = fmt.Sprintf("Stop %d", stop.StopID)
	}
	if departure.ScheduledDepartureUTC != nil {
		result.ScheduledDepartureUTC = *departure.ScheduledDepartureUTC
	}
	if departure.EstimatedDepartureUTC != nil {
		result.EstimatedDepartureUTC = *departure.EstimatedDepartureUTC
	}
	return result
}

func lookupVehicleDescriptor(ctx context.Context, client vehicleLookupClient, query string, routeScanLimit int) (*vehicleResult, error) {
	matches, err := scanRunsForVehicleID(ctx, client, query, routeScanLimit)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, errVehicleNotFound
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].run.RouteType < matches[j].run.RouteType ||
			(matches[i].run.RouteType == matches[j].run.RouteType && matches[i].run.RouteID < matches[j].run.RouteID)
	})

	result := resultFromRun("vehicle_descriptor.id", query, matches[0].route, matches[0].run)
	if len(matches) > 1 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("matched %d active runs; showing first", len(matches)))
	}
	attachNextStop(ctx, client, result)
	return result, nil
}

func lookupRunRef(ctx context.Context, client vehicleLookupClient, query string) (*vehicleResult, error) {
	var lastErr error
	for _, routeType := range routeTypesToProbe() {
		resp, err := client.Run(ctx, query, routeType, vehicleRunsOptions())
		if err != nil {
			lastErr = err
			continue
		}
		if resp.Run.RunRef == "" {
			continue
		}
		result := resultFromRun("run_ref", query, ptvapi.Route{RouteID: resp.Run.RouteID, RouteType: routeType}, resp.Run)
		result.Warnings = append(result.Warnings, "matched a PTV run_ref, not a physical vehicle id")
		attachNextStop(ctx, client, result)
		return result, nil
	}
	if lastErr != nil {
		return nil, errVehicleNotFound
	}
	return nil, errVehicleNotFound
}

func scanRunsForVehicleID(ctx context.Context, client vehicleLookupClient, query string, routeScanLimit int) ([]routeRun, error) {
	routes, err := allRoutes(ctx, client, routeScanLimit)
	if err != nil {
		return nil, err
	}

	var matches []routeRun
	for _, route := range routes {
		resp, err := client.RunsForRoute(ctx, route.RouteID, route.RouteType, vehicleRunsOptions())
		if err != nil {
			continue
		}
		for _, run := range resp.Runs {
			if vehicleDescriptorMatches(run.VehicleDescriptor, query) {
				matches = append(matches, routeRun{route: route, run: run})
			}
		}
	}
	return matches, nil
}

func vehicleDescriptorMatches(desc *ptvapi.VehicleDescriptor, query string) bool {
	if desc == nil || strings.TrimSpace(query) == "" {
		return false
	}
	query = strings.ToLower(strings.TrimSpace(query))
	for _, part := range vehicleDescriptorParts(desc.ID) {
		if strings.ToLower(part) == query {
			return true
		}
	}
	return false
}

func runRefMatches(mapKey, runRef, query string) bool {
	query = strings.TrimSpace(query)
	return query != "" && (strings.EqualFold(strings.TrimSpace(mapKey), query) || strings.EqualFold(strings.TrimSpace(runRef), query))
}

func vehicleDescriptorParts(id string) []string {
	fields := strings.FieldsFunc(id, func(r rune) bool {
		return r == '-' || r == ',' || r == '/' || r == ' '
	})
	out := make([]string, 0, len(fields)+1)
	if strings.TrimSpace(id) != "" {
		out = append(out, strings.TrimSpace(id))
	}
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field != "" {
			out = append(out, field)
		}
	}
	return out
}

func allRoutes(ctx context.Context, client vehicleLookupClient, limit int) ([]ptvapi.Route, error) {
	var routes []ptvapi.Route
	for _, routeType := range routeTypesToProbe() {
		resp, err := client.Routes(ctx, []int{routeType}, "")
		if err != nil {
			return nil, fmt.Errorf("loading %s routes: %w", strings.ToLower(routeTypeName(routeType)), err)
		}
		routes = append(routes, resp.Routes...)
		if limit > 0 && len(routes) >= limit {
			return routes[:limit], nil
		}
	}
	return routes, nil
}

func resultFromRun(matchedBy, query string, route ptvapi.Route, run ptvapi.Run) *vehicleResult {
	if run.RouteType != 0 {
		route.RouteType = run.RouteType
	}
	if route.RouteID == 0 {
		route.RouteID = run.RouteID
	}

	result := &vehicleResult{
		Query:        query,
		MatchedBy:    matchedBy,
		RouteType:    intPtr(route.RouteType),
		Mode:         routeTypeName(route.RouteType),
		RouteID:      route.RouteID,
		Route:        routeLabelForVehicle(route),
		RunRef:       run.RunRef,
		Destination:  run.DestinationName,
		Status:       run.Status,
		ServiceState: "current",
		Descriptor:   run.VehicleDescriptor,
	}
	if run.VehicleDescriptor != nil && run.VehicleDescriptor.ID != "" {
		result.VehicleID = run.VehicleDescriptor.ID
	}
	if result.VehicleID == "" && matchedBy == "vehicle_descriptor.id" {
		result.VehicleID = query
	}
	if run.VehiclePosition != nil {
		result.Position = vehiclePositionFromPTV(run.VehiclePosition)
		result.PositionSource = "ptv vehicle_position"
	}
	return result
}

func intPtr(v int) *int {
	return &v
}

func vehiclePositionFromPTV(pos *ptvapi.VehiclePosition) *vehiclePositionResult {
	result := &vehiclePositionResult{
		Latitude:    pos.Latitude,
		Longitude:   pos.Longitude,
		Easting:     pos.Easting,
		Northing:    pos.Northing,
		Direction:   pos.Direction,
		Bearing:     pos.Bearing,
		Supplier:    pos.Supplier,
		DatetimeUTC: pos.DatetimeUTC,
		ExpiryTime:  pos.ExpiryTime,
		Kind:        "unknown",
	}
	if pos.Latitude != nil && pos.Longitude != nil {
		result.Kind = "gps"
	} else if pos.Easting != nil && pos.Northing != nil {
		result.Kind = "grid"
	}
	return result
}

func attachNextStop(ctx context.Context, client vehicleLookupClient, result *vehicleResult) {
	if result.RunRef == "" {
		return
	}
	if result.RouteType == nil {
		return
	}
	pattern, err := client.Pattern(ctx, result.RunRef, *result.RouteType, ptvapi.PatternOptions{Expand: []string{ptvapi.ExpandStop}})
	if err != nil {
		result.Warnings = append(result.Warnings, "could not load stopping pattern for next stop estimate")
		return
	}
	stop := nextStopFromPattern(pattern)
	if stop != nil {
		if result.NextStop == nil {
			result.NextStop = stop
		}
		if result.Position == nil {
			result.PositionSource = "next stop estimate"
		}
	}
}

func nextStopFromPattern(pattern *ptvapi.StoppingPatternResponse) *vehicleStopResult {
	if pattern == nil || len(pattern.Departures) == 0 {
		return nil
	}
	now := time.Now()
	deps := append([]ptvapi.PatternDeparture(nil), pattern.Departures...)
	sort.Slice(deps, func(i, j int) bool {
		return departureSort(deps[i].Departure) < departureSort(deps[j].Departure)
	})
	for _, dep := range deps {
		when, _ := departureTime(dep.Departure)
		if !when.IsZero() && when.Before(now.Add(-2*time.Minute)) {
			continue
		}
		stop := vehicleStopResult{StopID: dep.StopID, DepartureSequence: dep.DepartureSequence}
		if s, ok := pattern.Stops[strconv.Itoa(dep.StopID)]; ok {
			stop.StopName = s.StopName
			stop.StopLatitude = s.StopLatitude
			stop.StopLongitude = s.StopLongitude
		}
		if stop.StopName == "" {
			stop.StopName = fmt.Sprintf("Stop %d", dep.StopID)
		}
		if dep.ScheduledDepartureUTC != nil {
			stop.ScheduledDepartureUTC = *dep.ScheduledDepartureUTC
		}
		if dep.EstimatedDepartureUTC != nil {
			stop.EstimatedDepartureUTC = *dep.EstimatedDepartureUTC
		}
		return &stop
	}
	return nil
}

func vehicleRunsOptions() ptvapi.RunsOptions {
	return ptvapi.RunsOptions{Expand: []string{ptvapi.ExpandVehiclePosition, ptvapi.ExpandVehicleDescriptor}}
}

func routeTypesToProbe() []int {
	return []int{0, 1, 2, 3, 4}
}

func routeLabelForVehicle(route ptvapi.Route) string {
	if route.RouteNumber != "" && route.RouteName != "" {
		return route.RouteNumber + " " + route.RouteName
	}
	if route.RouteNumber != "" {
		return route.RouteNumber
	}
	if route.RouteName != "" {
		return route.RouteName
	}
	if route.RouteID > 0 {
		return strconv.Itoa(route.RouteID)
	}
	return "-"
}

func printVehicleResult(result *vehicleResult) {
	if result.MatchedBy == "none" {
		fmt.Printf("Vehicle %s\n", render.CleanText(result.Query))
		fmt.Println("Status: not found")
		printVehicleWarnings(result.Warnings)
		return
	}

	name := result.VehicleID
	if name == "" {
		name = result.Query
	}
	fmt.Printf("Vehicle %s\n", render.CleanText(name))
	fmt.Printf("Mode: %s\n", render.CleanText(result.Mode))
	fmt.Printf("Matched by: %s\n", render.CleanText(result.MatchedBy))
	if result.Route != "" && result.Route != "-" {
		fmt.Printf("Route: %s\n", render.CleanText(result.Route))
	}
	if result.Destination != "" {
		fmt.Printf("Towards: %s\n", render.CleanText(result.Destination))
	}
	if result.RunRef != "" {
		fmt.Printf("Run ref: %s\n", render.CleanText(result.RunRef))
	}
	if result.ServiceState != "" {
		fmt.Printf("Service state: %s\n", render.CleanText(strings.ReplaceAll(result.ServiceState, "_", " ")))
	}
	if result.Status != "" {
		fmt.Printf("Status: %s\n", render.CleanText(result.Status))
	}
	printVehicleDescriptor(result.Descriptor)
	printVehiclePosition(result.Position, result.PositionSource)
	printVehicleGTFSRealtime(result.GTFSRealtime)
	printVehicleNextStop(result.NextStop)
	printVehicleLastSeen(result.LastSeen)
	printVehicleWarnings(result.Warnings)
}

func printVehicleDescriptor(desc *ptvapi.VehicleDescriptor) {
	if desc == nil {
		return
	}
	if desc.Operator != "" {
		fmt.Printf("Operator: %s\n", render.CleanText(desc.Operator))
	}
	if desc.Description != "" {
		fmt.Printf("Vehicle: %s\n", render.CleanText(desc.Description))
	}
	if desc.LowFloor != nil {
		fmt.Printf("Low floor: %t\n", *desc.LowFloor)
	}
	if desc.AirConditioned != nil {
		fmt.Printf("Air conditioned: %t\n", *desc.AirConditioned)
	}
}

func printVehiclePosition(pos *vehiclePositionResult, source string) {
	if pos == nil {
		if source != "" {
			fmt.Printf("Position: unavailable (%s)\n", render.CleanText(source))
		}
		return
	}
	if pos.Latitude != nil && pos.Longitude != nil {
		fmt.Printf("Position: %.6f, %.6f\n", *pos.Latitude, *pos.Longitude)
	} else if pos.Easting != nil && pos.Northing != nil {
		fmt.Printf("Position: easting %.2f, northing %.2f\n", *pos.Easting, *pos.Northing)
	} else {
		fmt.Println("Position: unavailable")
	}
	if pos.Direction != "" {
		fmt.Printf("Direction: %s\n", render.CleanText(pos.Direction))
	}
	if pos.Bearing != nil {
		fmt.Printf("Bearing: %.0f\n", *pos.Bearing)
	}
	if pos.DatetimeUTC != "" {
		fmt.Printf("Position time: %s\n", render.CleanText(formatVehicleTime(pos.DatetimeUTC)))
	}
	if source != "" {
		fmt.Printf("Source: %s\n", render.CleanText(source))
	}
}

func printVehicleGTFSRealtime(info *vehicleGTFSRealtime) {
	if info == nil {
		return
	}
	fmt.Println("GTFS Realtime")
	if info.VehicleID != "" {
		fmt.Printf("  Vehicle ID: %s\n", render.CleanText(info.VehicleID))
	}
	if info.Label != "" {
		fmt.Printf("  Label: %s\n", render.CleanText(info.Label))
	}
	if info.LicensePlate != "" {
		fmt.Printf("  Licence plate: %s\n", render.CleanText(info.LicensePlate))
	}
	if info.TripID != "" {
		fmt.Printf("  Trip ID: %s\n", render.CleanText(info.TripID))
	}
	if info.RouteID != "" {
		fmt.Printf("  GTFS route ID: %s\n", render.CleanText(info.RouteID))
	}
	if info.StopID != "" {
		fmt.Printf("  Current stop ID: %s\n", render.CleanText(info.StopID))
	}
	if info.CurrentStatus != "" {
		fmt.Printf("  Status: %s\n", render.CleanText(info.CurrentStatus))
	}
	if info.OccupancyStatus != "" {
		fmt.Printf("  Occupancy: %s\n", render.CleanText(info.OccupancyStatus))
	}
	if info.TimestampUTC != "" {
		fmt.Printf("  Timestamp: %s\n", render.CleanText(formatVehicleTime(info.TimestampUTC)))
	}
}

func printVehicleNextStop(stop *vehicleStopResult) {
	if stop == nil {
		return
	}
	when := stop.EstimatedDepartureUTC
	label := "est"
	if when == "" {
		when = stop.ScheduledDepartureUTC
		label = "scheduled"
	}
	if when != "" {
		fmt.Printf("Next stop: %s (%s %s)\n", render.CleanText(stop.StopName), label, render.CleanText(formatLocal(when)))
		return
	}
	fmt.Printf("Next stop: %s\n", render.CleanText(stop.StopName))
}

func printVehicleLastSeen(stop *vehicleStopResult) {
	if stop == nil {
		return
	}
	when := stop.EstimatedDepartureUTC
	label := "est"
	if when == "" {
		when = stop.ScheduledDepartureUTC
		label = "scheduled"
	}
	if when != "" {
		fmt.Printf("Last spotted: %s (%s %s)\n", render.CleanText(stop.StopName), label, render.CleanText(formatLocal(when)))
		return
	}
	fmt.Printf("Last spotted: %s\n", render.CleanText(stop.StopName))
}

func printVehicleWarnings(warnings []string) {
	if len(warnings) == 0 {
		return
	}
	fmt.Println("\nWarnings")
	for _, warning := range warnings {
		fmt.Printf("  - %s\n", render.CleanText(warning))
	}
}

func formatVehicleTime(utc string) string {
	t, err := time.Parse(time.RFC3339, utc)
	if err != nil {
		return utc
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

func init() {
	vehicleCmd.Flags().IntVar(&vehicleScanRoutes, "scan-routes", 0, "scan this many routes when matching physical vehicle ids without a stop hint (0 = disabled)")
	vehicleCmd.Flags().StringVar(&vehicleStop, "stop", "", "hint: stop or station where the vehicle was seen")
	vehicleCmd.Flags().StringVar(&vehicleRoute, "route", "", "hint: route or line where the vehicle was seen")
	rootCmd.AddCommand(vehicleCmd)
}
