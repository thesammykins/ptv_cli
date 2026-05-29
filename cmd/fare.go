package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/thesammykins/ptv_cli/internal/render"
)

var (
	fareMinZone int
	fareMaxZone int
)

var fareCmd = &cobra.Command{
	Use:   "fare",
	Short: "Estimate a myki fare by zone",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, _, err := loadClient()
		if err != nil {
			return err
		}
		if fareMinZone <= 0 || fareMaxZone <= 0 {
			return fmt.Errorf("provide --min-zone and --max-zone (e.g. --min-zone 1 --max-zone 2)")
		}
		resp, err := client.FareEstimate(ctx(), fareMinZone, fareMaxZone)
		if err != nil {
			return err
		}
		if flagJSON {
			return printJSON(resp)
		}
		fmt.Printf("Fare estimate, zones %d–%d\n\n", fareMinZone, fareMaxZone)
		t := render.NewTable("PASSENGER", "2HR PEAK", "2HR OFF-PEAK", "DAILY PEAK", "DAILY OFF-PEAK")
		for _, p := range resp.FareEstimateResult.PassengerFares {
			t.Row(p.PassengerType,
				fmt.Sprintf("$%.2f", p.Fare2HourPeak),
				fmt.Sprintf("$%.2f", p.Fare2HourOffPeak),
				fmt.Sprintf("$%.2f", p.FareDailyPeak),
				fmt.Sprintf("$%.2f", p.FareDailyOffPeak))
		}
		t.Flush()
		return nil
	},
}

func init() {
	fareCmd.Flags().IntVar(&fareMinZone, "min-zone", 0, "minimum zone travelled through")
	fareCmd.Flags().IntVar(&fareMaxZone, "max-zone", 0, "maximum zone travelled through")
	rootCmd.AddCommand(fareCmd)
}
