package cmd

import (
	"fmt"
	"sort"

	"github.com/elsammykins/ptv_cli/internal/ptvapi"
	"github.com/elsammykins/ptv_cli/internal/render"
	"github.com/spf13/cobra"
)

var (
	disruptionsModes []string
	disruptionsRoute string
)

var disruptionsCmd = &cobra.Command{
	Use:   "disruptions",
	Short: "View current and planned service disruptions",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, _, err := loadClient()
		if err != nil {
			return err
		}

		var resp *ptvapi.DisruptionsResponse
		if disruptionsRoute != "" {
			route, rerr := resolveRoute(client, disruptionsRoute)
			if rerr != nil {
				return rerr
			}
			resp, err = client.DisruptionsForRoute(ctx(), route.RouteID)
		} else {
			routeTypes, terr := modesToTypes(disruptionsModes)
			if terr != nil {
				return terr
			}
			resp, err = client.DisruptionsAll(ctx(), routeTypes)
		}
		if err != nil {
			return err
		}
		if flagJSON {
			return printJSON(resp)
		}

		modes := make([]string, 0, len(resp.Disruptions))
		for m := range resp.Disruptions {
			modes = append(modes, m)
		}
		sort.Strings(modes)

		total := 0
		for _, m := range modes {
			items := resp.Disruptions[m]
			if len(items) == 0 {
				continue
			}
			fmt.Printf("\n%s\n", m)
			t := render.NewTable("STATUS", "TITLE")
			for _, d := range items {
				t.Row(d.DisruptionStatus, d.Title)
				total++
			}
			t.Flush()
		}
		if total == 0 {
			fmt.Println("No disruptions.")
		}
		return nil
	},
}

func init() {
	disruptionsCmd.Flags().StringSliceVar(&disruptionsModes, "mode", nil, "filter by mode(s)")
	disruptionsCmd.Flags().StringVar(&disruptionsRoute, "route", "", "filter by route id or name")
	rootCmd.AddCommand(disruptionsCmd)
}
