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

// Journey is a planned itinerary.
type Journey struct {
	Legs        []Leg            `json:"legs"`
	DepTime     time.Time        `json:"depart"`
	ArrTime     time.Time        `json:"arrive"`
	Transfers   int              `json:"transfers"`
	Disruptions []DisruptionNote `json:"disruptions,omitempty"`
}
