// Package geocode resolves free-text place and address queries to coordinates
// using the OpenStreetMap Nominatim service. Results are biased to Victoria,
// Australia, cached on disk, and rate-limited per the Nominatim usage policy.
package geocode

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DefaultEndpoint is the public Nominatim search endpoint.
const DefaultEndpoint = "https://nominatim.openstreetmap.org/search"

// userAgent identifies this client to Nominatim, as required by its usage
// policy (https://operations.osmfoundation.org/policies/nominatim/).
const userAgent = "ptv_cli/1.0 (+https://github.com/thesammykins/ptv_cli)"

// Victorian bounding box used to bias and bound results.
// viewbox = west,north,east,south (lon,lat,lon,lat).
const vicViewbox = "140.9,-33.9,150.0,-39.2"

// Place is a geocoded location.
type Place struct {
	DisplayName string  `json:"display_name"`
	Lat         float64 `json:"lat"`
	Lon         float64 `json:"lon"`
}

// Geocoder resolves queries to places via Nominatim, with caching and
// throttling.
type Geocoder struct {
	Endpoint string
	HTTP     *http.Client
	CacheDir string

	minInterval time.Duration
	lastReq     time.Time
}

// New returns a Geocoder caching responses under cacheDir.
func New(cacheDir string) *Geocoder {
	return &Geocoder{
		Endpoint:    DefaultEndpoint,
		HTTP:        &http.Client{Timeout: 15 * time.Second},
		CacheDir:    filepath.Join(cacheDir, "geocode"),
		minInterval: time.Second,
	}
}

// nominatimResult mirrors the subset of the Nominatim JSON we consume.
type nominatimResult struct {
	DisplayName string `json:"display_name"`
	Lat         string `json:"lat"`
	Lon         string `json:"lon"`
}

// Lookup resolves a query to the best-matching Victorian place. It returns an
// error if nothing is found.
func (g *Geocoder) Lookup(ctx context.Context, query string) (*Place, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("empty location query")
	}

	if p, ok := g.readCache(query); ok {
		return p, nil
	}

	reqURL := g.buildURL(query)
	g.throttle()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
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
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("geocoding service returned %s", resp.Status)
	}

	var results []nominatimResult
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
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
	p := &Place{DisplayName: results[0].DisplayName, Lat: lat, Lon: lon}
	g.writeCache(query, p)
	return p, nil
}

// buildURL constructs the Nominatim search URL biased to Victoria.
func (g *Geocoder) buildURL(query string) string {
	q := url.Values{}
	q.Set("q", query)
	q.Set("format", "jsonv2")
	q.Set("limit", "1")
	q.Set("countrycodes", "au")
	q.Set("viewbox", vicViewbox)
	q.Set("bounded", "1")
	q.Set("addressdetails", "0")
	endpoint := g.Endpoint
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	return endpoint + "?" + q.Encode()
}

// throttle enforces the minimum interval between outbound requests.
func (g *Geocoder) throttle() {
	if g.minInterval <= 0 {
		return
	}
	if !g.lastReq.IsZero() {
		if wait := g.minInterval - time.Since(g.lastReq); wait > 0 {
			time.Sleep(wait)
		}
	}
	g.lastReq = time.Now()
}

// cacheFile returns the on-disk cache path for a query.
func (g *Geocoder) cacheFile(query string) string {
	sum := sha1.Sum([]byte(strings.ToLower(query)))
	return filepath.Join(g.CacheDir, hex.EncodeToString(sum[:])+".json")
}

// readCache returns a cached place for the query, if present and valid.
func (g *Geocoder) readCache(query string) (*Place, bool) {
	if g.CacheDir == "" {
		return nil, false
	}
	data, err := os.ReadFile(g.cacheFile(query))
	if err != nil {
		return nil, false
	}
	var p Place
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, false
	}
	return &p, true
}

// writeCache stores a resolved place for the query (best-effort).
func (g *Geocoder) writeCache(query string, p *Place) {
	if g.CacheDir == "" {
		return
	}
	if err := os.MkdirAll(g.CacheDir, 0o755); err != nil {
		return
	}
	data, err := json.Marshal(p)
	if err != nil {
		return
	}
	_ = os.WriteFile(g.cacheFile(query), data, 0o644)
}
