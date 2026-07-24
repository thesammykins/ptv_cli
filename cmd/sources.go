package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/thesammykins/ptv_cli/internal/config"
	"github.com/thesammykins/ptv_cli/internal/gtfs"
	"github.com/thesammykins/ptv_cli/internal/ptvapi"
	"github.com/thesammykins/ptv_cli/internal/v3static"
)

// resolvedSources keeps the primary data capabilities independent. Missing
// optional credentials are represented as empty/nil values and never prevent
// a local GTFS command from running.
type resolvedSources struct {
	GTFSStore   *gtfs.Store
	OpenDataKey string
	V3Client    *ptvapi.Client
	V3Static    *v3static.Snapshot
	Runtime     *config.RuntimeConfig
}

type sourceNoticeOutput struct {
	Attribution    string `json:"attribution"`
	License        string `json:"license"`
	Source         string `json:"source"`
	Modification   string `json:"modification"`
	Disclaimer     string `json:"disclaimer"`
	NonEndorsement string `json:"non_endorsement"`
}

func v3StaticNotice() *sourceNoticeOutput {
	return &sourceNoticeOutput{
		Attribution: v3static.Attribution, License: v3static.LicenseURL, Source: v3static.SourceURL,
		Modification: v3static.Modification, Disclaimer: v3static.Disclaimer, NonEndorsement: v3static.NonEndorsement,
	}
}

func resolveSources(ctx context.Context) (*resolvedSources, error) {
	runtimeCfg, err := loadRuntimeConfig()
	if err != nil {
		return nil, err
	}
	store, updateResult, err := gtfs.CheckAndAutoUpdate(ctx, gtfs.AutoUpdateConfig{Enabled: true, DataDir: runtimeCfg.DataDir, SourceURL: runtimeCfg.GTFSURL})
	if err != nil {
		if errors.Is(err, gtfs.ErrNoCurrentGeneration) {
			return nil, fmt.Errorf("local GTFS data is unavailable; run 'ptv gtfs update': %w", err)
		}
		return nil, err
	}
	if updateResult.Message != "" {
		fmt.Fprintln(os.Stderr, updateResult.Message)
	}
	sources := &resolvedSources{GTFSStore: store, Runtime: runtimeCfg}
	if snapshot, snapshotErr := v3static.LoadEmbedded(); snapshotErr == nil {
		sources.V3Static = snapshot
	}
	openData, openDataErr := config.OpenDataCredentialsWithOptions(config.LoadOptions{EnvFile: flagEnv})
	if openDataErr == nil {
		sources.OpenDataKey = strings.TrimSpace(openData.KeyID)
	}
	// v3 is enrichment-only for migrated commands. A missing credential is
	// intentionally not an error; malformed optional configuration is likewise
	// kept out of the primary result path.
	if credentials, credErr := config.LoadPTVCredentialsWithOptions(config.LoadOptions{EnvFile: flagEnv}); credErr == nil {
		sources.V3Client = ptvapi.New(runtimeCfg.BaseURL, credentials.APIKey, credentials.DevID)
	}
	return sources, nil
}

func closeSources(sources *resolvedSources) {
	if sources != nil && sources.GTFSStore != nil {
		_ = sources.GTFSStore.Close()
	}
}

type sourceFreshness struct {
	State      string  `json:"state"`
	AgeHours   float64 `json:"age_hours,omitempty"`
	AgeSeconds float64 `json:"age_seconds,omitempty"`
	Coverage   *struct {
		Start string `json:"start"`
		End   string `json:"end"`
	} `json:"coverage,omitempty"`
}

type freshnessOutput struct {
	GTFSStatic       *sourceFreshness `json:"gtfs_static,omitempty"`
	OpenDataRealtime *sourceFreshness `json:"opendata_realtime,omitempty"`
}

func currentGTFSFreshness(ctx context.Context, store *gtfs.Store) freshnessOutput {
	if store == nil {
		return freshnessOutput{}
	}
	state, err := store.DatasetState(ctx)
	if err != nil {
		return freshnessOutput{GTFSStatic: &sourceFreshness{State: "unknown"}}
	}
	age := 0.0
	if !state.IngestedAt.IsZero() {
		age = maxFloat(0, timeNow().Sub(state.IngestedAt).Hours())
	}
	return freshnessOutput{GTFSStatic: &sourceFreshness{
		State:    "current",
		AgeHours: age,
		Coverage: &struct {
			Start string `json:"start"`
			End   string `json:"end"`
		}{Start: state.Coverage.Start, End: state.Coverage.End},
	}}
}

func freshnessPtr(value freshnessOutput) *freshnessOutput { return &value }

var timeNow = func() time.Time { return time.Now().UTC() }

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func gtfsFeedModes(routeTypes []int) []int {
	if len(routeTypes) == 0 {
		return nil
	}
	result := make([]int, 0, len(routeTypes))
	for _, routeType := range routeTypes {
		result = append(result, apiToFeedModes(routeType)...)
	}
	return result
}

func apiToFeedModes(routeType int) []int {
	switch routeType {
	case 0:
		return []int{2}
	case 1:
		return []int{3}
	case 2:
		return []int{4, 6}
	case 3:
		return []int{1, 5}
	case 4:
		return []int{4, 6}
	default:
		return nil
	}
}
