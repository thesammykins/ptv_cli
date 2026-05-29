package cmd

import (
	"fmt"
	"os"

	"github.com/elsammykins/ptv_cli/internal/config"
	"github.com/elsammykins/ptv_cli/internal/gtfs"
	"github.com/spf13/cobra"
)

var gtfsKeepZip bool
var gtfsNoUpdateCheck bool

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
		dl, err := gtfs.Download(ctx(), cfg.GTFSURL, cfg.DataDir)
		if err != nil {
			return err
		}

		store, err := gtfs.Open(cfg.DBPath())
		if err != nil {
			return err
		}
		defer store.Close()

		fmt.Println("Ingesting feed into local database...")
		if err := gtfs.Ingest(ctx(), store, dl.Path, func(msg string) {
			fmt.Printf("  %s\n", msg)
		}); err != nil {
			return err
		}

		gtfs.RecordFeedProvenance(store, dl)

		if !gtfsKeepZip {
			_ = os.Remove(dl.Path)
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
		rep := gtfs.Freshness(ctx(), store, cfg.GTFSURL, !gtfsNoUpdateCheck, false)
		if flagJSON {
			return printJSON(map[string]any{
				"database":    cfg.DBPath(),
				"ingested":    true,
				"ingested_at": when,
				"stops":       counts["stops"],
				"routes":      counts["routes"],
				"trips":       counts["trips"],
				"stop_times":  counts["stop_times"],
				"freshness":   rep,
			})
		}
		fmt.Printf("Database: %s\n", cfg.DBPath())
		fmt.Printf("Ingested at: %s\n", when)
		if rep.AgeHours > 0 {
			fmt.Printf("Age: %.1f days (stale after %.0f)%s\n",
				rep.AgeHours/24, rep.StaleAfterDays, ifStale(rep.Stale))
		}
		if rep.FeedModified != "" {
			fmt.Printf("Feed last-modified: %s\n", rep.FeedModified)
		}
		switch {
		case rep.CheckError != "":
			fmt.Printf("Update check: failed (%s)\n", rep.CheckError)
		case rep.Checked && rep.UpdateAvailable:
			fmt.Println("Update check: a newer feed is available — run 'ptv gtfs update'.")
		case rep.Checked:
			fmt.Println("Update check: up to date.")
		}
		fmt.Printf("stops=%d routes=%d trips=%d stop_times=%d\n",
			counts["stops"], counts["routes"], counts["trips"], counts["stop_times"])
		return nil
	},
}

var gtfsCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Check the upstream feed for a newer GTFS dataset",
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
				return printJSON(map[string]any{"ingested": false})
			}
			fmt.Println("No local GTFS data. Run 'ptv gtfs update'.")
			return nil
		}

		rep := gtfs.Freshness(ctx(), store, cfg.GTFSURL, true, true)
		if flagJSON {
			return printJSON(rep)
		}
		if rep.CheckError != "" {
			return fmt.Errorf("update check failed: %s", rep.CheckError)
		}
		if rep.UpdateAvailable {
			fmt.Println("A newer PTV GTFS feed is available — run 'ptv gtfs update'.")
		} else {
			fmt.Println("Your local GTFS data is up to date.")
		}
		if rep.Stale {
			fmt.Printf("(Local data is %.1f days old.)\n", rep.AgeHours/24)
		}
		return nil
	},
}

// freshnessWarnings returns human-readable warning lines for a freshness report
// (printed to stderr by commands that consume GTFS data).
func freshnessWarnings(r gtfs.FreshnessReport) []string {
	var out []string
	if r.Stale {
		out = append(out, fmt.Sprintf("⚠ Local GTFS data is %.0f days old (stale after %.0f). Run 'ptv gtfs update'.",
			r.AgeHours/24, r.StaleAfterDays))
	}
	if r.UpdateAvailable {
		out = append(out, "⚠ A newer PTV GTFS feed is available upstream. Run 'ptv gtfs update'.")
	}
	return out
}

func ifStale(stale bool) string {
	if stale {
		return " — STALE"
	}
	return ""
}

func init() {
	gtfsUpdateCmd.Flags().BoolVar(&gtfsKeepZip, "keep-zip", false, "keep the downloaded zip after ingest")
	gtfsStatusCmd.Flags().BoolVar(&gtfsNoUpdateCheck, "no-update-check", false, "skip the live upstream update check")
	gtfsCmd.AddCommand(gtfsUpdateCmd, gtfsStatusCmd, gtfsCheckCmd)
	rootCmd.AddCommand(gtfsCmd)
}
