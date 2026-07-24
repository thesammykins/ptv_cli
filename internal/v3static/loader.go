package v3static

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed data/snapshot.json
var embeddedFS embed.FS

// LoadEmbedded loads the reviewed snapshot shipped in the binary.
func LoadEmbedded() (*Snapshot, error) {
	data, err := embeddedFS.ReadFile("data/snapshot.json")
	if err != nil {
		return nil, fmt.Errorf("read embedded v3 static snapshot: %w", err)
	}
	var snapshot Snapshot
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&snapshot); err != nil {
		return nil, fmt.Errorf("decode embedded v3 static snapshot: %w", err)
	}
	if err := snapshot.Validate(); err != nil {
		return nil, fmt.Errorf("validate embedded v3 static snapshot: %w", err)
	}
	return &snapshot, nil
}

func normalizeQuery(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}

// AllOutlets returns a copy of the bundled outlet records, optionally limited.
func (s *Snapshot) AllOutlets(limit int) []Outlet {
	if s == nil {
		return nil
	}
	items := append([]Outlet(nil), s.Outlets...)
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items
}

// SearchOutlets performs a conservative name/business/suburb search over the
// bundled data. It intentionally does not invent distance values.
func (s *Snapshot) SearchOutlets(term string, limit int) []Outlet {
	if s == nil {
		return nil
	}
	term = normalizeQuery(term)
	if term == "" {
		return s.AllOutlets(limit)
	}
	items := make([]Outlet, 0)
	for _, outlet := range s.Outlets {
		value := normalizeQuery(outlet.OutletName + " " + outlet.OutletBusiness + " " + outlet.OutletSuburb)
		if strings.Contains(value, term) {
			items = append(items, outlet)
			if limit > 0 && len(items) >= limit {
				break
			}
		}
	}
	return items
}

// FindStation returns an exact or unique station-facility record. An empty
// routeTypes slice means no mode restriction; this keeps PTV route_type zero
// unambiguous for train stations.
func (s *Snapshot) FindStation(query string, routeTypes []int) (StationFacility, bool) {
	if s == nil {
		return StationFacility{}, false
	}
	query = normalizeQuery(query)
	var exact StationFacility
	exactCount := 0
	var fuzzy StationFacility
	fuzzyCount := 0
	for _, station := range s.StationFacilities {
		if len(routeTypes) > 0 && !containsInt(routeTypes, station.RouteType) {
			continue
		}
		name := normalizeQuery(station.StopName)
		if name == query || fmt.Sprintf("%d", station.PTVStopID) == strings.TrimSpace(query) {
			exact = station
			exactCount++
			continue
		}
		if query != "" && strings.Contains(name, query) {
			fuzzy = station
			fuzzyCount++
		}
	}
	if exactCount == 1 {
		return exact, true
	}
	return fuzzy, fuzzyCount == 1
}

func containsInt(values []int, wanted int) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
