package cmd

import (
	"sort"

	"github.com/spf13/cobra"
	"github.com/thesammykins/ptv_cli/internal/ptvapi"
	"github.com/thesammykins/ptv_cli/internal/render"
)

var outletsCmd = &cobra.Command{
	Use:   "outlets [term]",
	Short: "List or search myki ticket outlets",
	Args:  cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, _, err := loadClient()
		if err != nil {
			return err
		}
		var outlets []ptvapi.ResultOutlet
		var status ptvapi.Status
		if len(args) > 0 {
			resp, err := client.Search(cmd.Context(), joinArgs(args), nil)
			if err != nil {
				return err
			}
			outlets = resp.Outlets
			status = resp.Status
		} else {
			resp, err := client.Outlets(cmd.Context(), flagLimit)
			if err != nil {
				return err
			}
			outlets = resp.Outlets
			status = resp.Status
		}
		sortOutlets(outlets)
		outlets = limitOutlets(outlets)
		output := newOutletsOutput(outlets, status)
		if flagJSON {
			return printJSON(output)
		}
		t := render.NewTable("NAME", "BUSINESS", "SUBURB")
		for _, o := range output.Outlets {
			t.Row(o.OutletName, o.OutletBusiness, o.OutletSuburb)
		}
		if err := t.Flush(); err != nil {
			return err
		}
		return nil
	},
}

type outletsOutput struct {
	Outlets []outletOutput     `json:"outlets"`
	Status  outletStatusOutput `json:"status"`
}

// outletOutput intentionally excludes outlet_slid_spid, an upstream lookup
// value that is not a stable public outlet identifier.
type outletOutput struct {
	OutletName      string  `json:"outlet_name"`
	OutletBusiness  string  `json:"outlet_business"`
	OutletLatitude  float64 `json:"outlet_latitude"`
	OutletLongitude float64 `json:"outlet_longitude"`
	OutletSuburb    string  `json:"outlet_suburb"`
	OutletDistance  float64 `json:"outlet_distance,omitempty"`
}

type outletStatusOutput struct {
	Version string `json:"version"`
	Health  int    `json:"health"`
}

func newOutletsOutput(outlets []ptvapi.ResultOutlet, status ptvapi.Status) outletsOutput {
	output := outletsOutput{
		Outlets: make([]outletOutput, 0, len(outlets)),
		Status: outletStatusOutput{
			Version: normalizedText(status.Version),
			Health:  status.Health,
		},
	}
	for _, outlet := range outlets {
		output.Outlets = append(output.Outlets, outletOutput{
			OutletName: normalizedText(outlet.OutletName), OutletBusiness: normalizedText(outlet.OutletBusiness),
			OutletLatitude: outlet.OutletLatitude, OutletLongitude: outlet.OutletLongitude,
			OutletSuburb: normalizedText(outlet.OutletSuburb), OutletDistance: outlet.OutletDistance,
		})
	}
	return output
}

func sortOutlets(outlets []ptvapi.ResultOutlet) {
	sort.SliceStable(outlets, func(i, j int) bool {
		left, right := outlets[i], outlets[j]
		if normalizedText(left.OutletSuburb) != normalizedText(right.OutletSuburb) {
			return normalizedText(left.OutletSuburb) < normalizedText(right.OutletSuburb)
		}
		if normalizedText(left.OutletName) != normalizedText(right.OutletName) {
			return normalizedText(left.OutletName) < normalizedText(right.OutletName)
		}
		if normalizedText(left.OutletBusiness) != normalizedText(right.OutletBusiness) {
			return normalizedText(left.OutletBusiness) < normalizedText(right.OutletBusiness)
		}
		if left.OutletLatitude != right.OutletLatitude {
			return left.OutletLatitude < right.OutletLatitude
		}
		if left.OutletLongitude != right.OutletLongitude {
			return left.OutletLongitude < right.OutletLongitude
		}
		return left.OutletDistance < right.OutletDistance
	})
}

func init() {
	rootCmd.AddCommand(outletsCmd)
}
