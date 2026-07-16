package geocode

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

func TestNewWithOptionsValidatesEndpoint(t *testing.T) {
	if _, err := NewWithOptions(Options{Endpoint: "http://example.com/search"}); err == nil {
		t.Fatal("NewWithOptions accepted remote HTTP endpoint")
	}
	if _, err := NewWithOptions(Options{Endpoint: "http://127.0.0.1:8080/search"}); err != nil {
		t.Fatalf("NewWithOptions rejected localhost HTTP: %v", err)
	}
}

func TestLookupDisclosesOnlyBeforeNetworkAndReturnsAttribution(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`[{"display_name":"Place","lat":"-37.8","lon":"144.9"}]`))
	}))
	defer srv.Close()

	disclosures := 0
	g, err := NewWithOptions(Options{
		Endpoint:      srv.URL,
		Provider:      "Test provider",
		Attribution:   "Test attribution",
		CacheDir:      t.TempDir(),
		MinInterval:   -1,
		BeforeRequest: func(string) { disclosures++ },
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := g.Lookup(context.Background(), "Place")
	if err != nil {
		t.Fatal(err)
	}
	if p.Provider != "Test provider" || p.Attribution != "Test attribution" {
		t.Fatalf("provider metadata = %#v", p)
	}
	if _, err := g.Lookup(context.Background(), "Place"); err != nil {
		t.Fatal(err)
	}
	if disclosures != 1 || requests.Load() != 1 {
		t.Fatalf("disclosures=%d requests=%d, want one network disclosure", disclosures, requests.Load())
	}
}

func TestLookupCancellationWhileRateLimited(t *testing.T) {
	cacheDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cacheDir, "rate-limit.timestamp"), []byte(time.Now().UTC().Format(time.RFC3339Nano)), 0o600); err != nil {
		t.Fatal(err)
	}
	g, err := NewWithOptions(Options{
		Endpoint:    "http://127.0.0.1:1/search",
		CacheDir:    cacheDir,
		MinInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.Lookup(ctx, "cancel me"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Lookup error = %v, want context.Canceled", err)
	}
}

func TestCacheExpiryAndProviderScopedKey(t *testing.T) {
	dir := t.TempDir()
	g1, err := NewWithOptions(Options{Endpoint: "http://127.0.0.1:8080/search", Provider: "one", CacheDir: dir, CacheTTL: time.Second, MinInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	g2, err := NewWithOptions(Options{Endpoint: "http://127.0.0.1:8080/search", Provider: "two", CacheDir: dir, CacheTTL: time.Second, MinInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	if g1.cacheFile("same") == g2.cacheFile("same") {
		t.Fatal("cache key did not include provider")
	}
	p := &Place{DisplayName: "old", Lat: -37.8, Lon: 144.9}
	if err := g1.writeCache("same", p, time.Now().Add(-2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, ok := g1.readCache("same", time.Now()); ok {
		t.Fatal("expired cache entry was accepted")
	}
}

func TestLookupRejectsOversizedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), int(maxResponseBytes)+1))
	}))
	defer srv.Close()
	g, err := NewWithOptions(Options{Endpoint: srv.URL, CacheDir: t.TempDir(), MinInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Lookup(context.Background(), "large"); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Lookup oversized response error = %v", err)
	}
}

func TestConcurrentRequestsAreSerialized(t *testing.T) {
	var mu sync.Mutex
	var starts []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()
		_, _ = w.Write([]byte(`[{"display_name":"Place","lat":"-37.8","lon":"144.9"}]`))
	}))
	defer srv.Close()
	g, err := NewWithOptions(Options{Endpoint: srv.URL, CacheDir: t.TempDir(), MinInterval: 30 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	for _, query := range []string{"one", "two"} {
		wg.Add(1)
		go func(query string) {
			defer wg.Done()
			_, err := g.Lookup(context.Background(), query)
			errCh <- err
		}(query)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(starts) != 2 || starts[1].Sub(starts[0]) < 20*time.Millisecond {
		t.Fatalf("request starts = %v, want serialized interval", starts)
	}
}

func TestRateLimitIsCoordinatedAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	barrier := filepath.Join(dir, "start")
	type child struct {
		command *exec.Cmd
		ready   string
		output  string
		log     *bytes.Buffer
	}
	children := make([]child, 2)
	for i := range children {
		children[i] = child{
			command: exec.Command(os.Args[0], "-test.run=^TestGeocoderThrottleProcessHelper$"),
			ready:   filepath.Join(dir, fmt.Sprintf("ready-%d", i)),
			output:  filepath.Join(dir, fmt.Sprintf("output-%d", i)),
			log:     &bytes.Buffer{},
		}
		children[i].command.Stdout = children[i].log
		children[i].command.Stderr = children[i].log
		children[i].command.Env = append(os.Environ(),
			"PTV_GEOCODER_THROTTLE_HELPER=1",
			"PTV_GEOCODER_THROTTLE_CACHE="+dir,
			"PTV_GEOCODER_THROTTLE_BARRIER="+barrier,
			"PTV_GEOCODER_THROTTLE_READY="+children[i].ready,
			"PTV_GEOCODER_THROTTLE_OUTPUT="+children[i].output,
		)
		if err := children[i].command.Start(); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		ready := true
		for _, child := range children {
			if _, err := os.Stat(child.ready); err != nil {
				ready = false
				break
			}
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for geocoder throttle helpers")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := os.WriteFile(barrier, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, child := range children {
		if err := child.command.Wait(); err != nil {
			t.Fatalf("throttle helper: %v\n%s", err, child.log.String())
		}
	}
	starts := make([]time.Time, 0, len(children))
	for _, child := range children {
		raw, err := os.ReadFile(child.output)
		if err != nil {
			t.Fatal(err)
		}
		when, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(raw)))
		if err != nil {
			t.Fatal(err)
		}
		starts = append(starts, when)
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })
	if gap := starts[1].Sub(starts[0]); gap < 200*time.Millisecond {
		t.Fatalf("cross-process request starts were only %v apart, want at least 200ms", gap)
	}
}

func TestGeocoderThrottleProcessHelper(t *testing.T) {
	if os.Getenv("PTV_GEOCODER_THROTTLE_HELPER") != "1" {
		return
	}
	ready := os.Getenv("PTV_GEOCODER_THROTTLE_READY")
	barrier := os.Getenv("PTV_GEOCODER_THROTTLE_BARRIER")
	output := os.Getenv("PTV_GEOCODER_THROTTLE_OUTPUT")
	if err := os.WriteFile(ready, []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(barrier); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for parent barrier")
		}
		time.Sleep(5 * time.Millisecond)
	}
	g, err := NewWithOptions(Options{
		Endpoint:    "http://127.0.0.1:1/search",
		CacheDir:    os.Getenv("PTV_GEOCODER_THROTTLE_CACHE"),
		MinInterval: 250 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.throttle(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, []byte(time.Now().UTC().Format(time.RFC3339Nano)), 0o600); err != nil {
		t.Fatal(err)
	}
}
