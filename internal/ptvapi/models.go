package ptvapi

// Status is the API status/metadata block returned with every response.
type Status struct {
	Version string `json:"version"`
	Health  int    `json:"health"`
}

// ErrorResponse is returned for non-200 responses.
type ErrorResponse struct {
	Message string `json:"message"`
	Status  Status `json:"status"`
}

// RouteType identifies a transport mode (0=Train,1=Tram,2=Bus,3=V/Line,4=Night Bus).
type RouteType struct {
	RouteTypeName string `json:"route_type_name"`
	RouteType     int    `json:"route_type"`
}

// RouteTypesResponse wraps the route_types endpoint.
type RouteTypesResponse struct {
	RouteTypes []RouteType `json:"route_types"`
	Status     Status      `json:"status"`
}

// Route is a train line, tram route, bus route, etc.
type Route struct {
	RouteType   int    `json:"route_type"`
	RouteID     int    `json:"route_id"`
	RouteName   string `json:"route_name"`
	RouteNumber string `json:"route_number"`
	RouteGTFSID string `json:"route_gtfs_id"`
}

// RouteResponse wraps the routes endpoints.
type RouteResponse struct {
	Routes []Route `json:"routes"`
	Route  *Route  `json:"route"`
	Status Status  `json:"status"`
}

// StopModel is a stop as returned in departures/search/geolocation responses.
type StopModel struct {
	StopID        int     `json:"stop_id"`
	StopName      string  `json:"stop_name"`
	StopSuburb    string  `json:"stop_suburb"`
	RouteType     int     `json:"route_type"`
	StopLatitude  float64 `json:"stop_latitude"`
	StopLongitude float64 `json:"stop_longitude"`
	StopLandmark  string  `json:"stop_landmark"`
	StopDistance  float64 `json:"stop_distance"`
	StopSequence  int     `json:"stop_sequence"`
}

// Direction is a direction of travel for a route.
type Direction struct {
	DirectionID   int    `json:"direction_id"`
	DirectionName string `json:"direction_name"`
	RouteID       int    `json:"route_id"`
	RouteType     int    `json:"route_type"`
}

// DirectionsResponse wraps the directions endpoints.
type DirectionsResponse struct {
	Directions []Direction `json:"directions"`
	Status     Status      `json:"status"`
}

// Departure is a single timetabled/real-time service departure.
type Departure struct {
	StopID                int      `json:"stop_id"`
	RouteID               int      `json:"route_id"`
	RunID                 int      `json:"run_id"`
	RunRef                string   `json:"run_ref"`
	DirectionID           int      `json:"direction_id"`
	DisruptionIDs         []int64  `json:"disruption_ids"`
	ScheduledDepartureUTC *string  `json:"scheduled_departure_utc"`
	EstimatedDepartureUTC *string  `json:"estimated_departure_utc"`
	AtPlatform            bool     `json:"at_platform"`
	PlatformNumber        *string  `json:"platform_number"`
	Flags                 string   `json:"flags"`
	DepartureSequence     int      `json:"departure_sequence"`
	DepartureNote         *string  `json:"departure_note"`
}

// Run is an individual trip/service of a route.
type Run struct {
	RunID           int    `json:"run_id"`
	RunRef          string `json:"run_ref"`
	RouteID         int    `json:"route_id"`
	RouteType       int    `json:"route_type"`
	FinalStopID     int    `json:"final_stop_id"`
	DestinationName string `json:"destination_name"`
	Status          string `json:"status"`
	DirectionID     int    `json:"direction_id"`
	RunSequence     int    `json:"run_sequence"`
	ExpressStopCount int   `json:"express_stop_count"`
}

// DisruptionRoute is a route referenced by a disruption.
type DisruptionRoute struct {
	RouteType   int    `json:"route_type"`
	RouteID     int    `json:"route_id"`
	RouteName   string `json:"route_name"`
	RouteNumber string `json:"route_number"`
	Direction   *DisruptionDirection `json:"direction"`
}

// DisruptionDirection is the optional direction a disruption applies to.
type DisruptionDirection struct {
	DirectionID   int    `json:"direction_id"`
	DirectionName string `json:"direction_name"`
}

// DisruptionStop is a stop referenced by a disruption.
type DisruptionStop struct {
	RouteType int    `json:"route_type"`
	StopID    int    `json:"stop_id"`
	StopName  string `json:"stop_name"`
}

// Disruption describes a service disruption.
type Disruption struct {
	DisruptionID     int64             `json:"disruption_id"`
	Title            string            `json:"title"`
	URL              string            `json:"url"`
	Description      string            `json:"description"`
	DisruptionStatus string            `json:"disruption_status"`
	DisruptionType   string            `json:"disruption_type"`
	PublishedOn      string            `json:"published_on"`
	LastUpdated      string            `json:"last_updated"`
	FromDate         string            `json:"from_date"`
	ToDate           *string           `json:"to_date"`
	Routes           []DisruptionRoute `json:"routes"`
	Stops            []DisruptionStop  `json:"stops"`
}

// DeparturesResponse wraps the departures endpoints.
type DeparturesResponse struct {
	Departures  []Departure           `json:"departures"`
	Stops       map[string]StopModel  `json:"stops"`
	Routes      map[string]Route      `json:"routes"`
	Runs        map[string]Run        `json:"runs"`
	Directions  map[string]Direction  `json:"directions"`
	Disruptions map[string]Disruption `json:"disruptions"`
	Status      Status                `json:"status"`
}

// StopsOnRouteResponse wraps stops/route/{route}/route_type/{rt}.
type StopsOnRouteResponse struct {
	Stops  []StopModel `json:"stops"`
	Status Status      `json:"status"`
}

// StopsByDistanceResponse wraps stops/location/{lat},{lng}.
type StopsByDistanceResponse struct {
	Stops  []StopModel `json:"stops"`
	Status Status      `json:"status"`
}

// StopDetails carries facility/platform information for a stop.
type StopDetails struct {
	StopID        int     `json:"stop_id"`
	StopName      string  `json:"stop_name"`
	StopType      string  `json:"stop_type"`
	RouteType     int     `json:"route_type"`
	StationType   string  `json:"station_type"`
	StationDescription string `json:"station_description"`
	StopLatitude  float64 `json:"stop_latitude"`
	StopLongitude float64 `json:"stop_longitude"`
	Routes        []Route `json:"routes"`
}

// StopResponse wraps stops/{id}/route_type/{rt}.
type StopResponse struct {
	Stop   StopDetails `json:"stop"`
	Status Status      `json:"status"`
}

// ResultOutlet is a myki ticket outlet in search results.
type ResultOutlet struct {
	OutletSlidID    string  `json:"outlet_slid_spid"`
	OutletName      string  `json:"outlet_name"`
	OutletBusiness  string  `json:"outlet_business"`
	OutletLatitude  float64 `json:"outlet_latitude"`
	OutletLongitude float64 `json:"outlet_longitude"`
	OutletSuburb    string  `json:"outlet_suburb"`
	OutletDistance  float64 `json:"outlet_distance"`
}

// SearchResult wraps the search endpoint.
type SearchResult struct {
	Stops   []StopModel    `json:"stops"`
	Routes  []Route        `json:"routes"`
	Outlets []ResultOutlet `json:"outlets"`
	Status  Status         `json:"status"`
}

// DisruptionsResponse wraps the disruptions endpoints. Disruptions are keyed
// by mode (e.g. "metro_train", "metro_tram").
type DisruptionsResponse struct {
	Disruptions map[string][]Disruption `json:"disruptions"`
	Status      Status                  `json:"status"`
}

// FareEstimateResponse wraps fare_estimate endpoints.
type FareEstimateResponse struct {
	FareEstimateResultStatus struct {
		StatusCode int    `json:"StatusCode"`
		Message    string `json:"Message"`
	} `json:"FareEstimateResultStatus"`
	FareEstimateResult struct {
		ZoneInfo struct {
			MinZone        int   `json:"MinZone"`
			MaxZone        int   `json:"MaxZone"`
			UniqueZones    []int `json:"UniqueZones"`
		} `json:"ZoneInfo"`
		PassengerFares []struct {
			PassengerType    string  `json:"PassengerType"`
			Fare2HourPeak    float64 `json:"Fare2HourPeak"`
			Fare2HourOffPeak float64 `json:"Fare2HourOffPeak"`
			FareDailyPeak    float64 `json:"FareDailyPeak"`
			FareDailyOffPeak float64 `json:"FareDailyOffPeak"`
		} `json:"PassengerFares"`
	} `json:"FareEstimateResult"`
	Status Status `json:"status"`
}

// OutletResponse wraps the outlets endpoints.
type OutletResponse struct {
	Outlets []ResultOutlet `json:"outlets"`
	Status  Status         `json:"status"`
}
