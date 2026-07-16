package gtfs

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const freshnessTestSource = "https://example.test/gtfs.zip"

type freshnessDoerFunc func(*http.Request) (*http.Response, error)

func (f freshnessDoerFunc) Do(request *http.Request) (*http.Response, error) {
	return f(request)
}

type freshnessTrackingBody struct {
	read   bool
	closed bool
}

func (b *freshnessTrackingBody) Read([]byte) (int, error) {
	b.read = true
	return 0, errors.New("freshness HEAD response body must not be read")
}

func (b *freshnessTrackingBody) Close() error {
	b.closed = true
	return nil
}

func freshnessHeadResponse(status int, etag, modified string, contentLength int64) *http.Response {
	headers := make(http.Header)
	if etag != "" {
		headers.Set("ETag", etag)
	}
	if modified != "" {
		headers.Set("Last-Modified", modified)
	}
	return &http.Response{
		StatusCode:    status,
		Header:        headers,
		Body:          http.NoBody,
		ContentLength: contentLength,
	}
}

func currentFreshnessDataset(now time.Time, sourceURL, etag string) DatasetState {
	return DatasetState{
		GenerationID: "generation-1",
		Provenance: FeedProvenance{
			SourceURL:       sourceURL,
			ETag:            etag,
			LastModified:    "Wed, 15 Jul 2026 00:00:00 GMT",
			DeclaredBytes:   1_024,
			ActualBytes:     1_000,
			PublicationTime: now.Add(-24 * time.Hour),
		},
		IngestedAt: now.Add(-12 * time.Hour),
		Coverage: ServiceCoverage{
			Start: "20260701",
			End:   "20260731",
		},
	}
}

func freshnessRequestForTest(t *testing.T, dataDir string, dataset DatasetState, sourceURL string, now *time.Time, doer HTTPDoer) FreshnessRequest {
	t.Helper()
	t.Setenv("PTV_GTFS_STALE_DAYS", "")
	return FreshnessRequest{
		DataDir:      dataDir,
		Dataset:      dataset,
		RequestedAt:  *now,
		SourceURL:    sourceURL,
		AllowNetwork: true,
		HTTPDoer:     doer,
		Now:          func() time.Time { return *now },
	}
}

// newTestStore is shared by focused tests in this package. Keep it here until
// the broader GTFS test helpers are consolidated by their owning lane.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestFeedDiffers(t *testing.T) {
	cases := []struct {
		name          string
		localETag     string
		localModified string
		remote        FeedHead
		want          bool
	}{
		{"etag same", `"a"`, "", FeedHead{ETag: `"a"`}, false},
		{"etag differs", `"a"`, "", FeedHead{ETag: `"b"`}, true},
		{"etag preferred over modified", `"a"`, "Mon", FeedHead{ETag: `"a"`, LastModified: "Tue"}, false},
		{"modified fallback differs", "", "Mon", FeedHead{LastModified: "Tue"}, true},
		{"modified fallback same", "", "Mon", FeedHead{LastModified: "Mon"}, false},
		{"no provenance is not comparable", "", "", FeedHead{ETag: `"x"`}, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := feedDiffers(test.localETag, test.localModified, test.remote); got != test.want {
				t.Errorf("feedDiffers = %v, want %v", got, test.want)
			}
		})
	}
}

func TestStaleAfterEnvOverride(t *testing.T) {
	t.Setenv("PTV_GTFS_STALE_DAYS", "")
	if got := StaleAfter(); got != DefaultStaleAfter {
		t.Fatalf("default StaleAfter = %v, want %v", got, DefaultStaleAfter)
	}
	t.Setenv("PTV_GTFS_STALE_DAYS", "3")
	if got := StaleAfter(); got != 3*24*time.Hour {
		t.Errorf("StaleAfter with override = %v, want 72h", got)
	}
	t.Setenv("PTV_GTFS_STALE_DAYS", "garbage")
	if got := StaleAfter(); got != DefaultStaleAfter {
		t.Errorf("StaleAfter with bad override = %v, want default", got)
	}
}

func TestCheckFreshnessDelayedPublicationUsesPublicationAge(t *testing.T) {
	now := time.Date(2026, time.July, 16, 2, 0, 0, 0, time.UTC)
	dataset := currentFreshnessDataset(now, freshnessTestSource, `"same"`)
	dataset.Provenance.PublicationTime = now.Add(-8 * 24 * time.Hour)
	dataset.IngestedAt = now.Add(-time.Hour)
	doer := freshnessDoerFunc(func(*http.Request) (*http.Response, error) {
		return freshnessHeadResponse(http.StatusOK, `"same"`, "", 1_024), nil
	})

	report, err := CheckFreshness(context.Background(), freshnessRequestForTest(t, t.TempDir(), dataset, freshnessTestSource, &now, doer))
	if err != nil {
		t.Fatalf("CheckFreshness: %v", err)
	}
	if report.State != FreshnessStale {
		t.Fatalf("State = %q, want %q (%s)", report.State, FreshnessStale, report.Reason)
	}
	if report.AgeBasis != "publication" {
		t.Errorf("AgeBasis = %q, want publication", report.AgeBasis)
	}
	if report.AgeHours != 8*24 {
		t.Errorf("AgeHours = %v, want %v", report.AgeHours, 8*24)
	}
	if !strings.Contains(report.Reason, "publication age") {
		t.Errorf("Reason = %q, want publication-age evidence", report.Reason)
	}
}

func TestCheckFreshnessUsesIngestOnlyAsLabeledFallback(t *testing.T) {
	now := time.Date(2026, time.July, 16, 2, 0, 0, 0, time.UTC)
	dataset := currentFreshnessDataset(now, freshnessTestSource, `"same"`)
	dataset.Provenance.PublicationTime = time.Time{}
	dataset.IngestedAt = now.Add(-8 * 24 * time.Hour)
	doer := freshnessDoerFunc(func(*http.Request) (*http.Response, error) {
		return freshnessHeadResponse(http.StatusOK, `"same"`, "", 1_024), nil
	})

	report, err := CheckFreshness(context.Background(), freshnessRequestForTest(t, t.TempDir(), dataset, freshnessTestSource, &now, doer))
	if err != nil {
		t.Fatalf("CheckFreshness: %v", err)
	}
	if report.State != FreshnessStale || report.AgeBasis != "ingest_fallback" {
		t.Fatalf("State/AgeBasis = %q/%q, want stale/ingest_fallback", report.State, report.AgeBasis)
	}
}

func TestCheckFreshnessWithoutAnUpstreamCheckCanStillProveLocalStaleness(t *testing.T) {
	now := time.Date(2026, time.July, 16, 2, 0, 0, 0, time.UTC)
	dataset := currentFreshnessDataset(now, freshnessTestSource, `"same"`)
	dataset.Provenance.PublicationTime = now.Add(-8 * 24 * time.Hour)
	dataDir := t.TempDir()
	request := freshnessRequestForTest(t, dataDir, dataset, freshnessTestSource, &now, nil)
	request.AllowNetwork = false

	report, err := CheckFreshness(context.Background(), request)
	if err != nil {
		t.Fatalf("CheckFreshness: %v", err)
	}
	if report.State != FreshnessStale || report.CheckSkipped != "network_disabled" || report.CheckPerformed {
		t.Fatalf("offline stale report = state %q, skipped %q, performed %v", report.State, report.CheckSkipped, report.CheckPerformed)
	}
	if _, err := os.Stat(filepath.Join(dataDir, freshnessStateFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("offline check created mutable freshness state: %v", err)
	}
}

func TestCheckFreshnessSourceChangeIsIsolatedAndSuccessIsThrottled(t *testing.T) {
	initialNow := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)
	now := initialNow
	dataDir := t.TempDir()
	sourceA := "https://a.example.test/gtfs.zip"
	sourceB := "https://b.example.test/gtfs.zip"
	datasetA := currentFreshnessDataset(initialNow, sourceA, `"a"`)
	datasetB := currentFreshnessDataset(initialNow, sourceB, `"b"`)
	hits := map[string]int{}
	doer := freshnessDoerFunc(func(request *http.Request) (*http.Response, error) {
		hits[request.URL.String()]++
		if request.URL.String() == sourceB {
			return freshnessHeadResponse(http.StatusOK, `"b"`, "", 2_000), nil
		}
		return freshnessHeadResponse(http.StatusOK, `"a"`, "", 1_000), nil
	})

	requestA := freshnessRequestForTest(t, dataDir, datasetA, sourceA, &now, doer)
	first, err := CheckFreshness(context.Background(), requestA)
	if err != nil || first.State != FreshnessCurrent {
		t.Fatalf("first source A check = state %q, err %v, reason %q", first.State, err, first.Reason)
	}
	now = initialNow.Add(time.Hour)
	requestA.RequestedAt = now
	second, err := CheckFreshness(context.Background(), requestA)
	if err != nil {
		t.Fatalf("throttled source A check: %v", err)
	}
	if second.CheckSkipped != "success_throttle" || hits[sourceA] != 1 {
		t.Fatalf("second check skipped/hits = %q/%d, want success_throttle/1", second.CheckSkipped, hits[sourceA])
	}

	requestB := freshnessRequestForTest(t, dataDir, datasetB, sourceB, &now, doer)
	third, err := CheckFreshness(context.Background(), requestB)
	if err != nil || third.State != FreshnessCurrent {
		t.Fatalf("source B check = state %q, err %v, reason %q", third.State, err, third.Reason)
	}
	if hits[sourceB] != 1 {
		t.Fatalf("source B hits = %d, want 1; source change must not inherit source A throttle", hits[sourceB])
	}

	now = initialNow.Add(successfulCheckThrottle + time.Second)
	requestA.RequestedAt = now
	fourth, err := CheckFreshness(context.Background(), requestA)
	if err != nil || !fourth.CheckPerformed {
		t.Fatalf("post-throttle source A check = performed %v, err %v", fourth.CheckPerformed, err)
	}
	if hits[sourceA] != 2 {
		t.Fatalf("source A hits after 24h = %d, want 2", hits[sourceA])
	}

	if _, found, err := LoadFreshnessCheckState(context.Background(), dataDir, sourceA); err != nil || !found {
		t.Fatalf("source A persisted = %v, err %v", found, err)
	}
	if _, found, err := LoadFreshnessCheckState(context.Background(), dataDir, sourceB); err != nil || !found {
		t.Fatalf("source B persisted = %v, err %v", found, err)
	}
}

func TestCheckFreshnessMissingValidatorsPreservesProvenLocalStaleness(t *testing.T) {
	now := time.Date(2026, time.July, 16, 2, 0, 0, 0, time.UTC)
	dataset := currentFreshnessDataset(now, freshnessTestSource, `"local"`)
	dataset.Provenance.PublicationTime = now.Add(-8 * 24 * time.Hour)
	doer := freshnessDoerFunc(func(*http.Request) (*http.Response, error) {
		return freshnessHeadResponse(http.StatusOK, "", "", -1), nil
	})

	report, err := CheckFreshness(context.Background(), freshnessRequestForTest(t, t.TempDir(), dataset, freshnessTestSource, &now, doer))
	if err != nil {
		t.Fatalf("CheckFreshness: %v", err)
	}
	if report.State != FreshnessStale {
		t.Fatalf("State = %q, want stale (%s)", report.State, report.Reason)
	}
	if report.Checked {
		t.Fatal("Checked = true without comparable validators")
	}
	if report.LastSuccessAt == "" {
		t.Fatal("successful validator-less HEAD must still record last success")
	}
}

func TestCheckFreshnessFailurePreservesProvenLocalStaleness(t *testing.T) {
	now := time.Date(2026, time.July, 16, 2, 0, 0, 0, time.UTC)
	dataset := currentFreshnessDataset(now, freshnessTestSource, `"local"`)
	dataset.Provenance.PublicationTime = now.Add(-8 * 24 * time.Hour)
	doer := freshnessDoerFunc(func(*http.Request) (*http.Response, error) {
		return freshnessHeadResponse(http.StatusServiceUnavailable, "", "", -1), nil
	})
	report, err := CheckFreshness(context.Background(), freshnessRequestForTest(t, t.TempDir(), dataset, freshnessTestSource, &now, doer))
	if err != nil {
		t.Fatalf("CheckFreshness: %v", err)
	}
	if report.State != FreshnessStale || report.CheckError == "" {
		t.Fatalf("State/CheckError = %q/%q, want stale/non-empty (%s)", report.State, report.CheckError, report.Reason)
	}
}

func TestCheckFreshnessDifferentValidatorsIsChanged(t *testing.T) {
	now := time.Date(2026, time.July, 16, 2, 0, 0, 0, time.UTC)
	dataset := currentFreshnessDataset(now, freshnessTestSource, `"local"`)
	dataset.Provenance.PublicationTime = now.Add(-8 * 24 * time.Hour)
	doer := freshnessDoerFunc(func(*http.Request) (*http.Response, error) {
		return freshnessHeadResponse(http.StatusOK, `"remote"`, "", 2_000), nil
	})

	report, err := CheckFreshness(context.Background(), freshnessRequestForTest(t, t.TempDir(), dataset, freshnessTestSource, &now, doer))
	if err != nil {
		t.Fatalf("CheckFreshness: %v", err)
	}
	if report.State != FreshnessChanged || !report.UpdateAvailable || !report.Checked {
		t.Fatalf("changed report = state %q, update %v, checked %v (%s)", report.State, report.UpdateAvailable, report.Checked, report.Reason)
	}
}

func TestCheckFreshnessPersistsPartialMetadataAndFailure(t *testing.T) {
	now := time.Date(2026, time.July, 16, 2, 0, 0, 0, time.UTC)
	dataDir := t.TempDir()
	dataset := DatasetState{
		GenerationID: "partial-generation",
		Provenance: FeedProvenance{
			SourceURL:   freshnessTestSource,
			ETag:        `"local"`,
			ActualBytes: 321,
		},
		Coverage: ServiceCoverage{Start: "20260701"},
	}
	doer := freshnessDoerFunc(func(*http.Request) (*http.Response, error) {
		return freshnessHeadResponse(http.StatusServiceUnavailable, `"remote"`, "Thu", 654), nil
	})

	report, err := CheckFreshness(context.Background(), freshnessRequestForTest(t, dataDir, dataset, freshnessTestSource, &now, doer))
	if err != nil {
		t.Fatalf("CheckFreshness: %v", err)
	}
	if report.State != FreshnessUnknown || report.CheckError == "" {
		t.Fatalf("State/CheckError = %q/%q, want unknown/non-empty", report.State, report.CheckError)
	}
	state, found, err := LoadFreshnessCheckState(context.Background(), dataDir, freshnessTestSource)
	if err != nil || !found {
		t.Fatalf("LoadFreshnessCheckState = found %v, err %v", found, err)
	}
	if state.GenerationID != "partial-generation" || state.ActualBytes != 321 || state.CoverageStart != "20260701" || state.CoverageEnd != "" {
		t.Errorf("partial local metadata not persisted: %+v", state)
	}
	if state.RemoteETag != `"remote"` || state.RemoteLastModified != "Thu" || state.RemoteContentLength != 654 {
		t.Errorf("partial remote metadata not persisted: %+v", state)
	}
	if state.FailureCount != 1 || state.LastAttemptAt == nil || state.LastSuccessAt != nil || state.NextAutomaticAttempt == nil {
		t.Errorf("failure state not persisted atomically: %+v", state)
	}
}

func TestCheckFreshnessCoverageOutsideRequestedMelbourneDateIsStale(t *testing.T) {
	// This UTC instant is 01:00 on 16 July in Melbourne.
	now := time.Date(2026, time.July, 15, 15, 0, 0, 0, time.UTC)
	dataset := currentFreshnessDataset(now, freshnessTestSource, `"same"`)
	dataset.Coverage.End = "20260715"
	doer := freshnessDoerFunc(func(*http.Request) (*http.Response, error) {
		return freshnessHeadResponse(http.StatusOK, `"same"`, "", 1_000), nil
	})

	report, err := CheckFreshness(context.Background(), freshnessRequestForTest(t, t.TempDir(), dataset, freshnessTestSource, &now, doer))
	if err != nil {
		t.Fatalf("CheckFreshness: %v", err)
	}
	if report.RequestedServiceDate != "20260716" {
		t.Fatalf("RequestedServiceDate = %q, want 20260716", report.RequestedServiceDate)
	}
	if report.State != FreshnessStale || report.CoverageState != "outside" {
		t.Fatalf("State/CoverageState = %q/%q, want stale/outside", report.State, report.CoverageState)
	}
}

func TestCheckFreshnessFailureBackoffAndForce(t *testing.T) {
	now := time.Date(2026, time.July, 16, 2, 0, 0, 0, time.UTC)
	dataDir := t.TempDir()
	dataset := currentFreshnessDataset(now, freshnessTestSource, `"same"`)
	hits := 0
	doer := freshnessDoerFunc(func(*http.Request) (*http.Response, error) {
		hits++
		if hits == 1 {
			return nil, errors.New("temporary upstream failure")
		}
		return freshnessHeadResponse(http.StatusOK, `"same"`, "", 1_000), nil
	})
	request := freshnessRequestForTest(t, dataDir, dataset, freshnessTestSource, &now, doer)

	failed, err := CheckFreshness(context.Background(), request)
	if err != nil {
		t.Fatalf("failed CheckFreshness: %v", err)
	}
	if failed.State != FreshnessUnknown || failed.FailureCount != 1 {
		t.Fatalf("failed state/count = %q/%d, want unknown/1", failed.State, failed.FailureCount)
	}

	now = now.Add(time.Minute)
	request.RequestedAt = now
	backedOff, err := CheckFreshness(context.Background(), request)
	if err != nil {
		t.Fatalf("backed-off CheckFreshness: %v", err)
	}
	if backedOff.CheckSkipped != "failure_backoff" || hits != 1 {
		t.Fatalf("backoff skip/hits = %q/%d, want failure_backoff/1", backedOff.CheckSkipped, hits)
	}

	request.Force = true
	forced, err := CheckFreshness(context.Background(), request)
	if err != nil {
		t.Fatalf("forced CheckFreshness: %v", err)
	}
	if hits != 2 || !forced.CheckPerformed || forced.State != FreshnessCurrent {
		t.Fatalf("forced hits/performed/state = %d/%v/%q, want 2/true/current (%s)", hits, forced.CheckPerformed, forced.State, forced.Reason)
	}
	if forced.FailureCount != 0 || forced.CheckError != "" {
		t.Fatalf("forced success retained failure state: %+v", forced)
	}
}

func TestCheckFreshnessInternalRequestTimeoutIsARecordedCheckFailure(t *testing.T) {
	now := time.Date(2026, time.July, 16, 2, 0, 0, 0, time.UTC)
	dataDir := t.TempDir()
	dataset := currentFreshnessDataset(now, freshnessTestSource, `"same"`)
	doer := freshnessDoerFunc(func(*http.Request) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	})

	report, err := CheckFreshness(context.Background(), freshnessRequestForTest(t, dataDir, dataset, freshnessTestSource, &now, doer))
	if err != nil {
		t.Fatalf("internal request timeout returned an operational error: %v", err)
	}
	if report.State != FreshnessUnknown || report.CheckError != "request timed out" || report.FailureCount != 1 {
		t.Fatalf("timeout report = state %q, error %q, failures %d", report.State, report.CheckError, report.FailureCount)
	}
}

func TestFailedCheckBackoffIsExponentialAndCapped(t *testing.T) {
	tests := []struct {
		failures int
		want     time.Duration
	}{
		{1, 5 * time.Minute},
		{2, 10 * time.Minute},
		{3, 20 * time.Minute},
		{7, 320 * time.Minute},
		{8, 6 * time.Hour},
		{100, 6 * time.Hour},
	}
	for _, test := range tests {
		if got := failedCheckBackoff(test.failures); got != test.want {
			t.Errorf("failedCheckBackoff(%d) = %v, want %v", test.failures, got, test.want)
		}
	}
}

func TestCheckFreshnessPersistsCompleteStateTransactionallyAndDoesNotReadBody(t *testing.T) {
	now := time.Date(2026, time.July, 16, 2, 0, 0, 123, time.UTC)
	dataDir := t.TempDir()
	dataset := currentFreshnessDataset(now, freshnessTestSource, `"same"`)
	body := &freshnessTrackingBody{}
	doer := freshnessDoerFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodHead {
			t.Errorf("method = %q, want HEAD", request.Method)
		}
		if request.Header.Get("Accept-Encoding") != "identity" {
			t.Errorf("Accept-Encoding = %q, want identity", request.Header.Get("Accept-Encoding"))
		}
		if _, ok := request.Context().Deadline(); !ok {
			t.Error("freshness HTTP request has no bounded deadline")
		}
		response := freshnessHeadResponse(http.StatusOK, `"same"`, "Wed, 15 Jul 2026 00:00:00 GMT", 1_111)
		response.Body = body
		return response, nil
	})

	report, err := CheckFreshness(context.Background(), freshnessRequestForTest(t, dataDir, dataset, freshnessTestSource, &now, doer))
	if err != nil || report.State != FreshnessCurrent {
		t.Fatalf("CheckFreshness = state %q, err %v, reason %q", report.State, err, report.Reason)
	}
	if body.read || !body.closed {
		t.Fatalf("HEAD body read/closed = %v/%v, want false/true", body.read, body.closed)
	}

	state, found, err := LoadFreshnessCheckState(context.Background(), dataDir, freshnessTestSource)
	if err != nil || !found {
		t.Fatalf("LoadFreshnessCheckState = found %v, err %v", found, err)
	}
	if state.SourceURL != freshnessTestSource || state.DatasetSourceURL != freshnessTestSource || state.GenerationID != dataset.GenerationID {
		t.Errorf("identity fields not persisted: %+v", state)
	}
	if state.LocalETag != dataset.Provenance.ETag || state.LocalLastModified != dataset.Provenance.LastModified || state.DeclaredBytes != 1_024 || state.ActualBytes != 1_000 {
		t.Errorf("local provenance fields not persisted: %+v", state)
	}
	if state.PublicationTime == nil || !state.PublicationTime.Equal(dataset.Provenance.PublicationTime) || state.IngestedAt == nil || !state.IngestedAt.Equal(dataset.IngestedAt) {
		t.Errorf("publication/ingest fields not persisted: %+v", state)
	}
	if state.CoverageStart != dataset.Coverage.Start || state.CoverageEnd != dataset.Coverage.End || state.LastAttemptAt == nil || state.LastSuccessAt == nil {
		t.Errorf("coverage/check times not persisted: %+v", state)
	}
	if state.Result != FreshnessCurrent || state.CheckError != "" || state.RemoteETag != `"same"` || state.RemoteContentLength != 1_111 || state.FailureCount != 0 || state.NextAutomaticAttempt == nil {
		t.Errorf("result/remote/schedule fields not persisted: %+v", state)
	}

	path, err := FreshnessStateDatabasePath(dataDir)
	if err != nil {
		t.Fatalf("FreshnessStateDatabasePath: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat freshness state database: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("freshness state database permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestCheckFreshnessCancellationInterruptsRequestAndPersistsFailure(t *testing.T) {
	now := time.Date(2026, time.July, 16, 2, 0, 0, 0, time.UTC)
	dataDir := t.TempDir()
	dataset := currentFreshnessDataset(now, freshnessTestSource, `"same"`)
	started := make(chan struct{})
	doer := freshnessDoerFunc(func(request *http.Request) (*http.Response, error) {
		close(started)
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		<-started
		cancel()
	}()

	report, err := CheckFreshness(ctx, freshnessRequestForTest(t, dataDir, dataset, freshnessTestSource, &now, doer))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CheckFreshness error = %v, want context.Canceled", err)
	}
	if report.State != FreshnessUnknown || report.CheckError != "request canceled" {
		t.Fatalf("canceled report = state %q, error %q", report.State, report.CheckError)
	}
	state, found, loadErr := LoadFreshnessCheckState(context.Background(), dataDir, freshnessTestSource)
	if loadErr != nil || !found {
		t.Fatalf("LoadFreshnessCheckState after cancel = found %v, err %v", found, loadErr)
	}
	if state.CheckError != "request canceled" || state.FailureCount != 1 || state.LastAttemptAt == nil {
		t.Errorf("canceled failure not persisted: %+v", state)
	}
}

func TestFreshnessCompatibilityFallbackPreservesLocalEvidence(t *testing.T) {
	store := newTestStore(t)
	ingested := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	if err := store.SetMeta(metaIngestedAt, ingested); err != nil {
		t.Fatalf("SetMeta ingested_at: %v", err)
	}
	if err := store.SetMeta(metaFeedSourceURL, freshnessTestSource); err != nil {
		t.Fatalf("SetMeta source_url: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	report := Freshness(ctx, store, freshnessTestSource, true, false)
	if !report.HasData || report.State != FreshnessUnknown || report.CheckError != "request canceled" {
		t.Fatalf("compatibility fallback = has data %v, state %q, error %q", report.HasData, report.State, report.CheckError)
	}
	if report.IngestedAt != ingested {
		t.Errorf("IngestedAt = %q, want %q", report.IngestedAt, ingested)
	}
}

func TestCheckFreshnessKeepsExactSourceKeyButRedactsPublicReportAndErrors(t *testing.T) {
	const sourceURL = "https://source-user:source-pass@example.test/private-path-token/gtfs.zip?token=source-token"
	now := time.Date(2026, time.July, 16, 2, 0, 0, 0, time.UTC)
	dataDir := t.TempDir()
	dataset := currentFreshnessDataset(now, sourceURL, `"same"`)
	doer := freshnessDoerFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != sourceURL {
			t.Fatalf("freshness request URL = %q, want exact configured URL", request.URL)
		}
		return nil, errors.New("dial failed for " + sourceURL)
	})

	report, err := CheckFreshness(context.Background(), freshnessRequestForTest(t, dataDir, dataset, sourceURL, &now, doer))
	if err != nil {
		t.Fatalf("CheckFreshness() error = %v", err)
	}
	for _, publicText := range []string{report.SourceURL, report.DatasetSourceURL, report.CheckError, report.Reason} {
		for _, secret := range []string{"source-user", "source-pass", "source-token", "private-path-token"} {
			if strings.Contains(publicText, secret) {
				t.Fatalf("freshness report leaked %q in %q", secret, publicText)
			}
		}
	}
	if report.SourceURL != "https://example.test" || report.DatasetSourceURL != report.SourceURL {
		t.Fatalf("redacted report sources = %q / %q", report.SourceURL, report.DatasetSourceURL)
	}

	state, found, err := LoadFreshnessCheckState(context.Background(), dataDir, sourceURL)
	if err != nil || !found {
		t.Fatalf("LoadFreshnessCheckState() = found %v, err %v", found, err)
	}
	if state.SourceURL != sourceURL || state.DatasetSourceURL != sourceURL {
		t.Fatalf("internal source key was not preserved exactly: %+v", state)
	}
}

func TestFreshnessReportRedactsPreviouslyPersistedRawSourceError(t *testing.T) {
	const sourceURL = "https://source-user:source-pass@example.test/private-path-token/gtfs.zip?token=source-token"
	state := FreshnessCheckState{
		SourceURL:        sourceURL,
		DatasetSourceURL: sourceURL,
		Result:           FreshnessUnknown,
		CheckError:       "historical transport failure for " + sourceURL,
	}
	report := reportFromFreshnessState(
		state,
		DatasetState{Provenance: FeedProvenance{SourceURL: sourceURL}},
		localFreshnessEvidence{},
		"latest upstream check failed: "+state.CheckError,
		false,
		"failure_backoff",
	)
	for _, publicText := range []string{report.SourceURL, report.DatasetSourceURL, report.CheckError, report.Reason} {
		for _, secret := range []string{"source-user", "source-pass", "source-token", "private-path-token"} {
			if strings.Contains(publicText, secret) {
				t.Fatalf("historical freshness report leaked %q in %q", secret, publicText)
			}
		}
	}
}
