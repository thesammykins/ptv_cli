package gtfsrt

import (
	"time"
)

// Named identifier types prevent accidental equality across unrelated PTV,
// static GTFS, feed-entity, and vehicle namespaces.
type (
	FeedEntityID       string
	StaticTripID       string
	StaticRouteID      string
	StaticStopID       string
	PublicVehicleLabel string
)

// Incrementality is the declared update semantics of a feed snapshot.
type Incrementality string

const (
	IncrementalityUnknown      Incrementality = "unknown"
	IncrementalityFullDataset  Incrementality = "full_dataset"
	IncrementalityDifferential Incrementality = "differential"
)

// FreshnessState describes timestamp evidence independently of identity.
type FreshnessState string

const (
	FreshnessCurrent FreshnessState = "current"
	FreshnessStale   FreshnessState = "stale"
	FreshnessUnknown FreshnessState = "unknown"
	FreshnessFuture  FreshnessState = "future"
)

// TimestampFreshness records the evidence used for a freshness classification.
type TimestampFreshness struct {
	State      FreshnessState `json:"state"`
	ObservedAt *time.Time     `json:"observed_at,omitempty"`
	AgeSeconds *int64         `json:"age_seconds,omitempty"`
}

// ObservationFreshness keeps feed and entity evidence separate and provides a
// conservative overall state for command integration.
type ObservationFreshness struct {
	Feed    TimestampFreshness `json:"feed"`
	Entity  TimestampFreshness `json:"entity"`
	Overall FreshnessState     `json:"overall"`
}

// EntityMetadata records which payload type an entity carried without leaking
// raw protobuf objects across the client boundary.
type EntityMetadata struct {
	ID            FeedEntityID `json:"entity_id"`
	Deleted       bool         `json:"deleted"`
	HasTripUpdate bool         `json:"has_trip_update"`
	HasVehicle    bool         `json:"has_vehicle"`
	HasAlert      bool         `json:"has_alert"`
}

// EntityCounts summarizes the normalized contents of a snapshot.
type EntityCounts struct {
	Entities    int `json:"entities"`
	TripUpdates int `json:"trip_updates"`
	Vehicles    int `json:"vehicles"`
	Alerts      int `json:"alerts"`
	Deleted     int `json:"deleted"`
	Duplicates  int `json:"duplicates,omitempty"`
}

type StopTimeUpdate struct {
	StopID               StaticStopID `json:"stop_id,omitempty"`
	StopSequence         *int32       `json:"stop_sequence,omitempty"`
	ArrivalTime          *int64       `json:"arrival_time,omitempty"`
	ArrivalDelay         *int32       `json:"arrival_delay,omitempty"`
	ArrivalUncertainty   *int32       `json:"arrival_uncertainty,omitempty"`
	DepartureTime        *int64       `json:"departure_time,omitempty"`
	DepartureDelay       *int32       `json:"departure_delay,omitempty"`
	DepartureUncertainty *int32       `json:"departure_uncertainty,omitempty"`
	ScheduleRelationship string       `json:"schedule_relationship"`
}

type TripUpdate struct {
	EntityID             FeedEntityID         `json:"entity_id"`
	TripID               StaticTripID         `json:"trip_id,omitempty"`
	RouteID              StaticRouteID        `json:"route_id,omitempty"`
	DirectionID          *int32               `json:"direction_id,omitempty"`
	StartDate            string               `json:"start_date,omitempty"`
	StartTime            string               `json:"start_time,omitempty"`
	ScheduleRelationship string               `json:"schedule_relationship"`
	VehicleLabel         PublicVehicleLabel   `json:"vehicle_label,omitempty"`
	VehicleID            string               `json:"-"`
	Timestamp            *time.Time           `json:"timestamp,omitempty"`
	StopTimeUpdates      []StopTimeUpdate     `json:"stop_time_updates"`
	Freshness            ObservationFreshness `json:"freshness"`
}

type AlertEntity struct {
	AgencyID    string `json:"agency_id,omitempty"`
	RouteID     string `json:"route_id,omitempty"`
	RouteType   *int32 `json:"route_type,omitempty"`
	DirectionID *int32 `json:"direction_id,omitempty"`
	TripID      string `json:"trip_id,omitempty"`
	StopID      string `json:"stop_id,omitempty"`
}

type AlertPeriod struct {
	Start *time.Time `json:"start,omitempty"`
	End   *time.Time `json:"end,omitempty"`
}
type TranslatedString struct {
	Language string `json:"language,omitempty"`
	Text     string `json:"text"`
}
type Alert struct {
	EntityID         FeedEntityID         `json:"entity_id"`
	Cause            string               `json:"cause,omitempty"`
	Effect           string               `json:"effect,omitempty"`
	ActivePeriods    []AlertPeriod        `json:"active_periods"`
	InformedEntities []AlertEntity        `json:"informed_entities"`
	HeaderText       []TranslatedString   `json:"header_text"`
	DescriptionText  []TranslatedString   `json:"description_text"`
	URL              []TranslatedString   `json:"url"`
	Freshness        ObservationFreshness `json:"freshness"`
}

// VehicleObservation is the normalized public boundary for a vehicle-position
// entity. The upstream descriptor ID is deliberately not retained because it
// is an internal identifier, not public vehicle identity.
type VehicleObservation struct {
	EntityID        FeedEntityID         `json:"entity_id"`
	TripID          StaticTripID         `json:"trip_id,omitempty"`
	RouteID         StaticRouteID        `json:"route_id,omitempty"`
	StartDate       string               `json:"start_date,omitempty"`
	StartTime       string               `json:"start_time,omitempty"`
	Label           PublicVehicleLabel   `json:"label,omitempty"`
	LicensePlate    string               `json:"license_plate,omitempty"`
	StopID          StaticStopID         `json:"stop_id,omitempty"`
	CurrentStatus   string               `json:"current_status,omitempty"`
	OccupancyStatus string               `json:"occupancy_status,omitempty"`
	ObservedAt      *time.Time           `json:"observed_at,omitempty"`
	Latitude        *float64             `json:"latitude,omitempty"`
	Longitude       *float64             `json:"longitude,omitempty"`
	Bearing         *float64             `json:"bearing,omitempty"`
	Speed           *float64             `json:"speed,omitempty"`
	Freshness       ObservationFreshness `json:"freshness"`
}

// Snapshot is one fetched, normalized feed. Lookup indexes are built once when
// the snapshot is created and remain private implementation details.
type Snapshot struct {
	Feed           Feed                 `json:"feed"`
	GTFSRealtime   string               `json:"gtfs_realtime_version"`
	Incrementality Incrementality       `json:"incrementality"`
	FeedTimestamp  *time.Time           `json:"feed_timestamp,omitempty"`
	FetchedAt      time.Time            `json:"fetched_at"`
	FeedFreshness  TimestampFreshness   `json:"feed_freshness"`
	Entities       []EntityMetadata     `json:"entities"`
	Counts         EntityCounts         `json:"counts"`
	Vehicles       []VehicleObservation `json:"vehicles"`
	TripUpdates    []TripUpdate         `json:"trip_updates"`
	Alerts         []Alert              `json:"alerts"`

	labelIndex  map[string][]int
	entityIndex map[FeedEntityID]int
	tripIndex   map[string]int
	alertIndex  map[FeedEntityID]int
}
