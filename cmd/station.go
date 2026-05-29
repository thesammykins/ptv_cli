package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
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
		if flagJSON {
			return printJSON(resp)
		}

		d := resp.Stop
		name := d.StopName
		if name == "" {
			name = stop.StopName
		}
		fmt.Printf("%s\n", name)
		fmt.Printf("Stop ID: %d   Mode: %s\n", stop.StopID, routeTypeName(stop.RouteType))
		if d.StationType != "" {
			fmt.Printf("Type: %s\n", d.StationType)
		}
		if d.StationDescription != "" {
			fmt.Printf("%s\n", d.StationDescription)
		}
		if d.StopLatitude != 0 || d.StopLongitude != 0 {
			fmt.Printf("Location: %.5f, %.5f\n", d.StopLatitude, d.StopLongitude)
		}

		if len(d.Routes) > 0 {
			fmt.Println("\nRoutes serving this stop")
			t := render.NewTable("ID", "NUMBER", "NAME", "MODE")
			for _, r := range d.Routes {
				t.Row(r.RouteID, r.RouteNumber, r.RouteName, routeTypeName(r.RouteType))
			}
			t.Flush()
		}
		return nil
	},
}

func init() {
	stationCmd.Flags().StringVar(&stationMode, "mode", "", "mode hint when passing a numeric stop id")
	rootCmd.AddCommand(stationCmd)
}
