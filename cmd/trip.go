package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/thesammykins/ptv_cli/internal/gtfs"
	"github.com/thesammykins/ptv_cli/internal/localtime"
	"github.com/thesammykins/ptv_cli/internal/render"
)

var tripDate string

type tripOutput struct {
	Trip        *gtfs.TripDetailResult `json:"trip"`
	ServiceDate string                 `json:"service_date"`
	DataSource  string                 `json:"data_source"`
	Freshness   freshnessOutput        `json:"freshness"`
	Warnings    []string               `json:"warnings"`
}

var tripCmd = &cobra.Command{
	Use: "trip <trip-id>", Short: "Show a scheduled trip stopping pattern", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		sources, err := resolveSources(cmd.Context())
		if err != nil {
			return err
		}
		defer closeSources(sources)
		date, err := parseServiceDate(tripDate)
		if err != nil {
			return err
		}
		trip, err := sources.GTFSStore.TripDetail(cmd.Context(), strings.TrimSpace(args[0]), date)
		if err != nil {
			return err
		}
		output := tripOutput{Trip: trip, ServiceDate: date.Format("20060102"), DataSource: "gtfs_static", Freshness: currentGTFSFreshness(cmd.Context(), sources.GTFSStore), Warnings: []string{}}
		if flagJSON {
			return printJSON(output)
		}
		fmt.Printf("Trip %s — %s\n\n", render.CleanText(trip.TripID), render.CleanText(trip.Headsign))
		t := render.NewTable("SEQ", "STOP", "ARR", "DEP")
		anchor := localtime.ServiceDayAnchor(date)
		for _, stop := range trip.Stops {
			t.Row(stop.StopSequence, stop.StopName, anchor.Add(time.Duration(stop.ArrivalSec)*time.Second).Format("15:04 MST"), anchor.Add(time.Duration(stop.DepartureSec)*time.Second).Format("15:04 MST"))
		}
		return t.Flush()
	},
}

func init() {
	tripCmd.Flags().StringVar(&tripDate, "date", "", "service date in YYYY-MM-DD (Melbourne time)")
	rootCmd.AddCommand(tripCmd)
}
