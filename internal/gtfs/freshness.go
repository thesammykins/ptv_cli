package gtfs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/thesammykins/ptv_cli/internal/localtime"
)

const (
	metaIngestedAt    = "ingested_at"
	metaFeedSourceURL = "source_url"
	metaFeedETag      = "feed_etag"
	metaFeedModified  = "feed_last_modified"
	metaFeedSize      = "feed_content_length"
	metaCoverageStart = "coverage_start"
	metaCoverageEnd   = "coverage_end"

	// DefaultStaleAfter is the maximum publication age before local feed data
	// is stale. Ingest time is used only when publication time is unavailable.
	DefaultStaleAfter = 7 * 24 * time.Hour

	successfulCheckThrottle = 24 * time.Hour
	failedBackoffInitial    = 5 * time.Minute
	failedBackoffMaximum    = 6 * time.Hour
	freshnessRequestTimeout = 30 * time.Second
)

// StaleAfter resolves the staleness threshold, honouring
// PTV_GTFS_STALE_DAYS for compatibility with existing installations.
func StaleAfter() time.Duration {
	if value := os.Getenv("PTV_GTFS_STALE_DAYS"); value != "" {
		if days, err := strconv.Atoi(value); err == nil && days > 0 {
			return time.Duration(days) * 24 * time.Hour
		}
	}
	return DefaultStaleAfter
}

// FreshnessClassification is the complete public freshness state machine.
type FreshnessClassification string

const (
	FreshnessCurrent FreshnessClassification = "current"
	FreshnessChanged FreshnessClassification = "changed"
	FreshnessStale   FreshnessClassification = "stale"
	FreshnessUnknown FreshnessClassification = "unknown"
)

// HTTPDoer is the injectable HTTP boundary used by freshness checks.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// FreshnessRequest supplies local dataset evidence and check policy. Dataset
// metadata is copied into a separate source-keyed state database; the published
// generation is never mutated.
type FreshnessRequest struct {
	DataDir      string
	Dataset      DatasetState
	RequestedAt  time.Time
	SourceURL    string
	AllowNetwork bool
	Force        bool
	HTTPDoer     HTTPDoer
	Now          func() time.Time
}

// FeedHead carries the bounded HEAD response metadata used for comparison.
type FeedHead struct {
	ETag          string `json:"etag,omitempty"`
	LastModified  string `json:"last_modified,omitempty"`
	ContentLength int64  `json:"content_length,omitempty"`
}

// FreshnessReport explains the classification and retains truthful
// current-major compatibility fields used by existing command renderers.
type FreshnessReport struct {
	State                FreshnessClassification `json:"state"`
	Reason               string                  `json:"reason"`
	SourceURL            string                  `json:"source_url"`
	DatasetSourceURL     string                  `json:"dataset_source_url,omitempty"`
	RequestedServiceDate string                  `json:"requested_service_date"`
	Coverage             ServiceCoverage         `json:"coverage"`
	CoverageState        string                  `json:"coverage_state"`
	PublicationTime      string                  `json:"publication_time,omitempty"`
	IngestedAt           string                  `json:"ingested_at,omitempty"`
	AgeBasis             string                  `json:"age_basis,omitempty"`
	AgeHours             float64                 `json:"age_hours,omitempty"`
	ActualBytes          int64                   `json:"actual_bytes,omitempty"`
	StaleAfterDays       float64                 `json:"stale_after_days"`
	FeedETag             string                  `json:"feed_etag,omitempty"`
	FeedModified         string                  `json:"feed_last_modified,omitempty"`
	RemoteETag           string                  `json:"remote_etag,omitempty"`
	RemoteModified       string                  `json:"remote_last_modified,omitempty"`
	RemoteContentLength  int64                   `json:"remote_content_length,omitempty"`
	LastAttemptAt        string                  `json:"last_attempt_at,omitempty"`
	LastSuccessAt        string                  `json:"last_success_at,omitempty"`
	NextAutomaticAttempt string                  `json:"next_automatic_attempt_at,omitempty"`
	FailureCount         int                     `json:"failure_count"`
	CheckPerformed       bool                    `json:"check_performed"`
	CheckSkipped         string                  `json:"check_skipped,omitempty"`
	CheckError           string                  `json:"check_error,omitempty"`
	HasData              bool                    `json:"has_data"`

	// Compatibility fields. Checked means a successful comparable validator
	// result exists, not merely that an HTTP request happened.
	Stale           bool   `json:"stale"`
	Checked         bool   `json:"checked"`
	CheckedAt       string `json:"checked_at,omitempty"`
	UpdateAvailable bool   `json:"update_available"`
}

type localFreshnessEvidence struct {
	requestedDate string
	coverageState string
	ageBasis      string
	ageHours      float64
	ageKnown      bool
	localStale    bool
	staleReason   string
	metadataIssue string
}

type validatorComparison int

const (
	validatorsMissing validatorComparison = iota
	validatorsEqual
	validatorsDifferent
)

// CheckFreshness evaluates and transactionally persists freshness state for
// one source URL. Network failures are represented as an unknown report with
// CheckError; operational state-database errors are returned.
func CheckFreshness(ctx context.Context, request FreshnessRequest) (FreshnessReport, error) {
	if err := ctx.Err(); err != nil {
		return FreshnessReport{}, err
	}
	sourceURL, err := validateFreshnessSourceURL(request.SourceURL)
	if err != nil {
		return FreshnessReport{}, err
	}
	now := time.Now().UTC()
	if request.Now != nil {
		now = request.Now().UTC()
	}
	requestedAt := request.RequestedAt
	if requestedAt.IsZero() {
		requestedAt = now
	}
	evidence := evaluateLocalFreshness(request.Dataset, requestedAt, now)
	if !request.AllowNetwork {
		// Offline evaluation is deliberately pure: --no-update-check must not
		// create or require mutable state beside an immutable generation.
		state := FreshnessCheckState{SourceURL: sourceURL, RemoteContentLength: -1, Result: FreshnessUnknown}
		applyDatasetToFreshnessState(&state, request.Dataset, sourceURL)
		classification, reason := classifyFreshness(state, request.Dataset, evidence)
		state.Result = classification
		return reportFromFreshnessState(state, request.Dataset, evidence, reason, false, "network_disabled"), nil
	}

	db, _, err := openFreshnessStateDB(ctx, request.DataDir)
	if err != nil {
		return FreshnessReport{}, err
	}
	defer db.Close()

	state, found, err := loadFreshnessCheckState(ctx, db, sourceURL)
	if err != nil {
		return FreshnessReport{}, err
	}
	if !found {
		state = FreshnessCheckState{
			SourceURL:           sourceURL,
			RemoteContentLength: -1,
			Result:              FreshnessUnknown,
		}
	}
	applyDatasetToFreshnessState(&state, request.Dataset, sourceURL)

	checkPerformed := false
	checkSkipped := ""
	if !request.Force && state.NextAutomaticAttempt != nil && state.NextAutomaticAttempt.After(now) {
		if state.FailureCount > 0 {
			checkSkipped = "failure_backoff"
		} else {
			checkSkipped = "success_throttle"
		}
	} else {
		checkPerformed = true
		attempt := now
		state.LastAttemptAt = &attempt
		head, err := headFeed(ctx, request.HTTPDoer, sourceURL)
		state.RemoteETag = strings.TrimSpace(head.ETag)
		state.RemoteLastModified = strings.TrimSpace(head.LastModified)
		state.RemoteContentLength = head.ContentLength
		if err != nil {
			state.CheckError = conciseFreshnessError(err, sourceURL)
			state.FailureCount++
			next := now.Add(failedCheckBackoff(state.FailureCount))
			state.NextAutomaticAttempt = &next
		} else {
			success := now
			state.LastSuccessAt = &success
			state.CheckError = ""
			state.FailureCount = 0
			next := now.Add(successfulCheckThrottle)
			state.NextAutomaticAttempt = &next
		}
	}

	classification, stateReason := classifyFreshness(state, request.Dataset, evidence)
	state.Result = classification
	state.UpdatedAt = now
	persistCtx := ctx
	cancelPersist := func() {}
	if ctx.Err() != nil {
		persistCtx, cancelPersist = context.WithTimeout(context.Background(), time.Second)
	}
	defer cancelPersist()
	if err := persistFreshnessCheckState(persistCtx, db, state); err != nil {
		if ctx.Err() != nil {
			return reportFromFreshnessState(state, request.Dataset, evidence, stateReason, checkPerformed, checkSkipped), errors.Join(ctx.Err(), err)
		}
		return FreshnessReport{}, err
	}

	report := reportFromFreshnessState(state, request.Dataset, evidence, stateReason, checkPerformed, checkSkipped)
	if ctx.Err() != nil {
		return report, ctx.Err()
	}
	return report, nil
}

func validateFreshnessSourceURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("GTFS freshness source URL must be absolute HTTP(S)")
	}
	return raw, nil
}

func applyDatasetToFreshnessState(state *FreshnessCheckState, dataset DatasetState, sourceURL string) {
	state.SourceURL = sourceURL
	state.DatasetSourceURL = strings.TrimSpace(dataset.Provenance.SourceURL)
	state.GenerationID = dataset.GenerationID
	state.LocalETag = strings.TrimSpace(dataset.Provenance.ETag)
	state.LocalLastModified = strings.TrimSpace(dataset.Provenance.LastModified)
	state.DeclaredBytes = dataset.Provenance.DeclaredBytes
	state.ActualBytes = dataset.Provenance.ActualBytes
	state.PublicationTime = timePointer(dataset.Provenance.PublicationTime)
	state.IngestedAt = timePointer(dataset.IngestedAt)
	state.CoverageStart = dataset.Coverage.Start
	state.CoverageEnd = dataset.Coverage.End
}

func evaluateLocalFreshness(dataset DatasetState, requestedAt, now time.Time) localFreshnessEvidence {
	evidence := localFreshnessEvidence{
		requestedDate: localtime.InMelbourne(requestedAt).Format("20060102"),
		coverageState: "unknown",
	}
	startValid := validServiceDate(dataset.Coverage.Start)
	endValid := validServiceDate(dataset.Coverage.End)
	if startValid && endValid && dataset.Coverage.Start <= dataset.Coverage.End {
		if evidence.requestedDate < dataset.Coverage.Start || evidence.requestedDate > dataset.Coverage.End {
			evidence.coverageState = "outside"
			evidence.localStale = true
			evidence.staleReason = fmt.Sprintf("requested service date %s is outside local coverage %s-%s", evidence.requestedDate, dataset.Coverage.Start, dataset.Coverage.End)
		} else {
			evidence.coverageState = "covered"
		}
	} else if dataset.Coverage.Start != "" || dataset.Coverage.End != "" {
		evidence.metadataIssue = "local service coverage metadata is incomplete or invalid"
	}

	ageTime := dataset.Provenance.PublicationTime
	if !ageTime.IsZero() {
		evidence.ageBasis = "publication"
	} else if !dataset.IngestedAt.IsZero() {
		ageTime = dataset.IngestedAt
		evidence.ageBasis = "ingest_fallback"
	}
	if !ageTime.IsZero() {
		age := now.Sub(ageTime.UTC())
		if age < 0 {
			evidence.metadataIssue = joinFreshnessReason(evidence.metadataIssue, evidence.ageBasis+" time is in the future")
		} else {
			evidence.ageKnown = true
			evidence.ageHours = age.Hours()
			if age > StaleAfter() && evidence.coverageState != "outside" {
				evidence.localStale = true
				evidence.staleReason = fmt.Sprintf("local feed %s age %.1f days exceeds %.1f days", evidence.ageBasis, age.Hours()/24, StaleAfter().Hours()/24)
			}
		}
	} else {
		evidence.metadataIssue = joinFreshnessReason(evidence.metadataIssue, "local publication and ingest times are missing")
	}
	return evidence
}

func classifyFreshness(state FreshnessCheckState, dataset DatasetState, evidence localFreshnessEvidence) (FreshnessClassification, string) {
	if evidence.coverageState == "outside" {
		return FreshnessStale, evidence.staleReason
	}
	if state.CheckError != "" {
		if evidence.localStale {
			return FreshnessStale, joinFreshnessReason(evidence.staleReason, "latest upstream check failed: "+state.CheckError)
		}
		return FreshnessUnknown, "latest upstream check failed: " + state.CheckError
	}

	sameSource := strings.TrimSpace(dataset.Provenance.SourceURL) != "" && strings.TrimSpace(dataset.Provenance.SourceURL) == state.SourceURL
	comparison := validatorsMissing
	if sameSource && state.LastSuccessAt != nil {
		comparison = compareFeedValidators(dataset.Provenance.ETag, dataset.Provenance.LastModified, FeedHead{
			ETag:         state.RemoteETag,
			LastModified: state.RemoteLastModified,
		})
	}
	switch comparison {
	case validatorsDifferent:
		return FreshnessChanged, "upstream validators differ from the published local generation"
	case validatorsEqual:
		if evidence.localStale {
			return FreshnessStale, evidence.staleReason
		}
		if evidence.metadataIssue != "" {
			return FreshnessUnknown, evidence.metadataIssue
		}
		return FreshnessCurrent, "successful upstream check has comparable equal validators and local coverage/publication are current"
	default:
		// Missing comparable validators can never prove current data, but they
		// also cannot erase independently proven publication/coverage staleness.
		if evidence.localStale {
			return FreshnessStale, evidence.staleReason
		}
		if sameSource && state.LastSuccessAt != nil {
			return FreshnessUnknown, "latest successful upstream check has no comparable local and remote validators"
		}
		if evidence.localStale {
			return FreshnessStale, evidence.staleReason
		}
		if !sameSource {
			return FreshnessUnknown, "local dataset source does not match the requested feed source"
		}
		if evidence.metadataIssue != "" {
			return FreshnessUnknown, evidence.metadataIssue
		}
		return FreshnessUnknown, "no successful check has comparable local and remote validators"
	}
}

func reportFromFreshnessState(state FreshnessCheckState, dataset DatasetState, evidence localFreshnessEvidence, reason string, checkPerformed bool, checkSkipped string) FreshnessReport {
	comparison := validatorsMissing
	if state.CheckError == "" && state.LastSuccessAt != nil && strings.TrimSpace(dataset.Provenance.SourceURL) == state.SourceURL {
		comparison = compareFeedValidators(dataset.Provenance.ETag, dataset.Provenance.LastModified, FeedHead{ETag: state.RemoteETag, LastModified: state.RemoteLastModified})
	}
	publicReason := redactSourceText(reason, state.SourceURL)
	publicCheckError := redactSourceText(state.CheckError, state.SourceURL)
	report := FreshnessReport{
		State:                state.Result,
		Reason:               publicReason,
		SourceURL:            RedactSourceURL(state.SourceURL),
		DatasetSourceURL:     RedactSourceURL(state.DatasetSourceURL),
		RequestedServiceDate: evidence.requestedDate,
		Coverage:             dataset.Coverage,
		CoverageState:        evidence.coverageState,
		AgeBasis:             evidence.ageBasis,
		AgeHours:             evidence.ageHours,
		ActualBytes:          state.ActualBytes,
		StaleAfterDays:       StaleAfter().Hours() / 24,
		FeedETag:             state.LocalETag,
		FeedModified:         state.LocalLastModified,
		RemoteETag:           state.RemoteETag,
		RemoteModified:       state.RemoteLastModified,
		RemoteContentLength:  state.RemoteContentLength,
		FailureCount:         state.FailureCount,
		CheckPerformed:       checkPerformed,
		CheckSkipped:         checkSkipped,
		CheckError:           publicCheckError,
		HasData:              datasetHasFreshnessEvidence(dataset),
		Stale:                state.Result == FreshnessStale,
		Checked:              comparison != validatorsMissing,
		UpdateAvailable:      state.Result == FreshnessChanged,
	}
	if state.PublicationTime != nil {
		report.PublicationTime = state.PublicationTime.UTC().Format(time.RFC3339)
	}
	if state.IngestedAt != nil {
		report.IngestedAt = state.IngestedAt.UTC().Format(time.RFC3339)
	}
	if state.LastAttemptAt != nil {
		report.LastAttemptAt = state.LastAttemptAt.UTC().Format(time.RFC3339)
	}
	if state.LastSuccessAt != nil {
		report.LastSuccessAt = state.LastSuccessAt.UTC().Format(time.RFC3339)
	}
	if state.NextAutomaticAttempt != nil {
		report.NextAutomaticAttempt = state.NextAutomaticAttempt.UTC().Format(time.RFC3339)
	}
	if report.Checked && state.LastAttemptAt != nil {
		report.CheckedAt = state.LastAttemptAt.UTC().Format(time.RFC3339)
	}
	return report
}

func compareFeedValidators(localETag, localModified string, remote FeedHead) validatorComparison {
	localETag = strings.TrimSpace(localETag)
	localModified = strings.TrimSpace(localModified)
	remoteETag := strings.TrimSpace(remote.ETag)
	remoteModified := strings.TrimSpace(remote.LastModified)
	if localETag != "" && remoteETag != "" {
		if localETag == remoteETag {
			return validatorsEqual
		}
		return validatorsDifferent
	}
	if localModified != "" && remoteModified != "" {
		if localModified == remoteModified {
			return validatorsEqual
		}
		return validatorsDifferent
	}
	return validatorsMissing
}

// feedDiffers is retained for focused compatibility tests. False means equal
// or not comparable; callers that need confidence must use CheckFreshness.
func feedDiffers(localETag, localModified string, remote FeedHead) bool {
	return compareFeedValidators(localETag, localModified, remote) == validatorsDifferent
}

func headFeed(ctx context.Context, doer HTTPDoer, sourceURL string) (FeedHead, error) {
	if doer == nil {
		doer = &http.Client{Timeout: freshnessRequestTimeout}
	}
	requestCtx, cancel := context.WithTimeout(ctx, freshnessRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodHead, sourceURL, nil)
	if err != nil {
		return FeedHead{ContentLength: -1}, err
	}
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := doer.Do(req)
	if err != nil {
		return FeedHead{ContentLength: -1}, err
	}
	if resp == nil {
		return FeedHead{ContentLength: -1}, errors.New("GTFS feed HEAD returned a nil response")
	}
	if resp.Body != nil {
		defer resp.Body.Close()
	}
	head := FeedHead{
		ETag:          resp.Header.Get("ETag"),
		LastModified:  resp.Header.Get("Last-Modified"),
		ContentLength: resp.ContentLength,
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return head, fmt.Errorf("GTFS feed HEAD failed: HTTP %d", resp.StatusCode)
	}
	return head, nil
}

// HeadFeed is the compatibility form using the bounded default HTTP client.
func HeadFeed(ctx context.Context, sourceURL string) (FeedHead, error) {
	return headFeed(ctx, nil, sourceURL)
}

func failedCheckBackoff(failures int) time.Duration {
	if failures <= 1 {
		return failedBackoffInitial
	}
	wait := failedBackoffInitial
	for i := 1; i < failures && wait < failedBackoffMaximum; i++ {
		wait *= 2
		if wait >= failedBackoffMaximum {
			return failedBackoffMaximum
		}
	}
	return wait
}

func validServiceDate(value string) bool {
	_, err := time.Parse("20060102", value)
	return err == nil
}

func datasetHasFreshnessEvidence(dataset DatasetState) bool {
	return dataset.GenerationID != "" || dataset.Provenance.SourceURL != "" || !dataset.IngestedAt.IsZero() || dataset.Coverage.Start != "" || dataset.Coverage.End != ""
}

func conciseFreshnessError(err error, sourceURL string) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "request canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "request timed out"
	}
	message := strings.TrimSpace(sanitizeSourceError(err, sourceURL).Error())
	if len(message) > 300 {
		message = message[:300]
	}
	return message
}

func joinFreshnessReason(left, right string) string {
	if left == "" {
		return right
	}
	if right == "" {
		return left
	}
	return left + "; " + right
}

// RecordFeedProvenance retains truthful legacy metadata without attempting to
// mutate source-keyed check state inside a generation.
func RecordFeedProvenance(store *Store, download DownloadResult) {
	if store == nil {
		return
	}
	_ = store.SetMeta(metaFeedSourceURL, download.SourceURL)
	_ = store.SetMeta(metaFeedETag, download.ETag)
	_ = store.SetMeta(metaFeedModified, download.LastModified)
	_ = store.SetMeta(metaFeedSize, strconv.FormatInt(download.ActualBytes, 10))
}

// Freshness is the current-major compatibility wrapper. New integrations
// should call CheckFreshness with DatasetState and an explicit requested time.
func Freshness(ctx context.Context, store *Store, sourceURL string, allowNetwork, force bool) FreshnessReport {
	if store == nil {
		return FreshnessReport{State: FreshnessUnknown, Reason: "local GTFS store is unavailable", SourceURL: RedactSourceURL(sourceURL), StaleAfterDays: StaleAfter().Hours() / 24}
	}
	dataset, err := store.DatasetState(ctx)
	if err != nil {
		dataset = legacyFreshnessDataset(store)
	}
	dataDir := filepath.Dir(store.Path())
	if filepath.Base(dataDir) == generationsDirname {
		dataDir = filepath.Dir(dataDir)
	}
	report, checkErr := CheckFreshness(ctx, FreshnessRequest{
		DataDir:      dataDir,
		Dataset:      dataset,
		RequestedAt:  time.Now(),
		SourceURL:    sourceURL,
		AllowNetwork: allowNetwork,
		Force:        force,
	})
	if checkErr != nil && report.SourceURL == "" {
		now := time.Now().UTC()
		evidence := evaluateLocalFreshness(dataset, now, now)
		state := FreshnessCheckState{
			SourceURL:           sourceURL,
			RemoteContentLength: -1,
			Result:              FreshnessUnknown,
			CheckError:          conciseFreshnessError(checkErr, sourceURL),
			UpdatedAt:           now,
		}
		applyDatasetToFreshnessState(&state, dataset, sourceURL)
		report = reportFromFreshnessState(state, dataset, evidence, state.CheckError, false, "")
	}
	if checkErr != nil && report.CheckError == "" {
		report.State = FreshnessUnknown
		report.CheckError = conciseFreshnessError(checkErr, sourceURL)
		report.Reason = report.CheckError
	}
	return report
}

func legacyFreshnessDataset(store *Store) DatasetState {
	var dataset DatasetState
	dataset.GenerationID = "legacy"
	dataset.Provenance.SourceURL, _ = store.Meta(metaFeedSourceURL)
	dataset.Provenance.ETag, _ = store.Meta(metaFeedETag)
	dataset.Provenance.LastModified, _ = store.Meta(metaFeedModified)
	if raw, _ := store.Meta(metaFeedSize); raw != "" {
		dataset.Provenance.ActualBytes, _ = strconv.ParseInt(raw, 10, 64)
	}
	if raw, _ := store.Meta(metaIngestedAt); raw != "" {
		dataset.IngestedAt, _ = time.Parse(time.RFC3339, raw)
	}
	dataset.Coverage.Start, _ = store.Meta(metaCoverageStart)
	dataset.Coverage.End, _ = store.Meta(metaCoverageEnd)
	if dataset.IngestedAt.IsZero() && dataset.Provenance.SourceURL == "" && dataset.Coverage.Start == "" && dataset.Coverage.End == "" {
		return DatasetState{}
	}
	return dataset
}
