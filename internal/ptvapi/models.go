package ptvapi

// RunRef identifies a PTV Timetable API run. It is deliberately distinct from
// static GTFS trip IDs and GTFS Realtime entity identifiers.
type RunRef string

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
	DirectionID               int    `json:"direction_id"`
	DirectionName             string `json:"direction_name"`
	RouteDirectionDescription string `json:"route_direction_description"`
	RouteID                   int    `json:"route_id"`
	RouteType                 int    `json:"route_type"`
}

// DirectionsResponse wraps the directions endpoints.
type DirectionsResponse struct {
	Directions []Direction `json:"directions"`
	Status     Status      `json:"status"`
}

// Departure is a single timetabled/real-time service departure.
type Departure struct {
	StopID                int     `json:"stop_id"`
	RouteID               int     `json:"route_id"`
	RunID                 int     `json:"run_id"`
	RunRef                string  `json:"run_ref"`
	DirectionID           int     `json:"direction_id"`
	DisruptionIDs         []int64 `json:"disruption_ids"`
	ScheduledDepartureUTC *string `json:"scheduled_departure_utc"`
	EstimatedDepartureUTC *string `json:"estimated_departure_utc"`
	AtPlatform            bool    `json:"at_platform"`
	PlatformNumber        *string `json:"platform_number"`
	Flags                 string  `json:"flags"`
	DepartureSequence     int     `json:"departure_sequence"`
	DepartureNote         *string `json:"departure_note"`
}

// Run is an individual trip/service of a route.
type Run struct {
	RunID             int                `json:"run_id"`
	RunRef            string             `json:"run_ref"`
	RouteID           int                `json:"route_id"`
	RouteType         int                `json:"route_type"`
	FinalStopID       int                `json:"final_stop_id"`
	DestinationName   string             `json:"destination_name"`
	Status            string             `json:"status"`
	DirectionID       int                `json:"direction_id"`
	RunSequence       int                `json:"run_sequence"`
	ExpressStopCount  int                `json:"express_stop_count"`
	VehiclePosition   *VehiclePosition   `json:"vehicle_position"`
	VehicleDescriptor *VehicleDescriptor `json:"vehicle_descriptor"`
	Geopath           []any              `json:"geopath"`
	RunNote           string             `json:"run_note"`
}

// VehiclePosition is the best live position PTV exposes for a run.
type VehiclePosition struct {
	Latitude    *float64 `json:"latitude"`
	Longitude   *float64 `json:"longitude"`
	Easting     *float64 `json:"easting"`
	Northing    *float64 `json:"northing"`
	Direction   string   `json:"direction"`
	Bearing     *float64 `json:"bearing"`
	Supplier    string   `json:"supplier"`
	DatetimeUTC string   `json:"datetime_utc"`
	ExpiryTime  string   `json:"expiry_time"`
}

// VehicleDescriptor identifies characteristics of the vehicle serving a run.
type VehicleDescriptor struct {
	Operator       string `json:"operator"`
	ID             string `json:"id"`
	LowFloor       *bool  `json:"low_floor"`
	AirConditioned *bool  `json:"air_conditioned"`
	Description    string `json:"description"`
	Supplier       string `json:"supplier"`
	Length         string `json:"length"`
}

// DisruptionRoute is a route referenced by a disruption.
type DisruptionRoute struct {
	RouteType   int                  `json:"route_type"`
	RouteID     int                  `json:"route_id"`
	RouteName   string               `json:"route_name"`
	RouteNumber string               `json:"route_number"`
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

// RunResponse wraps a single run endpoint.
type RunResponse struct {
	Run    Run    `json:"run"`
	Status Status `json:"status"`
}

// RunsResponse wraps route runs endpoints.
type RunsResponse struct {
	Runs   []Run  `json:"runs"`
	Status Status `json:"status"`
}

// PatternDeparture is a departure within a stopping pattern.
type PatternDeparture struct {
	Departure
	SkippedStops []StopModel `json:"skipped_stops"`
}

// StoppingPatternResponse wraps a run's stopping pattern.
type StoppingPatternResponse struct {
	Departures  []PatternDeparture   `json:"departures"`
	Stops       map[string]StopModel `json:"stops"`
	Routes      map[string]Route     `json:"routes"`
	Runs        map[string]Run       `json:"runs"`
	Directions  map[string]Direction `json:"directions"`
	Disruptions []Disruption         `json:"disruptions"`
	Status      Status               `json:"status"`
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

// StopGPS is the official nested coordinate representation returned by Stop
// Details.
type StopGPS struct {
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
}

// StopLocation carries the location portion of Stop Details.
type StopLocation struct {
	GPS *StopGPS `json:"gps"`
}

// StopAmenityDetails carries facilities reported for a stop.
type StopAmenityDetails struct {
	Toilet     *bool   `json:"toilet"`
	TaxiRank   *bool   `json:"taxi_rank"`
	CarParking *string `json:"car_parking"`
	CCTV       *bool   `json:"cctv"`
}

// StopAccessibilityWheelchair carries wheelchair-specific facilities. The
// misspellings in two JSON keys are part of the upstream v3 contract.
type StopAccessibilityWheelchair struct {
	AccessibleRamp        *bool `json:"accessible_ramp"`
	Parking               *bool `json:"parking"`
	Telephone             *bool `json:"telephone"`
	Toilet                *bool `json:"toilet"`
	LowTicketCounter      *bool `json:"low_ticket_counter"`
	Manouvering           *bool `json:"manouvering"`
	RaisedPlatform        *bool `json:"raised_platform"`
	Ramp                  *bool `json:"ramp"`
	SecondaryPath         *bool `json:"secondary_path"`
	RaisedPlatformShelter *bool `json:"raised_platform_shelther"`
	SteepRamp             *bool `json:"steep_ramp"`
}

// StopAccessibility carries general and wheelchair accessibility facilities.
type StopAccessibility struct {
	Lighting                      *bool                        `json:"lighting"`
	PlatformNumber                *int                         `json:"platform_number"`
	AudioCustomerInformation      *bool                        `json:"audio_customer_information"`
	Escalator                     *bool                        `json:"escalator"`
	HearingLoop                   *bool                        `json:"hearing_loop"`
	Lift                          *bool                        `json:"lift"`
	Stairs                        *bool                        `json:"stairs"`
	StopAccessible                *bool                        `json:"stop_accessible"`
	TactileGroundSurfaceIndicator *bool                        `json:"tactile_ground_surface_indicator"`
	WaitingRoom                   *bool                        `json:"waiting_room"`
	Wheelchair                    *StopAccessibilityWheelchair `json:"wheelchair"`
}

// StopStaffing carries the staffing windows returned by PTV. WedPMTo retains
// the upstream contract's capital T in wed_pm_To.
type StopStaffing struct {
	FriAMFrom        *string `json:"fri_am_from"`
	FriAMTo          *string `json:"fri_am_to"`
	FriPMFrom        *string `json:"fri_pm_from"`
	FriPMTo          *string `json:"fri_pm_to"`
	MonAMFrom        *string `json:"mon_am_from"`
	MonAMTo          *string `json:"mon_am_to"`
	MonPMFrom        *string `json:"mon_pm_from"`
	MonPMTo          *string `json:"mon_pm_to"`
	PHAdditionalText *string `json:"ph_additional_text"`
	PHFrom           *string `json:"ph_from"`
	PHTo             *string `json:"ph_to"`
	SatAMFrom        *string `json:"sat_am_from"`
	SatAMTo          *string `json:"sat_am_to"`
	SatPMFrom        *string `json:"sat_pm_from"`
	SatPMTo          *string `json:"sat_pm_to"`
	SunAMFrom        *string `json:"sun_am_from"`
	SunAMTo          *string `json:"sun_am_to"`
	SunPMFrom        *string `json:"sun_pm_from"`
	SunPMTo          *string `json:"sun_pm_to"`
	ThuAMFrom        *string `json:"thu_am_from"`
	ThuAMTo          *string `json:"thu_am_to"`
	ThuPMFrom        *string `json:"thu_pm_from"`
	ThuPMTo          *string `json:"thu_pm_to"`
	TueAMFrom        *string `json:"tue_am_from"`
	TueAMTo          *string `json:"tue_am_to"`
	TuePMFrom        *string `json:"tue_pm_from"`
	TuePMTo          *string `json:"tue_pm_to"`
	WedAMFrom        *string `json:"wed_am_from"`
	WedAMTo          *string `json:"wed_am_to"`
	WedPMFrom        *string `json:"wed_pm_from"`
	WedPMTo          *string `json:"wed_pm_To"`
}

// StopDetails carries the endpoint-specific official Stop Details response.
type StopDetails struct {
	DisruptionIDs      []int64             `json:"disruption_ids"`
	StationType        string              `json:"station_type"`
	StationDescription string              `json:"station_description"`
	RouteType          int                 `json:"route_type"`
	StopLocation       *StopLocation       `json:"stop_location"`
	StopAmenities      *StopAmenityDetails `json:"stop_amenities"`
	StopAccessibility  *StopAccessibility  `json:"stop_accessibility"`
	StopStaffing       *StopStaffing       `json:"stop_staffing"`
	Routes             []Route             `json:"routes"`
	StopID             int                 `json:"stop_id"`
	StopName           string              `json:"stop_name"`
	StopLandmark       *string             `json:"stop_landmark"`
}

// StopResponse wraps stops/{id}/route_type/{rt}.
type StopResponse struct {
	Stop        StopDetails           `json:"stop"`
	Disruptions map[string]Disruption `json:"disruptions"`
	Status      Status                `json:"status"`
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
			MinZone     int   `json:"MinZone"`
			MaxZone     int   `json:"MaxZone"`
			UniqueZones []int `json:"UniqueZones"`
		} `json:"ZoneInfo"`
		PassengerFares []struct {
			PassengerType    string  `json:"PassengerType"`
			Fare2HourPeak    float64 `json:"Fare2HourPeak"`
			Fare2HourOffPeak float64 `json:"Fare2HourOffPeak"`
			FareDailyPeak    float64 `json:"FareDailyPeak"`
			FareDailyOffPeak float64 `json:"FareDailyOffPeak"`
		} `json:"PassengerFares"`
	} `json:"FareEstimateResult"`
}

// OutletResponse wraps the outlets endpoints.
type OutletResponse struct {
	Outlets []ResultOutlet `json:"outlets"`
	Status  Status         `json:"status"`
}
