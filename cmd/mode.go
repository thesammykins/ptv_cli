package cmd

import (
	"fmt"
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

Show a full view of a %s route: its directions, ordered stops and any active
disruptions. Pass a route number (e.g. "109") or a route name.

Subcommands:
  %s lines           list all %s routes
  %s next "<stop>"   live departures from a stop (scoped to %s)`,
			short, modeName, use, modeName, use, modeName),
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("specify a route (e.g. %q) or a subcommand (lines, next)", use+" 109")
			}
			client, _, err := loadClient()
			if err != nil {
				return err
			}
			return runModeShow(client, routeType, joinArgs(args))
		},
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "lines",
		Short: fmt.Sprintf("List all %s routes", modeName),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := loadClient()
			if err != nil {
				return err
			}
			return runModeLines(client, routeType)
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
			return runModeNext(client, routeType, joinArgs(args))
		},
	}
	cmd.AddCommand(nextSub)

	return cmd
}

// runModeShow renders a route header, directions, ordered stops and disruptions.
func runModeShow(client *ptvapi.Client, routeType int, query string) error {
	route, err := resolveRouteForMode(client, routeType, query)
	if err != nil {
		return err
	}

	dirs, err := client.Directions(ctx(), route.RouteID)
	if err != nil {
		return err
	}

	dis, derr := client.DisruptionsForRoute(ctx(), route.RouteID)

	if flagJSON {
		out := map[string]any{
			"route":      route,
			"directions": dirs.Directions,
		}
		stopsByDir := map[string][]ptvapi.StopModel{}
		for _, d := range dirs.Directions {
			dID := d.DirectionID
			s, serr := client.StopsForRoute(ctx(), route.RouteID, route.RouteType, &dID)
			if serr != nil {
				return serr
			}
			stopsByDir[strconv.Itoa(d.DirectionID)] = s.Stops
		}
		out["stops"] = stopsByDir
		if derr == nil {
			out["disruptions"] = flattenDisruptions(dis)
		}
		return printJSON(out)
	}

	label := route.RouteName
	if route.RouteNumber != "" {
		label = fmt.Sprintf("%s — %s", route.RouteNumber, route.RouteName)
	}
	fmt.Printf("%s (%s)\n", label, routeTypeName(route.RouteType))
	fmt.Printf("Route ID: %d\n\n", route.RouteID)

	fmt.Println("Directions")
	dt := render.NewTable("ID", "NAME")
	for _, d := range dirs.Directions {
		dt.Row(d.DirectionID, d.DirectionName)
	}
	dt.Flush()
	fmt.Println()

	if len(dirs.Directions) > 0 {
		dID := dirs.Directions[0].DirectionID
		stops, serr := client.StopsForRoute(ctx(), route.RouteID, route.RouteType, &dID)
		if serr != nil {
			return serr
		}
		sort.Slice(stops.Stops, func(i, j int) bool {
			return stops.Stops[i].StopSequence < stops.Stops[j].StopSequence
		})
		fmt.Printf("Stops (towards %s)\n", dirs.Directions[0].DirectionName)
		st := render.NewTable("SEQ", "ID", "STOP", "SUBURB")
		for _, s := range stops.Stops {
			st.Row(s.StopSequence, s.StopID, s.StopName, s.StopSuburb)
		}
		st.Flush()
	}

	if derr == nil {
		ds := flattenDisruptions(dis)
		if len(ds) > 0 {
			fmt.Println("\nDisruptions")
			for _, d := range ds {
				fmt.Printf("  • [%s] %s\n", d.DisruptionStatus, d.Title)
				if d.URL != "" {
					fmt.Printf("    %s\n", d.URL)
				}
			}
		}
	}
	return nil
}

// runModeLines lists all routes for a mode.
func runModeLines(client *ptvapi.Client, routeType int) error {
	resp, err := client.Routes(ctx(), []int{routeType}, "")
	if err != nil {
		return err
	}
	if flagJSON {
		return printJSON(resp)
	}
	routes := resp.Routes
	sort.Slice(routes, func(i, j int) bool {
		return routes[i].RouteName < routes[j].RouteName
	})
	if flagLimit > 0 && len(routes) > flagLimit {
		routes = routes[:flagLimit]
	}
	t := render.NewTable("ID", "NUMBER", "NAME")
	for _, r := range routes {
		t.Row(r.RouteID, r.RouteNumber, r.RouteName)
	}
	t.Flush()
	fmt.Printf("\n%d %s routes\n", len(routes), routeTypeName(routeType))
	return nil
}

// runModeNext shows live departures from a stop, scoped to the mode.
func runModeNext(client *ptvapi.Client, routeType int, query string) error {
	stop, err := resolveStop(client, query, []int{routeType})
	if err != nil {
		return err
	}

	opts := ptvapi.DeparturesOptions{
		MaxResults: orDefault(flagLimit, 8),
		Expand:     []string{ptvapi.ExpandRoute, ptvapi.ExpandRun, ptvapi.ExpandDirection, ptvapi.ExpandDisruption},
	}
	resp, err := client.Departures(ctx(), routeType, stop.StopID, opts)
	if err != nil {
		return err
	}
	if flagJSON {
		return printJSON(resp)
	}

	deps := resp.Departures
	sort.Slice(deps, func(i, j int) bool {
		return departureSort(deps[i]) < departureSort(deps[j])
	})

	stopName := stop.StopName
	if s, ok := resp.Stops[strconv.Itoa(stop.StopID)]; ok && s.StopName != "" {
		stopName = s.StopName
	}
	fmt.Printf("Next departures — %s (%s)\n\n", stopName, routeTypeName(routeType))

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
	t.Flush()
	return nil
}

// resolveRouteForMode resolves a route constrained to a route_type, matching by
// route number or name. Pulls the mode's route list once and matches locally so
// numeric tram/bus route *numbers* aren't confused with route ids.
func resolveRouteForMode(client *ptvapi.Client, routeType int, query string) (*ptvapi.Route, error) {
	query = strings.TrimSpace(query)
	resp, err := client.Routes(ctx(), []int{routeType}, "")
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
	var out []ptvapi.Disruption
	seen := map[int64]bool{}
	for _, list := range resp.Disruptions {
		for _, d := range list {
			if seen[d.DisruptionID] {
				continue
			}
			seen[d.DisruptionID] = true
			out = append(out, d)
		}
	}
	return out
}

func init() {
	rootCmd.AddCommand(newModeCommand("tram", "Tram route info, stops, departures and disruptions", 1))
	rootCmd.AddCommand(newModeCommand("bus", "Bus route info, stops, departures and disruptions", 2))
	rootCmd.AddCommand(newModeCommand("vline", "V/Line route info, stops, departures and disruptions", 3))
}
