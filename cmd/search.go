package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/thesammykins/ptv_cli/internal/render"
)

var searchModes []string

var searchCmd = &cobra.Command{
	Use:   "search <term>",
	Short: "Search stops, routes and outlets",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, _, err := loadClient()
		if err != nil {
			return err
		}
		term := joinArgs(args)

		routeTypes, err := modesToTypes(searchModes)
		if err != nil {
			return err
		}

		resp, err := client.Search(ctx(), term, routeTypes)
		if err != nil {
			return err
		}
		if flagJSON {
			return printJSON(resp)
		}

		if len(resp.Stops) > 0 {
			fmt.Println("Stops")
			t := render.NewTable("ID", "NAME", "SUBURB", "MODE")
			for _, s := range limitStops(resp.Stops) {
				t.Row(s.StopID, s.StopName, s.StopSuburb, routeTypeName(s.RouteType))
			}
			t.Flush()
			fmt.Println()
		}
		if len(resp.Routes) > 0 {
			fmt.Println("Routes")
			t := render.NewTable("ID", "NUMBER", "NAME", "MODE")
			for _, r := range resp.Routes {
				t.Row(r.RouteID, r.RouteNumber, r.RouteName, routeTypeName(r.RouteType))
			}
			t.Flush()
			fmt.Println()
		}
		if len(resp.Outlets) > 0 {
			fmt.Println("Outlets")
			t := render.NewTable("NAME", "BUSINESS", "SUBURB")
			for _, o := range resp.Outlets {
				t.Row(o.OutletName, o.OutletBusiness, o.OutletSuburb)
			}
			t.Flush()
		}
		if len(resp.Stops) == 0 && len(resp.Routes) == 0 && len(resp.Outlets) == 0 {
			fmt.Println("No results.")
		}
		return nil
	},
}

func init() {
	searchCmd.Flags().StringSliceVar(&searchModes, "mode", nil, "filter by mode(s): train,tram,bus,vline,nightbus")
	rootCmd.AddCommand(searchCmd)
}
