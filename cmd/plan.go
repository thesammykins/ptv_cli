package cmd

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/thesammykins/ptv_cli/internal/geocode"
	"github.com/thesammykins/ptv_cli/internal/gtfs"
	"github.com/thesammykins/ptv_cli/internal/model"
	"github.com/thesammykins/ptv_cli/internal/render"
	"github.com/thesammykins/ptv_cli/internal/router"
)

var (
	planDepart        string
	planArriveBy      string
	planDate          string
	planRadius        float64
	planNoGeocode     bool
	planNoDisruptions bool
	planNoUpdateCheck bool
)

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
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if planDepart != "" && planArriveBy != "" {
			return fmt.Errorf("use only one of --depart or --arrive-by")
		}

		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		store, err := gtfs.Open(cfg.DBPath())
		if err != nil {
			return err
		}
		defer store.Close()
		if !store.IsIngested() {
			return fmt.Errorf("GTFS data not ingested yet; run 'ptv gtfs update' first")
		}

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

		tt, err := store.LoadTimetable(queryTime)
		if err != nil {
			return err
		}

		if !planNoUpdateCheck {
			rep := gtfs.Freshness(ctx(), store, cfg.GTFSURL, true, false)
			for _, w := range freshnessWarnings(rep) {
				fmt.Fprintln(os.Stderr, render.CleanText(w))
			}
		}

		var geo *geocode.Geocoder
		if !planNoGeocode {
			geo = geocode.New(cfg.DataDir)
		}

		sources, fromLabel, err := resolvePlanStops(ctx(), tt, args[0], planRadius, geo)
		if err != nil {
			return fmt.Errorf("origin: %w", err)
		}
		targets, toLabel, err := resolvePlanStops(ctx(), tt, args[1], planRadius, geo)
		if err != nil {
			return fmt.Errorf("destination: %w", err)
		}

		var journey *model.Journey
		if arriveByMode {
			journey, err = router.PlanLatestDeparture(tt, sources, targets, queryTime)
		} else {
			journey, err = router.PlanEarliestArrival(tt, sources, targets, queryTime)
		}
		if err != nil {
			return err
		}

		// Overlay real-time disruptions onto the journey (best-effort).
		var disruptionWarn string
		if !planNoDisruptions && len(journey.Legs) > 0 {
			if client, _, cerr := loadClient(); cerr == nil {
				if derr := annotateDisruptions(ctx(), client, journey); derr != nil {
					disruptionWarn = "real-time disruptions unavailable: " + derr.Error()
				}
			} else {
				disruptionWarn = "real-time disruptions skipped: " + cerr.Error()
			}
		}

		if flagJSON {
			return printJSON(journey)
		}
		if err := renderJourney(journey, orLabel(args[0], fromLabel), orLabel(args[1], toLabel), arriveByMode, queryTime); err != nil {
			return err
		}
		if disruptionWarn != "" {
			fmt.Println("\nNote:", render.CleanText(disruptionWarn))
		}
		return nil
	},
}

// orLabel augments the user's query with a resolved place label when geocoding
// produced one.
func orLabel(query, label string) string {
	if label == "" || strings.EqualFold(label, query) {
		return query
	}
	return fmt.Sprintf("%s (%s)", query, label)
}

// resolvePlanStops maps a user query to a set of GTFS stop indexes that can
// serve as journey endpoints. Resolution order: "lat,lng" coordinate, then a
// GTFS stop-name match, then (unless geo is nil) a geocoded place whose nearest
// stops within radiusM are used. The returned label is a human-readable place
// name when the query was geocoded.
func resolvePlanStops(ctx context.Context, tt *model.Timetable, query string, radiusM float64, geo *geocode.Geocoder) ([]int, string, error) {
	query = strings.TrimSpace(query)
	if lat, lon, err := parseLatLng(query); err == nil {
		idxs := stopsWithinRadius(tt, lat, lon, radiusM)
		if len(idxs) == 0 {
			return nil, "", fmt.Errorf("no stops found within %.0fm of %.5f,%.5f", radiusM, lat, lon)
		}
		return idxs, "", nil
	}

	lower := strings.ToLower(query)
	if idxs, ok := tt.NameIndex[lower]; ok {
		return idxs, "", nil
	}

	// Tiered name matching: prefer prefix matches, then substring matches.
	// All stop names in a tier are unioned so multi-platform / multi-modal
	// interchanges (e.g. a station's train, tram and bus stops) are all valid
	// journey endpoints.
	var prefix, contains []int
	for name, idxs := range tt.NameIndex {
		switch {
		case strings.HasPrefix(name, lower):
			prefix = append(prefix, idxs...)
		case strings.Contains(name, lower):
			contains = append(contains, idxs...)
		}
	}
	if len(prefix) > 0 {
		return prefix, "", nil
	}
	if len(contains) > 0 {
		return contains, "", nil
	}

	// Fall back to geocoding the free-text query to a coordinate, then use the
	// nearest stops to it.
	if geo != nil {
		place, err := geo.Lookup(ctx, query)
		if err != nil {
			return nil, "", fmt.Errorf("no stop matching %q and %v", query, err)
		}
		idxs := stopsWithinRadius(tt, place.Lat, place.Lon, radiusM)
		if len(idxs) == 0 {
			return nil, "", fmt.Errorf("found %q at %.5f,%.5f but no stops within %.0fm", place.DisplayName, place.Lat, place.Lon, radiusM)
		}
		return idxs, place.DisplayName, nil
	}

	return nil, "", fmt.Errorf("no stop matching %q (try a different name, a lat,lng, or drop --no-geocode)", query)
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
		fmt.Println("No journey found.")
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
		t.Row(ld, la, gtfsModeName(l.RouteType), route, l.FromStop.Name, l.ToStop.Name, dashIfEmpty(l.Headsign))
	}
	if err := t.Flush(); err != nil {
		return err
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

// melbourneLocation returns Australia/Melbourne, falling back to local time.
func melbourneLocation() *time.Location {
	if loc, err := time.LoadLocation("Australia/Melbourne"); err == nil {
		return loc
	}
	return time.Local
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
	planCmd.Flags().Float64Var(&planRadius, "radius", 800, "search radius in metres for a lat,lng or geocoded place")
	planCmd.Flags().BoolVar(&planNoGeocode, "no-geocode", false, "disable place/address geocoding (match local stop names only)")
	planCmd.Flags().BoolVar(&planNoDisruptions, "no-disruptions", false, "skip the real-time disruptions overlay")
	planCmd.Flags().BoolVar(&planNoUpdateCheck, "no-update-check", false, "skip the GTFS staleness / upstream-update check")
	rootCmd.AddCommand(planCmd)
}
