package cmd

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/thesammykins/ptv_cli/internal/geocode"
	"github.com/thesammykins/ptv_cli/internal/render"
)

var (
	stopsModes       []string
	stopsMaxDistance float64
)

var stopsCmd = &cobra.Command{
	Use:   "stops",
	Short: "Find stops near a location or on a route",
}

var stopsNearCmd = &cobra.Command{
	Use:   "near <lat,lng|place>",
	Short: "List stops near coordinates, a place or an address",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, cfg, err := loadClient()
		if err != nil {
			return err
		}
		lat, lng, err := parseLatLng(args[0])
		if err != nil {
			place, gerr := geocode.New(cfg.DataDir).Lookup(ctx(), args[0])
			if gerr != nil {
				return fmt.Errorf("expected lat,lng or geocodable place: %w", gerr)
			}
			lat, lng = place.Lat, place.Lon
		}
		routeTypes, err := modesToTypes(stopsModes)
		if err != nil {
			return err
		}
		resp, err := client.StopsNearLocation(ctx(), lat, lng, routeTypes, flagLimit, stopsMaxDistance)
		if err != nil {
			return err
		}
		resp.Stops = limitStops(resp.Stops)
		if flagJSON {
			return printJSON(resp)
		}
		sort.Slice(resp.Stops, func(i, j int) bool {
			return resp.Stops[i].StopDistance < resp.Stops[j].StopDistance
		})
		t := render.NewTable("ID", "STOP", "SUBURB", "MODE", "DIST(m)")
		for _, s := range resp.Stops {
			t.Row(s.StopID, s.StopName, s.StopSuburb, routeTypeName(s.RouteType), fmt.Sprintf("%.0f", s.StopDistance))
		}
		if err := t.Flush(); err != nil {
			return err
		}
		return nil
	},
}

var stopsOnModes []string

var stopsOnCmd = &cobra.Command{
	Use:   "on <route-id|name>",
	Short: "List all stops on a route",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, _, err := loadClient()
		if err != nil {
			return err
		}
		routeTypes, err := modesToTypes(stopsOnModes)
		if err != nil {
			return err
		}
		route, err := resolveRouteWithTypes(client, joinArgs(args), routeTypes)
		if err != nil {
			return err
		}
		resp, err := client.StopsForRoute(ctx(), route.RouteID, route.RouteType, nil)
		if err != nil {
			return err
		}
		if flagJSON {
			return printJSON(resp)
		}
		sortStopsBySequence(resp.Stops)
		fmt.Printf("Stops on %s (%s)\n", render.CleanText(route.RouteName), routeTypeName(route.RouteType))
		t := render.NewTable("ID", "STOP", "SUBURB")
		for _, s := range resp.Stops {
			t.Row(s.StopID, s.StopName, s.StopSuburb)
		}
		if err := t.Flush(); err != nil {
			return err
		}
		return nil
	},
}

// parseLatLng parses a "lat,lng" string.
func parseLatLng(s string) (float64, float64, error) {
	parts := strings.Split(s, ",")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("expected lat,lng (e.g. -37.818,144.952)")
	}
	lat, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid latitude: %w", err)
	}
	lng, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid longitude: %w", err)
	}
	return lat, lng, nil
}

func init() {
	stopsNearCmd.Flags().StringSliceVar(&stopsModes, "mode", nil, "filter by mode(s)")
	stopsNearCmd.Flags().Float64Var(&stopsMaxDistance, "max-distance", 0, "max distance in metres (default 300)")
	stopsOnCmd.Flags().StringSliceVar(&stopsOnModes, "mode", nil, "filter by mode(s)")
	stopsCmd.AddCommand(stopsNearCmd, stopsOnCmd)
	rootCmd.AddCommand(stopsCmd)
}
