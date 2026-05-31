package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
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
		client, _, err := loadClient()
		if err != nil {
			return err
		}

		var modeHint []int
		if stationMode != "" {
			rt, ok := parseMode(stationMode)
			if !ok {
				return fmt.Errorf("unknown mode %q", stationMode)
			}
			modeHint = []int{rt}
		}

		stop, err := resolveStationStop(client, joinArgs(args), modeHint)
		if err != nil {
			return err
		}
		if stop.RouteType < 0 {
			return fmt.Errorf("a numeric stop id needs --mode (train, tram, bus, vline, nightbus)")
		}

		resp, err := client.StopDetails(ctx(), stop.StopID, stop.RouteType)
		if err != nil {
			return err
		}
		if resp.Stop.StopLatitude == 0 && resp.Stop.StopLongitude == 0 {
			resp.Stop.StopLatitude = stop.StopLatitude
			resp.Stop.StopLongitude = stop.StopLongitude
		}
		if resp.Stop.StopLatitude == 0 && resp.Stop.StopLongitude == 0 {
			fillStationCoordinates(ctx(), client, &resp.Stop)
		}
		if flagJSON {
			return printJSON(resp)
		}

		d := resp.Stop
		name := d.StopName
		if name == "" {
			name = stop.StopName
		}
		fmt.Printf("%s\n", render.CleanText(name))
		fmt.Printf("Stop ID: %d   Mode: %s\n", stop.StopID, routeTypeName(stop.RouteType))
		if d.StationType != "" {
			fmt.Printf("Type: %s\n", render.CleanText(d.StationType))
		}
		if d.StationDescription != "" {
			fmt.Printf("%s\n", render.CleanText(d.StationDescription))
		}
		if d.StopLatitude != 0 || d.StopLongitude != 0 {
			fmt.Printf("Location: %.5f, %.5f\n", d.StopLatitude, d.StopLongitude)
		}

		if len(d.Routes) > 0 {
			fmt.Println("\nRoutes serving this stop")
			t := render.NewTable("ID", "NUMBER", "NAME", "MODE")
			for _, r := range deduplicateRoutes(d.Routes) {
				t.Row(r.RouteID, r.RouteNumber, r.RouteName, routeTypeName(r.RouteType))
			}
			if err := t.Flush(); err != nil {
				return err
			}
		}
		return nil
	},
}

func fillStationCoordinates(ctx context.Context, client *ptvapi.Client, stop *ptvapi.StopDetails) {
	name := strings.TrimSpace(stop.StopName)
	if name == "" {
		return
	}
	resp, err := client.Search(ctx, name, []int{stop.RouteType})
	if err != nil {
		return
	}
	for _, candidate := range resp.Stops {
		if candidate.StopID == stop.StopID && (candidate.StopLatitude != 0 || candidate.StopLongitude != 0) {
			stop.StopLatitude = candidate.StopLatitude
			stop.StopLongitude = candidate.StopLongitude
			return
		}
	}
	for _, candidate := range resp.Stops {
		if strings.EqualFold(strings.TrimSpace(candidate.StopName), name) && (candidate.StopLatitude != 0 || candidate.StopLongitude != 0) {
			stop.StopLatitude = candidate.StopLatitude
			stop.StopLongitude = candidate.StopLongitude
			return
		}
	}
}

// deduplicateRoutes removes duplicate routes by route_id, keeping the first
// occurrence. The PTV API sometimes returns the same route entry twice (once
// per direction) for stations served by multiple V/Line routes.
func deduplicateRoutes(routes []ptvapi.Route) []ptvapi.Route {
	seen := make(map[int]bool, len(routes))
	out := make([]ptvapi.Route, 0, len(routes))
	for _, r := range routes {
		if seen[r.RouteID] {
			continue
		}
		seen[r.RouteID] = true
		out = append(out, r)
	}
	return out
}

func init() {
	stationCmd.Flags().StringVar(&stationMode, "mode", "", "mode hint when passing a numeric stop id")
	rootCmd.AddCommand(stationCmd)
}
