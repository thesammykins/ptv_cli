# PRODUCT.md: Open Data First — GTFS and Open Data as the Primary Platform

## Problem

The `ptv` CLI currently treats the PTV Timetable API v3 (HMAC-SHA1 devid/key) as its primary data source. This creates a barrier: v3 credentials require an application process, take time to obtain, and carry rate-limit and signing complexity. Meanwhile, the CLI already ingests a comprehensive local GTFS static feed and has a working Transport Victoria Open Data GTFS Realtime client (KeyID header only, easier to obtain), but these are used only for journey planning and vehicle enrichment.

The two APIs are fundamentally different in access model and data shape. The v3 API should not be the default path that GTFS/Open Data "falls back" from. Instead, local GTFS and Open Data should be the **primary platform** that every command uses by default, with v3 providing **optional supplementary enrichment** for the handful of fields that only it carries.

## Goals

1. **GTFS static and Open Data are first-class citizens.** Every command whose core data can be served from local GTFS or Open Data uses those sources as the primary path, regardless of whether v3 credentials are configured.
2. **v3 is a supplementary enrichment layer.** When v3 credentials are present, commands merge in v3-only fields (amenities, staffing, platform numbers, fares, outlets, bus/VLine disruptions) on top of the GTFS/Open Data result.
3. **The CLI works out of the box after `ptv gtfs update`.** No credentials of any kind are required for stop search, route listing, timetable queries, trip detail, stop detail, nearby stops, or stops-on-route.
4. **Open Data (KeyID) unlocks real-time.** Trip updates and vehicle positions come from the Open Data GTFS-R feeds. Service alerts are available for the catalogued metro and tram feeds only; occupancy is reported only when the selected feed actually provides it. The product must not invent alerts or occupancy for modes/feed entities where the upstream feed omits them.
5. **Active migration, not coexistence.** Where Open Data can serve the same data as v3 (departures, disruptions, vehicle positions), the command uses Open Data as the source and v3 enriches. The v3-only path is retained only for capabilities that genuinely have no alternative.

## Data Source Architecture

### Primary: Local GTFS Static (no credentials)

The local GTFS generation database is the **authoritative source** for:

| Capability | GTFS tables |
|-----------|-------------|
| Stop search (name, substring) | `stops` |
| Nearby stops (Haversine) | `stops` (lat/lon) |
| Route listing | `routes` |
| Route detail + directions | `routes`, `trips` (headsign, direction_id), `stop_times` |
| Stops on a route | `stop_times` → `stops` |
| Scheduled timetable for a stop | `connections`, `trips`, `routes`, `calendar`, `calendar_dates` |
| Trip stopping pattern | `stop_times` → `stops`, `trips` |
| Stop detail (coordinates, parent station, wheelchair boarding, transfers, pathways) | `stops`, `transfers`, `pathways`, `levels` |
| Routes serving a stop | `stop_times` → `trips` → `routes` |
| Service calendar and coverage | `calendar`, `calendar_dates`, `dataset_state` |

### Primary: Open Data GTFS Realtime (KeyID only)

Open Data GTFS-R feeds are the **authoritative source** for real-time data:

| Capability | Feed(s) |
|-----------|---------|
| Real-time departures (delays, estimated times) | Trip updates: metro, tram, bus, vline |
| Live vehicle positions (lat/lon, speed, bearing) | Vehicle positions: metro, tram, bus, vline |
| Service alerts / disruptions | Service alerts: metro, tram |
| Vehicle occupancy (when present) | Vehicle positions: metro, tram, bus, vline |
| Trip cancellation / skip detection | Trip updates (schedule_relationship) |
| Vehicle lookup by public label | Vehicle positions (existing) |

### Supplementary: PTV Timetable API v3 (HMAC devid/key)

v3 provides **enrichment fields only** — data that GTFS and Open Data do not carry:

| Enrichment | v3 endpoint | Merged into |
|-----------|------------|-------------|
| Station amenities (toilet, taxi rank, parking, CCTV) | `/v3/stops/{id}/route_type/{type}` | `ptv station` |
| Station accessibility detail (escalator, hearing loop, lift, tactile, wheelchair facilities) | same | `ptv station` |
| Station staffing hours | same | `ptv station` |
| Platform numbers | `/v3/departures/...` | `ptv next`, `ptv timetable` |
| Myki fare estimates | `/v3/fare_estimate/...` | `ptv fare` (v3-only command) |
| Myki outlet locations | `/v3/outlets` | `ptv search`, `ptv outlets` (v3-only commands) |
| Bus/VLine disruption detail | `/v3/disruptions` | `ptv disruptions` (metro/tram from Open Data; bus/VLine from v3) |
| Run references and stopping patterns | `/v3/runs/...`, `/v3/pattern/...` | `ptv vehicle` (v3-specific entity namespace) |

## Command Specifications

### Existing commands — migrated to primary GTFS/Open Data

#### `ptv search <term>`
- **Primary**: local GTFS `stops` table + `routes` table.
- **v3 enrichment**: when v3 credentials are present, append v3 outlet results to the existing `outlets` collection. Outlets are a distinct entity type: they are never deduplicated against stops or routes, and no outlet ID is used as a stop/route ID.
- Stderr when v3 absent: nothing (this is the default path now). Stderr when v3 present: "merged outlet results from PTV API".
- JSON `data_source`: `"gtfs_static"` or `"gtfs_static+v3_outlets"`.

#### `ptv lines [--mode ...]`
- **Primary**: local GTFS `routes` table, grouped by feed_mode.
- **v3 enrichment**: none needed (GTFS routes are complete).
- If v3 credentials are present, do not make a v3 call — GTFS is authoritative.

#### `ptv lines show <route>`
- **Primary**: local GTFS `routes` + `trips` (directions from headsigns) + `stop_times` → `stops`.
- **v3 enrichment**: none needed.

#### `ptv stops on <route>`
- **Primary**: local GTFS `stop_times` → `stops`, ordered by stop_sequence.
- **v3 enrichment**: none needed.

#### `ptv stops near <lat,lng|place>`
- **Primary**: local GTFS `stops` table with Haversine distance.
- **v3 enrichment**: none needed (GTFS coordinates are authoritative).

#### `ptv station <stop>`
- **Primary**: local GTFS `stops` (coordinates, parent station, wheelchair_boarding) + routes serving the stop + transfers + pathways.
- **v3 enrichment**: when v3 credentials are present, fetch `StopDetails` and merge amenities, accessibility detail, staffing hours, and disruption references into the result.
- Stderr when v3 present and amenities found: "station facilities enriched from PTV API".
- JSON `data_source`: `"gtfs_static"` or `"gtfs_static+v3_facilities"`.

#### `ptv next <stop>`
- **Primary**: local GTFS scheduled departures + Open Data GTFS-R trip updates (delays applied to schedule).
- **v3 enrichment**: when v3 credentials are present, merge platform numbers and disruption IDs from v3 departures.
- Stderr when v3 present: "platform numbers enriched from PTV API".
- JSON `data_source`: `"gtfs_static+opendata_realtime"` or `"gtfs_static+opendata_realtime+v3_platforms"`.

#### `ptv disruptions [--mode ...]`
- **Primary**: Open Data GTFS-R service alerts (metro train + tram).
- **v3 enrichment**: when v3 credentials are present, fetch v3 disruptions for bus and V/Line (which lack Open Data alert feeds) and merge.
- Stderr: "metro and tram disruptions from Open Data; bus and V/Line disruptions from PTV API" when both sources used.
- JSON `data_source`: `"opendata_alerts"` or `"opendata_alerts+v3_bus_vline"`.
- **This is the single disruptions command.** There is no separate `ptv alerts` command — the existing `disruptions` command becomes the unified entry point for all service alert data, sourcing from Open Data for metro/tram and v3 for bus/V/Line.
- Without an Open Data key, the command returns a successful empty/degraded result with a machine-readable warning and stderr guidance; it does not claim that no disruptions exist. Without v3, bus/V/Line enrichment is omitted and the result identifies that limitation.

#### `ptv vehicle <id>`
- **Primary**: Open Data GTFS-R vehicle positions (already partially implemented).
- **v3 enrichment**: when v3 credentials are present, search PTV departure data for vehicle_descriptor matches (existing behavior retained as enrichment).
- No change to current dispatch — already uses Open Data as primary with v3 as supplementary search.

### New commands

#### `ptv timetable <stop> [--route ...] [--mode ...] [--date YYYY-MM-DD] [--limit N]`
- **Primary**: local GTFS `connections` for active service instances on the target date.
- Shows: departure time, route label, headsign, trip_id, pickup/drop-off policy.
- Accept `--date` within GTFS coverage window.
- `--date` is interpreted in `Australia/Melbourne`; when omitted it defaults to the local calendar date at invocation time. GTFS service-day times at or after midnight rollover are rendered on the corresponding local date using the existing `ServiceDayAnchor` rules. A date outside coverage returns the existing actionable coverage error unless the post-MVP auto-update policy retries it.
- The command is scheduled-only unless Open Data credentials are configured. Real-time overlays are matched per feed using the Phase 0-validated identity and never replace unmatched scheduled rows.
- **v3 enrichment**: when v3 credentials are present and Open Data KeyID is also present, overlay real-time estimated times alongside scheduled times.
- This is the headline new capability: a full timetable queryable without any API credentials.

#### `ptv trip <trip-id> [--date YYYY-MM-DD]`
- **Primary**: local GTFS `stop_times` → `stops` for the trip, with trip metadata.
- Shows: every stop with arrival/departure times, headsign, direction, block, service.
- **v3 enrichment**: none needed (GTFS is authoritative for static trip patterns).
- The argument accepts either the namespaced GTFS `trip_id` or an unambiguous source trip ID within the selected mode/feed. If the source ID is ambiguous, the command fails with the candidate modes/IDs rather than choosing arbitrarily. `--date` selects the active service instance and defaults to the local calendar date.

#### `ptv track <trip-id> [--date YYYY-MM-DD]`
- **Primary**: local GTFS stopping pattern + Open Data GTFS-R trip update.
- Shows: each stop with scheduled time, delay, estimated time, schedule_relationship.
- Vehicle position from Open Data vehicle-positions feed when available.
- **v3 enrichment**: none needed (Open Data trip updates are the authoritative real-time source).
- If the static trip cannot be joined to a current update, the command still returns the static stopping pattern with `realtime.state` set to `unknown` and a warning. Canceled trips are represented as canceled; skipped stops are represented as skipped; added/unscheduled realtime trips without a static pattern are not fabricated into a `track` result.

## User Experience

### Default path — no credentials required
```
$ ptv gtfs update                         # one-time data ingest
$ ptv search "Flinders Street"            # local GTFS stops + routes
$ ptv lines --mode train                  # local GTFS routes
$ ptv lines show "Frankston"              # local GTFS directions + stops
$ ptv stops on "Frankston"                # local GTFS stop list
$ ptv stops near "-37.818,144.967"        # local GTFS spatial query
$ ptv station "Southern Cross"            # local GTFS stop detail + transfers
$ ptv timetable "Flinders Street"         # local GTFS scheduled departures
$ ptv trip "2:METRO-12345"               # local GTFS trip pattern
```

### Open Data — real-time with KeyID
```
$ ptv auth opendata login                 # one-time KeyID setup
$ ptv next "Flinders Street"              # GTFS schedule + GTFS-R delays
$ ptv disruptions --mode train            # GTFS-R service alerts
$ ptv track "2:METRO-12345"              # live trip tracking (new)
$ ptv vehicle 243M --stop Mordialloc      # GTFS-R vehicle positions
```

### v3 enrichment — optional supplementary detail
```
$ ptv auth login                          # v3 HMAC credentials (optional)
$ ptv station "Southern Cross"            # + amenities, accessibility, staffing
$ ptv next "Flinders Street"              # + platform numbers
$ ptv disruptions --mode bus              # + bus/VLine disruptions
$ ptv fare --min-zone 1 --max-zone 2     # v3-only (myki fares)
$ ptv outlets                             # v3-only (myki outlets)
```

## Data Freshness & Automatic Updates

The CLI has three classes of data with different freshness characteristics. Each is handled with an appropriate freshness strategy so users get correct data without manual intervention.

### GTFS Static Data — auto-update when stale

The local GTFS generation database has bounded service-date coverage (typically a 30-day rolling window, updated weekly upstream). The existing freshness system (`internal/gtfs/freshness.go`) already detects staleness via HEAD requests comparing ETag/Last-Modified validators, with a 24-hour check throttle and exponential failure backoff.

**Current behavior**: staleness is detected and reported as a stderr warning, but the user must manually run `ptv gtfs update` to refresh.

**New behavior (post-MVP)**: when a command detects that local GTFS data is stale or that the requested service date is outside coverage, and the upstream feed has changed, the CLI may launch a separate update worker for the next invocation. The worker uses the existing generation manager and SQLite update lease; it is not an untracked goroutine and it never mutates an open immutable generation.

Auto-update rules:
1. **Trigger**: any command that loads GTFS data checks freshness first. If `FreshnessChanged` or `FreshnessStale` is detected and the upstream validators differ, auto-update is triggered.
2. **Non-blocking**: when data exists and the query is within coverage, the command serves the current (possibly stale) result immediately while a separate update worker runs. A stderr message says "updating GTFS data in background; results may be stale until next invocation". If the worker cannot be launched, the command keeps serving the current result and records the failure for `ptv gtfs status`.
3. **Blocking on first run**: if no GTFS data exists at all (`ErrNoCurrentGeneration`), the command blocks and runs the full update inline (the user explicitly needs data to proceed).
4. **Coverage gap**: if the requested date is outside local coverage, auto-update is triggered and the command waits for it (up to a configurable timeout) before retrying. If the new generation still doesn't cover the date, an actionable error is returned.
5. **Opt-out**: `--no-update-check` suppresses both the freshness check and auto-update (existing flag).
6. **Concurrency**: only one auto-update runs at a time. Reuse `GenerationManager.AcquireUpdate` and its existing SQLite lease (`.update-lock.sqlite`); do not add a second lock-file protocol or delete stale lock files. If a second command triggers while an update is in progress, it uses the existing immutable generation and reports `updating`.
7. **Failure handling**: if auto-update fails (network error, invalid feed), the failure is persisted in the existing freshness state DB with backoff and in the progress record. The command continues with stale data. A stderr message reports the failure without exposing credentials or raw upstream response bodies.

### GTFS Realtime — online-first with short-lived cache

GTFS-R feeds (trip updates, vehicle positions, service alerts) are inherently real-time. The upstream refresh rate is 30–60 seconds.

**Online-first rule**: when an internet connection is available, GTFS-R data is always fetched live. The CLI never serves a cached GTFS-R snapshot older than the upstream cache window (30 seconds).

**Snapshot cache**: within a single command invocation, multiple lookups against the same feed reuse one fetched snapshot (already partially implemented for vehicle positions). A per-command-invocation cache keyed by feed ID avoids redundant upstream calls.

**Offline GTFS-R**: when there is no internet connection (or Open Data KeyID is misconfigured), `next` and `track` fall back to static GTFS schedule/pattern data with a stderr warning: "real-time data unavailable; showing scheduled times only". `disruptions` returns an empty/degraded result with a warning because static GTFS has no alert feed; it must not present a schedule as a disruption result.

**Freshness signals in output**: every GTFS-R-sourced result includes the observation freshness state (`current`, `stale`, `unknown`) and age. Stale observations (>90s) are flagged but still shown — a 2-minute-old vehicle position is more useful than nothing.

### Static Data Completeness — prefer online sources when available

When the CLI has both local GTFS data and online access, it should prefer the online source for data that changes frequently:

| Data type | Local GTFS | Open Data GTFS-R | Preference |
|-----------|-----------|-----------------|------------|
| Stop names, coordinates | Authoritative | Not in GTFS-R | Local GTFS |
| Route names, numbers | Authoritative | Referenced by route_id | Local GTFS |
| Scheduled times | Authoritative | Baseline for delay calc | Local GTFS |
| Real-time delays | Not available | Authoritative | Open Data GTFS-R |
| Vehicle positions | Not available | Authoritative | Open Data GTFS-R |
| Service alerts | Not available | Authoritative (metro/tram) | Open Data GTFS-R |
| Trip cancellations/skips | Not available | Authoritative | Open Data GTFS-R |
| Occupancy (when present) | Not available | Authoritative for provided observations | Open Data GTFS-R |
| Platform numbers | Not in GTFS | Not in GTFS-R | v3 only |

The principle: **static topology and schedules come from local GTFS; anything time-sensitive comes from the online API**. A command never serves static schedule data when real-time data for the same trip is available and fresh.

### Staleness Transparency

Every command that serves data reports its freshness state:

- **JSON output**: `data_source` identifies contributing source tokens only; freshness is never encoded into that string. A `freshness` object provides machine-readable state and age, e.g. `data_source: "gtfs_static+opendata_realtime"` with `freshness.gtfs_static.state: "stale"`.
- **Human output**: a single stderr line when data is stale or when auto-update is running. No noise when data is current.
- **`ptv gtfs status`**: remains the canonical command for inspecting local GTFS freshness, coverage, and upstream check state. Post-MVP it may additionally show:
  - "update in progress (42% downloaded)" when a background auto-update is running
  - "update failed 3 minutes ago: network timeout" when the last auto-update failed
  - Last auto-update attempt and its result
  - Update-lease/progress age when a concurrent update is observable

## Invariants

1. **GTFS/Open Data first.** Every migrated command checks local GTFS and Open Data before considering v3. The v3 API is never the sole data source for a command whose core data exists in GTFS or Open Data. The explicitly v3-only `fare` and `outlets` commands are the exception.
2. **v3 enriches, never replaces.** When v3 credentials are present and a command has a GTFS/Open Data primary result, v3 fields are merged in. The v3 result does not replace the primary result.
3. **No hard failures on missing v3.** No command fails because v3 credentials are absent. Commands fail only when the primary data source (GTFS or Open Data) is unavailable.
4. **Namespaced identifiers.** GTFS trip_ids, stop_ids, and route_ids are namespaced (`{feedMode}:{id}`) and never confused with PTV v3 numeric IDs.
5. **GTFS-R joins by feed-local static trip identity plus `start_date`**, not by PTV `run_ref`. The exact namespace derivation and observed match rate must be recorded by the Phase 0 validation; production code must use the validated strategy and must not silently fall back to a different namespace.
6. **Rate limit compliance.** Open Data GTFS-R feeds enforce 24 calls/60s per endpoint. The client respects this.
7. **Melbourne timezone.** All user-facing times are in `Australia/Melbourne`.
8. **Transparent sourcing.** Every migrated data command's JSON output includes a `data_source` field identifying which sources contributed. Administrative commands (`version`, `auth`, and GTFS lifecycle/status/catalog commands) retain their existing purpose-specific contracts. Stderr notes v3 enrichment when it occurs.
9. **Auto-update is non-blocking after MVP.** Stale GTFS data is served immediately; a separately launched update worker may refresh the next invocation. The command must not rely on a detached goroutine surviving process exit. The user is forced to wait only when no data exists or a requested date is outside coverage and the update is explicitly being retried inline.
10. **Online-first for real-time.** GTFS-R data is fetched live when connectivity and credentials are available. Cached snapshots are used only within a single command invocation; the cache is not a cross-process or cross-invocation source of truth.
11. **Stale data is never silent.** When a command serves stale GTFS data or degraded (schedule-only) results, the user is told via stderr and the JSON freshness field.

## Minimum Viable Migration (MVP)

The full spec describes 11+ migrated commands, 3 new commands, and 8 implementation phases. To reduce delivery risk, the work is split into an MVP milestone that ships first and validates the core assumptions before the real-time commands are built.

**MVP scope (ship first):**
1. GTFS query layer (`internal/gtfs/queries.go`) — all query functions.
2. GTFS resolution helpers (`resolveStopFromGTFS`, `resolveRouteFromGTFS`) — with token-based search.
3. `ptv search` — validates stop search quality against real user inputs.
4. `ptv lines` / `ptv lines show` — validates route listing.
5. `ptv stops on` / `ptv stops near` — validates spatial queries.
6. `ptv station` — validates stop detail + transfers.
7. `ptv timetable` — validates scheduled departure queries (the headline new capability).
8. `ptv trip` — validates trip stopping pattern.
9. Credential dispatch (`resolveSources`) — GTFS/Open Data primary, with an
   optional v3 capability boundary; migrated-command enrichment is deferred to
   the final enrichment milestone.
10. JSON contract extensions (`data_source`, `freshness`, `warnings`).

**Deferred to post-MVP:**
- **Auto-update**: the user already has `ptv gtfs update` and `ptv gtfs check`. Auto-update is a quality-of-life improvement, not a blocker. Shipping it after MVP reduces surface area and lets the freshness system stabilize under real usage first.
- **`ptv plan`**: already uses GTFS as primary and does not participate in the dispatch refactor. Its v3 disruption overlay (`plan_disruptions.go`) uses `loadClient()` independently and is unaffected by `resolveSources`. `plan` is explicitly excluded from this migration — see TECH.md Backward Compatibility.

**MVP exit criteria:**
- All MVP commands work without v3 credentials.
- `ptv search` passes real-user input testing (see Success Criteria).
- Existing `go test ./...` passes.
- JSON contracts include `data_source` and `freshness` fields.
- `ptv plan` is verified unaffected by the dispatch refactor.
- MVP does not claim automatic GTFS updates; `ptv gtfs update` remains the explicit refresh path until the post-MVP auto-update milestone is complete.

**Post-MVP (gated by Phase 0 trip_id validation):**
- Phase 0 pre-flight validation runs against live Open Data trip-updates.
- If trip_id join works: proceed with `ptv next` (real-time), `ptv track`, `ptv disruptions` (Open Data alerts).
- If trip_id join fails: real-time commands use the fallback join strategy (route_id + start_time + direction_id) or are deferred until a workable join is found.
- v3 enrichment for all commands ships last.

This split ensures the CLI delivers immediate user value (credential-free transit data) before investing in the higher-risk real-time path.

## Success Criteria

- All existing commands (`search`, `lines`, `lines show`, `stops on`, `stops near`, `station`, `next`, `disruptions`, `vehicle`) use GTFS/Open Data as primary source.
- New commands (`timetable`, `trip`, `track`) work with GTFS/Open Data only.
- `ptv disruptions` sources metro/tram alerts from Open Data and bus/V/Line from v3.
- v3 enrichment merges supplementary fields when v3 credentials are present.
- Zero commands fail due to missing v3 credentials when GTFS data is ingested.
- `ptv fare` and `ptv outlets` remain v3-only (no GTFS alternative exists).
- After the post-MVP auto-update milestone, stale GTFS data triggers a separate background update worker without user intervention; MVP only reports staleness and preserves the explicit `ptv gtfs update` path.
- GTFS-R data is fetched live (not served from stale cache) when connectivity exists.
- All migrated data commands report data freshness in JSON output and stderr;
  administrative commands retain their purpose-specific contracts.
- `ptv search` tested against a corpus of real user inputs (station names, abbreviations, partial matches, typos) and produces results comparable to v3 search quality. At minimum: "Flinders Street", "Southern Cross", "Melbourne Central", "Box Hill", "Camberwell", "Ringwood", "Dandenong", "Frankston", "Williamstown", "St Kilda", "North Melbourne", "Jolimont", "Parliament", "Flagstaff", "Victoria Park", "Richmond", "South Yarra", "Hawksburn", "Malvern", "Caulfield", "Carnegie", "Murrumbeena", "Hughesdale", "Clayton", "Notting Hill", "Glen Waverley", " Blackburn", "Laburnum", "Springvale", "Noble Park", "Yarraman", "Sandown Park", "Narre Warren", "Berwick", "Pakenham", "Cranbourne", "Merinda Park", "Lynbrook", "Hallam", "Dandenong", "Flinders Street Station", "Southern Cross Station", "Jolimont-MCG", "Victoria Park Station", "Clifton Hill Station", "Westgarth Station", "Northcote Station", "Croxton Station", "Thornbury Station", "Bell Station", "Preston Station", "Reservoir Station", "Ruthven Station", "Keon Park Station", "Thomastown Station", "Lalor Station", "Epping Station", "South Morang Station", "Mernda Station", "Hawkstowe Station", "Middle Gorge Station", "Wollert Station". Include at least 5 partial/abbreviated inputs.
- `go vet ./...` and `go test ./...` pass.

## Non-Goals

- Implementing fare estimation from GTFS (fares.txt is not in the PTV feed).
- Running a persistent background daemon for GTFS updates (auto-update is command-triggered, not cron-like).
- Road disruption APIs (unplanned/planned road disruptions).
- Bluetooth/freeway travel time APIs.
- Removing v3 support entirely — v3 remains the source for fares, outlets, and enrichment fields.
