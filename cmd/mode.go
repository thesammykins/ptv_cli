package cmd

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/thesammykins/ptv_cli/internal/ptvapi"
	"github.com/thesammykins/ptv_cli/internal/render"
)

// newModeCommand builds a mode-scoped command (e.g. tram/bus/vline) that gives
// a unified per-route view plus `lines` and `next` subcommands, all constrained
// to the given PTV route_type.
func newModeCommand(use, short string, routeType int) *cobra.Command {
	modeName := routeTypeName(routeType)

	cmd := &cobra.Command{
		Use:   use + " <route>",
		Short: short,
		Long: fmt.Sprintf(`%s

Show a full view of a %s route: its directions, stops ordered where PTV supplies
a sequence, and any active disruptions. Pass a route number (e.g. "109") or a
route name.

Subcommands:
	%s lines              list all %s routes
	%s next "<stop>"      live departures from a stop (scoped to %s)
	%s disruptions        list active %s disruptions`,
			short, modeName, use, modeName, use, modeName, use, modeName),
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("specify a route (e.g. %q) or a subcommand (lines, next)", use+" 109")
			}
			client, _, err := loadClient()
			if err != nil {
				return err
			}
			return runModeShow(cmd.Context(), client, routeType, joinArgs(args))
		},
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "lines",
		Short: fmt.Sprintf("List all %s routes", modeName),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := loadClient()
			if err != nil {
				return err
			}
			return runModeLines(cmd.Context(), client, routeType)
		},
	})

	nextSub := &cobra.Command{
		Use:   "next <stop>",
		Short: fmt.Sprintf("Live departures from a stop (%s)", modeName),
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := loadClient()
			if err != nil {
				return err
			}
			return runModeNext(cmd.Context(), client, routeType, joinArgs(args))
		},
	}
	nextSub.Flags().StringVar(&nextRoute, "route", "", "filter to a specific route id or name")
	nextSub.Flags().StringVar(&nextPlatform, "platform", "", "filter to a specific platform number")
	cmd.AddCommand(nextSub)

	cmd.AddCommand(&cobra.Command{
		Use:   "disruptions",
		Short: fmt.Sprintf("List active %s disruptions", modeName),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := loadClient()
			if err != nil {
				return err
			}
			return runDisruptions(cmd.Context(), client, []int{routeType}, "")
		},
	})

	return cmd
}

// runModeShow renders a route header, directions, sequenced stops and disruptions.
func runModeShow(ctx context.Context, client *ptvapi.Client, routeType int, query string) error {
	route, err := resolveRouteForMode(ctx, client, routeType, query)
	if err != nil {
		return err
	}

	dirs, err := client.Directions(ctx, route.RouteID)
	if err != nil {
		return err
	}

	sortLineDirections(dirs.Directions)
	dis, derr := client.DisruptionsForRoute(ctx, route.RouteID)
	if derr != nil {
		fmt.Fprintln(os.Stderr, "warning: route disruptions are unavailable")
	}

	if flagJSON {
		stopsByDir := map[string][]ptvapi.StopModel{}
		for _, d := range dirs.Directions {
			dID := d.DirectionID
			s, serr := client.StopsForRoute(ctx, route.RouteID, route.RouteType, &dID)
			if serr != nil {
				return serr
			}
			sortStopsBySequence(s.Stops)
			stopsByDir[strconv.Itoa(d.DirectionID)] = limitStops(s.Stops)
		}
		disruptions := []ptvapi.Disruption{}
		if derr == nil {
			if ds := flattenDisruptions(dis); ds != nil {
				disruptions = ds
			}
		}
		return printJSON(newModeShowOutput(*route, dirs.Directions, stopsByDir, disruptions))
	}

	label := route.RouteName
	if route.RouteNumber != "" {
		label = fmt.Sprintf("%s — %s", route.RouteNumber, route.RouteName)
	}
	label = render.CleanText(label)
	fmt.Printf("%s (%s)\n", label, routeTypeName(route.RouteType))
	fmt.Printf("Route ID: %d\n\n", route.RouteID)

	fmt.Println("Directions")
	dt := render.NewTable("ID", "NAME")
	for _, d := range dirs.Directions {
		dt.Row(d.DirectionID, d.DirectionName)
	}
	if err := dt.Flush(); err != nil {
		return err
	}
	fmt.Println()

	if len(dirs.Directions) > 0 {
		dID := dirs.Directions[0].DirectionID
		stops, serr := client.StopsForRoute(ctx, route.RouteID, route.RouteType, &dID)
		if serr != nil {
			return serr
		}
		sortStopsBySequence(stops.Stops)
		stops.Stops = limitStops(stops.Stops)
		fmt.Printf("Stops (towards %s)\n", render.CleanText(dirs.Directions[0].DirectionName))
		st := render.NewTable("SEQ", "ID", "STOP", "SUBURB")
		for _, s := range stops.Stops {
			st.Row(stopSequenceLabel(s.StopSequence), s.StopID, s.StopName, s.StopSuburb)
		}
		if err := st.Flush(); err != nil {
			return err
		}
	}

	if derr == nil {
		ds := flattenDisruptions(dis)
		if len(ds) > 0 {
			fmt.Println("\nDisruptions")
			for _, d := range ds {
				fmt.Printf("  • [%s] %s\n", render.CleanText(d.DisruptionStatus), render.CleanText(d.Title))
				if d.URL != "" {
					fmt.Printf("    %s\n", render.CleanText(d.URL))
				}
			}
		}
	}
	return nil
}

// runModeLines lists all routes for a mode.
func runModeLines(ctx context.Context, client *ptvapi.Client, routeType int) error {
	resp, err := client.Routes(ctx, []int{routeType}, "")
	if err != nil {
		return err
	}
	sortLineRoutes(resp.Routes)
	resp.Routes = limitRoutes(resp.Routes)
	output := newModeLinesOutput(resp)
	if flagJSON {
		return printJSON(output)
	}
	t := render.NewTable("ID", "NUMBER", "NAME")
	for _, r := range output.Routes {
		t.Row(r.RouteID, r.RouteNumber, r.RouteName)
	}
	if err := t.Flush(); err != nil {
		return err
	}
	fmt.Printf("\n%d %s routes\n", len(output.Routes), routeTypeName(routeType))
	return nil
}

// runModeNext shows live departures from a stop, scoped to the mode.
func runModeNext(ctx context.Context, client *ptvapi.Client, routeType int, query string) error {
	stop, err := resolveStopContext(ctx, client, query, []int{routeType})
	if err != nil {
		return err
	}

	opts := ptvapi.DeparturesOptions{
		MaxResults: orDefault(flagLimit, 8),
		Expand:     []string{ptvapi.ExpandRoute, ptvapi.ExpandRun, ptvapi.ExpandDirection, ptvapi.ExpandDisruption},
	}
	if nextRoute != "" {
		route, rerr := resolveRouteWithTypesContext(ctx, client, nextRoute, []int{routeType})
		if rerr != nil {
			return rerr
		}
		if rerr := ensureRouteServesStop(ctx, client, stop, route); rerr != nil {
			return rerr
		}
		opts.RouteID = route.RouteID
	}
	resp, err := client.Departures(ctx, routeType, stop.StopID, opts)
	if err != nil {
		return err
	}
	deps := resp.Departures
	if nextPlatform != "" {
		deps = filterPlatform(deps, nextPlatform)
	}
	sortDepartures(deps)
	deps = limitDepartures(deps)
	resp.Departures = deps
	if flagJSON {
		return printJSON(newNextOutput(resp))
	}

	stopName := stop.StopName
	if s, ok := resp.Stops[strconv.Itoa(stop.StopID)]; ok && s.StopName != "" {
		stopName = s.StopName
	} else if stopName == "" {
		stopName = fmt.Sprintf("Stop %d", stop.StopID)
	}
	fmt.Printf("Next departures — %s (%s)\n\n", render.CleanText(stopName), routeTypeName(routeType))

	if len(deps) == 0 {
		fmt.Println("No upcoming departures.")
		return nil
	}

	now := time.Now()
	t := render.NewTable("IN", "SCHEDULED", "EST", "PLAT", "ROUTE", "TOWARDS", "STATUS")
	for _, d := range deps {
		when, isEst := departureTime(d)
		countdown := "-"
		estStr := "-"
		schedStr := "-"
		if d.ScheduledDepartureUTC != nil {
			schedStr = formatLocal(*d.ScheduledDepartureUTC)
		}
		if d.EstimatedDepartureUTC != nil {
			estStr = formatLocal(*d.EstimatedDepartureUTC)
		}
		if !when.IsZero() {
			countdown = formatCountdown(when.Sub(now))
		}
		plat := "-"
		if d.PlatformNumber != nil && *d.PlatformNumber != "" {
			plat = *d.PlatformNumber
		}
		t.Row(countdown, schedStr, estStr, plat, routeLabel(resp, d.RouteID), destinationFor(resp, d), delayStatus(d, isEst))
	}
	if err := t.Flush(); err != nil {
		return err
	}
	return nil
}

type modeShowOutput struct {
	Route       modeRouteOutput             `json:"route"`
	Directions  []modeDirectionOutput       `json:"directions"`
	Stops       map[string][]modeStopOutput `json:"stops"`
	Disruptions []disruptionOutput          `json:"disruptions"`
	TimeZone    string                      `json:"time_zone"`
}

type modeLinesOutput struct {
	Routes []modeRouteOutput `json:"routes"`
	Route  *modeRouteOutput  `json:"route"`
	Status modeStatusOutput  `json:"status"`
}

type modeRouteOutput struct {
	RouteType   int    `json:"route_type"`
	RouteID     int    `json:"route_id"`
	PTVRouteID  int    `json:"ptv_route_id"`
	RouteName   string `json:"route_name"`
	RouteNumber string `json:"route_number"`
	RouteGTFSID string `json:"route_gtfs_id,omitempty"`
}

type modeDirectionOutput struct {
	DirectionID               int    `json:"direction_id"`
	PTVDirectionID            int    `json:"ptv_direction_id"`
	DirectionName             string `json:"direction_name"`
	RouteDirectionDescription string `json:"route_direction_description,omitempty"`
	RouteID                   int    `json:"route_id"`
	PTVRouteID                int    `json:"ptv_route_id"`
	RouteType                 int    `json:"route_type"`
}

type modeStopOutput struct {
	StopID        int     `json:"stop_id"`
	PTVStopID     int     `json:"ptv_stop_id"`
	StopName      string  `json:"stop_name"`
	StopSuburb    string  `json:"stop_suburb"`
	RouteType     int     `json:"route_type"`
	StopLatitude  float64 `json:"stop_latitude"`
	StopLongitude float64 `json:"stop_longitude"`
	StopLandmark  string  `json:"stop_landmark,omitempty"`
	StopDistance  float64 `json:"stop_distance,omitempty"`
	StopSequence  int     `json:"stop_sequence"`
}

type modeStatusOutput struct {
	Version string `json:"version"`
	Health  int    `json:"health"`
}

func newModeShowOutput(
	route ptvapi.Route,
	directions []ptvapi.Direction,
	stops map[string][]ptvapi.StopModel,
	disruptions []ptvapi.Disruption,
) modeShowOutput {
	output := modeShowOutput{
		Route:       newModeRouteOutput(route),
		Directions:  make([]modeDirectionOutput, 0, len(directions)),
		Stops:       make(map[string][]modeStopOutput, len(stops)),
		Disruptions: make([]disruptionOutput, 0, len(disruptions)),
		TimeZone:    commandTimeZone,
	}
	for _, direction := range directions {
		output.Directions = append(output.Directions, modeDirectionOutput{
			DirectionID: direction.DirectionID, PTVDirectionID: direction.DirectionID,
			DirectionName:             normalizedText(direction.DirectionName),
			RouteDirectionDescription: normalizedText(direction.RouteDirectionDescription),
			RouteID:                   direction.RouteID, PTVRouteID: direction.RouteID, RouteType: direction.RouteType,
		})
	}
	for key, values := range stops {
		items := make([]modeStopOutput, 0, len(values))
		for _, stop := range values {
			items = append(items, modeStopOutput{
				StopID: stop.StopID, PTVStopID: stop.StopID,
				StopName: normalizedText(stop.StopName), StopSuburb: normalizedText(stop.StopSuburb),
				RouteType: stop.RouteType, StopLatitude: stop.StopLatitude, StopLongitude: stop.StopLongitude,
				StopLandmark: normalizedText(stop.StopLandmark), StopDistance: stop.StopDistance,
				StopSequence: stop.StopSequence,
			})
		}
		output.Stops[key] = items
	}
	for _, disruption := range disruptions {
		output.Disruptions = append(output.Disruptions, newDisruptionOutput(disruption))
	}
	return output
}

func newModeLinesOutput(response *ptvapi.RouteResponse) modeLinesOutput {
	output := modeLinesOutput{
		Routes: make([]modeRouteOutput, 0, len(response.Routes)),
		Status: modeStatusOutput{
			Version: normalizedText(response.Status.Version),
			Health:  response.Status.Health,
		},
	}
	for _, route := range response.Routes {
		output.Routes = append(output.Routes, newModeRouteOutput(route))
	}
	if response.Route != nil {
		route := newModeRouteOutput(*response.Route)
		output.Route = &route
	}
	return output
}

func newModeRouteOutput(route ptvapi.Route) modeRouteOutput {
	return modeRouteOutput{
		RouteType: route.RouteType, RouteID: route.RouteID, PTVRouteID: route.RouteID,
		RouteName: normalizedText(route.RouteName), RouteNumber: normalizedText(route.RouteNumber),
		RouteGTFSID: normalizedText(route.RouteGTFSID),
	}
}

// resolveRouteForMode resolves a route constrained to a route_type, matching by
// route number or name. Pulls the mode's route list once and matches locally so
// numeric tram/bus route *numbers* aren't confused with route ids.
func resolveRouteForMode(ctx context.Context, client *ptvapi.Client, routeType int, query string) (*ptvapi.Route, error) {
	query = strings.TrimSpace(query)
	resp, err := client.Routes(ctx, []int{routeType}, "")
	if err != nil {
		return nil, err
	}
	routes := resp.Routes
	if len(routes) == 0 {
		return nil, fmt.Errorf("no %s routes available", routeTypeName(routeType))
	}

	// Exact route number match (case-insensitive).
	for i := range routes {
		if strings.EqualFold(routes[i].RouteNumber, query) {
			return &routes[i], nil
		}
	}
	// Exact route name match.
	for i := range routes {
		if strings.EqualFold(routes[i].RouteName, query) {
			return &routes[i], nil
		}
	}
	// Numeric route id match.
	if id, err := strconv.Atoi(query); err == nil {
		for i := range routes {
			if routes[i].RouteID == id {
				return &routes[i], nil
			}
		}
	}
	// Substring match on name or number.
	var matches []*ptvapi.Route
	lq := strings.ToLower(query)
	for i := range routes {
		if strings.Contains(strings.ToLower(routes[i].RouteName), lq) ||
			strings.Contains(strings.ToLower(routes[i].RouteNumber), lq) {
			matches = append(matches, &routes[i])
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no %s route matching %q", routeTypeName(routeType), query)
	case 1:
		return matches[0], nil
	default:
		var names []string
		for _, r := range matches {
			label := r.RouteName
			if r.RouteNumber != "" {
				label = r.RouteNumber + " " + r.RouteName
			}
			names = append(names, fmt.Sprintf("%s (%d)", label, r.RouteID))
		}
		return nil, fmt.Errorf("%q is ambiguous; matches: %s", query, strings.Join(names, ", "))
	}
}

// flattenDisruptions flattens the mode-keyed disruptions map into a single slice.
func flattenDisruptions(resp *ptvapi.DisruptionsResponse) []ptvapi.Disruption {
	if resp == nil {
		return nil
	}
	var candidates []ptvapi.Disruption
	for _, list := range resp.Disruptions {
		candidates = append(candidates, list...)
	}
	// Sort before de-duplication so map iteration cannot decide which copy of a
	// repeated PTV disruption wins.
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].DisruptionID != candidates[j].DisruptionID {
			return candidates[i].DisruptionID < candidates[j].DisruptionID
		}
		if candidates[i].LastUpdated != candidates[j].LastUpdated {
			return candidates[i].LastUpdated > candidates[j].LastUpdated
		}
		if candidates[i].FromDate != candidates[j].FromDate {
			return candidates[i].FromDate < candidates[j].FromDate
		}
		return normalizedText(candidates[i].Title) < normalizedText(candidates[j].Title)
	})
	seen := make(map[int64]bool, len(candidates))
	out := make([]ptvapi.Disruption, 0, len(candidates))
	for _, disruption := range candidates {
		if seen[disruption.DisruptionID] {
			continue
		}
		seen[disruption.DisruptionID] = true
		out = append(out, disruption)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].FromDate != out[j].FromDate {
			return out[i].FromDate < out[j].FromDate
		}
		if normalizedText(out[i].Title) != normalizedText(out[j].Title) {
			return normalizedText(out[i].Title) < normalizedText(out[j].Title)
		}
		return out[i].DisruptionID < out[j].DisruptionID
	})
	return out
}

func init() {
	rootCmd.AddCommand(newModeCommand("train", "Train route info, stops, departures and disruptions", 0))
	rootCmd.AddCommand(newModeCommand("tram", "Tram route info, stops, departures and disruptions", 1))
	rootCmd.AddCommand(newModeCommand("bus", "Bus route info, stops, departures and disruptions", 2))
	rootCmd.AddCommand(newModeCommand("vline", "V/Line route info, stops, departures and disruptions", 3))
}
