package cmd

import (
	"fmt"
	"os"

	"github.com/elsammykins/ptv_cli/internal/config"
	"github.com/elsammykins/ptv_cli/internal/gtfs"
	"github.com/spf13/cobra"
)

var gtfsKeepZip bool

var gtfsCmd = &cobra.Command{
	Use:   "gtfs",
	Short: "Manage the local GTFS dataset used for journey planning",
}

var gtfsUpdateCmd = &cobra.Command{
	Use:   "update",
	Short: "Download and ingest the latest PTV GTFS feed",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}

		fmt.Printf("Downloading GTFS feed from %s\n", cfg.GTFSURL)
		fmt.Println("(this is a large file, ~200MB; please wait)")
		zipPath, err := gtfs.Download(ctx(), cfg.GTFSURL, cfg.DataDir)
		if err != nil {
			return err
		}

		store, err := gtfs.Open(cfg.DBPath())
		if err != nil {
			return err
		}
		defer store.Close()

		fmt.Println("Ingesting feed into local database...")
		if err := gtfs.Ingest(ctx(), store, zipPath, func(msg string) {
			fmt.Printf("  %s\n", msg)
		}); err != nil {
			return err
		}

		if !gtfsKeepZip {
			_ = os.Remove(zipPath)
		}

		counts, err := store.Counts()
		if err != nil {
			return err
		}
		fmt.Printf("Done. stops=%d routes=%d trips=%d stop_times=%d\n",
			counts["stops"], counts["routes"], counts["trips"], counts["stop_times"])
		return nil
	},
}

var gtfsStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the local GTFS dataset status",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		store, err := gtfs.Open(cfg.DBPath())
		if err != nil {
			return err
		}
		defer store.Close()

		if !store.IsIngested() {
			if flagJSON {
				return printJSON(map[string]any{
					"database": cfg.DBPath(),
					"ingested": false,
				})
			}
			fmt.Println("Status: not ingested. Run 'ptv gtfs update'.")
			return nil
		}
		when, _ := store.Meta("ingested_at")
		counts, err := store.Counts()
		if err != nil {
			return err
		}
		if flagJSON {
			return printJSON(map[string]any{
				"database":    cfg.DBPath(),
				"ingested":    true,
				"ingested_at": when,
				"stops":       counts["stops"],
				"routes":      counts["routes"],
				"trips":       counts["trips"],
				"stop_times":  counts["stop_times"],
			})
		}
		fmt.Printf("Database: %s\n", cfg.DBPath())
		fmt.Printf("Ingested at: %s\n", when)
		fmt.Printf("stops=%d routes=%d trips=%d stop_times=%d\n",
			counts["stops"], counts["routes"], counts["trips"], counts["stop_times"])
		return nil
	},
}

func init() {
	gtfsUpdateCmd.Flags().BoolVar(&gtfsKeepZip, "keep-zip", false, "keep the downloaded zip after ingest")
	gtfsCmd.AddCommand(gtfsUpdateCmd, gtfsStatusCmd)
	rootCmd.AddCommand(gtfsCmd)
}
