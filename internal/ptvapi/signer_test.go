package ptvapi

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSign(t *testing.T) {
	// Verified against an independent HMAC-SHA1 implementation.
	got := sign("testkey", "/v3/routes?devid=1")
	want := "F25D107119F54104AB5AC7D74068C579FFD415B3"
	if got != want {
		t.Fatalf("sign() = %q, want %q", got, want)
	}
}

func TestFormatFareTimeUsesSwaggerUTCFormat(t *testing.T) {
	tm := time.Date(2026, 5, 31, 16, 53, 44, 0, time.FixedZone("AEST", 10*60*60))
	if got := formatFareTime(tm); got != "2026-5-31 06:53" {
		t.Fatalf("formatFareTime() = %q, want %q", got, "2026-5-31 06:53")
	}
}

func TestBuildSignedURL(t *testing.T) {
	q := url.Values{}
	q.Set("route_types", "0")
	got := buildSignedURL("https://example.com/", "testkey", "1", "/v3/routes", q)

	if !strings.HasPrefix(got, "https://example.com/v3/routes?") {
		t.Fatalf("unexpected prefix: %s", got)
	}
	if !strings.Contains(got, "devid=1") {
		t.Errorf("missing devid: %s", got)
	}
	if !strings.Contains(got, "route_types=0") {
		t.Errorf("missing route_types: %s", got)
	}

	// The signature must be the HMAC of the path+query that precedes it.
	idx := strings.Index(got, "&signature=")
	if idx < 0 {
		t.Fatalf("missing signature: %s", got)
	}
	pathAndQuery := strings.TrimPrefix(got[:idx], "https://example.com")
	wantSig := sign("testkey", pathAndQuery)
	if got[idx+len("&signature="):] != wantSig {
		t.Errorf("signature mismatch for %q", pathAndQuery)
	}
}
