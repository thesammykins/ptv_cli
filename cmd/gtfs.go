package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/thesammykins/ptv_cli/internal/config"
	"github.com/thesammykins/ptv_cli/internal/gtfs"
	"github.com/thesammykins/ptv_cli/internal/gtfsrt"
	"github.com/thesammykins/ptv_cli/internal/render"
)

var gtfsKeepZip bool
var gtfsNoUpdateCheck bool
var gtfsCheckForce bool
var gtfsRealtimeAll bool
var gtfsBackgroundWorker bool

const (
	defaultGTFSStatusFreshnessBudget = 1500 * time.Millisecond
	gtfsRealtimeCommandTimeout       = 30 * time.Second
)

var gtfsStatusFreshnessBudget = defaultGTFSStatusFreshnessBudget

type gtfsUpdateOutput struct {
	Database      string               `json:"database"`
	DownloadedZip string               `json:"downloaded_zip"`
	KeptZip       bool                 `json:"kept_zip"`
	Generation    gtfs.GenerationRef   `json:"generation"`
	Coverage      gtfs.ServiceCoverage `json:"coverage"`
	Provenance    gtfsProvenanceOutput `json:"provenance"`
	Counts        gtfs.DatasetCounts   `json:"counts"`
	Stops         int64                `json:"stops"`
	Routes        int64                `json:"routes"`
	Trips         int64                `json:"trips"`
	StopTimes     int64                `json:"stop_times"`
}

type gtfsStatusOutput struct {
	Database   string               `json:"database"`
	Generation gtfs.GenerationRef   `json:"generation"`
	Ingested   bool                 `json:"ingested"`
	IngestedAt time.Time            `json:"ingested_at"`
	Coverage   gtfs.ServiceCoverage `json:"coverage"`
	Provenance gtfsProvenanceOutput `json:"provenance"`
	Counts     gtfs.DatasetCounts   `json:"counts"`
	Freshness  gtfs.FreshnessReport `json:"freshness"`
	Stops      int64                `json:"stops"`
	Routes     int64                `json:"routes"`
	Trips      int64                `json:"trips"`
	StopTimes  int64                `json:"stop_times"`
}

type gtfsProvenanceOutput struct {
	SourceURL       string     `json:"source_url"`
	ETag            string     `json:"etag,omitempty"`
	LastModified    string     `json:"last_modified,omitempty"`
	DeclaredBytes   int64      `json:"declared_bytes,omitempty"`
	ActualBytes     int64      `json:"actual_bytes"`
	PublicationTime *time.Time `json:"publication_time,omitempty"`
}

func newGTFSProvenanceOutput(provenance gtfs.FeedProvenance) gtfsProvenanceOutput {
	output := gtfsProvenanceOutput{
		SourceURL:     gtfs.RedactSourceURL(provenance.SourceURL),
		ETag:          provenance.ETag,
		LastModified:  provenance.LastModified,
		DeclaredBytes: provenance.DeclaredBytes,
		ActualBytes:   provenance.ActualBytes,
	}
	if !provenance.PublicationTime.IsZero() {
		publicationTime := provenance.PublicationTime
		output.PublicationTime = &publicationTime
	}
	return output
}

type gtfsUnavailableOutput struct {
	Database string `json:"database"`
	DataDir  string `json:"data_dir,omitempty"`
	Ingested bool   `json:"ingested"`
	State    string `json:"state"`
	Action   string `json:"action"`
}

var gtfsCmd = &cobra.Command{
	Use:   "gtfs",
	Short: "Manage the local GTFS dataset used for journey planning",
}

var gtfsUpdateCmd = &cobra.Command{
	Use:   "update",
	Short: "Download and ingest the latest PTV GTFS feed",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) (runErr error) {
		cfg, err := loadRuntimeConfig()
		if err != nil {
			return err
		}
		manager, err := gtfs.NewGenerationManager(cfg.DataDir)
		if err != nil {
			return err
		}
		update, err := manager.AcquireUpdate(cmd.Context())
		if err != nil {
			return err
		}
		defer update.Release()

		if gtfsBackgroundWorker {
			startedAt := time.Now().UTC().Format(time.RFC3339)
			_ = gtfs.WriteUpdateProgress(cfg.DataDir, gtfs.UpdateProgress{State: "downloading", StartedAt: startedAt})
			defer func() {
				if runErr != nil {
					_ = gtfs.WriteUpdateProgress(cfg.DataDir, gtfs.UpdateProgress{
						State: "failed", FailedAt: time.Now().UTC().Format(time.RFC3339), Error: render.CleanText(runErr.Error()),
					})
				}
			}()
		}
		if !flagJSON && !gtfsBackgroundWorker {
			fmt.Printf("Downloading GTFS feed from %s\n", render.CleanText(gtfs.RedactSourceURL(cfg.GTFSURL)))
			fmt.Println("(this is a large file, ~200MB; please wait)")
		}
		dl, err := gtfs.Download(cmd.Context(), cfg.GTFSURL, cfg.DataDir)
		if err != nil {
			return err
		}
		if !gtfsKeepZip {
			defer os.Remove(dl.Path)
		}

		staging, err := update.NewStaging(cmd.Context())
		if err != nil {
			return err
		}
		published := false
		defer func() {
			_ = staging.Close()
			// Publish moves a candidate out of staging before replacing the
			// manifest. Only delete a path that is still the owned .tmp file;
			// an error after the move must never delete a referenced generation.
			if !published && strings.HasSuffix(staging.Path(), ".tmp") {
				_ = os.Remove(staging.Path())
			}
		}()

		if !flagJSON && !gtfsBackgroundWorker {
			fmt.Println("Compiling and validating a new immutable generation...")
		}
		state, err := gtfs.IngestGeneration(cmd.Context(), staging.Store, dl.Path, gtfs.IngestGenerationOptions{
			GenerationID: staging.Ref.ID,
			Provenance:   dl.Provenance(),
			IngestedAt:   time.Now().UTC(),
			Progress: func(msg string) {
				if !flagJSON {
					fmt.Printf("  %s\n", render.CleanText(msg))
				}
			},
		})
		if err != nil {
			return err
		}
		if err := update.Publish(cmd.Context(), staging); err != nil {
			return err
		}
		published = true
		if gtfsBackgroundWorker {
			_ = gtfs.WriteUpdateProgress(cfg.DataDir, gtfs.UpdateProgress{State: "completed", CompletedAt: time.Now().UTC().Format(time.RFC3339), GenerationID: staging.Ref.ID})
		}
		if flagJSON {
			return printJSON(gtfsUpdateOutput{
				Database: staging.Path(), DownloadedZip: dl.Path, KeptZip: gtfsKeepZip,
				Generation: staging.Ref, Coverage: state.Coverage, Provenance: newGTFSProvenanceOutput(state.Provenance), Counts: state.Counts,
				Stops: state.Counts.Stops, Routes: state.Counts.Routes, Trips: state.Counts.Trips, StopTimes: state.Counts.StopTimes,
			})
		}
		fmt.Printf("Published generation %s. stops=%d routes=%d trips=%d stop_times=%d connections=%d\n",
			render.CleanText(staging.Ref.ID), state.Counts.Stops, state.Counts.Routes,
			state.Counts.Trips, state.Counts.StopTimes, state.Counts.Connections)
		return nil
	},
}

var gtfsStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the local GTFS dataset status",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadRuntimeConfig()
		if err != nil {
			return err
		}
		manager, err := gtfs.NewGenerationManager(cfg.DataDir)
		if err != nil {
			return err
		}
		store, generation, err := manager.OpenCurrent(cmd.Context())
		if errors.Is(err, gtfs.ErrNoCurrentGeneration) || errors.Is(err, gtfs.ErrLegacyDatabase) {
			state := "missing"
			if errors.Is(err, gtfs.ErrLegacyDatabase) {
				state = "legacy_reingest_required"
			}
			if flagJSON {
				return printJSON(gtfsUnavailableOutput{
					Database: manager.LegacyDatabasePath(), DataDir: cfg.DataDir,
					Ingested: false, State: state, Action: "run ptv gtfs update",
				})
			}
			if state == "legacy_reingest_required" {
				fmt.Println("Status: legacy GTFS database requires a one-time re-ingest. Run 'ptv gtfs update'.")
			} else {
				fmt.Println("Status: not ingested. Run 'ptv gtfs update'.")
			}
			return nil
		}
		if err != nil {
			return err
		}
		defer store.Close()

		state, err := store.DatasetState(cmd.Context())
		if err != nil {
			return err
		}
		rep, freshnessWarning, err := statusFreshness(
			cmd.Context(), cfg, state, time.Now(), !gtfsNoUpdateCheck,
		)
		if err != nil {
			return err
		}
		if freshnessWarning != "" {
			fmt.Fprintln(os.Stderr, "warning:", render.CleanText(freshnessWarning))
		}
		if flagJSON {
			return printJSON(gtfsStatusOutput{
				Database: store.Path(), Generation: generation, Ingested: true,
				IngestedAt: state.IngestedAt, Coverage: state.Coverage,
				Provenance: newGTFSProvenanceOutput(state.Provenance), Counts: state.Counts, Freshness: rep,
				Stops: state.Counts.Stops, Routes: state.Counts.Routes,
				Trips: state.Counts.Trips, StopTimes: state.Counts.StopTimes,
			})
		}
		fmt.Printf("Database: %s\n", render.CleanText(store.Path()))
		fmt.Printf("Generation: %s\n", render.CleanText(generation.ID))
		fmt.Printf("Ingested at: %s\n", render.CleanText(state.IngestedAt.UTC().Format(time.RFC3339)))
		fmt.Printf("Service coverage: %s to %s\n", render.CleanText(state.Coverage.Start), render.CleanText(state.Coverage.End))
		if !state.Provenance.PublicationTime.IsZero() {
			fmt.Printf("Feed publication: %s\n", render.CleanText(state.Provenance.PublicationTime.UTC().Format(time.RFC3339)))
		}
		fmt.Printf("Freshness: %s", render.CleanText(string(rep.State)))
		if rep.Reason != "" {
			fmt.Printf(" (%s)", render.CleanText(rep.Reason))
		}
		fmt.Println()
		if rep.AgeBasis != "" {
			fmt.Printf("Age: %.1f days using %s evidence (stale after %.0f)\n",
				rep.AgeHours/24, render.CleanText(rep.AgeBasis), rep.StaleAfterDays)
		}
		if rep.CheckError != "" {
			fmt.Printf("Update check: failed (%s)\n", render.CleanText(rep.CheckError))
		} else if rep.CheckSkipped != "" {
			fmt.Printf("Update check: skipped (%s)\n", render.CleanText(rep.CheckSkipped))
		}
		fmt.Printf("stops=%d routes=%d trips=%d stop_times=%d connections=%d\n",
			state.Counts.Stops, state.Counts.Routes, state.Counts.Trips, state.Counts.StopTimes, state.Counts.Connections)
		return nil
	},
}

var gtfsCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Check the upstream feed for a newer GTFS dataset",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadRuntimeConfig()
		if err != nil {
			return err
		}
		manager, err := gtfs.NewGenerationManager(cfg.DataDir)
		if err != nil {
			return err
		}
		store, _, err := manager.OpenCurrent(cmd.Context())
		if errors.Is(err, gtfs.ErrNoCurrentGeneration) || errors.Is(err, gtfs.ErrLegacyDatabase) {
			state := "missing"
			if errors.Is(err, gtfs.ErrLegacyDatabase) {
				state = "legacy_reingest_required"
			}
			if flagJSON {
				return printJSON(gtfsUnavailableOutput{
					Database: manager.LegacyDatabasePath(), DataDir: cfg.DataDir,
					Ingested: false, State: state, Action: "run ptv gtfs update",
				})
			}
			if state == "legacy_reingest_required" {
				fmt.Println("Legacy GTFS data requires a one-time re-ingest. Run 'ptv gtfs update'.")
			} else {
				fmt.Println("No local GTFS data. Run 'ptv gtfs update'.")
			}
			return nil
		}
		if err != nil {
			return err
		}
		defer store.Close()

		state, err := store.DatasetState(cmd.Context())
		if err != nil {
			return err
		}
		rep, err := checkGTFSFreshness(cmd.Context(), cfg, state, time.Now(), true, gtfsCheckForce)
		if err != nil {
			return err
		}
		if flagJSON {
			if err := printJSON(rep); err != nil {
				return err
			}
			if rep.CheckError != "" {
				return fmt.Errorf("update check failed: %s", rep.CheckError)
			}
			return nil
		}
		if rep.CheckError != "" {
			return fmt.Errorf("update check failed: %s", rep.CheckError)
		}
		switch rep.State {
		case gtfs.FreshnessChanged:
			fmt.Println("A newer PTV GTFS feed is available — run 'ptv gtfs update'.")
		case gtfs.FreshnessCurrent:
			fmt.Println("Your local GTFS data is up to date.")
		case gtfs.FreshnessStale:
			fmt.Printf("Local GTFS data is stale: %s Run 'ptv gtfs update'.\n", render.CleanText(rep.Reason))
		default:
			fmt.Printf("GTFS freshness is unknown: %s\n", render.CleanText(rep.Reason))
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
PTV_OPENDATA_KEY_ID is not configured, fetching fails locally without issuing
an unauthenticated request. GTFS Realtime credentials are resolved independently
from PTV Timetable API credentials.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := cmd.Context().Err(); err != nil {
			return err
		}
		feeds := gtfsrt.Feeds()
		if len(args) == 0 && !gtfsRealtimeAll {
			if err := cmd.Context().Err(); err != nil {
				return err
			}
			return printRealtimeCatalog(feeds)
		}
		if !gtfsRealtimeAll {
			feed, ok := gtfsrt.FeedByID(args[0])
			if !ok {
				return fmt.Errorf("unknown GTFS Realtime feed %q", args[0])
			}
			feeds = []gtfsrt.Feed{feed}
		}

		creds, err := config.OpenDataCredentialsWithOptions(config.LoadOptions{EnvFile: flagEnv})
		if err != nil {
			return err
		}
		client := gtfsrt.NewWithOptions(creds.KeyID, gtfsrt.ClientOptions{})
		fetchCtx, cancel := context.WithTimeout(cmd.Context(), gtfsRealtimeCommandTimeout)
		defer cancel()
		results, err := inspectRealtimeFeeds(fetchCtx, client, feeds)
		if err != nil {
			if parentErr := cmd.Context().Err(); parentErr != nil {
				return parentErr
			}
			return err
		}
		if err := cmd.Context().Err(); err != nil {
			return err
		}
		return printRealtimeStatuses(results)
	},
}

type gtfsRealtimeFetcher interface {
	FetchSnapshot(ctx context.Context, feed gtfsrt.Feed) (*gtfsrt.Snapshot, error)
}

type realtimeFeedStatus struct {
	gtfsrt.Feed
	OK                     bool   `json:"ok"`
	Error                  string `json:"error,omitempty"`
	GTFSRealtime           string `json:"gtfs_realtime_version,omitempty"`
	Incrementality         string `json:"incrementality,omitempty"`
	FeedTimestampUTC       string `json:"feed_timestamp_utc,omitempty"`
	EntityCount            int    `json:"entity_count"`
	TripUpdateCount        int    `json:"trip_update_count"`
	VehicleCount           int    `json:"vehicle_count"`
	AlertCount             int    `json:"alert_count"`
	DeletedCount           int    `json:"deleted_count"`
	SamplePublicLabel      string `json:"sample_public_label,omitempty"`
	SampleStaticGTFSTripID string `json:"sample_static_gtfs_trip_id,omitempty"`
}

func inspectRealtimeFeeds(ctx context.Context, client gtfsRealtimeFetcher, feeds []gtfsrt.Feed) ([]realtimeFeedStatus, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	results := make([]realtimeFeedStatus, 0, len(feeds))
	for _, feed := range feeds {
		status := inspectRealtimeFeed(ctx, client, feed)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		results = append(results, status)
	}
	return results, nil
}

func inspectRealtimeFeed(ctx context.Context, client gtfsRealtimeFetcher, feed gtfsrt.Feed) realtimeFeedStatus {
	status := realtimeFeedStatus{Feed: feed}
	snapshot, err := client.FetchSnapshot(ctx, feed)
	if err != nil {
		status.Error = err.Error()
		return status
	}
	status.OK = true
	status.GTFSRealtime = snapshot.GTFSRealtime
	status.Incrementality = string(snapshot.Incrementality)
	if snapshot.FeedTimestamp != nil {
		status.FeedTimestampUTC = snapshot.FeedTimestamp.UTC().Format(time.RFC3339)
	}
	status.EntityCount = snapshot.Counts.Entities
	status.DeletedCount = snapshot.Counts.Deleted
	status.TripUpdateCount = snapshot.Counts.TripUpdates
	status.VehicleCount = snapshot.Counts.Vehicles
	status.AlertCount = snapshot.Counts.Alerts
	for _, vehicle := range snapshot.Vehicles {
		if vehicle.Label != "" {
			status.SamplePublicLabel = string(vehicle.Label)
			status.SampleStaticGTFSTripID = string(vehicle.TripID)
			break
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
			result.SamplePublicLabel,
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
	switch r.State {
	case gtfs.FreshnessChanged:
		return []string{"A newer PTV GTFS feed is available upstream. Run 'ptv gtfs update'."}
	case gtfs.FreshnessStale:
		return []string{fmt.Sprintf("Local GTFS data is stale: %s Run 'ptv gtfs update'.", r.Reason)}
	case gtfs.FreshnessUnknown:
		return []string{fmt.Sprintf("Static GTFS freshness is unknown: %s", r.Reason)}
	default:
		return nil
	}
}

func checkGTFSFreshness(ctx context.Context, cfg *config.RuntimeConfig, state gtfs.DatasetState, requestedAt time.Time, allowNetwork, force bool) (gtfs.FreshnessReport, error) {
	return gtfs.CheckFreshness(ctx, gtfs.FreshnessRequest{
		DataDir: cfg.DataDir, Dataset: state, RequestedAt: requestedAt,
		SourceURL: cfg.GTFSURL, AllowNetwork: allowNetwork, Force: force,
	})
}

var checkGTFSFreshnessForCommand = checkGTFSFreshness

type commandFreshnessResult struct {
	report gtfs.FreshnessReport
	err    error
}

// statusFreshness keeps status a local, successful inventory operation even
// when its optional mutable/network freshness service is slow or unavailable.
// The explicit `gtfs check` command intentionally does not use this fallback.
func statusFreshness(
	ctx context.Context,
	cfg *config.RuntimeConfig,
	state gtfs.DatasetState,
	requestedAt time.Time,
	allowNetwork bool,
) (gtfs.FreshnessReport, string, error) {
	check := checkGTFSFreshnessForCommand
	if !allowNetwork {
		report, err := check(ctx, cfg, state, requestedAt, false, false)
		return report, "", err
	}

	checkCtx, cancel := context.WithTimeout(ctx, gtfsStatusFreshnessBudget)
	defer cancel()
	results := make(chan commandFreshnessResult, 1)
	go func() {
		report, err := check(checkCtx, cfg, state, requestedAt, true, false)
		results <- commandFreshnessResult{report: report, err: err}
	}()

	fallback := func(reason string) (gtfs.FreshnessReport, string, error) {
		report, err := check(ctx, cfg, state, requestedAt, false, false)
		if err != nil {
			return gtfs.FreshnessReport{}, "", err
		}
		return report, reason, nil
	}

	select {
	case result := <-results:
		if result.err != nil {
			if err := ctx.Err(); err != nil {
				return gtfs.FreshnessReport{}, "", err
			}
			return fallback("live GTFS freshness check unavailable: " + result.err.Error())
		}
		if result.report.CheckError != "" {
			return result.report, "live GTFS freshness check failed: " + result.report.CheckError, nil
		}
		return result.report, "", nil
	case <-checkCtx.Done():
		if err := ctx.Err(); err != nil {
			return gtfs.FreshnessReport{}, "", err
		}
		return fallback("live GTFS freshness check exceeded its optional time budget")
	}
}

func init() {
	gtfsUpdateCmd.Flags().BoolVar(&gtfsKeepZip, "keep-zip", false, "keep the downloaded zip after ingest")
	gtfsUpdateCmd.Flags().BoolVar(&gtfsBackgroundWorker, "background-worker", false, "internal auto-update worker mode")
	_ = gtfsUpdateCmd.Flags().MarkHidden("background-worker")
	gtfsStatusCmd.Flags().BoolVar(&gtfsNoUpdateCheck, "no-update-check", false, "skip the live upstream update check")
	gtfsCheckCmd.Flags().BoolVar(&gtfsCheckForce, "force", false, "bypass cached success timing or failed-check backoff")
	gtfsRealtimeCmd.Flags().BoolVar(&gtfsRealtimeAll, "all", false, "fetch every known GTFS Realtime feed")
	gtfsCmd.AddCommand(gtfsUpdateCmd, gtfsStatusCmd, gtfsCheckCmd, gtfsRealtimeCmd)
	rootCmd.AddCommand(gtfsCmd)
}
