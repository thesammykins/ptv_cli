package gtfsrt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gtfs "github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"google.golang.org/protobuf/proto"
)

func TestFetchSnapshotUsesOneExactKeyIDRequest(t *testing.T) {
	now := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)
	message := vehicleFeed(now, now.Add(-20*time.Second), "public-931M", "internal-42")
	body, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	client := NewWithOptions("subscription-key", ClientOptions{
		Now: func() time.Time { return now },
		HTTPClient: &http.Client{Transport: transportFunc(func(request *http.Request) (*http.Response, error) {
			requests.Add(1)
			if got := request.Header[KeyIDHeader]; len(got) != 1 || got[0] != "subscription-key" {
				t.Errorf("exact KeyID header = %v; headers=%v", got, request.Header)
			}
			if got := request.Header.Get("Authorization"); got != "" {
				t.Errorf("Authorization unexpectedly sent: %q", got)
			}
			if got := request.Header.Get("Ocp-Apim-Subscription-Key"); got != "" {
				t.Errorf("alternate subscription header unexpectedly sent: %q", got)
			}
			return protobufResponse(http.StatusOK, body), nil
		})},
	})
	feed := testFeed()

	snapshot, err := client.FetchSnapshot(context.Background(), feed)
	if err != nil {
		t.Fatalf("FetchSnapshot: %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
	if snapshot.Feed.ID != feed.ID || snapshot.GTFSRealtime != "2.0" || snapshot.Incrementality != IncrementalityFullDataset {
		t.Fatalf("snapshot header = %+v", snapshot)
	}
	if snapshot.FeedTimestamp == nil || !snapshot.FeedTimestamp.Equal(now) || !snapshot.FetchedAt.Equal(now) {
		t.Fatalf("snapshot times feed=%v fetched=%v", snapshot.FeedTimestamp, snapshot.FetchedAt)
	}
	if snapshot.Counts.Entities != 1 || snapshot.Counts.Vehicles != 1 || len(snapshot.Entities) != 1 || len(snapshot.Vehicles) != 1 {
		t.Fatalf("snapshot counts=%+v entities=%d vehicles=%d", snapshot.Counts, len(snapshot.Entities), len(snapshot.Vehicles))
	}
	observation := snapshot.Vehicles[0]
	if observation.EntityID != FeedEntityID("shared-string") || observation.TripID != StaticTripID("shared-string") || observation.Label != PublicVehicleLabel("public-931M") {
		t.Fatalf("identifier namespaces not retained: %+v", observation)
	}
	if observation.Freshness.Feed.State != FreshnessCurrent || observation.Freshness.Entity.State != FreshnessCurrent || observation.Freshness.Overall != FreshnessCurrent {
		t.Fatalf("freshness = %+v", observation.Freshness)
	}
}

func TestClientSendsNoBearerAuthentication(t *testing.T) {
	now := time.Now().UTC()
	body, _ := proto.Marshal(vehicleFeed(now, now, "label", "internal"))
	client := New("subscription-key")
	client.http = &http.Client{Transport: transportFunc(func(request *http.Request) (*http.Response, error) {
		if authorization := request.Header.Get("Authorization"); authorization != "" {
			t.Fatalf("Authorization = %q", authorization)
		}
		return protobufResponse(http.StatusOK, body), nil
	})}
	client.now = func() time.Time { return now }
	if _, err := client.FetchSnapshot(context.Background(), testFeed()); err != nil {
		t.Fatal(err)
	}
}

func TestMissingKeyIDFailsWithoutRequest(t *testing.T) {
	var requests atomic.Int32
	client := NewWithOptions("", ClientOptions{HTTPClient: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("must not run")
	})}})
	_, err := client.FetchSnapshot(context.Background(), testFeed())
	if !IsKind(err, ErrorAuthentication) {
		t.Fatalf("error = %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("requests = %d, want 0", requests.Load())
	}
}

func TestSnapshotIdentifierNamespacesCannotMatchAccidentally(t *testing.T) {
	now := time.Now().UTC()
	snapshot := NormalizeSnapshot(testFeed(), vehicleFeed(now, now, "public-label", "private-internal"), now)

	if _, ok := snapshot.FindVehicleByLabel(PublicVehicleLabel("private-internal")); ok {
		t.Fatal("internal vehicle ID matched public label lookup")
	}
	if observation, ok := snapshot.FindVehicleByLabel(PublicVehicleLabel("public-label")); !ok || observation.TripID != StaticTripID("shared-string") {
		t.Fatalf("public label lookup = %+v, %t", observation, ok)
	}
	if entity, ok := snapshot.FindEntity(FeedEntityID("shared-string")); !ok || !entity.HasVehicle {
		t.Fatalf("entity lookup = %+v, %t", entity, ok)
	}

	encoded, err := json.Marshal(snapshot.Vehicles[0])
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("private-internal")) || bytes.Contains(encoded, []byte("internal_vehicle")) {
		t.Fatalf("internal ID leaked in JSON: %s", encoded)
	}
}

func TestDuplicatePublicLabelPrefersCurrentObservation(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	feedTimestamp := uint64(now.Add(-10 * time.Second).Unix())
	staleTimestamp := uint64(now.Add(-2 * time.Minute).Unix())
	currentTimestamp := uint64(now.Add(-20 * time.Second).Unix())
	message := &gtfs.FeedMessage{
		Header: &gtfs.FeedHeader{GtfsRealtimeVersion: proto.String("2.0"), Timestamp: &feedTimestamp},
		Entity: []*gtfs.FeedEntity{
			{
				Id: proto.String("stale-entity"),
				Vehicle: &gtfs.VehiclePosition{
					Vehicle:   &gtfs.VehicleDescriptor{Label: proto.String("931M-932M")},
					Timestamp: &staleTimestamp,
				},
			},
			{
				Id: proto.String("current-entity"),
				Vehicle: &gtfs.VehiclePosition{
					Vehicle:   &gtfs.VehicleDescriptor{Label: proto.String("931M-932M")},
					Timestamp: &currentTimestamp,
				},
			},
		},
	}
	snapshot := NormalizeSnapshot(testFeed(), message, now)

	observation, ok := snapshot.FindVehicleByLabel(PublicVehicleLabel("931M"))
	if !ok || observation.EntityID != FeedEntityID("current-entity") || observation.Freshness.Overall != FreshnessCurrent {
		t.Fatalf("collision resolution = %+v, %t", observation, ok)
	}
}

func TestObservationFreshnessClassifications(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		feedOffset   *time.Duration
		entityOffset *time.Duration
		wantFeed     FreshnessState
		wantEntity   FreshnessState
		wantOverall  FreshnessState
	}{
		{"fresh", duration(-10 * time.Second), duration(-20 * time.Second), FreshnessCurrent, FreshnessCurrent, FreshnessCurrent},
		{"stale entity", duration(-10 * time.Second), duration(-91 * time.Second), FreshnessCurrent, FreshnessStale, FreshnessStale},
		{"stale feed fresh entity", duration(-91 * time.Second), duration(-10 * time.Second), FreshnessStale, FreshnessCurrent, FreshnessStale},
		{"future entity", duration(0), duration(31 * time.Second), FreshnessCurrent, FreshnessFuture, FreshnessFuture},
		{"missing entity", duration(0), nil, FreshnessCurrent, FreshnessUnknown, FreshnessUnknown},
		{"missing feed", nil, duration(-10 * time.Second), FreshnessUnknown, FreshnessCurrent, FreshnessUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message := vehicleFeedWithOptionalTimes(now, test.feedOffset, test.entityOffset)
			snapshot := NormalizeSnapshot(testFeed(), message, now)
			got := snapshot.Vehicles[0].Freshness
			if got.Feed.State != test.wantFeed || got.Entity.State != test.wantEntity || got.Overall != test.wantOverall {
				t.Fatalf("freshness = %+v, want feed=%s entity=%s overall=%s", got, test.wantFeed, test.wantEntity, test.wantOverall)
			}
		})
	}
}

func TestDifferentialIncrementalityIsExposed(t *testing.T) {
	value := gtfs.FeedHeader_DIFFERENTIAL
	message := vehicleFeed(time.Now(), time.Now(), "label", "internal")
	message.Header.Incrementality = &value
	snapshot := NormalizeSnapshot(testFeed(), message, time.Now())
	if snapshot.Incrementality != IncrementalityDifferential {
		t.Fatalf("Incrementality = %q", snapshot.Incrementality)
	}
}

func TestGTFSRealtimeBodyIsBounded(t *testing.T) {
	client := NewWithOptions("key", ClientOptions{
		MaxProtobufBytes: 16,
		HTTPClient: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
			return protobufResponse(http.StatusOK, bytes.Repeat([]byte{1}, 17)), nil
		})},
	})
	_, err := client.FetchSnapshot(context.Background(), testFeed())
	if !IsKind(err, ErrorInvalidResponse) || !strings.Contains(err.Error(), "16-byte limit") {
		t.Fatalf("error = %v", err)
	}
}

func TestGTFSRealtimeErrorsAreTypedAndNotRetried(t *testing.T) {
	var requests atomic.Int32
	client := NewWithOptions("key", ClientOptions{
		HTTPClient: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
			requests.Add(1)
			response := protobufResponse(http.StatusTooManyRequests, []byte("slow down"))
			response.Header.Set("Retry-After", "9999")
			return response, nil
		})},
	})
	_, err := client.FetchSnapshot(context.Background(), testFeed())
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Kind != ErrorRateLimit || apiErr.RetryAfter != maximumRetryAfter {
		t.Fatalf("error = %#v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
}

func TestConfiguredKeyIDCannotBeReflectedByUpstreamErrors(t *testing.T) {
	const secret = "super-secret-keyid-7f83"
	tests := []struct {
		name      string
		transport transportFunc
	}{
		{
			name: "HTTP response body",
			transport: func(request *http.Request) (*http.Response, error) {
				return protobufResponse(http.StatusUnauthorized, []byte("reflected "+request.Header.Get(KeyIDHeader))), nil
			},
		},
		{
			name: "transport error",
			transport: func(request *http.Request) (*http.Response, error) {
				return nil, errors.New("reflected " + request.Header.Get(KeyIDHeader))
			},
		},
		{
			name: "protobuf decode error",
			transport: func(request *http.Request) (*http.Response, error) {
				return protobufResponse(http.StatusOK, []byte("reflected "+request.Header.Get(KeyIDHeader))), nil
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := NewWithOptions(secret, ClientOptions{
				HTTPClient: &http.Client{Transport: test.transport},
			})
			feed := testFeed()
			feed.ID = "feed-reflected-" + secret

			_, err := client.FetchSnapshot(context.Background(), feed)
			if err == nil {
				t.Fatal("FetchSnapshot unexpectedly succeeded")
			}
			assertCredentialAbsentFromErrorSurface(t, err, secret)
		})
	}
}

func assertCredentialAbsentFromErrorSurface(t *testing.T, err error, credential string) {
	t.Helper()
	for current := err; current != nil; current = errors.Unwrap(current) {
		if strings.Contains(current.Error(), credential) {
			t.Fatalf("credential leaked through error chain: %q", current.Error())
		}
	}
	encoded, marshalErr := json.Marshal(err)
	if marshalErr != nil {
		t.Fatalf("marshal error surface: %v", marshalErr)
	}
	if bytes.Contains(encoded, []byte(credential)) {
		t.Fatalf("credential leaked through JSON-safe error surface: %s", encoded)
	}
}

func TestGTFSRealtimeCancellationIsTyped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := NewWithOptions("key", ClientOptions{HTTPClient: &http.Client{Transport: transportFunc(func(request *http.Request) (*http.Response, error) {
		return nil, request.Context().Err()
	})}})
	_, err := client.FetchSnapshot(ctx, testFeed())
	if !IsKind(err, ErrorCanceled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestGTFSRealtimeCancellationDuringBodyReadIsTyped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	client := NewWithOptions("key", ClientOptions{HTTPClient: &http.Client{Transport: transportFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       &contextResponseBody{ctx: request.Context(), started: started},
		}, nil
	})}})
	result := make(chan error, 1)
	go func() {
		_, err := client.FetchSnapshot(ctx, testFeed())
		result <- err
	}()
	<-started
	cancel()
	select {
	case err := <-result:
		if !IsKind(err, ErrorCanceled) || !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("FetchSnapshot did not stop after body-read cancellation")
	}
}

func TestCatalogFeedsDeclareKeyIDAuthentication(t *testing.T) {
	for _, feed := range Feeds() {
		if feed.Authentication.Header != KeyIDHeader || !feed.Authentication.Required {
			t.Fatalf("feed %s authentication = %+v", feed.ID, feed.Authentication)
		}
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (function transportFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type contextResponseBody struct {
	ctx     context.Context
	started chan struct{}
}

func (body *contextResponseBody) Read([]byte) (int, error) {
	close(body.started)
	<-body.ctx.Done()
	return 0, body.ctx.Err()
}

func (*contextResponseBody) Close() error { return nil }

func protobufResponse(status int, body []byte) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}

func testFeed() Feed {
	return Feed{
		ID:             "test-vehicle-positions",
		Mode:           "bus",
		Kind:           FeedKindVehiclePositions,
		Title:          "Test vehicle positions",
		URL:            "https://example.invalid/vehicle-positions",
		Authentication: FeedAuthentication{Header: KeyIDHeader, Required: true},
	}
}

func vehicleFeed(feedTime, entityTime time.Time, label, internalID string) *gtfs.FeedMessage {
	feedTimestamp := uint64(feedTime.Unix())
	entityTimestamp := uint64(entityTime.Unix())
	return makeVehicleFeed(&feedTimestamp, &entityTimestamp, label, internalID)
}

func vehicleFeedWithOptionalTimes(now time.Time, feedOffset, entityOffset *time.Duration) *gtfs.FeedMessage {
	var feedTimestamp *uint64
	if feedOffset != nil {
		timestamp := uint64(now.Add(*feedOffset).Unix())
		feedTimestamp = &timestamp
	}
	var entityTimestamp *uint64
	if entityOffset != nil {
		timestamp := uint64(now.Add(*entityOffset).Unix())
		entityTimestamp = &timestamp
	}
	return makeVehicleFeed(feedTimestamp, entityTimestamp, "public-label", "internal-42")
}

func makeVehicleFeed(feedTimestamp, entityTimestamp *uint64, label, internalID string) *gtfs.FeedMessage {
	full := gtfs.FeedHeader_FULL_DATASET
	header := &gtfs.FeedHeader{
		GtfsRealtimeVersion: proto.String("2.0"),
		Incrementality:      &full,
		Timestamp:           feedTimestamp,
	}
	position := &gtfs.VehiclePosition{
		Trip: &gtfs.TripDescriptor{
			TripId:  proto.String("shared-string"),
			RouteId: proto.String("route-1"),
		},
		Vehicle: &gtfs.VehicleDescriptor{
			Id:    proto.String(internalID),
			Label: proto.String(label),
		},
		Timestamp: entityTimestamp,
	}
	return &gtfs.FeedMessage{
		Header: header,
		Entity: []*gtfs.FeedEntity{{
			Id:      proto.String("shared-string"),
			Vehicle: position,
		}},
	}
}

func duration(value time.Duration) *time.Duration { return &value }
