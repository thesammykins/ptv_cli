package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	gtfsproto "github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"github.com/spf13/cobra"
	"github.com/thesammykins/ptv_cli/internal/gtfs"
	"github.com/thesammykins/ptv_cli/internal/gtfsrt"
	"github.com/thesammykins/ptv_cli/internal/render"
)

var gtfsKeepZip bool
var gtfsNoUpdateCheck bool
var gtfsRealtimeAll bool

var gtfsCmd = &cobra.Command{
	Use:   "gtfs",
	Short: "Manage the local GTFS dataset used for journey planning",
}

var gtfsUpdateCmd = &cobra.Command{
	Use:   "update",
	Short: "Download and ingest the latest PTV GTFS feed",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}

		if !flagJSON {
			fmt.Printf("Downloading GTFS feed from %s\n", render.CleanText(cfg.GTFSURL))
			fmt.Println("(this is a large file, ~200MB; please wait)")
		}
		dl, err := gtfs.Download(ctx(), cfg.GTFSURL, cfg.DataDir)
		if err != nil {
			return err
		}

		store, err := gtfs.Open(cfg.DBPath())
		if err != nil {
			return err
		}
		defer store.Close()

		if !flagJSON {
			fmt.Println("Ingesting feed into local database...")
		}
		if err := gtfs.Ingest(ctx(), store, dl.Path, func(msg string) {
			if !flagJSON {
				fmt.Printf("  %s\n", render.CleanText(msg))
			}
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
		if flagJSON {
			return printJSON(map[string]any{
				"database":       cfg.DBPath(),
				"downloaded_zip": dl.Path,
				"kept_zip":       gtfsKeepZip,
				"stops":          counts["stops"],
				"routes":         counts["routes"],
				"trips":          counts["trips"],
				"stop_times":     counts["stop_times"],
			})
		}
		fmt.Printf("Done. stops=%d routes=%d trips=%d stop_times=%d\n",
			counts["stops"], counts["routes"], counts["trips"], counts["stop_times"])
		return nil
	},
}

var gtfsStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the local GTFS dataset status",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
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
		fmt.Printf("Database: %s\n", render.CleanText(cfg.DBPath()))
		fmt.Printf("Ingested at: %s\n", render.CleanText(when))
		if rep.AgeHours > 0 {
			fmt.Printf("Age: %.1f days (stale after %.0f)%s\n",
				rep.AgeHours/24, rep.StaleAfterDays, ifStale(rep.Stale))
		}
		if rep.FeedModified != "" {
			fmt.Printf("Feed last-modified: %s\n", render.CleanText(rep.FeedModified))
		}
		switch {
		case rep.CheckError != "":
			fmt.Printf("Update check: failed (%s)\n", render.CleanText(rep.CheckError))
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
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
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

var gtfsRealtimeCmd = &cobra.Command{
	Use:   "realtime [feed-id]",
	Short: "Inspect Transport Victoria GTFS Realtime feeds",
	Long: `Inspect Transport Victoria Open Data GTFS Realtime feeds.

Without a feed id this lists the known feed catalog. Pass a feed id to fetch and
decode one protobuf feed, or use --all to fetch every known feed. If
PTV_OPENDATA_KEY_ID is not configured, ptv tries an unauthenticated request so
you can verify which feeds are public for your network/account.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}

		feeds := gtfsrt.Feeds()
		if len(args) == 0 && !gtfsRealtimeAll {
			return printRealtimeCatalog(feeds)
		}
		if !gtfsRealtimeAll {
			feed, ok := gtfsrt.FeedByID(args[0])
			if !ok {
				return fmt.Errorf("unknown GTFS Realtime feed %q", args[0])
			}
			feeds = []gtfsrt.Feed{feed}
		}

		client := gtfsrt.New(cfg.OpenDataKeyID, cfg.OpenDataAPIID)
		results := make([]realtimeFeedStatus, 0, len(feeds))
		for _, feed := range feeds {
			results = append(results, inspectRealtimeFeed(ctx(), client, feed))
		}
		return printRealtimeStatuses(results)
	},
}

type gtfsRealtimeFetcher interface {
	Fetch(ctx context.Context, feedURL string) (*gtfsproto.FeedMessage, error)
}

type realtimeFeedStatus struct {
	gtfsrt.Feed
	OK               bool   `json:"ok"`
	Error            string `json:"error,omitempty"`
	GTFSRealtime     string `json:"gtfs_realtime_version,omitempty"`
	Incrementality   string `json:"incrementality,omitempty"`
	FeedTimestampUTC string `json:"feed_timestamp_utc,omitempty"`
	EntityCount      int    `json:"entity_count"`
	TripUpdateCount  int    `json:"trip_update_count"`
	VehicleCount     int    `json:"vehicle_count"`
	AlertCount       int    `json:"alert_count"`
	DeletedCount     int    `json:"deleted_count"`
	SampleVehicleID  string `json:"sample_vehicle_id,omitempty"`
	SampleTripID     string `json:"sample_trip_id,omitempty"`
}

func inspectRealtimeFeed(ctx context.Context, client gtfsRealtimeFetcher, feed gtfsrt.Feed) realtimeFeedStatus {
	status := realtimeFeedStatus{Feed: feed}
	msg, err := client.Fetch(ctx, feed.URL)
	if err != nil {
		status.Error = err.Error()
		return status
	}
	status.OK = true
	header := msg.GetHeader()
	status.GTFSRealtime = header.GetGtfsRealtimeVersion()
	status.Incrementality = header.GetIncrementality().String()
	if ts := header.GetTimestamp(); ts > 0 {
		status.FeedTimestampUTC = time.Unix(int64(ts), 0).UTC().Format(time.RFC3339)
	}
	for _, entity := range msg.GetEntity() {
		status.EntityCount++
		if entity.GetIsDeleted() {
			status.DeletedCount++
		}
		if entity.GetTripUpdate() != nil {
			status.TripUpdateCount++
		}
		if entity.GetVehicle() != nil {
			status.VehicleCount++
			if status.SampleVehicleID == "" {
				vehicle := entity.GetVehicle()
				status.SampleVehicleID = firstNonEmptyString(vehicle.GetVehicle().GetLabel(), vehicle.GetVehicle().GetId(), vehicle.GetVehicle().GetLicensePlate())
				status.SampleTripID = vehicle.GetTrip().GetTripId()
			}
		}
		if entity.GetAlert() != nil {
			status.AlertCount++
		}
	}
	return status
}

func printRealtimeCatalog(feeds []gtfsrt.Feed) error {
	if flagJSON {
		return printJSON(map[string]any{"feeds": feeds})
	}
	t := render.NewTable("ID", "MODE", "KIND", "URL")
	for _, feed := range feeds {
		t.Row(feed.ID, feed.Mode, feed.Kind, feed.URL)
	}
	return t.Flush()
}

func printRealtimeStatuses(results []realtimeFeedStatus) error {
	if flagJSON {
		return printJSON(map[string]any{"feeds": results})
	}
	t := render.NewTable("ID", "OK", "TIMESTAMP", "ENTITIES", "TRIPS", "VEHICLES", "ALERTS", "SAMPLE", "ERROR")
	for _, result := range results {
		t.Row(
			result.ID,
			result.OK,
			result.FeedTimestampUTC,
			result.EntityCount,
			result.TripUpdateCount,
			result.VehicleCount,
			result.AlertCount,
			result.SampleVehicleID,
			shortRealtimeError(result.Error),
		)
	}
	return t.Flush()
}

func shortRealtimeError(err string) string {
	if err == "" {
		return ""
	}
	parts := strings.Split(err, ";")
	last := strings.TrimSpace(parts[len(parts)-1])
	if len(last) > 90 {
		return last[:87] + "..."
	}
	return last
}

// freshnessWarnings returns human-readable warning lines for a freshness report
// (printed to stderr by commands that consume GTFS data).
func freshnessWarnings(r gtfs.FreshnessReport) []string {
	var out []string
	if r.Stale {
		out = append(out, fmt.Sprintf("Local GTFS data is %.0f days old (stale after %.0f). Run 'ptv gtfs update'.",
			r.AgeHours/24, r.StaleAfterDays))
	}
	if r.UpdateAvailable {
		out = append(out, "A newer PTV GTFS feed is available upstream. Run 'ptv gtfs update'.")
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
	gtfsRealtimeCmd.Flags().BoolVar(&gtfsRealtimeAll, "all", false, "fetch every known GTFS Realtime feed")
	gtfsCmd.AddCommand(gtfsUpdateCmd, gtfsStatusCmd, gtfsCheckCmd, gtfsRealtimeCmd)
	rootCmd.AddCommand(gtfsCmd)
}
