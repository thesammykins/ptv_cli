package gtfsrt

import (
	"strings"
	"time"

	gtfs "github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
)

const (
	currentObservationWindow = 90 * time.Second
	allowedFutureClockSkew   = 30 * time.Second
)

// NormalizeSnapshot converts a protobuf feed into the package's typed boundary
// and builds its lookup indexes exactly once.
func NormalizeSnapshot(feed Feed, message *gtfs.FeedMessage, fetchedAt time.Time) *Snapshot {
	fetchedAt = fetchedAt.UTC()
	snapshot := &Snapshot{
		Feed:           feed,
		Incrementality: IncrementalityUnknown,
		FetchedAt:      fetchedAt,
		FeedFreshness:  TimestampFreshness{State: FreshnessUnknown},
		labelIndex:     make(map[string][]int),
		entityIndex:    make(map[FeedEntityID]int),
	}
	if message == nil {
		return snapshot
	}

	header := message.GetHeader()
	if header != nil {
		snapshot.GTFSRealtime = header.GetGtfsRealtimeVersion()
		if header.Incrementality != nil {
			switch header.GetIncrementality() {
			case gtfs.FeedHeader_FULL_DATASET:
				snapshot.Incrementality = IncrementalityFullDataset
			case gtfs.FeedHeader_DIFFERENTIAL:
				snapshot.Incrementality = IncrementalityDifferential
			default:
				snapshot.Incrementality = IncrementalityUnknown
			}
		}
		if header.Timestamp != nil && header.GetTimestamp() > 0 {
			observedAt := time.Unix(int64(header.GetTimestamp()), 0).UTC()
			snapshot.FeedTimestamp = &observedAt
			snapshot.FeedFreshness = classifyTimestamp(&observedAt, fetchedAt)
		}
	}

	entities := message.GetEntity()
	snapshot.Entities = make([]EntityMetadata, 0, len(entities))
	snapshot.Vehicles = make([]VehicleObservation, 0, len(entities))
	for _, entity := range entities {
		if entity == nil {
			continue
		}
		metadata := EntityMetadata{
			ID:            FeedEntityID(entity.GetId()),
			Deleted:       entity.GetIsDeleted(),
			HasTripUpdate: entity.GetTripUpdate() != nil,
			HasVehicle:    entity.GetVehicle() != nil,
			HasAlert:      entity.GetAlert() != nil,
		}
		snapshot.entityIndex[metadata.ID] = len(snapshot.Entities)
		snapshot.Entities = append(snapshot.Entities, metadata)
		snapshot.Counts.Entities++
		if metadata.Deleted {
			snapshot.Counts.Deleted++
		}
		if metadata.HasTripUpdate {
			snapshot.Counts.TripUpdates++
		}
		if metadata.HasAlert {
			snapshot.Counts.Alerts++
		}
		if !metadata.HasVehicle {
			continue
		}

		observation := normalizeVehicle(entity, snapshot.FeedFreshness, fetchedAt)
		vehicleIndex := len(snapshot.Vehicles)
		snapshot.Vehicles = append(snapshot.Vehicles, observation)
		snapshot.Counts.Vehicles++
		for _, key := range identifierKeys(string(observation.Label)) {
			snapshot.labelIndex[key] = append(snapshot.labelIndex[key], vehicleIndex)
		}
	}
	return snapshot
}

func normalizeVehicle(entity *gtfs.FeedEntity, feedFreshness TimestampFreshness, fetchedAt time.Time) VehicleObservation {
	position := entity.GetVehicle()
	trip := position.GetTrip()
	descriptor := position.GetVehicle()
	observation := VehicleObservation{
		EntityID:     FeedEntityID(entity.GetId()),
		TripID:       StaticTripID(trip.GetTripId()),
		RouteID:      StaticRouteID(trip.GetRouteId()),
		StartDate:    trip.GetStartDate(),
		StartTime:    trip.GetStartTime(),
		Label:        PublicVehicleLabel(descriptor.GetLabel()),
		LicensePlate: descriptor.GetLicensePlate(),
		StopID:       StaticStopID(position.GetStopId()),
		Freshness: ObservationFreshness{
			Feed:   feedFreshness,
			Entity: TimestampFreshness{State: FreshnessUnknown},
		},
	}
	if position.CurrentStatus != nil {
		observation.CurrentStatus = position.GetCurrentStatus().String()
	}
	if position.OccupancyStatus != nil {
		observation.OccupancyStatus = position.GetOccupancyStatus().String()
	}
	if position.Timestamp != nil && position.GetTimestamp() > 0 {
		observedAt := time.Unix(int64(position.GetTimestamp()), 0).UTC()
		observation.ObservedAt = &observedAt
		observation.Freshness.Entity = classifyTimestamp(&observedAt, fetchedAt)
	}
	observation.Freshness.Overall = combineFreshness(observation.Freshness.Feed.State, observation.Freshness.Entity.State)

	if coordinates := position.GetPosition(); coordinates != nil {
		if coordinates.Latitude != nil {
			latitude := float64(coordinates.GetLatitude())
			observation.Latitude = &latitude
		}
		if coordinates.Longitude != nil {
			longitude := float64(coordinates.GetLongitude())
			observation.Longitude = &longitude
		}
		if coordinates.Bearing != nil {
			bearing := float64(coordinates.GetBearing())
			observation.Bearing = &bearing
		}
		if coordinates.Speed != nil {
			speed := float64(coordinates.GetSpeed())
			observation.Speed = &speed
		}
	}
	return observation
}

func classifyTimestamp(observedAt *time.Time, now time.Time) TimestampFreshness {
	if observedAt == nil || observedAt.IsZero() {
		return TimestampFreshness{State: FreshnessUnknown}
	}
	observed := observedAt.UTC()
	age := now.UTC().Sub(observed)
	ageSeconds := int64(age / time.Second)
	state := FreshnessCurrent
	switch {
	case age < -allowedFutureClockSkew:
		state = FreshnessFuture
	case age > currentObservationWindow:
		state = FreshnessStale
	}
	return TimestampFreshness{State: state, ObservedAt: &observed, AgeSeconds: &ageSeconds}
}

func combineFreshness(feed, entity FreshnessState) FreshnessState {
	if entity == FreshnessFuture || feed == FreshnessFuture {
		return FreshnessFuture
	}
	if entity == FreshnessStale || feed == FreshnessStale {
		return FreshnessStale
	}
	if entity == FreshnessCurrent && feed == FreshnessCurrent {
		return FreshnessCurrent
	}
	return FreshnessUnknown
}

// FindVehicleByLabel finds the best normalized observation for a public label
// or consist component. Internal IDs and unrelated trip/entity identifiers are
// deliberately not accepted by this API.
func (s *Snapshot) FindVehicleByLabel(label PublicVehicleLabel) (*VehicleObservation, bool) {
	if s == nil {
		return nil, false
	}
	keys := identifierKeys(string(label))
	if len(keys) == 0 {
		return nil, false
	}
	indices := s.labelIndex[keys[0]]
	if len(indices) == 0 {
		return nil, false
	}
	best := indices[0]
	for _, candidate := range indices[1:] {
		if fresherObservation(s.Vehicles[candidate], s.Vehicles[best]) {
			best = candidate
		}
	}
	return &s.Vehicles[best], true
}

// FindEntity returns metadata only from the feed-entity namespace.
func (s *Snapshot) FindEntity(id FeedEntityID) (EntityMetadata, bool) {
	if s == nil {
		return EntityMetadata{}, false
	}
	index, ok := s.entityIndex[id]
	if !ok {
		return EntityMetadata{}, false
	}
	return s.Entities[index], true
}

func fresherObservation(candidate, current VehicleObservation) bool {
	rank := func(state FreshnessState) int {
		switch state {
		case FreshnessCurrent:
			return 0
		case FreshnessStale:
			return 1
		case FreshnessUnknown:
			return 2
		default:
			return 3
		}
	}
	if candidateRank, currentRank := rank(candidate.Freshness.Overall), rank(current.Freshness.Overall); candidateRank != currentRank {
		return candidateRank < currentRank
	}
	if candidate.ObservedAt == nil {
		return false
	}
	return current.ObservedAt == nil || candidate.ObservedAt.After(*current.ObservedAt)
}

func identifierKeys(value string) []string {
	value = normalize(value)
	if value == "" {
		return nil
	}
	keys := []string{value}
	seen := map[string]bool{value: true}
	for _, part := range strings.FieldsFunc(value, func(r rune) bool {
		return r == '-' || r == ' ' || r == ',' || r == '/'
	}) {
		if part != "" && !seen[part] {
			keys = append(keys, part)
			seen[part] = true
		}
	}
	return keys
}

func normalize(value string) string {
	return strings.ToUpper(strings.TrimSpace(value))
}
