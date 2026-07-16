// Package model holds shared domain types for the timetable and journey
// planner, kept separate to avoid import cycles between gtfs and router.
package model

import "time"

// TripInstanceID is a dense, query-local identifier for one occurrence of a
// GTFS trip on one service day. Zero is reserved for legacy/unknown instances;
// populated timetables allocate IDs starting at one.
type TripInstanceID uint32

const UnknownTripInstanceID TripInstanceID = 0

// ServiceInstance identifies one scheduled occurrence of a trip. TripID alone
// is not sufficient because the same GTFS trip may be active on consecutive
// service days, including after-midnight overlap.
type ServiceInstance struct {
	ID          TripInstanceID
	FeedMode    int
	ServiceDate time.Time
	TripID      string
	RouteIdx    int
	Headsign    string
	BlockID     string
}

// PassengerActionPolicy mirrors GTFS pickup_type/drop_off_type values.
type PassengerActionPolicy uint8

const (
	PassengerActionRegular          PassengerActionPolicy = 0
	PassengerActionForbidden        PassengerActionPolicy = 1
	PassengerActionPhoneAgency      PassengerActionPolicy = 2
	PassengerActionCoordinateDriver PassengerActionPolicy = 3
)

// Conditional reports whether the action requires passenger coordination.
func (p PassengerActionPolicy) Conditional() bool {
	return p == PassengerActionPhoneAgency || p == PassengerActionCoordinateDriver
}

// Allowed reports whether the action may be used by a query. Conditional
// pickup/drop-off is excluded unless the caller explicitly opts in.
func (p PassengerActionPolicy) Allowed(allowConditional bool) bool {
	switch p {
	case PassengerActionRegular:
		return true
	case PassengerActionPhoneAgency, PassengerActionCoordinateDriver:
		return allowConditional
	default:
		return false
	}
}

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
	DepStop        int
	ArrStop        int
	DepTime        int64
	ArrTime        int64
	TripID         string
	TripInstanceID TripInstanceID
	RouteIdx       int
	BlockID        string
	PickupPolicy   PassengerActionPolicy
	DropOffPolicy  PassengerActionPolicy
}

// WalkEdgeKind records where a physical walking edge came from. Contextual
// transfer restrictions are represented separately by TransferRule.
type WalkEdgeKind uint8

const (
	WalkEdgeUnspecified WalkEdgeKind = iota
	WalkEdgeProximity
	WalkEdgePathway
	WalkEdgeExplicitTransfer
)

// WalkEdge is a directed physical walking edge between two stops.
type WalkEdge struct {
	ToStop  int
	Seconds int
	Kind    WalkEdgeKind
}

// Footpath is the legacy name for WalkEdge. Keep it as an alias so existing
// timetable builders continue to compile while migrating to WalkEdges.
type Footpath = WalkEdge

// TransferType mirrors GTFS transfers.txt transfer_type values.
type TransferType uint8

const (
	TransferRecommended TransferType = iota
	TransferTimed
	TransferMinimumTime
	TransferForbidden
	TransferStayOnboard
	TransferNoStayOnboard
)

// TransferRule is a contextual transfer contract for types 0 through 3. Types
// 4 and 5 belong in Continuation. Stop and route indexes use -1 as a wildcard
// and trip-instance IDs use zero as a wildcard. Loaders must set wildcard
// indexes explicitly; the zero value is a real stop/route index.
type TransferRule struct {
	FromStop           int
	ToStop             int
	Type               TransferType
	MinTransferSeconds int
	FromRouteIdx       int
	ToRouteIdx         int
	FromTripInstanceID TripInstanceID
	ToTripInstanceID   TripInstanceID
}

// Continuation records an explicit stay-onboard permission/prohibition between
// active trip instances. FromStop and ToStop are the respective terminal and
// origin stops and may each be -1 as a wildcard. They are deliberately
// distinct: GTFS linked trips may continue at two nearby but non-identical
// stops. Type must be TransferStayOnboard or TransferNoStayOnboard; a matching
// prohibition takes precedence over a permission.
type Continuation struct {
	FromTripInstanceID TripInstanceID
	ToTripInstanceID   TripInstanceID
	FromStop           int
	ToStop             int
	Type               TransferType
}

// Endpoint is a weighted access/egress connection between a user-visible
// location and a timetable stop. Location may be nil when no virtual walking
// leg should be rendered.
type Endpoint struct {
	Stop        int
	WalkSeconds int
	Location    *Stop
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
	// ReverseConnections contains the time-reversed graph sorted by its
	// departure time. When nil, the router builds the legacy compatibility view
	// per latest-departure query.
	ReverseConnections []Connection
	// WalkEdges is the canonical directed physical walking graph. When nil,
	// routers fall back to the legacy Footpaths field.
	WalkEdges [][]WalkEdge
	// ReverseWalkEdges is the precomputed reverse physical walking graph. When
	// nil, the router derives it for a latest-departure query.
	ReverseWalkEdges [][]WalkEdge
	// Footpaths indexed by stop index.
	Footpaths [][]Footpath
	// TransferRules contains contextual transfer constraints, separate from
	// physical walking connectivity.
	TransferRules []TransferRule
	// Continuations contains explicit active-trip stay-onboard contracts.
	Continuations []Continuation
	// Stops indexed by stop index.
	Stops []Stop
	// Routes indexed by route index.
	Routes []RouteInfo
	// TripRoute maps a trip id to its route index.
	TripRoute map[string]int
	// TripHeadsign maps a trip id to its headsign.
	TripHeadsign map[string]string
	// TripBlock retains source block context for display/debugging only. Equal
	// block strings are not routing evidence; only explicit Continuations permit
	// a stay-onboard transition.
	TripBlock map[string]string
	// TripInstances is indexed by TripInstanceID; slot zero is reserved.
	TripInstances []ServiceInstance
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
	RouteShortName string                `json:"route_short_name,omitempty"`
	RouteLongName  string                `json:"route_long_name,omitempty"`
	RouteType      int                   `json:"mode,omitempty"`
	Headsign       string                `json:"headsign,omitempty"`
	TripID         string                `json:"trip_id,omitempty"`
	TripInstanceID TripInstanceID        `json:"-"`
	BlockID        string                `json:"block_id,omitempty"`
	StayOnboard    bool                  `json:"stay_onboard,omitempty"`
	Conditional    bool                  `json:"conditional,omitempty"`
	PickupPolicy   PassengerActionPolicy `json:"pickup_policy,omitempty"`
	DropOffPolicy  PassengerActionPolicy `json:"drop_off_policy,omitempty"`
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
