package cmd

import (
	"github.com/elsammykins/ptv_cli/internal/render"
	"github.com/spf13/cobra"
)

var outletsCmd = &cobra.Command{
	Use:   "outlets",
	Short: "List myki ticket outlets",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, _, err := loadClient()
		if err != nil {
			return err
		}
		resp, err := client.Outlets(ctx(), flagLimit)
		if err != nil {
			return err
		}
		if flagJSON {
			return printJSON(resp)
		}
		t := render.NewTable("NAME", "BUSINESS", "SUBURB")
		for _, o := range resp.Outlets {
			t.Row(o.OutletName, o.OutletBusiness, o.OutletSuburb)
		}
		t.Flush()
		return nil
	},
}

func init() {
	rootCmd.AddCommand(outletsCmd)
}
