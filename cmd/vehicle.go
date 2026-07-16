package cmd

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/thesammykins/ptv_cli/internal/config"
	"github.com/thesammykins/ptv_cli/internal/gtfsrt"
	"github.com/thesammykins/ptv_cli/internal/localtime"
	"github.com/thesammykins/ptv_cli/internal/ptvapi"
	"github.com/thesammykins/ptv_cli/internal/render"
)

var vehicleScanRoutes int
var vehicleStop string
var vehicleRoute string

const (
	vehiclePTVLookupBudget    = 30 * time.Second
	vehicleGTFSRealtimeBudget = 30 * time.Second
	vehicleMaxRouteScan       = 100
)

var vehicleCmd = &cobra.Command{
	Use:     "vehicle <vehicle-id|run-ref>",
	Aliases: []string{"vehicles"},
	Short:   "Find the best available live information for a vehicle",
	Long: `Find the best available live information for a vehicle.

The argument is treated as a physical vehicle id first. For Metro trains,
PTV exposes a consist string such as "113M-114M-1357T-1422T-243M-244M";
you can search either the full consist string or one component such as
"243M". For trams, PTV may expose the tram number (for example "6059")
from some departure contexts. Optional Transport Victoria GTFS Realtime can
match public train, tram, bus and V/Line vehicle labels
when Open Data credentials are configured with 'ptv auth opendata login' or
PTV_OPENDATA_KEY_ID. PTV run references and static GTFS trip/entity identifiers
are kept separate. If no vehicle descriptor matches, ptv falls back to trying
the argument as a PTV run_ref with the broad run endpoint.

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
    Transport Victoria GTFS Realtime credentials, train/tram/bus/VLine lookups
    can also use official vehicle-position feeds for public label, trip id,
    position, occupancy and status.
  * With --stop/--route, earlier departures can produce a "last_spotted"
    result. That means the vehicle appeared in prior PTV departure data for
    that stop/route but does not appear in upcoming departures there now.
  * --scan-routes is an explicit slow fallback for active route-run scans and
    accepts at most 100 routes.`,
	Args: func(cmd *cobra.Command, args []string) error {
		if err := cobra.ExactArgs(1)(cmd, args); err != nil {
			return err
		}
		return validateVehicleRouteScan(vehicleScanRoutes)
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		client, _, err := loadClient()
		if err != nil {
			return err
		}

		query := strings.TrimSpace(args[0])
		result, err := lookupVehicleWithinBudget(cmd.Context(), client, query, vehicleLookupHints{
			Stop:           vehicleStop,
			Route:          vehicleRoute,
			RouteScanLimit: vehicleScanRoutes,
		}, vehiclePTVLookupBudget)
		if err != nil {
			return err
		}
		openData, err := config.OpenDataCredentialsWithOptions(config.LoadOptions{EnvFile: flagEnv})
		if err != nil {
			return err
		}
		if openData.KeyID != "" {
			enrichmentCtx, cancel := context.WithTimeout(cmd.Context(), vehicleGTFSRealtimeBudget)
			result = enrichWithGTFSRealtime(enrichmentCtx, gtfsrt.New(openData.KeyID), query, result)
			cancel()
		} else if shouldMentionGTFSRealtimeBus(result) {
			result.Warnings = append(result.Warnings, "GTFS Realtime enrichment skipped; run 'ptv auth opendata login' or set PTV_OPENDATA_KEY_ID to enable Transport Victoria Open Data vehicle positions")
		}
		if err := cmd.Context().Err(); err != nil {
			return err
		}
		return printVehicleCommandResult(result)
	},
}

func printVehicleCommandResult(result *vehicleResult) error {
	if flagJSON {
		printVehicleWarnings(result.Warnings)
		return printJSON(result)
	}
	printVehicleResult(result)
	return nil
}

type concreteVehicleClient interface {
	vehicleLookupClient
	Departures(ctx context.Context, routeType, stopID int, opts ptvapi.DeparturesOptions) (*ptvapi.DeparturesResponse, error)
	Search(ctx context.Context, term string, routeTypes []int) (*ptvapi.SearchResult, error)
}

type gtfsRealtimeVehicleClient interface {
	FetchSnapshot(ctx context.Context, feed gtfsrt.Feed) (*gtfsrt.Snapshot, error)
}

type vehicleLookupClient interface {
	Routes(ctx context.Context, routeTypes []int, name string) (*ptvapi.RouteResponse, error)
	RunsForRoute(ctx context.Context, routeID, routeType int, opts ptvapi.RunsOptions) (*ptvapi.RunsResponse, error)
	RunsByRef(ctx context.Context, runRef ptvapi.RunRef, opts ptvapi.RunsOptions) (*ptvapi.RunsResponse, error)
	Pattern(ctx context.Context, runRef string, routeType int, opts ptvapi.PatternOptions) (*ptvapi.StoppingPatternResponse, error)
}

type vehicleLookupHints struct {
	Stop           string
	Route          string
	RouteScanLimit int
}

type vehicleResult struct {
	Query       string `json:"query"`
	MatchedBy   string `json:"matched_by"`
	PublicLabel string `json:"public_label,omitempty"`
	// VehicleID is retained for current-major compatibility and is populated
	// only from PTV's public vehicle descriptor, never a GTFS-R internal ID.
	VehicleID       string                   `json:"vehicle_id,omitempty"`
	PTVDescriptorID string                   `json:"ptv_vehicle_descriptor_id,omitempty"`
	RouteType       *int                     `json:"route_type,omitempty"`
	PTVRouteType    *int                     `json:"ptv_route_type,omitempty"`
	Mode            string                   `json:"mode,omitempty"`
	RouteID         int                      `json:"route_id,omitempty"`
	PTVRouteID      int                      `json:"ptv_route_id,omitempty"`
	Route           string                   `json:"route,omitempty"`
	RunRef          string                   `json:"run_ref,omitempty"`
	PTVRunRef       string                   `json:"ptv_run_ref,omitempty"`
	Destination     string                   `json:"destination,omitempty"`
	Status          string                   `json:"status,omitempty"`
	ServiceState    string                   `json:"service_state,omitempty"`
	Position        *vehiclePositionResult   `json:"position,omitempty"`
	Descriptor      *vehicleDescriptorResult `json:"vehicle_descriptor,omitempty"`
	GTFSRealtime    *vehicleGTFSRealtime     `json:"gtfs_realtime,omitempty"`
	NextStop        *vehicleStopResult       `json:"next_stop,omitempty"`
	LastSeen        *vehicleStopResult       `json:"last_seen,omitempty"`
	PositionSource  string                   `json:"position_source,omitempty"`
	Warnings        []string                 `json:"warnings,omitempty"`
}

type vehicleGTFSRealtime struct {
	Source           string                 `json:"source"`
	EntityID         string                 `json:"entity_id,omitempty"`
	FeedEntityID     string                 `json:"feed_entity_id,omitempty"`
	TripID           string                 `json:"trip_id,omitempty"`
	StaticGTFSTripID string                 `json:"static_gtfs_trip_id,omitempty"`
	GTFSTripID       string                 `json:"gtfs_trip_id,omitempty"`
	RouteID          string                 `json:"route_id,omitempty"`
	GTFSRouteID      string                 `json:"gtfs_route_id,omitempty"`
	StartDate        string                 `json:"start_date,omitempty"`
	StartTime        string                 `json:"start_time,omitempty"`
	PublicLabel      string                 `json:"public_label,omitempty"`
	LicensePlate     string                 `json:"license_plate,omitempty"`
	StopID           string                 `json:"stop_id,omitempty"`
	GTFSStopID       string                 `json:"gtfs_stop_id,omitempty"`
	CurrentStatus    string                 `json:"current_status,omitempty"`
	OccupancyStatus  string                 `json:"occupancy_status,omitempty"`
	ObservationState string                 `json:"observation_state"`
	ObservedAt       string                 `json:"observed_at,omitempty"`
	AgeSeconds       *int64                 `json:"age_seconds,omitempty"`
	FeedState        string                 `json:"feed_state"`
	FeedObservedAt   string                 `json:"feed_observed_at,omitempty"`
	FeedAgeSeconds   *int64                 `json:"feed_age_seconds,omitempty"`
	Position         *vehiclePositionResult `json:"position,omitempty"`
}

type vehiclePositionResult struct {
	Latitude    *float64 `json:"latitude,omitempty"`
	Longitude   *float64 `json:"longitude,omitempty"`
	Easting     *float64 `json:"easting,omitempty"`
	Northing    *float64 `json:"northing,omitempty"`
	Direction   string   `json:"direction,omitempty"`
	Bearing     *float64 `json:"bearing,omitempty"`
	Speed       *float64 `json:"speed,omitempty"`
	Supplier    string   `json:"supplier,omitempty"`
	DatetimeUTC string   `json:"datetime_utc,omitempty"`
	ExpiryTime  string   `json:"expiry_time,omitempty"`
	Kind        string   `json:"kind"`
}

// vehicleDescriptorResult keeps the command's JSON independent from the
// upstream transport DTO. ID is PTV's public descriptor value; GTFS-R private
// vehicle identifiers never enter this type.
type vehicleDescriptorResult struct {
	Operator       string `json:"operator"`
	ID             string `json:"id"`
	LowFloor       *bool  `json:"low_floor"`
	AirConditioned *bool  `json:"air_conditioned"`
	Description    string `json:"description"`
	Supplier       string `json:"supplier"`
	Length         string `json:"length"`
}

type vehicleStopResult struct {
	StopID                int     `json:"stop_id"`
	PTVStopID             int     `json:"ptv_stop_id"`
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

func validateVehicleRouteScan(limit int) error {
	if limit < 0 || limit > vehicleMaxRouteScan {
		return fmt.Errorf("--scan-routes must be between 0 and %d", vehicleMaxRouteScan)
	}
	return nil
}

func lookupVehicleWithinBudget(parent context.Context, client concreteVehicleClient, query string, hints vehicleLookupHints, budget time.Duration) (*vehicleResult, error) {
	ctx, cancel := context.WithTimeout(parent, budget)
	defer cancel()
	return lookupVehicleWithHints(ctx, client, query, hints)
}

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

func enrichWithGTFSRealtime(ctx context.Context, client gtfsRealtimeVehicleClient, query string, result *vehicleResult) *vehicleResult {
	if result == nil || !shouldMentionGTFSRealtime(result) {
		return result
	}
	label, ok := vehicleRealtimeLookupLabel(query, result)
	if !ok {
		if result.RunRef != "" {
			result.Warnings = append(result.Warnings, "GTFS Realtime was not joined to the PTV run_ref because PTV run references and static GTFS trip/entity identifiers are different namespaces")
		}
		return result
	}

	var errors []string
	for _, source := range vehicleRealtimeSources(result) {
		if err := ctx.Err(); err != nil {
			result.Warnings = append(result.Warnings, "GTFS Realtime vehicle enrichment stopped: "+err.Error())
			return result
		}
		snapshot, err := client.FetchSnapshot(ctx, source.Feed)
		if err != nil {
			errors = append(errors, source.Feed.ID+": "+err.Error())
			continue
		}
		observation, found := snapshot.FindVehicleByLabel(label)
		if !found {
			continue
		}
		return applyGTFSRealtimeVehicle(result, source, *observation)
	}

	if len(errors) > 0 {
		result.Warnings = append(result.Warnings, "GTFS Realtime vehicle enrichment failed: "+strings.Join(errors, "; "))
		return result
	}
	if result.MatchedBy == "none" {
		result.Warnings = append(result.Warnings, "GTFS Realtime vehicle-position feeds did not expose a matching vehicle")
	} else if result.RouteType != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("GTFS Realtime %s vehicle-position feed did not expose a matching vehicle", strings.ToLower(routeTypeName(*result.RouteType))))
	}
	return result
}

func vehicleRealtimeLookupLabel(query string, result *vehicleResult) (gtfsrt.PublicVehicleLabel, bool) {
	if result == nil {
		return "", false
	}
	if result.Descriptor != nil && strings.TrimSpace(result.Descriptor.ID) != "" {
		return gtfsrt.PublicVehicleLabel(strings.TrimSpace(result.Descriptor.ID)), true
	}
	if strings.TrimSpace(result.PublicLabel) != "" {
		return gtfsrt.PublicVehicleLabel(strings.TrimSpace(result.PublicLabel)), true
	}
	if strings.Contains(result.MatchedBy, "vehicle_descriptor.id") && strings.TrimSpace(result.VehicleID) != "" {
		return gtfsrt.PublicVehicleLabel(strings.TrimSpace(result.VehicleID)), true
	}
	if result.MatchedBy == "none" && strings.TrimSpace(query) != "" {
		return gtfsrt.PublicVehicleLabel(strings.TrimSpace(query)), true
	}
	return "", false
}

type realtimeVehicleSource struct {
	Feed      gtfsrt.Feed
	RouteType int
	Mode      string
}

func vehicleRealtimeSources(result *vehicleResult) []realtimeVehicleSource {
	sources := allVehicleRealtimeSources()
	if result == nil || result.RouteType == nil {
		return sources
	}
	filtered := sources[:0]
	for _, source := range sources {
		if source.RouteType == *result.RouteType {
			filtered = append(filtered, source)
		}
	}
	if len(filtered) > 0 {
		return filtered
	}
	return sources
}

func allVehicleRealtimeSources() []realtimeVehicleSource {
	var sources []realtimeVehicleSource
	for _, feed := range gtfsrt.Feeds() {
		if feed.Kind != gtfsrt.FeedKindVehiclePositions {
			continue
		}
		routeType, ok := realtimeFeedRouteType(feed)
		if !ok {
			continue
		}
		sources = append(sources, realtimeVehicleSource{
			Feed:      feed,
			RouteType: routeType,
			Mode:      routeTypeName(routeType),
		})
	}
	return sources
}

func realtimeFeedRouteType(feed gtfsrt.Feed) (int, bool) {
	switch feed.ID {
	case "metro-vehicle-positions":
		return 0, true
	case "tram-vehicle-positions":
		return 1, true
	case "bus-vehicle-positions":
		return 2, true
	case "vline-vehicle-positions":
		return 3, true
	default:
		return 0, false
	}
}

func applyGTFSRealtimeVehicle(result *vehicleResult, source realtimeVehicleSource, observation gtfsrt.VehicleObservation) *vehicleResult {
	result.GTFSRealtime = gtfsRealtimeFromObservation(source.Feed, observation)
	if result.PublicLabel == "" {
		result.PublicLabel = string(observation.Label)
	}
	if result.MatchedBy == "none" {
		result.MatchedBy = "gtfs_realtime.public_label"
		result.RouteType = intPtr(source.RouteType)
		result.Mode = source.Mode
		result.ServiceState = result.GTFSRealtime.ObservationState
		result.Status = strings.TrimPrefix(observation.CurrentStatus, "VEHICLE_STOP_STATUS_")
		result.Warnings = nil
	}
	return result
}

func shouldMentionGTFSRealtime(result *vehicleResult) bool {
	if result == nil {
		return false
	}
	if result.MatchedBy == "none" {
		return true
	}
	if result.RouteType != nil {
		switch *result.RouteType {
		case 0, 1, 2, 3:
			return true
		}
	}
	return false
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

func gtfsRealtimeFromObservation(feed gtfsrt.Feed, observation gtfsrt.VehicleObservation) *vehicleGTFSRealtime {
	result := &vehicleGTFSRealtime{
		Source:           feed.Title,
		EntityID:         string(observation.EntityID),
		FeedEntityID:     string(observation.EntityID),
		TripID:           string(observation.TripID),
		StaticGTFSTripID: string(observation.TripID),
		GTFSTripID:       string(observation.TripID),
		RouteID:          string(observation.RouteID),
		GTFSRouteID:      string(observation.RouteID),
		StartDate:        observation.StartDate,
		StartTime:        observation.StartTime,
		PublicLabel:      string(observation.Label),
		LicensePlate:     observation.LicensePlate,
		StopID:           string(observation.StopID),
		GTFSStopID:       string(observation.StopID),
		CurrentStatus:    observation.CurrentStatus,
		OccupancyStatus:  observation.OccupancyStatus,
		ObservationState: normalizeVehicleFreshness(observation.Freshness.Overall),
		AgeSeconds:       observation.Freshness.Entity.AgeSeconds,
		FeedState:        normalizeVehicleFreshness(observation.Freshness.Feed.State),
		FeedAgeSeconds:   observation.Freshness.Feed.AgeSeconds,
	}
	if observation.ObservedAt != nil {
		result.ObservedAt = observation.ObservedAt.UTC().Format(time.RFC3339)
	}
	if observation.Freshness.Feed.ObservedAt != nil {
		result.FeedObservedAt = observation.Freshness.Feed.ObservedAt.UTC().Format(time.RFC3339)
	}
	if observation.Latitude != nil || observation.Longitude != nil {
		result.Position = &vehiclePositionResult{
			Latitude:    normalizeVehicleCoordinate(observation.Latitude),
			Longitude:   normalizeVehicleCoordinate(observation.Longitude),
			Bearing:     observation.Bearing,
			Speed:       observation.Speed,
			DatetimeUTC: result.ObservedAt,
			Kind:        "gtfs_realtime_vehicle_position",
		}
	}
	return result
}

func normalizeVehicleFreshness(state gtfsrt.FreshnessState) string {
	switch state {
	case gtfsrt.FreshnessCurrent:
		return string(gtfsrt.FreshnessCurrent)
	case gtfsrt.FreshnessStale:
		return string(gtfsrt.FreshnessStale)
	default:
		return string(gtfsrt.FreshnessUnknown)
	}
}

func normalizeVehicleCoordinate(value *float64) *float64 {
	if value == nil {
		return nil
	}
	rounded := math.Round(*value*1_000_000) / 1_000_000
	return &rounded
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
				return nil, err
			}
		}
		for _, probeRouteType := range routeTypesToProbe() {
			if result, err := lookupVehicleInStopDepartures(ctx, client, query, stop, routeWithType(route, probeRouteType), probeRouteType, true); err == nil {
				return result, nil
			} else if !errors.Is(err, errVehicleNotFound) {
				return nil, err
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
			return nil, err
		}
	}
	for _, probeRouteType := range routeTypesToProbe() {
		if probeRouteType == routeType {
			continue
		}
		if result, err := lookupVehicleInStopDepartures(ctx, client, query, stop, nil, probeRouteType, true); err == nil {
			return result, nil
		} else if !errors.Is(err, errVehicleNotFound) {
			return nil, err
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
		if ptvapi.IsKind(err, ptvapi.ErrorNotFound) {
			return nil, errVehicleNotFound
		}
		return nil, err
	}
	if resp == nil {
		return nil, errVehicleNotFound
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
		PTVStopID:         stop.StopID,
		StopName:          normalizedText(stop.StopName),
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
	response, err := client.RunsByRef(ctx, ptvapi.RunRef(query), vehicleRunsOptions())
	if err != nil {
		if ptvapi.IsKind(err, ptvapi.ErrorNotFound) {
			return nil, errVehicleNotFound
		}
		return nil, err
	}
	if response == nil || len(response.Runs) == 0 {
		return nil, errVehicleNotFound
	}
	run := response.Runs[0]
	for _, candidate := range response.Runs {
		if strings.EqualFold(strings.TrimSpace(candidate.RunRef), strings.TrimSpace(query)) {
			run = candidate
			break
		}
	}
	if strings.TrimSpace(run.RunRef) == "" {
		return nil, errVehicleNotFound
	}
	result := resultFromRun("run_ref", query, ptvapi.Route{RouteID: run.RouteID, RouteType: run.RouteType}, run)
	result.Warnings = append(result.Warnings, "matched a PTV run_ref, not a physical vehicle id")
	attachNextStop(ctx, client, result)
	return result, nil
}

func scanRunsForVehicleID(ctx context.Context, client vehicleLookupClient, query string, routeScanLimit int) ([]routeRun, error) {
	routes, err := allRoutes(ctx, client, routeScanLimit)
	if err != nil {
		return nil, err
	}

	var matches []routeRun
	for _, route := range routes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		resp, err := client.RunsForRoute(ctx, route.RouteID, route.RouteType, vehicleRunsOptions())
		if err != nil {
			if ptvapi.IsKind(err, ptvapi.ErrorNotFound) {
				continue
			}
			return nil, err
		}
		if resp == nil {
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
	response, err := client.Routes(ctx, routeTypesToProbe(), "")
	if err != nil {
		return nil, fmt.Errorf("loading routes: %w", err)
	}
	if limit > 0 && len(response.Routes) > limit {
		return response.Routes[:limit], nil
	}
	return response.Routes, nil
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
		PTVRouteType: intPtr(route.RouteType),
		Mode:         routeTypeName(route.RouteType),
		RouteID:      route.RouteID,
		PTVRouteID:   route.RouteID,
		Route:        routeLabelForVehicle(route),
		RunRef:       strings.TrimSpace(run.RunRef),
		PTVRunRef:    strings.TrimSpace(run.RunRef),
		Destination:  normalizedText(run.DestinationName),
		Status:       normalizedText(run.Status),
		ServiceState: "current",
		Descriptor:   newVehicleDescriptorResult(run.VehicleDescriptor),
	}
	if run.VehicleDescriptor != nil && run.VehicleDescriptor.ID != "" {
		result.VehicleID = strings.TrimSpace(run.VehicleDescriptor.ID)
		result.PTVDescriptorID = result.VehicleID
		result.PublicLabel = result.VehicleID
	}
	if result.VehicleID == "" && matchedBy == "vehicle_descriptor.id" {
		result.VehicleID = query
		result.PTVDescriptorID = query
		result.PublicLabel = query
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
		Direction:   normalizedText(pos.Direction),
		Bearing:     pos.Bearing,
		Supplier:    normalizedText(pos.Supplier),
		DatetimeUTC: strings.TrimSpace(pos.DatetimeUTC),
		ExpiryTime:  strings.TrimSpace(pos.ExpiryTime),
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
		stop := vehicleStopResult{StopID: dep.StopID, PTVStopID: dep.StopID, DepartureSequence: dep.DepartureSequence}
		if s, ok := pattern.Stops[strconv.Itoa(dep.StopID)]; ok {
			stop.StopName = normalizedText(s.StopName)
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
	number := normalizedText(route.RouteNumber)
	name := normalizedText(route.RouteName)
	if number != "" && name != "" {
		return number + " " + name
	}
	if number != "" {
		return number
	}
	if name != "" {
		return name
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

	name := result.PublicLabel
	if name == "" {
		name = result.VehicleID
	}
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

func newVehicleDescriptorResult(desc *ptvapi.VehicleDescriptor) *vehicleDescriptorResult {
	if desc == nil {
		return nil
	}
	return &vehicleDescriptorResult{
		Operator: normalizedText(desc.Operator), ID: strings.TrimSpace(desc.ID),
		LowFloor: desc.LowFloor, AirConditioned: desc.AirConditioned,
		Description: normalizedText(desc.Description), Supplier: normalizedText(desc.Supplier),
		Length: normalizedText(desc.Length),
	}
}

func printVehicleDescriptor(desc *vehicleDescriptorResult) {
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
	if pos.Speed != nil {
		fmt.Printf("Speed: %.1f m/s\n", *pos.Speed)
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
	fmt.Printf("  Source: %s\n", render.CleanText(info.Source))
	if info.PublicLabel != "" {
		fmt.Printf("  Public label: %s\n", render.CleanText(info.PublicLabel))
	}
	if info.EntityID != "" {
		fmt.Printf("  Feed entity ID: %s\n", render.CleanText(info.EntityID))
	}
	if info.LicensePlate != "" {
		fmt.Printf("  Licence plate: %s\n", render.CleanText(info.LicensePlate))
	}
	if info.TripID != "" {
		fmt.Printf("  Static GTFS trip ID: %s\n", render.CleanText(info.TripID))
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
	fmt.Printf("  Observation state: %s\n", render.CleanText(info.ObservationState))
	if info.ObservedAt != "" {
		fmt.Printf("  Observed at: %s\n", render.CleanText(formatVehicleTime(info.ObservedAt)))
	}
	if info.AgeSeconds != nil {
		fmt.Printf("  Observation age: %s\n", render.CleanText(formatVehicleAge(*info.AgeSeconds)))
	}
	fmt.Printf("  Feed state: %s\n", render.CleanText(info.FeedState))
	if info.FeedObservedAt != "" {
		fmt.Printf("  Feed observed at: %s\n", render.CleanText(formatVehicleTime(info.FeedObservedAt)))
	}
	if info.FeedAgeSeconds != nil {
		fmt.Printf("  Feed age: %s\n", render.CleanText(formatVehicleAge(*info.FeedAgeSeconds)))
	}
	if position := info.Position; position != nil {
		if position.Latitude != nil && position.Longitude != nil {
			fmt.Printf("  Position: %.6f, %.6f\n", *position.Latitude, *position.Longitude)
		}
		if position.Bearing != nil {
			fmt.Printf("  Bearing: %.0f\n", *position.Bearing)
		}
		if position.Speed != nil {
			fmt.Printf("  Speed: %.1f m/s\n", *position.Speed)
		}
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
	fmt.Fprintln(os.Stderr, "\nWarnings")
	for _, warning := range warnings {
		fmt.Fprintf(os.Stderr, "  - %s\n", render.CleanText(warning))
	}
}

func formatVehicleTime(utc string) string {
	t, err := time.Parse(time.RFC3339, utc)
	if err != nil {
		return utc
	}
	return localtime.InMelbourne(t).Format("2006-01-02 15:04:05")
}

func formatVehicleAge(seconds int64) string {
	if seconds < 0 {
		return fmt.Sprintf("%ds in the future", -seconds)
	}
	return (time.Duration(seconds) * time.Second).String()
}

func init() {
	vehicleCmd.Flags().IntVar(&vehicleScanRoutes, "scan-routes", 0, fmt.Sprintf("scan up to this many routes when matching physical vehicle ids without a stop hint (0 = disabled, max %d)", vehicleMaxRouteScan))
	vehicleCmd.Flags().StringVar(&vehicleStop, "stop", "", "hint: stop or station where the vehicle was seen")
	vehicleCmd.Flags().StringVar(&vehicleRoute, "route", "", "hint: route or line where the vehicle was seen")
	rootCmd.AddCommand(vehicleCmd)
}
