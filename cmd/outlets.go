package cmd

import (
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"
	"github.com/thesammykins/ptv_cli/internal/config"
	"github.com/thesammykins/ptv_cli/internal/ptvapi"
	"github.com/thesammykins/ptv_cli/internal/render"
	"github.com/thesammykins/ptv_cli/internal/v3static"
)

var outletsCmd = &cobra.Command{
	Use:   "outlets [term]",
	Short: "List or search myki ticket outlets",
	Args:  cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		var staticSnapshot *v3static.Snapshot
		var staticErr error
		var client *ptvapi.Client
		if runtimeConfig, runtimeErr := loadRuntimeConfig(); runtimeErr == nil {
			if credentials, credentialErr := config.LoadPTVCredentialsWithOptions(config.LoadOptions{EnvFile: flagEnv}); credentialErr == nil {
				client = ptvapi.New(runtimeConfig.BaseURL, credentials.APIKey, credentials.DevID)
			}
		}
		var outlets []ptvapi.ResultOutlet
		var status ptvapi.Status
		dataSource := "v3_static_snapshot"
		var warnings []string
		if client != nil {
			if len(args) > 0 {
				resp, searchErr := client.Search(cmd.Context(), joinArgs(args), nil)
				if searchErr == nil {
					outlets = resp.Outlets
					status = resp.Status
					dataSource = "ptv_api_v3"
				} else {
					warnings = append(warnings, "PTV outlet data unavailable; showing bundled snapshot")
				}
			} else {
				resp, listErr := client.Outlets(cmd.Context(), flagLimit)
				if listErr == nil {
					outlets = resp.Outlets
					status = resp.Status
					dataSource = "ptv_api_v3"
				} else {
					warnings = append(warnings, "PTV outlet data unavailable; showing bundled snapshot")
				}
			}
		}
		if dataSource == "v3_static_snapshot" {
			staticSnapshot, staticErr = v3static.LoadEmbedded()
			if staticErr != nil {
				return staticErr
			}
			if len(args) > 0 {
				for _, outlet := range staticSnapshot.SearchOutlets(joinArgs(args), 0) {
					outlets = append(outlets, ptvapi.ResultOutlet{OutletName: outlet.OutletName, OutletBusiness: outlet.OutletBusiness, OutletLatitude: outlet.OutletLatitude, OutletLongitude: outlet.OutletLongitude, OutletSuburb: outlet.OutletSuburb})
				}
			} else {
				for _, outlet := range staticSnapshot.AllOutlets(0) {
					outlets = append(outlets, ptvapi.ResultOutlet{OutletName: outlet.OutletName, OutletBusiness: outlet.OutletBusiness, OutletLatitude: outlet.OutletLatitude, OutletLongitude: outlet.OutletLongitude, OutletSuburb: outlet.OutletSuburb})
				}
			}
			status = ptvapi.Status{Version: "static-v3-snapshot", Health: 1}
		}
		sortOutlets(outlets)
		outlets = limitOutlets(outlets)
		output := newOutletsOutput(outlets, status)
		output.DataSource = dataSource
		output.Warnings = warnings
		if dataSource == "v3_static_snapshot" {
			output.SnapshotGeneratedAt = staticSnapshot.GeneratedAt
			output.SourceNotice = v3StaticNotice()
		}
		if flagJSON {
			return printJSON(output)
		}
		for _, warning := range output.Warnings {
			fmt.Fprintln(os.Stderr, warning)
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
	Outlets             []outletOutput      `json:"outlets"`
	Status              outletStatusOutput  `json:"status"`
	DataSource          string              `json:"data_source,omitempty"`
	SnapshotGeneratedAt string              `json:"snapshot_generated_at,omitempty"`
	SourceNotice        *sourceNoticeOutput `json:"source_notice,omitempty"`
	Warnings            []string            `json:"warnings,omitempty"`
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
