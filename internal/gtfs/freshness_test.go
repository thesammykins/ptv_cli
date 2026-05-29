package gtfs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

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
		{"no provenance", "", "", FeedHead{ETag: `"x"`}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := feedDiffers(c.localETag, c.localModified, c.remote); got != c.want {
				t.Errorf("feedDiffers = %v, want %v", got, c.want)
			}
		})
	}
}

func TestStaleAfterEnvOverride(t *testing.T) {
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

func TestHeadFeed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("expected HEAD, got %s", r.Method)
		}
		w.Header().Set("ETag", `"abc123"`)
		w.Header().Set("Last-Modified", "Fri, 22 May 2026 00:25:40 GMT")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	head, err := HeadFeed(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("HeadFeed: %v", err)
	}
	if head.ETag != `"abc123"` {
		t.Errorf("ETag = %q", head.ETag)
	}
	if head.LastModified != "Fri, 22 May 2026 00:25:40 GMT" {
		t.Errorf("LastModified = %q", head.LastModified)
	}
}

func TestHeadFeedNonSuccessStatusIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := HeadFeed(context.Background(), srv.URL); err == nil {
		t.Fatal("HeadFeed succeeded for HTTP 500")
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestFreshnessNoData(t *testing.T) {
	store := newTestStore(t)
	r := Freshness(context.Background(), store, "http://example.invalid", false, false)
	if r.HasData {
		t.Errorf("expected HasData=false for empty store")
	}
}

func TestFreshnessStaleAndUpdateAvailable(t *testing.T) {
	store := newTestStore(t)
	// Backdate ingest well beyond the threshold and record an old etag.
	old := time.Now().Add(-30 * 24 * time.Hour).UTC().Format(time.RFC3339)
	if err := store.SetMeta(metaIngestedAt, old); err != nil {
		t.Fatal(err)
	}
	if err := store.SetMeta(metaFeedETag, `"old"`); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"new"`)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	r := Freshness(context.Background(), store, srv.URL, true, true)
	if !r.HasData {
		t.Fatal("expected HasData=true")
	}
	if !r.Stale {
		t.Errorf("expected Stale=true for 30-day-old data")
	}
	if !r.UpdateAvailable {
		t.Errorf("expected UpdateAvailable=true when etags differ")
	}
	// The check result must be cached in meta.
	if v, _ := store.Meta(metaUpdateAvail); v != "true" {
		t.Errorf("update_available not cached, got %q", v)
	}
	if v, _ := store.Meta(metaRemoteETag); v != `"new"` {
		t.Errorf("remote etag not cached, got %q", v)
	}
}

func TestFreshnessThrottleUsesCache(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC().Format(time.RFC3339)
	_ = store.SetMeta(metaIngestedAt, now)
	_ = store.SetMeta(metaFeedETag, `"local"`)
	// A recent check says an update is available; a fresh (throttled) call must
	// not hit the network and must surface the cached value.
	_ = store.SetMeta(metaCheckAt, now)
	_ = store.SetMeta(metaUpdateAvail, "true")
	_ = store.SetMeta(metaRemoteETag, `"remote"`)

	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	r := Freshness(context.Background(), store, srv.URL, true, false)
	if hits != 0 {
		t.Errorf("expected no network hit within throttle window, got %d", hits)
	}
	if !r.UpdateAvailable {
		t.Errorf("expected cached UpdateAvailable=true")
	}
}
