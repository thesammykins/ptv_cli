package geocode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestBuildURL(t *testing.T) {
	g := New(t.TempDir())
	raw := g.buildURL("Federation Square")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	q := u.Query()
	if q.Get("q") != "Federation Square" {
		t.Errorf("q = %q", q.Get("q"))
	}
	if q.Get("countrycodes") != "au" {
		t.Errorf("countrycodes = %q, want au", q.Get("countrycodes"))
	}
	if q.Get("bounded") != "1" || q.Get("viewbox") == "" {
		t.Errorf("expected bounded viewbox, got bounded=%q viewbox=%q", q.Get("bounded"), q.Get("viewbox"))
	}
	if q.Get("limit") != "1" {
		t.Errorf("limit = %q, want 1", q.Get("limit"))
	}
}

func TestLookupAndCache(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if ua := r.Header.Get("User-Agent"); !strings.Contains(ua, "ptv_cli") {
			t.Errorf("missing User-Agent, got %q", ua)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"display_name":"Federation Square, Melbourne VIC, Australia","lat":"-37.8179","lon":"144.9690"}]`))
	}))
	defer srv.Close()

	g := New(t.TempDir())
	g.Endpoint = srv.URL
	g.minInterval = 0

	p, err := g.Lookup(context.Background(), "Federation Square")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if p.Lat != -37.8179 || p.Lon != 144.9690 {
		t.Errorf("coords = %f,%f", p.Lat, p.Lon)
	}
	if !strings.Contains(p.DisplayName, "Federation Square") {
		t.Errorf("display name = %q", p.DisplayName)
	}

	// Second lookup must be served from cache (no extra HTTP call).
	if _, err := g.Lookup(context.Background(), "Federation Square"); err != nil {
		t.Fatalf("cached Lookup: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 HTTP call (cache hit on second), got %d", calls)
	}
}

func TestLookupNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	g := New(t.TempDir())
	g.Endpoint = srv.URL
	g.minInterval = 0
	if _, err := g.Lookup(context.Background(), "nowhere-xyz"); err == nil {
		t.Error("expected error for empty results")
	}
}
