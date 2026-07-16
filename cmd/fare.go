package cmd

import (
	"fmt"
	"sort"
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
		resp, err := client.FareEstimate(cmd.Context(), fareMinZone, fareMaxZone, touchOn, touchOn.Add(2*time.Hour))
		if err != nil {
			return err
		}
		if err := fareEstimateResultError(resp); err != nil {
			return err
		}
		if fareEstimateAllZero(resp) {
			return fmt.Errorf("PTV returned a zero fare estimate for zones %d-%d; fare data is unavailable from the API", fareMinZone, fareMaxZone)
		}
		output := newFareOutput(resp)
		if flagJSON {
			return printJSON(output)
		}
		fmt.Printf("Fare estimate, zones %d–%d\n\n", fareMinZone, fareMaxZone)
		t := render.NewTable("PASSENGER", "2HR PEAK", "2HR OFF-PEAK", "DAILY PEAK", "DAILY OFF-PEAK")
		for _, p := range output.FareEstimateResult.PassengerFares {
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

// fareOutput preserves the Fare Estimate endpoint's established top-level
// casing while preventing its anonymous upstream structs from becoming the
// command contract.
type fareOutput struct {
	FareEstimateResultStatus fareResultStatusOutput `json:"FareEstimateResultStatus"`
	FareEstimateResult       fareResultOutput       `json:"FareEstimateResult"`
}

type fareResultStatusOutput struct {
	StatusCode int    `json:"StatusCode"`
	Message    string `json:"Message"`
}

type fareResultOutput struct {
	ZoneInfo       fareZoneOutput        `json:"ZoneInfo"`
	PassengerFares []farePassengerOutput `json:"PassengerFares"`
}

type fareZoneOutput struct {
	MinZone     int   `json:"MinZone"`
	MaxZone     int   `json:"MaxZone"`
	UniqueZones []int `json:"UniqueZones"`
}

type farePassengerOutput struct {
	PassengerType    string  `json:"PassengerType"`
	Fare2HourPeak    float64 `json:"Fare2HourPeak"`
	Fare2HourOffPeak float64 `json:"Fare2HourOffPeak"`
	FareDailyPeak    float64 `json:"FareDailyPeak"`
	FareDailyOffPeak float64 `json:"FareDailyOffPeak"`
}

func newFareOutput(response *ptvapi.FareEstimateResponse) fareOutput {
	output := fareOutput{
		FareEstimateResultStatus: fareResultStatusOutput{
			StatusCode: response.FareEstimateResultStatus.StatusCode,
			Message:    normalizedText(response.FareEstimateResultStatus.Message),
		},
		FareEstimateResult: fareResultOutput{
			ZoneInfo: fareZoneOutput{
				MinZone:     response.FareEstimateResult.ZoneInfo.MinZone,
				MaxZone:     response.FareEstimateResult.ZoneInfo.MaxZone,
				UniqueZones: append([]int{}, response.FareEstimateResult.ZoneInfo.UniqueZones...),
			},
			PassengerFares: make([]farePassengerOutput, 0, len(response.FareEstimateResult.PassengerFares)),
		},
	}
	for _, passenger := range response.FareEstimateResult.PassengerFares {
		output.FareEstimateResult.PassengerFares = append(output.FareEstimateResult.PassengerFares, farePassengerOutput{
			PassengerType: normalizedText(passenger.PassengerType),
			Fare2HourPeak: passenger.Fare2HourPeak, Fare2HourOffPeak: passenger.Fare2HourOffPeak,
			FareDailyPeak: passenger.FareDailyPeak, FareDailyOffPeak: passenger.FareDailyOffPeak,
		})
	}
	sort.SliceStable(output.FareEstimateResult.PassengerFares, func(i, j int) bool {
		return output.FareEstimateResult.PassengerFares[i].PassengerType <
			output.FareEstimateResult.PassengerFares[j].PassengerType
	})
	return output
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

func fareEstimateResultError(resp *ptvapi.FareEstimateResponse) error {
	if resp == nil {
		return fmt.Errorf("PTV fare estimate returned no result")
	}
	if resp.FareEstimateResultStatus.StatusCode == 0 {
		return nil
	}
	message := normalizedText(resp.FareEstimateResultStatus.Message)
	if message == "" {
		message = "unspecified upstream failure"
	}
	return fmt.Errorf("PTV fare estimate failed (status %d): %s", resp.FareEstimateResultStatus.StatusCode, message)
}

func init() {
	fareCmd.Flags().IntVar(&fareMinZone, "min-zone", 0, "minimum zone travelled through")
	fareCmd.Flags().IntVar(&fareMaxZone, "max-zone", 0, "maximum zone travelled through")
	rootCmd.AddCommand(fareCmd)
}
