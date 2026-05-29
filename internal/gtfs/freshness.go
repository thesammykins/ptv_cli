package gtfs

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"
)

// Meta keys used for feed provenance and update-check throttling.
const (
	metaIngestedAt     = "ingested_at"
	metaFeedETag       = "feed_etag"
	metaFeedModified   = "feed_last_modified"
	metaFeedSize       = "feed_content_length"
	metaCheckAt        = "update_check_at"
	metaUpdateAvail    = "update_available"
	metaRemoteETag     = "update_remote_etag"
	metaRemoteModified = "update_remote_last_modified"
)

// DefaultStaleAfter is how old (since ingest) the local GTFS data may be before
// it is considered stale. PTV publishes the feed roughly weekly, so 7 days is
// the default. Overridable via PTV_GTFS_STALE_DAYS.
const DefaultStaleAfter = 7 * 24 * time.Hour

// updateCheckThrottle bounds how often the live HEAD update-check runs.
const updateCheckThrottle = 24 * time.Hour

// StaleAfter resolves the staleness threshold, honouring PTV_GTFS_STALE_DAYS.
func StaleAfter() time.Duration {
	if v := os.Getenv("PTV_GTFS_STALE_DAYS"); v != "" {
		if days, err := strconv.Atoi(v); err == nil && days > 0 {
			return time.Duration(days) * 24 * time.Hour
		}
	}
	return DefaultStaleAfter
}

// FeedHead carries the upstream provenance headers for the GTFS feed.
type FeedHead struct {
	ETag          string
	LastModified  string
	ContentLength int64
}

// HeadFeed issues a HEAD request to the feed URL and returns its provenance
// headers, used to cheaply detect whether a newer feed has been published.
func HeadFeed(ctx context.Context, url string) (FeedHead, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return FeedHead{}, err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return FeedHead{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return FeedHead{}, fmt.Errorf("GTFS feed HEAD failed: HTTP %d", resp.StatusCode)
	}
	return FeedHead{
		ETag:          resp.Header.Get("ETag"),
		LastModified:  resp.Header.Get("Last-Modified"),
		ContentLength: resp.ContentLength,
	}, nil
}

// FreshnessReport summarises how current the local GTFS data is.
type FreshnessReport struct {
	HasData        bool    `json:"has_data"`
	IngestedAt     string  `json:"ingested_at,omitempty"`
	AgeHours       float64 `json:"age_hours,omitempty"`
	Stale          bool    `json:"stale"`
	StaleAfterDays float64 `json:"stale_after_days"`
	FeedETag       string  `json:"feed_etag,omitempty"`
	FeedModified   string  `json:"feed_last_modified,omitempty"`

	// Live/cached upstream comparison.
	Checked         bool   `json:"checked"`
	CheckedAt       string `json:"checked_at,omitempty"`
	UpdateAvailable bool   `json:"update_available"`
	RemoteETag      string `json:"remote_etag,omitempty"`
	RemoteModified  string `json:"remote_last_modified,omitempty"`
	CheckError      string `json:"check_error,omitempty"`
}

// RecordFeedProvenance stores the freshly-downloaded feed's headers and resets
// the update-check cache (we just fetched the newest feed). Best-effort.
func RecordFeedProvenance(store *Store, dl DownloadResult) {
	_ = store.SetMeta(metaFeedETag, dl.ETag)
	_ = store.SetMeta(metaFeedModified, dl.LastModified)
	_ = store.SetMeta(metaFeedSize, strconv.FormatInt(dl.ContentLength, 10))
	_ = store.SetMeta(metaCheckAt, time.Now().UTC().Format(time.RFC3339))
	_ = store.SetMeta(metaUpdateAvail, "false")
	_ = store.SetMeta(metaRemoteETag, dl.ETag)
	_ = store.SetMeta(metaRemoteModified, dl.LastModified)
}

// Freshness computes a FreshnessReport. When allowNetwork is true it performs a
// live HEAD comparison if force is set or the cached check is older than 24h,
// persisting the result; otherwise it reports the cached update state.
func Freshness(ctx context.Context, store *Store, url string, allowNetwork, force bool) FreshnessReport {
	r := FreshnessReport{StaleAfterDays: StaleAfter().Hours() / 24}

	ingestedAt, _ := store.Meta(metaIngestedAt)
	if ingestedAt == "" {
		return r // no data ingested yet
	}
	r.HasData = true
	r.IngestedAt = ingestedAt
	r.FeedETag, _ = store.Meta(metaFeedETag)
	r.FeedModified, _ = store.Meta(metaFeedModified)

	if t, err := time.Parse(time.RFC3339, ingestedAt); err == nil {
		age := time.Since(t)
		r.AgeHours = age.Hours()
		r.Stale = age > StaleAfter()
	}

	// Seed from cached check state.
	if v, _ := store.Meta(metaUpdateAvail); v != "" {
		r.UpdateAvailable = v == "true"
		r.RemoteETag, _ = store.Meta(metaRemoteETag)
		r.RemoteModified, _ = store.Meta(metaRemoteModified)
		if at, _ := store.Meta(metaCheckAt); at != "" {
			r.Checked = true
			r.CheckedAt = at
		}
	}

	if !allowNetwork {
		return r
	}

	// Throttle: only re-check live if forced or the cache is stale.
	if !force {
		if at, _ := store.Meta(metaCheckAt); at != "" {
			if t, err := time.Parse(time.RFC3339, at); err == nil && time.Since(t) < updateCheckThrottle {
				return r
			}
		}
	}

	head, err := HeadFeed(ctx, url)
	if err != nil {
		r.CheckError = err.Error()
		return r
	}
	r.Checked = true
	r.CheckedAt = time.Now().UTC().Format(time.RFC3339)
	r.RemoteETag = head.ETag
	r.RemoteModified = head.LastModified
	r.UpdateAvailable = feedDiffers(r.FeedETag, r.FeedModified, head)

	_ = store.SetMeta(metaCheckAt, r.CheckedAt)
	_ = store.SetMeta(metaUpdateAvail, strconv.FormatBool(r.UpdateAvailable))
	_ = store.SetMeta(metaRemoteETag, head.ETag)
	_ = store.SetMeta(metaRemoteModified, head.LastModified)
	return r
}

// feedDiffers reports whether the upstream feed looks newer than what we
// ingested, preferring the ETag and falling back to Last-Modified.
func feedDiffers(localETag, localModified string, remote FeedHead) bool {
	if localETag != "" && remote.ETag != "" {
		return localETag != remote.ETag
	}
	if localModified != "" && remote.LastModified != "" {
		return localModified != remote.LastModified
	}
	// Without provenance we can't tell; assume up-to-date to avoid false alarms.
	return false
}
