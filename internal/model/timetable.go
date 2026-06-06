// Package model holds shared domain types for the timetable and journey
// planner, kept separate to avoid import cycles between gtfs and router.
package model

import "time"

// Stop is a planning node.
type Stop struct {
	Index int     `json:"index"`
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	Lat   float64 `json:"lat"`
	Lon   float64 `json:"lon"`
	Mode  int     `json:"mode"` // PTV GTFS feed mode, -1 if unknown
}

// Connection is a single elementary trip segment between two consecutive
// stops on a trip. Times are absolute unix seconds for the query day.
type Connection struct {
	DepStop  int
	ArrStop  int
	DepTime  int64
	ArrTime  int64
	TripID   string
	RouteIdx int
	BlockID  string
}

// Footpath is a walking transfer between two stops.
type Footpath struct {
	ToStop  int
	Seconds int
}

// RouteInfo describes a GTFS route for journey labelling.
type RouteInfo struct {
	ShortName string
	LongName  string
	RouteType int
}

// Timetable is the in-memory dataset the planner scans for one query day.
type Timetable struct {
	// Connections sorted ascending by departure time.
	Connections []Connection
	// Footpaths indexed by stop index.
	Footpaths [][]Footpath
	// Stops indexed by stop index.
	Stops []Stop
	// Routes indexed by route index.
	Routes []RouteInfo
	// TripRoute maps a trip id to its route index.
	TripRoute map[string]int
	// TripHeadsign maps a trip id to its headsign.
	TripHeadsign map[string]string
	// TripBlock maps a trip id to its block id (shared by consecutive
	// journeys of the same physical vehicle), empty if unavailable.
	TripBlock map[string]string
	// ByName maps lower-cased stop name to stop indexes (for resolution).
	NameIndex map[string][]int
	// Day is midnight (local) of the query day.
	Day time.Time
}

// Leg is one stage of a planned journey.
type Leg struct {
	// Walk indicates a walking transfer leg.
	Walk     bool      `json:"walk"`
	FromStop Stop      `json:"from"`
	ToStop   Stop      `json:"to"`
	DepTime  time.Time `json:"depart"`
	ArrTime  time.Time `json:"arrive"`
	// Transit-only fields:
	RouteShortName string `json:"route_short_name,omitempty"`
	RouteLongName  string `json:"route_long_name,omitempty"`
	RouteType      int    `json:"mode,omitempty"`
	Headsign       string `json:"headsign,omitempty"`
	TripID         string `json:"trip_id,omitempty"`
	BlockID        string `json:"block_id,omitempty"`
	// Disruption annotations (populated when real-time disruptions are
	// overlaid onto the journey).
	Disrupted     bool    `json:"disrupted,omitempty"`
	DisruptionIDs []int64 `json:"disruption_ids,omitempty"`
}

// DisruptionNote summarises a disruption affecting a planned journey.
type DisruptionNote struct {
	ID     int64  `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status,omitempty"`
	Type   string `json:"type,omitempty"`
	URL    string `json:"url,omitempty"`
}

// BuildStopModes populates each stop's Mode field by examining the routes
// that serve it through the connection graph. A stop's mode is the "most
// major" route type (lowest feed_mode value) among routes that visit it,
// so Train (2) beats Tram (3) beats Bus (4), and V/Line (1) beats all.
func (tt *Timetable) BuildStopModes() {
	initial := make([]int, len(tt.Stops))
	for i := range tt.Stops {
		initial[i] = tt.Stops[i].Mode
	}
	for _, c := range tt.Connections {
		if c.RouteIdx >= 0 && c.RouteIdx < len(tt.Routes) {
			mode := tt.Routes[c.RouteIdx].RouteType
			if mode > 0 {
				if tt.Stops[c.DepStop].Mode <= 0 || mode < tt.Stops[c.DepStop].Mode {
					tt.Stops[c.DepStop].Mode = mode
				}
				if tt.Stops[c.ArrStop].Mode <= 0 || mode < tt.Stops[c.ArrStop].Mode {
					tt.Stops[c.ArrStop].Mode = mode
				}
			}
		}
	}
	// Preserve feed-mode prefix for stops without any route association.
	for i := range tt.Stops {
		if tt.Stops[i].Mode < 0 {
			tt.Stops[i].Mode = initial[i]
		}
	}
}

// Journey is a planned itinerary.
type Journey struct {
	Legs        []Leg            `json:"legs"`
	DepTime     time.Time        `json:"depart"`
	ArrTime     time.Time        `json:"arrive"`
	Transfers   int              `json:"transfers"`
	Disruptions []DisruptionNote `json:"disruptions,omitempty"`
}
