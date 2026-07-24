package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/thesammykins/ptv_cli/internal/config"
	"github.com/thesammykins/ptv_cli/internal/gtfs"
	"github.com/thesammykins/ptv_cli/internal/gtfsrt"
)

type realtimeValidationOutput struct {
	Feed             string   `json:"feed"`
	Strategy         string   `json:"strategy"`
	TotalUpdates     int      `json:"total_updates"`
	JoinableUpdates  int      `json:"joinable_updates"`
	MatchedUpdates   int      `json:"matched_updates"`
	MatchRate        float64  `json:"match_rate"`
	NamespaceFormat  string   `json:"namespace_format"`
	MatchedTripIDs   []string `json:"matched_trip_ids"`
	UnmatchedTripIDs []string `json:"unmatched_trip_ids"`
	Warnings         []string `json:"warnings"`
}

var validateRealtimeCmd = &cobra.Command{
	Use: "validate-realtime", Short: "Validate GTFS static to Open Data trip-update identity joins", Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		runtimeCfg, err := loadRuntimeConfig()
		if err != nil {
			return err
		}
		credentials, err := config.OpenDataCredentialsWithOptions(config.LoadOptions{EnvFile: flagEnv})
		if err != nil || strings.TrimSpace(credentials.KeyID) == "" {
			return fmt.Errorf("Open Data KeyID is required for Phase 0 validation; run 'ptv auth opendata login'")
		}
		manager, err := gtfs.NewGenerationManager(runtimeCfg.DataDir)
		if err != nil {
			return err
		}
		store, _, err := manager.OpenCurrent(cmd.Context())
		if err != nil {
			return err
		}
		defer store.Close()
		feed, ok := gtfsrt.FeedByID("metro-trip-updates")
		if !ok {
			return fmt.Errorf("metro trip-updates feed is not catalogued")
		}
		snapshot, err := gtfsrt.New(credentials.KeyID).FetchSnapshot(cmd.Context(), feed)
		if err != nil {
			return err
		}
		output := realtimeValidationOutput{Feed: feed.ID, Strategy: "feed-local trip_id + start_date -> validated namespaced static trip_id", NamespaceFormat: "{feedMode}:{source_trip_id}", MatchedTripIDs: []string{}, UnmatchedTripIDs: []string{}, Warnings: []string{}}
		for _, update := range snapshot.TripUpdates {
			if update.TripID == "" || update.StartDate == "" {
				continue
			}
			output.TotalUpdates++
			candidates, qerr := store.ResolveTripSourceID(cmd.Context(), string(update.TripID), 2, update.StartDate)
			if qerr != nil {
				return qerr
			}
			if len(candidates) == 1 {
				output.MatchedUpdates++
				if len(output.MatchedTripIDs) < 10 {
					output.MatchedTripIDs = append(output.MatchedTripIDs, candidates[0])
				}
			} else if len(output.UnmatchedTripIDs) < 10 {
				output.UnmatchedTripIDs = append(output.UnmatchedTripIDs, string(update.TripID))
			}
		}
		output.JoinableUpdates = output.TotalUpdates
		if output.TotalUpdates > 0 {
			output.MatchRate = float64(output.MatchedUpdates) / float64(output.TotalUpdates)
		}
		if output.MatchRate < .95 {
			output.Warnings = append(output.Warnings, "trip_id join rate is below the 95% gate; realtime commands must not silently use a different namespace")
		}
		if flagJSON {
			return printJSON(output)
		}
		fmt.Printf("Phase 0 %s: %d/%d matched (%.1f%%)\n", feed.ID, output.MatchedUpdates, output.TotalUpdates, output.MatchRate*100)
		fmt.Printf("Strategy: %s\n", output.Strategy)
		return nil
	},
}

func init() { gtfsCmd.AddCommand(validateRealtimeCmd) }
