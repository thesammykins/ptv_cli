// Package geocode resolves free-text place and address queries to coordinates
// using a configurable provider. The default OpenStreetMap Nominatim provider
// is Victoria-bounded, persistently cached, attributed, and rate limited.
package geocode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thesammykins/ptv_cli/internal/atomicfile"
)

const (
	// DefaultEndpoint is the public Nominatim search endpoint.
	DefaultEndpoint = "https://nominatim.openstreetmap.org/search"
	// DefaultProvider identifies the public geocoding provider.
	DefaultProvider = "OpenStreetMap Nominatim"
	// DefaultAttribution is required whenever default-provider data is shown.
	DefaultAttribution = "© OpenStreetMap contributors"

	userAgent           = "ptv_cli/1.0 (+https://github.com/thesammykins/ptv_cli)"
	vicViewbox          = "140.9,-33.9,150.0,-39.2"
	cacheSchemaVersion  = 2
	defaultCacheTTL     = 30 * 24 * time.Hour
	maxResponseBytes    = int64(1 << 20)
	limiterPollInterval = 25 * time.Millisecond
	limiterStaleAfter   = 30 * time.Second
)

// Place is a geocoded location with its provider attribution.
type Place struct {
	DisplayName string  `json:"display_name"`
	Lat         float64 `json:"lat"`
	Lon         float64 `json:"lon"`
	Provider    string  `json:"provider,omitempty"`
	Attribution string  `json:"attribution,omitempty"`
}

// Options configures a geocoding provider.
type Options struct {
	Endpoint      string
	Provider      string
	Attribution   string
	HTTP          *http.Client
	CacheDir      string
	CacheTTL      time.Duration
	MinInterval   time.Duration
	BeforeRequest func(query string)
}

// Geocoder resolves queries with provider-aware caching and request limiting.
// Exported fields remain for compatibility; prefer NewWithOptions for new code.
type Geocoder struct {
	Endpoint      string
	Provider      string
	Attribution   string
	HTTP          *http.Client
	CacheDir      string
	CacheTTL      time.Duration
	BeforeRequest func(query string)

	minInterval time.Duration
	mu          sync.Mutex
	lastReq     time.Time
}

// New returns a public Nominatim geocoder caching beneath cacheDir.
func New(cacheDir string) *Geocoder {
	g, err := NewWithOptions(Options{CacheDir: filepath.Join(cacheDir, "geocode")})
	if err != nil {
		panic(err) // defaults are compile-time constants and must remain valid
	}
	return g
}

// NewWithOptions validates and constructs a provider-specific geocoder.
func NewWithOptions(opts Options) (*Geocoder, error) {
	endpoint := firstNonEmpty(opts.Endpoint, DefaultEndpoint)
	if err := validateEndpoint(endpoint); err != nil {
		return nil, err
	}
	provider := firstNonEmpty(opts.Provider, DefaultProvider)
	attribution := opts.Attribution
	if attribution == "" && endpoint == DefaultEndpoint {
		attribution = DefaultAttribution
	}
	httpClient := opts.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	ttl := opts.CacheTTL
	if ttl == 0 {
		ttl = defaultCacheTTL
	}
	interval := opts.MinInterval
	if interval == 0 && endpoint == DefaultEndpoint {
		interval = time.Second
	}
	return &Geocoder{
		Endpoint:      endpoint,
		Provider:      provider,
		Attribution:   attribution,
		HTTP:          httpClient,
		CacheDir:      opts.CacheDir,
		CacheTTL:      ttl,
		BeforeRequest: opts.BeforeRequest,
		minInterval:   interval,
	}, nil
}

type nominatimResult struct {
	DisplayName string `json:"display_name"`
	Lat         string `json:"lat"`
	Lon         string `json:"lon"`
}

type cacheEntry struct {
	Version  int       `json:"version"`
	Provider string    `json:"provider"`
	Endpoint string    `json:"endpoint"`
	Query    string    `json:"query"`
	CachedAt time.Time `json:"cached_at"`
	Place    Place     `json:"place"`
}

// Lookup resolves a query to the best matching Victorian place.
func (g *Geocoder) Lookup(ctx context.Context, query string) (*Place, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("empty location query")
	}
	if err := validateEndpoint(g.endpoint()); err != nil {
		return nil, err
	}
	if p, ok := g.readCache(query, time.Now()); ok {
		return p, nil
	}
	if g.BeforeRequest != nil {
		g.BeforeRequest(query)
	}
	if err := g.throttle(ctx); err != nil {
		return nil, fmt.Errorf("waiting for geocoding rate limit: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.buildURL(query), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := g.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("geocoding request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading geocoding response: %w", err)
	}
	if int64(len(body)) > maxResponseBytes {
		return nil, fmt.Errorf("geocoding response exceeds %d bytes", maxResponseBytes)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("geocoding service returned %s", resp.Status)
	}

	var results []nominatimResult
	if err := json.Unmarshal(body, &results); err != nil {
		return nil, fmt.Errorf("decoding geocoding response: %w", err)
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("no place found for %q in Victoria", query)
	}
	lat, err := strconv.ParseFloat(results[0].Lat, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid latitude from geocoder: %w", err)
	}
	lon, err := strconv.ParseFloat(results[0].Lon, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid longitude from geocoder: %w", err)
	}
	p := &Place{
		DisplayName: results[0].DisplayName,
		Lat:         lat,
		Lon:         lon,
		Provider:    firstNonEmpty(g.Provider, DefaultProvider),
		Attribution: g.Attribution,
	}
	_ = g.writeCache(query, p, time.Now())
	return p, nil
}

func (g *Geocoder) buildURL(query string) string {
	q := url.Values{}
	q.Set("q", query)
	q.Set("format", "jsonv2")
	q.Set("limit", "1")
	q.Set("countrycodes", "au")
	q.Set("viewbox", vicViewbox)
	q.Set("bounded", "1")
	q.Set("addressdetails", "0")
	return g.endpoint() + "?" + q.Encode()
}

func (g *Geocoder) endpoint() string { return firstNonEmpty(g.Endpoint, DefaultEndpoint) }

func validateEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("invalid geocoder endpoint %q", raw)
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" {
		host := u.Hostname()
		if strings.EqualFold(host, "localhost") || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()) {
			return nil
		}
	}
	return fmt.Errorf("geocoder endpoint must use https")
}

// throttle coordinates request starts across goroutines and local processes.
func (g *Geocoder) throttle(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.minInterval <= 0 {
		return nil
	}
	if g.CacheDir == "" {
		if err := waitUntil(ctx, g.lastReq.Add(g.minInterval)); err != nil {
			return err
		}
		g.lastReq = time.Now()
		return nil
	}
	if err := os.MkdirAll(g.CacheDir, 0o700); err != nil {
		return err
	}
	lockPath := filepath.Join(g.CacheDir, "rate-limit.lock")
	statePath := filepath.Join(g.CacheDir, "rate-limit.timestamp")
	for {
		lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_ = lock.Close()
			defer os.Remove(lockPath)
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		if info, statErr := os.Stat(lockPath); statErr == nil && time.Since(info.ModTime()) > limiterStaleAfter {
			_ = os.Remove(lockPath)
			continue
		}
		if err := waitFor(ctx, limiterPollInterval); err != nil {
			return err
		}
	}
	if raw, err := os.ReadFile(statePath); err == nil {
		if previous, parseErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(raw))); parseErr == nil {
			if err := waitUntil(ctx, previous.Add(g.minInterval)); err != nil {
				return err
			}
		}
	}
	now := time.Now().UTC()
	if err := atomicfile.WriteFile(statePath, []byte(now.Format(time.RFC3339Nano)+"\n"), 0o600); err != nil {
		return err
	}
	g.lastReq = now
	return nil
}

func waitUntil(ctx context.Context, at time.Time) error {
	return waitFor(ctx, time.Until(at))
}

func waitFor(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (g *Geocoder) cacheFile(query string) string {
	key := strings.Join([]string{
		strconv.Itoa(cacheSchemaVersion),
		strings.ToLower(firstNonEmpty(g.Provider, DefaultProvider)),
		g.endpoint(),
		strings.ToLower(strings.TrimSpace(query)),
		"au", vicViewbox, "jsonv2",
	}, "\x00")
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(g.CacheDir, hex.EncodeToString(sum[:])+".json")
}

func (g *Geocoder) readCache(query string, now time.Time) (*Place, bool) {
	if g.CacheDir == "" || g.CacheTTL < 0 {
		return nil, false
	}
	data, err := os.ReadFile(g.cacheFile(query))
	if err != nil {
		return nil, false
	}
	var entry cacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, false
	}
	if entry.Version != cacheSchemaVersion || entry.Provider != firstNonEmpty(g.Provider, DefaultProvider) || entry.Endpoint != g.endpoint() || entry.Query != normalizeQuery(query) {
		return nil, false
	}
	if g.CacheTTL > 0 && now.Sub(entry.CachedAt) > g.CacheTTL {
		return nil, false
	}
	return &entry.Place, true
}

func (g *Geocoder) writeCache(query string, place *Place, now time.Time) error {
	if g.CacheDir == "" || g.CacheTTL < 0 {
		return nil
	}
	if err := os.MkdirAll(g.CacheDir, 0o700); err != nil {
		return err
	}
	entry := cacheEntry{
		Version:  cacheSchemaVersion,
		Provider: firstNonEmpty(g.Provider, DefaultProvider),
		Endpoint: g.endpoint(),
		Query:    normalizeQuery(query),
		CachedAt: now.UTC(),
		Place:    *place,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(g.cacheFile(query), data, 0o600)
}

func normalizeQuery(query string) string {
	return strings.ToLower(strings.Join(strings.Fields(query), " "))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
