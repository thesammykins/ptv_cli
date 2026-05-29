package cmd

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/thesammykins/ptv_cli/internal/ptvapi"
	"github.com/thesammykins/ptv_cli/internal/render"
)

var linesModes []string

var linesCmd = &cobra.Command{
	Use:   "lines",
	Short: "List transport lines/routes",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, _, err := loadClient()
		if err != nil {
			return err
		}
		routeTypes, err := modesToTypes(linesModes)
		if err != nil {
			return err
		}
		if len(args) > 0 {
			return runLineShow(client, joinArgs(args), routeTypes)
		}
		resp, err := client.Routes(ctx(), routeTypes, "")
		if err != nil {
			return err
		}
		if flagJSON {
			return printJSON(resp)
		}
		routes := resp.Routes
		sort.Slice(routes, func(i, j int) bool {
			if routes[i].RouteType != routes[j].RouteType {
				return routes[i].RouteType < routes[j].RouteType
			}
			return routes[i].RouteName < routes[j].RouteName
		})
		if flagLimit > 0 && len(routes) > flagLimit {
			routes = routes[:flagLimit]
		}
		t := render.NewTable("ID", "NUMBER", "NAME", "MODE")
		for _, r := range routes {
			t.Row(r.RouteID, r.RouteNumber, r.RouteName, routeTypeName(r.RouteType))
		}
		t.Flush()
		fmt.Printf("\n%d routes\n", len(routes))
		return nil
	},
}

var linesShowCmd = &cobra.Command{
	Use:   "show <route-id|name>",
	Short: "Show directions and stops for a line",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, _, err := loadClient()
		if err != nil {
			return err
		}
		routeTypes, err := modesToTypes(linesModes)
		if err != nil {
			return err
		}
		return runLineShow(client, joinArgs(args), routeTypes)
	},
}

func runLineShow(client *ptvapi.Client, query string, routeTypes []int) error {
	route, err := resolveRouteWithTypes(client, query, routeTypes)
	if err != nil {
		return err
	}

	dirs, err := client.Directions(ctx(), route.RouteID)
	if err != nil {
		return err
	}

	if flagJSON {
		out := map[string]any{"route": route, "directions": dirs.Directions}
		stopsByDir := map[string][]ptvapi.StopModel{}
		for _, d := range dirs.Directions {
			dID := d.DirectionID
			s, err := client.StopsForRoute(ctx(), route.RouteID, route.RouteType, &dID)
			if err != nil {
				return err
			}
			stopsByDir[strconv.Itoa(d.DirectionID)] = s.Stops
		}
		out["stops"] = stopsByDir
		return printJSON(out)
	}

	fmt.Printf("%s — %s (%s)\n", route.RouteName, route.RouteNumber, routeTypeName(route.RouteType))
	fmt.Printf("Route ID: %d\n\n", route.RouteID)

	fmt.Println("Directions")
	dt := render.NewTable("ID", "NAME")
	for _, d := range dirs.Directions {
		dt.Row(d.DirectionID, d.DirectionName)
	}
	dt.Flush()
	fmt.Println()

	// Stops in the first direction give the line's stop order.
	if len(dirs.Directions) > 0 {
		dID := dirs.Directions[0].DirectionID
		stops, err := client.StopsForRoute(ctx(), route.RouteID, route.RouteType, &dID)
		if err != nil {
			return err
		}
		sortStopsBySequence(stops.Stops)
		fmt.Printf("Stops (towards %s)\n", dirs.Directions[0].DirectionName)
		st := render.NewTable("SEQ", "ID", "STOP", "SUBURB")
		for _, s := range stops.Stops {
			st.Row(s.StopSequence, s.StopID, s.StopName, s.StopSuburb)
		}
		st.Flush()
	}
	return nil
}

func init() {
	linesCmd.Flags().StringSliceVar(&linesModes, "mode", nil, "filter by mode(s): train,tram,bus,vline,nightbus")
	linesCmd.AddCommand(linesShowCmd)
	rootCmd.AddCommand(linesCmd)
}
