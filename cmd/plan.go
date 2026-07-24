package cmd

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/thesammykins/ptv_cli/internal/geocode"
	"github.com/thesammykins/ptv_cli/internal/gtfs"
	"github.com/thesammykins/ptv_cli/internal/localtime"
	"github.com/thesammykins/ptv_cli/internal/model"
	"github.com/thesammykins/ptv_cli/internal/render"
	"github.com/thesammykins/ptv_cli/internal/router"
)

var (
	planDepart           string
	planArriveBy         string
	planDate             string
	planRadius           float64
	planNoGeocode        bool
	planNoDisruptions    bool
	planAllowConditional bool
)

const planWalkingSpeedMetresPerSecond = 1.4
const defaultPlanOptionalNetworkBudget = 1500 * time.Millisecond

var planOptionalNetworkBudget = defaultPlanOptionalNetworkBudget

var planTimetableHorizons = []time.Duration{
	2 * time.Hour,
	6 * time.Hour,
	gtfs.TimetableHorizon,
}

type planFreshnessResult struct {
	report gtfs.FreshnessReport
	err    error
}

type planStopResolution struct {
	Endpoints   []model.Endpoint
	Label       string
	Attribution string
}

type planOutput struct {
	Legs        []planLegOutput        `json:"legs"`
	Depart      string                 `json:"depart"`
	Arrive      string                 `json:"arrive"`
	Transfers   int                    `json:"transfers"`
	Disruptions []planDisruptionOutput `json:"disruptions,omitempty"`
	TimeZone    string                 `json:"time_zone"`
	Attribution []string               `json:"attribution,omitempty"`
	Warnings    []string               `json:"warnings,omitempty"`
}

type planLegOutput struct {
	Walk           bool                        `json:"walk"`
	From           planStopOutput              `json:"from"`
	To             planStopOutput              `json:"to"`
	Depart         string                      `json:"depart"`
	Arrive         string                      `json:"arrive"`
	RouteShortName string                      `json:"route_short_name,omitempty"`
	RouteLongName  string                      `json:"route_long_name,omitempty"`
	Mode           int                         `json:"mode,omitempty"`
	GTFSFeedMode   int                         `json:"gtfs_feed_mode,omitempty"`
	Headsign       string                      `json:"headsign,omitempty"`
	TripID         string                      `json:"trip_id,omitempty"`
	GTFSTripID     string                      `json:"gtfs_trip_id,omitempty"`
	BlockID        string                      `json:"block_id,omitempty"`
	GTFSBlockID    string                      `json:"gtfs_block_id,omitempty"`
	StayOnboard    bool                        `json:"stay_onboard,omitempty"`
	Conditional    bool                        `json:"conditional,omitempty"`
	PickupPolicy   model.PassengerActionPolicy `json:"pickup_policy,omitempty"`
	DropOffPolicy  model.PassengerActionPolicy `json:"drop_off_policy,omitempty"`
	Disrupted      bool                        `json:"disrupted,omitempty"`
	DisruptionIDs  []int64                     `json:"disruption_ids,omitempty"`
	PTVDisruptions []int64                     `json:"ptv_disruption_ids,omitempty"`
}

type planStopOutput struct {
	Index      int     `json:"index"`
	ID         string  `json:"id"`
	GTFSStopID string  `json:"gtfs_stop_id,omitempty"`
	Name       string  `json:"name"`
	Lat        float64 `json:"lat"`
	Lon        float64 `json:"lon"`
	Mode       int     `json:"mode"`
	GTFSMode   int     `json:"gtfs_feed_mode"`
}

type planDisruptionOutput struct {
	ID     int64  `json:"id"`
	PTVID  int64  `json:"ptv_disruption_id"`
	Title  string `json:"title"`
	Status string `json:"status,omitempty"`
	Type   string `json:"type,omitempty"`
	URL    string `json:"url,omitempty"`
}

func newPlanOutput(journey *model.Journey, attribution, warnings []string) planOutput {
	output := planOutput{
		Legs:        make([]planLegOutput, 0, len(journey.Legs)),
		Depart:      formatPlanTime(journey.DepTime),
		Arrive:      formatPlanTime(journey.ArrTime),
		Transfers:   journey.Transfers,
		TimeZone:    "Australia/Melbourne",
		Attribution: attribution,
		Warnings:    warnings,
	}
	for _, leg := range journey.Legs {
		output.Legs = append(output.Legs, planLegOutput{
			Walk: leg.Walk, From: newPlanStopOutput(leg.FromStop), To: newPlanStopOutput(leg.ToStop),
			Depart: formatPlanTime(leg.DepTime), Arrive: formatPlanTime(leg.ArrTime),
			RouteShortName: normalizedText(leg.RouteShortName), RouteLongName: normalizedText(leg.RouteLongName),
			Mode: leg.RouteType, GTFSFeedMode: leg.RouteType, Headsign: normalizedText(leg.Headsign),
			TripID: leg.TripID, GTFSTripID: leg.TripID, BlockID: leg.BlockID, GTFSBlockID: leg.BlockID,
			StayOnboard: leg.StayOnboard, Conditional: leg.Conditional,
			PickupPolicy: leg.PickupPolicy, DropOffPolicy: leg.DropOffPolicy,
			Disrupted: leg.Disrupted, DisruptionIDs: leg.DisruptionIDs, PTVDisruptions: leg.DisruptionIDs,
		})
	}
	for _, disruption := range journey.Disruptions {
		output.Disruptions = append(output.Disruptions, planDisruptionOutput{
			ID: disruption.ID, PTVID: disruption.ID, Title: normalizedText(disruption.Title),
			Status: normalizedText(disruption.Status), Type: normalizedText(disruption.Type), URL: normalizedPublicURL(disruption.URL),
		})
	}
	return output
}

func newPlanStopOutput(stop model.Stop) planStopOutput {
	return planStopOutput{
		Index: stop.Index, ID: stop.ID, GTFSStopID: stop.ID,
		Name: normalizedText(stop.Name), Lat: stop.Lat, Lon: stop.Lon,
		Mode: stop.Mode, GTFSMode: stop.Mode,
	}
}

func formatPlanTime(value time.Time) string {
	return value.In(melbourneLocation()).Format(time.RFC3339)
}

var planCmd = &cobra.Command{
	Use:   "plan <from> <to>",
	Short: "Plan a multi-modal journey between two places",
	Long: `Plan a public-transport journey across train, tram, bus and V/Line
services with walking transfers, using the locally ingested GTFS dataset.

<from> and <to> may be a stop name (e.g. "Flinders Street"), a place or
address (e.g. "Federation Square", geocoded via OpenStreetMap and biased to
Victoria), or a "lat,lng" coordinate. By default the trip departs now; use
--depart to leave at a given time, or --arrive-by to arrive no later than a
given time.

Examples:
  ptv plan "Flinders Street" "Box Hill"
  ptv plan "Federation Square" "Melbourne Zoo"
  ptv plan "Flinders Street" "Southern Cross" --depart 17:30
  ptv plan --arrive-by 09:00 -- "-37.8183,144.9671" "Camberwell"

Note: a coordinate beginning with '-' (Melbourne latitudes do) must follow a
'--' separator so it is not mistaken for a flag.`,
	Args: func(cmd *cobra.Command, args []string) error {
		return cobra.ExactArgs(2)(cmd, planPositionals(args))
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		args = planPositionals(args)
		if planDepart != "" && planArriveBy != "" {
			return fmt.Errorf("use only one of --depart or --arrive-by")
		}

		cfg, err := loadRuntimeConfig()
		if err != nil {
			return err
		}
		manager, err := gtfs.NewGenerationManager(cfg.DataDir)
		if err != nil {
			return err
		}
		store, _, err := manager.OpenCurrent(cmd.Context())
		if err != nil {
			if errors.Is(err, gtfs.ErrLegacyDatabase) {
				return fmt.Errorf("legacy GTFS data requires a one-time re-ingest; run 'ptv gtfs update' first")
			}
			if errors.Is(err, gtfs.ErrNoCurrentGeneration) {
				return fmt.Errorf("GTFS data not ingested yet; run 'ptv gtfs update' first")
			}
			return err
		}
		defer store.Close()

		loc := melbourneLocation()
		now := time.Now().In(loc)

		arriveByMode := planArriveBy != ""
		queryTime := now
		if planDepart != "" {
			queryTime, err = parseClockOrTime(planDepart, planDate, loc, now)
			if err != nil {
				return err
			}
		} else if planArriveBy != "" {
			queryTime, err = parseClockOrTime(planArriveBy, planDate, loc, now)
			if err != nil {
				return err
			}
		}

		// Freshness and disruption overlays share one command-level budget that
		// starts before the local timetable work. A slow optional endpoint can
		// therefore never add serial timeout windows after routing completes.
		optionalCtx := cmd.Context()
		cancelOptional := func() {}
		if !flagNoUpdateCheck || !planNoDisruptions {
			optionalCtx, cancelOptional = context.WithTimeout(cmd.Context(), planOptionalNetworkBudget)
		}
		defer cancelOptional()

		progress := newProgress()
		progress.Start()

		direction := gtfs.TimetableForward
		if arriveByMode {
			direction = gtfs.TimetableReverse
		}
		horizonIndex := 0
		tt, err := store.LoadTimetableWindowContext(
			cmd.Context(), queryTime, direction, planTimetableHorizons[horizonIndex],
		)
		if errors.Is(err, gtfs.ErrQueryOutsideCoverage) {
			// A query just beyond the service-date boundary can still be served by
			// an overflowing previous service day. Retry the complete supported
			// horizon before classifying it as outside coverage.
			horizonIndex = len(planTimetableHorizons) - 1
			tt, err = store.LoadTimetableWindowContext(
				cmd.Context(), queryTime, direction, planTimetableHorizons[horizonIndex],
			)
		}
		if err != nil {
			progress.Stop()
			if errors.Is(err, gtfs.ErrQueryOutsideCoverage) {
				return fmt.Errorf("%w; run 'ptv gtfs update' to install current service coverage", err)
			}
			return err
		}

		var freshnessResult <-chan planFreshnessResult
		if !flagNoUpdateCheck {
			state, stateErr := store.DatasetState(cmd.Context())
			if stateErr != nil {
				progress.Stop()
				return stateErr
			}
			results := make(chan planFreshnessResult, 1)
			freshnessResult = results
			check := checkGTFSFreshnessForCommand
			go func() {
				report, checkErr := check(optionalCtx, cfg, state, queryTime, true, false)
				results <- planFreshnessResult{report: report, err: checkErr}
			}()
		}

		var geo *geocode.Geocoder
		if !planNoGeocode {
			geo, err = geocode.NewWithOptions(geocode.Options{
				Endpoint:    cfg.GeocoderURL,
				Provider:    cfg.GeocoderProvider,
				Attribution: cfg.GeocoderAttribution,
				CacheDir:    filepath.Join(cfg.DataDir, "geocode"),
				BeforeRequest: func(_ string) {
					progress.Stop()
					fmt.Fprintf(os.Stderr, "No local stop matched; sending the place query to %s.\n", render.CleanText(cfg.GeocoderProvider))
				},
			})
			if err != nil {
				progress.Stop()
				return err
			}
		}

		source, err := resolvePlanEndpoints(cmd.Context(), tt, args[0], planRadius, geo)
		if err != nil {
			progress.Stop()
			return fmt.Errorf("origin: %w", err)
		}
		target, err := resolvePlanEndpoints(cmd.Context(), tt, args[1], planRadius, geo)
		if err != nil {
			progress.Stop()
			return fmt.Errorf("destination: %w", err)
		}

		planJourney := func() (*model.Journey, error) {
			if strings.EqualFold(strings.TrimSpace(args[0]), strings.TrimSpace(args[1])) {
				return &model.Journey{Legs: []model.Leg{}, DepTime: queryTime, ArrTime: queryTime}, nil
			}
			if arriveByMode {
				return router.PlanLatestDepartureContext(cmd.Context(), tt, source.Endpoints, target.Endpoints, queryTime, router.PlanOptions{AllowConditional: planAllowConditional})
			}
			return router.PlanEarliestArrivalContext(cmd.Context(), tt, source.Endpoints, target.Endpoints, queryTime, router.PlanOptions{AllowConditional: planAllowConditional})
		}

		journey, err := planJourney()
		for errors.Is(err, router.ErrNoJourney) && horizonIndex+1 < len(planTimetableHorizons) {
			horizonIndex++
			tt, err = store.LoadTimetableWindowContext(
				cmd.Context(), queryTime, direction, planTimetableHorizons[horizonIndex],
			)
			if err != nil {
				break
			}
			journey, err = planJourney()
		}
		if err != nil {
			progress.Stop()
			if errors.Is(err, gtfs.ErrQueryOutsideCoverage) {
				return fmt.Errorf("%w; run 'ptv gtfs update' to install current service coverage", err)
			}
			return err
		}

		// Overlay real-time disruptions onto the journey (best-effort).
		var disruptionWarn string
		if err := cmd.Context().Err(); err != nil {
			progress.Stop()
			return err
		}
		if !planNoDisruptions && len(journey.Legs) > 0 {
			if optionalCtx.Err() != nil {
				disruptionWarn = "real-time disruptions skipped: optional network time budget exhausted"
			} else if client, _, cerr := loadClient(); cerr == nil {
				derr := annotateDisruptions(optionalCtx, client, journey)
				if cmd.Context().Err() != nil {
					progress.Stop()
					return cmd.Context().Err()
				}
				if derr != nil {
					disruptionWarn = "real-time disruptions unavailable: " + derr.Error()
				}
			} else {
				disruptionWarn = "real-time disruptions skipped: " + cerr.Error()
			}
		}

		progress.Stop()
		var warnings []string
		if freshnessResult != nil {
			select {
			case result := <-freshnessResult:
				if cmd.Context().Err() != nil {
					return cmd.Context().Err()
				}
				if result.err != nil {
					warnings = append(warnings, "static GTFS freshness check unavailable: "+result.err.Error())
				} else {
					warnings = append(warnings, freshnessWarnings(result.report)...)
				}
			case <-optionalCtx.Done():
				if cmd.Context().Err() != nil {
					return cmd.Context().Err()
				}
				warnings = append(warnings, "static GTFS freshness check skipped: optional network time budget exhausted")
			}
		}
		cancelOptional()
		if disruptionWarn != "" {
			warnings = append(warnings, disruptionWarn)
		}
		if err := cmd.Context().Err(); err != nil {
			return err
		}
		if journeyUsesConditionalService(journey) {
			warnings = append(warnings, "journey uses conditional pickup or drop-off; contact the operator or coordinate with the driver as required")
		}
		for _, warning := range warnings {
			fmt.Fprintln(os.Stderr, "warning:", render.CleanText(warning))
		}
		attributions := uniqueNonEmpty(source.Attribution, target.Attribution)
		if flagJSON {
			return printJSON(newPlanOutput(journey, attributions, warnings))
		}
		if err := renderJourney(journey, orLabel(args[0], source.Label), orLabel(args[1], target.Label), arriveByMode, queryTime); err != nil {
			return err
		}
		if len(attributions) > 0 {
			fmt.Printf("\nData attribution: %s\n", render.CleanText(strings.Join(attributions, "; ")))
		}
		return nil
	},
}

func planPositionals(args []string) []string {
	if len(args) > 0 && args[0] == "--" {
		return args[1:]
	}
	return args
}

// orLabel augments the user's query with a resolved place label when geocoding
// produced one.
func orLabel(query, label string) string {
	if label == "" || strings.EqualFold(label, query) {
		return query
	}
	return fmt.Sprintf("%s (%s)", query, label)
}

// resolvePlanEndpoints maps a query to weighted access/egress connectors.
// Local stop-name matches have zero access cost; coordinates and geocoded
// places retain distance-derived walking time and their user-visible location.
func resolvePlanEndpoints(ctx context.Context, tt *model.Timetable, query string, radiusM float64, geo *geocode.Geocoder) (planStopResolution, error) {
	query = strings.TrimSpace(query)
	if lat, lon, err := parseLatLng(query); err == nil {
		endpoints := weightedEndpointsWithinRadius(tt, lat, lon, radiusM, query)
		if len(endpoints) == 0 {
			return planStopResolution{}, fmt.Errorf("no stops found within %.0fm of %.5f,%.5f", radiusM, lat, lon)
		}
		return planStopResolution{Endpoints: endpoints}, nil
	}

	lower := strings.ToLower(query)
	if idxs, ok := tt.NameIndex[lower]; ok {
		return planStopResolution{Endpoints: zeroCostEndpoints(sortedUniqueStops(idxs))}, nil
	}

	var prefix, contains []int
	for name, idxs := range tt.NameIndex {
		switch {
		case strings.HasPrefix(name, lower):
			prefix = append(prefix, idxs...)
		case strings.Contains(name, lower):
			contains = append(contains, idxs...)
		}
	}
	prefix = sortedUniqueStops(prefix)
	contains = sortedUniqueStops(contains)
	if len(prefix) > 0 {
		if majors := filterMajorStops(tt.Stops, prefix); len(majors) > 0 {
			return planStopResolution{Endpoints: zeroCostEndpoints(majors)}, nil
		}
		return planStopResolution{Endpoints: zeroCostEndpoints(prefix)}, nil
	}
	if len(contains) > 0 {
		if majors := filterMajorStops(tt.Stops, contains); len(majors) > 0 {
			return planStopResolution{Endpoints: zeroCostEndpoints(majors)}, nil
		}
		return planStopResolution{Endpoints: zeroCostEndpoints(contains)}, nil
	}

	if geo != nil {
		place, err := geo.Lookup(ctx, query)
		if err != nil {
			return planStopResolution{}, fmt.Errorf("no stop matching %q and %v", query, err)
		}
		endpoints := weightedEndpointsWithinRadius(tt, place.Lat, place.Lon, radiusM, place.DisplayName)
		if len(endpoints) == 0 {
			return planStopResolution{}, fmt.Errorf("found %q at %.5f,%.5f but no stops within %.0fm", place.DisplayName, place.Lat, place.Lon, radiusM)
		}
		if isStreetResult(place.DisplayName) && !isStreetQuery(query) {
			stations := stopsWithinRadius(tt, place.Lat, place.Lon, 2000)
			if majors := filterNamedMajorStops(tt.Stops, stations, lower); len(majors) > 0 {
				endpoints = weightedEndpoints(tt, majors, place.Lat, place.Lon, place.DisplayName)
			}
		}
		return planStopResolution{
			Endpoints:   endpoints,
			Label:       place.DisplayName,
			Attribution: place.Attribution,
		}, nil
	}

	return planStopResolution{}, fmt.Errorf("no stop matching %q (try a different name, a lat,lng, or drop --no-geocode)", query)
}

func zeroCostEndpoints(stops []int) []model.Endpoint {
	endpoints := make([]model.Endpoint, len(stops))
	for i, stop := range stops {
		endpoints[i] = model.Endpoint{Stop: stop}
	}
	return endpoints
}

func sortedUniqueStops(stops []int) []int {
	if len(stops) == 0 {
		return nil
	}
	result := append([]int(nil), stops...)
	sort.Ints(result)
	write := 1
	for read := 1; read < len(result); read++ {
		if result[read] == result[write-1] {
			continue
		}
		result[write] = result[read]
		write++
	}
	return result[:write]
}

func weightedEndpointsWithinRadius(tt *model.Timetable, lat, lon, radiusM float64, label string) []model.Endpoint {
	return weightedEndpoints(tt, stopsWithinRadius(tt, lat, lon, radiusM), lat, lon, label)
}

func weightedEndpoints(tt *model.Timetable, stops []int, lat, lon float64, label string) []model.Endpoint {
	location := &model.Stop{Index: -1, Name: label, Lat: lat, Lon: lon, Mode: -1}
	endpoints := make([]model.Endpoint, 0, len(stops))
	for _, stop := range stops {
		if stop < 0 || stop >= len(tt.Stops) {
			continue
		}
		distance := haversineM(lat, lon, tt.Stops[stop].Lat, tt.Stops[stop].Lon)
		seconds := int(math.Ceil(distance / planWalkingSpeedMetresPerSecond))
		endpoints = append(endpoints, model.Endpoint{Stop: stop, WalkSeconds: seconds, Location: location})
	}
	sort.Slice(endpoints, func(i, j int) bool {
		if endpoints[i].WalkSeconds != endpoints[j].WalkSeconds {
			return endpoints[i].WalkSeconds < endpoints[j].WalkSeconds
		}
		return endpoints[i].Stop < endpoints[j].Stop
	})
	return endpoints
}

func journeyUsesConditionalService(journey *model.Journey) bool {
	for _, leg := range journey.Legs {
		if leg.Conditional {
			return true
		}
	}
	return false
}

func uniqueNonEmpty(values ...string) []string {
	seen := make(map[string]bool, len(values))
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

// isRailMode returns true for feed modes that represent rail-based transport
// (V/Line Train mode 1 and Metro Train mode 2).
func isRailMode(mode int) bool {
	return mode == 1 || mode == 2
}

// filterMajorStops filters a set of stop indexes to only those served by
// rail-based transport (V/Line or Metro Train). These are what users
// typically intend when providing a place name like "Geelong".
func filterMajorStops(stops []model.Stop, idxs []int) []int {
	var out []int
	for _, i := range idxs {
		if i >= 0 && i < len(stops) && isRailMode(stops[i].Mode) {
			out = append(out, i)
		}
	}
	return out
}

// filterNamedMajorStops filters a set of stop indexes to rail stops whose
// name contains the query as a substring.
func filterNamedMajorStops(stops []model.Stop, idxs []int, query string) []int {
	var out []int
	for _, i := range idxs {
		if i >= 0 && i < len(stops) {
			name := strings.ToLower(stops[i].Name)
			if isRailMode(stops[i].Mode) && strings.Contains(name, query) {
				out = append(out, i)
			}
		}
	}
	return out
}

// isStreetResult checks whether a Nominatim display_name looks like a street
// or road rather than a city/town/station.
func isStreetResult(displayName string) bool {
	suffixes := []string{" rd", " road", " st", " street", " ave", " avenue",
		" dr", " drive", " cres", " crescent", " gr", " grove",
		" cl", " close", " pl", " place", " ln", " lane", " hwy", " highway",
		" way", " court", " circuit", " boulevard", " parade", " pde"}
	lower := strings.ToLower(displayName)
	for _, s := range suffixes {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

// isStreetQuery checks whether the user's query contains street-like suffixes,
// indicating they specifically intend a street address rather than a place.
func isStreetQuery(query string) bool {
	suffixes := []string{" rd", " road", " st", " street", " ave", " avenue",
		" dr", " drive", " cres", " crescent", " ln", " lane", " hwy", " highway",
		" pde", " parade", " way", " court", " circuit", " boulevard"}
	lower := strings.ToLower(query)
	for _, s := range suffixes {
		if strings.HasSuffix(lower, s) {
			return true
		}
	}
	return false
}

// stopsWithinRadius returns stop indexes within radiusM metres of a point.
func stopsWithinRadius(tt *model.Timetable, lat, lon, radiusM float64) []int {
	var out []int
	for _, s := range tt.Stops {
		if haversineM(lat, lon, s.Lat, s.Lon) <= radiusM {
			out = append(out, s.Index)
		}
	}
	return out
}

// renderJourney prints a planned itinerary as a sequence of legs.
func renderJourney(j *model.Journey, from, to string, arriveBy bool, queryTime time.Time) error {
	loc := melbourneLocation()
	if len(j.Legs) == 0 {
		fmt.Printf("Journey: %s → %s\n", render.CleanText(from), render.CleanText(to))
		fmt.Printf("Already at destination at %s (0 minutes, 0 transfers).\n", j.ArrTime.In(loc).Format("Mon 15:04"))
		return nil
	}

	fmt.Printf("Journey: %s → %s\n", render.CleanText(from), render.CleanText(to))
	dep := j.DepTime.In(loc)
	arr := j.ArrTime.In(loc)
	dur := arr.Sub(dep).Round(time.Minute)
	fmt.Printf("Depart %s  •  Arrive %s  •  %s  •  %d transfer(s)\n\n",
		dep.Format("Mon 15:04"), arr.Format("Mon 15:04"), formatDuration(dur), j.Transfers)

	t := render.NewTable("DEP", "ARR", "MODE", "ROUTE", "FROM", "TO", "TOWARDS")
	for _, l := range j.Legs {
		ld := l.DepTime.In(loc).Format("15:04")
		la := l.ArrTime.In(loc).Format("15:04")
		if l.Walk {
			t.Row(ld, la, "Walk", "-", l.FromStop.Name, l.ToStop.Name, "-")
			continue
		}
		route := l.RouteShortName
		if route == "" {
			route = l.RouteLongName
		}
		if l.Disrupted {
			route += " ⚠"
		}
		mode := gtfsModeName(l.RouteType)
		if l.StayOnboard {
			mode = "Stay onboard"
		}
		t.Row(ld, la, mode, route, l.FromStop.Name, l.ToStop.Name, dashIfEmpty(l.Headsign))
	}
	if err := t.Flush(); err != nil {
		return err
	}

	// Show only proven in-seat continuations. Equal block strings alone are not
	// enough evidence across feeds, service days, or explicit type-5 rules.
	for i := range j.Legs {
		if j.Legs[i].StayOnboard {
			fmt.Printf("\n  Stay onboard as the service continues from %s\n",
				render.CleanText(j.Legs[i].FromStop.Name))
		}
	}

	if len(j.Disruptions) > 0 {
		fmt.Println("\n⚠ Disruptions affecting this journey")
		for _, d := range j.Disruptions {
			status := d.Status
			if status == "" {
				status = "Disruption"
			}
			fmt.Printf("  • [%s] %s\n", render.CleanText(status), render.CleanText(d.Title))
			if d.URL != "" {
				fmt.Printf("    %s\n", render.CleanText(d.URL))
			}
		}
	}
	return nil
}

// gtfsModeName maps a PTV GTFS feed mode (the feed/zip number) to a label.
func gtfsModeName(mode int) string {
	switch mode {
	case 1:
		return "V/Line Train"
	case 2:
		return "Train"
	case 3:
		return "Tram"
	case 4:
		return "Bus"
	case 5:
		return "V/Line Coach"
	case 6:
		return "Regional Bus"
	case 7, 8:
		return "Bus"
	case 10, 11:
		return "Bus"
	default:
		return "Transit"
	}
}

// parseClockOrTime parses either a "HH:MM" clock time (on the given date or
// today) or a full RFC3339 / "2006-01-02 15:04" timestamp.
func parseClockOrTime(value, dateStr string, loc *time.Location, now time.Time) (time.Time, error) {
	value = strings.TrimSpace(value)
	if t, err := time.ParseInLocation("2006-01-02 15:04", value, loc); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t.In(loc), nil
	}

	layouts := []string{"15:04", "3:04pm", "3:04PM", "15:04:05"}
	day := now
	if dateStr != "" {
		d, err := time.ParseInLocation("2006-01-02", dateStr, loc)
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid --date %q (want YYYY-MM-DD)", dateStr)
		}
		day = d
	}
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, value, loc); err == nil {
			return time.Date(day.Year(), day.Month(), day.Day(), t.Hour(), t.Minute(), t.Second(), 0, loc), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid time %q (want HH:MM or YYYY-MM-DD HH:MM)", value)
}

// melbourneLocation returns the embedded Australia/Melbourne location.
func melbourneLocation() *time.Location {
	return localtime.Melbourne()
}

// haversineM returns the great-circle distance in metres.
func haversineM(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371000.0
	p1 := lat1 * math.Pi / 180
	p2 := lat2 * math.Pi / 180
	dp := (lat2 - lat1) * math.Pi / 180
	dl := (lon2 - lon1) * math.Pi / 180
	a := math.Sin(dp/2)*math.Sin(dp/2) + math.Cos(p1)*math.Cos(p2)*math.Sin(dl/2)*math.Sin(dl/2)
	return r * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// formatDuration renders a duration as "Xh Ym" / "Ym".
func formatDuration(d time.Duration) string {
	mins := int(d.Minutes())
	if mins < 0 {
		mins = 0
	}
	if mins < 60 {
		return fmt.Sprintf("%dm", mins)
	}
	return fmt.Sprintf("%dh %dm", mins/60, mins%60)
}

func dashIfEmpty(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func init() {
	planCmd.Flags().StringVar(&planDepart, "depart", "", "depart at this time (HH:MM or 'YYYY-MM-DD HH:MM'); default now")
	planCmd.Flags().StringVar(&planArriveBy, "arrive-by", "", "arrive no later than this time (HH:MM or 'YYYY-MM-DD HH:MM')")
	planCmd.Flags().StringVar(&planDate, "date", "", "service date for HH:MM times (YYYY-MM-DD); default today")
	planCmd.Flags().Float64Var(&planRadius, "radius", 1000, "search radius in metres for a lat,lng or geocoded place")
	planCmd.Flags().BoolVar(&planNoGeocode, "no-geocode", false, "disable place/address geocoding (match local stop names only)")
	planCmd.Flags().BoolVar(&planNoDisruptions, "no-disruptions", false, "skip the real-time disruptions overlay")
	planCmd.Flags().BoolVar(&planAllowConditional, "allow-conditional", false, "allow pickup/drop-off that requires advance arrangement or driver coordination")
	rootCmd.AddCommand(planCmd)
}
