# Audit remediation — technical specification

**Feature:** `audit-remediation`

**Status:** implemented and validated

**Last updated:** 16 July 2026

This specification implements [PRODUCT.md](PRODUCT.md) and consolidates the
approved technical requirements in [audit specs 001–004](../../docs/specs/).
Where this document is more specific, it records the implementation decision;
where implementation proves a decision wrong, update this file in the same
change as the code.

## Goals and constraints

1. Correctness precedes optimization. A faster planner that permits an invalid
   journey does not satisfy the feature.
2. Source contracts remain explicit at storage and domain boundaries. Do not
   infer equality between identifiers, freshness from absence, or transfer
   permission from proximity.
3. Published GTFS databases are immutable generations. Readers never observe
   a partly rebuilt schema or index set.
4. Optional network capabilities degrade independently from local planning.
5. Current-major JSON changes are additive except when removing an upstream
   internal identifier or correcting a field whose existing meaning is false.
6. Use the standard library and existing dependencies unless measurement shows
   a missing primitive.

## Target architecture

```text
GTFS ZIP -> staged generation -> validate/index/optimize -> atomic publish
                                |
                                v
                    service-window connection query
                                |
origin access -> transfer/pathway graph -> CSA + walking relaxation -> egress

PTV v3 --------> endpoint DTOs -----> normalized command DTOs
GTFS Realtime -> feed snapshot -----> freshness + typed identifiers

base runtime config -> optional PTV capability / Open Data capability
                    -> static GTFS / geocoder / local paths
```

## Parallel work lanes and ownership

Agents may work concurrently only within these boundaries:

| Lane | Exclusive implementation ownership | May add tests under |
| --- | --- | --- |
| A0 — store foundation | `internal/gtfs/{schema,store,download}.go`, new migration/generation/state helpers | `internal/gtfs/` store tests |
| A1 — GTFS compiler | `internal/model/`, `internal/gtfs/{ingest,timetable}.go`, new transfer/pathway/query helpers | `internal/gtfs/` ingest/timetable tests |
| A2 — router core | `internal/router/` after the model boundary is frozen | `internal/router/` |
| B — external clients | `internal/ptvapi/`, `internal/gtfsrt/` | those same packages |
| C — runtime services | `internal/config/`, `internal/geocode/`, `internal/gtfs/freshness.go` | those same packages |
| Integration — primary agent | `cmd/`, cross-lane adapters, root docs/specs | `cmd/`, integration and benchmark harnesses |

The following are serial choke points and must not be edited concurrently:

- Lane A0 freezes the versioned schema before Lane A1 changes ingest/timetable;
  A0 and A1 must not edit the same GTFS file concurrently.
- `cmd/plan.go`, `cmd/gtfs.go`, `cmd/station.go`, `cmd/vehicle.go`, `cmd/root.go`,
  and `cmd/helpers.go` are integrated only after their underlying packages land.
- `PRODUCT.md`, `TECH.md`, audit docs, README, and CONTRIBUTING are owned by the
  primary agent so shipped behavior and documentation remain synchronized.

Subagents must not commit, stage, move shared documentation, or rewrite files
outside their lane. Each handoff names files changed and focused checks run.

## Phase 0 — regression harness

Before changing behavior, add the smallest failing fixtures for the defect being
implemented. Tests use local ZIP/CSV fixtures, local HTTP handlers, injected
clients/clocks, and temporary directories; they never require live credentials.

Add benchmark surfaces for:

- GTFS service activation and connection loading;
- forward and reverse routing over a representative generated timetable; and
- a documented external production-snapshot harness with wall-time and peak-RSS
  capture.

Benchmark diagnostics are opt-in test/harness output. They include no paths
containing credentials, signed URLs, or query secrets and never alter command
stdout.

## Phase 1 — GTFS schema and ingest contract

### Versioned schema

Replace free-form schema/migrations with `PRAGMA user_version` migrations run in
a checked transaction. The new generation schema stores:

- stops: feed namespace, `location_type`, `parent_station`, level,
  `stop_access`, and relevant accessibility fields;
- stop times: pickup/drop-off policy alongside arrival/departure seconds;
- transfers: type plus nullable from/to stop, route, and trip specificity, with
  linked-trip templates retaining distinct from-stop and to-stop endpoints;
- pathways: direction, mode, traversal time/length, and endpoint stops;
- trips: feed-namespaced block identity and source identifiers; and
- elementary connections: service, trip, route, stop endpoints, time seconds,
  board/alight policy, feed, and block/link metadata.

Use integer primary keys for stops, routes, trips, services, and dense
query-local trip instances. Preserve source IDs in separate namespaced columns
for display and cross-source mapping.

The currently unversioned database cannot reconstruct fields that were never
ingested. Detect it as a legacy generation and return an actionable
“run `ptv gtfs update`” result for correctness-dependent planning. Do not
pretend an additive SQL migration restored missing source semantics.

### Validated ingest

- Recognize at least one supported inner feed.
- For each recognized feed require `stops.txt`, `routes.txt`, `trips.txt`, and
  `stop_times.txt`, plus `calendar.txt` or `calendar_dates.txt`.
- Validate required headers and non-zero core rows before publication.
- Ingest complete transfer specificity and transfer types 0–5. Type 4 requires
  in-seat continuation; type 5 prohibits it even if block metadata agrees.
- Ingest station hierarchy/pathways. Explicit pathways are authoritative inside
  their station component.
- At the Transport Victoria source adapter only, normalize its observed zero
  sentinel for optional pathway traversal/stair values to absent; retain signed
  non-zero values and use the conservative length/mode walking-time fallback.
- Generate proximity transfers only for eligible nodes outside authoritative
  pathway components. Explicit prohibitions override generated edges.
- Derive the spatial neighbor span from radius and latitude; every pair within
  the configured radius must reach the exact Haversine check.
- Spool inner ZIPs into unique private temporary files rather than materialize
  their full uncompressed contents in memory.

Build elementary connections once during ingest so query-time code does not
join and order the full `stop_times` table. Add indexes led by active query
predicates, including `(date, service_id)` for calendar exceptions and
service/time orderings for forward and reverse connections. Run
`PRAGMA optimize` after indexes are complete.

## Phase 2 — timetable and routing correctness

### Service instances and time

Introduce a compact service-instance identity containing feed, service date,
and trip ID. Connections from consecutive dates with the same raw trip string
must never share onboard reachability.

Construct a service-day anchor as `Australia/Melbourne` local noon minus twelve
elapsed hours, then add GTFS seconds. Query all service dates intersecting a
36-hour forward or backward window, including previous-day overflow and the
next service day.

### Transfer graph

Build a typed transfer graph that distinguishes:

- ordinary or minimum-time walking;
- forbidden transfers;
- same-stop minimum change time;
- directed station pathways; and
- in-seat required/prohibited trip links.

Apply GTFS transfer specificity in official precedence order. Expand
station-scoped rules to applicable children. The router carries board/alight
permission on connection boundaries and distinguishes staying onboard from a
new boarding or alighting action.

Pickup/drop-off values 2 and 3 are not automatically feasible: exclude them by
default and expose an explicit plan opt-in that marks the resulting leg as
conditional. Type-4 in-seat continuation is represented by distinct legs with
`stay_onboard: true` and no transfer increment; type 5 prohibits that state.

Router dominance state is keyed by stop plus relevant arrival trip/transfer
context. A chronologically earlier arrival that is barred by a specific transfer
rule must not suppress a later compatible arrival. Validate the optimized scan
against a small time-expanded Dijkstra oracle in property tests.

Replace one-hop footpath relaxation with a priority-queue/queue propagation of
improved walking labels and predecessor data. The implementation may use a
validated precomputed closure only if benchmarks show it is smaller/faster and
tests prove the required graph properties.

### Endpoints and journeys

Represent source and target connectors with stop index plus walking seconds.
Stop-name matches may have zero access cost; coordinates/geocoded places use
distance-derived access and egress durations and render walking legs where
meaningful.

Define a zero-leg journey explicitly: departure and arrival equal the request
instant and transfers are zero. Reconstruction failure before reaching a source
is an invariant error, not a partial journey.

Arrive-by uses a preordered reverse view or reverse query; it must not copy and
sort the entire day on every command.

## Phase 3 — immutable store publication and planner performance

### Store modes

Provide separate APIs for:

- opening an existing generation read-only;
- creating/migrating a staging generation for update; and
- atomically publishing a fully validated staging database.

Configure SQLite connection pragmas through URI-safe DSNs, with an explicit
busy timeout and command contexts. Canonicalize data paths, reject filesystem
roots and unexpected database symlinks, and use private unique temporary files.

### Publication

`ptv gtfs update` downloads and ingests into a uniquely named immutable sibling
generation. It
verifies required rows, foreign references used by planning, indexes,
`quick_check`/integrity, coverage bounds, counts, and schema version. After all
handles close, publish by atomically replacing a small `current` manifest that
names the generation. This avoids replacing an open SQLite filename on Windows.
On every failure retain the old manifest/generation and remove only owned
staging artifacts.

Use a lock scoped to the data directory for competing updates. Readers continue
using the old immutable generation until replacement and reopen normally on the
next command.

Persist counts, source URL, validators, publication/ingest times, and service
coverage as verified metadata; status must not count the full stop-time table.

### Query shape and budget

Load only stops/routes needed for resolution and connections for active service
instances inside the requested window. Use integer indexes/arrays in the hot
scan, maintain the best target incrementally, and validate timetable invariants
once when constructed.

First meet the budget with the precomputed connection table and bounded query.
Add a generation/date cache only if phase timings prove it is still necessary.
Acceptance remains the PRODUCT budget: median at most three seconds, peak RSS at
most 256 MiB, and at least 10× improvement on both audited cases.

## Phase 4 — PTV and GTFS Realtime clients

### HTTP foundation

Both clients receive injected `http.Client` support for tests, bounded response
readers, typed status/auth/rate-limit/not-found/decode errors, and a shared
context deadline. Retries are limited to idempotent, documented transient
conditions; `Retry-After` is bounded. Changing authentication headers is never
a retry for timeout, decode, 429, 5xx, or non-authentication 4xx failures.

### PTV v3

- Use endpoint-specific DTOs checked against captured current Swagger shapes.
- Correct Stop Details to nested location, amenities, accessibility, staffing,
  and disruption data. Remove compensating Search calls.
- Add the broad `/v3/runs/{run_ref}` endpoint and use multi-valued route types
  rather than serial per-mode calls.

### GTFS Realtime

Add auth metadata to each catalog `Feed`. Fetch a `Snapshot` containing feed
version, incrementality, header timestamp, fetch time, and normalized entities.
The current contract sends exactly one `KeyID` subscription header. Do not try
case variants or alternate headers without newer authoritative per-feed
evidence, and do not send the optional platform bearer token without an
official contract; retain config compatibility while deprecating unsupported
transmission.

Use distinct Go types/fields for PTV run references, static trip IDs, feed entity
IDs, public vehicle labels, and internal vehicle IDs. Remove direct `run_ref ==
trip_id/entity_id` matching. A cross-source join requires static service date,
time, route, and trip context.

Classify observation freshness from entity timestamp with feed timestamp/fetch
time retained as evidence. `current` requires a valid timestamp no more than 90
seconds old; missing, future-skewed, or older values become documented unknown
or stale states. Internal vehicle IDs remain usable only as opaque matching data
inside the package and are omitted from default command DTOs.

Normalize a feed once and build combined lookup indexes rather than scanning and
allocating it once per candidate identifier.

## Phase 5 — runtime capabilities, geocoder, freshness, and CLI integration

### Capability configuration

Split base configuration (paths, GTFS URL, API endpoints, geocoder) from PTV
and Open Data credentials. Loading base config never requires credentials.
Create signed clients only in commands/overlays that need them.

Local planning and static GTFS commands work without PTV credentials. Optional
disruption/live overlays return a typed unavailable result and a stderr warning
while preserving successful JSON. Dotenv stays explicit through `--env-file`.

### Context, time, and JSON

Create the root command context with `signal.NotifyContext` and consistently use
`cmd.Context()` through HTTP, SQL, geocoding, ingest, and routing. Replace
`time.Local` in user output with one shared `Australia/Melbourne` location and
embed timezone data for cross-compiled/minimal systems.

Command-specific output DTOs become the JSON boundary. Change help text from
“raw” to “normalized”; map/clean values before encoding. Preserve current-major
top-level shapes where truthful. Document nullable fields, enum values, time
format, identifier namespaces, warnings, and attribution. Golden tests ensure
stderr never corrupts stdout and internal vehicle IDs are absent.

### Geocoder

- Add a validated runtime provider/base URL setting.
- Prefer local stop/coordinate resolution before geocoding and disclose an
  address-like public lookup before sending it.
- Include provider attribution in human and structured JSON results.
- Make waits context-aware and goroutine-safe. Coordinate the public one-request
  per-second limit across local processes with a private lock/timestamp file.
- Version and key cache entries by provider, endpoint, normalized query,
  country/viewbox, and schema; enforce a TTL and atomic private replacement.
- Document that distributed installs require an operator-controlled provider.

### Feed freshness

Persist verified source provenance, actual bytes, publication/ingest times, and
service coverage atomically inside the immutable generation. Persist mutable
attempt/success/backoff state separately in a source-keyed freshness database;
offline evaluation never creates or writes that database. Expose `current`,
`changed`, `stale`, or `unknown`; failed or validator-less checks are never
current. Back off automatic failures while preserving forced checks.

## Compatibility and rollout

- Schema changes require a static-feed re-ingest. The CLI detects a legacy
  database and explains the one-time update; it never silently routes with
  incomplete new semantics.
- Existing credentials and keyring entries remain readable. Unsupported API-ID
  bearer transmission is deprecated without deleting the stored value.
- Current-major JSON retains truthful existing fields and adds corrected fields.
  Misnamed cross-namespace fields are not populated with false data; document
  the correction in release notes.
- No automatic `.env` read, credential migration, network deployment, tag, or
  release is part of implementation.

## Risks and mitigations

| Risk | Mitigation |
| --- | --- |
| GTFS transfer precedence is implemented partially | Golden CSV fixtures for every type/specificity combination before code lands |
| New schema makes existing databases unusable without explanation | Explicit legacy detection and one-command re-ingest guidance |
| Staging replacement differs on Windows | Platform-specific replace tests and closed handles before publication |
| Walking propagation increases scan cost | Compact adjacency, priority-queue benchmarks, bounded components |
| Connection materialization increases ingest size/time | Measure generation size/time; retain prior DB until success |
| API docs differ by feed | Catalog-level auth metadata and captured per-feed official fixtures |
| JSON correction breaks scripts | Additive DTOs where possible, explicit release note for false/misnamed fields |
| Parallel agents collide | Exclusive lanes plus primary-owned serial choke points |

## Verification map

Implementation is complete only after all of these pass:

1. Focused table/property tests named in audit Specs 001–004.
2. `mise run check` and `go test -race -count=1 ./...` under pinned Go.
3. Linux, macOS, and Windows path/migration/build checks represented locally or
   in CI; `CGO_ENABLED=0` build remains supported.
4. Command/config matrix with no credentials, PTV only, Open Data only, and both.
5. Golden human/JSON tests under non-Melbourne host time zones.
6. Fault-injected staging publication and concurrent reader/updater tests.
7. Request-count and official-shape local HTTP/protobuf fixtures.
8. Five raw production-snapshot samples for forward and arrive-by, plus focused
   loader/router benchmark evidence and peak RSS, meeting the PRODUCT budgets.
9. Owner-authorized live PTV station and GTFS-R probes using `--env-file` without
   reading or printing the file, credentials, or signed URLs.

Record any deliberately unavailable platform/live check here before completion;
do not silently weaken a PRODUCT success criterion.
