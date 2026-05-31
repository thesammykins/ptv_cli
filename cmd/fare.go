package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/thesammykins/ptv_cli/internal/ptvapi"
	"github.com/thesammykins/ptv_cli/internal/render"
)

var (
	fareMinZone int
	fareMaxZone int
)

var fareCmd = &cobra.Command{
	Use:   "fare",
	Short: "Estimate a myki fare by zone",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, _, err := loadClient()
		if err != nil {
			return err
		}
		if fareMinZone <= 0 || fareMaxZone <= 0 {
			return fmt.Errorf("provide --min-zone and --max-zone (e.g. --min-zone 1 --max-zone 2)")
		}
		if fareMinZone > fareMaxZone {
			return fmt.Errorf("--min-zone must be less than or equal to --max-zone")
		}
		touchOn := time.Now().UTC()
		resp, err := client.FareEstimate(ctx(), fareMinZone, fareMaxZone, touchOn, touchOn.Add(2*time.Hour))
		if err != nil {
			return err
		}
		if fareEstimateAllZero(resp) {
			return fmt.Errorf("PTV returned a zero fare estimate for zones %d-%d; fare data is unavailable from the API", fareMinZone, fareMaxZone)
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
		if err := t.Flush(); err != nil {
			return err
		}
		return nil
	},
}

func fareEstimateAllZero(resp *ptvapi.FareEstimateResponse) bool {
	if resp == nil || len(resp.FareEstimateResult.PassengerFares) == 0 {
		return false
	}
	if resp.FareEstimateResult.ZoneInfo.MinZone == 0 && resp.FareEstimateResult.ZoneInfo.MaxZone == 0 {
		return false
	}
	for _, p := range resp.FareEstimateResult.PassengerFares {
		if p.Fare2HourPeak != 0 || p.Fare2HourOffPeak != 0 || p.FareDailyPeak != 0 || p.FareDailyOffPeak != 0 {
			return false
		}
	}
	return true
}

func init() {
	fareCmd.Flags().IntVar(&fareMinZone, "min-zone", 0, "minimum zone travelled through")
	fareCmd.Flags().IntVar(&fareMaxZone, "max-zone", 0, "maximum zone travelled through")
	rootCmd.AddCommand(fareCmd)
}
