package cmd

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/spf13/cobra"
	"github.com/thesammykins/ptv_cli/internal/ptvapi"
	"github.com/thesammykins/ptv_cli/internal/render"
)

var linesModes []string

var linesCmd = &cobra.Command{
	Use:   "lines",
	Short: "List transport lines/routes",
	RunE: func(cmd *cobra.Command, args []string) error {
		sources, err := resolveSources(cmd.Context())
		if err != nil {
			return err
		}
		defer closeSources(sources)
		routeTypes, err := modesToTypes(linesModes)
		if err != nil {
			return err
		}
		if len(args) > 0 {
			return runLineShowGTFS(cmd.Context(), sources.GTFSStore, joinArgs(args), gtfsFeedModes(routeTypes), sources.GTFSFreshness)
		}
		routes, err := sources.GTFSStore.RoutesByMode(cmd.Context(), gtfsFeedModes(routeTypes))
		if err != nil {
			return err
		}
		if flagLimit > 0 && len(routes) > flagLimit {
			routes = routes[:flagLimit]
		}
		output := newGTFSLinesListOutput(cmd.Context(), sources.GTFSStore, routes, sources.GTFSFreshness)
		if flagJSON {
			return printJSON(output)
		}
		t := render.NewTable("ID", "NUMBER", "NAME", "MODE")
		for _, r := range output.Routes {
			t.Row(r.GTFSRouteID, r.RouteNumber, r.RouteName, r.Mode)
		}
		if err := t.Flush(); err != nil {
			return err
		}
		fmt.Printf("\n%d routes\n", len(output.Routes))
		return nil
	},
}

var linesShowCmd = &cobra.Command{
	Use:   "show <route-id|name>",
	Short: "Show directions and stops for a line",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		sources, err := resolveSources(cmd.Context())
		if err != nil {
			return err
		}
		defer closeSources(sources)
		routeTypes, err := modesToTypes(linesModes)
		if err != nil {
			return err
		}
		return runLineShowGTFS(cmd.Context(), sources.GTFSStore, joinArgs(args), gtfsFeedModes(routeTypes), sources.GTFSFreshness)
	},
}

func runLineShow(ctx context.Context, client *ptvapi.Client, query string, routeTypes []int) error {
	route, err := resolveRouteWithTypesContext(ctx, client, query, routeTypes)
	if err != nil {
		return err
	}

	dirs, err := client.Directions(ctx, route.RouteID)
	if err != nil {
		return err
	}
	sortLineDirections(dirs.Directions)

	if flagJSON {
		stopsByDir := map[string][]ptvapi.StopModel{}
		for _, d := range dirs.Directions {
			dID := d.DirectionID
			s, err := client.StopsForRoute(ctx, route.RouteID, route.RouteType, &dID)
			if err != nil {
				return err
			}
			sortStopsBySequence(s.Stops)
			stopsByDir[strconv.Itoa(d.DirectionID)] = limitStops(s.Stops)
		}
		return printJSON(newLinesShowOutput(*route, dirs.Directions, stopsByDir))
	}

	outputRoute := newLineRouteOutput(*route)
	fmt.Printf("%s — %s (%s)\n", outputRoute.RouteName, outputRoute.RouteNumber, routeTypeName(outputRoute.RouteType))
	fmt.Printf("Route ID: %d\n\n", outputRoute.RouteID)

	fmt.Println("Directions")
	dt := render.NewTable("ID", "NAME")
	for _, d := range dirs.Directions {
		output := newLineDirectionOutput(d)
		dt.Row(output.DirectionID, output.DirectionName)
	}
	if err := dt.Flush(); err != nil {
		return err
	}
	fmt.Println()

	// Stops in the first direction give the known line order. Upstream sequence
	// zero is retained as unsequenced and sorted after numbered rows.
	if len(dirs.Directions) > 0 {
		dID := dirs.Directions[0].DirectionID
		stops, err := client.StopsForRoute(ctx, route.RouteID, route.RouteType, &dID)
		if err != nil {
			return err
		}
		sortStopsBySequence(stops.Stops)
		stops.Stops = limitStops(stops.Stops)
		fmt.Printf("Stops (towards %s)\n", normalizedText(dirs.Directions[0].DirectionName))
		st := render.NewTable("SEQ", "ID", "STOP", "SUBURB")
		for _, s := range stops.Stops {
			output := newLineStopOutput(s)
			st.Row(stopSequenceLabel(output.StopSequence), output.StopID, output.StopName, output.StopSuburb)
		}
		if err := st.Flush(); err != nil {
			return err
		}
	}
	return nil
}

// linesListOutput preserves the Routes endpoint's current-major top-level
// shape while ensuring the command, rather than the upstream client, owns the
// JSON contract.
type linesListOutput struct {
	Routes     []lineRouteOutput `json:"routes"`
	Route      *lineRouteOutput  `json:"route"`
	Status     lineStatusOutput  `json:"status"`
	DataSource string            `json:"data_source,omitempty"`
	Freshness  *freshnessOutput  `json:"freshness,omitempty"`
	Warnings   []string          `json:"warnings,omitempty"`
}

type linesShowOutput struct {
	Route      lineRouteOutput             `json:"route"`
	Directions []lineDirectionOutput       `json:"directions"`
	Stops      map[string][]lineStopOutput `json:"stops"`
	DataSource string                      `json:"data_source,omitempty"`
	Freshness  *freshnessOutput            `json:"freshness,omitempty"`
	Warnings   []string                    `json:"warnings,omitempty"`
}

type lineRouteOutput struct {
	RouteType   int    `json:"route_type"`
	RouteID     int    `json:"route_id"`
	PTVRouteID  int    `json:"ptv_route_id"`
	RouteName   string `json:"route_name"`
	RouteNumber string `json:"route_number"`
	RouteGTFSID string `json:"route_gtfs_id,omitempty"`
	GTFSRouteID string `json:"gtfs_route_id,omitempty"`
	Mode        string `json:"mode,omitempty"`
}

type lineDirectionOutput struct {
	DirectionID               int    `json:"direction_id"`
	PTVDirectionID            int    `json:"ptv_direction_id"`
	DirectionName             string `json:"direction_name"`
	RouteDirectionDescription string `json:"route_direction_description,omitempty"`
	RouteID                   int    `json:"route_id"`
	PTVRouteID                int    `json:"ptv_route_id"`
	RouteType                 int    `json:"route_type"`
}

type lineStopOutput struct {
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
	GTFSStopID    string  `json:"gtfs_stop_id,omitempty"`
}

type lineStatusOutput struct {
	Version string `json:"version"`
	Health  int    `json:"health"`
}

func newLinesListOutput(response *ptvapi.RouteResponse) linesListOutput {
	output := linesListOutput{
		Routes: make([]lineRouteOutput, 0, len(response.Routes)),
		Status: lineStatusOutput{
			Version: normalizedText(response.Status.Version),
			Health:  response.Status.Health,
		},
	}
	for _, route := range response.Routes {
		output.Routes = append(output.Routes, newLineRouteOutput(route))
	}
	if response.Route != nil {
		route := newLineRouteOutput(*response.Route)
		output.Route = &route
	}
	return output
}

func newLinesShowOutput(route ptvapi.Route, directions []ptvapi.Direction, stops map[string][]ptvapi.StopModel) linesShowOutput {
	output := linesShowOutput{
		Route:      newLineRouteOutput(route),
		Directions: make([]lineDirectionOutput, 0, len(directions)),
		Stops:      make(map[string][]lineStopOutput, len(stops)),
	}
	for _, direction := range directions {
		output.Directions = append(output.Directions, newLineDirectionOutput(direction))
	}
	for key, values := range stops {
		items := make([]lineStopOutput, 0, len(values))
		for _, stop := range values {
			items = append(items, newLineStopOutput(stop))
		}
		output.Stops[key] = items
	}
	return output
}

func newLineRouteOutput(route ptvapi.Route) lineRouteOutput {
	return lineRouteOutput{
		RouteType:   route.RouteType,
		RouteID:     route.RouteID,
		PTVRouteID:  route.RouteID,
		RouteName:   normalizedText(route.RouteName),
		RouteNumber: normalizedText(route.RouteNumber),
		RouteGTFSID: normalizedText(route.RouteGTFSID),
	}
}

func newLineDirectionOutput(direction ptvapi.Direction) lineDirectionOutput {
	return lineDirectionOutput{
		DirectionID:               direction.DirectionID,
		PTVDirectionID:            direction.DirectionID,
		DirectionName:             normalizedText(direction.DirectionName),
		RouteDirectionDescription: normalizedText(direction.RouteDirectionDescription),
		RouteID:                   direction.RouteID,
		PTVRouteID:                direction.RouteID,
		RouteType:                 direction.RouteType,
	}
}

func newLineStopOutput(stop ptvapi.StopModel) lineStopOutput {
	return lineStopOutput{
		StopID:        stop.StopID,
		PTVStopID:     stop.StopID,
		StopName:      normalizedText(stop.StopName),
		StopSuburb:    normalizedText(stop.StopSuburb),
		RouteType:     stop.RouteType,
		StopLatitude:  stop.StopLatitude,
		StopLongitude: stop.StopLongitude,
		StopLandmark:  normalizedText(stop.StopLandmark),
		StopDistance:  stop.StopDistance,
		StopSequence:  stop.StopSequence,
	}
}

func sortLineRoutes(routes []ptvapi.Route) {
	sort.SliceStable(routes, func(i, j int) bool {
		if routes[i].RouteType != routes[j].RouteType {
			return routes[i].RouteType < routes[j].RouteType
		}
		if normalizedText(routes[i].RouteNumber) != normalizedText(routes[j].RouteNumber) {
			return normalizedText(routes[i].RouteNumber) < normalizedText(routes[j].RouteNumber)
		}
		if normalizedText(routes[i].RouteName) != normalizedText(routes[j].RouteName) {
			return normalizedText(routes[i].RouteName) < normalizedText(routes[j].RouteName)
		}
		return routes[i].RouteID < routes[j].RouteID
	})
}

func sortLineDirections(directions []ptvapi.Direction) {
	sort.SliceStable(directions, func(i, j int) bool {
		if directions[i].DirectionID != directions[j].DirectionID {
			return directions[i].DirectionID < directions[j].DirectionID
		}
		return normalizedText(directions[i].DirectionName) < normalizedText(directions[j].DirectionName)
	})
}

func normalizedText(value string) string {
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	return strings.Join(strings.Fields(render.CleanText(cleaned)), " ")
}

func normalizedString(value *string) *string {
	if value == nil {
		return nil
	}
	cleaned := normalizedText(*value)
	if cleaned == "" {
		return nil
	}
	return &cleaned
}

// resolveRouteWithTypesContext mirrors the established route resolution
// contract while making the command context explicit for every HTTP request.
func resolveRouteWithTypesContext(ctx context.Context, client *ptvapi.Client, query string, routeTypes []int) (*ptvapi.Route, error) {
	query = strings.TrimSpace(query)
	resp, err := client.Routes(ctx, routeTypes, "")
	if err != nil {
		return nil, err
	}
	routes := resp.Routes

	if len(routeTypes) > 0 {
		for i := range routes {
			if strings.EqualFold(routes[i].RouteNumber, query) {
				return &routes[i], nil
			}
		}
	}
	if id, idErr := strconv.Atoi(query); idErr == nil {
		for i := range routes {
			if routes[i].RouteID == id {
				return &routes[i], nil
			}
		}
		if len(routeTypes) == 0 {
			single, requestErr := client.Route(ctx, id)
			if requestErr != nil {
				return nil, requestErr
			}
			if single.Route != nil {
				return single.Route, nil
			}
		}
	}
	if len(routes) == 0 {
		return nil, fmt.Errorf("no route matching %q", query)
	}
	for i := range routes {
		if strings.EqualFold(routes[i].RouteName, query) {
			return &routes[i], nil
		}
	}
	var matches []ptvapi.Route
	lower := strings.ToLower(query)
	for _, route := range routes {
		if strings.Contains(strings.ToLower(route.RouteName), lower) || strings.Contains(strings.ToLower(route.RouteNumber), lower) {
			matches = append(matches, route)
		}
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no route matching %q", query)
	}
	if len(matches) > 1 {
		names := make([]string, 0, len(matches))
		for _, route := range matches {
			label := route.RouteName
			if route.RouteNumber != "" {
				label = route.RouteNumber + " " + label
			}
			names = append(names, fmt.Sprintf("%s (%d)", label, route.RouteID))
		}
		return nil, fmt.Errorf("%q is ambiguous; matches: %s", query, strings.Join(names, ", "))
	}
	return &matches[0], nil
}

func init() {
	linesCmd.PersistentFlags().StringSliceVar(&linesModes, "mode", nil, "filter by mode(s): train,tram,bus,vline,nightbus")
	linesCmd.AddCommand(linesShowCmd)
	rootCmd.AddCommand(linesCmd)
}
