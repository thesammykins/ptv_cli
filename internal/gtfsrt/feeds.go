package gtfsrt

import "strings"

// FeedKind identifies the GTFS Realtime entity carried by a feed.
type FeedKind string

const (
	FeedKindTripUpdates      FeedKind = "trip_updates"
	FeedKindVehiclePositions FeedKind = "vehicle_positions"
	FeedKindServiceAlerts    FeedKind = "service_alerts"
)

// FeedAuthentication describes the documented request authentication for a
// catalog feed. Header names are contract data, not retry candidates.
type FeedAuthentication struct {
	Header   string `json:"header"`
	Required bool   `json:"required"`
}

// KeyIDHeader is the currently documented Transport Victoria Open Data
// subscription header.
const KeyIDHeader = "KeyID"

// Feed describes a Transport Victoria GTFS Realtime feed endpoint.
type Feed struct {
	ID             string             `json:"id"`
	Mode           string             `json:"mode"`
	Kind           FeedKind           `json:"kind"`
	Title          string             `json:"title"`
	URL            string             `json:"url"`
	Authentication FeedAuthentication `json:"authentication"`
	Description    string             `json:"description,omitempty"`
}

// Feeds returns the known Transport Victoria GTFS Realtime feed catalog.
func Feeds() []Feed {
	feeds := []Feed{
		{
			ID:          "metro-trip-updates",
			Mode:        "train",
			Kind:        FeedKindTripUpdates,
			Title:       "Metro Train trip updates",
			URL:         "https://api.opendata.transport.vic.gov.au/opendata/public-transport/gtfs/realtime/v1/metro/trip-updates",
			Description: "Real-time arrival, departure and delay information for metropolitan train trips.",
		},
		{
			ID:          "metro-vehicle-positions",
			Mode:        "train",
			Kind:        FeedKindVehiclePositions,
			Title:       "Metro Train vehicle positions",
			URL:         "https://api.opendata.transport.vic.gov.au/opendata/public-transport/gtfs/realtime/v1/metro/vehicle-positions",
			Description: "Live location and occupancy information for metropolitan train services.",
		},
		{
			ID:          "metro-service-alerts",
			Mode:        "train",
			Kind:        FeedKindServiceAlerts,
			Title:       "Metro Train service alerts",
			URL:         "https://api.opendata.transport.vic.gov.au/opendata/public-transport/gtfs/realtime/v1/metro/service-alerts",
			Description: "Disruptions affecting metropolitan train stations, routes or the network.",
		},
		{
			ID:          "tram-trip-updates",
			Mode:        "tram",
			Kind:        FeedKindTripUpdates,
			Title:       "Yarra Trams trip updates",
			URL:         "https://api.opendata.transport.vic.gov.au/opendata/public-transport/gtfs/realtime/v1/tram/trip-updates",
			Description: "Real-time arrival, departure and delay information for tram trips.",
		},
		{
			ID:          "tram-vehicle-positions",
			Mode:        "tram",
			Kind:        FeedKindVehiclePositions,
			Title:       "Yarra Trams vehicle positions",
			URL:         "https://api.opendata.transport.vic.gov.au/opendata/public-transport/gtfs/realtime/v1/tram/vehicle-positions",
			Description: "Live location information for tram services.",
		},
		{
			ID:          "tram-service-alerts",
			Mode:        "tram",
			Kind:        FeedKindServiceAlerts,
			Title:       "Yarra Trams service alerts",
			URL:         "https://api.opendata.transport.vic.gov.au/opendata/public-transport/gtfs/realtime/v1/tram/service-alerts",
			Description: "Disruptions affecting tram stops, routes or the network.",
		},
		{
			ID:          "bus-trip-updates",
			Mode:        "bus",
			Kind:        FeedKindTripUpdates,
			Title:       "Metro and regional bus trip updates",
			URL:         "https://api.opendata.transport.vic.gov.au/opendata/public-transport/gtfs/realtime/v1/bus/trip-updates",
			Description: "Real-time arrival, departure and delay information for metro bus trips.",
		},
		{
			ID:          "bus-vehicle-positions",
			Mode:        "bus",
			Kind:        FeedKindVehiclePositions,
			Title:       "Metro and regional bus vehicle positions",
			URL:         "https://api.opendata.transport.vic.gov.au/opendata/public-transport/gtfs/realtime/v1/bus/vehicle-positions",
			Description: "Live location and occupancy information for metro and regional bus services.",
		},
		{
			ID:          "vline-trip-updates",
			Mode:        "vline",
			Kind:        FeedKindTripUpdates,
			Title:       "V/Line train trip updates",
			URL:         "https://api.opendata.transport.vic.gov.au/opendata/public-transport/gtfs/realtime/v1/vline/trip-updates",
			Description: "Real-time arrival, departure and delay information for V/Line train trips.",
		},
		{
			ID:          "vline-vehicle-positions",
			Mode:        "vline",
			Kind:        FeedKindVehiclePositions,
			Title:       "V/Line train vehicle positions",
			URL:         "https://api.opendata.transport.vic.gov.au/opendata/public-transport/gtfs/realtime/v1/vline/vehicle-positions",
			Description: "Live location and occupancy information for V/Line train services.",
		},
	}
	for i := range feeds {
		feeds[i].Authentication = FeedAuthentication{Header: KeyIDHeader, Required: true}
	}
	return feeds
}

// FeedByID returns the known feed with id.
func FeedByID(id string) (Feed, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, feed := range Feeds() {
		if feed.ID == id {
			return feed, true
		}
	}
	return Feed{}, false
}
