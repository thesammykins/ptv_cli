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
		tripIndex:      make(map[string]int),
		alertIndex:     make(map[FeedEntityID]int),
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
		if _, exists := snapshot.entityIndex[metadata.ID]; exists {
			snapshot.Counts.Duplicates++
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
		if metadata.Deleted {
			continue
		}
		if metadata.HasVehicle {
			observation := normalizeVehicle(entity, snapshot.FeedFreshness, fetchedAt)
			vehicleIndex := len(snapshot.Vehicles)
			snapshot.Vehicles = append(snapshot.Vehicles, observation)
			snapshot.Counts.Vehicles++
			for _, key := range identifierKeys(string(observation.Label)) {
				snapshot.labelIndex[key] = append(snapshot.labelIndex[key], vehicleIndex)
			}
		}
		if metadata.HasTripUpdate {
			update := normalizeTripUpdate(entity, snapshot.FeedFreshness, fetchedAt)
			key := string(update.TripID) + "\x00" + update.StartDate
			if previous, exists := snapshot.tripIndex[key]; exists {
				snapshot.Counts.Duplicates++
				snapshot.TripUpdates[previous] = update
			} else {
				snapshot.tripIndex[key] = len(snapshot.TripUpdates)
				snapshot.TripUpdates = append(snapshot.TripUpdates, update)
			}
		}
		if metadata.HasAlert {
			alert := normalizeAlert(entity, snapshot.FeedFreshness, fetchedAt)
			if previous, exists := snapshot.alertIndex[alert.EntityID]; exists {
				snapshot.Counts.Duplicates++
				snapshot.Alerts[previous] = alert
			} else {
				snapshot.alertIndex[alert.EntityID] = len(snapshot.Alerts)
				snapshot.Alerts = append(snapshot.Alerts, alert)
			}
		}
	}
	return snapshot
}

func normalizeTripUpdate(entity *gtfs.FeedEntity, feedFreshness TimestampFreshness, fetchedAt time.Time) TripUpdate {
	update := entity.GetTripUpdate()
	descriptor := update.GetTrip()
	result := TripUpdate{EntityID: FeedEntityID(entity.GetId()), TripID: StaticTripID(descriptor.GetTripId()), RouteID: StaticRouteID(descriptor.GetRouteId()), StartDate: descriptor.GetStartDate(), StartTime: descriptor.GetStartTime(), ScheduleRelationship: descriptor.GetScheduleRelationship().String(), StopTimeUpdates: []StopTimeUpdate{}, Freshness: ObservationFreshness{Feed: feedFreshness, Entity: TimestampFreshness{State: FreshnessUnknown}}}
	if update.Vehicle != nil {
		result.VehicleLabel = PublicVehicleLabel(update.GetVehicle().GetLabel())
		result.VehicleID = update.GetVehicle().GetId()
	}
	if descriptor.DirectionId != nil {
		v := int32(descriptor.GetDirectionId())
		result.DirectionID = &v
	}
	if update.Timestamp != nil && update.GetTimestamp() > 0 {
		observed := time.Unix(int64(update.GetTimestamp()), 0).UTC()
		result.Timestamp = &observed
		result.Freshness.Entity = classifyTimestamp(&observed, fetchedAt)
	}
	for _, item := range update.GetStopTimeUpdate() {
		if item == nil {
			continue
		}
		normalized := StopTimeUpdate{StopID: StaticStopID(item.GetStopId()), ScheduleRelationship: item.GetScheduleRelationship().String()}
		if item.StopSequence != nil {
			v := int32(item.GetStopSequence())
			normalized.StopSequence = &v
		}
		normalized.ArrivalTime, normalized.ArrivalDelay, normalized.ArrivalUncertainty = stopEvent(item.GetArrival())
		normalized.DepartureTime, normalized.DepartureDelay, normalized.DepartureUncertainty = stopEvent(item.GetDeparture())
		result.StopTimeUpdates = append(result.StopTimeUpdates, normalized)
	}
	result.Freshness.Overall = combineFreshness(result.Freshness.Feed.State, result.Freshness.Entity.State)
	return result
}

func stopEvent(event *gtfs.TripUpdate_StopTimeEvent) (*int64, *int32, *int32) {
	if event == nil {
		return nil, nil, nil
	}
	var at *int64
	var delay, uncertainty *int32
	if event.Time != nil {
		v := event.GetTime()
		at = &v
	}
	if event.Delay != nil {
		v := event.GetDelay()
		delay = &v
	}
	if event.Uncertainty != nil {
		v := event.GetUncertainty()
		uncertainty = &v
	}
	return at, delay, uncertainty
}

func normalizeAlert(entity *gtfs.FeedEntity, feedFreshness TimestampFreshness, fetchedAt time.Time) Alert {
	raw := entity.GetAlert()
	result := Alert{EntityID: FeedEntityID(entity.GetId()), Cause: raw.GetCause().String(), Effect: raw.GetEffect().String(), ActivePeriods: []AlertPeriod{}, InformedEntities: []AlertEntity{}, HeaderText: selectTranslations(raw.GetHeaderText()), DescriptionText: selectTranslations(raw.GetDescriptionText()), URL: selectTranslations(raw.GetUrl()), Freshness: ObservationFreshness{Feed: feedFreshness, Entity: TimestampFreshness{State: FreshnessUnknown}}}
	for _, period := range raw.GetActivePeriod() {
		if period == nil {
			continue
		}
		item := AlertPeriod{}
		if period.Start != nil {
			v := time.Unix(int64(period.GetStart()), 0).UTC()
			item.Start = &v
		}
		if period.End != nil {
			v := time.Unix(int64(period.GetEnd()), 0).UTC()
			item.End = &v
		}
		result.ActivePeriods = append(result.ActivePeriods, item)
	}
	for _, selector := range raw.GetInformedEntity() {
		if selector == nil {
			continue
		}
		item := AlertEntity{AgencyID: selector.GetAgencyId(), RouteID: selector.GetRouteId(), StopID: selector.GetStopId()}
		if selector.RouteType != nil {
			v := selector.GetRouteType()
			item.RouteType = &v
		}
		if selector.DirectionId != nil {
			v := int32(selector.GetDirectionId())
			item.DirectionID = &v
		}
		if selector.Trip != nil {
			item.TripID = selector.GetTrip().GetTripId()
		}
		result.InformedEntities = append(result.InformedEntities, item)
	}
	if len(result.ActivePeriods) == 0 {
		// GTFS-Realtime defines an alert without active_period as always active.
		result.Freshness.Entity = TimestampFreshness{State: FreshnessCurrent}
	} else {
		now := fetchedAt
		for _, period := range result.ActivePeriods {
			if (period.Start == nil || !now.Before(*period.Start)) && (period.End == nil || now.Before(*period.End)) {
				result.Freshness.Entity = TimestampFreshness{State: FreshnessCurrent}
				break
			}
		}
	}
	result.Freshness.Overall = combineFreshness(result.Freshness.Feed.State, result.Freshness.Entity.State)
	return result
}

func selectTranslations(value *gtfs.TranslatedString) []TranslatedString {
	if value == nil {
		return []TranslatedString{}
	}
	var fallback *gtfs.TranslatedString_Translation
	for _, item := range value.GetTranslation() {
		if item == nil || strings.TrimSpace(item.GetText()) == "" {
			continue
		}
		if fallback == nil {
			fallback = item
		}
		if strings.EqualFold(item.GetLanguage(), "en") || strings.EqualFold(item.GetLanguage(), "en-US") {
			return []TranslatedString{{Language: item.GetLanguage(), Text: item.GetText()}}
		}
	}
	if fallback == nil {
		return []TranslatedString{}
	}
	return []TranslatedString{{Language: fallback.GetLanguage(), Text: fallback.GetText()}}
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

func (s *Snapshot) FindTripUpdate(tripID StaticTripID, startDate string) (*TripUpdate, bool) {
	if s == nil {
		return nil, false
	}
	index, ok := s.tripIndex[string(tripID)+"\x00"+startDate]
	if !ok {
		return nil, false
	}
	return &s.TripUpdates[index], true
}

func (s *Snapshot) AlertsForRoute(routeID string) []Alert {
	if s == nil {
		return nil
	}
	var result []Alert
	for _, alert := range s.Alerts {
		for _, entity := range alert.InformedEntities {
			if entity.RouteID == routeID {
				result = append(result, alert)
				break
			}
		}
	}
	return result
}
func (s *Snapshot) AlertsForStop(stopID string) []Alert {
	if s == nil {
		return nil
	}
	var result []Alert
	for _, alert := range s.Alerts {
		for _, entity := range alert.InformedEntities {
			if entity.StopID == stopID {
				result = append(result, alert)
				break
			}
		}
	}
	return result
}
func (s *Snapshot) AllAlerts() []Alert {
	if s == nil {
		return nil
	}
	return append([]Alert(nil), s.Alerts...)
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
