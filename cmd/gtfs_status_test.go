package cmd

import (
	"context"
	"testing"
	"time"

	"github.com/thesammykins/ptv_cli/internal/config"
	"github.com/thesammykins/ptv_cli/internal/gtfs"
)

func TestStatusFreshnessFallsBackWithinOptionalBudget(t *testing.T) {
	originalCheck := checkGTFSFreshnessForCommand
	originalBudget := gtfsStatusFreshnessBudget
	t.Cleanup(func() {
		checkGTFSFreshnessForCommand = originalCheck
		gtfsStatusFreshnessBudget = originalBudget
	})

	gtfsStatusFreshnessBudget = 20 * time.Millisecond
	checkGTFSFreshnessForCommand = func(
		ctx context.Context,
		_ *config.RuntimeConfig,
		_ gtfs.DatasetState,
		_ time.Time,
		allowNetwork, _ bool,
	) (gtfs.FreshnessReport, error) {
		if allowNetwork {
			<-ctx.Done()
			return gtfs.FreshnessReport{}, ctx.Err()
		}
		return gtfs.FreshnessReport{State: gtfs.FreshnessStale, Reason: "coverage ended"}, nil
	}

	started := time.Now()
	report, warning, err := statusFreshness(
		context.Background(), &config.RuntimeConfig{}, gtfs.DatasetState{}, time.Now(), true,
	)
	if err != nil {
		t.Fatalf("statusFreshness: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("statusFreshness took %s, want bounded fallback", elapsed)
	}
	if report.State != gtfs.FreshnessStale || warning == "" {
		t.Fatalf("report=%+v warning=%q, want local report and warning", report, warning)
	}
}
