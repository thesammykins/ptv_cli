# TECH.md: Open Data First — Implementation

## Architecture Overview

The existing codebase has three data layers:
- `internal/gtfs/` — local SQLite static GTFS (generation manager, schema v2, connections, timetable loader)
- `internal/gtfsrt/` — Transport Victoria Open Data GTFS Realtime protobuf client
- `internal/ptvapi/` — PTV Timetable API v3 HMAC-signed HTTP client

The work establishes GTFS and Open Data as the primary platform, with v3 as a supplementary enrichment layer. This inverts the current dispatch priority where v3 is tried first.

### Live repository boundaries

The published GTFS generation is schema v2. Its text IDs (`stop_id`, `route_id`,
`trip_id`, and related labels) are namespaced with `feedMode:` by the compiler,
but relationships are keyed by generation-local integers (`*_key`). New query
code must resolve the public namespaced ID to its local key and join through the
key columns; it must not assume that a source ID is globally unique or use a
text-ID join where a key join is available. Returned DTOs may expose both the
namespaced public ID and the feed mode, but must not expose local keys as stable
user-facing identities.

The existing `GenerationManager.OpenCurrent` opens an immutable read-only
generation and `GenerationManager.AcquireUpdate` owns the SQLite-backed update
lease. This migration must preserve that publication model. It must not add a
second stale-lock deletion protocol or mutate an open generation in place.

The existing GTFS-Realtime normalizer currently exposes vehicle positions and
typed freshness/entity metadata. Trip updates and alerts are new normalized
surfaces; they must retain the feed-local identity and source provenance without
serializing private vehicle IDs or treating a feed entity ID as a static trip ID.

## Core Principle: Primary + Enrichment

Every command follows this pattern:

```
1. Resolve primary result from GTFS static and/or Open Data GTFS-R.
2. If v3 credentials are present, fetch supplementary fields and merge into the primary result.
3. Return the merged result. The primary result is never discarded in favour of v3.
```

This replaces the current pattern where commands call v3 first and fall back to GTFS only on credential failure.

## Implementation Phases

### Phase 0: Pre-Flight Validation — GTFS Static ↔ GTFS-R trip_id Join

Before building any real-time departure or trip tracking on the assumption that GTFS static trip_ids match GTFS-R trip_update trip_ids, this must be validated against live data.

**Validation procedure** (`cmd/gtfs.go` — add `ptv gtfs validate-realtime` subcommand, or a one-off test script):

1. Fetch a metro trip-updates snapshot from Open Data.
2. For each trip_update, extract `trip.trip_id` and `trip.start_date`.
3. Query the local GTFS `trips` table by feed mode and source trip ID/namespaced trip ID, using the service-date context; do not guess a raw string prefix without recording the observed mapping.
4. Report: match rate (% of trip_updates that resolve to a local trip), sample matched and unmatched trip_ids, and the namespace format used by each source.

**Expected outcomes and contingency:**
- **>95% match with `{feedMode}:{trip_id}` namespacing**: proceed with the join design as specified in Phase 4g/5c.
- **High match but different namespace**: adjust the `FindTripUpdate` lookup to use the correct key derivation.
- **Low match rate**: the static↔realtime join is broken. Do not silently fall back at runtime. Choose and document one alternative strategy (for example route_id + start_time + direction_id, or stop_id + scheduled departure window), add it as a `TripUpdateMatchStrategy` field in the join code, and add fixtures proving ambiguity handling and false-positive rejection.

This validation gates all real-time commands (`next`, `track`, `disruptions`). It runs once during development and is re-run after each GTFS feed update to catch namespace drift. The selected strategy and observed match rate are recorded in implementation notes and surfaced in debug/contract evidence; a command must report `unknown` rather than claim a realtime match when identity or `start_date` is missing.

### Phase 1: GTFS Query Layer (`internal/gtfs/queries.go` — new file)

New file providing typed query functions over the existing schema. All queries
use the existing `Store` and respect read-only generation semantics. Each query
must accept a namespaced public ID or a resolved local key through an internal
resolver, then perform relationship joins on `stop_key`, `route_key`,
`trip_key`, and `service_key`. A source ID is accepted only when it is
unambiguous within the selected feed/mode. Search and nearby results return
namespaced text IDs; local integer keys remain internal.

```go
// StopSearch returns stops matching a term (substring, case-insensitive).
func (s *Store) StopSearch(ctx context.Context, term string, feedModes []int, limit int) ([]StopResult, error)

// NearbyStops returns stops within maxDistance metres of lat/lng, ordered by distance.
func (s *Store) NearbyStops(ctx context.Context, lat, lng float64, feedModes []int, maxDistance float64, limit int) ([]NearbyStopResult, error)

// RoutesByMode returns all routes, optionally filtered by feed_mode.
func (s *Store) RoutesByMode(ctx context.Context, feedModes []int) ([]RouteResult, error)

// RouteDetail returns a route with its directions (derived from trips) and ordered stops.
func (s *Store) RouteDetail(ctx context.Context, routeID string) (*RouteDetailResult, error)

// StopsOnRoute returns stops ordered by stop_sequence for a route, optionally by direction.
func (s *Store) StopsOnRoute(ctx context.Context, routeID string, directionID *int) ([]StopResult, error)

// StopDepartures returns scheduled departures for a stop on a service date.
// It filters active calendar/calendar_dates services and uses the existing
// ServiceDayAnchor semantics for times beyond midnight.
func (s *Store) StopDepartures(ctx context.Context, stopID string, date time.Time, routeID string, feedModes []int, limit int) ([]DepartureResult, error)

// TripDetail returns the full stopping pattern for a trip on a service date.
// It resolves an active service instance and rejects ambiguous source IDs.
func (s *Store) TripDetail(ctx context.Context, tripID string, date time.Time) (*TripDetailResult, error)

// StopDetail returns stop info with serving routes, transfers, and pathways.
func (s *Store) StopDetail(ctx context.Context, stopID string) (*StopDetailResult, error)

// RoutesServingStop returns distinct routes that serve a stop.
func (s *Store) RoutesServingStop(ctx context.Context, stopID string) ([]RouteResult, error)
```

**Result types** defined in the same file:

```go
type StopResult struct {
    StopID             string  // namespaced: "{feedMode}:{sourceId}"
    StopName           string
    StopLat            float64
    StopLon            float64
    FeedMode           int
    ParentStation      string
    LocationType       int
    WheelchairBoarding int
}

type NearbyStopResult struct {
    StopResult
    DistanceMetres float64
}

type RouteResult struct {
    RouteID   string // namespaced
    ShortName string
    LongName  string
    FeedMode  int
    RouteType int // GTFS route_type
}

type RouteDetailResult struct {
    Route      RouteResult
    Directions []DirectionResult
    Stops      map[int][]StopResult // directionID → ordered stops
}

type DirectionResult struct {
    DirectionID int
    Headsign    string
    Description string // derived from long_name + direction
}

type DepartureResult struct {
    TripID         string
    RouteID        string
    StopID         string
    StopSequence   int
    DepartureSec   int    // seconds since service day anchor
    ArrivalSec     int
    Headsign       string
    RouteShortName string
    RouteLongName  string
    FeedMode       int
    DirectionID    int
    ServiceDate    string // YYYYMMDD
    BlockID        string
    PickupType     int
    DropOffType    int
}

type TripDetailResult struct {
    TripID      string
    RouteID     string
    Headsign    string
    DirectionID int
    BlockID     string
    ServiceID   string
    FeedMode    int
    Stops       []TripStopResult
}

type TripStopResult struct {
    StopID       string
    StopName     string
    StopLat      float64
    StopLon      float64
    StopSequence int
    ArrivalSec   int
    DepartureSec int
    PickupType   int
    DropOffType  int
}

type StopDetailResult struct {
    Stop      StopResult
    Routes    []RouteResult
    Transfers []TransferResult
    Pathways  []PathwayResult
}

type TransferResult struct {
    FromStopID   string
    ToStopID     string
    TransferType int
    MinTransferTime int
}

type PathwayResult struct {
    PathwayID          string
    FromStopID         string
    ToStopID           string
    PathwayMode        int
    IsBidirectional    bool
    Length             *float64
    TraversalTime      *int
    SignpostedAs       string
    ReversedSignpostedAs string
}
```

**Key implementation notes:**
- NearbyStops uses the Haversine formula in Go after a bounding-box SQL pre-filter on lat/lon ranges.
- StopDepartures resolves active service keys for the target date (reuses `activeServiceKeys`), then queries `connections` joined to `trips` and `routes` filtered by `dep_stop_key`.
- TripDetail queries `stop_times` for the trip joined to `stops`, ordered by `stop_sequence`.
- StopDepartures must join `connections` to `stops` and `routes` through their
  integer keys and must return the connection's `trip_id`, `route_id`, and stop
  IDs from the namespaced text columns. It must not treat the materialized
  connection row as a complete trip or infer a service date from `block_id`.
- StopDetail resolves a parent station to its child/platform stops for
  routable serving routes, while retaining the queried parent in the response.
  Transfers and pathways are returned with their transfer/pathway semantics and
  namespaced endpoint IDs; proximity-generated transfers are identified as
  derived data rather than claimed as source `transfers.txt` rows.
- StopSearch uses a two-stage approach:
  1. **Token-based SQL query**: split the search term on whitespace, build a `WHERE` clause that requires each token to appear as a substring in `stop_name` (case-insensitive `LIKE %token%` per token, ANDed). This handles "Flinders St", "Southern Cross", "Melbourne Central" naturally.
  2. **Go-side ranking**: score results by (a) number of tokens matched, (b) prefix match bonus, (c) shorter names ranked higher (more specific). Return top N by score.
  3. **Fallback to substring**: if token matching returns zero results, retry with a single `LIKE %term%` for fuzzy partial matches (e.g. "Flndrs" won't match but "Flinders" will catch "Flinders Street Station").
  This is significantly better than bare `LIKE %term%` for the common CLI input patterns. The ~30k row table is small enough that multi-token AND queries are fast without FTS.

### Phase 2: GTFS-R Trip Update & Alert Decoding (`internal/gtfsrt/`)

Extend the existing snapshot normalizer to decode trip updates and service alerts.

#### `internal/gtfsrt/types.go` — new types

```go
type StopTimeUpdate struct {
    StopID               StaticStopID
    StopSequence         *int32
    ArrivalTime          *int64 // unix seconds
    ArrivalDelay         *int32 // seconds
    ArrivalUncertainty   *int32
    DepartureTime        *int64
    DepartureDelay       *int32
    DepartureUncertainty *int32
    ScheduleRelationship string // SCHEDULED, SKIPPED, NO_DATA, UNSCHEDULED
}

type TripUpdate struct {
    EntityID             FeedEntityID
    TripID               StaticTripID
    RouteID              StaticRouteID
    DirectionID          *int32
    StartDate            string
    StartTime            string
    ScheduleRelationship string // SCHEDULED, ADDED, UNSCHEDULED, CANCELED
    VehicleLabel         PublicVehicleLabel
    VehicleID            string
    Timestamp            *time.Time
    StopTimeUpdates      []StopTimeUpdate
    Freshness            ObservationFreshness
}

type AlertEntity struct {
    AgencyID    string
    RouteID     string
    RouteType   *int32
    DirectionID *int32
    TripID      string
    StopID      string
}

type AlertPeriod struct {
    Start *time.Time
    End   *time.Time
}

type TranslatedString struct {
    Language string
    Text     string
}

type Alert struct {
    EntityID         FeedEntityID
    Cause            string
    Effect           string
    ActivePeriods    []AlertPeriod
    InformedEntities []AlertEntity
    HeaderText       []TranslatedString
    DescriptionText  []TranslatedString
    URL              []TranslatedString
    Freshness        ObservationFreshness
}
```

#### `internal/gtfsrt/snapshot.go` — extend `Snapshot`

```go
type Snapshot struct {
    // ... existing fields ...
    TripUpdates []TripUpdate
    Alerts      []Alert

    tripIndex  map[StaticTripID]int // for trip_update lookup
    alertIndex map[FeedEntityID]int
}
```

Extend `NormalizeSnapshot` to decode `TripUpdate` and `Alert` protobuf entities alongside vehicle positions.

Normalization rules are part of the contract, not implementation details:

- Preserve the feed-local `trip_id`, `route_id`, `stop_id`, `start_date`, and
  `start_time` exactly as typed values. A GTFS-R `FeedEntity.id` is only an
  entity identity and is never used as a static trip identity.
- For a stop-time update, use `stop_sequence` as the primary static join when
  present; otherwise use `stop_id`. If both are present they must resolve to the
  same static stop, or the update is ignored with a warning. If neither is
  present it is not joinable. Prefer departure time/delay for departures and
  arrival time/delay for arrivals; preserve missing timestamps as unknown rather
  than manufacturing a zero.
- Keep protobuf schedule relationships, including `CANCELED`, `SKIPPED`,
  `NO_DATA`, `ADDED`, and `UNSCHEDULED`, as explicit normalized values. Deleted
  feed entities are retained only in metadata/counts and are not user-visible
  observations.
- For translated alert strings, select the configured/default English
  translation when present, otherwise the first non-empty translation. Preserve
  active periods and all informed entity selectors; an alert with no active
  period is treated as time-unknown, not automatically current.
- Duplicate trip updates or alerts with the same feed entity ID are resolved
  deterministically (last normalized occurrence wins) and counted in contract
  diagnostics; a map lookup must never return an arbitrary duplicate.

Add lookup methods:

```go
func (s *Snapshot) FindTripUpdate(tripID StaticTripID, startDate string) (*TripUpdate, bool)
func (s *Snapshot) AlertsForRoute(routeID string) []Alert
func (s *Snapshot) AlertsForStop(stopID string) []Alert
func (s *Snapshot) AllAlerts() []Alert
```

#### Rate limiting

The Open Data API enforces 24 calls/60s per endpoint. Add a token-bucket rate
limiter keyed by the canonical feed URL (not one shared bucket for all modes):

```go
// In Client struct:
limiters map[string]*rate.Limiter // one limiter per feed URL
// each limiter: 24/60s = 0.4/s with burst 24
```

Wait on the endpoint limiter before each request, including requests made by
the invocation cache. Construct one client per command invocation so the
limiter is shared across all feeds used by that command; it need not be a
cross-process global. A rate-limit wait must honor `ctx` cancellation. This
requires the one new module dependency `golang.org/x/time/rate`; the earlier
"no new dependencies" statement applies only to the GTFS query layer.

### Phase 3: Primary + Enrichment Dispatch (`cmd/`)

#### `cmd/resolve.go` — inverted credential resolution

The dispatch helper resolves data sources with GTFS/Open Data as primary:

```go
type resolvedSources struct {
    GTFSStore   *gtfs.Store    // primary: nil only when no GTFS data ingested
    OpenDataKey string         // primary: empty when Open Data credentials absent
    V3Client    *ptvapi.Client // enrichment: nil when v3 credentials absent
}

func resolveSources(cfg *config.RuntimeConfig, opts config.LoadOptions) (*resolvedSources, error)
```

Resolution order:
1. Open local GTFS generation (existing `GenerationManager.OpenCurrent`).
2. Resolve Open Data credentials (env → keyring → env-file).
3. Attempt v3 credential resolution (existing `loadClient` path). Missing v3
   credentials leave `V3Client` nil without failing the primary command; an
   operational credential-store/configuration error is retained as a warning
   and never printed into JSON as a secret-bearing error.

This function **succeeds** as long as GTFS data is ingested. v3 and Open Data are independently optional.

### Phase 4: GTFS Resolution Helpers (must ship before Phase 5–6)

#### `cmd/resolve.go` — new functions

```go
// resolveStopFromGTFS resolves a stop name or namespaced ID from the local store.
// Uses token-based matching: each whitespace-delimited token must appear as a
// case-insensitive substring of stop_name. Results are ranked by token match
// count, prefix bonus, and name length.
func resolveStopFromGTFS(ctx context.Context, store *gtfs.Store, query string, feedModes []int) (*gtfs.StopResult, error)

// resolveRouteFromGTFS resolves a route by name, number, or namespaced ID.
func resolveRouteFromGTFS(ctx context.Context, store *gtfs.Store, query string, feedModes []int) (*gtfs.RouteResult, error)
```

These become the **primary** resolution functions. The existing `resolveStop`/`resolveRoute` (which call v3) are retained only for v3 enrichment paths that need PTV numeric IDs.

**Stop resolution detail**: the existing v3 `Search` endpoint does fuzzy matching server-side. The GTFS path must replicate reasonable behaviour:
- `"Flinders Street"` → token match on `flinders` AND `street` → matches "Flinders Street Station"
- `"Southern Cross"` → token match → matches "Southern Cross Station"
- `"11048"` → numeric → direct stop_id lookup (namespaced or raw)
- `"Flndrs"` → no token match → fallback to `LIKE %flndrs%` → likely no match → actionable error

#### v3 enrichment merge key normalization

Merging v3 enrichment fields into GTFS primary results requires matching entities across two different ID namespaces. The merge must be robust to naming variations.

**Merge strategy**: use normalized name tokens as the merge key, not raw strings or IDs.

```go
// cmd/merge.go — new file

// mergeKey produces a canonical key from a route or stop name for cross-source matching.
// It lowercases, strips common suffixes ("Station", "Railway Station", "Stop"),
// collapses whitespace, and removes punctuation.
func mergeKey(name, number string) string

// mergeKeys produces all candidate keys for an entity (e.g. both "flinders street"
// and "flinders street station" for a stop named "Flinders Street Station").
func mergeKeys(name, number string) []string
```

**Merge strategy**: use normalized name tokens only as a candidate key, never as
proof of identity. Prefer an explicit PTV/GTFS cross-reference when present;
otherwise require a unique candidate within the mode and preserve unmatched or
ambiguous enrichment as a warning rather than attaching it to an arbitrary
stop/route.

**Normalization rules:**
1. Lowercase all input.
2. Strip trailing "station", "railway station", "stop", "platform".
3. Do not strip abbreviations such as "st", "rd", or "ave" by default: they
   can change identity. If an abbreviation variant is added, retain both the
   lossless key and the variant as candidates and require a unique match.
4. Collapse multiple spaces to one.
5. Remove punctuation (parentheses, hyphens, slashes become spaces).
6. If a route number is present, it is prepended: `"1 frankston"` rather than `"frankston"`.

**Existing precedent**: `plan_disruptions.go` already uses `routeKeys()` which normalizes route name/number for cross-referencing. The new `mergeKey` function formalizes and extends this pattern for all enrichment merges.

**Known lossy cases**: "Flinders Street Station" (GTFS) vs "Flinders Street Railway Station" (v3) — handled by explicit suffix candidates. "Box Hill Station" vs "Box Hill" — handled by a suffix candidate. Cases that remain unmatched or produce multiple candidates are logged at debug level and surfaced in the `warnings` array so scripts can detect incomplete enrichment. Enrichment must never overwrite a primary result solely because a lossy name key matched.

#### Feed mode mapping

Reuse the existing `feedToAPIType` helper in `cmd/plan_disruptions.go` and
`gtfsModeName` helper in `cmd/plan.go` (or move both to a neutral shared helper
file without changing their mappings):

```
feedMode 1 → V/Line Train → route_type 3
feedMode 2 → Metro Train  → route_type 0
feedMode 3 → Tram         → route_type 1
feedMode 4 → Bus          → route_type 2
feedMode 5 → V/Line Coach → route_type 4
feedMode 6 → Regional Bus → route_type 2
```

### Phase 5: Command-by-Command Migration

Each existing command is restructured so that GTFS/Open Data is the primary path. Each command's `RunE` follows the primary + enrichment pattern:

```go
RunE: func(cmd *cobra.Command, args []string) error {
    sources, err := resolveSources(cfg, opts)
    if err != nil {
        return err
    }

    // Step 1: Primary result from GTFS/Open Data.
    result, err := buildPrimaryResult(cmd, args, sources)
    if err != nil {
        return err
    }

    // Step 2: Optional v3 enrichment.
    if sources.V3Client != nil {
        enrichWithV3(cmd.Context(), sources.V3Client, result)
    }

    // Step 3: Output.
    return renderResult(cmd, result)
}
```

#### 5a. `ptv search`

**Primary** (`runSearchPrimary`):
- `store.StopSearch(ctx, term, feedModes, limit)` → stops.
- `store.RoutesByMode(ctx, feedModes)` filtered by term → routes.
- Output: `searchOutput` with `data_source: "gtfs_static"`.

**v3 enrichment** (`enrichSearchWithV3`):
- `client.Search(ctx, term, routeTypes)` → merge outlet results.
- Update `data_source` to `"gtfs_static+v3_outlets"`.

#### 5b. `ptv lines`

**Primary** (`runLinesPrimary`):
- `store.RoutesByMode(ctx, feedModes)`.
- Maps feed_mode to mode labels via the shared `gtfsModeName` mapping.

**v3 enrichment**: none needed. GTFS routes are complete. Do not make a v3 call.

#### 5c. `ptv lines show`

**Primary** (`runLineShowPrimary`):
- `store.RouteDetail(ctx, routeID)`.
- Directions from trip headsigns + direction_id.
- Stops from stop_times ordered by stop_sequence.

**v3 enrichment**: none needed.

#### 5d. `ptv stops on`

**Primary** (`runStopsOnPrimary`):
- `store.StopsOnRoute(ctx, routeID, nil)`.

**v3 enrichment**: none needed.

#### 5e. `ptv stops near`

**Primary** (`runStopsNearPrimary`):
- `store.NearbyStops(ctx, lat, lng, feedModes, maxDistance, limit)`.
- Geocoding path unchanged (already credential-free via OpenStreetMap).

**v3 enrichment**: none needed.

#### 5f. `ptv station`

**Primary** (`runStationPrimary`):
- `store.StopDetail(ctx, stopID)`.
- Shows stop name, coordinates, parent station, wheelchair_boarding, routes serving the stop, transfers, pathways.

**v3 enrichment** (`enrichStationWithV3`):
- `client.StopDetails(ctx, stopID, routeType)` → merge amenities, accessibility, staffing, disruption references.
- Stderr: "station facilities enriched from PTV API".
- Update `data_source` to `"gtfs_static+v3_facilities"`.

#### 5g. `ptv next`

**Primary** (`runNextPrimary`):
1. `store.StopDepartures(ctx, stopID, now, "", feedModes, limit)` → scheduled departures for next 2 hours.
2. If Open Data KeyID present: fetch trip_updates for the matching mode feed.
3. Join trip_updates to scheduled departures by trip_id + start_date.
4. Apply arrival/departure delay to produce estimated times.
5. Handle schedule_relationship: SKIPPED stops marked, CANCELLED trips excluded.
6. Output: departures with scheduled + estimated + delay + route + headsign.
7. `data_source`: `"gtfs_static"` or `"gtfs_static+opendata_realtime"`.

The join is performed within the selected mode/feed using the Phase 0 strategy
and `start_date`; an update with a missing or ambiguous identity is not applied.
A trip-level cancellation removes its departures from the live result only when
the update is a validated match. A skipped stop remains in the result with an
explicit `skipped` relationship. Added/unscheduled trips are ignored unless a
separate static pattern can be resolved.

**v3 enrichment** (`enrichNextWithV3`):
- `client.Departures(ctx, routeType, stopID, opts)` → extract platform numbers and disruption IDs.
- Merge platform numbers into matching departures by scheduled time + route.
- Platform enrichment is best-effort and must not be applied when multiple
  departures share the same scheduled-time/route candidate; emit a warning and
  leave the primary row unchanged.
- Stderr: "platform numbers enriched from PTV API".
- Update `data_source` to include `"+v3_platforms"`.

#### 5h. `ptv disruptions`

**Schema unification**: Open Data GTFS-R alerts and v3 disruptions have fundamentally different schemas:

| Field | Open Data alert | v3 disruption |
|-------|----------------|---------------|
| ID | string (entity ID) | int64 (numeric) |
| Cause | enum (STRIKE, ACCIDENT, WEATHER, ...) | string (free text) |
| Effect | enum (NO_SERVICE, REDUCED_SERVICE, ...) | not present |
| Status | not present | string ("Current", "Planned") |
| Type | not present | string (free text) |
| Title | translated string | string |
| Description | translated string | string |
| Active period | start/end unix timestamps | from_date/to_date ISO |
| Affected entities | route_id, stop_id, trip_id, agency_id | routes[], stops[] with names |

The unified output schema preserves both source schemas in a single shape:

```go
type unifiedDisruption struct {
    // Cross-source fields
    ID          string   `json:"id"`                    // Open Data entity ID or "v3:{numeric_id}"
    Source      string   `json:"source"`                // "opendata" or "v3"
    Title       string   `json:"title"`
    Description string   `json:"description,omitempty"`
    URL         string   `json:"url,omitempty"`
    FromDate    string   `json:"from_date,omitempty"`   // RFC3339
    ToDate      *string  `json:"to_date,omitempty"`     // RFC3339

    // Open Data alert fields (null when source=v3)
    Cause  *string `json:"cause,omitempty"`
    Effect *string `json:"effect,omitempty"`

    // v3 disruption fields (null when source=opendata)
    DisruptionStatus *string `json:"disruption_status,omitempty"`
    DisruptionType   *string `json:"disruption_type,omitempty"`
    PTVID            *int64  `json:"ptv_disruption_id,omitempty"`

    // Affected entities (normalized from both sources)
    Routes []disruptionRouteOutput `json:"routes"`
    Stops  []disruptionStopOutput  `json:"stops"`
}
```

The `id` field uses a source-prefixed format: Open Data alerts keep their raw entity ID; v3 disruptions become `"v3:{numeric_id}"`. This prevents ID collisions and makes the source unambiguous.

**Primary** (`runDisruptionsPrimary`):
- If Open Data KeyID present: fetch service-alerts feeds for metro and tram.
- Normalize alerts to `unifiedDisruption` shape.
- Match alerts to routes by GTFS route_id → route_short_name/long_name.
- `data_source`: `"opendata_alerts"`.
- If Open Data KeyID is absent or the feed is unavailable: return a successful
  empty/degraded result with `warnings[]` and stderr guidance to run `ptv auth
  opendata login`; do not represent the absence of a feed as proof that no
  disruption exists.

Alert matching must use the alert's typed informed-entity selectors in this
order: exact feed-local trip, exact static stop/route ID within the selected
mode, then a route/stop name only when it is unique. Alerts with no recognized
selector remain network-level alerts rather than being attached to every
route. Active-period filtering uses `Australia/Melbourne` for presentation but
compares instants in UTC.

**v3 enrichment** (`enrichDisruptionsWithV3`):
- `client.DisruptionsAll(ctx, routeTypes)` → fetch bus and V/Line disruptions.
- Normalize to `unifiedDisruption` shape with `source: "v3"`.
- Merge into the result under their respective mode keys using `mergeKey` for route matching.
- Stderr: "bus and V/Line disruptions from PTV API; metro and tram from Open Data".
- Update `data_source` to `"opendata_alerts+v3_bus_vline"`.

### Phase 6: New Commands

#### 6a. `ptv timetable` (`cmd/timetable.go` — new file)

```
ptv timetable <stop-id|name> [--route <route>] [--mode <mode>] [--date YYYY-MM-DD] [--limit N]
```

- Resolves stop from GTFS via `resolveStopFromGTFS`.
- `store.StopDepartures(ctx, stopID, date, routeID, feedModes, limit)`.
- Formats times from service-day seconds using `localtime.ServiceDayAnchor`.
- Table: TIME, ROUTE, TOWARDS, TRIP_ID.
- JSON: array of departure objects.
- **v3 enrichment**: when v3 + Open Data both present, overlay real-time estimated times.
- `date` defaults to the current Melbourne calendar date. The output carries
  both the service date and the rendered local timestamp so trips after
  midnight are not mistaken for the next calendar day's service.

#### 6b. `ptv trip` (`cmd/trip.go` — new file)

```
ptv trip <trip-id> [--date YYYY-MM-DD]
```

- `store.TripDetail(ctx, tripID, date)`.
- Shows trip metadata (headsign, direction, block, service).
- Table of stops: SEQ, STOP, ARR, DEP.
- **v3 enrichment**: none needed.

#### 6c. `ptv track` (`cmd/track.go` — new file)

```
ptv track <trip-id> [--date YYYY-MM-DD]
```

- Loads static trip stopping pattern from GTFS.
- Fetches trip_update from Open Data GTFS-R for the matching mode feed.
- Merges: for each stop, show scheduled time, delay, estimated time.
- Vehicle position from Open Data vehicle-positions feed when available.
- Table: SEQ, STOP, SCHEDULED, ESTIMATED, DELAY, STATUS.
- **v3 enrichment**: none needed (Open Data trip updates are authoritative).
- A missing realtime match returns the static pattern with `realtime.state:
  "unknown"` and a warning. A matched cancellation or skipped stop is explicit;
  a missing stop-time update does not imply zero delay. The vehicle-position
  lookup is joined only through the validated trip/start-date context and its
  public label; the private GTFS-R vehicle ID is never emitted.

### Phase 7: JSON Contract Extensions

All migrated existing data commands include additive `data_source`,
`freshness`, and `warnings` fields at their existing JSON object boundary.
Administrative commands (`version`, `auth`, and GTFS lifecycle/status/catalog
commands) retain their purpose-specific JSON contracts unless separately
extended by this spec. New commands define their own top-level object/array
contract before implementation. `data_source`
contains `+`-delimited source tokens only; freshness state is never embedded in
that string.

```json
{
  "data_source": "gtfs_static+opendata_realtime+v3_platforms",
  "freshness": {
    "gtfs_static": { "state": "current" | "stale" | "changed" | "unknown", "age_hours": 42.3, "coverage": {"start": "20260701", "end": "20260801"} },
    "opendata_realtime": { "state": "current" | "stale" | "unknown", "age_seconds": 12 }
  },
  "warnings": ["..."],
  ...existing fields...
}
```

The allowed source tokens are `gtfs_static`, `opendata_realtime`,
`opendata_alerts`, `ptv_api_v3`, `v3_facilities`, `v3_platforms`,
`v3_outlets`, and `v3_bus_vline`. Tokens are ordered primary-to-enrichment and
must be unique. `ptv_api_v3` may appear alone only for v3-only commands
(`fare`, `outlets`). The `freshness` object contains only the components that
contributed; schedule-only output may include `gtfs_static` without an
`opendata_realtime` member.

### Phase 8: Data Freshness & Auto-Update (`internal/gtfs/autoupdate.go` — new file)

#### GTFS static auto-update

New file `internal/gtfs/autoupdate.go` provides a non-blocking auto-update mechanism triggered by freshness checks.

```go
// AutoUpdateConfig controls auto-update behavior.
type AutoUpdateConfig struct {
    Enabled       bool          // default true; false = --no-update-check
    BlockOnEmpty  bool          // block when no GTFS data exists (first run)
    BlockOnGap    bool          // block when requested date is outside coverage
    BlockTimeout  time.Duration // max wait for blocking updates (default 300s; env PTV_GTFS_UPDATE_TIMEOUT)
    DataDir       string
    SourceURL     string
    RequestedDate *time.Time    // optional date that may trigger BlockOnGap
}

// AutoUpdateResult reports what happened.
type AutoUpdateResult struct {
    Triggered   bool   // whether an update was triggered
    Background  bool   // true = running in background, false = completed inline
    State       string // "current", "updating", "failed", "no_update_needed"
    Message     string // human-readable status for stderr
}

// CheckAndAutoUpdate evaluates freshness and triggers an update if needed.
// Returns immediately with the current store; a worker process may still be running.
func CheckAndAutoUpdate(ctx context.Context, cfg AutoUpdateConfig) (*gtfs.Store, AutoUpdateResult, error)
```

**Implementation:**

1. **Freshness check**: reuse existing `CheckFreshness` with `AllowNetwork: true`. This already handles the 24-hour success throttle and exponential failure backoff via the freshness state DB.

2. **Trigger condition**: auto-update triggers when:
   - `FreshnessChanged` (upstream validators differ from local)
   - `FreshnessStale` AND `UpdateAvailable` (age exceeds threshold AND upstream has new data)
   - `CoverageOutsideError` on a query (requested date outside local coverage)

3. **Non-blocking execution**: when triggered on a normal command (data exists,
   not a coverage gap), launch the current executable as a hidden
   `gtfs update --background-worker` command with the validated data directory
   and source URL. The worker owns its own five-minute timeout, writes the
   progress record, and acquires the existing SQLite update lease. The parent
   command returns the current (stale) store immediately and prints the
   background-update message. A detached goroutine is forbidden because a CLI
   process may exit as soon as `RunE` returns.

4. **Blocking execution**: when no GTFS data exists or the query date is outside coverage:
   - Run the update inline with the command's context.
   - **Graduated timeout**: `BlockTimeout` defaults to 300s (5 minutes) — the GTFS feed is ~200MB and ingest takes 1–3 minutes on a good connection. The timeout is configurable via `PTV_GTFS_UPDATE_TIMEOUT` env var.
   - If the timeout fires, the partial download is discarded and the command returns an actionable error: "GTFS update timed out after 5m; retry with 'ptv gtfs update' or increase PTV_GTFS_UPDATE_TIMEOUT".
   - On success, re-open the store and retry the query.
   - On failure (non-timeout), the error is persisted in the freshness state DB with backoff, and the command returns the error with guidance.

5. **Update lease**: call `GenerationManager.AcquireUpdate`. It returns
   `ErrUpdateInProgress` without waiting when another process owns the SQLite
   lease; the caller keeps using the current generation. The OS releases the
   lease if a worker exits, so no timestamp-based stale-lock deletion is
   permitted.

6. **Progress file**: when an auto-update runs (background or blocking), it writes progress to `{dataDir}/.update.progress.json`:
   ```json
   {"state": "downloading", "percent": 42, "started_at": "...", "source_url": "..."}
   {"state": "ingesting", "started_at": "..."}
   {"state": "completed", "completed_at": "...", "generation_id": "..."}
   {"state": "failed", "error": "...", "failed_at": "..."}
   ```
   This file is written atomically (write to `.tmp`, rename). `ptv gtfs status` reads it to show "update in progress (42% downloaded)" or "update failed 3 minutes ago: network timeout". The progress file is deleted after a successful completion that is more than 1 hour old.

7. **Update procedure**: identical to `ptv gtfs update` — calls `Download` then
   `IngestGeneration` then `Publish`. Progress messages go to stderr only when
   the update is blocking (inline). Background workers write to the progress
   file but do not write command JSON or stdout. Worker startup and completion
   are recorded with PID, generation ID, and sanitized error text.

8. **Post-update**: the next command invocation opens the new generation. No hot-swap of the in-memory store — generations are immutable and the store is opened read-only per command.

#### Integration with command dispatch

`resolveSources` in `cmd/resolve.go` incorporates the auto-update check:

```go
func resolveSources(ctx context.Context, cfg *config.RuntimeConfig, opts resolveOptions) (*resolvedSources, error) {
    // Step 1: Open GTFS with auto-update.
    store, updateResult, err := gtfs.CheckAndAutoUpdate(ctx, gtfs.AutoUpdateConfig{
        Enabled:   !opts.NoUpdateCheck,
        DataDir:   cfg.DataDir,
        SourceURL: cfg.GTFSURL,
        RequestedDate: opts.RequestedDate,
    })
    if updateResult.Message != "" {
        fmt.Fprintln(os.Stderr, updateResult.Message)
    }
    // ... rest of resolution (Open Data, v3)
}
```

`resolveOptions` carries `NoUpdateCheck`, the requested service date (when a
command has one), and the explicit env-file selection used for credential
resolution. The auto-update policy must be passed explicitly; it must not read a
mutable package-global flag from a library path.

#### GTFS-R snapshot cache (per-invocation)

Within a single command invocation, multiple GTFS-R feed fetches for the same feed ID reuse one snapshot. This is implemented as a simple map passed through the command context:

```go
// internal/gtfsrt/cache.go — new file

// InvocationCache caches GTFS-R snapshots for the lifetime of one command.
type InvocationCache struct {
    mu        sync.Mutex
    snapshots map[string]*Snapshot // keyed by feed ID
    inFlight  map[string]*cacheFetch
}

type cacheFetch struct {
    done chan struct{}
    snap *Snapshot
    err  error
}

func (c *InvocationCache) GetOrFetch(ctx context.Context, client *Client, feed Feed) (*Snapshot, error) {
    c.mu.Lock()
    if snap, ok := c.snapshots[feed.ID]; ok {
        c.mu.Unlock()
        return snap, nil
    }
    if fetch, ok := c.inFlight[feed.ID]; ok {
        c.mu.Unlock()
        select {
        case <-fetch.done:
            return fetch.snap, fetch.err
        case <-ctx.Done():
            return nil, ctx.Err()
        }
    }
    fetch := &cacheFetch{done: make(chan struct{})}
    c.inFlight[feed.ID] = fetch
    c.mu.Unlock()
    snap, err := client.FetchSnapshot(ctx, feed)
    c.mu.Lock()
    fetch.snap, fetch.err = snap, err
    if err == nil {
        c.snapshots[feed.ID] = snap
    }
    delete(c.inFlight, feed.ID)
    close(fetch.done)
    c.mu.Unlock()
    return snap, err
}
```

The constructor initializes both maps. Concurrent callers for the same feed
share one request; a failed request is not cached, but all waiters receive the
same error. This prevents the check-then-fetch race in a plain mutex/map
implementation.

The cache is created at the start of each command's `RunE` and injected via context. It is never persisted across invocations — GTFS-R data is real-time by nature.

#### Online-first fallback for GTFS-R commands

Commands that depend on GTFS-R (`next`, `track`, `disruptions`) follow this connectivity pattern:

```go
func runNextPrimary(ctx context.Context, store *gtfs.Store, cache *gtfsrt.InvocationCache, ...) {
    // Step 1: Always load scheduled departures from GTFS (local, instant).
    scheduled, err := store.StopDepartures(ctx, stopID, now, ...)

    // Step 2: Attempt GTFS-R enrichment (online).
    if openDataKey != "" {
        snapshot, err := cache.GetOrFetch(ctx, client, feed)
        if err != nil {
            // Network failure or auth error — serve schedule-only with warning.
            fmt.Fprintln(os.Stderr, "real-time data unavailable: "+err.Error())
            return buildScheduleOnlyResult(scheduled), nil
        }
        // Merge real-time delays into scheduled departures.
        return mergeRealtime(scheduled, snapshot), nil
    }

    // Step 3: No Open Data credentials — serve schedule-only.
    return buildScheduleOnlyResult(scheduled), nil
}
```

## Backward Compatibility

Existing scripts and agents parse the current JSON output from every command. The migration changes the data source and adds new fields but must not break existing consumers.

**Additive changes (safe, no breaking change):**
- New `data_source` field added to all migrated data-command JSON outputs.
- New `freshness` object added to all migrated data-command JSON outputs.
- New `warnings` array added to commands that don't already have one.
- New fields on existing types (e.g. `gtfs_stop_id`, `gtfs_trip_id` alongside existing `stop_id`, `run_ref`).

**Semantic changes (documented, non-breaking):**
- `stop_id`, `route_id`, and `run_ref` retain their existing types and values
  when v3 is the source. When GTFS is primary, existing fields that can carry
  the namespaced GTFS value are preserved and parallel
  `ptv_stop_id`/`ptv_route_id`/`ptv_run_ref` fields identify the v3 namespace
  when present. A field whose old type cannot represent a namespaced GTFS ID
  must not silently change type; it gets a new string field instead.
- Existing `status` fields are never replaced or removed. The additive
  `freshness` object is the canonical source/freshness signal for GTFS/Open
  Data, while `status` remains the legacy v3/API health object when that command
  already exposed it. New commands define whether a status object is present.
- Disruption IDs: when sourced from Open Data alerts, `disruption_id` is the
  alert entity ID (string), not a numeric PTV ID. The existing
  `ptv_disruption_id` field is null for Open Data-sourced disruptions.

**Breaking changes (none planned):**
- No existing JSON field is removed.
- No existing field type changes (string stays string, int stays int).
- No existing command is removed or renamed.

**Migration guide for script consumers:**
- Check `data_source` to determine which fields are populated.
- Prefer `ptv_stop_id`/`ptv_route_id` when present (v3 namespace); fall back to `stop_id`/`route_id` (GTFS namespace).
- Prefer `freshness.*.state` and `data_source` checks for source-aware behavior;
  existing `status.health` checks remain valid for legacy v3 command fields.

**`ptv plan` is explicitly excluded from this migration.** It already uses GTFS as its primary data source (CSA journey planner on local SQLite) and opens the generation store directly via `GenerationManager.OpenCurrent`, bypassing `loadClient()` and `resolveSources` entirely. Its optional v3 disruption overlay (`plan_disruptions.go`) uses `loadClient()` independently and is unaffected by the dispatch refactor. The `plan` command's JSON output, freshness check, and disruption annotation logic remain unchanged. Future work may optionally add Open Data GTFS-R alert overlays to `plan`, but this is out of scope for this migration.

## Dependencies

- **No new Go module dependencies** for GTFS queries (existing `modernc.org/sqlite`).
- `golang.org/x/time/rate` for GTFS-R rate limiting (add to `go.mod`).
- GTFS-R trip update and alert decoding uses the existing `github.com/MobilityData/gtfs-realtime-bindings` protobuf package.

## Database Schema Changes

No schema changes to the GTFS generation database (schema v2). All new queries use existing tables and indexes. The existing indexes (`idx_stops_name`, `idx_stop_times_stop`, `idx_trips_route`, `idx_connections_forward`) support the new queries efficiently.

The auto-update system uses the existing generation manager
(`GenerationManager`) and freshness state DB (`.freshness.sqlite`) without
schema changes. It uses the existing SQLite-backed update lease
(`.update-lock.sqlite`); no second `.update.lock` or stale-lock deletion
protocol is introduced.

## Testing Strategy

### Unit tests
- `internal/gtfs/queries_test.go`: table-driven tests for each query function
  using the repository's existing in-memory/foundation-store fixture helpers;
  do not assume a checked-in `internal/gtfs/testdata/` directory. StopSearch
  tests must cover token matching ("Flinders Street" → "Flinders Street
  Station"), multi-token AND logic, prefix bonus ranking, and substring
  fallback. Query tests must also prove namespaced-ID resolution, integer-key
  joins, parent-station/platform behavior, active calendar exceptions, and
  service-day times beyond midnight.
- `internal/gtfsrt/snapshot_test.go`: extend existing tests to cover trip update and alert normalization.
- `internal/gtfs/autoupdate_test.go`: test trigger conditions (stale, changed,
  coverage gap), the existing SQLite update lease and `ErrUpdateInProgress`,
  blocking vs worker-process modes, progress file writes, worker failure
  persistence, and immutable-generation reopening.
- `internal/gtfsrt/cache_test.go`: test invocation cache deduplication,
  same-feed concurrent callers, failed-request non-caching, and context
  cancellation.

### Contract tests
- `cmd/timetable_contract_test.go`: verify timetable command JSON shape, `data_source`, and `freshness` fields.
- `cmd/trip_contract_test.go`: verify trip command JSON shape.
- `cmd/next_primary_contract_test.go`: verify GTFS+GTFS-R departure output shape.
- `cmd/next_v3_enrichment_contract_test.go`: verify v3 enrichment merges platform numbers without replacing the primary result.
- `cmd/disruptions_opendata_contract_test.go`: verify disruptions sources from Open Data alerts and v3 merge correctly.
- Golden/contract fixtures must verify that legacy `status` fields remain,
  namespaced GTFS IDs do not change old JSON field types, source tokens are
  deterministic, stale/degraded warnings stay off stdout, and private GTFS-R
  vehicle IDs are absent.

### Pre-flight validation
- Phase 0 validation runs against live Open Data trip-updates feed and reports trip_id match rate.
- Validation is re-runnable via `ptv gtfs validate-realtime` or test script after each GTFS feed update.

### Real-user search validation
- A corpus of 40+ real stop/station names (including abbreviations, partial matches, and common CLI inputs) is tested against the GTFS `StopSearch` function.
- Results are compared against v3 `Search` output for the same inputs to measure recall and ranking quality.
- Any input where GTFS search returns zero results but v3 returns matches is investigated and either fixed (search improvement) or documented (known limitation).
- This validation runs as a manual integration test during MVP development and is retained as a regression test in `cmd/search_quality_test.go`.

### Integration verification
- `ptv gtfs update` + `ptv search/lines/timetable/trip` all work without any credentials.
- `ptv search "Flinders Street"` returns correct stops via token matching.
- `ptv search "Flndrs"` returns empty with actionable error (tests substring fallback).
- `ptv next <stop>` with Open Data KeyID produces real-time departures.
- `ptv next <stop>` with both Open Data + v3 produces real-time departures with platform numbers.
- `ptv disruptions` shows metro/tram from Open Data and bus/V/Line from v3.
- Stale GTFS data triggers the post-MVP worker; `ptv gtfs status` shows progress; next invocation uses new data. MVP only verifies the explicit `ptv gtfs update` path and freshness reporting.
- GTFS-R commands degrade to schedule-only when Open Data is unreachable.
- `ptv disruptions` degrades to an explicitly warned empty result when no alert feed is available.
- `ptv timetable` and `ptv trip` default dates and service-day rollover are
  verified in Melbourne time; `ptv track` shows unknown realtime state for an
  unmatched static trip rather than fabricating estimates.
- All existing `go test ./...` continues to pass.

## Rollout Order

The rollout is split into an **MVP milestone** (credential-free static data) and a **real-time milestone** (gated by Phase 0 validation).

### MVP milestone — ship first

Ship library code and static-data commands. No real-time dependency.

1. GTFS query layer (Phase 1) — library code, no user-visible changes.
2. GTFS resolution helpers (Phase 4) + dispatch (Phase 3) + merge key normalization — infrastructure for all command migrations.
3. `ptv search` primary — validates token-based stop resolution against real user inputs.
4. `ptv lines` / `ptv lines show` primary.
5. `ptv stops on` / `ptv stops near` primary.
6. `ptv station` primary.
7. `ptv timetable` new command.
8. `ptv trip` new command.
9. JSON contract extensions (Phase 7) — `data_source`, `freshness`, and
   `warnings` on all migrated data commands; administrative contracts remain
   purpose-specific.
10. Verify `ptv plan` is unaffected by dispatch refactor (run existing plan contract tests + manual smoke test).

**MVP is shippable as a single release.** All MVP commands work without any
credentials. Existing commands that have not yet migrated retain their current
v3 behavior; migrated MVP commands do not claim v3 enrichment until the
enrichment milestone, and their additive source/freshness contracts remain
valid without it.

### Post-MVP — auto-update

11. GTFS auto-update (Phase 8) — integrated into `resolveSources`. User already has `ptv gtfs update`; this is quality-of-life, not a blocker.

### Real-time milestone — gated by Phase 0

After MVP ships, run Phase 0 pre-flight validation against live Open Data trip-updates:

12. **Phase 0 pre-flight validation** — validate trip_id join. Document match rate and chosen strategy.
13. GTFS-R trip update + alert decoding (Phase 2) + invocation cache + rate limiter.
14. `ptv next` primary (GTFS + Open Data real-time) — uses validated join strategy.
15. `ptv disruptions` primary (Open Data alerts for metro/tram + v3 for bus/V/Line).
16. `ptv track` new command — uses validated join strategy.

### Enrichment milestone — last

17. v3 enrichment for `station`, `next`, `disruptions`, `search` — can ship in any order after primary is stable.

## Risks

| Risk | Mitigation |
|------|-----------|
| GTFS-R trip_id does not match static GTFS trip_id | **Phase 0 pre-flight validation** against live data before building real-time join; alternative join strategies documented (route_id + start_time + direction_id) |
| GTFS stop search quality worse than v3 fuzzy search | Token-based AND matching with prefix bonus and length ranking; substring fallback for partial inputs; tested against common CLI inputs |
| GTFS stop search performance on multi-token queries | ~30k rows is well within SQLite capability for ANDed LIKE clauses; no FTS needed |
| Open Data rate limit (24/60s) hit during `ptv next` | Rate limiter in client; invocation cache avoids redundant fetches within one command |
| Service alerts only for metro/tram on Open Data | v3 enrichment fills bus/V/Line when credentials present; clear stderr when absent |
| GTFS coverage window is bounded | Auto-update on coverage gap; `CoverageOutsideError` with actionable messages |
| v3 enrichment merge mismatches (stop IDs, route IDs differ between namespaces) | Merge key normalization (`mergeKey`/`mergeKeys`) strips common suffixes, collapses whitespace, prepends route numbers; unmatched entries logged at debug level and surfaced in `warnings` array |
| Disruptions schema asymmetry (Open Data alerts vs v3 disruptions) | Unified `unifiedDisruption` output schema preserves both source fields with explicit `source` tag and source-prefixed `id` to prevent collisions |
| Commands slower from GTFS than v3 (full table scan vs indexed API) | GTFS is local SQLite — should be faster than network calls. Benchmark if concerns arise |
| Auto-update downloads ~200MB over metered connection | Stderr warning before blocking updates; `--no-update-check` opt-out; background updates are cancellable |
| Abandoned update worker blocks legitimate updates | Existing SQLite lease is released by process exit; callers handle `ErrUpdateInProgress` without timestamp-based lock deletion |
| Background auto-update progress invisible to user | Progress file (`.update.progress.json`) written during update; `ptv gtfs status` reads and displays it |
| Background auto-update race with concurrent commands | Existing SQLite update lease prevents concurrent downloads; generation publish is atomic and readers use immutable generations |
| GTFS-R snapshot served from cache is stale within a command | Cache lifetime is one command invocation (~seconds); upstream cache window is 30s |
