# Spec 001 — GTFS routing correctness

**Status:** implemented and validated

**Addresses:** F01–F05, F15

## Objective

Make every journey obey the imported GTFS service-day, boarding, alighting,
station-pathway, and transfer contracts. Explicit GTFS rules must always win
over inferred walking links.

## Required design

### Preserve the source contract

- Store stop hierarchy/location type, `stop_access`, and the fields needed by
  `pathways.txt`.
- Store `pickup_type` and `drop_off_type` on each stop-time boundary.
- Store all nullable route/trip specificity fields and `transfer_type` from
  `transfers.txt`; do not require stop IDs for linked-trip forms that permit
  them to be absent.
- Namespace trip and block identity by feed. Represent an active trip as a
  service instance `(feed_mode, service_date, trip_id)`, never as raw `trip_id`.

### Build a valid transfer graph

- Implement GTFS transfer types 0–5 and the mandated specificity precedence.
  Type 3 removes an edge. Type 4 requires an in-seat continuation; type 5
  prohibits one even when shared block metadata would otherwise imply it.
- Expand station-scoped transfer rules to the applicable child stops. Enforce
  same-stop minimum change times.
- Treat directed pathways as exhaustive inside station complexes where the feed
  supplies them. Preserve traversal time and direction; do not synthesize a
  straight-line shortcut across such a component.
- Normalize Transport Victoria's observed non-conforming zero sentinel for
  optional pathway traversal/stair values to absent, never to instant travel;
  use the conservative length/mode fallback when traversal time is unknown.
- Generate proximity walks only between eligible locations outside an explicit
  station graph. Compute the spatial-neighbor span from radius and latitude so
  every pair within 250 m reaches the Haversine check.
- Run a queue/Dijkstra relaxation after a stop label improves, or precompute and
  validate a bounded shortest-path closure. One-hop relaxation is not allowed
  on a non-closed graph.

### Respect time and passenger state

- Derive a service-day epoch from local noon minus 12 elapsed hours in
  `Australia/Melbourne`, then add GTFS seconds, including values above 24:00.
- Load all service days that intersect a documented planning horizon in the
  search direction. The initial contract is at least 36 hours from the query.
- Distinguish staying onboard from boarding and alighting. Apply pickup/drop-off
  policy only to the corresponding action; define visible handling for values 2
  and 3 rather than silently treating them as regular service.
- Permit a continuation without a transfer penalty only for a resolved GTFS
  transfer type 4 between active trip instances, retaining distinct terminal
  `from_stop` and origin `to_stop` endpoints. Equal `block_id` values never
  prove continuation, and a matching type 5 prohibits it.
- Process zero-duration cross-trip connections at the same timestamp to a
  bounded fixpoint so source row ordering cannot hide a reachable journey.
- Give coordinate/place origins and destinations weighted access/egress edges
  based on walking duration. Do not seed every nearby stop at the same instant.
- Return an explicit same-location/zero-leg journey with query-time timestamps;
  never serialize Go zero time.

## Acceptance criteria

- Table-driven fixtures cover transfer types 0–5, specificity precedence,
  parent-station expansion, forbidden and same-stop transfers, one-way
  pathways, linked-trip override, and cross-feed block-ID collisions.
- Router fixtures cover two-hop walking in both directions, no shortcut across
  a barrier, weighted access/egress, pickup/drop-off prohibition, and staying
  onboard through a non-service stop.
- Melbourne DST start/end fixtures cover times before/after the transition and
  `25:xx:xx` values. Midnight fixtures cover previous and next service days.
- A consecutive-service-day fixture reuses the same raw `trip_id` in current-day
  and previous-day-overflow connections and proves onboard reachability cannot
  cross between those service instances.
- A property test checks that no returned transit leg boards/alights contrary
  to its source stop-time policy and no returned walk uses a prohibited edge.
- Earliest-arrival and arrive-by return equivalent feasible journey sets on a
  reversible fixture graph.

## Non-goals

Realtime delay propagation and fare optimization are separate work. This spec
establishes a correct static timetable and transfer graph for those features.
