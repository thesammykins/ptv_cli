package cmd

import (
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
			resp, err := client.Search(ctx(), joinArgs(args), nil)
			if err != nil {
				return err
			}
			outlets = resp.Outlets
			status = resp.Status
		} else {
			resp, err := client.Outlets(ctx(), flagLimit)
			if err != nil {
				return err
			}
			outlets = resp.Outlets
			status = resp.Status
		}
		outlets = limitOutlets(outlets)
		resp := ptvapi.OutletResponse{Outlets: outlets, Status: status}
		if flagJSON {
			return printJSON(resp)
		}
		t := render.NewTable("NAME", "BUSINESS", "SUBURB")
		for _, o := range outlets {
			t.Row(o.OutletName, o.OutletBusiness, o.OutletSuburb)
		}
		t.Flush()
		return nil
	},
}

func init() {
	rootCmd.AddCommand(outletsCmd)
}
