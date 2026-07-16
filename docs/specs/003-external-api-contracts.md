# Spec 003 — External API and JSON contracts

**Status:** implemented and live-validated

**Addresses:** F08–F10, F12, F16, F19–F21

## Objective

Model each PTV and GTFS Realtime contract exactly, keep identifier namespaces
distinct, report live-data freshness honestly, and give `--json` a stable
documented shape.

## Required design

### PTV Timetable API

- Replace shared partial DTOs with endpoint-specific models generated from or
  fixture-checked against the current v3 Swagger document.
- Model Stop Details with nested `stop_location.gps`, amenities,
  accessibility, staffing, and disruptions. Use those coordinates directly,
  render the promised facilities, and remove compensating Search calls.
- Use `/v3/runs/{run_ref}` for a broad run lookup and one multi-valued
  `route_types` request for a multi-mode route list.
- Add bounded response reads and typed HTTP errors. Retry only documented
  idempotent transient failures with a shared command deadline and bounded
  `Retry-After`; never turn authentication/network failures into “not found.”

### GTFS Realtime

- Define distinct types and JSON fields for PTV `run_ref`, static-GTFS
  `trip_id`, GTFS-R `FeedEntity.id`, and vehicle label/internal ID. Remove direct
  equality between these namespaces. A join is permitted only through a proven
  static-GTFS service-instance mapping with date/time/route context.
- Retain feed timestamp, incrementality, fetch time, entity timestamp, trip
  start date/time, and route context during normalization. Reject unsupported
  incrementality or expose it explicitly.
- Classify a vehicle observation as current only when its timestamp is present
  and at most 90 seconds old. Otherwise expose `last_seen`/`stale` with
  `observed_at` and `age_seconds`; an undated entity can never override fresher
  PTV state.
- Use the public vehicle label for user identity. Keep the upstream-declared
  internal ID private and omit it from default human and JSON output.
- Put exactly one documented `KeyID` subscription header on each feed request.
  Do not guess alternate spellings/headers or send a bearer token without a
  current authoritative per-feed contract requiring it. Authentication,
  timeout, rate-limit, upstream, and decode failures remain distinct.
- Normalize a fetched feed once and build all required lookup indexes from that
  value. Enforce a documented protobuf body limit.

### CLI JSON

- Change `--json` help/documentation from “raw” to “normalized.” Introduce
  endpoint-specific output DTOs and golden schemas; never serialize upstream
  transport structs directly.
- Preserve existing top-level shapes through the current major release where
  possible. Additive corrections are allowed; removals/renames require a
  documented deprecation and next-major plan.
- Normalize presentation text while mapping into an output DTO, not by
  recursively mutating arbitrary encoded values.
- Document time format, nullable/omitted fields, identifier namespace, enum
  values, warning placement, and stdout/stderr behavior for every command.

## Acceptance criteria

- Local HTTP fixtures use captured official response shapes for Stop Details,
  Runs, Routes, errors, and every GTFS-R mode. A contract test fails when a
  required official field is silently dropped.
- A station lookup makes one Stop Details request and exposes coordinates and
  facilities in human and JSON output.
- A broad run lookup makes one PTV request; a four-mode route lookup makes one.
  A four-feed GTFS-R scan makes exactly four requests with valid credentials,
  each carrying one `KeyID` header and no alternate or bearer credential.
- Collision tests prove identical strings in `run_ref`, trip, entity, and
  vehicle namespaces cannot match accidentally or be serialized under the
  wrong field name.
- Fresh, stale, future-skewed, missing-timestamp, stale-feed/fresh-entity, and
  fresh-feed/stale-entity fixtures produce documented confidence states.
- Golden JSON tests prove internal vehicle IDs are absent and stderr warnings do
  not corrupt stdout.
- Fare fixtures use only `FareEstimateResultStatus` and `FareEstimateResult`;
  they never invent the generic status object absent from that endpoint.
- A route-filtered departure query validates cross-mode stop membership before
  fetching departures, while a served route with no current departures remains
  a valid empty result. Zero route-stop sequences remain `0` in JSON and render
  as unsequenced in human output.
