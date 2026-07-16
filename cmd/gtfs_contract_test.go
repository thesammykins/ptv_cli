package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thesammykins/ptv_cli/internal/gtfs"
	"github.com/thesammykins/ptv_cli/internal/gtfsrt"
)

type realtimeSnapshotFetcherFunc func(context.Context, gtfsrt.Feed) (*gtfsrt.Snapshot, error)

func (f realtimeSnapshotFetcherFunc) FetchSnapshot(ctx context.Context, feed gtfsrt.Feed) (*gtfsrt.Snapshot, error) {
	return f(ctx, feed)
}

func TestGTFSProvenanceOutputRedactsSourceSecretsAndOmitsUnknownPublicationTime(t *testing.T) {
	output := newGTFSProvenanceOutput(gtfs.FeedProvenance{
		SourceURL:     "https://source-user:source-pass@example.test/private-path-token/gtfs.zip?token=source-token",
		ETag:          `"feed"`,
		DeclaredBytes: 123,
		ActualBytes:   123,
	})
	raw, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(raw)
	for _, secret := range []string{"source-user", "source-pass", "source-token", "private-path-token", "gtfs.zip", "0001-01-01"} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("provenance output leaked %q: %s", secret, encoded)
		}
	}
	if strings.Contains(encoded, `"publication_time"`) {
		t.Fatalf("unknown publication time was emitted: %s", encoded)
	}
	if !strings.Contains(encoded, `"source_url":"https://example.test"`) {
		t.Fatalf("redacted source URL missing from provenance: %s", encoded)
	}
}

func TestInspectRealtimeFeedUsesQualifiedPublicIdentifierFields(t *testing.T) {
	feed := gtfsrt.Feed{ID: "test-feed", URL: "https://example.test/feed"}
	fetcher := realtimeSnapshotFetcherFunc(func(context.Context, gtfsrt.Feed) (*gtfsrt.Snapshot, error) {
		return &gtfsrt.Snapshot{
			Feed:   feed,
			Counts: gtfsrt.EntityCounts{Entities: 1, Vehicles: 1},
			Vehicles: []gtfsrt.VehicleObservation{{
				EntityID: "feed-entity-private-looking",
				Label:    "public-label-42",
				TripID:   "static-trip-7",
			}},
		}, nil
	})

	raw, err := json.Marshal(inspectRealtimeFeed(t.Context(), fetcher, feed))
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(raw)
	for _, required := range []string{
		`"sample_public_label":"public-label-42"`,
		`"sample_static_gtfs_trip_id":"static-trip-7"`,
	} {
		if !strings.Contains(encoded, required) {
			t.Fatalf("GTFS-R status missing %s: %s", required, encoded)
		}
	}
	for _, forbidden := range []string{"sample_vehicle_id", `"sample_trip_id"`, "feed-entity-private-looking"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("GTFS-R status exposed misnamed/private identifier %q: %s", forbidden, encoded)
		}
	}
}

func TestInspectRealtimeFeedsPropagatesProcessCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	fetcher := realtimeSnapshotFetcherFunc(func(ctx context.Context, _ gtfsrt.Feed) (*gtfsrt.Snapshot, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	go func() {
		<-started
		cancel()
	}()

	_, err := inspectRealtimeFeeds(ctx, fetcher, []gtfsrt.Feed{{ID: "test-feed"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("inspectRealtimeFeeds() error = %v, want context.Canceled", err)
	}
}

func TestInspectRealtimeFeedsUsesOneSharedDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	requests := 0
	fetcher := realtimeSnapshotFetcherFunc(func(ctx context.Context, _ gtfsrt.Feed) (*gtfsrt.Snapshot, error) {
		requests++
		<-ctx.Done()
		return nil, ctx.Err()
	})

	_, err := inspectRealtimeFeeds(ctx, fetcher, []gtfsrt.Feed{{ID: "one"}, {ID: "two"}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("inspectRealtimeFeeds() error = %v, want context.DeadlineExceeded", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want only the in-flight feed before the shared deadline", requests)
	}
}

func TestGTFSRealtimeCatalogDoesNotReadCredentialFile(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "malformed.env")
	if err := os.WriteFile(envFile, []byte("this is not dotenv\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	previousAll, previousEnv := gtfsRealtimeAll, flagEnv
	gtfsRealtimeAll = false
	t.Cleanup(func() {
		gtfsRealtimeAll = previousAll
		flagEnv = previousEnv
	})
	stdout, stderr, err := executeCommand(t, "--env-file", envFile, "--json", "gtfs", "realtime")
	if err != nil {
		t.Fatalf("catalog consulted irrelevant credential file: %v", err)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if !json.Valid([]byte(stdout)) || !strings.Contains(stdout, `"feeds"`) {
		t.Fatalf("catalog stdout is not one feed document: %s", stdout)
	}
}
